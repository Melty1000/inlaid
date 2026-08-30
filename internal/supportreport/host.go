package supportreport

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type platformFacts struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Version      string `json:"version"`
	Distribution string `json:"distribution"`
	Kernel       string `json:"kernel"`
	GoVersion    string `json:"go_version"`
	LogicalCPUs  int    `json:"logical_cpus"`
}

type launchFacts struct {
	Terminal        string `json:"terminal"`
	TerminalVersion string `json:"terminal_version"`
	ShellHint       string `json:"shell_hint"`
	TrueColorHint   bool   `json:"truecolor_hint"`
}

type hostFacts struct {
	Platform platformFacts
	Launch   launchFacts
}

func collectHostFacts() hostFacts {
	version, distribution, kernel := platformRelease()
	return hostFacts{
		Platform: platformFacts{
			OS: runtime.GOOS, Architecture: runtime.GOARCH,
			Version: version, Distribution: distribution, Kernel: kernel,
			GoVersion: safeToken(runtime.Version(), 32), LogicalCPUs: boundedInt(runtime.NumCPU(), 1, 4096),
		},
		Launch: collectLaunchFacts(),
	}
}

func collectLaunchFacts() launchFacts {
	terminal := "unknown"
	switch {
	case envPresent("WT_SESSION"):
		terminal = "windows-terminal"
	case envPresent("WEZTERM_PANE"):
		terminal = "wezterm"
	case envPresent("KITTY_WINDOW_ID"):
		terminal = "kitty"
	case envPresent("ALACRITTY_WINDOW_ID"):
		terminal = "alacritty"
	case envPresent("VSCODE_INJECTION") || envPresent("VSCODE_GIT_IPC_HANDLE"):
		terminal = "vscode"
	default:
		if value, ok := os.LookupEnv("TERM_PROGRAM"); ok {
			terminal = recognizedTerminal(value)
		}
	}

	version := ""
	if value, ok := os.LookupEnv("TERM_PROGRAM_VERSION"); ok && terminal != "unknown" {
		version = safeDottedVersion(value, 32)
	}
	color := terminal == "windows-terminal"
	if value, ok := os.LookupEnv("COLORTERM"); ok {
		value = strings.ToLower(strings.TrimSpace(value))
		color = color || value == "truecolor" || value == "24bit"
	}
	return launchFacts{
		Terminal: terminal, TerminalVersion: version,
		ShellHint: recognizedShell(), TrueColorHint: color,
	}
}

func envPresent(name string) bool {
	value, ok := os.LookupEnv(name)
	return ok && strings.TrimSpace(value) != ""
}

func recognizedTerminal(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "apple_terminal":
		return "apple-terminal"
	case "iterm.app":
		return "iterm2"
	case "wezterm":
		return "wezterm"
	case "vscode":
		return "vscode"
	case "hyper":
		return "hyper"
	case "rio":
		return "rio"
	default:
		return "unknown"
	}
}

func recognizedShell() string {
	value := ""
	if candidate, ok := os.LookupEnv("SHELL"); ok {
		value = candidate
	} else if candidate, ok := os.LookupEnv("ComSpec"); ok {
		value = candidate
	}
	name := strings.ToLower(filepath.Base(strings.TrimSpace(value)))
	name = strings.TrimSuffix(name, filepath.Ext(name))
	switch name {
	case "sh", "bash", "zsh", "fish", "pwsh", "powershell", "cmd", "nu", "xonsh", "tcsh":
		return name
	default:
		return "unknown"
	}
}
