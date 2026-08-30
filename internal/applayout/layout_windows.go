//go:build windows

package applayout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

type ResolveOptions struct {
	Executable   string
	SourceRoot   string
	ExplicitRoot string
	WorkingDir   string
	knownFolder  func(*windows.KNOWNFOLDERID) (string, error)
	markerStatus func(string) (bool, error)
}

func Resolve(options ResolveOptions) (Layout, error) {
	if root := strings.TrimSpace(options.SourceRoot); root != "" {
		return Local(root, Source)
	}
	if root := strings.TrimSpace(options.ExplicitRoot); root != "" {
		return Local(root, ExplicitTest)
	}
	executable := strings.TrimSpace(options.Executable)
	if executable == "" {
		return Layout{}, fmt.Errorf("resolve Windows layout: executable path is empty")
	}
	programRoot := filepath.Dir(executable)
	markerStatus := options.markerStatus
	if markerStatus == nil {
		markerStatus = func(path string) (bool, error) {
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return false, fmt.Errorf("portable marker must be a direct regular file")
			}
			return true, nil
		}
	}
	markedPortable, err := markerStatus(filepath.Join(programRoot, PortableMarker))
	if err != nil {
		return Layout{}, fmt.Errorf("inspect Windows portable marker: %w", err)
	}
	if markedPortable {
		return Local(programRoot, Portable)
	}
	knownFolder := options.knownFolder
	if knownFolder == nil {
		knownFolder = func(id *windows.KNOWNFOLDERID) (string, error) {
			return windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT)
		}
	}
	requireKnownFolder := func(name string, id *windows.KNOWNFOLDERID) (string, error) {
		path, err := knownFolder(id)
		if err != nil {
			return "", fmt.Errorf("resolve installed %s directory: %w", name, err)
		}
		if strings.TrimSpace(path) == "" {
			return "", fmt.Errorf("resolve installed %s directory: known folder is empty", name)
		}
		return path, nil
	}
	localAppData, err := requireKnownFolder("settings", windows.FOLDERID_LocalAppData)
	if err != nil {
		return Layout{}, err
	}
	videos, err := requireKnownFolder("recordings", windows.FOLDERID_Videos)
	if err != nil {
		return Layout{}, err
	}
	pictures, err := requireKnownFolder("snapshots", windows.FOLDERID_Pictures)
	if err != nil {
		return Layout{}, err
	}
	documents, err := requireKnownFolder("documents", windows.FOLDERID_Documents)
	if err != nil {
		return Layout{}, err
	}
	root := filepath.Join(localAppData, "Inlaid")
	layout := Layout{
		ProgramRoot: programRoot, SettingsFile: filepath.Join(root, "inlaid-settings.json"),
		RecordingsDir: filepath.Join(videos, "Inlaid"), SnapshotsDir: filepath.Join(pictures, "Inlaid"),
		RecoveryDir: filepath.Join(root, "Recovery"), FiltersDir: filepath.Join(documents, "Inlaid", "Filters"),
		SupportReportsDir: filepath.Join(documents, "Inlaid", "Support Reports"), Mode: Installed,
	}
	return layout, layout.Validate()
}
