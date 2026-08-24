package startup

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestLaunchDecisionOnlyInterceptsPlainExplorerLaunch(t *testing.T) {
	tests := []struct {
		name        string
		parent      string
		environment map[string]string
		want        launchAction
	}{
		{name: "explorer", parent: `C:\Windows\explorer.exe`, environment: map[string]string{}, want: relaunchInTerminal},
		{name: "Windows Terminal", parent: `C:\Program Files\WindowsApps\WindowsTerminal.exe`, environment: map[string]string{}, want: continueLaunch},
		{name: "PowerShell", parent: `C:\Program Files\PowerShell\7\pwsh.exe`, environment: map[string]string{}, want: continueLaunch},
		{name: "Explorer with inherited terminal hint", parent: `C:\Windows\explorer.exe`, environment: map[string]string{"WT_SESSION": "session"}, want: relaunchInTerminal},
		{name: "failed Explorer relaunch", parent: `C:\Windows\explorer.exe`, environment: map[string]string{relaunchMarker: "1"}, want: relaunchFailed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := decideLaunch(test.parent, test.environment); got != test.want {
				t.Fatalf("decideLaunch() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestTerminalArgumentsPreserveExecutableAndArguments(t *testing.T) {
	executable := `E:\Inlaid Build\bin\inlaid.exe`
	root := `E:\Inlaid Build`
	arguments := []string{"--settings", `E:\Inlaid Build\inlaid-settings.json`, "", `quoted\"value`, "a;b"}
	want := []string{"-w", "new", "new-tab", "-d", root, "--", executable, "--settings", `E:\Inlaid Build\inlaid-settings.json`, "", `quoted\"value`, "a;b"}
	if got := terminalArguments(executable, root, arguments); !reflect.DeepEqual(got, want) {
		t.Fatalf("terminalArguments() = %#v, want %#v", got, want)
	}
}

func TestInstallRootUsesParentOfBin(t *testing.T) {
	if got := installRoot(`E:\Inlaid\bin\inlaid.exe`); got != `E:\Inlaid` {
		t.Fatalf("installRoot(bin) = %q", got)
	}
	if got := installRoot(`E:\Portable\inlaid.exe`); got != `E:\Portable` {
		t.Fatalf("installRoot(portable) = %q", got)
	}
}

func TestExplorerRelaunchUsesStructuredProcessArguments(t *testing.T) {
	var startedPath string
	var startedArguments []string
	var startedRoot string
	var startedEnvironment []string
	var notification string

	relaunched, err := relaunchFromExplorer(launchDependencies{
		arguments:        []string{"--settings", `E:\Inlaid Build\settings.json`, ""},
		environment:      []string{"PATH=original"},
		executable:       func() (string, error) { return `E:\Inlaid Build\bin\inlaid.exe`, nil },
		parentExecutable: func() (string, error) { return `C:\Windows\explorer.exe`, nil },
		lookPath:         func(name string) (string, error) { return `C:\WindowsApps\wt.exe`, nil },
		start: func(path string, arguments []string, root string, environment []string) error {
			startedPath = path
			startedArguments = append([]string(nil), arguments...)
			startedRoot = root
			startedEnvironment = append([]string(nil), environment...)
			return nil
		},
		notify: func(message string) { notification = message },
	})
	if err != nil || !relaunched {
		t.Fatalf("relaunchFromExplorer() = %v, %v", relaunched, err)
	}
	if notification != "" {
		t.Fatalf("unexpected notification %q", notification)
	}
	if startedPath != `C:\WindowsApps\wt.exe` || startedRoot != `E:\Inlaid Build` {
		t.Fatalf("started %q in %q", startedPath, startedRoot)
	}
	wantArguments := []string{"-w", "new", "new-tab", "-d", `E:\Inlaid Build`, "--", `E:\Inlaid Build\bin\inlaid.exe`, "--settings", `E:\Inlaid Build\settings.json`, ""}
	if !reflect.DeepEqual(startedArguments, wantArguments) {
		t.Fatalf("arguments = %#v, want %#v", startedArguments, wantArguments)
	}
	joinedEnvironment := strings.Join(startedEnvironment, "\n")
	if !strings.Contains(joinedEnvironment, relaunchMarker+"=1") {
		t.Fatalf("relaunch marker was not added: %q", joinedEnvironment)
	}
	if !strings.Contains(joinedEnvironment, launcherMarker+"=direct") {
		t.Fatalf("launcher marker was not added: %q", joinedEnvironment)
	}
}

func TestExplorerLaunchFailsClearlyWithoutWindowsTerminal(t *testing.T) {
	var notification string
	relaunched, err := relaunchFromExplorer(launchDependencies{
		environment:      []string{"PATH=original"},
		executable:       func() (string, error) { return `E:\Inlaid\bin\inlaid.exe`, nil },
		parentExecutable: func() (string, error) { return `C:\Windows\explorer.exe`, nil },
		lookPath:         func(string) (string, error) { return "", errors.New("not found") },
		start:            func(string, []string, string, []string) error { t.Fatal("start called"); return nil },
		notify:           func(message string) { notification = message },
	})
	if relaunched || err == nil {
		t.Fatalf("relaunchFromExplorer() = %v, %v", relaunched, err)
	}
	if !strings.Contains(notification, "Windows Terminal was not found") {
		t.Fatalf("notification = %q", notification)
	}
}
