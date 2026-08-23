package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/celllive"
	"github.com/Melty1000/inlaid/internal/cellrender"
	"github.com/Melty1000/inlaid/internal/celltape"
	ffmpegexe "github.com/Melty1000/inlaid/internal/ffmpeg"
	"github.com/Melty1000/inlaid/internal/mfcapture"
	"github.com/Melty1000/inlaid/internal/recording"
	"github.com/Melty1000/inlaid/internal/taperecovery"
)

// Runtime owns one camera and every non-visual operation behind the dashboard.
// It is deliberately independent from Bubble Tea: all public actions return
// immediately and report completion through Events or Previews.
type Runtime struct {
	ctx                context.Context
	cancel             context.CancelFunc
	root, settingsPath string

	previews             chan PreviewUpdate
	events               chan RuntimeEvent
	startOnce, closeOnce sync.Once
	closed               atomic.Bool
	wg                   sync.WaitGroup

	settingsMu sync.RWMutex
	settings   Settings
	viewMu     sync.RWMutex
	view       ViewOptions
	looksMu    sync.RWMutex
	looks      lookCatalog

	ffmpegMu         sync.RWMutex
	ffmpeg           string
	devicesMu        sync.RWMutex
	deviceIDs        map[string]string
	cameraGeneration atomic.Uint64
	cameraOpMu       sync.Mutex
	sessionMu        sync.RWMutex
	session          *celllive.Session

	recordMu                                      sync.Mutex
	tape                                          *celltape.Recorder
	tapeStarted                                   time.Time
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

	deliveryMu      sync.Mutex
	deliveryStarted time.Time
	deliveryCount   uint64
	deliveryFPS     float64
	deliveryDropped uint64
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
	recordingStallBudgetSeconds = 4
	maxRecordingQueueFrames     = 240
)

// recordingQueueCapacity gives the lossless tape writer a bounded cushion for
// ordinary filesystem, antivirus, scheduler, and compression stalls. Buffers
// allocate cell storage as the bounded FIFO circulates, then reuse it for the
// rest of the recording. At normal 30-60 FPS the queue covers four seconds;
// its memory can never grow beyond the 240-frame ceiling.
func recordingQueueCapacity(targetFPS int) int {
	if targetFPS <= 0 {
		targetFPS = 30
	}
	targetFPS = min(targetFPS, 60)
	return min(max(targetFPS*recordingStallBudgetSeconds, 8), maxRecordingQueueFrames)
}

func NewRuntime(cfg Settings, settingsPath, root string) *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	if strings.TrimSpace(root) == "" {
		root, _ = os.Getwd()
	}
	runtime := &Runtime{
		ctx: ctx, cancel: cancel, root: root, settingsPath: settingsPath, settings: cfg,
		// Keep exactly one composed frame waiting for Bubble Tea. A zero-capacity
		// channel discarded a camera frame whenever Bubble Tea was between its
		// one-shot receive commands, turning a healthy 29.8 FPS source into a
		// roughly 27 FPS preview. One latest-wins slot removes that scheduling race
		// without allowing latency to accumulate.
		previews: make(chan PreviewUpdate, 1), events: make(chan RuntimeEvent, 64),
		deviceIDs: make(map[string]string),
		looks:     builtInLookCatalog(),
		saveWake:  make(chan struct{}, 1), saveStop: make(chan struct{}), saveDone: make(chan struct{}),
	}
	runtime.wg.Add(1)
	go func() {
		defer runtime.wg.Done()
		runtime.runSettingsSaver()
	}()
	return runtime
}

func (r *Runtime) Start(view ViewOptions) {
	r.viewMu.Lock()
	r.view = view
	r.viewMu.Unlock()
	r.startOnce.Do(func() {
		r.wg.Add(2)
		go func() { defer r.wg.Done(); r.discover() }()
		go func() { defer r.wg.Done(); r.discoverLooks() }()
	})
}

func (r *Runtime) Previews() <-chan PreviewUpdate { return r.previews }
func (r *Runtime) Events() <-chan RuntimeEvent    { return r.events }

