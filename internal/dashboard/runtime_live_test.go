package dashboard

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Melty1000/inlaid/internal/celllive"
	"github.com/charmbracelet/x/ansi"
)

func TestForwardSessionDetachesUnexpectedlyClosedSession(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	defer runtime.Close()
	view := ViewOptions{
		Version: 1, MaxColumns: 40, MaxRows: 12, Fill: true,
		Symbol: "quarter", TargetFPS: 30,
	}
	session, err := celllive.StartSynthetic(runtime.ctx, 320, 180, 30, runtime.toCellLiveView(view))
	if err != nil {
		t.Fatal(err)
	}
	generation := runtime.cameraGeneration.Add(1)
	runtime.installSession(session)
	done := make(chan struct{})
	go func() {
		runtime.forwardSession(generation, "DEMO", session)
		close(done)
	}()

	session.Close()
	select {
	case event := <-runtime.Events():
		if event.Kind != RuntimeCameraError || event.Err == nil || !strings.Contains(event.Err.Error(), "ended unexpectedly") {
			t.Fatalf("event = %+v, want unexpected camera end", event)
		}
	case <-time.After(time.Second):
		t.Fatal("unexpected stream end was not reported")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session forwarder did not stop")
	}
	runtime.sessionMu.RLock()
	installed := runtime.session
	runtime.sessionMu.RUnlock()
	if installed != nil {
		t.Fatal("ended session remained installed")
	}
}

// This is the single end-to-end hardware acceptance check. It is opt-in so
// ordinary tests never turn on a user's camera.
func TestRuntimeLiveCameraRecordAndSnapshot(t *testing.T) {
	if os.Getenv("INLAID_LIVE_TEST") != "1" {
		t.Skip("set INLAID_LIVE_TEST=1 to exercise a connected camera")
	}
	cfg := DefaultSettings()
	cfg.Device = os.Getenv("INLAID_TEST_DEVICE")
	if cfg.Device == "" {
		cfg.Device = "c922 Pro Stream Webcam"
	}
	root := t.TempDir()
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Clean(filepath.Join(packageDir, "..", ".."))
	t.Setenv("INLAID_FFMPEG", filepath.Join(projectRoot, ".tools", "ffmpeg", "bin", "ffmpeg.exe"))
	settingsPath := filepath.Join(root, "inlaid-settings.json")
	if err := SaveSettings(settingsPath, cfg); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(cfg, settingsPath, root)
	defer runtime.Close()
	runtime.Start(ViewOptions{
		Version: 1, MaxColumns: 120, MaxRows: 30, Fill: true,
		Symbol: "quarter", Detail: "AUTO", TargetFPS: 30,
	})

	deadline := time.After(15 * time.Second)
	liveReady, cadenceReady := false, false
	for !liveReady || !cadenceReady {
		select {
		case event := <-runtime.Events():
			if event.Kind == RuntimeCameraError {
				t.Fatalf("camera failed: %v", event.Err)
			}
			liveReady = liveReady || event.Kind == RuntimeCameraLive
		case preview := <-runtime.Previews():
			preview.acknowledgeRendered(true)
			if preview.Columns != 120 || preview.Rows != 30 {
				t.Fatalf("Fill preview = %dx%d, want 120x30", preview.Columns, preview.Rows)
			}
			cadenceReady = cadenceReady || preview.SourceFPS >= 28
		case <-deadline:
			t.Fatalf("camera acceptance timed out (live=%v cadence=%v)", liveReady, cadenceReady)
		}
	}

	options := RecordOptions{Format: "mp4", Quality: "high", Symbol: "quarter", Detail: "AUTO", Width: 1280, Height: 720, FPS: 30, Fill: true}
	runtime.StartRecording(options)
	waitForRuntimeEvent(t, runtime, 10*time.Second, RuntimeRecordingStarted)
	if shown := observePreviewFor(t, runtime, 2500*time.Millisecond); shown < 28 {
		t.Fatalf("preview while recording = %.1f fps, want close to 30", shown)
	}
	runtime.StopRecording()
	recording := waitForRuntimeEvent(t, runtime, 20*time.Second, RuntimeRecordingSaved)
	if info, err := os.Stat(recording.Path); err != nil || info.Size() == 0 {
		t.Fatalf("recording was not playable output: %v", err)
	}

	gifOptions := options
	gifOptions.Format, gifOptions.Width, gifOptions.Height, gifOptions.FPS = "gif", 640, 360, 15
	runtime.StartRecording(gifOptions)
	waitForRuntimeEvent(t, runtime, 10*time.Second, RuntimeRecordingStarted)
	time.Sleep(time.Second)
	runtime.StopRecording()
	gif := waitForRuntimeEvent(t, runtime, 20*time.Second, RuntimeRecordingSaved)
	if info, err := os.Stat(gif.Path); err != nil || info.Size() == 0 {
		t.Fatalf("GIF was not playable output: %v", err)
	}

	runtime.Snapshot(options)
	snapshot := waitForRuntimeEvent(t, runtime, 10*time.Second, RuntimeSnapshotSaved)
	if info, err := os.Stat(snapshot.Path); err != nil || info.Size() == 0 {
		t.Fatalf("snapshot was not written: %v", err)
	}
}

