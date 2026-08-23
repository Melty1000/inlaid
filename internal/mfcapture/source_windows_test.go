//go:build windows

package mfcapture

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestRealC922ControlInventory(t *testing.T) {
	if os.Getenv("INLAID_MF_CAPTURE_REAL") != "1" {
		t.Skip("set INLAID_MF_CAPTURE_REAL=1 to exercise the attached camera")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	shutdown, err := startMediaFoundation()
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()
	set, err := enumerateActivations()
	if err != nil {
		t.Fatal(err)
	}
	defer set.close()
	var selected mfActivation
	for _, candidate := range set.items {
		if strings.Contains(strings.ToLower(candidate.device.Name), "c922") {
			selected = candidate
			break
		}
	}
	if selected.device.ID == "" {
		t.Fatalf("C922 not found: %+v", set.items)
	}
	var sourcePtr unsafe.Pointer
	if hr := selected.object.call(33, uintptr(unsafe.Pointer(&iidIMFMediaSource)), uintptr(unsafe.Pointer(&sourcePtr))); failed(hr) || sourcePtr == nil {
		t.Fatal(hrError("IMFActivate.ActivateObject(IMFMediaSource)", hr))
	}
	source := comObject{sourcePtr}
	defer func() {
		source.call(12)
		source.release()
		selected.object.call(34)
	}()

	logProperties := func(label string, iid *windows.GUID, names []string) {
		var pointer unsafe.Pointer
		if hr := source.call(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&pointer))); failed(hr) || pointer == nil {
			t.Logf("%s unavailable: %v", label, hrError("QueryInterface", hr))
			return
		}
		control := comObject{pointer}
		defer control.release()
		for property, name := range names {
			var minimum, maximum, step, defaultValue, caps, value, flags int32
			hr := control.call(3, uintptr(property), uintptr(unsafe.Pointer(&minimum)), uintptr(unsafe.Pointer(&maximum)), uintptr(unsafe.Pointer(&step)), uintptr(unsafe.Pointer(&defaultValue)), uintptr(unsafe.Pointer(&caps)))
			if failed(hr) {
				t.Logf("%s.%s unavailable: %#x", label, name, uint32(hr))
				continue
			}
			hr = control.call(5, uintptr(property), uintptr(unsafe.Pointer(&value)), uintptr(unsafe.Pointer(&flags)))
			if failed(hr) {
				t.Logf("%s.%s range=%d..%d step=%d default=%d caps=%#x current unavailable=%#x", label, name, minimum, maximum, step, defaultValue, caps, uint32(hr))
				continue
			}
			t.Logf("%s.%s range=%d..%d step=%d default=%d caps=%#x current=%d flags=%#x", label, name, minimum, maximum, step, defaultValue, caps, value, flags)
		}
	}
	logProperties("camera", &iidIAMCameraControl, []string{"pan", "tilt", "roll", "zoom", "exposure", "iris", "focus", "scan-mode", "privacy", "pan-tilt", "pan-relative", "tilt-relative", "roll-relative", "zoom-relative", "exposure-relative", "iris-relative", "focus-relative", "pan-tilt-relative", "focal-length", "auto-exposure-priority"})
	logProperties("procamp", &iidIAMVideoProcAmp, []string{"brightness", "contrast", "hue", "saturation", "sharpness", "gamma", "color-enable", "white-balance", "backlight-compensation", "gain"})
}

