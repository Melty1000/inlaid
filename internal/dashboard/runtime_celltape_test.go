package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/cellrender"
	"github.com/Melty1000/inlaid/internal/celltape"
	ffmpegexe "github.com/Melty1000/inlaid/internal/ffmpeg"
	"github.com/Melty1000/inlaid/internal/taperecovery"
)

func TestRecordingQueueCapacityProvidesBoundedStallBudget(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		fps, want int
	}{
		{fps: 0, want: 120},
		{fps: 1, want: 8},
		{fps: 30, want: 120},
		{fps: 60, want: 240},
		{fps: 1000, want: 240},
	} {
		if got := recordingQueueCapacity(test.fps); got != test.want {
			t.Errorf("recordingQueueCapacity(%d) = %d, want %d", test.fps, got, test.want)
		}
	}
}

func TestRuntimeAutomaticallyRepairsAndExportsCrashStagingTape(t *testing.T) {
	ffmpeg, err := filepath.Abs(ffmpegexe.BundledPath(filepath.Join("..", "..")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ffmpeg); err != nil {
		t.Skip("bundled FFmpeg is unavailable")
	}
	root := t.TempDir()
	recoveryDir := filepath.Join(root, "recordings", ".recovery")
	finalTape := filepath.Join(recoveryDir, "Inlaid_crashed.celltape")
	tape, err := celltape.Create(context.Background(), finalTape, celltape.Config{Limits: canonicalTapeLimits, QueueCapacity: 2, Compression: celltape.CompressionFast})
	if err != nil {
		t.Fatal(err)
	}
	solver, err := cellframe.NewSolver(cellframe.Config{Columns: 8, Rows: 4, Mode: cellframe.ModeDetailed, Buffers: 1})
	if err != nil {
		t.Fatal(err)
	}
	pixels := make([]byte, 16*8*3)
	for i := range pixels {
		pixels[i] = byte(i * 17)
	}
	frame, err := solver.SolveRGB24(cellframe.RGB24{Pix: pixels, Width: 16, Height: 8, Stride: 48}, cellframe.SourceMeta{
		GeometryEpoch: 1, SourceSequence: 1, PTS: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(tapeRecordingConfig{Version: 1, Output: RecordOptions{
		Format: "mp4", Quality: "high", Width: 320, Height: 180, FPS: 10,
	}})
	if err == nil {
		err = tape.SubmitCellFrame(frame, 1, config, celltape.BoundaryDiscontinuity, 0)
	}
	frame.Release()
	if err != nil {
		t.Fatal(err)
	}
	if err := tape.Close(); err != nil {
		t.Fatal(err)
	}
	staging := tape.StagingPath()
	file, err := os.OpenFile(staging, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("torn-crash-tail")); err == nil {
		err = file.Close()
	} else {
		_ = file.Close()
	}
	if err != nil {
		t.Fatal(err)
	}

	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	defer runtime.Close()
	runtime.recoverRecordings(ffmpeg, nil)
	waitRuntimeKind(t, runtime.Events(), RuntimeRecoveryStarting)
	recovered := waitRuntimeKind(t, runtime.Events(), RuntimeRecoverySaved)
	if recovered.Count != 1 {
		t.Fatalf("recovered count = %d, want 1", recovered.Count)
	}
	if info, err := os.Stat(recovered.Path); err != nil || info.Size() == 0 {
		t.Fatalf("recovered media is not a nonempty file: info=%v err=%v", info, err)
	}
	candidates, err := filepath.Glob(filepath.Join(recoveryDir, "*.celltape*"))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("successful recovery left candidates %v, err=%v", candidates, err)
	}
}

func TestRuntimeReleasesRecoveryClaimAfterExportFailure(t *testing.T) {
	root := t.TempDir()
	recoveryDir := filepath.Join(root, "recordings", ".recovery")
	finalTape := filepath.Join(recoveryDir, "Inlaid_retry.celltape")
	recorder, err := celltape.Create(context.Background(), finalTape, celltape.Config{
		Limits: canonicalTapeLimits, QueueCapacity: 1, Compression: celltape.CompressionNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(tapeRecordingConfig{Version: 1, Output: RecordOptions{
		Format: "mp4", Quality: "high", Width: 320, Height: 180, FPS: 10,
	}})
	if err == nil {
		err = recorder.Submit(celltape.Input{
			GeometryEpoch: 1, ConfigEpoch: 1, Columns: 1, Rows: 1, Config: config,
			Cells: []celltape.Cell{{Mask: 0, FG: 0x112233, BG: 0x112233}},
		}, 0)
	}
	if closeErr := recorder.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = celltape.Publish(recorder.StagingPath(), finalTape)
	}
	if err != nil {
		t.Fatal(err)
	}

	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	defer runtime.Close()
	runtime.recoverRecordings("", errors.New("forced unavailable encoder"))
	waitRuntimeKind(t, runtime.Events(), RuntimeRecoveryStarting)
	waitRuntimeKind(t, runtime.Events(), RuntimeRecoveryError)

	engine, err := taperecovery.New(recoveryDir, taperecovery.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reclaimed, err := engine.Claim(taperecovery.Candidate{Path: finalTape, Kind: taperecovery.Published})
	if err != nil {
		t.Fatalf("claim remained locked after failed runtime recovery: %v", err)
	}
	if err := reclaimed.Release(); err != nil {
		t.Fatalf("release reclaimed tape: %v", err)
	}
}

func TestRecoveryOutputDoesNotTreatNonemptyCollisionAsCompletion(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	defer runtime.Close()
	recordings := filepath.Join(root, "recordings")
	if err := os.MkdirAll(recordings, 0o755); err != nil {
		t.Fatal(err)
	}
	tape := filepath.Join(recordings, ".recovery", "Inlaid_collision.celltape")
	preferred := filepath.Join(recordings, "Inlaid_collision.mp4")
	marker := []byte("unrelated-media")
	if err := os.WriteFile(preferred, marker, 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := runtime.recoveryOutput(tape, "mp4")
	if err != nil {
		t.Fatal(err)
	}
	if output == preferred {
		t.Fatal("same-name nonempty file was treated as proof of completed recovery")
	}
	if got, err := os.ReadFile(preferred); err != nil || string(got) != string(marker) {
		t.Fatalf("collision changed: got=%q err=%v", got, err)
	}
}

func TestRetireClaimedRecoveryTapeRefusesDifferentObject(t *testing.T) {
	recoveryDir := t.TempDir()
	path := filepath.Join(recoveryDir, "recording.celltape")
	recorder, err := celltape.Create(context.Background(), path, celltape.Config{
		Limits: canonicalTapeLimits, QueueCapacity: 1, Compression: celltape.CompressionNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(tapeRecordingConfig{Version: 1, Output: RecordOptions{
		Format: "mp4", Quality: "high", Width: 320, Height: 180, FPS: 30,
	}})
	if err == nil {
		err = recorder.Submit(celltape.Input{
			GeometryEpoch: 1, ConfigEpoch: 1, Columns: 1, Rows: 1, Config: config,
			Cells: []celltape.Cell{{FG: 0x112233, BG: 0x112233}},
		}, 0)
	}
	if closeErr := recorder.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = celltape.Publish(recorder.StagingPath(), path)
	}
	if err != nil {
		t.Fatal(err)
	}
	engine, err := taperecovery.New(recoveryDir, taperecovery.Options{TapeLimits: canonicalTapeLimits})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := engine.Claim(taperecovery.Candidate{Path: path, Kind: taperecovery.Published})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claimed.Release() })

	replacementPath := filepath.Join(recoveryDir, "replacement.celltape")
	replacement := []byte("replacement must survive")
	if err = os.WriteFile(replacementPath, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	claimed.Path = replacementPath
	if err = retireClaimedRecoveryTape(&claimed); !errors.Is(err, taperecovery.ErrIdentityChanged) {
		t.Fatalf("retireClaimedRecoveryTape error = %v, want ErrIdentityChanged", err)
	}
	if got, readErr := os.ReadFile(replacementPath); readErr != nil || string(got) != string(replacement) {
		t.Fatalf("replacement changed: got=%q err=%v", got, readErr)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("claimed object changed: %v", statErr)
	}
}

func TestValidateRecoveredOutputUsesProductBounds(t *testing.T) {
	valid := RecordOptions{Format: "mp4", Quality: "high", Width: 320, Height: 180, FPS: 60}
	if err := validateRecoveredOutput(valid, "mp4"); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.FPS = 240
	if err := validateRecoveredOutput(invalid, "mp4"); err == nil {
		t.Fatal("recovery accepted FPS unavailable in the product")
	}
	invalid = valid
	invalid.Width, invalid.Height = 7680, 4320
	if err := validateRecoveredOutput(invalid, "mp4"); err == nil {
		t.Fatal("recovery accepted canvas unavailable in the product")
	}
}

func TestRuntimeRecordsAcceptedCellsThroughCellTape(t *testing.T) {
	ffmpeg, err := filepath.Abs(ffmpegexe.BundledPath(filepath.Join("..", "..")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ffmpeg); err != nil {
		t.Skip("bundled FFmpeg is unavailable")
	}
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	runtime.ffmpeg = ffmpeg
	t.Cleanup(func() { _ = runtime.Close() })

	solver, err := cellframe.NewSolver(cellframe.Config{Columns: 8, Rows: 4, Mode: cellframe.ModeDetailed, Buffers: 4})
	if err != nil {
		t.Fatal(err)
	}
	accept := func(sequence uint64) {
		pixels := make([]byte, 16*8*3)
		for i := 0; i < len(pixels); i += 3 {
			pixels[i], pixels[i+1], pixels[i+2] = byte(sequence*31), byte(i/3), byte(255-sequence*17)
		}
		frame, solveErr := solver.SolveRGB24(cellframe.RGB24{Pix: pixels, Width: 16, Height: 8, Stride: 48}, cellframe.SourceMeta{
			GeometryEpoch: 1, SourceSequence: sequence, PTS: time.Duration(sequence) * 30 * time.Millisecond,
		})
		if solveErr != nil {
			t.Fatal(solveErr)
		}
		ansi, renderErr := cellrender.ANSI(frame)
		if renderErr != nil {
			frame.Release()
			t.Fatal(renderErr)
		}
		prepared, tape, started, preparedOK := runtime.prepareCanonicalFrame(frame)
		if !runtime.acceptCanonicalFrame(PreviewUpdate{ANSI: ansi, Columns: 8, Rows: 4, Sequence: sequence}, frame) {
			if preparedOK {
				prepared.Abort()
			}
			frame.Release()
			t.Fatal("preview was not accepted")
		}
		if preparedOK {
			runtime.commitCanonicalFrame(prepared, tape, started)
		}
	}
	accept(1)
	runtime.StartRecording(RecordOptions{Format: "mp4", Quality: "high", Width: 320, Height: 180, FPS: 10})
	waitRuntimeKind(t, runtime.Events(), RuntimeRecordingStarted)
	for sequence := uint64(2); sequence <= 5; sequence++ {
		accept(sequence)
		time.Sleep(20 * time.Millisecond)
	}
	runtime.StopRecording()
	saved := waitRuntimeKind(t, runtime.Events(), RuntimeRecordingSaved)
	info, err := os.Stat(saved.Path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("recording is not a nonempty file: info=%v err=%v", info, err)
	}
	recovery, err := filepath.Glob(filepath.Join(root, "recordings", ".recovery", "*.celltape"))
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery) != 0 {
		t.Fatalf("successful export left recovery tape: %v", recovery)
	}
}

func TestRejectedPreviewIsNotRecordedAndFailedExportKeepsRecoverableTape(t *testing.T) {
	ffmpeg, err := filepath.Abs(ffmpegexe.BundledPath(filepath.Join("..", "..")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ffmpeg); err != nil {
		t.Skip("bundled FFmpeg is unavailable")
	}
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	runtime.ffmpeg = ffmpeg
	t.Cleanup(func() { _ = runtime.Close() })

	solver, err := cellframe.NewSolver(cellframe.Config{Columns: 8, Rows: 4, Mode: cellframe.ModeDetailed, Buffers: 4})
	if err != nil {
		t.Fatal(err)
	}
	makeFrame := func(sequence uint64) *cellframe.CellFrame {
		pixels := make([]byte, 16*8*3)
		for i := 0; i < len(pixels); i += 3 {
			pixels[i], pixels[i+1], pixels[i+2] = byte(sequence*31), byte(i/3), byte(255-sequence*17)
		}
		frame, solveErr := solver.SolveRGB24(cellframe.RGB24{Pix: pixels, Width: 16, Height: 8, Stride: 48}, cellframe.SourceMeta{
			GeometryEpoch: 1, SourceSequence: sequence, PTS: time.Duration(sequence) * 30 * time.Millisecond,
		})
		if solveErr != nil {
			t.Fatal(solveErr)
		}
		return frame
	}

	first := makeFrame(1)
	firstANSI, err := cellrender.ANSI(first)
	if err != nil {
		first.Release()
		t.Fatal(err)
	}
	if !runtime.acceptCanonicalFrame(PreviewUpdate{ANSI: firstANSI, Columns: 8, Rows: 4, Sequence: 1}, first) {
		first.Release()
		t.Fatal("first preview was not accepted")
	}
	runtime.StartRecording(RecordOptions{Format: "mp4", Quality: "high", Width: 320, Height: 180, FPS: 10})
	waitRuntimeKind(t, runtime.Events(), RuntimeRecordingStarted)

	rejected := makeFrame(2)
	prepared, _, _, ok := runtime.prepareCanonicalFrame(rejected)
	if !ok {
		rejected.Release()
		t.Fatal("could not reserve rejected preview")
	}
	prepared.Abort()
	rejected.Release()

	accepted := makeFrame(3)
	acceptedANSI, err := cellrender.ANSI(accepted)
	if err != nil {
		accepted.Release()
		t.Fatal(err)
	}
	prepared, acceptedTape, started, ok := runtime.prepareCanonicalFrame(accepted)
	if !ok {
		accepted.Release()
		t.Fatal("could not reserve accepted preview")
	}
	if !runtime.acceptCanonicalFrame(PreviewUpdate{ANSI: acceptedANSI, Columns: 8, Rows: 4, Sequence: 3}, accepted) {
		prepared.Abort()
		accepted.Release()
		t.Fatal("third preview was not accepted")
	}
	runtime.commitCanonicalFrame(prepared, acceptedTape, started)

	// Reserve one more state before Stop, then acknowledge it after Stop has
	// detached the tape. This deterministically covers the real asynchronous UI
	// race: a late render acknowledgement must be aborted, not appended beyond
	// the recording's end timestamp.
	late := makeFrame(4)
	lateANSI, err := cellrender.ANSI(late)
	if err != nil {
		late.Release()
		t.Fatal(err)
	}
	latePrepared, lateTape, lateStarted, ok := runtime.prepareCanonicalFrame(late)
	if !ok {
		late.Release()
		t.Fatal("could not reserve late preview")
	}

	runtime.ffmpegMu.Lock()
	runtime.ffmpeg = filepath.Join(root, "missing-ffmpeg.exe")
	runtime.ffmpegMu.Unlock()
	runtime.StopRecording()
	if !runtime.acceptCanonicalFrame(PreviewUpdate{ANSI: lateANSI, Columns: 8, Rows: 4, Sequence: 4}, late) {
		latePrepared.Abort()
		late.Release()
		t.Fatal("late preview was not accepted by the display")
	}
	runtime.commitCanonicalFrame(latePrepared, lateTape, lateStarted)
	var failed RuntimeEvent
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for failed.Kind != RuntimeRecordingError {
		select {
		case event := <-runtime.Events():
			if event.Kind == RuntimeRecordingError {
				failed = event
			}
		case <-timer.C:
			t.Fatal("timed out waiting for forced export failure")
		}
	}

	recovery, err := filepath.Glob(filepath.Join(root, "recordings", ".recovery", "*.celltape"))
	if err != nil || len(recovery) != 1 {
		t.Fatalf("recoverable tapes = %v, err=%v", recovery, err)
	}
	replay, err := celltape.Open(recovery[0], celltape.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	var sourceNanos []uint64
	if err := replay.Iterate(context.Background(), func(frame celltape.Frame) error {
		sourceNanos = append(sourceNanos, frame.SourceNanos)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantSource := []uint64{uint64(30 * time.Millisecond), uint64(90 * time.Millisecond)}
	if len(sourceNanos) != len(wantSource) || sourceNanos[0] != wantSource[0] || sourceNanos[1] != wantSource[1] {
		t.Fatalf("replayed source times = %v, want first + third only %v", sourceNanos, wantSource)
	}
	if failed.Path != "" {
		if info, statErr := os.Stat(failed.Path); statErr == nil && info.Size() == 0 {
			t.Fatal("failed export published a zero-byte media file")
		}
	}
}

func waitRuntimeKind(t testing.TB, events <-chan RuntimeEvent, kind RuntimeEventKind) RuntimeEvent {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Kind == RuntimeRecordingError {
				t.Fatalf("recording failed: %v", event.Err)
			}
			if event.Kind == kind {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for runtime event %v", kind)
		}
	}
}
