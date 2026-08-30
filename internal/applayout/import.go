package applayout

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ImportAction string

const (
	ImportCopied   ImportAction = "copied"
	ImportSkipped  ImportAction = "skipped"
	ImportConflict ImportAction = "conflict"
	ImportRetained ImportAction = "retained"
)

type ImportItem struct {
	Path   string
	Action ImportAction
	Detail string
}

type ImportReport struct{ Items []ImportItem }

type importPlan struct {
	item        ImportItem
	destination string
	data        []byte
	copy        bool
}

type importApplier func(importPlan) (ImportItem, error)

// v0.2.0-beta.1 (adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3)
// predates the portable manifest. These release-owned paths are the narrow
// structural signature accepted for that one markerless migration source.
// Import never executes or trusts the contents of any of these files.
var legacyPublishedV020RequiredFiles = []string{
	filepath.Join("bin", "inlaid.exe"),
	"START-INLAID.cmd",
	"START-INLAID.ps1",
	"README.md",
	"CHANGELOG.md",
	"CONTRIBUTING.md",
	"LICENSE",
	"SECURITY.md",
	"THIRD_PARTY_NOTICES.md",
	filepath.Join("docs", "CELL_PIPELINE.md"),
	filepath.Join("docs", "COMPATIBILITY.md"),
	filepath.Join("docs", "DESIGN.md"),
	filepath.Join("docs", "FILTERS.md"),
	filepath.Join("docs", "PHASE_1.md"),
	filepath.Join("docs", "PHASE_2.md"),
	filepath.Join("docs", "ROADMAP.md"),
	filepath.Join("docs", "TESTING.md"),
	filepath.Join("filters", "README.md"),
	filepath.Join("scripts", "install-ffmpeg.ps1"),
}

var legacyPublishedV020RequiredDirectories = []string{"bin", "docs", "filters", "scripts"}

var legacyPublishedV020ForbiddenPaths = []string{
	"inlaid.exe", "update-portable.ps1", "go.mod", ".git", "cmd", "internal",
}

// ImportPortable imports only settings and custom filters from a portable root.
// It never discovers roots, overwrites files, moves media, or copies recovery
// tapes. Callers must present the root selected by the user.
func ImportPortable(sourceRoot string, destination Layout) (ImportReport, error) {
	return importPortable(sourceRoot, destination, applyImportPlan)
}

func importPortable(sourceRoot string, destination Layout, apply importApplier) (ImportReport, error) {
	if destination.Mode != Installed {
		return ImportReport{}, fmt.Errorf("portable import requires an installed destination, got %q", destination.Mode)
	}
	if err := destination.Validate(); err != nil {
		return ImportReport{}, err
	}
	sourceRoot = strings.TrimSpace(sourceRoot)
	if sourceRoot == "" {
		return ImportReport{}, errors.New("portable import root is empty")
	}
	sourceRoot = filepath.Clean(sourceRoot)
	if err := directDirectory(sourceRoot); err != nil {
		return ImportReport{}, fmt.Errorf("portable import root: %w", err)
	}
	if err := validatePortableImportRoot(sourceRoot); err != nil {
		return ImportReport{}, err
	}
	if err := requireEmptyRecovery(filepath.Join(sourceRoot, "recordings", ".recovery")); err != nil {
		return ImportReport{}, err
	}

	settingsPlan, err := preflightPortableSettings(sourceRoot, destination.SettingsFile)
	if err != nil {
		return ImportReport{}, fmt.Errorf("import settings: %w", err)
	}

	filters, err := preflightFilters(filepath.Join(sourceRoot, "filters"), destination.FiltersDir)
	if err != nil {
		return ImportReport{}, err
	}
	plans := append([]importPlan{settingsPlan}, filters...)
	report := ImportReport{}
	for _, plan := range plans {
		item, err := apply(plan)
		if err != nil {
			failed := plan.item
			failed.Action = ImportSkipped
			failed.Detail = "not copied: " + err.Error()
			report.Items = append(report.Items, failed)
			return report, fmt.Errorf("import %s: %w", plan.item.Path, err)
		}
		report.Items = append(report.Items, item)
	}
	report.Items = append(report.Items,
		ImportItem{Path: filepath.Join(sourceRoot, "recordings"), Action: ImportRetained, Detail: "recordings stay in the portable folder"},
		ImportItem{Path: filepath.Join(sourceRoot, "snapshots"), Action: ImportRetained, Detail: "snapshots stay in the portable folder"},
		ImportItem{Path: filepath.Join(sourceRoot, "support-reports"), Action: ImportRetained, Detail: "support reports stay in the portable folder"},
	)
	return report, nil
}

func preflightPortableSettings(sourceRoot, destination string) (importPlan, error) {
	current := filepath.Join(sourceRoot, "inlaid-settings.json")
	if err := directFile(current); err == nil {
		return preflightImportFile(current, destination)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return importPlan{}, fmt.Errorf("current settings source: %w", err)
	}

	legacy := filepath.Join(sourceRoot, "webcam-settings.json")
	if err := directFile(legacy); err == nil {
		plan, err := preflightImportFile(legacy, destination)
		if err != nil {
			return importPlan{}, fmt.Errorf("legacy settings source: %w", err)
		}
		if plan.item.Action == ImportCopied {
			plan.item.Detail = "copied legacy webcam-settings.json to the current settings name without removing the portable source"
		}
		return plan, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return importPlan{}, fmt.Errorf("legacy settings source: %w", err)
	}

	return importPlan{item: ImportItem{
		Path: current, Action: ImportSkipped,
		Detail: "current and legacy source settings files are absent",
	}}, nil
}

