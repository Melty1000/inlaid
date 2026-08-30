package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Melty1000/inlaid/internal/applayout"
	"github.com/Melty1000/inlaid/internal/capture"
	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/celllive"
	"github.com/Melty1000/inlaid/internal/cellrender"
	"github.com/Melty1000/inlaid/internal/celltape"
	ffmpegexe "github.com/Melty1000/inlaid/internal/ffmpeg"
	"github.com/Melty1000/inlaid/internal/recording"
	"github.com/Melty1000/inlaid/internal/supportreport"
	"github.com/Melty1000/inlaid/internal/taperecovery"
)

type cameraSession interface {
	close() error
	errors() <-chan error
	results() <-chan celllive.Result
	update(celllive.ViewConfig)
}

type cellLiveCameraSession struct {
	session *celllive.Session
}

func (s *cellLiveCameraSession) close() error                    { return s.session.Close() }
func (s *cellLiveCameraSession) errors() <-chan error            { return s.session.Errors }
func (s *cellLiveCameraSession) results() <-chan celllive.Result { return s.session.Results }
func (s *cellLiveCameraSession) update(view celllive.ViewConfig) { s.session.Update(view) }

// Runtime owns one camera and every non-visual operation behind the dashboard.
// It is deliberately independent from Bubble Tea: all public actions return
// immediately and report completion through Events or Previews.
type Runtime struct {
	ctx    context.Context
	cancel context.CancelFunc
	layout applayout.Layout

	previews             chan PreviewUpdate
	events               chan RuntimeEvent
	startOnce, closeOnce sync.Once
	closed               atomic.Bool
	actionMu             sync.Mutex
	wg                   sync.WaitGroup
	closeErr             error

	settingsMu sync.RWMutex
	settings   Settings
	viewMu     sync.RWMutex
	view       ViewOptions
	looksMu    sync.RWMutex
	looks      lookCatalog

	ffmpegMu          sync.RWMutex
	ffmpeg            string
	findFFmpeg        func(context.Context, string) (string, error)
	openFolder        func(context.Context, string) error
	enumerateCameras  func(context.Context) ([]capture.Device, error)
	startCamera       func(context.Context, capture.Config, celllive.ViewConfig) (*celllive.Session, error)
	devicesMu         sync.RWMutex
	deviceIDs         map[string]string
	cameraGeneration  atomic.Uint64
	cameraSelectMu    sync.Mutex
	cameraOpMu        sync.Mutex
	cameraLifetimeMu  sync.Mutex
	cameraLifetimeGen uint64
	cameraCancel      context.CancelFunc
	sessionMu         sync.RWMutex
	session           cameraSession
	cameraShutdownErr error

	recordMu                                      sync.Mutex
	tape                                          *celltape.Recorder
	tapeReservation                               taperecovery.Reservation
	tapeStarted                                   time.Time
	recordDuration                                time.Duration
	recordResult                                  supportreport.Code
	tapeFinal                                     string
	tapeConfig                                    []byte
	recordOptions                                 RecordOptions
	recordStarting, recordClosing, stopAfterStart bool
	recoveryRunning                               bool
	recordFormat, recordOutput                    string
	snapshotSaving                                bool
	lastSaved                                     string
	previewMu                                     sync.RWMutex
	latestPreview                                 PreviewUpdate
	latestFrame                                   *cellframe.CellFrame
	hasPreview                                    bool
	previewQueueMu                                sync.Mutex

	saveMu         sync.Mutex
	savePending    Settings
	savePendingSet bool
	saveClosing    bool
	saveWake       chan struct{}
	saveStop       chan struct{}
	saveDone       chan struct{}
	saveErr        error

	deliveryMu       sync.Mutex
	deliveryStarted  time.Time
	deliveryCount    uint64
	deliveryFPS      float64
	deliveryDropped  uint64
	deliveryAccepted uint64

	support         *supportreport.Collector
	supportMu       sync.Mutex
	supportSource   celllive.SourceInfo
	supportCamera   string
	supportSampleAt time.Time
}

type snapshotRequest struct {
	options RecordOptions
	path    string
	frame   *cellframe.CellFrame
}

type tapeRecordingConfig struct {
	Version int           `json:"version"`
	View    ViewOptions   `json:"view"`
	Output  RecordOptions `json:"output"`
}

var canonicalTapeLimits = celltape.Limits{
	MaxColumns: 600, MaxRows: 200, MaxCells: 40_000,
	MaxConfigBytes: 64 << 10, MaxChunkBytes: 1 << 20,
}

const (
	recordingStallBudgetSeconds  = 4
	maxRecordingQueueFrames      = 240
	maxAutomaticRecoveryDuration = 7 * 24 * time.Hour
)

var errCameraRestartRequired = errors.New("camera restart required")

// recordingQueueCapacity gives the lossless tape writer a bounded cushion for
// ordinary filesystem, antivirus, scheduler, and compression stalls. Its LIFO
// free list allocates cell storage only as actual in-flight depth grows, then
// reuses the hottest backing. At normal 30-60 FPS the queue covers four seconds;
// its memory can never grow beyond the 240-frame ceiling.
func recordingQueueCapacity(targetFPS int) int {
	if targetFPS <= 0 {
		targetFPS = 30
	}
	targetFPS = min(targetFPS, 60)
	return min(max(targetFPS*recordingStallBudgetSeconds, 8), maxRecordingQueueFrames)
}

func recordingRecoveryEngine(directory string) (*taperecovery.Engine, error) {
	return taperecovery.New(directory, taperecovery.Options{
		MaxConfigBytes: int(canonicalTapeLimits.MaxConfigBytes),
		TapeLimits:     canonicalTapeLimits,
	})
}

func NewRuntime(cfg Settings, settingsPath, root string) *Runtime {
	return NewRuntimeWithBuild(cfg, settingsPath, root, supportreport.BuildFacts{Version: "dev"})
}

func NewRuntimeWithBuild(cfg Settings, settingsPath, root string, build supportreport.BuildFacts) *Runtime {
	layout, err := applayout.Local(root, applayout.ExplicitTest)
	if err != nil {
		panic(fmt.Sprintf("dashboard test layout: %v", err))
	}
	if strings.TrimSpace(settingsPath) != "" {
		layout.SettingsFile, err = filepath.Abs(settingsPath)
		if err != nil {
			panic(fmt.Sprintf("dashboard test settings path: %v", err))
		}
	}
	runtime, err := NewRuntimeWithLayout(cfg, layout, build)
	if err != nil {
		panic(fmt.Sprintf("dashboard test runtime: %v", err))
	}
	return runtime
}

// NewRuntimeWithLayout consumes locations already resolved at the application
// boundary. The dashboard never decides whether a run is installed, portable,
// source, or a test fixture.
func NewRuntimeWithLayout(cfg Settings, layout applayout.Layout, build supportreport.BuildFacts) (*Runtime, error) {
	ctx, cancel := context.WithCancel(context.Background())
	if err := layout.Validate(); err != nil {
		cancel()
		return nil, fmt.Errorf("dashboard layout: %w", err)
	}
	localFFmpegRoot := layoutFFmpegRoot(layout)
	runtime := &Runtime{
		ctx: ctx, cancel: cancel, layout: layout, settings: cfg,
		// Keep exactly one composed frame waiting for Bubble Tea. A zero-capacity
		// channel discarded a camera frame whenever Bubble Tea was between its
		// one-shot receive commands, turning a healthy 29.8 FPS source into a
		// roughly 27 FPS preview. One latest-wins slot removes that scheduling race
		// without allowing latency to accumulate.
		previews: make(chan PreviewUpdate, 1), events: make(chan RuntimeEvent, 64),
		deviceIDs: make(map[string]string),
		looks:     builtInLookCatalog(),
		findFFmpeg: func(ctx context.Context, explicit string) (string, error) {
			return ffmpegexe.FindContext(ctx, explicit, localFFmpegRoot)
		},
		openFolder:       openFolder,
		enumerateCameras: capture.Enumerate,
		startCamera:      celllive.StartCamera,
		support:          supportreport.New(build),
		saveWake:         make(chan struct{}, 1), saveStop: make(chan struct{}), saveDone: make(chan struct{}),
	}
	runtime.wg.Add(1)
	go func() {
		defer runtime.wg.Done()
		runtime.runSettingsSaver()
	}()
	return runtime, nil
}

