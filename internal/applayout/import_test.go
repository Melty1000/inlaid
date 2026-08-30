package applayout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installedLayout(t *testing.T) Layout {
	t.Helper()
	root := t.TempDir()
	layout, err := Local(root, Installed)
	if err != nil {
		t.Fatal(err)
	}
	return layout
}

func portableRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, PortableMarker), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func legacyPublishedV020Root(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, relative := range legacyPublishedV020RequiredFiles {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		// Deliberately not an executable. Import validates the pinned package
		// shape without launching or parsing release-owned helpers.
		if err := os.WriteFile(path, []byte("published v0.2.0-beta.1 fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestImportPortableCopiesSettingsAndTopLevelFiltersWithoutMovingMedia(t *testing.T) {
	source := portableRoot(t)
	if err := os.WriteFile(filepath.Join(source, "inlaid-settings.json"), []byte("{\"Device\":\"Camera\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "filters"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "filters", "warm.cube"), []byte("LUT_3D_SIZE 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "recordings"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "recordings", "keep.mp4"), []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}

	destination := installedLayout(t)
	report, err := ImportPortable(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination.SettingsFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination.FiltersDir, "warm.cube")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "recordings", "keep.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination.RecordingsDir, "keep.mp4")); !os.IsNotExist(err) {
		t.Fatalf("recording was copied: %v", err)
	}
	if len(report.Items) < 5 {
		t.Fatalf("report omitted copied/retained paths: %#v", report)
	}
}

func TestImportPortableCopiesLegacySettingsToCurrentInstalledName(t *testing.T) {
	fixtures := []struct {
		name string
		root func(*testing.T) string
	}{
		{name: "marked portable", root: portableRoot},
		{name: "pinned markerless v0.2.0-beta.1", root: legacyPublishedV020Root},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			source := fixture.root(t)
			legacyPath := filepath.Join(source, "webcam-settings.json")
			legacyData := []byte("{\"Device\":\"Legacy Camera\"}\n")
			if err := os.WriteFile(legacyPath, legacyData, 0o600); err != nil {
				t.Fatal(err)
			}

			destination := installedLayout(t)
			report, err := ImportPortable(source, destination)
			if err != nil {
				t.Fatal(err)
			}
			installedData, err := os.ReadFile(destination.SettingsFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(installedData) != string(legacyData) {
				t.Fatalf("installed settings = %q, want legacy bytes %q", installedData, legacyData)
			}
			retainedData, err := os.ReadFile(legacyPath)
			if err != nil || string(retainedData) != string(legacyData) {
				t.Fatalf("legacy source changed or disappeared: %q, %v", retainedData, err)
			}
			if len(report.Items) == 0 || report.Items[0].Action != ImportCopied {
				t.Fatalf("legacy settings result = %#v", report.Items)
			}
		})
	}
}

func TestImportPortablePrefersCurrentSettingsWhenBothNamesExist(t *testing.T) {
	source := portableRoot(t)
	currentData := []byte("{\"Device\":\"Current Camera\"}\n")
	legacyData := []byte("{\"Device\":\"Legacy Camera\"}\n")
	if err := os.WriteFile(filepath.Join(source, "inlaid-settings.json"), currentData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "webcam-settings.json"), legacyData, 0o600); err != nil {
		t.Fatal(err)
	}

	destination := installedLayout(t)
	if _, err := ImportPortable(source, destination); err != nil {
		t.Fatal(err)
	}
	installedData, err := os.ReadFile(destination.SettingsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(installedData) != string(currentData) {
		t.Fatalf("installed settings = %q, want current settings %q", installedData, currentData)
	}
}

func TestImportPortableHandlesLegacySettingsAbsenceConflictAndInvalidTypes(t *testing.T) {
	t.Run("both absent", func(t *testing.T) {
		source := portableRoot(t)
		destination := installedLayout(t)
		report, err := ImportPortable(source, destination)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Items) == 0 || report.Items[0].Action != ImportSkipped ||
			!strings.Contains(report.Items[0].Detail, "current and legacy") {
			t.Fatalf("missing-settings result = %#v", report.Items)
		}
		if _, err := os.Stat(destination.SettingsFile); !os.IsNotExist(err) {
			t.Fatalf("missing settings created a destination: %v", err)
		}
	})

	t.Run("legacy conflicts without overwrite", func(t *testing.T) {
		source := portableRoot(t)
		legacyPath := filepath.Join(source, "webcam-settings.json")
		if err := os.WriteFile(legacyPath, []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
		destination := installedLayout(t)
		if err := os.MkdirAll(filepath.Dir(destination.SettingsFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination.SettingsFile, []byte("installed"), 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := ImportPortable(source, destination)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Items) == 0 || report.Items[0].Action != ImportConflict {
			t.Fatalf("legacy conflict result = %#v", report.Items)
		}
		data, err := os.ReadFile(destination.SettingsFile)
		if err != nil || string(data) != "installed" {
			t.Fatalf("legacy conflict overwrote destination: %q, %v", data, err)
		}
	})

	for _, name := range []string{"inlaid-settings.json", "webcam-settings.json"} {
		t.Run("reject wrong type "+name, func(t *testing.T) {
			source := portableRoot(t)
			if name == "inlaid-settings.json" {
				if err := os.WriteFile(filepath.Join(source, "webcam-settings.json"), []byte("valid fallback"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(source, name), 0o700); err != nil {
				t.Fatal(err)
			}
			destination := installedLayout(t)
			if _, err := ImportPortable(source, destination); err == nil || !strings.Contains(err.Error(), "must be a direct regular file") {
				t.Fatalf("wrong-type %s error = %v", name, err)
			}
			if _, err := os.Stat(destination.SettingsFile); !os.IsNotExist(err) {
				t.Fatalf("wrong-type %s wrote settings: %v", name, err)
			}
		})
	}
}

func TestImportPortableRejectsSymlinkedSelectedSettings(t *testing.T) {
	for _, name := range []string{"inlaid-settings.json", "webcam-settings.json"} {
		t.Run(name, func(t *testing.T) {
			source := portableRoot(t)
			if name == "inlaid-settings.json" {
				if err := os.WriteFile(filepath.Join(source, "webcam-settings.json"), []byte("valid fallback"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(t.TempDir(), "settings-target.json")
			if err := os.WriteFile(target, []byte("symlink target"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(source, name)); err != nil {
				t.Skipf("file symlinks are unavailable on this host: %v", err)
			}

			destination := installedLayout(t)
			if _, err := ImportPortable(source, destination); err == nil || !strings.Contains(err.Error(), "must be a direct regular file") {
				t.Fatalf("symlinked %s error = %v", name, err)
			}
			if _, err := os.Stat(destination.SettingsFile); !os.IsNotExist(err) {
				t.Fatalf("symlinked %s wrote settings: %v", name, err)
			}
		})
	}
}

func TestImportPortableAcceptsPinnedMarkerlessPublishedZIPShapeWithoutExecution(t *testing.T) {
	source := legacyPublishedV020Root(t)
	if err := os.WriteFile(filepath.Join(source, "inlaid-settings.json"), []byte("{\"Device\":\"Camera\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "filters", "legacy.cube"), []byte("LUT_3D_SIZE 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	destination := installedLayout(t)
	report, err := ImportPortable(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Items) < 5 {
		t.Fatalf("legacy import report omitted results: %#v", report.Items)
	}
	for _, path := range []string{destination.SettingsFile, filepath.Join(destination.FiltersDir, "legacy.cube")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("legacy import did not copy %q: %v", path, err)
		}
	}
}

func TestImportPortableRejectsArbitraryMarkerlessRootBeforeWriting(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "inlaid-settings.json"), []byte("settings"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := installedLayout(t)
	_, err := ImportPortable(source, destination)
	if err == nil || !strings.Contains(err.Error(), "published v0.2.0-beta.1 layout") {
		t.Fatalf("markerless import error = %v", err)
	}
	if _, statErr := os.Stat(destination.SettingsFile); !os.IsNotExist(statErr) {
		t.Fatalf("ambiguous markerless import wrote settings: %v", statErr)
	}
}

func TestImportPortableRejectsAmbiguousLegacyAndCurrentHybrid(t *testing.T) {
	source := legacyPublishedV020Root(t)
	if err := os.WriteFile(filepath.Join(source, "inlaid.exe"), []byte("current-shaped root executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := installedLayout(t)
	_, err := ImportPortable(source, destination)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("hybrid markerless import error = %v", err)
	}
}

func TestImportPortableRejectsMarkerlessCurrentAndSourceShapes(t *testing.T) {
	for _, relative := range []string{"inlaid.exe", "go.mod"} {
		t.Run(relative, func(t *testing.T) {
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, relative), []byte("markerless non-baseline shape"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "inlaid-settings.json"), []byte("settings"), 0o600); err != nil {
				t.Fatal(err)
			}
			destination := installedLayout(t)
			_, err := ImportPortable(source, destination)
			if err == nil {
				t.Fatalf("markerless %s shape was accepted", relative)
			}
			if _, statErr := os.Stat(destination.SettingsFile); !os.IsNotExist(statErr) {
				t.Fatalf("markerless %s shape wrote settings: %v", relative, statErr)
			}
		})
	}
}

func TestImportPortableRefusesRecoveryAndDoesNotWriteDestination(t *testing.T) {
	source := portableRoot(t)
	if err := os.MkdirAll(filepath.Join(source, "recordings", ".recovery"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "recordings", ".recovery", "active.celltape"), []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := installedLayout(t)
	_, err := ImportPortable(source, destination)
	if err == nil || !strings.Contains(err.Error(), "live recovery tapes") {
		t.Fatalf("import error = %v", err)
	}
	if _, statErr := os.Stat(destination.SettingsFile); !os.IsNotExist(statErr) {
		t.Fatalf("import wrote settings despite recovery tape: %v", statErr)
	}
}

func TestImportPortableReportsConflictsWithoutOverwrite(t *testing.T) {
	source := portableRoot(t)
	if err := os.WriteFile(filepath.Join(source, "inlaid-settings.json"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := installedLayout(t)
	if err := os.MkdirAll(filepath.Dir(destination.SettingsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination.SettingsFile, []byte("installed"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := ImportPortable(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Items) == 0 || report.Items[0].Action != ImportConflict {
		t.Fatalf("settings result = %#v", report.Items)
	}
	data, err := os.ReadFile(destination.SettingsFile)
	if err != nil || string(data) != "installed" {
		t.Fatalf("destination settings overwritten: %q, %v", data, err)
	}
}

func TestImportPortablePreflightsAllDestinationsBeforeWriting(t *testing.T) {
	source := portableRoot(t)
	if err := os.WriteFile(filepath.Join(source, "inlaid-settings.json"), []byte("settings"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "filters"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "filters", "warm.cube"), []byte("LUT_3D_SIZE 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := installedLayout(t)
	if err := os.MkdirAll(filepath.Dir(destination.FiltersDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination.FiltersDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := ImportPortable(source, destination)
	if err == nil || !strings.Contains(err.Error(), `import filter "warm.cube"`) {
		t.Fatalf("import error = %v", err)
	}
	if len(report.Items) != 0 {
		t.Fatalf("preflight failure returned a mutation report: %#v", report.Items)
	}
	if _, statErr := os.Stat(destination.SettingsFile); !os.IsNotExist(statErr) {
		t.Fatalf("settings were written before the later filter destination failed: %v", statErr)
	}
}

func TestImportPortableReturnsCompletedPartialReportWhenAnApplyFails(t *testing.T) {
	source := portableRoot(t)
	if err := os.WriteFile(filepath.Join(source, "inlaid-settings.json"), []byte("settings"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "filters"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "filters", "warm.cube"), []byte("LUT_3D_SIZE 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	destination := installedLayout(t)
	applied := 0
	report, err := importPortable(source, destination, func(plan importPlan) (ImportItem, error) {
		applied++
		if applied == 2 {
			return ImportItem{}, os.ErrPermission
		}
		return applyImportPlan(plan)
	})
	if err == nil {
		t.Fatal("import succeeded despite the injected apply failure")
	}
	if len(report.Items) != 2 || report.Items[0].Action != ImportCopied || report.Items[1].Action != ImportSkipped {
		t.Fatalf("partial report lost completed or failed items: %#v", report.Items)
	}
	if !strings.Contains(report.Items[1].Detail, "not copied") {
		t.Fatalf("failed item does not explain the partial result: %#v", report.Items[1])
	}
	if _, statErr := os.Stat(destination.SettingsFile); statErr != nil {
		t.Fatalf("completed settings copy was not retained with the partial report: %v", statErr)
	}
}
