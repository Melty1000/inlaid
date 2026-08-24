//go:build windows

package capture

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Enumerate returns Media Foundation friendly names and stable symbolic links.
func Enumerate(ctx context.Context) ([]Device, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	shutdown, err := startMediaFoundation()
	if err != nil {
		return nil, err
	}
	defer shutdown()
	set, err := enumerateActivations()
	if err != nil {
		return nil, err
	}
	defer set.close()
	devices := make([]Device, 0, len(set.items))
	for _, activation := range set.items {
		devices = append(devices, activation.device)
	}
	return devices, ctx.Err()
}

// Open starts the closest deterministic native MJPEG capture and pooled WIC
// planar decode for the exact stable device identity in cfg.
func Open(parent context.Context, cfg Config) (*Session, error) {
	normalized, err := normalize(cfg, true)
	if err != nil {
		return nil, err
	}
	source, err := newMFNativeSource(parent, normalized)
	if err != nil {
		return nil, err
	}
	selected := source.Mode()
	selectedConfig := normalized
	selectedConfig.Width = selected.Width
	selectedConfig.Height = selected.Height
	selectedConfig.FPS = selected.NominalFPS()
	selectedConfig, err = normalize(selectedConfig, true)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("selected native mode is unusable: %w", err), source.Close())
	}
	decoder, err := newWICDecoder(selectedConfig)
	if err != nil {
		return nil, errors.Join(err, source.Close())
	}
	session, err := StartPipeline(parent, selectedConfig, source, decoder)
	if err != nil {
		return nil, errors.Join(err, decoder.Close(), source.Close())
	}
	session.selectedMode = selected
	return session, nil
}

type mfNativeSource struct {
	cfg        Config
	mode       Mode
	packets    chan Packet
	errors     chan error
	stop       chan struct{}
	done       chan struct{}
	flush      chan struct{}
	closeOnce  sync.Once
	flushOnce  sync.Once
	closeMu    sync.Mutex
	closeErr   error
	outputMu   sync.Mutex
	stopping   atomic.Bool
	dropped    atomic.Uint64
	requestMu  sync.Mutex
	reader     comObject
	callbacks  callbackGate
	packetPool *packetPool
	initGuard  *mfNativeInitializationGuard

	callbackCount    atomic.Uint64
	callbackLastNS   atomic.Uint64
	callbackGapNS    atomic.Uint64
	callbackMaxGapNS atomic.Uint64
	requestCount     atomic.Uint64
	requestTotalNS   atomic.Uint64
	requestMaxNS     atomic.Uint64
	frameRateControl frameRateControlResult
}

type sourceInit struct{ err error }

type mfNativeSourceControl func(context.Context, *mfNativeSource, chan<- sourceInit)

type mfNativeInitializationGuard struct {
	mu           sync.Mutex
	initializing bool
	uncertain    *mfNativeSource
	uncertainErr error
}

var windowsMFInitialization mfNativeInitializationGuard

func newMFNativeSource(parent context.Context, cfg Config) (*mfNativeSource, error) {
	return newMFNativeSourceWithControl(parent, cfg, &windowsMFInitialization,
		func(parent context.Context, source *mfNativeSource, ready chan<- sourceInit) {
			source.control(parent, ready)
		})
}

func newMFNativeSourceWithControl(parent context.Context, cfg Config, guard *mfNativeInitializationGuard, control mfNativeSourceControl) (_ *mfNativeSource, resultErr error) {
	if parent == nil {
		parent = context.Background()
	}
	if err := guard.begin(); err != nil {
		return nil, err
	}
	source := &mfNativeSource{
		cfg:     cfg,
		packets: make(chan Packet, cfg.PacketQueueDepth),
		errors:  make(chan error, 2),
		stop:    make(chan struct{}), done: make(chan struct{}), flush: make(chan struct{}),
		packetPool: newPacketPool(cfg.MaxPacketPoolBytes),
		initGuard:  guard,
	}
	defer func() { guard.finish(source, resultErr) }()
	ready := make(chan sourceInit, 1)
	go control(parent, source, ready)
	select {
	case initialized := <-ready:
		if initialized.err != nil {
			return nil, errors.Join(initialized.err, source.stopInitializing(sessionCloseWait(cfg.CloseTimeout)))
		}
		if err := parent.Err(); err != nil {
			return nil, errors.Join(err, source.stopInitializing(sessionCloseWait(cfg.CloseTimeout)))
		}
		return source, nil
	case <-parent.Done():
		return nil, errors.Join(parent.Err(), source.stopInitializing(sessionCloseWait(cfg.CloseTimeout)))
	}
}