func layoutFFmpegRoot(layout applayout.Layout) string {
	switch layout.Mode {
	case applayout.Portable, applayout.Source, applayout.ExplicitTest:
		return layout.ProgramRoot
	default:
		return ""
	}
}

func (r *Runtime) Start(view ViewOptions) {
	if !r.beginAction() {
		return
	}
	defer r.wg.Done()
	r.viewMu.Lock()
	r.view = view
	r.viewMu.Unlock()
	r.startOnce.Do(func() {
		r.wg.Add(3)
		go func() { defer r.wg.Done(); r.discover() }()
		go func() { defer r.wg.Done(); r.discoverRecovery() }()
		go func() { defer r.wg.Done(); r.discoverLooks() }()
	})
}

func (r *Runtime) Previews() <-chan PreviewUpdate { return r.previews }
func (r *Runtime) Events() <-chan RuntimeEvent    { return r.events }

func (r *Runtime) discover() {
	r.publishEvent(RuntimeEvent{Kind: RuntimeFindingCameras})
	// Camera permission belongs to the user, not an arbitrary startup deadline.
	// In particular, the first macOS prompt may remain open while the user reads
	// it. Runtime cancellation still ends discovery when Inlaid closes.
	nativeDevices, listErr := r.enumerateCameras(r.ctx)
	devices := make([]string, 0, len(nativeDevices))
	ids := make(map[string]string, len(nativeDevices))
	for _, device := range nativeDevices {
		name := safeTerminalText(device.Name)
		if name == "" {
			name = "Camera"
		}
		base := name
		for suffix := 2; ids[name] != ""; suffix++ {
			name = fmt.Sprintf("%s (%d)", base, suffix)
		}
		devices = append(devices, name)
		ids[name] = device.ID
	}
	r.devicesMu.Lock()
	r.deviceIDs = ids
	r.devicesMu.Unlock()
	settings := r.currentSettings()
	selected := ""
	if strings.EqualFold(settings.Device, "DEMO") {
		selected = "DEMO"
	} else if settings.DeviceID != "" {
		for name, id := range ids {
			if id == settings.DeviceID {
				selected = name
				break
			}
		}
	} else if containsFold(devices, settings.Device) {
		for _, name := range devices {
			if strings.EqualFold(name, settings.Device) {
				selected = name
				break
			}
		}
	}
	if selected == "" && len(devices) > 0 {
		selected = devices[0]
	}
	if selected == "" && settings.DeviceID != "" && strings.TrimSpace(settings.Device) != "" && listErr != nil {
		// Enumeration can fail while a persisted stable ID remains usable. Keep
		// the friendly label, but selection still opens only the exact ID.
		selected = safeTerminalText(settings.Device)
		if selected != "" {
			devices = append(devices, selected)
			r.devicesMu.Lock()
			r.deviceIDs[selected] = settings.DeviceID
			r.devicesMu.Unlock()
		}
	}
	if selected == "" {
		selected = "DEMO"
	}
	r.publishEvent(RuntimeEvent{Kind: RuntimeDevicesFound, Devices: devices, Device: selected, Err: listErr})
	r.SelectCamera(selected)
}

func (r *Runtime) discoverRecovery() {
	ffmpegPath, ffmpegErr := r.findFFmpeg(r.ctx, "")
	if ffmpegErr == nil {
		r.ffmpegMu.Lock()
		r.ffmpeg = ffmpegPath
		r.ffmpegMu.Unlock()
	}
	r.recoverRecordings(ffmpegPath, ffmpegErr)
}

func (r *Runtime) discoverLooks() {
	catalog, err := loadLookCatalog(r.layout.FiltersDir)
	if r.closed.Load() {
		return
	}
	r.looksMu.Lock()
	r.looks = catalog
	r.looksMu.Unlock()
	r.publishEvent(RuntimeEvent{Kind: RuntimeLooksFound, Looks: append([]string(nil), catalog.names...), Err: err})
	// A saved custom look may have been unavailable when camera discovery began.
	// Reconcile the current view after the immutable catalog is installed.
	r.UpdateView(r.currentView())
}

func (r *Runtime) SelectCamera(device string) {
	if !r.beginAction() {
		return
	}
	defer r.wg.Done()
	r.cameraSelectMu.Lock()
	defer r.cameraSelectMu.Unlock()
	if r.closed.Load() {
		return
	}
	device = strings.TrimSpace(device)
	if device == "" {
		device = "DEMO"
	}
	r.devicesMu.RLock()
	deviceID := r.deviceIDs[device]
	r.devicesMu.RUnlock()
	r.settingsMu.Lock()
	if strings.EqualFold(device, "DEMO") {
		r.settings.Device, r.settings.DeviceID = "DEMO", ""
	} else {
		r.settings.Device, r.settings.DeviceID = device, deviceID
	}
	r.settingsMu.Unlock()
	r.supportMu.Lock()
	r.supportCamera = device
	r.supportSource = celllive.SourceInfo{}
	r.supportMu.Unlock()
	generation := r.advanceCameraGeneration()
	cameraContext, current := r.replaceCameraLifetime(generation)
	if !current {
		return
	}
	r.publishEvent(RuntimeEvent{Kind: RuntimeConnecting, Device: device})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if !r.switchCameraContext(cameraContext, generation, device) {
			r.finishCameraLifetime(generation)
		}
	}()
}

// advanceCameraGeneration invalidates every representation of the previous
// camera under the same lock used to publish a preview. A sender either lands
// before this point and is drained, or observes the new generation and is
// rejected; it cannot enqueue stale work between those two operations.
func (r *Runtime) advanceCameraGeneration() uint64 {
	r.previewQueueMu.Lock()
	generation := r.cameraGeneration.Add(1)
	var pending PreviewUpdate
	select {
	case pending = <-r.previews:
	default:
	}
	r.previewMu.Lock()
	latest := r.latestFrame
	r.latestFrame = nil
	r.latestPreview = PreviewUpdate{}
	r.hasPreview = false
	r.previewMu.Unlock()
	r.previewQueueMu.Unlock()

	pending.acknowledgeRendered(false)
	if latest != nil {
		latest.Release()
	}
	return generation
}

func (r *Runtime) replaceCameraLifetime(generation uint64) (context.Context, bool) {
	ctx, cancel := context.WithCancel(r.ctx)
	r.cameraLifetimeMu.Lock()
	if r.closed.Load() || generation != r.cameraGeneration.Load() {
		r.cameraLifetimeMu.Unlock()
		cancel()
		return ctx, false
	}
	previous := r.cameraCancel
	r.cameraLifetimeGen = generation
	r.cameraCancel = cancel
	r.cameraLifetimeMu.Unlock()
	if previous != nil {
		previous()
	}
	return ctx, true
}

func (r *Runtime) finishCameraLifetime(generation uint64) {
	r.cameraLifetimeMu.Lock()
	if r.cameraLifetimeGen != generation {
		r.cameraLifetimeMu.Unlock()
		return
	}
	cancel := r.cameraCancel
	r.cameraLifetimeGen = 0
	r.cameraCancel = nil
	r.cameraLifetimeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runtime) cancelCameraLifetime() {
	r.cameraLifetimeMu.Lock()
	cancel := r.cameraCancel
	r.cameraLifetimeGen = 0
	r.cameraCancel = nil
	r.cameraLifetimeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runtime) closeCameraSession(session cameraSession) error {
	if session == nil {
		return nil
	}
	err := session.close()
	if err == nil {
		return nil
	}
	return r.rememberCameraShutdownFailure(err)
}

func (r *Runtime) rememberCameraShutdownFailure(err error) error {
	if !errors.Is(err, capture.ErrShutdownUncertain) {
		return err
	}
	r.sessionMu.Lock()
	if r.cameraShutdownErr == nil {
		r.cameraShutdownErr = err
	}
	err = r.cameraShutdownErr
	r.sessionMu.Unlock()
	return err
}

func (r *Runtime) cameraShutdownFailure() error {
	r.sessionMu.RLock()
	defer r.sessionMu.RUnlock()
	return r.cameraShutdownErr
}

func cameraRestartBlockedError(err error) error {
	return errors.Join(
		errCameraRestartRequired,
		fmt.Errorf("cannot open another camera because the previous capture did not shut down cleanly; restart Inlaid first: %w", err),
	)
}