func (r *Runtime) discover() {
	r.publishEvent(RuntimeEvent{Kind: RuntimeFindingCameras})
	// FFmpeg is now only an offline export dependency. Camera discovery and
	// capture use Media Foundation's stable symbolic links directly.
	ffmpegPath, ffmpegErr := ffmpegexe.Find(filepath.Join(r.root, ".tools", "ffmpeg", "bin", "ffmpeg.exe"))
	if ffmpegErr == nil {
		r.ffmpegMu.Lock()
		r.ffmpeg = ffmpegPath
		r.ffmpegMu.Unlock()
	}
	// Recovery is independent of camera opening. Run it off-thread so even a
	// long prior recording can be repaired/exported without delaying the live
	// preview. discover itself still owns a WaitGroup slot while adding this one.
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.recoverRecordings(ffmpegPath, ffmpegErr)
	}()

	ctx, cancel := context.WithTimeout(r.ctx, 8*time.Second)
	nativeDevices, listErr := mfcapture.Enumerate(ctx)
	cancel()
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
	r.publishEvent(RuntimeEvent{Kind: RuntimeDevicesFound, Devices: devices, Device: selected})
	r.SelectCamera(selected)
}

func (r *Runtime) discoverLooks() {
	catalog, err := loadLookCatalog(filepath.Join(r.root, "filters"))
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
	generation := r.advanceCameraGeneration()
	r.publishEvent(RuntimeEvent{Kind: RuntimeConnecting, Device: device})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.switchCamera(generation, device)
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

func (r *Runtime) switchCamera(generation uint64, device string) {
	r.cameraOpMu.Lock()
	defer r.cameraOpMu.Unlock()
	if r.closed.Load() || generation != r.cameraGeneration.Load() {
		return
	}

	r.sessionMu.Lock()
	old := r.session
	r.session = nil
	r.sessionMu.Unlock()
	if old != nil {
		old.Close()
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
	if r.closed.Load() || generation != r.cameraGeneration.Load() {
		return
	}

	view := r.currentView()
	liveView := r.toCellLiveView(view)
	settings := r.currentSettings()
	var session *celllive.Session
	var err error
	if strings.EqualFold(device, "DEMO") {
		session, err = celllive.StartSynthetic(r.ctx, settings.CaptureWidth, settings.CaptureHeight, settings.CaptureFPS, liveView)
	} else {
		r.devicesMu.RLock()
		deviceID := r.deviceIDs[device]
		r.devicesMu.RUnlock()
		if deviceID == "" {
			err = fmt.Errorf("camera %q no longer has a Media Foundation device ID", safeTerminalText(device))
		} else {
			cfg := mfcapture.DefaultConfig()
			cfg.DeviceID = deviceID
			cfg.Width, cfg.Height, cfg.FPS = settings.CaptureWidth, settings.CaptureHeight, settings.CaptureFPS
			// Quarter-size WIC decode still supplies the full 2x2 sample grid
			// for ordinary terminals up to 240 columns, while cutting full-HD
			// pixel traffic by 93.75%. Larger grids interpolate the same source
			// information instead of pretending to add detail.
			cfg.Lowres = 2
			session, err = celllive.StartMediaFoundation(r.ctx, cfg, liveView)
		}
	}
	if err != nil {
		r.publishEvent(RuntimeEvent{Kind: RuntimeCameraError, Device: device, Err: err})
		return
	}
	if r.closed.Load() || generation != r.cameraGeneration.Load() {
		session.Close()
		return
	}
	// Install and reconcile under one session lock. A WindowSizeMsg can update
	// the requested view while Media Foundation is still opening. Without this
	// second read, the newly opened session can keep emitting the stale version
	// forever while the model correctly rejects every frame.
	r.installSession(session)
	sourceInfo := session.SourceInfo()
	r.publishEvent(RuntimeEvent{
		Kind: RuntimeCameraLive, Device: device,
		Width: sourceInfo.Width, Height: sourceInfo.Height, FPS: sourceInfo.FPS, Backend: sourceInfo.Backend,
	})
	r.wg.Add(1)
	go func() { defer r.wg.Done(); r.forwardSession(generation, device, session) }()
}

func (r *Runtime) installSession(session *celllive.Session) {
	r.sessionMu.Lock()
	r.session = session
	r.sessionMu.Unlock()
	if session != nil {
		// Publish the pointer before reading the current view. This ordering
		// closes both sides of the camera-open race without nesting sessionMu
		// and viewMu: an UpdateView either sees no session and this read catches
		// its value, or it sees the session and sends its own newer update.
		session.Update(r.toCellLiveView(r.currentView()))
	}
}

func (r *Runtime) forwardSession(generation uint64, device string, session *celllive.Session) {
	terminalReported := false
	defer func() {
		// A session that ends owns native buffers and callbacks until Close has
		// drained them. Detach only if this is still the installed generation;
		// a newer camera must never be cleared by an older forwarder.
		session.Close()
		r.sessionMu.Lock()
		wasCurrent := r.session == session
		if wasCurrent {
			r.session = nil
		}
		r.sessionMu.Unlock()
		if wasCurrent && !terminalReported && generation == r.cameraGeneration.Load() && !r.closed.Load() {
			r.publishEvent(RuntimeEvent{
				Kind: RuntimeCameraError, Device: device,
				Err: errors.New("camera stream ended unexpectedly"),
			})
		}
	}()
	results, errorsCh := session.Results, session.Errors
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
				Dropped: result.Dropped, RenderDuration: result.SolveDuration,
				cameraGeneration: generation,
			}
			if result.Frame != nil {
				update.CapturedAt = result.Frame.SourceCapturedAt()
			}
			var acknowledgeOnce sync.Once
			acceptedUpdate := update
			canonical := result.Frame
			prepared, preparedTape, tapeStarted, preparedOK := r.prepareCanonicalFrame(canonical)
			update.acknowledge = func(accepted bool) {
				acknowledgeOnce.Do(func() {
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
				if mfcapture.IsTemporary(err) {
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
	if broken {
		r.tape, r.tapeConfig, r.recordClosing = nil, nil, true
	}
	r.recordMu.Unlock()
	if broken {
		r.finishBrokenTape(tape, err)
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
	if broken {
		r.tape, r.tapeConfig, r.recordClosing = nil, nil, true
	}
	r.recordMu.Unlock()
	if err != nil {
		prepared.Abort()
	}
	if broken {
		r.finishBrokenTape(tape, err)
	}
}

func (r *Runtime) UpdateView(view ViewOptions) {
	r.viewMu.Lock()
	r.view = view
	r.viewMu.Unlock()
	r.sessionMu.RLock()
	session := r.session
	r.sessionMu.RUnlock()
	if session != nil {
		session.Update(r.toCellLiveView(view))
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
	if r.closed.Load() {
		return
	}
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
	output, err := r.nextOutput("recordings", string(format))
	if err != nil {
		r.recordStartFailed(options.Format, err)
		return
	}
	r.ffmpegMu.RLock()
	ffmpeg := r.ffmpeg
	r.ffmpegMu.RUnlock()
	if ffmpeg == "" {
		ffmpeg, err = ffmpegexe.Find(filepath.Join(r.root, ".tools", "ffmpeg", "bin", "ffmpeg.exe"))
	}
	if err != nil {
		r.recordStartFailed(options.Format, err)
		return
	}
	r.ffmpegMu.Lock()
	r.ffmpeg = ffmpeg // validated now; the actual encoder starts only after Stop.
	r.ffmpegMu.Unlock()
	recoveryDirectory := filepath.Join(r.root, "recordings", ".recovery")
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
	configBlob, err := json.Marshal(tapeRecordingConfig{Version: 1, View: r.currentView(), Output: options})
	if err == nil {
		err = tape.SubmitCellFrame(firstFrame, 1, configBlob, celltape.BoundaryDiscontinuity, 0)
	}
	if err != nil {
		_ = tape.Close()
		r.recordStartFailed(options.Format, err)
		return
	}

	r.recordMu.Lock()
	if r.closed.Load() {
		r.recordStarting = false
		r.recordMu.Unlock()
		_ = tape.Close()
		_ = celltape.Publish(tape.StagingPath(), tapeFinal)
		return
	}
	r.tape, r.tapeStarted, r.tapeFinal, r.tapeConfig = tape, time.Now(), tapeFinal, configBlob
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
	r.recordMu.Unlock()
	r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingError, Format: format, Err: err})
}

func (r *Runtime) StopRecording() {
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
	tape, output, format := r.tape, r.recordOutput, r.recordFormat
	started, tapeFinal, options := r.tapeStarted, r.tapeFinal, r.recordOptions
	endHostNanos := uint64(max(time.Since(started), 0))
	r.tape, r.tapeConfig, r.recordClosing = nil, nil, true
	r.recordMu.Unlock()
	r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingSaving, Format: format, Path: output})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.finishTape(tape, endHostNanos, tapeFinal, output, format, options)
	}()
}

func (r *Runtime) finishTape(tape *celltape.Recorder, endHostNanos uint64, tapeFinal, output, format string, options RecordOptions) {
	err := tape.Close()
	if publishErr := celltape.Publish(tape.StagingPath(), tapeFinal); publishErr != nil {
		err = errors.Join(err, fmt.Errorf("publish recoverable recording tape: %w", publishErr))
	}
	if err == nil {
		r.ffmpegMu.RLock()
		ffmpeg := r.ffmpeg
		r.ffmpegMu.RUnlock()
		crf, gifColors := 16, 256
		if strings.EqualFold(options.Quality, "standard") {
			crf, gifColors = 20, 192
		}
		_, err = recording.ExportCellTape(r.ctx, recording.CellTapeExportConfig{
			TapePath: tapeFinal, EndHostNanos: endHostNanos,
			Writer: recording.Config{
				FFmpeg: ffmpeg, Output: output, Width: options.Width, Height: options.Height,
				FPS: options.FPS, Format: recording.Format(format), CRF: crf, GIFColors: gifColors,
			},
		})
	}
	if err == nil {
		err = requireNonEmpty(output)
	}
	r.recordMu.Lock()
	r.recordClosing = false
	if err == nil {
		r.lastSaved = output
	}
	r.recordMu.Unlock()
	if err != nil {
		r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingError, Format: format, Path: output, Err: fmt.Errorf("%w (recoverable cells kept at %s)", err, tapeFinal)})
		return
	}
	_ = os.Remove(tapeFinal)
	r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingSaved, Format: format, Path: output})
}

