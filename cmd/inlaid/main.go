package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Melty1000/inlaid/internal/applayout"
	"github.com/Melty1000/inlaid/internal/dashboard"
	"github.com/Melty1000/inlaid/internal/supportreport"
	"github.com/charmbracelet/colorprofile"
)

// version is replaced by the release workflow with -ldflags. Source builds
// intentionally identify themselves as development builds.
var version = "dev"

func main() {
	renderPreview := flag.String("render-preview", "", "render one deterministic page at WIDTHxHEIGHT")
	settingsPath := flag.String("settings", "", "path to inlaid-settings.json")
	sourceRoot := flag.String("source-root", "", "source checkout data root")
	explicitRoot := flag.String("data-root", "", "explicit test data root")
	portableImport := flag.String("import-portable", "", "portable Inlaid folder to import into an installed copy")
	showVersion := flag.Bool("version", false, "print the Inlaid version")
	flag.Parse()
	if *showVersion {
		fmt.Println("Inlaid " + version)
		return
	}
	if strings.TrimSpace(*renderPreview) != "" {
		width, height, err := parseSize(*renderPreview)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		model := dashboard.New(dashboard.DefaultSettings())
		model.SetSize(width, height)
		fmt.Println(model.RenderPreview())
		return
	}

	workingDirectory, workingDirectoryErr := os.Getwd()
	if workingDirectoryErr != nil {
		fmt.Fprintln(os.Stderr, "Inlaid:", workingDirectoryErr)
		os.Exit(1)
	}
	executable, executableErr := os.Executable()
	if executableErr != nil {
		fmt.Fprintln(os.Stderr, "Inlaid:", executableErr)
		os.Exit(1)
	}
	layout, layoutErr := applayout.Resolve(applayout.ResolveOptions{
		Executable: executable, SourceRoot: *sourceRoot, ExplicitRoot: *explicitRoot, WorkingDir: workingDirectory,
	})
	if layoutErr != nil {
		fmt.Fprintln(os.Stderr, "Inlaid:", layoutErr)
		os.Exit(1)
	}
	if portableRoot := strings.TrimSpace(*portableImport); portableRoot != "" {
		report, err := applayout.ImportPortable(portableRoot, layout)
		for _, item := range report.Items {
			fmt.Printf("%s: %s (%s)\n", item.Action, item.Path, item.Detail)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "Inlaid:", err)
			os.Exit(1)
		}
		return
	}
	path := strings.TrimSpace(*settingsPath)
	if path == "" {
		path = layout.SettingsFile
	}
	loadPath := compatibleSettingsLoadPath(path)
	settings, settingsErr := dashboard.LoadSettings(loadPath)

	absoluteSettings, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Inlaid:", err)
		os.Exit(1)
	}
	layout.SettingsFile = absoluteSettings
	appRuntime, runtimeErr := dashboard.NewRuntimeWithLayout(settings, layout, currentBuildFacts())
	if runtimeErr != nil {
		fmt.Fprintln(os.Stderr, "Inlaid:", runtimeErr)
		os.Exit(1)
	}
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