func (g *mfNativeInitializationGuard) begin() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.uncertain == nil {
		if g.initializing {
			return errors.New("another Windows camera initialization is already in progress")
		}
		g.initializing = true
		return nil
	}
	select {
	case <-g.uncertain.done:
		g.uncertain = nil
		g.uncertainErr = nil
	default:
		return errors.Join(g.uncertainErr, errors.New("a previous Windows camera initializer still owns native state"))
	}
	if g.initializing {
		return errors.New("another Windows camera initialization is already in progress")
	}
	g.initializing = true
	return nil
}

func (g *mfNativeInitializationGuard) finish(source *mfNativeSource, err error) {
	g.mu.Lock()
	g.initializing = false
	if errors.Is(err, ErrShutdownUncertain) {
		g.retainLocked(source, err)
	}
	g.mu.Unlock()
}

func (g *mfNativeInitializationGuard) remember(source *mfNativeSource, err error) {
	if source == nil || !errors.Is(err, ErrShutdownUncertain) {
		return
	}
	g.mu.Lock()
	g.retainLocked(source, err)
	g.mu.Unlock()
}

func (g *mfNativeInitializationGuard) retainLocked(source *mfNativeSource, err error) {
	if g.uncertain == source {
		g.uncertainErr = err
		return
	}
	g.uncertain = source
	g.uncertainErr = err
	go func() {
		<-source.done
		g.mu.Lock()
		if g.uncertain == source {
			g.uncertain = nil
			g.uncertainErr = nil
		}
		g.mu.Unlock()
	}()
}

func (s *mfNativeSource) Packets() <-chan Packet { return s.packets }
func (s *mfNativeSource) Errors() <-chan error   { return s.errors }
func (s *mfNativeSource) DroppedPackets() uint64 { return s.dropped.Load() }
func (s *mfNativeSource) Mode() Mode             { return s.mode }

func (s *mfNativeSource) Close() error {
	s.signalStop()
	err := s.waitForShutdown(sessionCloseWait(s.cfg.CloseTimeout), "Windows camera shutdown")
	if s.initGuard != nil {
		s.initGuard.remember(s, err)
	}
	return err
}

func (s *mfNativeSource) signalStop() {
	s.closeOnce.Do(func() { close(s.stop) })
}

func (s *mfNativeSource) stopInitializing(wait time.Duration) error {
	s.signalStop()
	return s.waitForShutdown(wait, "Windows camera initialization")
}

func (s *mfNativeSource) waitForShutdown(wait time.Duration, operation string) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-s.done:
		return s.getCloseError()
	case <-timer.C:
		return fmt.Errorf("%w: %s did not stop within %s", ErrShutdownUncertain, operation, wait)
	}
}

func (s *mfNativeSource) getCloseError() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
}

func (s *mfNativeSource) setCloseError(err error) {
	if err == nil {
		return
	}
	s.closeMu.Lock()
	if s.closeErr == nil {
		s.closeErr = err
	}
	s.closeMu.Unlock()
}

func (s *mfNativeSource) report(err error) {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	if err == nil || s.stopping.Load() {
		return
	}
	publishBoundedError(s.errors, err)
}

func (s *mfNativeSource) publish(packet Packet) {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	if s.stopping.Load() {
		packet.Release()
		return
	}
	select {
	case s.packets <- packet:
		return
	default:
	}
	select {
	case old := <-s.packets:
		s.dropped.Add(1)
		old.Release()
	default:
	}
	select {
	case s.packets <- packet:
	default:
		s.dropped.Add(1)
		packet.Release()
	}
}