func (r *Runtime) finishBrokenTape(tape *celltape.Recorder, submitErr error) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		closeErr := tape.Close()
		r.recordMu.Lock()
		format, output, tapeFinal := r.recordFormat, r.recordOutput, r.tapeFinal
		r.recordClosing = false
		r.recordMu.Unlock()
		publishErr := celltape.Publish(tape.StagingPath(), tapeFinal)
		err := errors.Join(submitErr, closeErr, publishErr)
		r.publishEvent(RuntimeEvent{Kind: RuntimeRecordingError, Format: format, Path: output, Err: fmt.Errorf("%w (recoverable cells kept at %s)", err, tapeFinal)})
	}()
}

// recoverRecordings turns both atomically published failure tapes and
// crash-left staging tapes back into media without blocking camera startup.
// Claim repairs only a CRC-checked committed prefix and ignores a staging file
// still owned by another live process. Every failure retains the canonical
// tape for a later retry.
func (r *Runtime) recoverRecordings(ffmpeg string, ffmpegErr error) {
	recoveryDirectory := filepath.Join(r.root, "recordings", ".recovery")
	engine, err := taperecovery.New(recoveryDirectory, taperecovery.Options{})
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
		tape, claimErr := engine.Claim(candidate)
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
	output, alreadyPublished, outputErr := r.recoveryOutput(tape.Path, format)
	if outputErr != nil {
		return "", false, fmt.Errorf("choose recovered output: %w", outputErr)
	}
	if alreadyPublished {
		return output, true, retireRecoveryTape(tape.Path)
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
	_, exportErr := recording.ExportCellTape(r.ctx, recording.CellTapeExportConfig{
		TapePath: tape.Path, RepairTail: false,
		Writer: recording.Config{
			FFmpeg: ffmpeg, Output: output, Width: stored.Output.Width, Height: stored.Output.Height,
			FPS: stored.Output.FPS, Format: recording.Format(format), CRF: crf, GIFColors: gifColors,
		},
	})
	if exportErr == nil {
		exportErr = requireNonEmpty(output)
	}
	if exportErr != nil {
		return "", false, fmt.Errorf("recover %s: %w (CellTape kept at %s)", filepath.Base(tape.Path), exportErr, tape.Path)
	}
	// The media is already an atomically published, validated success. A tape
	// cleanup failure is diagnostic, but this recovery still counts as complete.
	return output, true, retireRecoveryTape(tape.Path)
}

func (r *Runtime) recoveryOutput(tapePath, format string) (string, bool, error) {
	name := strings.TrimSuffix(filepath.Base(tapePath), filepath.Ext(tapePath)) + "." + format
	preferred := filepath.Join(r.root, "recordings", name)
	if info, err := os.Stat(preferred); errors.Is(err, os.ErrNotExist) {
		return preferred, false, nil
	} else if err != nil {
		return "", false, err
	} else if info.Mode().IsRegular() && info.Size() > 0 {
		// A prior recovery may have published successfully and then crashed
		// before deleting its canonical tape. Treat the validated final as the
		// completion marker instead of creating a duplicate every launch.
		return preferred, true, nil
	}
	output, err := r.nextOutput("recordings", format)
	return output, false, err
}

func retireRecoveryTape(path string) error {
	if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	} else {
		// Leave the known canonical name in the scan set. On the next launch the
		// already-published media is the completion marker, so recovery retries
		// only this deletion and never creates a duplicate or strands an
		// untracked multi-gigabyte renamed tape.
		return fmt.Errorf("remove recovered CellTape %s: %w", path, err)
	}
}