func (r *Runtime) switchCameraContext(cameraContext context.Context, generation uint64, device string) bool {
	r.cameraOpMu.Lock()
	defer r.cameraOpMu.Unlock()
	if r.closed.Load() || cameraContext.Err() != nil || generation != r.cameraGeneration.Load() {
		return false
	}

	r.sessionMu.Lock()
	old := r.session
	r.session = nil
	shutdownErr := r.cameraShutdownErr
	r.sessionMu.Unlock()
	if old != nil {
		if closeErr := r.closeCameraSession(old); errors.Is(closeErr, capture.ErrShutdownUncertain) {
			shutdownErr = closeErr
		}
	}
	r.previewMu.Lock()
	staleFrame := r.latestFrame
	r.latestFrame = nil
	r.hasPreview = false
	r.latestPreview = PreviewUpdate{}
	r.previewMu.Unlock()
	if staleFrame != nil {
		staleFrame.Release()
	}
	if r.closed.Load() || cameraContext.Err() != nil || generation != r.cameraGeneration.Load() {
		return false
	}
	if shutdownErr == nil {
		shutdownErr = r.cameraShutdownFailure()
	}
	if shutdownErr != nil {
		r.publishEvent(RuntimeEvent{Kind: RuntimeCameraError, Device: device, Err: cameraRestartBlockedError(shutdownErr)})
		return false
	}

	view := r.currentView()
	liveView := r.toCellLiveView(view)
	settings := r.currentSettings()
	var opened *celllive.Session
	var err error
	if strings.EqualFold(device, "DEMO") {
		opened, err = celllive.StartSynthetic(cameraContext, settings.CaptureWidth, settings.CaptureHeight, settings.CaptureFPS, liveView)
	} else {
		r.devicesMu.RLock()
		deviceID := r.deviceIDs[device]
		r.devicesMu.RUnlock()
		if deviceID == "" {
			err = fmt.Errorf("camera %q is no longer available", safeTerminalText(device))
		} else {
			cfg := capture.DefaultConfig()
			cfg.DeviceID = deviceID
			cfg.Width, cfg.Height, cfg.FPS = settings.CaptureWidth, settings.CaptureHeight, settings.CaptureFPS
			// Quarter-size capture still supplies the full 2x2 sample grid for
			// ordinary terminals while sharply reducing native pixel traffic.
			cfg.Downsample = 4
			opened, err = r.startCamera(cameraContext, cfg, liveView)
		}
	}
	if err != nil {
		if errors.Is(err, capture.ErrShutdownUncertain) {
			err = cameraRestartBlockedError(r.rememberCameraShutdownFailure(err))
		}
		if r.closed.Load() || cameraContext.Err() != nil || generation != r.cameraGeneration.Load() {
			return false
		}
		r.publishEvent(RuntimeEvent{Kind: RuntimeCameraError, Device: device, Err: err})
		return false
	}
	session := &cellLiveCameraSession{session: opened}
	if r.closed.Load() || cameraContext.Err() != nil || generation != r.cameraGeneration.Load() {
		r.closeCameraSession(session)
		return false
	}
	// Install and reconcile under one session lock. A WindowSizeMsg can update
	// the requested view while the native camera is still opening. Without this
	// second read, the newly opened session can keep emitting the stale version
	// forever while the model correctly rejects every frame.
	r.installSession(session)
	sourceInfo := opened.SourceInfo()
	r.supportMu.Lock()
	r.supportCamera, r.supportSource = device, sourceInfo
	r.supportMu.Unlock()
	r.publishEvent(RuntimeEvent{
		Kind: RuntimeCameraLive, Device: device,
		Width: sourceInfo.Width, Height: sourceInfo.Height, FPS: sourceInfo.FPS,
	})
	r.wg.Add(1)
	go func() { defer r.wg.Done(); r.forwardSession(generation, device, session) }()
	return true
}

func (r *Runtime) installSession(session cameraSession) {
	r.sessionMu.Lock()
	r.session = session
	r.sessionMu.Unlock()
	if session != nil {
		// Publish the pointer before reading the current view. This ordering
		// closes both sides of the camera-open race without nesting sessionMu
		// and viewMu: an UpdateView either sees no session and this read catches
		// its value, or it sees the session and sends its own newer update.
		session.update(r.toCellLiveView(r.currentView()))
	}
}

func (r *Runtime) forwardSession(generation uint64, device string, session cameraSession) {
	terminalReported := false
	defer func() {
		// A session that ends owns native buffers and callbacks until Close has
		// drained them. Detach only if this is still the installed generation;
		// a newer camera must never be cleared by an older forwarder.
		closeErr := r.closeCameraSession(session)
		r.sessionMu.Lock()
		wasCurrent := r.session == session
		if wasCurrent {
			r.session = nil
		}
		r.sessionMu.Unlock()
		if wasCurrent && !terminalReported && generation == r.cameraGeneration.Load() && !r.closed.Load() {
			terminalErr := errors.New("camera stream ended unexpectedly")
			if errors.Is(closeErr, capture.ErrShutdownUncertain) {
				terminalErr = cameraRestartBlockedError(closeErr)
			} else if closeErr != nil {
				terminalErr = errors.Join(terminalErr, closeErr)
			}
			r.publishEvent(RuntimeEvent{
				Kind: RuntimeCameraError, Device: device,
				Err: terminalErr,
			})
		}
		r.finishCameraLifetime(generation)
	}()
	results, errorsCh := session.results(), session.errors()
	for results != nil || errorsCh != nil {
		select {
		case <-r.ctx.Done():
			return
		case result, ok := <-results:
			if !ok {
				results = nil
				continue
			}
			if generation != r.cameraGeneration.Load() {
				if result.Frame != nil {
					result.Frame.Release()
				}
				continue
			}
			update := PreviewUpdate{
				Version: result.Version, ANSI: result.ANSI, Columns: result.Columns, Rows: result.Rows,
				Sequence: result.Sequence, SourceFPS: result.SourceFPS, ShownFPS: result.ShownFPS,
				Dropped:          result.Dropped,
				cameraGeneration: generation,
			}
			r.observeSupportSample(result)
			var acknowledgeOnce sync.Once
			acceptedUpdate := update
			canonical := result.Frame
			prepared, preparedTape, tapeStarted, preparedOK := r.prepareCanonicalFrame(canonical)
			update.acknowledge = func(accepted bool) {
				acknowledgeOnce.Do(func() {
					if !r.beginAction() {
						r.observePreviewOutcome(false)
						if preparedOK {
							prepared.Abort()
						}
						if canonical != nil {
							canonical.Release()
						}
						return
					}
					defer r.wg.Done()
					accepted = accepted && !r.closed.Load() && generation == r.cameraGeneration.Load() && r.acceptCanonicalFrame(acceptedUpdate, canonical)
					r.observePreviewOutcome(accepted)
					if !accepted {
						if preparedOK {
							prepared.Abort()
						}
						if canonical != nil {
							canonical.Release()
						}
						return
					}
					if !preparedOK {
						return
					}
					r.commitCanonicalFrame(prepared, preparedTape, tapeStarted)
				})
			}
			if !r.publishPreview(update) {
				update.acknowledgeRendered(false)
			}
		case err, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
				continue
			}
			if err != nil && generation == r.cameraGeneration.Load() && !r.closed.Load() {
				if capture.IsTemporary(err) {
					continue
				}
				terminalReported = true
				r.publishEvent(RuntimeEvent{Kind: RuntimeCameraError, Device: device, Err: err})
				return
			}
		}
	}
}

// prepareCanonicalFrame reserves the recorder's bounded queue slot before the
// terminal handoff. The matching acknowledgement either commits that exact
// state after Bubble Tea composes it or aborts the reservation if it is stale
// or dropped. Disk and compression remain entirely off the UI path.
func (r *Runtime) prepareCanonicalFrame(frame *cellframe.CellFrame) (celltape.PreparedCellFrame, *celltape.Recorder, time.Time, bool) {
	if frame == nil || !frame.Valid() || r.closed.Load() {
		return celltape.PreparedCellFrame{}, nil, time.Time{}, false
	}
	r.recordMu.Lock()
	tape, started := r.tape, r.tapeStarted
	if tape == nil {
		r.recordMu.Unlock()
		return celltape.PreparedCellFrame{}, nil, time.Time{}, false
	}
	prepared, err := tape.PrepareCellFrame(frame, 1, r.tapeConfig, 0)
	broken := err != nil && !errors.Is(err, celltape.ErrClosed) && r.tape == tape
	reservation := taperecovery.Reservation{}
	if broken {
		reservation = r.tapeReservation
		r.tapeReservation = taperecovery.Reservation{}
		r.recordDuration = max(time.Since(r.tapeStarted), 0)
		r.recordResult = supportreport.CodeSaveFailed
		r.tapeStarted = time.Time{}
		r.tape, r.tapeConfig, r.recordClosing = nil, nil, true
	}
	r.recordMu.Unlock()
	if broken {
		r.finishBrokenTape(tape, reservation, err)
	}
	if err != nil {
		return celltape.PreparedCellFrame{}, nil, time.Time{}, false
	}
	return prepared, tape, started, true
}