func TestCallbackIUnknownOwnership(t *testing.T) {
	owner := &mfNativeSource{}
	callback := newMFCallback(owner)
	if got := callback.refs.Load(); got != 1 {
		t.Fatalf("initial refs = %d", got)
	}
	var output unsafe.Pointer
	if hr := callbackQueryInterface(callback, &iidIMFSourceReaderCB, &output); hr != 0 {
		t.Fatalf("QueryInterface HRESULT = %#x", hr)
	}
	if output != unsafe.Pointer(callback) {
		t.Fatal("QueryInterface returned another object")
	}
	if got := callback.refs.Load(); got != 2 {
		t.Fatalf("refs after QI = %d", got)
	}
	if got := callbackRelease(callback); got != 1 {
		t.Fatalf("refs after COM release = %d", got)
	}
	callback.releaseOwner()
	callbackRootsMu.Lock()
	_, rooted := callbackRoots[callback]
	callbackRootsMu.Unlock()
	if rooted {
		t.Fatal("zero-ref callback remained rooted")
	}
}

func TestCallbackGateRejectsLateAdmissionAndDrainsInflight(t *testing.T) {
	var gate callbackGate
	if !gate.begin() {
		t.Fatal("fresh callback gate rejected admission")
	}
	drained := make(chan struct{})
	go func() {
		gate.closeAndWait()
		close(drained)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		gate.mu.Lock()
		closed := gate.closed
		gate.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("callback gate did not begin closing")
		}
		time.Sleep(time.Millisecond)
	}
	if gate.begin() {
		t.Fatal("callback gate admitted work after closing began")
	}
	select {
	case <-drained:
		t.Fatal("callback gate returned while work was in flight")
	default:
	}
	gate.end()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("callback gate did not drain completed work")
	}
}

func TestNativeSourceLatestQueueReleasesDisplacedPackets(t *testing.T) {
	pool := newPacketPool(16 << 10)
	source := &mfNativeSource{packets: make(chan Packet, 1), packetPool: pool}
	first, err := pool.acquire(100)
	if err != nil {
		t.Fatal(err)
	}
	firstOwner := first.owner
	second, err := pool.acquire(100)
	if err != nil {
		t.Fatal(err)
	}
	source.publish(first)
	source.publish(second)
	if token := firstOwner.token.Load(); token != 0 {
		t.Fatalf("displaced packet retained token %d", token)
	}
	(<-source.packets).Release()
	source.stopping.Store(true)
	stopped, err := pool.acquire(100)
	if err != nil {
		t.Fatal(err)
	}
	stoppedOwner := stopped.owner
	source.publish(stopped)
	if token := stoppedOwner.token.Load(); token != 0 {
		t.Fatalf("stopped source retained rejected packet token %d", token)
	}
}

func TestPlanarPoolIsBoundedAndReusesBuffers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QueueDepth = 1
	cfg.MaxPoolBytes = 1 << 20
	pool := planarPool{cfg: cfg}
	layout := frameLayout{
		{format: wicPixelFormatY, width: 16, height: 8, stride: 16, size: 128},
		{format: wicPixelFormatCb, width: 8, height: 8, stride: 8, size: 64},
		{format: wicPixelFormatCr, width: 8, height: 8, stride: 8, size: 64},
	}
	ctx := context.Background()
	stop := make(chan struct{})
	first, err := pool.acquire(ctx, stop, layout)
	if err != nil {
		t.Fatal(err)
	}
	address := unsafe.Pointer(&first[0][0])
	held := make([]*pooledBuffers, 0, cap(pool.free)-1)
	for len(held) < cap(pool.free)-1 {
		buffers, acquireErr := pool.acquire(ctx, stop, layout)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		held = append(held, buffers)
	}
	pool.release(first)
	second, err := pool.acquire(ctx, stop, layout)
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.Pointer(&second[0][0]) != address {
		t.Fatal("pool did not reuse released plane storage")
	}
	pool.release(second)
	for _, buffers := range held {
		pool.release(buffers)
	}
}