func validatePortableImportRoot(root string) error {
	marker := filepath.Join(root, PortableMarker)
	if err := directFile(marker); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("portable import marker: %w", err)
	}
	return validateLegacyPublishedV020Root(root)
}

func validateLegacyPublishedV020Root(root string) error {
	for _, relative := range legacyPublishedV020RequiredDirectories {
		path := filepath.Join(root, relative)
		if err := directDirectory(path); err != nil {
			return fmt.Errorf("markerless portable import is not the published v0.2.0-beta.1 layout: require direct %s directory: %w", relative, err)
		}
	}
	for _, relative := range legacyPublishedV020RequiredFiles {
		path := filepath.Join(root, relative)
		if err := directFile(path); err != nil {
			return fmt.Errorf("markerless portable import is not the published v0.2.0-beta.1 layout: require %s: %w", relative, err)
		}
	}
	for _, relative := range legacyPublishedV020ForbiddenPaths {
		path := filepath.Join(root, relative)
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect markerless portable import path %s: %w", relative, err)
		}
		return fmt.Errorf("markerless portable import is ambiguous because %s is present", relative)
	}
	return nil
}

func directDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("must be a direct directory")
	}
	return nil
}

func directFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("must be a direct regular file")
	}
	return nil
}

func requireEmptyRecovery(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect portable recovery: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("portable recovery must be a direct directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("inspect portable recovery: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("portable import refuses a root with live recovery tapes; finish or abandon recovery in the portable copy first")
	}
	return nil
}

func preflightImportFile(source, destination string) (importPlan, error) {
	if err := directFile(source); errors.Is(err, fs.ErrNotExist) {
		return importPlan{item: ImportItem{Path: source, Action: ImportSkipped, Detail: "source file is absent"}}, nil
	} else if err != nil {
		return importPlan{}, err
	}
	sourceData, err := os.ReadFile(source)
	if err != nil {
		return importPlan{}, err
	}
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return importPlan{}, errors.New("destination must be a direct regular file")
		}
		existing, err := os.ReadFile(destination)
		if err != nil {
			return importPlan{}, err
		}
		if bytes.Equal(existing, sourceData) {
			return importPlan{item: ImportItem{Path: destination, Action: ImportSkipped, Detail: "destination already has identical content"}}, nil
		}
		return importPlan{item: ImportItem{Path: destination, Action: ImportConflict, Detail: "destination already exists with different content"}}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return importPlan{}, fmt.Errorf("inspect destination: %w", err)
	}
	if err := directDestinationParent(destination); err != nil {
		return importPlan{}, err
	}
	return importPlan{
		item:        ImportItem{Path: destination, Action: ImportCopied, Detail: "copied without removing the portable source"},
		destination: destination,
		data:        sourceData,
		copy:        true,
	}, nil
}

func directDestinationParent(destination string) error {
	for parent := filepath.Dir(destination); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("destination parent must be a direct directory")
			}
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect destination parent: %w", err)
		}
		if next := filepath.Dir(parent); next == parent {
			return errors.New("destination parent does not have an existing directory")
		}
	}
}

func applyImportPlan(plan importPlan) (ImportItem, error) {
	if !plan.copy {
		return plan.item, nil
	}
	if err := os.MkdirAll(filepath.Dir(plan.destination), 0o700); err != nil {
		return ImportItem{}, err
	}
	output, err := os.OpenFile(plan.destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ImportItem{}, err
	}
	complete := false
	defer func() {
		_ = output.Close()
		if !complete {
			_ = os.Remove(plan.destination)
		}
	}()
	if _, err := output.Write(plan.data); err != nil {
		return ImportItem{}, err
	}
	if err := output.Sync(); err != nil {
		return ImportItem{}, err
	}
	if err := output.Close(); err != nil {
		return ImportItem{}, err
	}
	complete = true
	return plan.item, nil
}

func preflightFilters(sourceDir, destinationDir string) ([]importPlan, error) {
	info, err := os.Lstat(sourceDir)
	if errors.Is(err, fs.ErrNotExist) {
		return []importPlan{{item: ImportItem{Path: sourceDir, Action: ImportSkipped, Detail: "source filters directory is absent"}}}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("portable filters must be a direct directory")
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, err
	}
	plans := make([]importPlan, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".cube") {
			continue
		}
		plan, err := preflightImportFile(filepath.Join(sourceDir, entry.Name()), filepath.Join(destinationDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("import filter %q: %w", entry.Name(), err)
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		plans = append(plans, importPlan{item: ImportItem{Path: sourceDir, Action: ImportSkipped, Detail: "no top-level .cube filters to import"}})
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].item.Path < plans[j].item.Path })
	return plans, nil
}