// commitCanonicalFrame and StopRecording share recordMu. Exactly one ordering
// is therefore possible: either this accepted state commits before Stop takes
// its end timestamp, or Stop detaches the tape first and this reservation is
// aborted. A late UI acknowledgement can never extend the tape past its end.
func (r *Runtime) commitCanonicalFrame(prepared celltape.PreparedCellFrame, tape *celltape.Recorder, started time.Time) {
	if tape == nil {
		prepared.Abort()
		return
	}
	r.recordMu.Lock()
	if r.tape != tape {
		r.recordMu.Unlock()
		prepared.Abort()
		return
	}
	err := prepared.Commit(uint64(max(time.Since(started), 0)))
	broken := err != nil && !errors.Is(err, celltape.ErrClosed) && r.tape == tape
	reservation := taperecovery.Reservation{}
	if broken {
		reservation = r.tapeReservation
		r.tapeReservation = taperecovery.Reservation{}
		r.recordDuration = max(time.Since(r.tapeStarted), 0)
		r.recordResult = supportreport.CodeSaveFailed
		r.tapeStarted = time.Time{}
		r.tape, r.tapeConfig, r.recordClosing = nil, nil, true
	}
	r.recordMu.Unlock()
	if err != nil {
		prepared.Abort()
	}
	if broken {
		r.finishBrokenTape(tape, reservation, err)
	}
}

func (r *Runtime) UpdateView(view ViewOptions) {
	if !r.beginAction() {
		return
	}
	defer r.wg.Done()
	r.viewMu.Lock()
	r.view = view
	r.viewMu.Unlock()
	r.sessionMu.RLock()
	session := r.session
	r.sessionMu.RUnlock()
	if session != nil {
		session.update(r.toCellLiveView(view))
	}
}

func (r *Runtime) toCellLiveView(view ViewOptions) celllive.ViewConfig {
	mode := cellframe.ModeDetailed
	if strings.EqualFold(view.Symbol, "half") || strings.EqualFold(view.Detail, "smooth") {
		mode = cellframe.ModeSoft
	}
	r.looksMu.RLock()
	transform, transformID := r.looks.resolve(view.ColorLook, view.LookStrength)
	r.looksMu.RUnlock()
	return celllive.ViewConfig{
		Version: view.Version, MaxColumns: view.MaxColumns, MaxRows: view.MaxRows,
		Fill: view.Fill, Mirror: view.Mirror, Paused: view.Paused,
		Mode: mode, TargetFPS: view.TargetFPS, Transform: transform, TransformID: transformID,
	}
}

// acceptCanonicalFrame runs only for states handed to Bubble Tea. The
// CellFrame, not its ANSI projection, is the shared source of truth for
// preview, tape recording, snapshots, and eventual video export.
func (r *Runtime) acceptCanonicalFrame(update PreviewUpdate, frame *cellframe.CellFrame) bool {
	if frame == nil || !frame.Valid() {
		return false
	}
	r.previewQueueMu.Lock()
	defer r.previewQueueMu.Unlock()
	if r.closed.Load() || update.cameraGeneration != r.cameraGeneration.Load() {
		return false
	}
	r.previewMu.Lock()
	previous := r.latestFrame
	r.latestPreview, r.hasPreview = update, true
	r.latestFrame = frame // transfer the Result lease to Runtime
	r.previewMu.Unlock()
	if previous != nil {
		previous.Release()
	}
	return true
}

func (r *Runtime) currentCanonicalFrame() (*cellframe.CellFrame, bool) {
	r.previewMu.RLock()
	defer r.previewMu.RUnlock()
	if !r.hasPreview || r.latestFrame == nil || !r.latestFrame.Valid() {
		return nil, false
	}
	if err := r.latestFrame.Retain(); err != nil {
		return nil, false
	}
	return r.latestFrame, true
}

func (r *Runtime) StartRecording(options RecordOptions) {
	if !r.beginAction() {
		return
	}
	defer r.wg.Done()
	r.recordMu.Lock()
	if r.tape != nil || r.recordStarting || r.recordClosing || r.recoveryRunning {
		recovering := r.recoveryRunning
		r.recordMu.Unlock()
		if recovering {
			r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingError, Format: options.Format, Err: errors.New("a previous recording is still being recovered")})
		}
		return
	}
	r.recordStarting = true
	r.recordFormat = strings.ToLower(options.Format)
	r.recordOptions = options
	r.recordDuration = 0
	r.recordResult = supportreport.CodeUnknown
	r.recordMu.Unlock()
	r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingStarting, Format: options.Format})
	r.wg.Add(1)
	go func() { defer r.wg.Done(); r.startRecording(options) }()
}

func (r *Runtime) startRecording(options RecordOptions) {
	firstFrame, ok := r.currentCanonicalFrame()
	if !ok {
		r.recordStartFailed(options.Format, errors.New("the terminal preview is not ready yet"))
		return
	}
	defer firstFrame.Release()
	format := recording.Format(strings.ToLower(options.Format))
	output, err := r.nextOutput(r.layout.RecordingsDir, string(format))
	if err != nil {
		r.recordStartFailed(options.Format, err)
		return
	}
	r.ffmpegMu.RLock()
	ffmpeg := r.ffmpeg
	r.ffmpegMu.RUnlock()
	if ffmpeg == "" {
		ffmpeg, err = r.findFFmpeg(r.ctx, "")
	}
	if err != nil {
		r.recordStartFailed(options.Format, err)
		return
	}
	r.ffmpegMu.Lock()
	r.ffmpeg = ffmpeg // validated now; the actual encoder starts only after Stop.
	r.ffmpegMu.Unlock()
	recoveryDirectory := r.layout.RecoveryDir
	tapeFinal := filepath.Join(recoveryDirectory, strings.TrimSuffix(filepath.Base(output), filepath.Ext(output))+".celltape")
	r.viewMu.RLock()
	displayFPS := r.view.TargetFPS
	r.viewMu.RUnlock()
	tape, err := celltape.Create(context.Background(), tapeFinal, celltape.Config{
		Limits:        canonicalTapeLimits,
		QueueCapacity: recordingQueueCapacity(displayFPS), KeyframeInterval: 120,
		DurabilityWindow: time.Second, Compression: celltape.CompressionFast,
	})
	if err != nil {
		r.recordStartFailed(options.Format, err)
		return
	}
	engine, err := recordingRecoveryEngine(recoveryDirectory)
	if err != nil {
		err = errors.Join(err, tape.Close())
		r.recordStartFailed(options.Format, err)
		return
	}
	reservation, err := engine.Reserve(tape.StagingPath())
	if err != nil {
		err = errors.Join(err, tape.Close())
		r.recordStartFailed(options.Format, err)
		return
	}
	configBlob, err := json.Marshal(tapeRecordingConfig{Version: 1, View: r.currentView(), Output: options})
	if err == nil {
		err = tape.SubmitCellFrame(firstFrame, 1, configBlob, celltape.BoundaryDiscontinuity, 0)
	}
	if err != nil {
		err = errors.Join(err, tape.Close(), reservation.Release())
		r.recordStartFailed(options.Format, err)
		return
	}

	r.recordMu.Lock()
	if r.closed.Load() {
		r.recordStarting = false
		r.recordMu.Unlock()
		claim, publishErr := closeAndPublishReservedTape(tape, reservation, tapeFinal)
		_ = errors.Join(publishErr, claim.Release())
		return
	}
	r.tape, r.tapeReservation, r.tapeStarted, r.tapeFinal, r.tapeConfig = tape, reservation, time.Now(), tapeFinal, configBlob
	r.recordDuration, r.recordResult = 0, supportreport.CodeUnknown
	r.recordOptions, r.recordOutput, r.recordStarting = options, output, false
	stopNow := r.stopAfterStart
	r.stopAfterStart = false
	r.recordMu.Unlock()
	r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingStarted, Format: options.Format, Path: output})
	if stopNow {
		r.StopRecording()
	}
}

