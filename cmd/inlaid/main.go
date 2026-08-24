package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Melty1000/inlaid/internal/dashboard"
	"github.com/Melty1000/inlaid/internal/startup"
	"github.com/Melty1000/inlaid/internal/supportreport"
	"github.com/charmbracelet/colorprofile"
)

// version is replaced by the release workflow with -ldflags. Source builds
// intentionally identify themselves as development builds.
var version = "dev"

func main() {
	relaunched, err := startup.RelaunchFromExplorer()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Inlaid:", err)
		os.Exit(1)
	}
	if relaunched {
		return
	}

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
	appRuntime := dashboard.NewRuntimeWithBuild(settings, absoluteSettings, filepath.Dir(absoluteSettings), currentBuildFacts())
	model := dashboard.NewLive(settings, appRuntime, settingsErr)
	_, runErr := tea.NewProgram(model, uiProgramOptions()...).Run()
	closeErr := appRuntime.Close()
	if err := errors.Join(runErr, closeErr); err != nil {
		fmt.Fprintln(os.Stderr, "Inlaid:", err)
		os.Exit(1)
	}
}

func currentBuildFacts() supportreport.BuildFacts {
	facts := supportreport.BuildFacts{Version: version}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return facts
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			facts.Revision = setting.Value
		case "vcs.modified":
			facts.Modified = setting.Value == "true"
		}
	}
	return facts
}

func uiProgramOptions() []tea.ProgramOption {
	// The camera image requires 24-bit foreground and background colors. Keep an
	// unrelated parent-shell opt-out from discarding every RGB cell pair.
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
	cwd, _ := os.Getwd()
	executable, _ := os.Executable()
	return settingsPathsForRoots(settingsRoots(cwd, executable))
}

func settingsRoots(cwd, executable string) []string {
	roots := make([]string, 0, 2)
	executableDirectory := filepath.Dir(strings.TrimSpace(executable))
	if strings.EqualFold(filepath.Base(executableDirectory), "bin") {
		roots = append(roots, filepath.Dir(executableDirectory))
	}
	if cwd = strings.TrimSpace(cwd); cwd != "" && (len(roots) == 0 || !pathEqual(runtime.GOOS, roots[0], cwd)) {
		roots = append(roots, cwd)
	}
	if len(roots) == 0 && executableDirectory != "." {
		roots = append(roots, executableDirectory)
	}
	return roots
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
	if !pathEqual(runtime.GOOS, filepath.Base(savePath), "inlaid-settings.json") {
		return savePath
	}
	legacy := filepath.Join(filepath.Dir(savePath), "webcam-settings.json")
	if info, err := os.Stat(legacy); err == nil && !info.IsDir() {
		return legacy
	}
	return savePath
}

func pathEqual(goos, left, right string) bool {
	if goos == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
