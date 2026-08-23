package recording

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizeInfersFormatAndRefusesInvalidDimensions(t *testing.T) {
	t.Parallel()
	cfg, err := normalize(Config{
		FFmpeg: "ffmpeg",
		Output: filepath.Join(t.TempDir(), "capture.gif"),
		Width:  320,
		Height: 180,
		FPS:    24,
	})
	if err != nil {
		t.Fatalf("normalize() error = %v", err)
	}
	if cfg.Format != FormatGIF {
		t.Errorf("format = %q, want %q", cfg.Format, FormatGIF)
	}
	if cfg.CRF != 16 || cfg.GIFColors != 256 {
		t.Errorf("defaults = CRF %d/colors %d, want 16/256", cfg.CRF, cfg.GIFColors)
	}

	_, err = normalize(Config{FFmpeg: "ffmpeg", Output: "bad.mp4", Width: 0, Height: 10, FPS: 30})
	if err == nil || !strings.Contains(err.Error(), "width and height") {
		t.Fatalf("normalize() error = %v, want dimensions error", err)
	}

	_, err = normalize(Config{FFmpeg: "ffmpeg", Output: "huge.mp4", Width: maxMediaDimension + 1, Height: 10, FPS: 30})
	if err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("normalize() oversized dimension error = %v", err)
	}
	_, err = normalize(Config{FFmpeg: "ffmpeg", Output: "huge.mp4", Width: 7680, Height: 5000, FPS: 30})
	if err == nil || !strings.Contains(err.Error(), "canvas") {
		t.Fatalf("normalize() oversized canvas error = %v", err)
	}
}

func TestByteTailRetainsOnlyNewestDiagnostics(t *testing.T) {
	t.Parallel()
	tail := newByteTail(8)
	for _, part := range []string{"abc", "defg", "hijk"} {
		if written, err := tail.Write([]byte(part)); err != nil || written != len(part) {
			t.Fatalf("Write(%q) = %d, %v", part, written, err)
		}
	}
	if got := tail.String(); got != "defghijk" {
		t.Fatalf("tail = %q, want newest eight bytes", got)
	}
	if _, err := tail.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if got := tail.String(); got != "23456789" {
		t.Fatalf("oversized write tail = %q", got)
	}
}

func TestRawEncodeArgumentsPreserveNoOverwrite(t *testing.T) {
	t.Parallel()
	args := rawEncodeArgs(Config{
		Width:  320,
		Height: 181,
		FPS:    24,
		Format: FormatMP4,
		CRF:    18,
	}, "capture.mp4")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-n") {
		t.Errorf("arguments do not preserve safe no-overwrite behavior: %s", joined)
	}
	if !strings.Contains(joined, "pad=ceil(iw/2)*2:ceil(ih/2)*2") {
		t.Errorf("arguments do not pad odd dimensions for yuv420p: %s", joined)
	}
	if !strings.Contains(joined, "-preset veryfast -tune animation -crf 18") {
		t.Errorf("arguments do not use the real-time animation profile: %s", joined)
	}
	for _, expected := range []string{
		"-filter_threads 1",
		"-threads:v 2",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("arguments %q do not contain resource ceiling %q", joined, expected)
		}
	}
	if strings.Contains(joined, "faststart") {
		t.Errorf("local recording unexpectedly requests an O(file size) faststart relocation: %s", joined)
	}
}

func TestEncoderFinalizeTimeoutHasLongRecordingHeadroom(t *testing.T) {
	t.Parallel()
	if got := encoderFinalizeTimeout(30*60, 30); got != 2*time.Minute {
		t.Fatalf("one-minute finalize timeout = %s, want 2m", got)
	}
	long := encoderFinalizeTimeout(30*60*60*24, 30)
	if long <= 14*time.Minute || long >= 16*time.Minute {
		t.Fatalf("24-hour finalize timeout = %s, want duration-aware headroom", long)
	}
}