func (r *Runtime) recordStartFailed(format string, err error) {
	r.recordMu.Lock()
	r.recordStarting = false
	r.stopAfterStart = false
	r.recordDuration = 0
	r.recordResult = supportreport.CodeSaveFailed
	r.recordMu.Unlock()
	r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingError, Format: format, Err: err})
}

func (r *Runtime) StopRecording() {
	if !r.beginAction() {
		return
	}
	defer r.wg.Done()
	r.recordMu.Lock()
	if r.recordStarting {
		r.stopAfterStart = true
		r.recordMu.Unlock()
		r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingSaving, Format: r.recordFormat})
		return
	}
	if r.tape == nil || r.recordClosing {
		r.recordMu.Unlock()
		return
	}
	tape, reservation, output, format := r.tape, r.tapeReservation, r.recordOutput, r.recordFormat
	started, tapeFinal, options := r.tapeStarted, r.tapeFinal, r.recordOptions
	duration := max(time.Since(started), 0)
	endHostNanos := uint64(duration)
	r.recordDuration = duration
	r.tapeStarted = time.Time{}
	r.tape, r.tapeReservation, r.tapeConfig, r.recordClosing = nil, taperecovery.Reservation{}, nil, true
	r.recordMu.Unlock()
	r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingSaving, Format: format, Path: output})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.finishTape(tape, reservation, endHostNanos, tapeFinal, output, format, options)
	}()
}

func (r *Runtime) finishTape(tape *celltape.Recorder, reservation taperecovery.Reservation, endHostNanos uint64, tapeFinal, output, format string, options RecordOptions) {
	staging := tape.StagingPath()
	claim, err := closeAndPublishReservedTape(tape, reservation, tapeFinal)
	claimed := claim.Path != ""
	if err == nil {
		r.ffmpegMu.RLock()
		ffmpeg := r.ffmpeg
		r.ffmpegMu.RUnlock()
		crf, gifColors := 16, 256
		if strings.EqualFold(options.Quality, "standard") {
			crf, gifColors = 20, 192
		}
		replay, openErr := claim.OpenReplayContext(r.ctx, celltape.OpenOptions{Limits: canonicalTapeLimits})
		if openErr != nil {
			err = fmt.Errorf("open claimed recording CellTape: %w", openErr)
		} else {
			_, err = recording.ExportCellTapeReplay(r.ctx, replay, recording.CellTapeExportConfig{
				TapePath: claim.Path, EndHostNanos: endHostNanos,
				TapeLimits: canonicalTapeLimits,
				Writer: recording.Config{
					FFmpeg: ffmpeg, Output: output, Width: options.Width, Height: options.Height,
					FPS: options.FPS, Format: recording.Format(format), CRF: crf, GIFColors: gifColors,
				},
			})
			err = errors.Join(err, replay.Close())
		}
		if identityErr := claim.VerifyIdentity(); identityErr != nil {
			err = errors.Join(err, fmt.Errorf("recording CellTape identity changed during export: %w", identityErr))
		}
	}
	if err == nil {
		err = requireNonEmpty(output)
	}
	var retireErr error
	if claimed {
		if err == nil {
			retireErr = claim.Retire()
			if releaseErr := claim.Release(); releaseErr != nil && !errors.Is(retireErr, releaseErr) {
				retireErr = errors.Join(retireErr, releaseErr)
			}
		} else {
			err = errors.Join(err, claim.Release())
		}
	}
	r.recordMu.Lock()
	r.recordClosing = false
	if err == nil {
		r.lastSaved = output
		r.recordResult = supportreport.CodeCompleted
	} else {
		r.recordResult = supportreport.CodeSaveFailed
	}
	r.recordMu.Unlock()
	if err != nil {
		retained := retainedTapePath(staging, tapeFinal)
		r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingError, Format: format, Path: output, Err: fmt.Errorf("%w (recoverable cells kept at %s)", err, retained)})
		return
	}
	if retireErr != nil {
		retireErr = fmt.Errorf("recording saved at %s, but its recovery tape could not be removed: %w", output, retireErr)
	}
	r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingSaved, Format: format, Path: output, Err: retireErr})
}

func (r *Runtime) finishBrokenTape(tape *celltape.Recorder, reservation taperecovery.Reservation, submitErr error) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		staging := tape.StagingPath()
		r.recordMu.Lock()
		format, output, tapeFinal := r.recordFormat, r.recordOutput, r.tapeFinal
		r.recordMu.Unlock()
		claim, publishErr := closeAndPublishReservedTape(tape, reservation, tapeFinal)
		err := errors.Join(submitErr, publishErr, claim.Release())
		r.recordMu.Lock()
		r.recordClosing = false
		r.recordResult = supportreport.CodeSaveFailed
		r.recordMu.Unlock()
		retained := retainedTapePath(staging, tapeFinal)
		r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingError, Format: format, Path: output, Err: fmt.Errorf("%w (recoverable cells kept at %s)", err, retained)})
	}()
}

func closeAndPublishReservedTape(tape *celltape.Recorder, reservation taperecovery.Reservation, tapeFinal string) (taperecovery.Claim, error) {
	closeErr := tape.Close()
	engine, engineErr := recordingRecoveryEngine(filepath.Dir(tapeFinal))
	if engineErr != nil {
		return taperecovery.Claim{}, errors.Join(closeErr, engineErr, reservation.Release())
	}
	claim, publishErr := engine.PublishReserved(&reservation, tapeFinal)
	if publishErr != nil {
		publishErr = errors.Join(publishErr, reservation.Release())
	}
	return claim, errors.Join(closeErr, publishErr)
}

func retainedTapePath(staging, published string) string {
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		return staging
	}
	return published
}

// recoverRecordings turns both atomically published failure tapes and
// crash-left staging tapes back into media without blocking camera startup.
// Claim repairs only a CRC-checked committed prefix and ignores a staging file
// still owned by another live process. Every failure retains the canonical
// tape for a later retry.
func (r *Runtime) recoverRecordings(ffmpeg string, ffmpegErr error) {
	recoveryDirectory := r.layout.RecoveryDir
	engine, err := taperecovery.New(recoveryDirectory, taperecovery.Options{
		MaxConfigBytes: int(canonicalTapeLimits.MaxConfigBytes),
		TapeLimits:     canonicalTapeLimits,
	})
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		r.publishEvent(RuntimeEvent{Kind: RuntimeRecoveryError, Err: err})
		return
	}
	candidates, err := engine.Scan()
	if err != nil {
		r.publishEvent(RuntimeEvent{Kind: RuntimeRecoveryError, Err: err})
		return
	}
	if len(candidates) == 0 || r.closed.Load() {
		return
	}

	r.recordMu.Lock()
	if r.tape != nil || r.recordStarting || r.recordClosing || r.recoveryRunning {
		r.recordMu.Unlock()
		return
	}
	r.recoveryRunning = true
	r.recordMu.Unlock()
	defer func() {
		r.recordMu.Lock()
		r.recoveryRunning = false
		r.recordMu.Unlock()
	}()

	startedEvent := false
	var failures []error
	recovered, lastPath := 0, ""
	for _, candidate := range candidates {
		if err := r.ctx.Err(); err != nil {
			return
		}
		tape, claimErr := engine.ClaimContext(r.ctx, candidate)
		if errors.Is(claimErr, taperecovery.ErrBusy) {
			continue
		}
		if claimErr != nil {
			failures = append(failures, fmt.Errorf("claim %s: %w", filepath.Base(candidate.Path), claimErr))
			continue
		}
		if !startedEvent {
			startedEvent = true
			r.publishEvent(RuntimeEvent{Kind: RuntimeRecoveryStarting, Count: len(candidates)})
		}
		output, completed, recoveryErr := r.recoverClaimedTape(tape, ffmpeg, ffmpegErr)
		if completed {
			recovered++
			lastPath = output
		}
		if recoveryErr != nil {
			failures = append(failures, recoveryErr)
		}
	}

	r.recordMu.Lock()
	if lastPath != "" {
		r.lastSaved = lastPath
	}
	r.recordMu.Unlock()
	if len(failures) != 0 {
		r.publishEvent(RuntimeEvent{Kind: RuntimeRecoveryError, Count: recovered, Path: lastPath, Err: errors.Join(failures...)})
		return
	}
	if recovered > 0 {
		r.publishEvent(RuntimeEvent{Kind: RuntimeRecoverySaved, Count: recovered, Path: lastPath})
	}
}

