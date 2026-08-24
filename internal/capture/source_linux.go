//go:build linux

package capture

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	linuxDevicePrefix = "v4l2:"
	linuxPollMillis   = 100
)

var errLinuxCapturePrerequisite = errors.New("Linux camera capture requires cgo, pkg-config, and libturbojpeg 2.0 or newer development files")

type linuxDeviceNode struct {
	path string
	id   string
}

type linuxStableLink struct {
	path string
	id   string
}

type linuxDecodeResult struct {
	frame *Frame
	err   error
}

type linuxPlaneLayout struct {
	yWidth, yHeight   int
	yStride, yRows    int
	cbWidth, cbHeight int
	crWidth, crHeight int
}

type linuxPlaneBuffers struct {
	y, cb, cr []byte
}

type linuxPlanarPool struct {
	cfg Config

	mu         sync.Mutex
	configured bool
	layout     linuxPlaneLayout
	free       chan *linuxPlaneBuffers
}

func Enumerate(ctx context.Context) ([]Device, error) {
	if !linuxNativeAvailable() {
		return nil, errLinuxCapturePrerequisite
	}
	if ctx == nil {
		ctx = context.Background()
	}
	nodes, err := discoverLinuxDevices()
	if err != nil {
		return nil, err
	}
	probeConfig := DefaultConfig()
	devices := make([]Device, 0, len(nodes))
	var firstFailure error
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fd, name, bufferType, err := nativeProbe(node.path)
		if err != nil {
			if firstFailure == nil {
				firstFailure = err
			}
			continue
		}
		modes, modeErr := nativeEnumerateModes(fd, bufferType, probeConfig)
		nativeCloseFD(fd)
		if modeErr != nil {
			if firstFailure == nil {
				firstFailure = modeErr
			}
			continue
		}
		if _, ok := chooseLinuxMode(modes, probeConfig); !ok {
			continue
		}
		if strings.TrimSpace(name) == "" {
			name = filepath.Base(node.path)
		}
		name = strings.ToValidUTF8(name, "�")
		devices = append(devices, Device{Name: name, ID: node.id})
	}
	if len(devices) == 0 && firstFailure != nil {
		return nil, firstFailure
	}
	sort.Slice(devices, func(left, right int) bool {
		if devices[left].Name == devices[right].Name {
			return devices[left].ID < devices[right].ID
		}
		return devices[left].Name < devices[right].Name
	})
	makeLinuxDeviceNamesUnique(devices)
	return devices, nil
}

