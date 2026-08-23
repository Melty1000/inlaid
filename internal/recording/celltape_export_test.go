package recording

import (
	"context"
	"errors"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Melty1000/inlaid/internal/celltape"
)

type tapeState struct {
	host  uint64
	color celltape.RGB
	cols  uint32
	rows  uint32
}

func createExportTape(t *testing.T, states ...tapeState) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.celltape")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := celltape.New(context.Background(), file, celltape.Config{QueueCapacity: 16, Compression: celltape.CompressionFast})
	if err != nil {
		t.Fatal(err)
	}
	var epoch uint64 = 1
	var previousColumns, previousRows uint32
	for index, state := range states {
		if state.cols == 0 {
			state.cols = 1
		}
		if state.rows == 0 {
			state.rows = 1
		}
		if index > 0 && (state.cols != previousColumns || state.rows != previousRows) {
			epoch++
		}
		cells := make([]celltape.Cell, int(state.cols*state.rows))
		for cell := range cells {
			cells[cell] = celltape.Cell{FG: state.color, BG: state.color}
		}
		input := celltape.Input{
			GeometryEpoch: epoch,
			ConfigEpoch:   1,
			Columns:       state.cols,
			Rows:          state.rows,
			Cells:         cells,
			SourceNanos:   state.host,
		}
		if err = recorder.Submit(input, state.host); err != nil {
			t.Fatalf("Submit(%d): %v", index, err)
		}
		previousColumns, previousRows = state.cols, state.rows
	}
	if err = recorder.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEmitCellTapeFramesPreservesHostCadenceAndPixels(t *testing.T) {
	path := createExportTape(t,
		tapeState{host: 0, color: 0xff0000},
		tapeState{host: uint64(50 * time.Millisecond), color: 0x00ff00},
		tapeState{host: uint64(110 * time.Millisecond), color: 0x0000ff},
	)
	replay, err := celltape.Open(path, celltape.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()

	var got [][4]uint8
	err = emitCellTapeFrames(context.Background(), replay, 4, 20, func(frame *image.RGBA) error {
		got = append(got, [4]uint8{frame.Pix[0], frame.Pix[1], frame.Pix[2], frame.Pix[3]})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][4]uint8{
		{0xff, 0, 0, 0xff},
		{0, 0xff, 0, 0xff},
		{0, 0xff, 0, 0xff},
		{0, 0, 0xff, 0xff},
	}
	if len(got) != len(want) {
		t.Fatalf("emitted %d frames, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("frame %d pixel = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestFrameCountNeverShortensRequestedDuration(t *testing.T) {
	for _, test := range []struct {
		nanos uint64
		fps   int
		want  uint64
	}{
		{nanos: 1, fps: 30, want: 1},
		{nanos: uint64(100 * time.Millisecond), fps: 30, want: 3},
		{nanos: uint64(100*time.Millisecond + 1), fps: 30, want: 4},
		{nanos: uint64(175 * time.Millisecond), fps: 20, want: 4},
	} {
		frames, err := framesForDuration(test.nanos, test.fps)
		if err != nil {
			t.Fatal(err)
		}
		if frames != test.want {
			t.Errorf("framesForDuration(%d, %d) = %d, want %d", test.nanos, test.fps, frames, test.want)
		}
		if duration := durationForFrames(frames, test.fps); duration < test.nanos {
			t.Errorf("encoded duration %d shortened request %d", duration, test.nanos)
		}
	}
}

func TestCellTapeGridChangeUsesReusableExactCanvas(t *testing.T) {
	path := createExportTape(t,
		tapeState{host: 0, color: 0xff0000, cols: 1, rows: 1},
		tapeState{host: uint64(50 * time.Millisecond), color: 0x00ff00, cols: 2, rows: 1},
	)
	replay, err := celltape.Open(path, celltape.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	plan, err := inspectCellTape(context.Background(), replay, 10, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.variable || plan.changes != 1 {
		t.Fatalf("resize plan = %+v", plan)
	}
	replay.Rewind()
	var canvases [][]byte
	var reused *image.RGBA
	err = emitCellTapeCanvasFrames(context.Background(), replay, 2, 20, 10, 8, func(canvas *image.RGBA) error {
		if reused != nil && canvas != reused {
			t.Error("resize fallback allocated a different canvas")
		}
		reused = canvas
		canvases = append(canvases, append([]byte(nil), canvas.Pix...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pixel := func(frame, x, y int) [4]byte {
		offset := y*10*4 + x*4
		return [4]byte(canvases[frame][offset : offset+4])
	}
	black := [4]byte{0, 0, 0, 0xff}
	if got := pixel(0, 0, 0); got != black {
		t.Errorf("first grid left padding = %#v, want black", got)
	}
	if got := pixel(0, 3, 0); got != [4]byte{0xff, 0, 0, 0xff} {
		t.Errorf("first grid projected pixel = %#v, want red", got)
	}
	if got := pixel(1, 0, 0); got != black {
		t.Errorf("second grid left padding = %#v, want black", got)
	}
	if got := pixel(1, 1, 0); got != [4]byte{0, 0xff, 0, 0xff} {
		t.Errorf("second grid projected pixel = %#v, want green", got)
	}
}

func TestExportCellTapeRequiresExplicitTailRepair(t *testing.T) {
	path := createExportTape(t, tapeState{host: 0, color: 0x123456})
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("torn-tail")); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	_, err = ExportCellTape(context.Background(), CellTapeExportConfig{
		TapePath: path,
		Writer:   Config{FFmpeg: "definitely-not-started", FPS: 30},
	})
	if !errors.Is(err, ErrCellTapeTail) {
		t.Fatalf("ExportCellTape() error = %v, want tail error", err)
	}
	after, _ := os.Stat(path)
	if before.Size() != after.Size() {
		t.Fatalf("tape changed without RepairTail: %d -> %d", before.Size(), after.Size())
	}
}

func TestEmitCellTapeFramesHonorsCancellation(t *testing.T) {
	path := createExportTape(t, tapeState{host: 0, color: 0x123456})
	replay, err := celltape.Open(path, celltape.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	ctx, cancel := context.WithCancel(context.Background())
	writes := 0
	err = emitCellTapeFrames(ctx, replay, 100, 30, func(*image.RGBA) error {
		writes++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || writes != 1 {
		t.Fatalf("emit cancellation = %v after %d writes", err, writes)
	}
}

func TestExportCellTapeCreatesPlayableMP4AndGIF(t *testing.T) {
	ffmpeg := testFFmpeg(t)
	tape := createExportTape(t,
		tapeState{host: 0, color: 0x112233},
		tapeState{host: uint64(80 * time.Millisecond), color: 0xaabbcc},
	)
	for _, format := range []Format{FormatMP4, FormatGIF} {
		format := format
		t.Run(string(format), func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "capture."+string(format))
			report, err := ExportCellTape(context.Background(), CellTapeExportConfig{
				TapePath:     tape,
				EndHostNanos: uint64(175 * time.Millisecond),
				Writer: Config{
					FFmpeg: ffmpeg, Output: output, Width: 64, Height: 48,
					FPS: 20, Format: format, Overwrite: true,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if report.EncodedFrames != 4 || report.EncodedDurationNanos < report.RequestedHostNanos {
				t.Fatalf("report = %+v", report)
			}
			info, err := os.Stat(output)
			if err != nil {
				t.Fatalf("inspect export output: %v", err)
			}
			if info.Size() < 100 {
				t.Fatalf("export output is unexpectedly small: %d", info.Size())
			}
			command := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", output, "-f", "null", "-")
			if detail, decodeErr := command.CombinedOutput(); decodeErr != nil {
				t.Fatalf("decode export: %v: %s", decodeErr, strings.TrimSpace(string(detail)))
			}
			decodeFrame := exec.Command(
				ffmpeg,
				"-hide_banner", "-loglevel", "error",
				"-i", output,
				"-frames:v", "1",
				"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1",
			)
			pixels, decodeErr := decodeFrame.Output()
			if decodeErr != nil {
				t.Fatalf("decode first export frame: %v", decodeErr)
			}
			if len(pixels) != 64*48*4 {
				t.Fatalf("decoded frame has %d bytes, want a 64x48 RGBA canvas (%d)", len(pixels), 64*48*4)
			}
		})
	}

	t.Run("MP4 variable grid", func(t *testing.T) {
		resizeTape := createExportTape(t,
			tapeState{host: 0, color: 0x112233, cols: 1, rows: 1},
			tapeState{host: uint64(80 * time.Millisecond), color: 0xaabbcc, cols: 2, rows: 1},
		)
		output := filepath.Join(t.TempDir(), "resized.mp4")
		report, err := ExportCellTape(context.Background(), CellTapeExportConfig{
			TapePath:     resizeTape,
			EndHostNanos: uint64(175 * time.Millisecond),
			Writer: Config{
				FFmpeg: ffmpeg, Output: output, Width: 64, Height: 48,
				FPS: 20, Format: FormatMP4, Overwrite: true,
				// A stale fixed-grid hint is safely cleared when preflight finds
				// a resize instead of sending mismatched tiny frames to FFmpeg.
				CellColumns: 1, CellRows: 1,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !report.VariableGeometry || report.GeometryChanges != 1 || report.EncodedFrames != 4 {
			t.Fatalf("resize report = %+v", report)
		}
		if _, err = os.Stat(resizeTape); err != nil {
			t.Fatalf("canonical tape was not retained: %v", err)
		}
		command := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", output, "-f", "null", "-")
		if detail, decodeErr := command.CombinedOutput(); decodeErr != nil {
			t.Fatalf("decode resized export: %v: %s", decodeErr, strings.TrimSpace(string(detail)))
		}
	})
}

func TestExportCellTapeDiscardsRedundantCompletedStagesOnFailure(t *testing.T) {
	fakeFFmpeg := buildLifecycleFFmpeg(t)

	t.Run("MP4 publish", func(t *testing.T) {
		directory := t.TempDir()
		output := filepath.Join(directory, "capture.mp4")
		tape := createExportTape(t, tapeState{host: 0, color: 0x123456})
		t.Setenv("INLAID_FAKE_FFMPEG_MODE", "mp4-publish-fail")
		t.Setenv("INLAID_FAKE_FFMPEG_COLLISION", output)

		_, err := ExportCellTape(context.Background(), CellTapeExportConfig{
			TapePath: tape, EndHostNanos: uint64(100 * time.Millisecond),
			Writer: Config{FFmpeg: fakeFFmpeg, Output: output, Width: 64, Height: 48, FPS: 10, Format: FormatMP4},
		})
		if err == nil {
			t.Fatal("ExportCellTape() succeeded despite forced MP4 publish collision")
		}
		assertCanonicalTapeRetained(t, tape)
		assertNoPrivateMediaStages(t, directory)
		if got, readErr := os.ReadFile(output); readErr != nil || string(got) != "collision" {
			t.Fatalf("publish collision marker changed: got=%q err=%v", got, readErr)
		}
	})

	t.Run("GIF conversion", func(t *testing.T) {
		directory := t.TempDir()
		output := filepath.Join(directory, "capture.gif")
		tape := createExportTape(t, tapeState{host: 0, color: 0x654321})
		t.Setenv("INLAID_FAKE_FFMPEG_MODE", "gif-conversion-fail")
		t.Setenv("INLAID_FAKE_FFMPEG_COLLISION", "")

		_, err := ExportCellTape(context.Background(), CellTapeExportConfig{
			TapePath: tape, EndHostNanos: uint64(100 * time.Millisecond),
			Writer: Config{FFmpeg: fakeFFmpeg, Output: output, Width: 64, Height: 48, FPS: 10, Format: FormatGIF},
		})
		if err == nil {
			t.Fatal("ExportCellTape() succeeded despite forced GIF conversion failure")
		}
		assertCanonicalTapeRetained(t, tape)
		assertNoPrivateMediaStages(t, directory)
		if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed GIF conversion published a final: %v", statErr)
		}
	})

	t.Run("GIF publish", func(t *testing.T) {
		directory := t.TempDir()
		output := filepath.Join(directory, "capture.gif")
		tape := createExportTape(t, tapeState{host: 0, color: 0xabcdef})
		t.Setenv("INLAID_FAKE_FFMPEG_MODE", "gif-publish-fail")
		t.Setenv("INLAID_FAKE_FFMPEG_COLLISION", output)

		_, err := ExportCellTape(context.Background(), CellTapeExportConfig{
			TapePath: tape, EndHostNanos: uint64(100 * time.Millisecond),
			Writer: Config{FFmpeg: fakeFFmpeg, Output: output, Width: 64, Height: 48, FPS: 10, Format: FormatGIF},
		})
		if err == nil {
			t.Fatal("ExportCellTape() succeeded despite forced GIF publish collision")
		}
		assertCanonicalTapeRetained(t, tape)
		assertNoPrivateMediaStages(t, directory)
		if got, readErr := os.ReadFile(output); readErr != nil || string(got) != "collision" {
			t.Fatalf("publish collision marker changed: got=%q err=%v", got, readErr)
		}
	})

	t.Run("GIF cancellation", func(t *testing.T) {
		directory := t.TempDir()
		output := filepath.Join(directory, "capture.gif")
		marker := filepath.Join(t.TempDir(), "conversion-started")
		tape := createExportTape(t, tapeState{host: 0, color: 0x334455})
		t.Setenv("INLAID_FAKE_FFMPEG_MODE", "gif-cancel")
		t.Setenv("INLAID_FAKE_FFMPEG_MARKER", marker)
		t.Setenv("INLAID_FAKE_FFMPEG_COLLISION", "")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := ExportCellTape(ctx, CellTapeExportConfig{
				TapePath: tape, EndHostNanos: uint64(100 * time.Millisecond),
				Writer: Config{FFmpeg: fakeFFmpeg, Output: output, Width: 64, Height: 48, FPS: 10, Format: FormatGIF},
			})
			result <- err
		}()

		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			select {
			case err := <-result:
				t.Fatalf("export returned before cancellation marker: %v", err)
			case <-deadline.C:
				t.Fatal("timed out waiting for GIF conversion to start")
			case <-time.After(5 * time.Millisecond):
			}
		}
		cancel()
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("canceled GIF export reported success")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled GIF export did not stop")
		}
		assertCanonicalTapeRetained(t, tape)
		assertNoPrivateMediaStages(t, directory)
		if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("canceled GIF export published a final: %v", statErr)
		}
	})
}

func assertCanonicalTapeRetained(t testing.TB, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("canonical CellTape was not retained: info=%v err=%v", info, err)
	}
}

func assertNoPrivateMediaStages(t testing.TB, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if strings.HasPrefix(name, ".inlaid-recording-") || strings.HasPrefix(name, ".inlaid-gif-") {
			t.Fatalf("redundant private media stage remains: %s", entry.Name())
		}
	}
}

func buildLifecycleFFmpeg(t testing.TB) string {
	t.Helper()
	directory := t.TempDir()
	source := filepath.Join(directory, "main.go")
	executable := filepath.Join(directory, "fake-ffmpeg")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	program := `package main

import (
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		os.Exit(2)
	}
	conversion := false
	for _, arg := range args {
		if arg == "-filter_complex" {
			conversion = true
			break
		}
	}
	mode := os.Getenv("INLAID_FAKE_FFMPEG_MODE")
	output := args[len(args)-1]
	if !conversion {
		_, _ = io.Copy(io.Discard, os.Stdin)
		if err := os.WriteFile(output, []byte("fake-lossless-media"), 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		if mode == "mp4-publish-fail" {
			if err := os.WriteFile(os.Getenv("INLAID_FAKE_FFMPEG_COLLISION"), []byte("collision"), 0600); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(4)
			}
		}
		return
	}
	switch mode {
	case "gif-conversion-fail":
		fmt.Fprintln(os.Stderr, "forced GIF conversion failure")
		os.Exit(5)
	case "gif-cancel":
		if err := os.WriteFile(os.Getenv("INLAID_FAKE_FFMPEG_MARKER"), []byte("started"), 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(6)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if err := os.WriteFile(output, []byte("fake-gif"), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(7)
	}
	if mode == "gif-publish-fail" {
		if err := os.WriteFile(os.Getenv("INLAID_FAKE_FFMPEG_COLLISION"), []byte("collision"), 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(8)
		}
	}
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goBinary += ".exe"
	}
	command := exec.Command(goBinary, "build", "-o", executable, source)
	if detail, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build lifecycle FFmpeg helper: %v: %s", err, strings.TrimSpace(string(detail)))
	}
	return executable
}