// recoverClaimedTape owns one claim through output selection, export, and tape
// retirement. Its named return joins a release failure into every exit path,
// including malformed settings and cancellation, so a live process never
// accidentally strands an ownership lock.
func (r *Runtime) recoverClaimedTape(tape taperecovery.Tape, ffmpeg string, ffmpegErr error) (output string, completed bool, err error) {
	defer func() {
		if releaseErr := tape.Release(); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release recovery claim for %s: %w", filepath.Base(tape.Path), releaseErr))
		}
	}()

	var stored tapeRecordingConfig
	if decodeErr := json.Unmarshal(tape.Config.Raw, &stored); decodeErr != nil || stored.Version != 1 {
		return "", false, fmt.Errorf("read %s recording settings: %w", filepath.Base(tape.Path), errors.Join(decodeErr, taperecovery.ErrConfig))
	}
	format := strings.ToLower(strings.TrimSpace(stored.Output.Format))
	if format != string(recording.FormatMP4) && format != string(recording.FormatGIF) {
		return "", false, fmt.Errorf("recover %s: unsupported output format %q", filepath.Base(tape.Path), safeTerminalText(format))
	}
	if validationErr := validateRecoveredOutput(stored.Output, format); validationErr != nil {
		return "", false, fmt.Errorf("recover %s: %w", filepath.Base(tape.Path), validationErr)
	}
	output, outputErr := r.recoveryOutput(tape.Path, format)
	if outputErr != nil {
		return "", false, fmt.Errorf("choose recovered output: %w", outputErr)
	}
	if ffmpegErr != nil || strings.TrimSpace(ffmpeg) == "" {
		if ffmpegErr == nil {
			ffmpegErr = errors.New("FFmpeg is unavailable")
		}
		return "", false, fmt.Errorf("recover %s: %w", filepath.Base(tape.Path), ffmpegErr)
	}

	crf, gifColors := 16, 256
	if strings.EqualFold(stored.Output.Quality, "standard") {
		crf, gifColors = 20, 192
	}
	replay, openErr := tape.OpenReplayContext(r.ctx, celltape.OpenOptions{Limits: canonicalTapeLimits})
	if openErr != nil {
		return "", false, fmt.Errorf("recover %s: open claimed CellTape: %w", filepath.Base(tape.Path), openErr)
	}
	_, exportErr := recording.ExportCellTapeReplay(r.ctx, replay, recording.CellTapeExportConfig{
		TapePath:        tape.Path,
		TapeLimits:      canonicalTapeLimits,
		MaxOutputFrames: uint64(maxAutomaticRecoveryDuration/time.Second) * uint64(stored.Output.FPS),
		Writer: recording.Config{
			FFmpeg: ffmpeg, Output: output, Width: stored.Output.Width, Height: stored.Output.Height,
			FPS: stored.Output.FPS, Format: recording.Format(format), CRF: crf, GIFColors: gifColors,
		},
	})
	exportErr = errors.Join(exportErr, replay.Close())
	if identityErr := tape.VerifyIdentity(); identityErr != nil {
		return output, false, fmt.Errorf(
			"recover %s: CellTape identity changed during export: %w",
			filepath.Base(tape.Path),
			errors.Join(exportErr, identityErr),
		)
	}
	if exportErr == nil {
		exportErr = requireNonEmpty(output)
	}
	if exportErr != nil {
		return "", false, fmt.Errorf("recover %s: %w (CellTape kept at %s)", filepath.Base(tape.Path), exportErr, tape.Path)
	}
	// The media is already an atomically published, validated success. A tape
	// cleanup failure is diagnostic, but this recovery still counts as complete.
	return output, true, retireClaimedRecoveryTape(&tape)
}

func (r *Runtime) recoveryOutput(tapePath, format string) (string, error) {
	name := strings.TrimSuffix(filepath.Base(tapePath), filepath.Ext(tapePath)) + "." + format
	preferred := filepath.Join(r.layout.RecordingsDir, name)
	if _, err := os.Lstat(preferred); errors.Is(err, os.ErrNotExist) {
		return preferred, nil
	} else if err != nil {
		return "", err
	}
	output, err := r.nextOutput(r.layout.RecordingsDir, format)
	return output, err
}

func validateRecoveredOutput(options RecordOptions, format string) error {
	maximumWidth, maximumHeight := 3840, 2160
	if format == string(recording.FormatGIF) {
		maximumWidth, maximumHeight = 1920, 1080
	}
	if options.Width < 1 || options.Height < 1 || options.Width > maximumWidth || options.Height > maximumHeight {
		return fmt.Errorf("recovery output size %dx%d is outside the product limit %dx%d", options.Width, options.Height, maximumWidth, maximumHeight)
	}
	maximumFPS := 60
	if format == string(recording.FormatGIF) {
		maximumFPS = 30
	}
	if options.FPS < 1 || options.FPS > maximumFPS {
		return fmt.Errorf("recovery output FPS %d is outside 1..%d", options.FPS, maximumFPS)
	}
	if !strings.EqualFold(options.Quality, "standard") && !strings.EqualFold(options.Quality, "high") {
		return fmt.Errorf("recovery output quality %q is invalid", safeTerminalText(options.Quality))
	}
	return nil
}

func retireClaimedRecoveryTape(tape *taperecovery.Tape) error {
	if err := tape.Retire(); err != nil {
		return fmt.Errorf("retire claimed CellTape %s: %w", tape.Path, err)
	}
	return nil
}

func (r *Runtime) Snapshot(options RecordOptions) {
	if !r.beginAction() {
		return
	}
	defer r.wg.Done()
	frame, ok := r.currentCanonicalFrame()
	if !ok {
		r.publishEvent(RuntimeEvent{Kind: RuntimeSnapshotError, Err: errors.New("the terminal preview is not ready yet")})
		return
	}
	r.recordMu.Lock()
	if r.snapshotSaving {
		r.recordMu.Unlock()
		frame.Release()
		return
	}
	path, err := r.nextOutput(r.layout.SnapshotsDir, "png")
	if err == nil {
		r.snapshotSaving = true
	}
	r.recordMu.Unlock()
	if err != nil {
		frame.Release()
		r.publishEvent(RuntimeEvent{Kind: RuntimeSnapshotError, Err: err})
		return
	}
	r.publishEvent(RuntimeEvent{Kind: RuntimeSnapshotSaving, Path: path})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.writeSnapshot(snapshotRequest{options: options, path: path, frame: frame})
	}()
}

func (r *Runtime) writeSnapshot(request snapshotRequest) {
	defer request.frame.Release()
	output, err := cellrender.CanvasRGBA(request.frame, request.options.Width, request.options.Height, nil)
	if err == nil {
		err = writePNGAtomic(request.path, output)
	}
	r.recordMu.Lock()
	r.snapshotSaving = false
	r.recordMu.Unlock()
	if err != nil {
		r.publishEvent(RuntimeEvent{Kind: RuntimeSnapshotError, Path: request.path, Err: err})
		return
	}
	r.recordMu.Lock()
	r.lastSaved = request.path
	r.recordMu.Unlock()
	r.publishEvent(RuntimeEvent{Kind: RuntimeSnapshotSaved, Path: request.path})
}

func (r *Runtime) OpenFolder() {
	if !r.beginAction() {
		return
	}
	defer r.wg.Done()
	r.recordMu.Lock()
	last := r.lastSaved
	r.recordMu.Unlock()
	directory := r.layout.RecordingsDir
	if last != "" {
		directory = filepath.Dir(last)
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if err := os.MkdirAll(directory, 0o755); err != nil {
			r.publishEvent(RuntimeEvent{Kind: RuntimeFolderError, Err: err})
			return
		}
		if err := r.openFolder(r.ctx, directory); err != nil {
			r.publishEvent(RuntimeEvent{Kind: RuntimeFolderError, Err: err})
			return
		}
		r.publishEvent(RuntimeEvent{Kind: RuntimeFolderOpened, Path: directory})
	}()
}