func Open(parent context.Context, cfg Config) (*Session, error) {
	if !linuxNativeAvailable() {
		return nil, errLinuxCapturePrerequisite
	}
	normalized, err := normalize(cfg, true)
	if err != nil {
		return nil, err
	}
	nodes, err := discoverLinuxDevices()
	if err != nil {
		return nil, err
	}
	var node linuxDeviceNode
	found := false
	for _, candidate := range nodes {
		if candidate.id == normalized.DeviceID {
			node, found = candidate, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("camera disconnected: stable device %q is not present", normalized.DeviceID)
	}

	fd, _, bufferType, err := nativeProbe(node.path)
	if err != nil {
		return nil, err
	}
	modes, err := nativeEnumerateModes(fd, bufferType, normalized)
	if err != nil {
		nativeCloseFD(fd)
		return nil, err
	}
	selected, ok := chooseLinuxMode(modes, normalized)
	if !ok {
		nativeCloseFD(fd)
		return nil, errors.New("camera has no usable native MJPEG mode")
	}
	selected, err = nativeConfigure(fd, selected)
	nativeCloseFD(fd)
	if err != nil {
		return nil, err
	}
	if !validCaptureMode(selected.Mode) {
		return nil, errors.New("camera returned an invalid native MJPEG mode")
	}

	selectedConfig := normalized
	selectedConfig.Width = selected.Width
	selectedConfig.Height = selected.Height
	selectedConfig.FPS = selected.NominalFPS()
	selectedConfig, err = normalize(selectedConfig, true)
	if err != nil {
		return nil, fmt.Errorf("selected native mode is unusable: %w", err)
	}
	ready := make(chan error, 1)
	session, err := startDirect(parent, selectedConfig, selected.Mode, func(ctx context.Context, session *Session) error {
		return runLinuxCamera(ctx, session, selectedConfig, node.path, selected, ready)
	})
	if err != nil {
		return nil, err
	}
	waitContext := parent
	if waitContext == nil {
		waitContext = context.Background()
	}
	select {
	case readyErr := <-ready:
		if readyErr != nil {
			return nil, errors.Join(readyErr, session.Close())
		}
		if err := waitContext.Err(); err != nil {
			return nil, errors.Join(err, session.Close())
		}
		return session, nil
	case <-waitContext.Done():
		return nil, errors.Join(waitContext.Err(), session.Close())
	}
}

func runLinuxCamera(ctx context.Context, session *Session, cfg Config, path string, mode linuxNativeMode, ready chan<- error) (returnErr error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	requestedBuffers := cfg.PacketQueueDepth + 2
	if requestedBuffers < 4 {
		requestedBuffers = 4
	}
	if requestedBuffers > 8 {
		requestedBuffers = 8
	}
	stream, actual, err := nativeStart(path, mode, requestedBuffers, cfg.MaxPacketBytes, cfg.MaxPacketPoolBytes)
	if err != nil {
		ready <- err
		return err
	}
	if !sameLinuxMode(actual, mode) {
		_ = stream.close()
		err = fmt.Errorf("camera changed native mode during open: selected %s, received %s", describeLinuxMode(mode.Mode), describeLinuxMode(actual.Mode))
		ready <- err
		return err
	}

	workerContext, stopWorker := context.WithCancel(ctx)
	requests := make(chan Packet, cfg.PacketQueueDepth)
	results := make(chan linuxDecodeResult, 1)
	workerReady := make(chan error, 1)
	workerDone := make(chan struct{})
	pool := &linuxPlanarPool{cfg: cfg}
	go runLinuxDecoder(workerContext, cfg, mode.Mode, pool, requests, results, workerReady, workerDone)
	if err = <-workerReady; err != nil {
		stopWorker()
		close(requests)
		<-workerDone
		_ = stream.close()
		ready <- err
		return err
	}

	wakeStop := make(chan struct{})
	wakeDone := make(chan struct{})
	go func() {
		defer close(wakeDone)
		select {
		case <-ctx.Done():
			_ = stream.wake()
		case <-wakeStop:
		}
	}()
	packetPool := newPacketPool(cfg.MaxPacketPoolBytes)
	defer func() {
		close(wakeStop)
		<-wakeDone
		if closeErr := stream.close(); returnErr == nil && closeErr != nil {
			returnErr = closeErr
		}
		stopWorker()
		close(requests)
		<-workerDone
		for request := range requests {
			request.Release()
		}
		for result := range results {
			if result.frame != nil {
				result.frame.Release()
			}
		}
		packetPool.close()
	}()

	ready <- nil
	started := time.Now()
	lastDecoded := started
	initialFrame := true
	consecutiveDecodeErrors := 0
	sequence := linuxSequenceTracker{}
	timestamps := linuxTimestampTracker{started: started}
	timestampWarning := false

	consumeResult := func(result linuxDecodeResult) error {
		if result.err != nil {
			session.stats.decodeErrors.Add(1)
			consecutiveDecodeErrors++
			if consecutiveDecodeErrors >= cfg.MaxConsecutiveErrors {
				return fmt.Errorf("decode native MJPEG failed %d consecutive times: %w", consecutiveDecodeErrors, result.err)
			}
			session.report(temporaryCaptureError{err: result.err})
			return nil
		}
		if result.frame == nil {
			session.stats.decodeErrors.Add(1)
			consecutiveDecodeErrors++
			if consecutiveDecodeErrors >= cfg.MaxConsecutiveErrors {
				return fmt.Errorf("native MJPEG decoder returned nil %d consecutive times", consecutiveDecodeErrors)
			}
			session.report(temporaryCaptureError{err: errors.New("native MJPEG decoder returned nil")})
			return nil
		}
		consecutiveDecodeErrors = 0
		lastDecoded = time.Now()
		initialFrame = false
		session.acceptFrame(result.frame)
		return nil
	}

	for {
		for {
			select {
			case result, ok := <-results:
				if !ok {
					return errors.New("TurboJPEG decoder stopped unexpectedly")
				}
				if err := consumeResult(result); err != nil {
					return err
				}
			default:
				goto drained
			}
		}
	drained:
		if err := ctx.Err(); err != nil {
			return nil
		}
		watchdog := frameWatchdogTimeout(cfg.FPS, initialFrame)
		if time.Since(lastDecoded) >= watchdog {
			return fmt.Errorf("no decodable camera frame arrived for %s", watchdog)
		}

		sample, status, err := stream.next(linuxPollMillis)
		if err != nil {
			return err
		}
		if status == 2 {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		if status == 1 {
			continue
		}
		session.stats.packets.Add(1)
		if gap := sequence.observe(sample.sequence); gap > 0 {
			session.stats.droppedPackets.Add(uint64(gap))
		}

		var packet Packet
		var sampleErr error
		switch {
		case sample.damaged:
			sampleErr = errors.New("V4L2 marked a camera frame as damaged")
		case sample.bytesUsed < 1 || sample.bytesUsed > cfg.MaxPacketBytes:
			sampleErr = fmt.Errorf("native MJPEG packet length %d is outside 1..%d", sample.bytesUsed, cfg.MaxPacketBytes)
		default:
			packet, sampleErr = packetPool.acquire(sample.bytesUsed)
			if sampleErr == nil {
				stream.copySample(sample, packet.Data)
			}
		}
		if requeueErr := stream.requeue(sample.index); requeueErr != nil {
			packet.Release()
			return requeueErr
		}
		if sampleErr != nil {
			session.stats.droppedPackets.Add(1)
			session.report(temporaryCaptureError{err: sampleErr})
			continue
		}

		pts, fallback := timestamps.observe(sample, time.Now())
		if fallback && cfg.Diagnostics && !timestampWarning {
			timestampWarning = true
			session.report(temporaryCaptureError{err: errors.New("V4L2 capture timestamp was unusable; using the local monotonic clock")})
		}
		packet.PTS = pts
		if enqueueLatestLinuxPacket(ctx, requests, packet, session) {
			continue
		}
		packet.Release()
		return nil
	}
}

func runLinuxDecoder(ctx context.Context, cfg Config, mode Mode, pool *linuxPlanarPool,
	requests <-chan Packet, results chan<- linuxDecodeResult, ready chan<- error, done chan<- struct{}) {
	defer close(done)
	defer close(results)
	decoder, err := newLinuxJPEGDecoder()
	ready <- err
	if err != nil {
		return
	}
	defer decoder.close()
	for {
		select {
		case <-ctx.Done():
			return
		case request, ok := <-requests:
			if !ok {
				return
			}
			frame, decodeErr := decodeLinuxPacket(ctx, decoder, pool, cfg, mode, request)
			request.Release()
			if ctx.Err() != nil {
				if frame != nil {
					frame.Release()
				}
				return
			}
			select {
			case results <- linuxDecodeResult{frame: frame, err: decodeErr}:
			case <-ctx.Done():
				if frame != nil {
					frame.Release()
				}
				return
			}
		}
	}
}

func decodeLinuxPacket(ctx context.Context, decoder *linuxJPEGDecoder, pool *linuxPlanarPool,
	cfg Config, mode Mode, request Packet) (*Frame, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	jpegLayout, err := decoder.layout(request.Data, mode, cfg.Downsample)
	if err != nil {
		return nil, err
	}
	layout := linuxPlaneLayout{
		yWidth: jpegLayout.imageWidth, yHeight: jpegLayout.imageHeight,
		yStride: jpegLayout.yWidth, yRows: jpegLayout.yHeight,
		cbWidth: jpegLayout.cbWidth, cbHeight: jpegLayout.cbHeight,
		crWidth: jpegLayout.crWidth, crHeight: jpegLayout.crHeight,
	}
	buffers, err := pool.acquire(ctx, layout)
	if err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			pool.release(buffers)
		}
	}()
	if err := decoder.decode(request.Data, jpegLayout, buffers); err != nil {
		return nil, err
	}
	frame := &Frame{
		Layout: PixelLayoutPlanarYCbCr,
		Range:  ColorRangeFull,
		Matrix: ColorMatrixBT601,
		Y:      Plane{Pix: buffers.y, Width: layout.yWidth, Height: layout.yHeight, Stride: layout.yStride},
		Cb:     Plane{Pix: buffers.cb, Width: layout.cbWidth, Height: layout.cbHeight, Stride: layout.cbWidth},
		Cr:     Plane{Pix: buffers.cr, Width: layout.crWidth, Height: layout.crHeight, Stride: layout.crWidth},
		PTS:    request.PTS,
		release: func() {
			pool.release(buffers)
		},
	}
	release = false
	return frame, nil
}

