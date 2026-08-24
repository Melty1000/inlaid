//go:build linux

package capture

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestChooseLinuxModePrefersNativeMJPEG(t *testing.T) {
	cfg := DefaultConfig()
	modes := []linuxNativeMode{
		{Mode: Mode{Width: 1920, Height: 1080, FPSNumerator: 30, FPSDenominator: 1, Format: "MJPG"}, formatFlag: linuxFormatEmulated},
		{Mode: Mode{Width: 1280, Height: 720, FPSNumerator: 30, FPSDenominator: 1, Format: "MJPG"}},
	}
	selected, ok := chooseLinuxMode(modes, cfg)
	if !ok {
		t.Fatal("no mode selected")
	}
	if selected.Width != 1280 || selected.Height != 720 {
		t.Fatalf("selected %dx%d, want native 1280x720", selected.Width, selected.Height)
	}
}

func TestChooseLinuxModeRejectsEmulatedMJPEG(t *testing.T) {
	cfg := DefaultConfig()
	modes := []linuxNativeMode{{
		Mode:       Mode{Width: 1920, Height: 1080, FPSNumerator: 30, FPSDenominator: 1, Format: "MJPG"},
		formatFlag: linuxFormatEmulated,
	}}
	if selected, ok := chooseLinuxMode(modes, cfg); ok {
		t.Fatalf("selected emulated MJPEG mode %+v", selected)
	}
}

func TestLinuxStableIdentityRejectsEphemeralDeviceNumber(t *testing.T) {
	identity, ok := linuxStableIdentity("/dev/video7", "/dev/video7", nil, nil, func(string) string { return "" })
	if ok {
		t.Fatalf("accepted unstable device identity %+v", identity)
	}
}

func TestLinuxStableIdentityUsesPersistentSources(t *testing.T) {
	resolved := "/dev/video2"
	byID := map[string]linuxStableLink{resolved: {path: "/dev/v4l/by-id/camera", id: "v4l2:by-id:camera"}}
	identity, ok := linuxStableIdentity(resolved, resolved, byID, nil, func(string) string { return "v4l2:sysfs:ignored" })
	if !ok || identity != byID[resolved] {
		t.Fatalf("identity = %+v, ok = %t, want by-id identity", identity, ok)
	}

	identity, ok = linuxStableIdentity(resolved, resolved, nil, nil, func(string) string { return "v4l2:sysfs:device" })
	if !ok || identity.path != resolved || identity.id != "v4l2:sysfs:device" {
		t.Fatalf("identity = %+v, ok = %t, want sysfs identity", identity, ok)
	}
}

func TestLinuxSequenceTrackerCountsGapsAcrossWrap(t *testing.T) {
	tracker := linuxSequenceTracker{}
	for _, sequence := range []uint32{^uint32(0) - 1, ^uint32(0), 0, 3} {
		gap := tracker.observe(sequence)
		if sequence == 3 {
			if gap != 2 {
				t.Fatalf("gap = %d, want 2", gap)
			}
		} else if gap != 0 {
			t.Fatalf("gap at %d = %d, want 0", sequence, gap)
		}
	}
}

func TestLinuxTimestampTrackerNormalizesMonotonicCaptureTime(t *testing.T) {
	started := time.Unix(0, 0)
	tracker := linuxTimestampTracker{started: started}
	first, fallback := tracker.observe(linuxNativeSample{monotonic: true, seconds: 100, microseconds: 50}, started)
	if first != 0 || fallback {
		t.Fatalf("first timestamp = %s, fallback %t", first, fallback)
	}
	second, fallback := tracker.observe(linuxNativeSample{monotonic: true, seconds: 100, microseconds: 33_383}, started.Add(33*time.Millisecond))
	if second != 33_333*time.Microsecond || fallback {
		t.Fatalf("second timestamp = %s, fallback %t", second, fallback)
	}
	regressed, fallback := tracker.observe(linuxNativeSample{monotonic: true, seconds: 99}, started.Add(67*time.Millisecond))
	if regressed != 67*time.Millisecond || !fallback {
		t.Fatalf("regressed timestamp = %s, fallback %t", regressed, fallback)
	}
}

func TestLinuxPayloadBoundsExposeOnlyPlanePayload(t *testing.T) {
	offset, length, err := linuxPayloadBounds(4096, 3072, 128)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 128 || length != 2944 {
		t.Fatalf("payload = offset %d length %d, want offset 128 length 2944", offset, length)
	}
}

