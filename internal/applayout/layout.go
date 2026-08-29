// Package applayout resolves the locations owned by one Inlaid run.
//
// The dashboard receives a resolved Layout rather than inferring writable
// locations from its current directory, executable, or settings file.
package applayout

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Mode string

const (
	Installed    Mode = "installed"
	Portable     Mode = "portable"
	Source       Mode = "source"
	ExplicitTest Mode = "explicit-test"
)

const PortableMarker = "inlaid-portable.json"

// Layout is the complete set of filesystem locations the application may use.
// ProgramRoot is an input-only location; every other path is user-writable.
type Layout struct {
	ProgramRoot       string
	SettingsFile      string
	RecordingsDir     string
	SnapshotsDir      string
	RecoveryDir       string
	FiltersDir        string
	SupportReportsDir string
	Mode              Mode
}

func (l Layout) Validate() error {
	if l.Mode != Installed && l.Mode != Portable && l.Mode != Source && l.Mode != ExplicitTest {
		return fmt.Errorf("layout has unsupported mode %q", l.Mode)
	}
	for _, location := range []struct {
		name, path string
	}{
		{"program root", l.ProgramRoot}, {"settings file", l.SettingsFile},
		{"recordings directory", l.RecordingsDir}, {"snapshots directory", l.SnapshotsDir},
		{"recovery directory", l.RecoveryDir}, {"filters directory", l.FiltersDir},
		{"support reports directory", l.SupportReportsDir},
	} {
		if strings.TrimSpace(location.path) == "" {
			return fmt.Errorf("layout %s is empty", location.name)
		}
		if !filepath.IsAbs(location.path) {
			return fmt.Errorf("layout %s is not absolute", location.name)
		}
	}
	return nil
}

// Local returns the colocated layout used by portable, source, and explicit
// test runs. It deliberately does not inspect writability: callers must not
// turn a failed installed lookup into a silently portable run.
func Local(root string, mode Mode) (Layout, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return Layout{}, fmt.Errorf("%s layout root is empty", mode)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Layout{}, fmt.Errorf("resolve %s layout root: %w", mode, err)
	}
	root = filepath.Clean(root)
	layout := Layout{
		ProgramRoot: root, SettingsFile: filepath.Join(root, "inlaid-settings.json"),
		RecordingsDir: filepath.Join(root, "recordings"), SnapshotsDir: filepath.Join(root, "snapshots"),
		RecoveryDir: filepath.Join(root, "recordings", ".recovery"), FiltersDir: filepath.Join(root, "filters"),
		SupportReportsDir: filepath.Join(root, "support-reports"), Mode: mode,
	}
	return layout, layout.Validate()
}
