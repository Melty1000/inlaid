package applayout

import (
	"path/filepath"
	"testing"
)

func TestLocalKeepsEveryPortablePathUnderRoot(t *testing.T) {
	root := t.TempDir()
	layout, err := Local(root, Portable)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{layout.SettingsFile, layout.RecordingsDir, layout.SnapshotsDir, layout.RecoveryDir, layout.FiltersDir, layout.SupportReportsDir} {
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || filepath.IsAbs(relative) {
			t.Fatalf("%q escaped %q", path, root)
		}
	}
}

func TestLocalAlwaysReturnsAbsolutePathsForEveryMode(t *testing.T) {
	for _, mode := range []Mode{Installed, Portable, Source, ExplicitTest} {
		layout, err := Local("relative-layout-root", mode)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{layout.ProgramRoot, layout.SettingsFile, layout.RecordingsDir, layout.SnapshotsDir, layout.RecoveryDir, layout.FiltersDir, layout.SupportReportsDir} {
			if !filepath.IsAbs(path) {
				t.Fatalf("%s layout returned relative path %q", mode, path)
			}
		}
	}
}

func TestLayoutRejectsUnresolvedPaths(t *testing.T) {
	layout, err := Local(t.TempDir(), Installed)
	if err != nil {
		t.Fatal(err)
	}
	layout.SupportReportsDir = "relative-reports"
	if err := layout.Validate(); err == nil {
		t.Fatal("layout accepted an unresolved support-report path")
	}
}