func TestGIFStageArgumentsAreLosslessFFV1Matroska(t *testing.T) {
	t.Parallel()
	args := rawEncodeArgs(Config{
		Width:  320,
		Height: 180,
		FPS:    24,
		Format: FormatGIF,
	}, "capture.mkv")
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"-c:v ffv1",
		"-level 3",
		"-pix_fmt bgra",
		"-f matroska",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("GIF stage arguments %q do not contain %q", joined, expected)
		}
	}
	for _, lossy := range []string{"libx264", "yuv420p", "-crf"} {
		if strings.Contains(joined, lossy) {
			t.Errorf("GIF stage arguments %q unexpectedly contain lossy setting %q", joined, lossy)
		}
	}
}

func TestCellEncodeArgumentsUseTinyCanonicalRaster(t *testing.T) {
	t.Parallel()
	cfg, err := normalize(Config{
		FFmpeg: "ffmpeg", Output: filepath.Join(t.TempDir(), "cells.mp4"),
		Width: 1920, Height: 1080, FPS: 30, Format: FormatMP4,
		CellColumns: 240, CellRows: 67,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rawEncodeArgs(cfg, cfg.Output), " ")
	for _, expected := range []string{
		"-video_size 480x134",
		"scale=1920:1072:flags=neighbor,pad=1920:1080:0:4:color=black",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("cell arguments %q do not contain %q", joined, expected)
		}
	}
	writer := &Writer{cfg: cfg}
	if err := writer.WriteFrame(image.NewRGBA(image.Rect(0, 0, 1920, 1080))); err == nil || !strings.Contains(err.Error(), "expected 480x134") {
		t.Fatalf("full-canvas frame error = %v", err)
	}
}

func TestCellGIFDefersFullCanvasProjectionToBoundedConversion(t *testing.T) {
	t.Parallel()
	cfg, err := normalize(Config{
		FFmpeg: "ffmpeg", Output: filepath.Join(t.TempDir(), "cells.gif"),
		Width: 1920, Height: 1080, FPS: 30, Format: FormatGIF,
		CellColumns: 240, CellRows: 67,
	})
	if err != nil {
		t.Fatal(err)
	}
	stage := strings.Join(rawEncodeArgs(cfg, "capture.mkv"), " ")
	for _, expected := range []string{
		"-video_size 480x134",
		"-vf null",
		"-filter_threads 1",
		"-threads:v 2",
	} {
		if !strings.Contains(stage, expected) {
			t.Errorf("GIF stage arguments %q do not contain %q", stage, expected)
		}
	}
	if strings.Contains(stage, "scale=1920:1072") {
		t.Errorf("live GIF stage unexpectedly expands to the full canvas: %s", stage)
	}

	conversion := strings.Join(gifConvertArgs(cfg, "capture.mkv", "capture.gif"), " ")
	for _, expected := range []string{
		"-filter_threads 1",
		"-filter_complex_threads 1",
		"-threads:v 2",
		"scale=1920:1072:flags=neighbor,pad=1920:1080:0:4:color=black",
	} {
		if !strings.Contains(conversion, expected) {
			t.Errorf("GIF conversion arguments %q do not contain %q", conversion, expected)
		}
	}
}

func TestGIFFilterUsesEntireOpaquePalette(t *testing.T) {
	t.Parallel()
	filter := gifFilter(Config{FPS: 30, GIFColors: 256})
	for _, expected := range []string{
		"fps=30",
		"max_colors=256",
		"reserve_transparent=0",
		"dither=sierra2_4a",
	} {
		if !strings.Contains(filter, expected) {
			t.Errorf("GIF filter %q does not contain %q", filter, expected)
		}
	}
}

func TestWriteFrameRejectsWrongSize(t *testing.T) {
	t.Parallel()
	writer := &Writer{cfg: Config{Width: 2, Height: 2}}
	err := writer.WriteFrame(image.NewRGBA(image.Rect(0, 0, 3, 2)))
	if err == nil || !strings.Contains(err.Error(), "expected 2x2") {
		t.Fatalf("WriteFrame() error = %v, want size error", err)
	}
}