func (p *linuxPlanarPool) acquire(ctx context.Context, layout linuxPlaneLayout) (*linuxPlaneBuffers, error) {
	frameBytes, err := linuxLayoutBytes(layout)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if !p.configured {
		if frameBytes > p.cfg.MaxFrameBytes {
			p.mu.Unlock()
			return nil, fmt.Errorf("reduced planar frame requires %d bytes, bound is %d", frameBytes, p.cfg.MaxFrameBytes)
		}
		slots := p.cfg.QueueDepth + 2
		if maximum := p.cfg.MaxPoolBytes / frameBytes; slots > maximum {
			slots = maximum
		}
		if slots < 1 {
			p.mu.Unlock()
			return nil, fmt.Errorf("reduced planar frame requires %d bytes, pool bound is %d", frameBytes, p.cfg.MaxPoolBytes)
		}
		p.layout = layout
		p.free = make(chan *linuxPlaneBuffers, slots)
		for index := 0; index < slots; index++ {
			p.free <- &linuxPlaneBuffers{
				y:  make([]byte, layout.yStride*layout.yRows),
				cb: make([]byte, layout.cbWidth*layout.cbHeight),
				cr: make([]byte, layout.crWidth*layout.crHeight),
			}
		}
		p.configured = true
	} else if p.layout != layout {
		p.mu.Unlock()
		return nil, errors.New("MJPEG planar layout changed during capture")
	}
	free := p.free
	p.mu.Unlock()
	select {
	case buffers := <-free:
		return buffers, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *linuxPlanarPool) release(buffers *linuxPlaneBuffers) {
	if buffers == nil {
		return
	}
	p.mu.Lock()
	free := p.free
	p.mu.Unlock()
	if free != nil {
		free <- buffers
	}
}

func linuxLayoutBytes(layout linuxPlaneLayout) (int, error) {
	dimensions := []int{layout.yWidth, layout.yHeight, layout.yStride, layout.yRows, layout.cbWidth, layout.cbHeight, layout.crWidth, layout.crHeight}
	for _, dimension := range dimensions {
		if dimension < 1 || dimension > maxDimension {
			return 0, errors.New("invalid reduced MJPEG plane geometry")
		}
	}
	if layout.yStride < layout.yWidth || layout.yRows < layout.yHeight {
		return 0, errors.New("invalid padded MJPEG luminance geometry")
	}
	total := int64(layout.yStride)*int64(layout.yRows) +
		int64(layout.cbWidth)*int64(layout.cbHeight) +
		int64(layout.crWidth)*int64(layout.crHeight)
	if total > int64(^uint(0)>>1) {
		return 0, errors.New("reduced MJPEG plane storage overflows int")
	}
	return int(total), nil
}

func linuxPayloadBounds(mappedLength, bytesUsed, dataOffset uint64) (int, int, error) {
	if dataOffset > bytesUsed || bytesUsed > mappedLength {
		return 0, 0, fmt.Errorf("V4L2 returned invalid plane payload bounds: offset %d, bytes used %d, mapped length %d", dataOffset, bytesUsed, mappedLength)
	}
	maximumInt := uint64(^uint(0) >> 1)
	if dataOffset > maximumInt || bytesUsed-dataOffset > maximumInt {
		return 0, 0, errors.New("V4L2 plane payload exceeds native integer bounds")
	}
	return int(dataOffset), int(bytesUsed - dataOffset), nil
}

func enqueueLatestLinuxPacket(ctx context.Context, destination chan Packet, next Packet, session *Session) bool {
	select {
	case destination <- next:
		return true
	default:
	}
	select {
	case previous := <-destination:
		previous.Release()
		session.stats.droppedPackets.Add(1)
	default:
	}
	select {
	case destination <- next:
		return true
	case <-ctx.Done():
		return false
	default:
		next.Release()
		session.stats.droppedPackets.Add(1)
		return true
	}
}

type linuxSequenceTracker struct {
	initialized bool
	previous    uint32
}

func (t *linuxSequenceTracker) observe(sequence uint32) uint32 {
	if !t.initialized {
		t.initialized = true
		t.previous = sequence
		return 0
	}
	difference := sequence - t.previous
	t.previous = sequence
	if difference > 1 && difference < 1<<31 {
		return difference - 1
	}
	return 0
}

type linuxTimestampTracker struct {
	started           time.Time
	sourceInitialized bool
	sourceBase        time.Duration
	previous          time.Duration
	initialized       bool
}

func (t *linuxTimestampTracker) observe(sample linuxNativeSample, now time.Time) (time.Duration, bool) {
	local := now.Sub(t.started)
	candidate := local
	fallback := true
	if sample.monotonic && sample.seconds >= 0 && sample.seconds <= int64((1<<63-1)/time.Second) &&
		sample.microseconds >= 0 && sample.microseconds < 1_000_000 {
		raw := time.Duration(sample.seconds)*time.Second + time.Duration(sample.microseconds)*time.Microsecond
		if !t.sourceInitialized {
			t.sourceInitialized = true
			t.sourceBase = raw
		}
		if raw >= t.sourceBase {
			candidate = raw - t.sourceBase
			fallback = false
		}
	}
	if t.initialized && candidate <= t.previous {
		candidate = t.previous + time.Nanosecond
		fallback = true
	}
	t.initialized = true
	t.previous = candidate
	return candidate, fallback
}

func chooseLinuxMode(modes []linuxNativeMode, cfg Config) (linuxNativeMode, bool) {
	candidates := make([]modeCandidate, 0, len(modes))
	byIndex := make(map[int]linuxNativeMode, len(modes))
	for index, mode := range modes {
		if mode.Format != "MJPG" || !validCaptureMode(mode.Mode) || mode.formatFlag&linuxFormatEmulated != 0 {
			continue
		}
		candidateIndex := index
		candidates = append(candidates, modeCandidate{Mode: mode.Mode, Index: candidateIndex})
		byIndex[candidateIndex] = mode
	}
	target := Mode{Width: cfg.Width, Height: cfg.Height, FPSNumerator: uint32(cfg.FPS), FPSDenominator: 1, Format: "MJPG"}
	selected, ok := chooseBestMode(candidates, target)
	if !ok {
		return linuxNativeMode{}, false
	}
	return byIndex[selected.Index], true
}

func sameLinuxMode(left, right linuxNativeMode) bool {
	return left.Width == right.Width && left.Height == right.Height && left.Format == right.Format &&
		left.bufferType == right.bufferType &&
		uint64(left.FPSNumerator)*uint64(right.FPSDenominator) == uint64(right.FPSNumerator)*uint64(left.FPSDenominator)
}

func describeLinuxMode(mode Mode) string {
	return fmt.Sprintf("%dx%d %s %.3f fps", mode.Width, mode.Height, mode.Format, mode.FPS())
}

func discoverLinuxDevices() ([]linuxDeviceNode, error) {
	paths, err := filepath.Glob("/dev/video*")
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	byID := linuxStableLinks("/dev/v4l/by-id", "by-id")
	byPath := linuxStableLinks("/dev/v4l/by-path", "by-path")
	seen := make(map[string]bool, len(paths))
	nodes := make([]linuxDeviceNode, 0, len(paths))
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode()&os.ModeDevice == 0 {
			continue
		}
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			continue
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		identity, ok := linuxStableIdentity(path, resolved, byID, byPath, linuxSysfsIdentity)
		if !ok {
			continue
		}
		nodes = append(nodes, linuxDeviceNode{path: identity.path, id: identity.id})
	}
	return nodes, nil
}