func (r *Runtime) CreateSupportReport() {
	if !r.beginAction() {
		return
	}
	go func() {
		defer r.wg.Done()
		prepared, _, err := r.support.Prepare(r.currentSupportFacts(), supportreport.Include{})
		if err == nil {
			var saved supportreport.Saved
			saved, err = r.support.SaveDirectory(r.layout.SupportReportsDir, prepared)
			if err == nil {
				r.recordMu.Lock()
				r.lastSaved = saved.Path
				r.recordMu.Unlock()
				r.publishEvent(RuntimeEvent{Kind: RuntimeSupportReportSaved, Path: saved.Path})
				return
			}
		}
		r.publishEvent(RuntimeEvent{Kind: RuntimeSupportReportError, Err: err})
	}()
}

func (r *Runtime) currentSupportFacts() supportreport.Current {
	settings := r.currentSettings()
	view := r.currentView()

	r.supportMu.Lock()
	cameraName, source := r.supportCamera, r.supportSource
	r.supportMu.Unlock()
	r.devicesMu.RLock()
	deviceCount := len(r.deviceIDs)
	r.devicesMu.RUnlock()

	r.previewMu.RLock()
	columns, rows := r.latestPreview.Columns, r.latestPreview.Rows
	r.previewMu.RUnlock()
	if columns <= 0 || rows <= 0 {
		columns, rows = view.MaxColumns, view.MaxRows
	}

	r.recordMu.Lock()
	state := "idle"
	switch {
	case r.recordStarting:
		state = "starting"
	case r.tape != nil:
		state = "recording"
	case r.recordClosing || r.recoveryRunning:
		state = "saving"
	}
	recordOptions, recordStarted := r.recordOptions, r.tapeStarted
	duration, recordResult := r.recordDuration, r.recordResult
	if recordOptions.Format == "" {
		recordOptions = RecordOptions{
			Format: settings.SaveFormat, Quality: settings.ExportQuality,
			Width: settings.RecordingWidth, Height: settings.RecordingHeight, FPS: settings.RecordingFPS,
		}
	}
	r.recordMu.Unlock()
	if !recordStarted.IsZero() {
		duration = max(time.Since(recordStarted), 0)
	}
	r.ffmpegMu.RLock()
	ffmpegAvailable := r.ffmpeg != ""
	r.ffmpegMu.RUnlock()

	backend, requestedFormat := supportCameraBackend(cameraName)
	look := "custom"
	switch strings.ToLower(strings.TrimSpace(view.ColorLook)) {
	case "", "none":
		look = "none"
	case "warm", "cool", "mono":
		look = "built-in"
	}
	detail := "balanced"
	switch strings.ToLower(strings.TrimSpace(view.Detail)) {
	case "smooth", "soft":
		detail = "soft"
	case "sharp", "crisp":
		detail = "crisp"
	}
	framing := "whole"
	if view.Fill {
		framing = "fill"
	}
	downsample := 4
	pixelLayout := "planar-ycbcr"
	if backend == "demo" {
		downsample = 0
		pixelLayout = "demo"
	} else if backend == "avfoundation" {
		pixelLayout = "nv12"
	} else if backend == "unknown" {
		pixelLayout = "unknown"
	}
	return supportreport.Current{
		DistributionMode: string(r.layout.Mode),
		Camera: supportreport.CameraFacts{
			Model: cameraName, Backend: backend, DeviceCount: deviceCount,
			Requested: supportreport.ModeFacts{
				Width: settings.CaptureWidth, Height: settings.CaptureHeight,
				FPSNumerator: uint32(max(settings.CaptureFPS, 0)), FPSDenominator: 1, Format: requestedFormat,
			},
			Selected: supportreport.ModeFacts{
				Width: source.Width, Height: source.Height,
				FPSNumerator: source.FPSNumerator, FPSDenominator: source.FPSDenominator, Format: source.Format,
			},
			Downsample: downsample, PixelLayout: pixelLayout, Permission: "unknown",
		},
		View: supportreport.ViewFacts{
			GridColumns: columns, GridRows: rows, Framing: framing, Mirror: view.Mirror,
			Detail: detail, Look: look, LookMix: view.LookStrength, TargetFPS: view.TargetFPS,
		},
		Recording: supportreport.RecordingFacts{
			State: state, Format: strings.ToLower(recordOptions.Format), Width: recordOptions.Width,
			Height: recordOptions.Height, FPS: recordOptions.FPS, Quality: strings.ToLower(recordOptions.Quality),
			DurationMillis: duration.Milliseconds(), Result: recordResult,
			FFmpeg: supportreport.FFmpegFacts{Available: ffmpegAvailable},
		},
	}
}

func supportCameraBackend(camera string) (backend, requestedFormat string) {
	if strings.EqualFold(strings.TrimSpace(camera), "DEMO") {
		return "demo", "DEMO"
	}
	switch goruntime.GOOS {
	case "windows":
		return "media-foundation", "MJPG"
	case "linux":
		return "v4l2", "MJPG"
	case "darwin":
		return "avfoundation", "NV12"
	default:
		return "unknown", "unknown"
	}
}

func (r *Runtime) observeSupportSample(result celllive.Result) {
	if r.support == nil {
		return
	}
	now := time.Now()
	r.supportMu.Lock()
	if !r.supportSampleAt.IsZero() && now.Sub(r.supportSampleAt) < 5*time.Second {
		r.supportMu.Unlock()
		return
	}
	r.supportSampleAt = now
	r.supportMu.Unlock()

	var memory goruntime.MemStats
	goruntime.ReadMemStats(&memory)
	r.deliveryMu.Lock()
	accepted, dropped, shownFPS := r.deliveryAccepted, r.deliveryDropped, r.deliveryFPS
	r.deliveryMu.Unlock()
	queue := supportreport.QueueHealth{}
	r.recordMu.Lock()
	if r.tape != nil {
		pressure := r.tape.QueuePressure()
		queue = supportreport.QueueHealth{
			Active: true, InFlight: pressure.InFlight, HighWater: pressure.HighWater, Capacity: pressure.Capacity,
		}
	}
	r.recordMu.Unlock()
	captureDropped := result.Capture.DroppedPackets + result.Capture.DroppedFrames + result.Capture.DecodeErrors
	presentationDropped := uint64(0)
	if result.Dropped > captureDropped {
		presentationDropped = result.Dropped - captureDropped
	}
	r.support.Observe(supportreport.Sample{
		ObservedAt: now, SourceFPS: result.SourceFPS, ShownFPS: shownFPS,
		GridColumns: result.Columns, GridRows: result.Rows,
		Capture: supportreport.CaptureHealth{
			Packets: result.Capture.Packets, Decoded: result.Capture.Decoded,
			DroppedPackets: result.Capture.DroppedPackets, DroppedFrames: result.Capture.DroppedFrames,
			DecodeErrors: result.Capture.DecodeErrors, TemporaryErrors: result.Capture.TemporaryErrors,
		},
		Presentation: supportreport.PresentationHealth{Accepted: accepted, Dropped: dropped + presentationDropped},
		Queue:        queue,
		Process: supportreport.ProcessHealth{
			HeapBytes: memory.HeapAlloc, Goroutines: goruntime.NumGoroutine(), GCCycles: memory.NumGC,
		},
	})
}

const settingsSaveDebounce = 35 * time.Millisecond

func (r *Runtime) Save(settings Settings) {
	if !r.beginAction() {
		return
	}
	defer r.wg.Done()
	// saveMu is an admission gate as well as the one-slot pending state. Close
	// takes the same gate before choosing its final snapshot, so a Save either
	// becomes part of that snapshot or is cleanly rejected after shutdown.
	r.saveMu.Lock()
	if r.saveClosing || r.closed.Load() {
		r.saveMu.Unlock()
		return
	}
	r.settingsMu.Lock()
	// The UI deals only in friendly labels. Preserve the stable native ID that
	// discovery bound to the currently selected label.
	if settings.DeviceID == "" && strings.EqualFold(settings.Device, r.settings.Device) {
		settings.DeviceID = r.settings.DeviceID
	}
	r.settings = settings
	r.settingsMu.Unlock()
	r.savePending, r.savePendingSet = settings, true
	r.saveMu.Unlock()
	select {
	case r.saveWake <- struct{}{}:
	default:
	}
}

// runSettingsSaver owns all settings-file writes. Input is one latest-only
// slot plus a bounded wake signal, so rapid controls cannot create a goroutine
// backlog. Close stops admission and asks this worker for one final flush.
func (r *Runtime) runSettingsSaver() {
	defer close(r.saveDone)
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-r.saveStop:
			if timer != nil && !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			r.writePendingSettings(true)
			return
		case <-r.saveWake:
			if timer == nil {
				timer = time.NewTimer(settingsSaveDebounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(settingsSaveDebounce)
			}
			timerC = timer.C
		case <-timerC:
			timerC = nil
			r.writePendingSettings(false)
		}
	}
}