func (s *mfNativeSource) requestNext() error {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if s.stopping.Load() || !s.reader.valid() {
		return nil
	}
	var started time.Time
	if s.cfg.Diagnostics {
		started = time.Now()
	}
	// Async mode requires every output parameter after control flags to be nil.
	hr := s.reader.call(9, mfSourceReaderFirstVideoStream, 0, 0, 0, 0, 0)
	if s.cfg.Diagnostics {
		elapsed := uint64(time.Since(started))
		s.requestCount.Add(1)
		s.requestTotalNS.Add(elapsed)
		atomicMax(&s.requestMaxNS, elapsed)
	}
	if failed(hr) {
		return hrError("IMFSourceReader.ReadSample(async)", hr)
	}
	return nil
}

func (s *mfNativeSource) control(parent context.Context, ready chan<- sourceInit) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	readySent := false
	readyWith := func(err error) {
		if !readySent {
			ready <- sourceInit{err: err}
			readySent = true
		}
	}
	var (
		shutdownMF           func()
		set                  *activationSet
		activation           mfActivation
		mediaSource          comObject
		reader               comObject
		callback             *mfCallback
		restoreCameraControl func() error
	)
	defer func() {
		s.stopping.Store(true)
		// Reject late COM callbacks and drain callbacks already using this Go
		// owner before releasing the reader, media source, or callback root.
		s.callbacks.closeAndWait()
		s.requestMu.Lock()
		s.reader = comObject{}
		s.requestMu.Unlock()
		if restoreCameraControl != nil {
			s.setCloseError(restoreCameraControl())
		}
		if reader.valid() {
			reader.release()
		}
		if mediaSource.valid() {
			mediaSource.call(12)
			mediaSource.release()
		}
		if activation.object.valid() {
			activation.object.call(34)
		}
		if callback != nil {
			callback.releaseOwner()
		}
		if set != nil {
			set.close()
		}
		if shutdownMF != nil {
			shutdownMF()
		}
		if !readySent {
			readyWith(errors.New("source initialization ended unexpectedly in Media Foundation"))
		}
		s.outputMu.Lock()
		close(s.packets)
		for packet := range s.packets {
			packet.Release()
		}
		s.packetPool.close()
		close(s.errors)
		s.outputMu.Unlock()
		close(s.done)
	}()

	var err error
	shutdownMF, err = startMediaFoundation()
	if err != nil {
		readyWith(err)
		return
	}
	if err = parent.Err(); err != nil {
		readyWith(err)
		return
	}
	set, err = enumerateActivations()
	if err != nil {
		readyWith(err)
		return
	}
	activation, err = set.exact(s.cfg.DeviceID)
	if err != nil {
		readyWith(err)
		return
	}

	var sourcePtr unsafe.Pointer
	hr := activation.object.call(33, uintptr(unsafe.Pointer(&iidIMFMediaSource)), uintptr(unsafe.Pointer(&sourcePtr)))
	if failed(hr) || sourcePtr == nil {
		readyWith(hrError("IMFActivate.ActivateObject(IMFMediaSource)", hr))
		return
	}
	mediaSource = comObject{sourcePtr}

	callback = newMFCallback(s)
	var attrsPtr unsafe.Pointer
	hr, _, _ = procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&attrsPtr)), 2)
	if failed(hr) || attrsPtr == nil {
		readyWith(hrError("MFCreateAttributes(source reader)", hr))
		return
	}
	readerAttrs := comObject{attrsPtr}
	if hr = readerAttrs.call(27, uintptr(unsafe.Pointer(&mfReaderAsyncCallback)), uintptr(unsafe.Pointer(callback))); failed(hr) {
		readerAttrs.release()
		readyWith(hrError("IMFAttributes.SetUnknown(async callback)", hr))
		return
	}
	if hr = readerAttrs.call(21, uintptr(unsafe.Pointer(&mfReaderDisconnect)), 1); failed(hr) {
		readerAttrs.release()
		readyWith(hrError("IMFAttributes.SetUINT32(disconnect on shutdown)", hr))
		return
	}
	var readerPtr unsafe.Pointer
	hr, _, _ = procMFCreateSourceReaderFromMediaSource.Call(uintptr(mediaSource.ptr), uintptr(readerAttrs.ptr), uintptr(unsafe.Pointer(&readerPtr)))
	readerAttrs.release()
	if failed(hr) || readerPtr == nil {
		readyWith(hrError("MFCreateSourceReaderFromMediaSource(async)", hr))
		return
	}
	reader = comObject{readerPtr}
	if hr = reader.call(4, mfSourceReaderAllStreams, 0); failed(hr) {
		readyWith(hrError("IMFSourceReader.SetStreamSelection(all=false)", hr))
		return
	}
	if hr = reader.call(4, mfSourceReaderFirstVideoStream, 1); failed(hr) {
		readyWith(hrError("IMFSourceReader.SetStreamSelection(video=true)", hr))
		return
	}
	selectedType, selectedMode, selectErr := selectBestNativeType(reader, s.cfg)
	if selectErr != nil {
		readyWith(selectErr)
		return
	}
	selectedConfig := s.cfg
	selectedConfig.Width = selectedMode.Width
	selectedConfig.Height = selectedMode.Height
	selectedConfig.FPS = selectedMode.NominalFPS()
	selectedConfig, err = normalize(selectedConfig, true)
	if err != nil {
		selectedType.release()
		readyWith(fmt.Errorf("selected native mode is unusable: %w", err))
		return
	}
	if hr = reader.call(7, mfSourceReaderFirstVideoStream, 0, uintptr(selectedType.ptr)); failed(hr) {
		selectedType.release()
		readyWith(hrError("IMFSourceReader.SetCurrentMediaType(native MJPG)", hr))
		return
	}
	selectedType.release()
	var currentPtr unsafe.Pointer
	if hr = reader.call(6, mfSourceReaderFirstVideoStream, uintptr(unsafe.Pointer(&currentPtr))); failed(hr) || currentPtr == nil {
		readyWith(hrError("IMFSourceReader.GetCurrentMediaType", hr))
		return
	}
	current := comObject{currentPtr}
	negotiated, inspectErr := inspectMediaType(current)
	current.release()
	if inspectErr != nil {
		readyWith(inspectErr)
		return
	}
	if !mediaTypeMatches(negotiated, selectedMode) {
		readyWith(fmt.Errorf("negotiated Media Foundation mode %+v differs from selected native mode %+v", negotiated, selectedMode))
		return
	}
	s.cfg = selectedConfig
	s.mode = selectedMode
	// UVC drivers may reset camera controls while the source reader negotiates
	// its native media type. Apply the reversible cadence/lighting transaction
	// only after the selected type has been installed and verified.
	if s.cfg.AllowVariableFrameRate {
		s.frameRateControl = frameRateControlResult{Method: "disabled by configuration"}
	} else {
		s.frameRateControl, restoreCameraControl = lockCameraFrameRate(mediaSource, activation.device, s.cfg.FPS)
	}

	s.requestMu.Lock()
	s.reader = reader
	s.requestMu.Unlock()
	if err = s.requestNext(); err != nil {
		readyWith(err)
		return
	}
	readyWith(nil)

	select {
	case <-parent.Done():
	case <-s.stop:
	}
	s.stopping.Store(true)
	s.requestMu.Lock()
	flushHR := reader.call(10, mfSourceReaderFirstVideoStream)
	s.requestMu.Unlock()
	if failed(flushHR) {
		s.setCloseError(hrError("IMFSourceReader.Flush", flushHR))
	}

	flushTimeout := s.cfg.CloseTimeout / 2
	timer := time.NewTimer(flushTimeout)
	select {
	case <-s.flush:
	case <-timer.C:
		s.setCloseError(fmt.Errorf("flush callback from Media Foundation timed out after %s", flushTimeout))
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	s.callbacks.closeAndWait()
}

