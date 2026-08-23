package dashboard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/celllive"
)

func TestPreviewQueueKeepsOnlyNewestFrameAndRejectsDisplacedLease(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	defer runtime.Close()

	var mu sync.Mutex
	acknowledged := make(map[uint64][]bool)
	update := func(sequence uint64) PreviewUpdate {
		return PreviewUpdate{Sequence: sequence, acknowledge: func(accepted bool) {
			mu.Lock()
			acknowledged[sequence] = append(acknowledged[sequence], accepted)
			mu.Unlock()
			runtime.observePreviewOutcome(accepted)
		}}
	}

	if !runtime.publishPreview(update(1)) || !runtime.publishPreview(update(2)) {
		t.Fatal("latest-wins preview queue rejected a live update")
	}
	got := <-runtime.Previews()
	if got.Sequence != 2 {
		t.Fatalf("queued sequence = %d, want newest sequence 2", got.Sequence)
	}
	got.acknowledgeRendered(true)

	mu.Lock()
	defer mu.Unlock()
	if values := acknowledged[1]; len(values) != 1 || values[0] {
		t.Fatalf("displaced frame acknowledgements = %v, want [false]", values)
	}
	if values := acknowledged[2]; len(values) != 1 || !values[0] {
		t.Fatalf("delivered frame acknowledgements = %v, want [true]", values)
	}
	runtime.deliveryMu.Lock()
	dropped, accepted := runtime.deliveryDropped, runtime.deliveryCount
	runtime.deliveryMu.Unlock()
	if dropped != 1 || accepted != 1 {
		t.Fatalf("delivery counters = dropped %d accepted %d, want 1/1", dropped, accepted)
	}
}

func TestCameraSwitchRejectsBufferedPreviousGeneration(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	t.Cleanup(func() { _ = runtime.Close() })

	acknowledged := make(chan bool, 1)
	if !runtime.publishPreview(PreviewUpdate{
		Sequence: 1, cameraGeneration: runtime.cameraGeneration.Load(),
		acknowledge: func(accepted bool) { acknowledged <- accepted },
	}) {
		t.Fatal("initial preview was not buffered")
	}
	runtime.SelectCamera("DEMO")
	select {
	case accepted := <-acknowledged:
		if accepted {
			t.Fatal("camera switch accepted a buffered frame from the previous generation")
		}
	case <-time.After(time.Second):
		t.Fatal("camera switch did not reject its buffered previous-generation frame")
	}
}

func TestCloseRejectsBufferedPreview(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	acknowledged := make(chan bool, 1)
	if !runtime.publishPreview(PreviewUpdate{
		cameraGeneration: runtime.cameraGeneration.Load(),
		acknowledge:      func(accepted bool) { acknowledged <- accepted },
	}) {
		t.Fatal("preview was not buffered before Close")
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case accepted := <-acknowledged:
		if accepted {
			t.Fatal("Close accepted a buffered preview")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not reject its buffered preview")
	}
}

func TestReceivedPreviewCannotBeAcceptedAfterCameraGenerationChanges(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	defer runtime.Close()
	solver, err := cellframe.NewSolver(cellframe.Config{Columns: 1, Rows: 1, Mode: cellframe.ModeDetailed, Buffers: 1})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := solver.SolveRGB24(cellframe.RGB24{
		Pix: []byte{
			255, 0, 0, 0, 255, 0,
			0, 0, 255, 255, 255, 255,
		},
		Width: 2, Height: 2, Stride: 6,
	}, cellframe.SourceMeta{GeometryEpoch: 1, SourceSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	stale := PreviewUpdate{cameraGeneration: runtime.cameraGeneration.Load()}
	runtime.advanceCameraGeneration()
	if runtime.acceptCanonicalFrame(stale, frame) {
		t.Fatal("preview received before a camera change was accepted afterward")
	}
	frame.Release()
}

func TestSettingsSaverFlushesLatestValueOnClose(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "settings.json")
	runtime := NewRuntime(DefaultSettings(), path, root)
	latest := DefaultSettings()
	for width := 160; width < 360; width++ {
		latest.CaptureWidth = width
		runtime.Save(latest)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"CaptureWidth":359`) {
		t.Fatalf("final settings did not contain the latest coalesced width: %s", data)
	}
	select {
	case <-runtime.saveDone:
	default:
		t.Fatal("settings saver did not end during Close")
	}
}

func TestSettingsSaverClosesCleanlyDuringConcurrentInputs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "settings.json")
	runtime := NewRuntime(DefaultSettings(), path, root)
	start := make(chan struct{})
	var saves sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		saves.Add(1)
		go func(worker int) {
			defer saves.Done()
			<-start
			for offset := 0; offset < 100; offset++ {
				settings := DefaultSettings()
				settings.CaptureWidth = 320 + worker*100 + offset
				runtime.Save(settings)
			}
		}(worker)
	}
	close(start)
	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	saves.Wait()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(path); err != nil {
		t.Fatalf("concurrent close left invalid settings: %v", err)
	}
}

func TestInstallSessionReconcilesViewChangedWhileCameraOpened(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	defer runtime.Close()

	stale := celllive.ViewConfig{
		Version: 1, MaxColumns: 80, MaxRows: 20, Fill: true,
		Mode: cellframe.ModeDetailed, TargetFPS: 30,
	}
	session, err := celllive.StartSynthetic(context.Background(), 640, 480, 30, stale)
	if err != nil {
		t.Fatal(err)
	}

	// This is the ordering produced by a WindowSizeMsg during a slow native
	// camera open: Runtime owns the newest request before the Session exists.
	runtime.viewMu.Lock()
	runtime.view = ViewOptions{
		Version: 2, MaxColumns: 120, MaxRows: 30, Fill: true,
		Symbol: "quarter", Detail: "auto", TargetFPS: 30,
	}
	runtime.viewMu.Unlock()
	runtime.installSession(session)

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case result := <-session.Results:
			if result.Frame != nil {
				result.Frame.Release()
			}
			if result.Version == 2 {
				return
			}
		case <-deadline.C:
			t.Fatal("new session never received the view version chosen while it opened")
		}
	}
}

func TestChangingCameraClearsPreviouslyPersistedDeviceID(t *testing.T) {
	cfg := DefaultSettings()
	cfg.Device, cfg.DeviceID = "Old Camera", "old-stable-id"
	m := New(cfg)
	m.selectCameraSetting("New Camera")
	if m.settings.Device != "New Camera" || m.settings.DeviceID != "" {
		t.Fatalf("camera settings = %q / %q, want new label with unresolved ID", m.settings.Device, m.settings.DeviceID)
	}
}