func (r *Runtime) writePendingSettings(final bool) {
	r.saveMu.Lock()
	if !r.savePendingSet {
		r.saveMu.Unlock()
		return
	}
	settings := r.savePending
	r.savePendingSet = false
	r.saveMu.Unlock()

	err := SaveSettings(r.layout.SettingsFile, settings)
	if final {
		r.saveMu.Lock()
		r.saveErr = err
		r.saveMu.Unlock()
		return
	}
	if err != nil {
		r.publishEvent(RuntimeEvent{Kind: RuntimeSettingsError, Err: err})
		return
	}
	r.publishEvent(RuntimeEvent{Kind: RuntimeSettingsSaved})
}

func (r *Runtime) stopSettingsSaver() error {
	r.saveMu.Lock()
	r.saveClosing = true
	r.settingsMu.RLock()
	r.savePending = r.settings
	r.settingsMu.RUnlock()
	r.savePendingSet = true
	r.saveMu.Unlock()
	close(r.saveStop)
	<-r.saveDone
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	return r.saveErr
}

func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.actionMu.Lock()
		r.closed.Store(true)
		r.actionMu.Unlock()
		r.cameraSelectMu.Lock()
		r.advanceCameraGeneration()
		r.cancelCameraLifetime()
		r.cameraSelectMu.Unlock()
		r.cancel()
		settingsErr := r.stopSettingsSaver()
		r.cameraOpMu.Lock()
		r.sessionMu.Lock()
		session := r.session
		r.session = nil
		r.sessionMu.Unlock()
		var cameraCloseErr error
		if session != nil {
			cameraCloseErr = r.closeCameraSession(session)
		}
		r.sessionMu.RLock()
		cameraShutdownErr := r.cameraShutdownErr
		r.sessionMu.RUnlock()
		r.cameraOpMu.Unlock()

		r.recordMu.Lock()
		tape, reservation, tapeFinal := r.tape, r.tapeReservation, r.tapeFinal
		r.tape, r.tapeReservation, r.tapeConfig = nil, taperecovery.Reservation{}, nil
		r.snapshotSaving = false
		r.recordMu.Unlock()
		if tape != nil {
			claim, publishErr := closeAndPublishReservedTape(tape, reservation, tapeFinal)
			r.closeErr = errors.Join(r.closeErr, publishErr, claim.Release())
		}
		r.previewMu.Lock()
		latest := r.latestFrame
		r.latestFrame = nil
		r.hasPreview = false
		r.previewMu.Unlock()
		if latest != nil {
			latest.Release()
		}
		if errors.Is(cameraCloseErr, capture.ErrShutdownUncertain) {
			cameraCloseErr = nil
		}
		r.closeErr = errors.Join(r.closeErr, settingsErr, cameraCloseErr, cameraShutdownErr)
		r.wg.Wait()
		close(r.previews)
		close(r.events)
	})
	return r.closeErr
}

func (r *Runtime) beginAction() bool {
	if r == nil {
		return false
	}
	r.actionMu.Lock()
	defer r.actionMu.Unlock()
	if r.closed.Load() {
		return false
	}
	r.wg.Add(1)
	return true
}

func (r *Runtime) currentSettings() Settings {
	r.settingsMu.RLock()
	defer r.settingsMu.RUnlock()
	return r.settings
}
func (r *Runtime) currentView() ViewOptions {
	r.viewMu.RLock()
	defer r.viewMu.RUnlock()
	return r.view
}

func (r *Runtime) publishPreview(update PreviewUpdate) bool {
	r.deliveryMu.Lock()
	update.ShownFPS = r.deliveryFPS
	update.Dropped += r.deliveryDropped
	r.deliveryMu.Unlock()

	r.previewQueueMu.Lock()
	if r.closed.Load() || update.cameraGeneration != r.cameraGeneration.Load() {
		r.previewQueueMu.Unlock()
		return false
	}
	select {
	case r.previews <- update:
		r.previewQueueMu.Unlock()
		return true
	case <-r.ctx.Done():
		r.previewQueueMu.Unlock()
		return false
	default:
	}

	// The pending frame has not reached Model.Update yet, so reject its
	// canonical lease and replace it with the newer camera state. This is
	// bounded latest-wins delivery: it smooths Bubble Tea scheduling jitter but
	// never queues stale video.
	var displaced PreviewUpdate
	select {
	case old := <-r.previews:
		displaced = old
	default:
	}
	accepted := false
	select {
	case r.previews <- update:
		accepted = true
	case <-r.ctx.Done():
	default:
	}
	r.previewQueueMu.Unlock()
	displaced.acknowledgeRendered(false)
	return accepted
}

// observePreviewOutcome measures frames that actually reached and were
// composed by Model.Update. Counting successful channel sends overstates FPS
// whenever a pending frame is superseded before the UI sees it.
func (r *Runtime) observePreviewOutcome(accepted bool) {
	now := time.Now()
	r.deliveryMu.Lock()
	if r.deliveryStarted.IsZero() {
		r.deliveryStarted = now
	}
	if accepted {
		r.deliveryCount++
		r.deliveryAccepted++
	} else {
		r.deliveryDropped++
	}
	if elapsed := now.Sub(r.deliveryStarted); elapsed >= time.Second {
		r.deliveryFPS = float64(r.deliveryCount) / elapsed.Seconds()
		r.deliveryStarted, r.deliveryCount = now, 0
	}
	r.deliveryMu.Unlock()
}

func (r *Runtime) publishEvent(event RuntimeEvent) {
	r.recordSupportEvent(event)
	select {
	case r.events <- event:
	case <-r.ctx.Done():
	}
}

func (r *Runtime) recordSupportEvent(event RuntimeEvent) {
	if r.support == nil {
		return
	}
	record := supportreport.Event{OccurredAt: time.Now(), Severity: supportreport.SeverityError}
	switch event.Kind {
	case RuntimeDevicesFound:
		if event.Err == nil {
			return
		}
		record.Area, record.Code = supportreport.AreaCamera, supportreport.CodeDiscoveryFailed
	case RuntimeCameraLive:
		record.Area, record.Code, record.Severity = supportreport.AreaCamera, supportreport.CodeCompleted, supportreport.SeverityInfo
	case RuntimeCameraError:
		record.Area, record.Code = supportreport.AreaCamera, supportreport.CodeStreamEnded
	case RuntimeRecordingSaved:
		record.Area, record.Code, record.Severity = supportreport.AreaRecording, supportreport.CodeCompleted, supportreport.SeverityInfo
	case RuntimeRecordingError:
		record.Area, record.Code = supportreport.AreaRecording, supportreport.CodeSaveFailed
	case RuntimeRecoveryError:
		record.Area, record.Code = supportreport.AreaRecovery, supportreport.CodeRecoveryFailed
	case RuntimeSnapshotSaved:
		record.Area, record.Code, record.Severity = supportreport.AreaRecording, supportreport.CodeCompleted, supportreport.SeverityInfo
	case RuntimeSnapshotError, RuntimeFolderError:
		record.Area, record.Code = supportreport.AreaRecording, supportreport.CodeSaveFailed
	case RuntimeSettingsError:
		record.Area, record.Code = supportreport.AreaSettings, supportreport.CodeSettingsFailed
	case RuntimeLooksFound:
		if event.Err == nil {
			return
		}
		record.Area, record.Code = supportreport.AreaLooks, supportreport.CodeLookRejected
	default:
		return
	}
	r.support.Record(record)
}

func (r *Runtime) nextOutput(directory, extension string) (string, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create output folder: %w", err)
	}
	base := "Inlaid_" + time.Now().Format("2006-01-02_15-04-05-000")
	for suffix := 1; suffix < 1000; suffix++ {
		name := base
		if suffix > 1 {
			name += fmt.Sprintf("-%d", suffix)
		}
		path := filepath.Join(directory, name+"."+extension)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not find an unused output filename")
}

func writePNGAtomic(path string, frame *image.RGBA) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".inlaid-snapshot-*.png")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(tempPath)
		}
	}()
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(temp, frame); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func requireNonEmpty(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return errors.New("encoder produced an empty file")
	}
	return nil
}