// callbackGate is an admission-controlled in-flight counter. Unlike a
// sync.WaitGroup, begin can race safely with closeAndWait: once closing starts,
// late COM callbacks cannot increment behind the waiter.
type callbackGate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	closed   bool
	inFlight int
}

func (g *callbackGate) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.inFlight++
	return true
}

func (g *callbackGate) end() {
	g.mu.Lock()
	if g.inFlight > 0 {
		g.inFlight--
	}
	if g.closed && g.inFlight == 0 && g.cond != nil {
		g.cond.Broadcast()
	}
	g.mu.Unlock()
}

func (g *callbackGate) closeAndWait() {
	g.mu.Lock()
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
	}
	g.closed = true
	for g.inFlight != 0 {
		g.cond.Wait()
	}
	g.mu.Unlock()
}

type callbackVTable struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	OnReadSample   uintptr
	OnFlush        uintptr
	OnEvent        uintptr
}

type mfCallback struct {
	vtbl  *callbackVTable
	refs  atomic.Uint32
	owner *mfNativeSource
}

var (
	callbackTableOnce  sync.Once
	callbackTableValue callbackVTable
	callbackRootsMu    sync.Mutex
	callbackRoots      = map[*mfCallback]struct{}{}
)

func callbackTable() *callbackVTable {
	callbackTableOnce.Do(func() {
		callbackTableValue = callbackVTable{
			QueryInterface: windows.NewCallback(callbackQueryInterface),
			AddRef:         windows.NewCallback(callbackAddRef),
			Release:        windows.NewCallback(callbackRelease),
			OnReadSample:   windows.NewCallback(callbackOnReadSample),
			OnFlush:        windows.NewCallback(callbackOnFlush),
			OnEvent:        windows.NewCallback(callbackOnEvent),
		}
	})
	return &callbackTableValue
}

