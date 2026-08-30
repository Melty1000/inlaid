package dashboard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Melty1000/inlaid/internal/applayout"
	"github.com/Melty1000/inlaid/internal/supportreport"
)

func TestLayoutFFmpegRootDisablesInstalledLocalToolsOnly(t *testing.T) {
	for _, mode := range []applayout.Mode{applayout.Installed, applayout.Portable, applayout.Source, applayout.ExplicitTest} {
		t.Run(string(mode), func(t *testing.T) {
			layout, err := applayout.Local(t.TempDir(), mode)
			if err != nil {
				t.Fatal(err)
			}
			got := layoutFFmpegRoot(layout)
			if mode == applayout.Installed && got != "" {
				t.Fatalf("installed local FFmpeg root = %q, want disabled", got)
			}
			if mode != applayout.Installed && !filepath.IsAbs(got) {
				t.Fatalf("%s local FFmpeg root = %q, want absolute program root", mode, got)
			}
		})
	}
}

func TestSupportFactsReportOnlyResolvedLayoutMode(t *testing.T) {
	layout, err := applayout.Local(t.TempDir(), applayout.Installed)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntimeWithLayout(DefaultSettings(), layout, supportreport.BuildFacts{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	facts := runtime.currentSupportFacts()
	if facts.DistributionMode != "installed" {
		t.Fatalf("support distribution facts = %+v", facts)
	}
}

func TestRuntimeRoutesResolvedLocationsForEveryLayoutMode(t *testing.T) {
	for _, mode := range []applayout.Mode{applayout.Installed, applayout.Portable, applayout.Source, applayout.ExplicitTest} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			layout := applayout.Layout{
				ProgramRoot:       filepath.Join(root, "program"),
				SettingsFile:      filepath.Join(root, "state", "inlaid-settings.json"),
				RecordingsDir:     filepath.Join(root, "media", "recordings"),
				SnapshotsDir:      filepath.Join(root, "media", "snapshots"),
				RecoveryDir:       filepath.Join(root, "state", "recovery"),
				FiltersDir:        filepath.Join(root, "documents", "filters"),
				SupportReportsDir: filepath.Join(root, "documents", "reports"),
				Mode:              mode,
			}
			runtime, err := NewRuntimeWithLayout(DefaultSettings(), layout, supportreport.BuildFacts{Version: "test"})
			if err != nil {
				t.Fatal(err)
			}

			if err := os.MkdirAll(layout.FiltersDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(layout.FiltersDir, "invalid.cube"), []byte("not a cube\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runtime.discoverLooks()
			select {
			case event := <-runtime.Events():
				if event.Kind != RuntimeLooksFound || event.Err == nil {
					t.Fatalf("filters event = %+v, want rejected file from resolved directory", event)
				}
			case <-time.After(time.Second):
				t.Fatal("resolved filters directory was not inspected")
			}

			opened := make(chan string, 1)
			runtime.openFolder = func(_ context.Context, directory string) error {
				opened <- directory
				return nil
			}
			runtime.OpenFolder()
			select {
			case directory := <-opened:
				if directory != layout.RecordingsDir {
					t.Fatalf("opened directory = %q, want %q", directory, layout.RecordingsDir)
				}
			case <-time.After(time.Second):
				t.Fatal("resolved recordings directory was not opened")
			}

			if snapshot, err := runtime.nextOutput(layout.SnapshotsDir, "png"); err != nil || filepath.Dir(snapshot) != layout.SnapshotsDir {
				t.Fatalf("snapshot route = %q, %v; want %q", snapshot, err, layout.SnapshotsDir)
			}
			if recovered, err := runtime.recoveryOutput(filepath.Join(layout.RecoveryDir, "recording.celltape"), "mp4"); err != nil || filepath.Dir(recovered) != layout.RecordingsDir {
				t.Fatalf("recovery output route = %q, %v; want %q", recovered, err, layout.RecordingsDir)
			}

			runtime.CreateSupportReport()
			var reportPath string
			deadline := time.After(3 * time.Second)
			for reportPath == "" {
				select {
				case event := <-runtime.Events():
					switch event.Kind {
					case RuntimeSupportReportError:
						t.Fatal(event.Err)
					case RuntimeSupportReportSaved:
						reportPath = event.Path
					}
				case <-deadline:
					t.Fatal("support report was not created")
				}
			}
			if filepath.Dir(reportPath) != layout.SupportReportsDir {
				t.Fatalf("support report directory = %q, want %q", filepath.Dir(reportPath), layout.SupportReportsDir)
			}
			data, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				DistributionMode string `json:"distribution_mode"`
			}
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			if report.DistributionMode != string(mode) {
				t.Fatalf("support report mode = %q, want %q", report.DistributionMode, mode)
			}

			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(layout.SettingsFile); err != nil {
				t.Fatalf("settings were not saved to resolved path: %v", err)
			}
		})
	}
}
