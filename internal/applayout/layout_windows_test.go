//go:build windows

package applayout

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestResolveWindowsInstalledUsesKnownFolders(t *testing.T) {
	root := t.TempDir()
	folders := map[*windows.KNOWNFOLDERID]string{
		windows.FOLDERID_LocalAppData: filepath.Join(root, "LocalAppData"),
		windows.FOLDERID_Videos:       filepath.Join(root, "Videos"),
		windows.FOLDERID_Pictures:     filepath.Join(root, "Pictures"),
		windows.FOLDERID_Documents:    filepath.Join(root, "Documents"),
	}
	layout, err := Resolve(ResolveOptions{
		Executable:   filepath.Join(root, "Programs", "Inlaid", "inlaid.exe"),
		knownFolder:  func(id *windows.KNOWNFOLDERID) (string, error) { return folders[id], nil },
		markerStatus: func(string) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if layout.Mode != Installed ||
		layout.SettingsFile != filepath.Join(root, "LocalAppData", "Inlaid", "inlaid-settings.json") ||
		layout.RecordingsDir != filepath.Join(root, "Videos", "Inlaid") ||
		layout.SnapshotsDir != filepath.Join(root, "Pictures", "Inlaid") ||
		layout.RecoveryDir != filepath.Join(root, "LocalAppData", "Inlaid", "Recovery") ||
		layout.FiltersDir != filepath.Join(root, "Documents", "Inlaid", "Filters") ||
		layout.SupportReportsDir != filepath.Join(root, "Documents", "Inlaid", "Support Reports") {
		t.Fatalf("installed layout = %#v", layout)
	}
}

func TestResolveWindowsExplicitModesPrecedePortableAndInstalled(t *testing.T) {
	root := t.TempDir()
	source, err := Resolve(ResolveOptions{
		Executable: filepath.Join(root, "inlaid.exe"), SourceRoot: filepath.Join(root, "source"),
		markerStatus: func(string) (bool, error) { return true, nil },
	})
	if err != nil || source.Mode != Source {
		t.Fatalf("source layout = %#v, %v", source, err)
	}
	testLayout, err := Resolve(ResolveOptions{
		Executable: filepath.Join(root, "inlaid.exe"), ExplicitRoot: filepath.Join(root, "test"),
		markerStatus: func(string) (bool, error) { return true, nil },
	})
	if err != nil || testLayout.Mode != ExplicitTest {
		t.Fatalf("explicit test layout = %#v, %v", testLayout, err)
	}
}

func TestResolveWindowsKnownFolderFailureDoesNotFallBackToPortable(t *testing.T) {
	_, err := Resolve(ResolveOptions{
		Executable:   filepath.Join(t.TempDir(), "Inlaid", "inlaid.exe"),
		knownFolder:  func(*windows.KNOWNFOLDERID) (string, error) { return "", errors.New("unavailable") },
		markerStatus: func(string) (bool, error) { return false, nil },
	})
	if err == nil {
		t.Fatal("installed known-folder failure was accepted")
	}
}

func TestResolveWindowsPortableMarkerWinsAfterExplicitRoots(t *testing.T) {
	root := t.TempDir()
	layout, err := Resolve(ResolveOptions{Executable: filepath.Join(root, "inlaid.exe"), markerStatus: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if layout.Mode != Portable || layout.RecordingsDir != filepath.Join(root, "recordings") {
		t.Fatalf("portable layout = %#v", layout)
	}
}

func TestResolveWindowsMalformedPortableMarkerFailsClosed(t *testing.T) {
	_, err := Resolve(ResolveOptions{
		Executable:   filepath.Join(t.TempDir(), "inlaid.exe"),
		markerStatus: func(string) (bool, error) { return false, errors.New("marker is a reparse point") },
	})
	if err == nil {
		t.Fatal("malformed portable marker silently fell back to installed mode")
	}
}
