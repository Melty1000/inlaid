package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Melty1000/inlaid/internal/dashboard"
	"github.com/charmbracelet/colorprofile"
)

// version is replaced by the release workflow with -ldflags. Source builds
// intentionally identify themselves as development builds.
var version = "dev"

func main() {
	renderPreview := flag.String("render-preview", "", "render one deterministic page at WIDTHxHEIGHT")
	settingsPath := flag.String("settings", "", "path to inlaid-settings.json")
	showVersion := flag.Bool("version", false, "print the Inlaid version")
	flag.Parse()
	if *showVersion {
		fmt.Println("Inlaid " + version)
		return
	}

	path := strings.TrimSpace(*settingsPath)
	loadPath := path
	if path == "" {
		path, loadPath = defaultSettingsPaths()
	} else {
		loadPath = compatibleSettingsLoadPath(path)
	}
	settings, settingsErr := dashboard.LoadSettings(loadPath)

	if strings.TrimSpace(*renderPreview) != "" {
		width, height, err := parseSize(*renderPreview)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		model := dashboard.New(settings)
		model.SetSize(width, height)
		fmt.Println(model.RenderPreview())
		return
	}

	absoluteSettings, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Inlaid:", err)
		os.Exit(1)
	}
	runtime := dashboard.NewRuntime(settings, absoluteSettings, filepath.Dir(absoluteSettings))
	model := dashboard.NewLive(settings, runtime, settingsErr)
	_, runErr := tea.NewProgram(model, uiProgramOptions()...).Run()
	closeErr := runtime.Close()
	if err := errors.Join(runErr, closeErr); err != nil {
		fmt.Fprintln(os.Stderr, "Inlaid:", err)
		os.Exit(1)
	}
}

func uiProgramOptions() []tea.ProgramOption {
	// Inlaid runs inside Windows Terminal, which supports 24-bit color.
	// Force the renderer profile so an unrelated parent-shell NO_COLOR or
	// TERM=dumb setting cannot discard the RGB pairs in every camera cell.
	return []tea.ProgramOption{
		tea.WithFPS(60),
		tea.WithColorProfile(colorprofile.TrueColor),
	}
}

func parseSize(value string) (int, int, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("--render-preview must use WIDTHxHEIGHT, for example 120x38")
	}
	width, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || width < 60 || height < 18 {
		return 0, 0, fmt.Errorf("--render-preview requires at least 60x18 cells")
	}
	if width > dashboard.MaximumDashboardWidth || height > dashboard.MaximumDashboardHeight {
		return 0, 0, fmt.Errorf("--render-preview is limited to %dx%d cells", dashboard.MaximumDashboardWidth, dashboard.MaximumDashboardHeight)
	}
	return width, height, nil
}

func defaultSettingsPaths() (savePath, loadPath string) {
	roots := make([]string, 0, 2)
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, cwd)
	}
	if executable, err := os.Executable(); err == nil {
		root := filepath.Dir(filepath.Dir(executable))
		if len(roots) == 0 || !strings.EqualFold(roots[0], root) {
			roots = append(roots, root)
		}
	}

	return settingsPathsForRoots(roots)
}

func settingsPathsForRoots(roots []string) (savePath, loadPath string) {
	const currentName = "inlaid-settings.json"
	const legacyName = "webcam-settings.json"

	for _, root := range roots {
		candidate := filepath.Join(root, currentName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, candidate
		}
	}
	// Compatibility read for pre-Inlaid local installs. Runtime saves use the
	// new filename, so the old file is never copied, overwritten, or removed.
	for _, root := range roots {
		candidate := filepath.Join(root, legacyName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return filepath.Join(root, currentName), candidate
		}
	}
	if len(roots) > 0 {
		candidate := filepath.Join(roots[0], currentName)
		return candidate, candidate
	}
	return currentName, currentName
}

func compatibleSettingsLoadPath(savePath string) string {
	if info, err := os.Stat(savePath); err == nil && !info.IsDir() {
		return savePath
	}
	if !strings.EqualFold(filepath.Base(savePath), "inlaid-settings.json") {
		return savePath
	}
	legacy := filepath.Join(filepath.Dir(savePath), "webcam-settings.json")
	if info, err := os.Stat(legacy); err == nil && !info.IsDir() {
		return legacy
	}
	return savePath
}