func TestExposureForFPS(t *testing.T) {
	if got := exposureForFPS(-11, -2, 1, -5, cameraControlFlagAuto, 30); got != -5 {
		t.Fatalf("30 fps exposure = %d, want -5", got)
	}
	if got := exposureForFPS(-11, -2, 1, -5, cameraControlFlagAuto, 60); got != -6 {
		t.Fatalf("60 fps exposure = %d, want -6", got)
	}
	if got := exposureForFPS(-11, -2, 1, -7, cameraControlFlagManual, 30); got != -7 {
		t.Fatalf("existing shorter manual exposure = %d, want -7", got)
	}
	if got := exposureForFPS(-11, -2, 2, -5, cameraControlFlagAuto, 30); got != -5 {
		t.Fatalf("stepped 30 fps exposure = %d, want -5", got)
	}
}

func TestMediaTypeVerificationUsesSelectedRationalMode(t *testing.T) {
	selected := Mode{Width: 1920, Height: 1080, FPSNumerator: 30000, FPSDenominator: 1001, Format: "MJPG"}
	if !mediaTypeMatches(selected, selected) {
		t.Fatal("selected rational mode did not verify against itself")
	}
	requestedInteger := Mode{Width: 1920, Height: 1080, FPSNumerator: 30, FPSDenominator: 1, Format: "MJPG"}
	if mediaTypeMatches(selected, requestedInteger) {
		t.Fatal("verification silently replaced the selected rational rate with requested integer fps")
	}
	wrongGeometry := selected
	wrongGeometry.Width = 1280
	if mediaTypeMatches(selected, wrongGeometry) {
		t.Fatal("verification accepted the wrong selected geometry")
	}
}

func TestGainFloor(t *testing.T) {
	for _, test := range []struct {
		minimum, maximum, step, current, want int32
	}{
		{0, 255, 1, 4, 64},
		{0, 255, 1, 96, 96},
		{10, 110, 5, 10, 35},
		{-20, 20, 2, -20, -10},
	} {
		if got := gainFloor(test.minimum, test.maximum, test.step, test.current); got != test.want {
			t.Fatalf("gainFloor(%d, %d, %d, %d) = %d, want %d", test.minimum, test.maximum, test.step, test.current, got, test.want)
		}
	}
}

func TestCadenceGainSupportIsC922Specific(t *testing.T) {
	if !needsCadenceGainSupport(Device{Name: "c922 Pro Stream Webcam"}) ||
		!needsCadenceGainSupport(Device{ID: `\\?\usb#vid_046d&pid_085c#example`}) {
		t.Fatal("C922 was not recognized")
	}
	if needsCadenceGainSupport(Device{Name: "Generic Webcam", ID: `\\?\usb#vid_1234&pid_5678`}) {
		t.Fatal("gain compensation leaked to an unmeasured camera")
	}
	if !allowsManualExposureFallback(Device{Name: "C922 Pro Stream Webcam"}) {
		t.Fatal("C922 manual exposure fallback was not allowlisted")
	}
	if allowsManualExposureFallback(Device{Name: "Generic Webcam", ID: `\\?\usb#vid_1234&pid_5678`}) {
		t.Fatal("manual exposure fallback leaked to an unmeasured camera")
	}
}