func newMFCallback(owner *mfNativeSource) *mfCallback {
	callback := &mfCallback{vtbl: callbackTable(), owner: owner}
	callback.refs.Store(1)
	callbackRootsMu.Lock()
	callbackRoots[callback] = struct{}{}
	callbackRootsMu.Unlock()
	return callback
}

func (c *mfCallback) releaseOwner() { callbackRelease(c) }

func callbackQueryInterface(this *mfCallback, requested *windows.GUID, output *unsafe.Pointer) uintptr {
	if output == nil {
		return hresultPointer
	}
	*output = nil
	if this == nil || requested == nil {
		return hresultPointer
	}
	if *requested != iidIUnknown && *requested != iidIMFSourceReaderCB {
		return hresultNoInterface
	}
	*output = unsafe.Pointer(this)
	callbackAddRef(this)
	return 0
}

func callbackAddRef(this *mfCallback) uintptr {
	if this == nil {
		return 0
	}
	return uintptr(this.refs.Add(1))
}

func callbackRelease(this *mfCallback) uintptr {
	if this == nil {
		return 0
	}
	remaining := this.refs.Add(^uint32(0))
	if remaining == 0 {
		callbackRootsMu.Lock()
		delete(callbackRoots, this)
		callbackRootsMu.Unlock()
	}
	return uintptr(remaining)
}

