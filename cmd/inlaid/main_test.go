package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Melty1000/inlaid/internal/dashboard"
)

type trueColorProbe struct{}

func (trueColorProbe) Init() tea.Cmd { return nil }

func (m trueColorProbe) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }

func (trueColorProbe) View() tea.View {
	var view tea.View
	view.SetContent("\x1b[38;2;250;60;140;48;2;15;25;35mTRUECOLOR\x1b[0m")
	return view
}

func TestUIForcesTrueColorDespiteParentOptOut(t *testing.T) {
	var output bytes.Buffer
	options := append(uiProgramOptions(),
		tea.WithInput(nil),
		tea.WithOutput(&output),
		tea.WithEnvironment([]string{"NO_COLOR=1", "TERM=dumb"}),
		tea.WithWindowSize(40, 8),
		tea.WithoutSignals(),
	)
	program := tea.NewProgram(trueColorProbe{}, options...)
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	program.Quit()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Bubble Tea program did not stop")
	}

	rendered := output.String()
	if !strings.Contains(rendered, "38;2;250;60;140") && !strings.Contains(rendered, "38:2::250:60:140") {
		t.Fatalf("truecolor foreground was stripped under NO_COLOR/TERM=dumb: %q", rendered)
	}
	if !strings.Contains(rendered, "48;2;15;25;35") && !strings.Contains(rendered, "48:2::15:25:35") {
		t.Fatalf("truecolor background was stripped under NO_COLOR/TERM=dumb: %q", rendered)
	}
}

func TestParseSizeRejectsAllocationSizedInput(t *testing.T) {
	if _, _, err := parseSize("2147483647x2147483647"); err == nil {
		t.Fatal("allocation-sized render preview was accepted")
	}
	value := "600x200"
	w, h, err := parseSize(value)
	if err != nil || w != dashboard.MaximumDashboardWidth || h != dashboard.MaximumDashboardHeight {
		t.Fatalf("parseSize(%q) = %dx%d, %v", value, w, h, err)
	}
}

func TestSettingsPathsReadLegacyButSaveAsInlaid(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "inlaid-settings.json")
	legacy := filepath.Join(root, "webcam-settings.json")

	if save, load := settingsPathsForRoots([]string{root}); save != current || load != current {
		t.Fatalf("new install paths = save %q, load %q; want %q", save, load, current)
	}
	if err := os.WriteFile(legacy, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if save, load := settingsPathsForRoots([]string{root}); save != current || load != legacy {
		t.Fatalf("legacy install paths = save %q, load %q; want save %q, load %q", save, load, current, legacy)
	}
	if got := compatibleSettingsLoadPath(current); got != legacy {
		t.Fatalf("explicit current settings fallback = %q, want %q", got, legacy)
	}
	if err := os.WriteFile(current, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if save, load := settingsPathsForRoots([]string{root}); save != current || load != current {
		t.Fatalf("current paths = save %q, load %q; want %q", save, load, current)
	}
}

func TestPackagedExecutableKeepsDataBesideItsInstall(t *testing.T) {
	roots := settingsRoots(`C:\Users\Alice`, `E:\Apps\Inlaid\bin\inlaid.exe`)
	want := []string{`E:\Apps\Inlaid`, `C:\Users\Alice`}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("settingsRoots() = %#v, want %#v", roots, want)
	}
}

func TestPathEqualityFollowsHostCaseRules(t *testing.T) {
	if !pathEqual("windows", `C:\Inlaid`, `c:\inlaid`) {
		t.Fatal("Windows paths with case-only differences were treated as distinct")
	}
	if pathEqual("linux", "/opt/Inlaid", "/opt/inlaid") {
		t.Fatal("Linux paths with case-only differences were treated as equal")
	}
	if pathEqual("darwin", "/Applications/Inlaid", "/Applications/inlaid") {
		t.Fatal("macOS path comparison must preserve the mounted filesystem's spelling")
	}
}