func (r *Runtime) Snapshot(options RecordOptions) {
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
	path, err := r.nextOutput("snapshots", "png")
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
	r.recordMu.Lock()
	last := r.lastSaved
	r.recordMu.Unlock()
	directory := filepath.Join(r.root, "recordings")
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
		if err := exec.Command("explorer.exe", directory).Start(); err != nil {
			r.publishEvent(RuntimeEvent{Kind: RuntimeFolderError, Err: err})
			return
		}
		r.publishEvent(RuntimeEvent{Kind: RuntimeFolderOpened, Path: directory})
	}()
}

const settingsSaveDebounce = 35 * time.Millisecond

func (r *Runtime) Save(settings Settings) {
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

	err := SaveSettings(r.settingsPath, settings)
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
	var closeErr error
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		r.advanceCameraGeneration()
		r.cancel()
		settingsErr := r.stopSettingsSaver()
		r.cameraOpMu.Lock()
		r.sessionMu.Lock()
		session := r.session
		r.session = nil
		r.sessionMu.Unlock()
		if session != nil {
			session.Close()
		}
		r.cameraOpMu.Unlock()

		r.recordMu.Lock()
		tape, tapeFinal := r.tape, r.tapeFinal
		r.tape, r.tapeConfig = nil, nil
		r.snapshotSaving = false
		r.recordMu.Unlock()
		if tape != nil {
			closeErr = tape.Close()
			if publishErr := celltape.Publish(tape.StagingPath(), tapeFinal); publishErr != nil {
				closeErr = errors.Join(closeErr, publishErr)
			}
		}
		r.previewMu.Lock()
		latest := r.latestFrame
		r.latestFrame = nil
		r.hasPreview = false
		r.previewMu.Unlock()
		if latest != nil {
			latest.Release()
		}
		closeErr = errors.Join(closeErr, settingsErr)
		r.wg.Wait()
		close(r.previews)
		close(r.events)
	})
	return closeErr
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
	select {
	case r.events <- event:
	case <-r.ctx.Done():
	}
}

func (r *Runtime) nextOutput(folder, extension string) (string, error) {
	directory := filepath.Join(r.root, folder)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create %s folder: %w", folder, err)
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