func callbackOnReadSample(this *mfCallback, status int32, _ uint32, flags uint32, timestamp int64, sample unsafe.Pointer) uintptr {
	if this == nil || this.owner == nil {
		return hresultPointer
	}
	owner := this.owner
	if !owner.callbacks.begin() {
		return 0
	}
	defer owner.callbacks.end()
	if owner.cfg.Diagnostics {
		now := uint64(time.Now().UnixNano())
		previous := owner.callbackLastNS.Swap(now)
		owner.callbackCount.Add(1)
		if previous != 0 && now >= previous {
			gap := now - previous
			owner.callbackGapNS.Add(gap)
			atomicMax(&owner.callbackMaxGapNS, gap)
		}
	}
	if owner.stopping.Load() {
		return 0
	}
	rearm := true
	defer func() {
		if !rearm || owner.stopping.Load() {
			return
		}
		if err := owner.requestNext(); err != nil {
			owner.report(err)
		}
	}()
	statusHR := uintptr(uint32(status))
	if failed(statusHR) {
		rearm = false
		owner.report(hrError("IMFSourceReaderCallback.OnReadSample", statusHR))
		return 0
	}
	if flags&mfSourceReaderFlagError != 0 {
		rearm = false
		owner.report(fmt.Errorf("reader error flag from Media Foundation: %#x", flags))
		return 0
	}
	if flags&mfSourceReaderFlagEOS != 0 {
		rearm = false
		owner.report(errors.New("camera reached end of Media Foundation stream"))
		return 0
	}
	if sample != nil {
		packet, err := copyMFSample(comObject{sample}, timestamp, owner.cfg.MaxPacketBytes, owner.packetPool)
		if err != nil {
			owner.dropped.Add(1)
			owner.report(temporaryCaptureError{err: err})
			return 0
		}
		owner.publish(packet)
	} else if flags&mfSourceReaderFlagStreamTick == 0 {
		owner.dropped.Add(1)
		owner.report(temporaryCaptureError{err: errors.New("neither sample nor stream tick returned by Media Foundation")})
		return 0
	}
	return 0
}

func callbackOnFlush(this *mfCallback, _ uint32) uintptr {
	if this == nil || this.owner == nil {
		return hresultPointer
	}
	owner := this.owner
	if !owner.callbacks.begin() {
		return 0
	}
	defer owner.callbacks.end()
	owner.flushOnce.Do(func() { close(owner.flush) })
	return 0
}

func callbackOnEvent(this *mfCallback, _ uint32, event unsafe.Pointer) uintptr {
	if this == nil || this.owner == nil {
		return hresultPointer
	}
	owner := this.owner
	if !owner.callbacks.begin() {
		return 0
	}
	defer owner.callbacks.end()
	if event == nil || owner.stopping.Load() {
		return 0
	}
	// IMFMediaEvent is borrowed for the duration of this callback. Read its
	// source status in place; do not Release or retain it. Failed event status
	// is terminal even when no OnReadSample callback follows, which prevents a
	// disconnected driver from leaving the preview frozen in a false LIVE state.
	mediaEvent := comObject{event}
	var eventType uint32
	hr := mediaEvent.call(33, uintptr(unsafe.Pointer(&eventType))) // IMFMediaEvent.GetType
	if failed(hr) {
		owner.report(hrError("IMFMediaEvent.GetType", hr))
		return 0
	}
	var status int32
	hr = mediaEvent.call(35, uintptr(unsafe.Pointer(&status))) // IMFMediaEvent.GetStatus
	if failed(hr) {
		owner.report(hrError("IMFMediaEvent.GetStatus", hr))
		return 0
	}
	if err := classifyMediaEvent(eventType, uintptr(uint32(status))); err != nil {
		owner.report(err)
	}
	return 0
}

func classifyMediaEvent(eventType uint32, status uintptr) error {
	if failed(status) {
		return hrError(fmt.Sprintf("Media Foundation source event %d", eventType), status)
	}
	switch eventType {
	case mfEventError:
		return errors.New("camera source error reported by Media Foundation")
	case mfEventVideoCaptureDeviceRemoved:
		return errors.New("camera device was removed")
	case mfEventVideoCaptureDevicePreempted:
		return errors.New("camera was taken over by another application")
	case mfEventNonFatalError:
		return temporaryCaptureError{err: errors.New("recoverable camera event reported by Media Foundation")}
	default:
		return nil
	}
}

func atomicMax(value *atomic.Uint64, candidate uint64) {
	for current := value.Load(); candidate > current; current = value.Load() {
		if value.CompareAndSwap(current, candidate) {
			return
		}
	}
}