func TestFFmpegWriterCreatesPlayableContainers(t *testing.T) {
	ffmpeg := testFFmpeg(t)
	formats := []struct {
		name   string
		format Format
		ext    string
		stage  string
	}{
		{name: "MP4", format: FormatMP4, ext: ".mp4", stage: ".mp4"},
		{name: "GIF", format: FormatGIF, ext: ".gif", stage: ".mkv"},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "capture"+format.ext)
			writer, err := Start(context.Background(), Config{
				FFmpeg:    ffmpeg,
				Output:    output,
				Width:     64,
				Height:    48,
				FPS:       12,
				Format:    format.format,
				Overwrite: true,
			})
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			stagePath := writer.stagePath
			if filepath.Dir(stagePath) != filepath.Dir(output) {
				t.Fatalf("stage directory = %q, want same directory as %q", filepath.Dir(stagePath), output)
			}
			if extension := strings.ToLower(filepath.Ext(stagePath)); extension != format.stage {
				t.Fatalf("stage extension = %q, want %q", extension, format.stage)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("final output became visible before Close: %v", statErr)
			}
			for index := 0; index < 6; index++ {
				frame := image.NewRGBA(image.Rect(0, 0, 64, 48))
				fill := color.RGBA{R: uint8(index * 40), G: uint8(255 - index*30), B: uint8(index * 17), A: 255}
				for pixel := 0; pixel < len(frame.Pix); pixel += 4 {
					frame.Pix[pixel+0] = fill.R
					frame.Pix[pixel+1] = fill.G
					frame.Pix[pixel+2] = fill.B
					frame.Pix[pixel+3] = fill.A
				}
				if err := writer.WriteFrame(frame); err != nil {
					t.Fatalf("WriteFrame(%d) error = %v", index, err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if _, statErr := os.Stat(stagePath); !os.IsNotExist(statErr) {
				t.Fatalf("private stage remains after successful publication: %v", statErr)
			}
			info, err := os.Stat(output)
			if err != nil {
				t.Fatalf("recording output: %v", err)
			}
			if info.Size() < 100 {
				t.Errorf("recording output is unexpectedly small: %d bytes", info.Size())
			}
			decode := exec.Command(
				ffmpeg,
				"-hide_banner",
				"-loglevel", "error",
				"-i", output,
				"-map", "0:v:0",
				"-f", "null",
				"-",
			)
			if detail, decodeErr := decode.CombinedOutput(); decodeErr != nil {
				t.Fatalf("FFmpeg cannot decode the completed %s: %v: %s", format.name, decodeErr, strings.TrimSpace(string(detail)))
			}
		})
	}
}