func TestBubbleTeaProgramReceivesLiveCameraPreview(t *testing.T) {
	if os.Getenv("INLAID_LIVE_TEST") != "1" {
		t.Skip("set INLAID_LIVE_TEST=1 to exercise a connected camera")
	}
	cfg := DefaultSettings()
	cfg.Device = "c922 Pro Stream Webcam"
	// Match the persisted settings used by the actual Windows Terminal visual
	// acceptance so this covers the Whole + Soft path as well as the direct
	// Fill + Balanced runtime gate above.
	cfg.Framing, cfg.Symbols = "whole", "half"
	root := t.TempDir()
	runtime := NewRuntime(cfg, filepath.Join(root, "inlaid-settings.json"), root)
	defer runtime.Close()
	model := NewLive(cfg, runtime, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var output bytes.Buffer
	program := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(&output),
		tea.WithWindowSize(112, 30),
	)
	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			program.Send(tea.Quit())
		case <-ctx.Done():
		}
	}()
	final, err := program.Run()
	if err != nil {
		t.Fatal(err)
	}
	got := final.(Model)
	if got.liveANSI == "" || got.sequence == 0 {
		t.Fatalf("Bubble Tea received no C922 preview: state=%s version=%d camera=%s", got.cameraState, got.viewVersion, got.cameraName)
	}
	if strings.Contains(ansi.Strip(got.rendered), "WAITING FOR CAMERA") {
		t.Fatal("Bubble Tea stored a C922 preview but left the waiting frame rendered")
	}
	if !strings.ContainsAny(output.String(), "▘▝▀▖▌▞▛") {
		t.Fatal("Bubble Tea renderer never flushed a C922 block cell")
	}
}

func observePreviewFor(t *testing.T, runtime *Runtime, duration time.Duration) float64 {
	t.Helper()
	deadline := time.After(duration)
	started := time.Now()
	lowest := 1e9
	for {
		select {
		case preview := <-runtime.Previews():
			preview.acknowledgeRendered(true)
			if time.Since(started) >= 1100*time.Millisecond && preview.ShownFPS > 0 && preview.ShownFPS < lowest {
				lowest = preview.ShownFPS
			}
		case event := <-runtime.Events():
			if event.Kind == RuntimeCameraError || event.Kind == RuntimeRecordingError {
				t.Fatalf("runtime failed while observing preview: %v", event.Err)
			}
		case <-deadline:
			if lowest == 1e9 {
				return 0
			}
			return lowest
		}
	}
}

func waitForRuntimeEvent(t *testing.T, runtime *Runtime, timeout time.Duration, wanted RuntimeEventKind) RuntimeEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case event := <-runtime.Events():
			if event.Kind == RuntimeCameraError || event.Kind == RuntimeRecordingError || event.Kind == RuntimeSnapshotError {
				t.Fatalf("runtime failed while waiting for %v: %v", wanted, event.Err)
			}
			if event.Kind == wanted {
				return event
			}
		case preview := <-runtime.Previews():
			// Keep latest-frame delivery moving while waiting for a media event.
			preview.acknowledgeRendered(true)
		case <-deadline:
			t.Fatalf("timed out waiting for runtime event %v", wanted)
		}
	}
}
