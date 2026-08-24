//go:build darwin && cgo

package capture

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestAVFModeChoicePreservesNTSCAndPrefersMJPEGOnTies(t *testing.T) {
	cfg := DefaultConfig()
	ranges := []avfModeRange{
		{
			FormatIndex: 0, Width: 1920, Height: 1080, Format: "2vuy",
			MinimumValue: 1001, MinimumTimescale: 30000,
			MaximumValue: 1, MaximumTimescale: 15,
		},
		{
			FormatIndex: 4, Width: 1920, Height: 1080, Format: "mjpg",
			MinimumValue: 1001, MinimumTimescale: 30000,
			MaximumValue: 1, MaximumTimescale: 15,
		},
	}
	choice, ok := chooseAVFMode(ranges, cfg)
	if !ok {
		t.Fatal("no AVFoundation mode selected")
	}
	if choice.formatIndex != 4 || choice.Mode.Format != "mjpg" {
		t.Fatalf("selected mode = %+v at format %d", choice.Mode, choice.formatIndex)
	}
	if choice.Mode.FPSNumerator != 30000 || choice.Mode.FPSDenominator != 1001 ||
		choice.frameDurationValue != 1001 || choice.frameDurationTimescale != 30000 {
		t.Fatalf("NTSC cadence was not preserved: %+v", choice)
	}
}

func TestAVFModeChoiceClampsToSupportedRange(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FPS = 60
	ranges := []avfModeRange{{
		FormatIndex: 2, Width: 1280, Height: 720, Format: "420v",
		MinimumValue: 1, MinimumTimescale: 30,
		MaximumValue: 1, MaximumTimescale: 15,
	}}
	choice, ok := chooseAVFMode(ranges, cfg)
	if !ok {
		t.Fatal("no AVFoundation mode selected")
	}
	if choice.Mode.FPSNumerator != 30 || choice.Mode.FPSDenominator != 1 ||
		choice.frameDurationValue != 1 || choice.frameDurationTimescale != 30 {
		t.Fatalf("cadence was not clamped to 30 fps: %+v", choice)
	}
}

func TestAVFOutputDimensionsRemainExactAspectAndEven(t *testing.T) {
	for _, test := range []struct {
		width, height, downsample int
		wantWidth, wantHeight     int
	}{
		{1920, 1080, 4, 480, 270},
		{1280, 720, 8, 160, 90},
		{640, 480, 4, 160, 120},
	} {
		width, height := avfOutputDimensions(test.width, test.height, test.downsample)
		if width != test.wantWidth || height != test.wantHeight {
			t.Fatalf("avfOutputDimensions(%d, %d, %d) = %dx%d, want %dx%d", test.width, test.height, test.downsample, width, height, test.wantWidth, test.wantHeight)
		}
		if width*test.height != height*test.width || width&1 != 0 || height&1 != 0 {
			t.Fatalf("output %dx%d lost source aspect or NV12 alignment", width, height)
		}
	}
}

func TestAVFDurationConversion(t *testing.T) {
	if got, ok := avfDuration(1001, 30000); !ok || got != 33*time.Millisecond+366666*time.Nanosecond {
		t.Fatalf("avfDuration(1001, 30000) = %s, %t", got, ok)
	}
	if _, ok := avfDuration(1, 0); ok {
		t.Fatal("zero CMTime timescale was accepted")
	}
}

func TestAVFRetainedFramesStayWithinPoolBound(t *testing.T) {
	delivery := avfDelivery{maxPoolBytes: 100}
	if !delivery.reserve(60) {
		t.Fatal("first frame reservation failed")
	}
	if delivery.reserve(50) {
		t.Fatal("reservation exceeded the configured pool bound")
	}
	delivery.retainedBytes.Add(-60)
	if !delivery.reserve(50) {
		t.Fatal("released capacity was not reusable")
	}
}

func TestRealAVFoundationCaptureAndReopen(t *testing.T) {
	if os.Getenv("INLAID_AVF_CAPTURE_REAL") != "1" {
		t.Skip("set INLAID_AVF_CAPTURE_REAL=1 on a Mac with an attached camera")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	devices, err := Enumerate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) == 0 {
		t.Fatal("no AVFoundation camera found")
	}
	cfg := DefaultConfig()
	cfg.DeviceID = devices[0].ID
	cfg.QueueDepth = 2
	cfg.CloseTimeout = 2 * time.Second
	for attempt := 0; attempt < 2; attempt++ {
		session, err := Open(ctx, cfg)
		if err != nil {
			t.Fatalf("open attempt %d: %v", attempt+1, err)
		}
		var previousSequence uint64
		var previousPTS time.Duration
		for count := 0; count < 12; count++ {
			select {
			case frame, ok := <-session.Frames:
				if !ok {
					t.Fatal("frame stream closed early")
				}
				if frame.Layout != PixelLayoutNV12 ||
					(frame.Range != ColorRangeVideo && frame.Range != ColorRangeFull) ||
					(frame.Matrix != ColorMatrixBT601 && frame.Matrix != ColorMatrixBT709) {
					frame.Release()
					t.Fatalf("invalid AVFoundation metadata: %+v", frame)
				}
				if count > 0 && (frame.Sequence <= previousSequence || frame.PTS < previousPTS) {
					frame.Release()
					t.Fatalf("non-monotonic frame: sequence=%d PTS=%s", frame.Sequence, frame.PTS)
				}
				previousSequence, previousPTS = frame.Sequence, frame.PTS
				frame.Release()
			case captureErr := <-session.Errors:
				if !IsTemporary(captureErr) {
					t.Fatal(captureErr)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		if err := session.Close(); err != nil {
			t.Fatalf("close attempt %d: %v", attempt+1, err)
		}
	}
}