func TestMP4OverwritePublishesOnlyAfterSuccessfulClose(t *testing.T) {
	ffmpeg := testFFmpeg(t)
	directory := t.TempDir()
	output := filepath.Join(directory, "capture.mp4")
	original := []byte("existing-good-output")
	if err := os.WriteFile(output, original, 0o600); err != nil {
		t.Fatal(err)
	}

	writer, err := Start(context.Background(), Config{
		FFmpeg: ffmpeg, Output: output,
		Width: 32, Height: 24, FPS: 12, Format: FormatMP4, Overwrite: true,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := writer.WriteFrame(solidFrame(32, 24, color.RGBA{R: 20, G: 80, B: 180, A: 255})); err != nil {
		_ = writer.Abort()
		t.Fatal(err)
	}
	beforeClose, err := os.ReadFile(output)
	if err != nil {
		_ = writer.Abort()
		t.Fatal(err)
	}
	if string(beforeClose) != string(original) {
		_ = writer.Abort()
		t.Fatalf("existing output changed before Close: %q", beforeClose)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	afterClose, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterClose) == string(original) || len(afterClose) < 100 {
		t.Fatalf("successful publication did not replace old output: %d bytes", len(afterClose))
	}
}

func TestMP4AbortLeavesNoNewFinalOrIncompleteStage(t *testing.T) {
	ffmpeg := testFFmpeg(t)
	directory := t.TempDir()
	output := filepath.Join(directory, "capture.mp4")
	writer, err := Start(context.Background(), Config{
		FFmpeg: ffmpeg, Output: output,
		Width: 320, Height: 180, FPS: 30, Format: FormatMP4,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	stagePath := writer.stagePath
	if err := writer.WriteFrame(solidFrame(320, 180, color.RGBA{R: 180, G: 40, B: 30, A: 255})); err != nil {
		_ = writer.Abort()
		t.Fatal(err)
	}
	_ = writer.Abort()
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("aborted recording published a final output: %v", statErr)
	}
	if _, statErr := os.Stat(stagePath); !os.IsNotExist(statErr) {
		t.Fatalf("aborted recording left an incomplete stage: %v", statErr)
	}
}

func TestMP4ContextCancellationCannotPublishPartialFinal(t *testing.T) {
	ffmpeg := testFFmpeg(t)
	directory := t.TempDir()
	output := filepath.Join(directory, "capture.mp4")
	ctx, cancel := context.WithCancel(context.Background())
	writer, err := Start(ctx, Config{
		FFmpeg: ffmpeg, Output: output,
		Width: 320, Height: 180, FPS: 30, Format: FormatMP4,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	stagePath := writer.stagePath
	if err := writer.WriteFrame(solidFrame(320, 180, color.RGBA{R: 70, G: 20, B: 150, A: 255})); err != nil {
		_ = writer.Abort()
		t.Fatal(err)
	}
	cancel()
	if err := writer.Close(); err == nil {
		t.Fatal("canceled writer reported a successful complete recording")
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("canceled writer published a partial final: %v", statErr)
	}
	if _, statErr := os.Stat(stagePath); !os.IsNotExist(statErr) {
		t.Fatalf("canceled writer retained an incomplete stage: %v", statErr)
	}
}

func TestMP4AbortLeavesExistingFinalUntouched(t *testing.T) {
	ffmpeg := testFFmpeg(t)
	directory := t.TempDir()
	output := filepath.Join(directory, "capture.mp4")
	original := []byte("known-good-existing-output")
	if err := os.WriteFile(output, original, 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := Start(context.Background(), Config{
		FFmpeg: ffmpeg, Output: output,
		Width: 320, Height: 180, FPS: 30, Format: FormatMP4, Overwrite: true,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	stagePath := writer.stagePath
	if err := writer.WriteFrame(solidFrame(320, 180, color.RGBA{R: 30, G: 180, B: 90, A: 255})); err != nil {
		_ = writer.Abort()
		t.Fatal(err)
	}
	_ = writer.Abort()
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("aborted overwrite changed the existing final: %q", got)
	}
	if _, statErr := os.Stat(stagePath); !os.IsNotExist(statErr) {
		t.Fatalf("aborted overwrite left an incomplete stage: %v", statErr)
	}
}

func TestGIFFailedConversionRetainsPlayableLosslessStage(t *testing.T) {
	ffmpeg := testFFmpeg(t)
	directory := t.TempDir()
	output := filepath.Join(directory, "capture.gif")
	writer, err := Start(context.Background(), Config{
		FFmpeg: ffmpeg, Output: output,
		Width: 64, Height: 48, FPS: 12, Format: FormatGIF,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	stagePath := writer.stagePath
	var firstFrame []byte
	for index := 0; index < 3; index++ {
		frame := solidFrame(64, 48, color.RGBA{R: uint8(40 + index*60), G: 100, B: 220, A: 255})
		if index == 0 {
			firstFrame = append(firstFrame, frame.Pix...)
		}
		if err := writer.WriteFrame(frame); err != nil {
			_ = writer.Abort()
			t.Fatal(err)
		}
	}
	writer.cfg.FFmpeg = filepath.Join(directory, "missing-ffmpeg")
	err = writer.Close()
	if err == nil || !strings.Contains(err.Error(), "recoverable lossless FFV1 stage kept") {
		t.Fatalf("Close() error = %v, want recoverable lossless stage detail", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("failed GIF conversion published a final output: %v", statErr)
	}
	info, statErr := os.Stat(stagePath)
	if statErr != nil || info.Size() == 0 {
		t.Fatalf("lossless GIF stage was not retained: info=%v err=%v", info, statErr)
	}
	decode := exec.Command(
		ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-i", stagePath,
		"-map", "0:v:0", "-f", "null", "-",
	)
	if detail, decodeErr := decode.CombinedOutput(); decodeErr != nil {
		t.Fatalf("retained FFV1 stage is not playable: %v: %s", decodeErr, strings.TrimSpace(string(detail)))
	}
	decodeRGBA := exec.Command(
		ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-i", stagePath,
		"-map", "0:v:0", "-frames:v", "1",
		"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1",
	)
	decodedFrame, decodeErr := decodeRGBA.Output()
	if decodeErr != nil {
		t.Fatalf("decode retained FFV1 pixels: %v", decodeErr)
	}
	if !bytes.Equal(decodedFrame, firstFrame) {
		t.Fatalf("FFV1 stage changed terminal raster pixels: got %d bytes, want %d exact bytes", len(decodedFrame), len(firstFrame))
	}
}

func solidFrame(width, height int, fill color.RGBA) *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	for pixel := 0; pixel < len(frame.Pix); pixel += 4 {
		frame.Pix[pixel+0] = fill.R
		frame.Pix[pixel+1] = fill.G
		frame.Pix[pixel+2] = fill.B
		frame.Pix[pixel+3] = fill.A
	}
	return frame
}

// TestFFmpegWriter1080pThroughput is an opt-in integration measurement rather
// than a normal pass/fail speed gate: encoder throughput varies by machine.
// Run it with INLAID_TEST_1080P=1 to exercise the same 1920x1080@30 stream used
// by a full-resolution export.
func TestFFmpegWriter1080pThroughput(t *testing.T) {
	if os.Getenv("INLAID_TEST_1080P") == "" {
		t.Skip("set INLAID_TEST_1080P=1 to measure 1080p encoder throughput")
	}
	ffmpeg := testFFmpeg(t)
	const (
		width      = 1920
		height     = 1080
		fps        = 30
		frameCount = 180
	)
	frames := make([]*image.RGBA, 4)
	for phase := range frames {
		frames[phase] = benchmarkFrame(width, height, phase)
	}
	output := filepath.Join(t.TempDir(), "throughput.mp4")
	started := time.Now()
	writer, err := Start(context.Background(), Config{
		FFmpeg:    ffmpeg,
		Output:    output,
		Width:     width,
		Height:    height,
		FPS:       fps,
		Format:    FormatMP4,
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	for index := 0; index < frameCount; index++ {
		if err := writer.WriteFrame(frames[index%len(frames)]); err != nil {
			_ = writer.Abort()
			t.Fatalf("WriteFrame(%d) error = %v", index, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	elapsed := time.Since(started)
	info, err := os.Stat(output)
	if err != nil {
		t.Fatalf("recording output: %v", err)
	}
	t.Logf(
		"encoded %d 1920x1080 frames in %s (%.1f fps, %.2fx real-time at 30 fps, %d bytes)",
		frameCount,
		elapsed.Round(time.Millisecond),
		float64(frameCount)/elapsed.Seconds(),
		(float64(frameCount)/elapsed.Seconds())/fps,
		info.Size(),
	)
}

func benchmarkFrame(width, height, phase int) *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	const (
		cellWidth  = 8
		cellHeight = 16
	)
	for y := 0; y < height; y++ {
		cellY := y / cellHeight
		for x := 0; x < width; x++ {
			cellX := x / cellWidth
			value := uint8((cellX*7 + cellY*11 + phase*29) % 256)
			offset := frame.PixOffset(x, y)
			frame.Pix[offset+0] = value
			frame.Pix[offset+1] = uint8((int(value)*3 + cellY*5) % 256)
			frame.Pix[offset+2] = uint8((255 - int(value) + cellX*3) % 256)
			frame.Pix[offset+3] = 255
		}
	}
	return frame
}

func testFFmpeg(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("INLAID_TEST_FFMPEG"); configured != "" {
		return configured
	}
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	local := filepath.Join("..", "..", ".tools", "ffmpeg", "bin", name)
	if absolute, err := filepath.Abs(local); err == nil {
		if _, statErr := os.Stat(absolute); statErr == nil {
			return absolute
		}
	}
	if found, err := exec.LookPath(name); err == nil {
		return found
	}
	t.Skip("FFmpeg is not available for the recording integration test")
	return ""
}
