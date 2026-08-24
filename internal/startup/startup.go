package startup

import (
	"path/filepath"
	"strings"
)

const relaunchMarker = "INLAID_TERMINAL_RELAUNCHED"
const launcherMarker = "INLAID_LAUNCHER"

type launchAction uint8

const (
	continueLaunch launchAction = iota
	relaunchInTerminal
	relaunchFailed
)

func decideLaunch(parent string, environment map[string]string) launchAction {
	if !strings.EqualFold(filepath.Base(parent), "explorer.exe") {
		return continueLaunch
	}
	if strings.TrimSpace(environmentValue(environment, relaunchMarker)) != "" {
		return relaunchFailed
	}
	return relaunchInTerminal
}

func environmentValue(environment map[string]string, name string) string {
	if value, found := environment[name]; found {
		return value
	}
	for candidate, value := range environment {
		if strings.EqualFold(candidate, name) {
			return value
		}
	}
	return ""
}

func terminalArguments(executable, root string, arguments []string) []string {
	result := []string{"-w", "new", "new-tab", "-d", root, "--", executable}
	return append(result, arguments...)
}

func installRoot(executable string) string {
	directory := filepath.Dir(executable)
	if strings.EqualFold(filepath.Base(directory), "bin") {
		return filepath.Dir(directory)
	}
	return directory
}