func TestRealC922AsyncCaptureAndClose(t *testing.T) {
	if os.Getenv("INLAID_MF_CAPTURE_REAL") != "1" {
		t.Skip("set INLAID_MF_CAPTURE_REAL=1 to exercise the attached camera")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	devices, err := Enumerate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var selected Device
	for _, device := range devices {
		if strings.Contains(strings.ToLower(device.Name), "c922") {
			selected = device
			break
		}
	}
	if selected.ID == "" {
		t.Fatalf("C922 not found: %+v", devices)
	}
	beforePriority := testReadC922Control(t, &iidIAMCameraControl, cameraControlAutoExposurePriority)
	beforeExposure := testReadC922Control(t, &iidIAMCameraControl, cameraControlExposure)
	beforeGain := testReadC922Control(t, &iidIAMVideoProcAmp, videoProcAmpGain)
	cfg := DefaultConfig()
	cfg.DeviceID = selected.ID
	cfg.QueueDepth = 2
	cfg.CloseTimeout = 2 * time.Second
	callbackRootsMu.Lock()
	rootBaseline := len(callbackRoots)
	callbackRootsMu.Unlock()
	session, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	firstControl := session.source.(*mfNativeSource).frameRateControl
	var previous int64
	transientErrors := 0
	frames := session.Frames
	errorsCh := session.Errors
	for index := 0; index < 6; index++ {
		var frame *Frame
		for frame == nil {
			select {
			case candidate, ok := <-frames:
				if !ok {
					t.Fatal("frame channel closed early")
				}
				frame = candidate
			case captureErr, ok := <-errorsCh:
				if !ok {
					errorsCh = nil
					continue
				}
				transientErrors++
				t.Logf("transient capture/decode error: %v", captureErr)
				if transientErrors > 8 {
					t.Fatalf("too many transient errors: %v", captureErr)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		if frame.Y.Width != 480 || frame.Y.Height != 270 || frame.Cb.Width != 240 || frame.Cb.Height != 270 || frame.Cr.Width != 240 || frame.Cr.Height != 270 {
			t.Fatalf("unexpected planes: Y=%+v Cb=%+v Cr=%+v", frame.Y, frame.Cb, frame.Cr)
		}
		if index > 0 && frame.ReaderTimestamp100ns <= previous {
			t.Fatalf("timestamp %d did not follow %d", frame.ReaderTimestamp100ns, previous)
		}
		previous = frame.ReaderTimestamp100ns
		frame.Release()
	}
	closeStart := time.Now()
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(closeStart); elapsed > 3*time.Second {
		t.Fatalf("Close took %s", elapsed)
	}
	stats := session.Stats()
	if stats.Decoded < 6 {
		t.Fatalf("stats = %+v", stats)
	}
	callbackRootsMu.Lock()
	rootsAfterClose := len(callbackRoots)
	callbackRootsMu.Unlock()
	if rootsAfterClose != rootBaseline {
		t.Fatalf("callback roots after Close = %d, baseline %d", rootsAfterClose, rootBaseline)
	}
	if after := testReadC922Control(t, &iidIAMCameraControl, cameraControlAutoExposurePriority); after != beforePriority {
		t.Fatalf("auto-exposure priority after Close = %+v, want %+v", after, beforePriority)
	}
	if after := testReadC922Control(t, &iidIAMCameraControl, cameraControlExposure); after != beforeExposure {
		t.Fatalf("exposure after Close = %+v, want %+v", after, beforeExposure)
	}
	if after := testReadC922Control(t, &iidIAMVideoProcAmp, videoProcAmpGain); after != beforeGain {
		t.Fatalf("gain after Close = %+v, want %+v", after, beforeGain)
	}

	// Reopening the same stable identity proves the first session relinquished
	// the camera, reader, activation, and callback ownership.
	second, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen after Close: %v", err)
	}
	secondControl := second.source.(*mfNativeSource).frameRateControl
	if firstControl.Applied && firstControl.Method == secondControl.Method && (secondControl.BeforeValue != firstControl.BeforeValue || secondControl.BeforeFlags != firstControl.BeforeFlags) {
		t.Fatalf("camera control was not restored before reopen: first=%+v second=%+v", firstControl, secondControl)
	}
	var reopenedFrame *Frame
	reopenedFrames := second.Frames
	reopenedErrors := second.Errors
	for reopenedFrame == nil {
		select {
		case candidate, ok := <-reopenedFrames:
			if !ok {
				t.Fatal("reopened frame channel closed early")
			}
			reopenedFrame = candidate
		case captureErr, ok := <-reopenedErrors:
			if !ok {
				reopenedErrors = nil
				continue
			}
			t.Logf("transient reopen error: %v", captureErr)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	reopenedFrame.Release()
	if err := second.Close(); err != nil {
		t.Fatalf("close reopened session: %v", err)
	}
}

func TestRealC922SustainedHalfDecodeCadence(t *testing.T) {
	if os.Getenv("INLAID_MF_CAPTURE_REAL") != "1" {
		t.Skip("set INLAID_MF_CAPTURE_REAL=1 to exercise the attached camera")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	devices, err := Enumerate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var selected Device
	for _, device := range devices {
		if strings.Contains(strings.ToLower(device.Name), "c922") {
			selected = device
			break
		}
	}
	if selected.ID == "" {
		t.Fatalf("C922 not found: %+v", devices)
	}
	cfg := DefaultConfig()
	cfg.DeviceID = selected.ID
	cfg.Lowres = 1
	session, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	native := session.source.(*mfNativeSource)
	wic := session.decoder.(*wicDecoder)
	frames, errorsCh := session.Frames, session.Errors
	var (
		count                         int
		firstWall, lastWall           time.Time
		firstTimestamp, lastTimestamp int64
		previousTimestamp             int64
		minimumTimestampGap           = int64(^uint64(0) >> 1)
		maximumTimestampGap           int64
	)
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
loop:
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatal("frame channel closed during cadence run")
			}
			now := time.Now()
			if count == 0 {
				firstWall, firstTimestamp = now, frame.ReaderTimestamp100ns
			} else {
				gap := frame.ReaderTimestamp100ns - previousTimestamp
				if gap < minimumTimestampGap {
					minimumTimestampGap = gap
				}
				if gap > maximumTimestampGap {
					maximumTimestampGap = gap
				}
			}
			count++
			lastWall, lastTimestamp, previousTimestamp = now, frame.ReaderTimestamp100ns, frame.ReaderTimestamp100ns
			// Match the observed 6-8 ms cell reduction/solve cost while retaining
			// overlap with the native source and WIC decoder goroutines.
			time.Sleep(8 * time.Millisecond)
			frame.Release()
		case captureErr, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
				continue
			}
			t.Logf("capture/decode event: %v", captureErr)
			if !IsTemporary(captureErr) {
				t.Fatal(captureErr)
			}
		case <-deadline.C:
			break loop
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	stats := session.Stats()
	wallFPS, timestampFPS := 0.0, 0.0
	if count > 1 {
		wallFPS = float64(count-1) / lastWall.Sub(firstWall).Seconds()
		if lastTimestamp > firstTimestamp {
			timestampFPS = float64(count-1) * 1e7 / float64(lastTimestamp-firstTimestamp)
		}
	}
	callbacks := native.callbackCount.Load()
	requests := native.requestCount.Load()
	callbackAverage, requestAverage := time.Duration(0), time.Duration(0)
	if callbacks > 1 {
		callbackAverage = time.Duration(native.callbackGapNS.Load() / (callbacks - 1))
	}
	if requests > 0 {
		requestAverage = time.Duration(native.requestTotalNS.Load() / requests)
	}
	decodes := wic.decodeCount.Load()
	decodeAverage := time.Duration(0)
	if decodes > 0 {
		decodeAverage = time.Duration(wic.decodeTotalNS.Load() / decodes)
	}
	t.Logf("delivered=%d wall_fps=%.2f timestamp_fps=%.2f pts_gap=%s..%s stats=%+v", count, wallFPS, timestampFPS, time.Duration(minimumTimestampGap*100), time.Duration(maximumTimestampGap*100), stats)
	t.Logf("frame_rate_control=%+v", native.frameRateControl)
	t.Logf("callbacks=%d callback_gap_avg=%s max=%s requests=%d request_avg=%s max=%s decodes=%d decode_avg=%s max=%s", callbacks, callbackAverage, time.Duration(native.callbackMaxGapNS.Load()), requests, requestAverage, time.Duration(native.requestMaxNS.Load()), decodes, decodeAverage, time.Duration(wic.decodeMaxNS.Load()))
	if wallFPS < 28 {
		t.Fatalf("delivered cadence %.2f fps is not close enough to the selected 30 fps mode", wallFPS)
	}
}

// TestRealC922ThreeMinuteSoak is intentionally opt-in and long-running. It
// catches device/driver failures that appear well after the short cadence
// acceptance gate while exercising the same quarter-decode path as the app.
func TestRealC922ThreeMinuteSoak(t *testing.T) {
	if os.Getenv("INLAID_MF_CAPTURE_SOAK") != "1" {
		t.Skip("set INLAID_MF_CAPTURE_SOAK=1 to run the real three-minute soak")
	}
	const duration = 3 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), duration+15*time.Second)
	defer cancel()
	devices, err := Enumerate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var selected Device
	for _, device := range devices {
		if strings.Contains(strings.ToLower(device.Name), "c922") {
			selected = device
			break
		}
	}
	if selected.ID == "" {
		t.Fatalf("C922 not found: %+v", devices)
	}
	cfg := DefaultConfig()
	cfg.DeviceID = selected.ID
	session, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	started := time.Now()
	windowStarted := started
	deadline := time.NewTimer(duration)
	ticker := time.NewTicker(10 * time.Second)
	defer deadline.Stop()
	defer ticker.Stop()
	var frames, windowFrames, temporaryErrors int
	var firstTimestamp, lastTimestamp int64
	minimumWindowFPS := 1e9
	firstMean, lastMean := 0.0, 0.0
	for {
		select {
		case frame, ok := <-session.Frames:
			if !ok {
				t.Fatalf("frame channel closed after %s", time.Since(started))
			}
			if frames == 0 {
				firstTimestamp = frame.ReaderTimestamp100ns
				firstMean = sampledPlaneMean(frame.Y.Pix)
			}
			if lastTimestamp != 0 && frame.ReaderTimestamp100ns <= lastTimestamp {
				frame.Release()
				t.Fatalf("timestamp %d did not follow %d", frame.ReaderTimestamp100ns, lastTimestamp)
			}
			lastTimestamp = frame.ReaderTimestamp100ns
			lastMean = sampledPlaneMean(frame.Y.Pix)
			frames++
			windowFrames++
			frame.Release()
		case captureErr, ok := <-session.Errors:
			if !ok {
				t.Fatalf("error channel closed after %s", time.Since(started))
			}
			if !IsTemporary(captureErr) {
				t.Fatalf("terminal capture error after %s: %v", time.Since(started), captureErr)
			}
			temporaryErrors++
			t.Logf("temporary capture event after %s: %v", time.Since(started), captureErr)
		case now := <-ticker.C:
			windowFPS := float64(windowFrames) / now.Sub(windowStarted).Seconds()
			if windowFPS < minimumWindowFPS {
				minimumWindowFPS = windowFPS
			}
			t.Logf("soak t=%s frames=%d window_fps=%.2f Y_mean=%.1f temporary_errors=%d", now.Sub(started).Round(time.Second), frames, windowFPS, lastMean, temporaryErrors)
			if windowFPS < 28 {
				t.Fatalf("ten-second cadence %.2f fps is not close enough to 30", windowFPS)
			}
			windowStarted, windowFrames = now, 0
		case <-deadline.C:
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			timestampFPS := float64(frames-1) * 1e7 / float64(lastTimestamp-firstTimestamp)
			t.Logf("soak passed duration=%s frames=%d source_fps=%.2f min_10s_fps=%.2f Y_mean_first=%.1f last=%.1f temporary_errors=%d stats=%+v", time.Since(started), frames, timestampFPS, minimumWindowFPS, firstMean, lastMean, temporaryErrors, session.Stats())
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func sampledPlaneMean(pixels []byte) float64 {
	var sum uint64
	var count int
	for index := 0; index < len(pixels); index += 16 {
		sum += uint64(pixels[index])
		count++
	}
	if count == 0 {
		return 0
	}
	return float64(sum) / float64(count)
}
