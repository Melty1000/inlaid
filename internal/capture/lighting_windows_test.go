//go:build windows

package capture

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type testControlValue struct {
	value int32
	flags int32
}

func TestRealC922LightingStrategies(t *testing.T) {
	if os.Getenv("INLAID_MF_CAPTURE_REAL") != "1" {
		t.Skip("set INLAID_MF_CAPTURE_REAL=1 to exercise the attached camera")
	}
	originalExposure := testReadC922Control(t, &iidIAMCameraControl, cameraControlExposure)
	originalGain := testReadC922Control(t, &iidIAMVideoProcAmp, videoProcAmpGain)
	defer func() {
		testWriteC922Control(t, &iidIAMCameraControl, cameraControlExposure, originalExposure)
		testWriteC922Control(t, &iidIAMVideoProcAmp, videoProcAmpGain, originalGain)
	}()

	// Let the camera's own metering converge first. Log one-second windows so
	// the settling curve and cadence cost are both visible.
	testWriteC922Control(t, &iidIAMCameraControl, cameraControlExposure, testControlValue{value: -5, flags: cameraControlFlagAuto})
	testCaptureLighting(t, "auto", 7*time.Second)
	autoExposure := testReadC922Control(t, &iidIAMCameraControl, cameraControlExposure)
	autoGain := testReadC922Control(t, &iidIAMVideoProcAmp, videoProcAmpGain)
	t.Logf("auto converged exposure=%+v gain=%+v", autoExposure, autoGain)

	// Freeze at the cadence-safe exposure while retaining the gain found by
	// auto metering. This is the least invasive hardware-only candidate.
	testWriteC922Control(t, &iidIAMCameraControl, cameraControlExposure, testControlValue{value: -5, flags: cameraControlFlagManual})
	testCaptureLighting(t, fmt.Sprintf("manual-5 gain-%d", autoGain.value), 4*time.Second)

	// Bracket practical gains in case C922 auto exposure does not raise the
	// separately exposed IAMVideoProcAmp gain while it meters.
	for _, gain := range []int32{32, 64, 128, 192, 255} {
		testWriteC922Control(t, &iidIAMVideoProcAmp, videoProcAmpGain, testControlValue{value: gain, flags: cameraControlFlagManual})
		testCaptureLighting(t, fmt.Sprintf("manual-5 gain-%d", gain), 3*time.Second)
	}
	for _, gain := range []int32{128, 192, 255} {
		testWriteC922Control(t, &iidIAMVideoProcAmp, videoProcAmpGain, testControlValue{value: gain, flags: cameraControlFlagManual})
		testWriteC922Control(t, &iidIAMCameraControl, cameraControlExposure, testControlValue{value: -5, flags: cameraControlFlagAuto})
		testCaptureLighting(t, fmt.Sprintf("auto gain-%d", gain), 4*time.Second)
	}
}

func testCaptureLighting(t *testing.T, label string, duration time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), duration+5*time.Second)
	defer cancel()
	devices, err := Enumerate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, device := range devices {
		if strings.Contains(strings.ToLower(device.Name), "c922") {
			id = device.ID
			break
		}
	}
	if id == "" {
		t.Fatalf("C922 not found: %+v", devices)
	}
	cfg := DefaultConfig()
	cfg.DeviceID = id
	cfg.AllowVariableFrameRate = true
	session, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	started := time.Now()
	windowStart := started
	var firstWall, lastWall time.Time
	var firstPTS, lastPTS time.Duration
	var frames, windowFrames int
	var ySum, yCount uint64
	var histogram [256]uint64
	flush := func(now time.Time) {
		if windowFrames == 0 {
			return
		}
		p := func(numerator uint64) int {
			target := (yCount*numerator + 99) / 100
			var cumulative uint64
			for value, count := range histogram {
				cumulative += count
				if cumulative >= target {
					return value
				}
			}
			return 255
		}
		windowFPS := float64(windowFrames) / now.Sub(windowStart).Seconds()
		t.Logf("%s t=%.1fs fps=%.2f Y mean=%.1f p10=%d p50=%d p90=%d p99=%d", label, now.Sub(started).Seconds(), windowFPS, float64(ySum)/float64(yCount), p(10), p(50), p(90), p(99))
		windowStart, windowFrames, ySum, yCount = now, 0, 0, 0
		clear(histogram[:])
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case frame, ok := <-session.Frames:
			if !ok {
				t.Fatal("camera frame channel closed")
			}
			now := time.Now()
			if frames == 0 {
				firstWall, firstPTS = now, frame.PTS
			}
			frames++
			windowFrames++
			lastWall, lastPTS = now, frame.PTS
			for index := 0; index < len(frame.Y.Pix); index += 4 {
				value := frame.Y.Pix[index]
				histogram[value]++
				ySum += uint64(value)
				yCount++
			}
			frame.Release()
		case captureErr, ok := <-session.Errors:
			if ok {
				t.Logf("%s capture event: %v", label, captureErr)
				if !IsTemporary(captureErr) {
					t.Fatal(captureErr)
				}
			}
		case now := <-ticker.C:
			flush(now)
		case <-timer.C:
			flush(time.Now())
			wallFPS, timestampFPS := 0.0, 0.0
			if frames > 1 {
				wallFPS = float64(frames-1) / lastWall.Sub(firstWall).Seconds()
				timestampFPS = float64(frames-1) / (lastPTS - firstPTS).Seconds()
			}
			t.Logf("%s total frames=%d wall=%.2f source=%.2f stats=%+v", label, frames, wallFPS, timestampFPS, session.Stats())
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func testReadC922Control(t *testing.T, iid *windows.GUID, property int) testControlValue {
	t.Helper()
	var result testControlValue
	testWithC922Control(t, iid, func(control comObject) {
		if hr := control.call(5, uintptr(property), uintptr(unsafe.Pointer(&result.value)), uintptr(unsafe.Pointer(&result.flags))); failed(hr) {
			t.Fatal(hrError("camera control Get", hr))
		}
	})
	return result
}

func testWriteC922Control(t *testing.T, iid *windows.GUID, property int, value testControlValue) {
	t.Helper()
	testWithC922Control(t, iid, func(control comObject) {
		if hr := control.call(4, uintptr(property), uintptr(uint32(value.value)), uintptr(uint32(value.flags))); failed(hr) {
			t.Fatal(hrError("camera control Set", hr))
		}
	})
}

func testWithC922Control(t *testing.T, iid *windows.GUID, use func(comObject)) {
	t.Helper()
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
		t.Fatal("C922 not found")
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
	var pointer unsafe.Pointer
	if hr := source.call(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&pointer))); failed(hr) || pointer == nil {
		t.Fatal(hrError("camera control QueryInterface", hr))
	}
	control := comObject{pointer}
	defer control.release()
	use(control)
}