func TestLinuxPayloadBoundsRejectInvalidDriverValues(t *testing.T) {
	tests := []struct {
		name         string
		mappedLength uint64
		bytesUsed    uint64
		dataOffset   uint64
	}{
		{name: "offset after bytes used", mappedLength: 4096, bytesUsed: 64, dataOffset: 65},
		{name: "bytes used after mapping", mappedLength: 4096, bytesUsed: 4097},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := linuxPayloadBounds(test.mappedLength, test.bytesUsed, test.dataOffset); err == nil {
				t.Fatal("expected invalid payload bounds to fail")
			}
		})
	}
}

func TestMakeLinuxDeviceNamesUnique(t *testing.T) {
	devices := []Device{{Name: "Camera", ID: "one"}, {Name: "Camera", ID: "two"}, {Name: "Capture", ID: "three"}}
	makeLinuxDeviceNamesUnique(devices)
	if devices[0].Name != "Camera (1)" || devices[1].Name != "Camera (2)" || devices[2].Name != "Capture" {
		t.Fatalf("unexpected names: %#v", devices)
	}
}

func TestRealV4L2CaptureAndReopen(t *testing.T) {
	if os.Getenv("INLAID_V4L2_CAPTURE_REAL") != "1" {
		t.Skip("set INLAID_V4L2_CAPTURE_REAL=1 on Linux with an attached V4L2 camera")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	devices, err := Enumerate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) == 0 {
		t.Fatal("no V4L2 camera found")
	}
	device := devices[0]
	if requested := strings.TrimSpace(os.Getenv("INLAID_V4L2_CAPTURE_DEVICE")); requested != "" {
		found := false
		for _, candidate := range devices {
			if candidate.ID == requested || candidate.Name == requested {
				device, found = candidate, true
				break
			}
		}
		if !found {
			t.Fatalf("requested V4L2 camera %q was not found", requested)
		}
	}

	cfg := DefaultConfig()
	cfg.DeviceID = device.ID
	cfg.QueueDepth = 2
	cfg.CloseTimeout = 2 * time.Second
	for attempt := 0; attempt < 2; attempt++ {
		session, err := Open(ctx, cfg)
		if err != nil {
			t.Fatalf("open attempt %d: %v", attempt+1, err)
		}
		var firstPTS, previousPTS time.Duration
		var previousSequence uint64
		errorsCh := session.Errors
		for count := 0; count < 32; {
			select {
			case frame, ok := <-session.Frames:
				if !ok {
					t.Fatal("frame stream closed early")
				}
				if frame.Layout != PixelLayoutPlanarYCbCr || frame.Range != ColorRangeFull || frame.Matrix != ColorMatrixBT601 ||
					frame.Y.Width < 2 || frame.Y.Height < 2 || len(frame.Y.Pix) == 0 || len(frame.Cb.Pix) == 0 || len(frame.Cr.Pix) == 0 {
					frame.Release()
					t.Fatalf("invalid V4L2 frame metadata: %+v", frame)
				}
				if count > 0 && (frame.Sequence <= previousSequence || frame.PTS < previousPTS) {
					frame.Release()
					t.Fatalf("non-monotonic frame: sequence=%d PTS=%s", frame.Sequence, frame.PTS)
				}
				if count == 0 {
					firstPTS = frame.PTS
				}
				previousSequence, previousPTS = frame.Sequence, frame.PTS
				frame.Release()
				count++
			case captureErr, ok := <-errorsCh:
				if !ok {
					errorsCh = nil
					continue
				}
				if captureErr != nil && !IsTemporary(captureErr) {
					t.Fatal(captureErr)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		elapsed := previousPTS - firstPTS
		if elapsed <= 0 {
			t.Fatal("V4L2 timestamps did not advance")
		}
		observedFPS := 31 / elapsed.Seconds()
		if minimum := session.Mode().FPS() * 0.8; observedFPS < minimum {
			t.Fatalf("observed %.1f fps, want at least %.1f fps for selected mode %+v", observedFPS, minimum, session.Mode())
		}
		if err := session.Close(); err != nil {
			t.Fatalf("close attempt %d: %v", attempt+1, err)
		}
	}
}