func linuxStableIdentity(path, resolved string, byID, byPath map[string]linuxStableLink, sysfsIdentity func(string) string) (linuxStableLink, bool) {
	identity := byID[resolved]
	if identity.id == "" {
		identity = byPath[resolved]
	}
	if identity.id != "" {
		return identity, true
	}
	id := sysfsIdentity(path)
	if id == "" {
		return linuxStableLink{}, false
	}
	return linuxStableLink{path: path, id: id}, true
}

func linuxStableLinks(directory, class string) map[string]linuxStableLink {
	links, err := filepath.Glob(filepath.Join(directory, "*"))
	if err != nil {
		return nil
	}
	sort.Strings(links)
	result := make(map[string]linuxStableLink, len(links))
	for _, link := range links {
		resolved, resolveErr := filepath.EvalSymlinks(link)
		if resolveErr != nil || !strings.HasPrefix(filepath.Base(resolved), "video") {
			continue
		}
		if _, exists := result[resolved]; !exists {
			result[resolved] = linuxStableLink{path: link, id: linuxDevicePrefix + class + ":" + filepath.Base(link)}
		}
	}
	return result
}

func linuxSysfsIdentity(path string) string {
	name := filepath.Base(path)
	device, err := filepath.EvalSymlinks(filepath.Join("/sys/class/video4linux", name, "device"))
	if err != nil {
		return ""
	}
	indexBytes, err := os.ReadFile(filepath.Join("/sys/class/video4linux", name, "index"))
	if err != nil {
		return ""
	}
	index, err := strconv.ParseUint(strings.TrimSpace(string(indexBytes)), 10, 32)
	if err != nil {
		return ""
	}
	payload := device + "\x00" + strconv.FormatUint(index, 10)
	return linuxDevicePrefix + "sysfs:" + base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func makeLinuxDeviceNamesUnique(devices []Device) {
	totals := make(map[string]int, len(devices))
	for _, device := range devices {
		totals[device.Name]++
	}
	seen := make(map[string]int, len(totals))
	for index := range devices {
		name := devices[index].Name
		if totals[name] < 2 {
			continue
		}
		seen[name]++
		devices[index].Name = fmt.Sprintf("%s (%d)", name, seen[name])
	}
}
