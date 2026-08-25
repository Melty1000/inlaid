package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Melty1000/inlaid/internal/capture"
	"github.com/Melty1000/inlaid/internal/celllive"
	"github.com/Melty1000/inlaid/internal/supportreport"
)

func TestRuntimeCreatesExplicitLocalSupportReportWithoutCameraIdentity(t *testing.T) {
	root := t.TempDir()
	opened := make(chan string, 1)
	settings := DefaultSettings()
	settings.Device = "Private Camera Name"
	settings.DeviceID = `private-device-id-C:\Users\Alice`
	runtime := NewRuntimeWithBuild(
		settings,
		filepath.Join(root, "inlaid-settings.json"),
		root,
		supportreport.BuildFacts{Version: "v0.2.0-beta.1", Revision: "abcdef012345"},
	)
	runtime.openFolder = func(_ context.Context, directory string) error {
		opened <- directory
		return nil
	}
	defer runtime.Close()

	runtime.CreateSupportReport()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-runtime.Events():
			switch event.Kind {
			case RuntimeSupportReportError:
				t.Fatal(event.Err)
			case RuntimeSupportReportSaved:
				data, err := os.ReadFile(event.Path)
				if err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range [][]byte{
					[]byte("Private Camera Name"), []byte("private-device-id"), []byte(`C:\\Users\\Alice`), []byte(root),
				} {
					if bytes.Contains(data, forbidden) {
						t.Fatalf("support report leaked %q", forbidden)
					}
				}
				if !bytes.Contains(data, []byte(`"uploads_data": false`)) {
					t.Fatal("support report did not state its local-only policy")
				}
				runtime.OpenFolder()
				select {
				case directory := <-opened:
					if directory != filepath.Dir(event.Path) {
						t.Fatalf("opened directory = %q, want support report directory %q", directory, filepath.Dir(event.Path))
					}
				case <-time.After(time.Second):
					t.Fatal("support report directory was not opened")
				}
				return
			}
		case <-deadline:
			t.Fatal("support report was not created")
		}
	}
}

func TestSupportReportKeepsFinalRecordingDurationStable(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "inlaid-settings.json"), root)
	defer runtime.Close()
	runtime.recordMu.Lock()
	runtime.recordOptions = RecordOptions{Format: "mp4", Quality: "high", Width: 1280, Height: 720, FPS: 30}
	runtime.recordDuration = 2500 * time.Millisecond
	runtime.recordResult = supportreport.CodeCompleted
	runtime.recordMu.Unlock()

	first := runtime.currentSupportFacts().Recording
	time.Sleep(10 * time.Millisecond)
	second := runtime.currentSupportFacts().Recording
	if first.DurationMillis != 2500 || second.DurationMillis != first.DurationMillis || second.Result != supportreport.CodeCompleted {
		t.Fatalf("final recording facts changed: first=%+v second=%+v", first, second)
	}
}

func TestSupportSampleUsesComposedDeliveryFPS(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "inlaid-settings.json"), root)
	defer runtime.Close()
	runtime.deliveryMu.Lock()
	runtime.deliveryFPS = 12.5
	runtime.deliveryMu.Unlock()

	runtime.observeSupportSample(celllive.Result{SourceFPS: 30, ShownFPS: 30, Columns: 120, Rows: 36})
	prepared, _, err := runtime.support.Prepare(runtime.currentSupportFacts(), supportreport.Include{})
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		RecentSamples []struct {
			ShownFPS float64 `json:"shown_fps"`
		} `json:"recent_samples"`
	}
	if err := json.Unmarshal(prepared.Content(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.RecentSamples) != 1 || report.RecentSamples[0].ShownFPS != 12.5 {
		t.Fatalf("support shown FPS = %+v, want composed delivery FPS 12.5", report.RecentSamples)
	}
}

func TestSupportSampleMapsTemporaryCaptureErrors(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "inlaid-settings.json"), root)
	defer runtime.Close()

	runtime.observeSupportSample(celllive.Result{Capture: capture.Stats{TemporaryErrors: 3}})
	prepared, _, err := runtime.support.Prepare(runtime.currentSupportFacts(), supportreport.Include{})
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		RecentSamples []struct {
			Capture supportreport.CaptureHealth `json:"capture"`
		} `json:"recent_samples"`
	}
	if err := json.Unmarshal(prepared.Content(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.RecentSamples) != 1 || report.RecentSamples[0].Capture.TemporaryErrors != 3 {
		t.Fatalf("support capture health = %+v, want 3 temporary errors", report.RecentSamples)
	}
}

func TestFailedCameraSwitchDoesNotReportPreviousSelectedMode(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "inlaid-settings.json"), root)
	defer runtime.Close()
	runtime.supportMu.Lock()
	runtime.supportCamera = "FIRST CAMERA"
	runtime.supportSource = celllive.SourceInfo{
		Width: 1920, Height: 1080, FPS: 30,
		FPSNumerator: 30, FPSDenominator: 1, Format: "MJPG",
	}
	runtime.supportMu.Unlock()
	runtime.devicesMu.Lock()
	runtime.deviceIDs["SECOND CAMERA"] = "second-camera-id"
	runtime.devicesMu.Unlock()
	want := errors.New("camera open failed")
	runtime.startCamera = func(context.Context, capture.Config, celllive.ViewConfig) (*celllive.Session, error) {
		return nil, want
	}

	runtime.SelectCamera("SECOND CAMERA")
	select {
	case event := <-runtime.Events():
		if event.Kind != RuntimeConnecting {
			t.Fatalf("first event = %v, want connecting", event.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("camera switch did not begin")
	}
	select {
	case event := <-runtime.Events():
		if event.Kind != RuntimeCameraError || !errors.Is(event.Err, want) {
			t.Fatalf("second event = %+v, want camera open error", event)
		}
	case <-time.After(time.Second):
		t.Fatal("failed camera switch did not report an error")
	}

	facts := runtime.currentSupportFacts().Camera
	if facts.Model != "SECOND CAMERA" {
		t.Fatalf("support camera = %q, want requested camera", facts.Model)
	}
	if facts.Selected != (supportreport.ModeFacts{}) {
		t.Fatalf("support selected mode = %+v, want unavailable mode", facts.Selected)
	}
}
