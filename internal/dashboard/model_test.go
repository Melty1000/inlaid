package dashboard

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type cameraSwitchProbe struct {
	RuntimeClient
	calls []string
}

func (p *cameraSwitchProbe) SelectCamera(name string) {
	p.calls = append(p.calls, "select:"+name)
}

func (p *cameraSwitchProbe) UpdateView(view ViewOptions) {
	p.calls = append(p.calls, fmt.Sprintf("view:%d", view.Version))
}

func (p *cameraSwitchProbe) Save(Settings) {
	p.calls = append(p.calls, "save")
}

type previewFixtureRuntime struct {
	RuntimeClient
	previews  chan PreviewUpdate
	events    chan RuntimeEvent
	accepted  chan struct{}
	mu        sync.Mutex
	sequence  uint64
	ackOnce   sync.Once
	closeOnce sync.Once
}

func newPreviewFixtureRuntime() *previewFixtureRuntime {
	return &previewFixtureRuntime{
		previews: make(chan PreviewUpdate, 1),
		events:   make(chan RuntimeEvent),
		accepted: make(chan struct{}),
	}
}

func (r *previewFixtureRuntime) Start(view ViewOptions) { r.send(view) }
func (r *previewFixtureRuntime) UpdateView(view ViewOptions) {
	r.send(view)
}
func (r *previewFixtureRuntime) Previews() <-chan PreviewUpdate { return r.previews }
func (r *previewFixtureRuntime) Events() <-chan RuntimeEvent    { return r.events }
func (r *previewFixtureRuntime) Close() error {
	r.closeOnce.Do(func() {
		close(r.previews)
		close(r.events)
	})
	return nil
}

func (r *previewFixtureRuntime) send(view ViewOptions) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sequence++
	update := PreviewUpdate{
		Version: view.Version, ANSI: "\x1b[38;2;120;210;255m▀\x1b[0m", Columns: 1, Rows: 1,
		Sequence: r.sequence, SourceFPS: 30, ShownFPS: 30,
		acknowledge: func(accepted bool) {
			if accepted {
				r.ackOnce.Do(func() { close(r.accepted) })
			}
		},
	}
	select {
	case r.previews <- update:
	default:
		old := <-r.previews
		old.acknowledgeRendered(false)
		r.previews <- update
	}
}

func TestCameraSwitchFencesPreviewAlreadyReceivedByModel(t *testing.T) {
	probe := &cameraSwitchProbe{}
	m := NewLive(DefaultSettings(), probe, nil)
	m.camera.Options, m.camera.Index, m.camera.Enabled = []string{"OLD", "NEW"}, 0, true
	m.viewVersion = 7
	m.liveANSI = "OLD FRAME"

	m.adjust(m.camera.ID, 1)
	if got := strings.Join(probe.calls, ","); !strings.HasPrefix(got, "select:NEW,view:8") {
		t.Fatalf("camera switch order = %q, want runtime generation before view fence", got)
	}
	if m.liveANSI != "" {
		t.Fatalf("camera switch retained old frame %q", m.liveANSI)
	}

	acknowledged := true
	stale := PreviewUpdate{
		Version: 7,
		ANSI:    "STALE FRAME",
		acknowledge: func(accepted bool) {
			acknowledged = accepted
		},
	}
	updated, _ := m.Update(previewMsg{stale})
	m = updated.(Model)
	if acknowledged || m.liveANSI != "" {
		t.Fatalf("stale preview accepted=%v live=%q", acknowledged, m.liveANSI)
	}
}

func TestBubbleTeaProgramReceivesRuntimePreview(t *testing.T) {
	cfg := DefaultSettings()
	runtime := newPreviewFixtureRuntime()
	defer runtime.Close()
	model := NewLive(cfg, runtime, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	program := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithWindowSize(120, 38),
	)
	go func() {
		select {
		case <-runtime.accepted:
			program.Send(tea.Quit())
		case <-ctx.Done():
		}
	}()
	final, err := program.Run()
	if err != nil {
		t.Fatal(err)
	}
	got := final.(Model)
	if got.liveANSI == "" || got.sequence == 0 {
		t.Fatalf("Bubble Tea received no runtime preview: state=%s version=%d", got.cameraState, got.viewVersion)
	}
	if strings.Contains(ansi.Strip(got.rendered), "WAITING FOR CAMERA") {
		t.Fatal("Bubble Tea stored a preview but left the waiting frame rendered")
	}
	for _, character := range got.rendered {
		if character < 0x20 && character != '\n' && character != '\x1b' {
			t.Fatalf("rendered live page contains unexpected control U+%04X", character)
		}
	}
}

func TestBubbleTeaRendererFlushesRuntimePreview(t *testing.T) {
	cfg := DefaultSettings()
	runtime := newPreviewFixtureRuntime()
	defer runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var output bytes.Buffer
	program := tea.NewProgram(
		NewLive(cfg, runtime, nil),
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(&output),
		tea.WithWindowSize(120, 38),
	)
	go func() {
		select {
		case <-runtime.accepted:
			timer := time.NewTimer(100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return
			}
			program.Send(tea.Quit())
		case <-ctx.Done():
		}
	}()
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.ContainsAny(output.String(), "▘▝▀▖▌▞▛") {
		t.Fatal("Bubble Tea renderer never flushed a canonical block cell")
	}
}

func TestDistilledPageAndCameraAspectExportGrid(t *testing.T) {
	m := New(DefaultSettings())
	m.SetSize(80, 24)
	page := ansi.Strip(m.RenderPreview())
	if got := len(strings.Split(page, "\n")); got != 24 {
		t.Fatalf("80x24 preview rendered %d lines", got)
	}
	for _, want := range []string{"FILL WINDOW", "SHOW WHOLE CAMERA", "COLOR LOOK", "SAVE AS", "RECORD MP4", "FOLDER"} {
		if !strings.Contains(page, want) {
			t.Fatalf("preview missing %q", want)
		}
	}
	for _, hidden := range []string{"PROCESS CAPACITY", "Cells 148x"} {
		if strings.Contains(page, hidden) {
			t.Fatalf("collapsed page leaked diagnostic %q", hidden)
		}
	}
	m.activate(m.details.ID)
	detailed := ansi.Strip(m.RenderPreview())
	for _, want := range []string{"Camera 1080p", "Terminal"} {
		if !strings.Contains(detailed, want) {
			t.Fatalf("expanded details missing %q", want)
		}
	}

}

func TestSavedCustomLookStaysSelectedThroughDiscovery(t *testing.T) {
	cfg := DefaultSettings()
	cfg.ColorLook = "Night Film"
	m := New(cfg)
	if got := m.colorLook.Value(); got != "Night Film" {
		t.Fatalf("initial color look = %q, want saved custom look", got)
	}
	m.setLooks([]string{"Night Film", "Day Film"})
	if got := m.colorLook.Value(); got != "Night Film" {
		t.Fatalf("discovered color look = %q, want saved custom look", got)
	}
}

func TestLookMixIsDisabledWhenNoColorLookIsSelected(t *testing.T) {
	m := New(DefaultSettings())
	if m.lookStrength.Enabled {
		t.Fatal("look mix is enabled while COLOR LOOK is NONE")
	}
	m.colorLook.Index = indexOfFold(m.colorLook.Options, "WARM")
	m.syncEnabled()
	if !m.lookStrength.Enabled {
		t.Fatal("look mix stayed disabled for an active color look")
	}
}

func TestSmallWindowShowsResizeState(t *testing.T) {
	m := New(DefaultSettings())
	m.SetSize(60, 18)
	page := ansi.Strip(m.RenderPreview())
	if !strings.Contains(page, "WINDOW TOO SMALL") || !strings.Contains(page, "80 columns wide") || !strings.Contains(page, "24 rows") {
		t.Fatalf("small window did not show the explicit resize state")
	}
}

func TestCompactCameraAndFormatControlsStayTruthful(t *testing.T) {
	m := New(DefaultSettings())
	m.SetSize(80, 24)
	m.camera.Options = []string{"c922 Pro Stream Webcam"}
	page := ansi.Strip(m.RenderPreview())
	if !strings.Contains(page, "SHOW WHOLE CAMERA") {
		t.Fatal("compact camera name truncated the framing choice")
	}

	m.saveAs.Index = 1
	m.loadFormatSettings()
	if len(m.size.Options) != 2 || m.size.Options[1] != "1080p" || m.fps.Max != 30 {
		t.Fatalf("GIF controls = %v, max FPS %d; want 720p/1080p and 30", m.size.Options, m.fps.Max)
	}

	control := SegmentedControl{Label: "SIZE", Options: []string{"720p", "1080p"}, Index: 1}
	optionWidth := (len("720p") + 2) + 1 + (len("1080p") + 2)
	width := 2 + len(control.Label) + 1 + optionWidth + 1
	firstCenter := width - optionWidth - 1 + (len("720p")+2)/2
	if got := segmentIndexAt(control, firstCenter, width); got != 0 {
		t.Fatalf("clicking 720p selected option %d", got)
	}
}

func TestMousePressReleaseActivatesSameButton(t *testing.T) {
	m := New(DefaultSettings())
	m.SetSize(120, 38)
	x, y := findHit(t, m.hitMap, "transport.live")

	updated, _ := m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	m = updated.(Model)
	if m.paused {
		t.Fatal("mouse-down activated the button before release")
	}
	if m.pressed != "transport.live" {
		t.Fatalf("pressed state = %q, want transport.live", m.pressed)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "┏") {
		t.Fatal("pressed state did not render the brief heavy-border pulse")
	}

	updated, cmd := m.Update(tea.MouseReleaseMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	m = updated.(Model)
	if !m.paused {
		t.Fatal("mouse-up on the pressed button did not activate it")
	}
	if cmd == nil {
		t.Fatal("successful release did not schedule the pressed-state release")
	}
}

func TestTransportIgnoresDoubleClickAndHeldKeyRepeat(t *testing.T) {
	m := New(DefaultSettings())
	started := time.Unix(100, 0)
	m.now = started
	if !m.allowTransportAction("transport.record") {
		t.Fatal("first record activation was rejected")
	}
	m.now = started.Add(283 * time.Millisecond)
	if m.allowTransportAction("transport.record") {
		t.Fatal("double-click was allowed to turn record into an immediate stop")
	}
	m.now = started.Add(transportRepeatGuard)
	if !m.allowTransportAction("transport.record") {
		t.Fatal("intentional stop remained locked after the repeat guard")
	}

	repeatedR := tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r", IsRepeat: true})
	updated, cmd := m.Update(repeatedR)
	m = updated.(Model)
	if cmd != nil || m.recordState != "idle" {
		t.Fatal("held R key started or toggled a recording")
	}
}

func TestDashboardReservesRightMarginAndKeepsFixedHeight(t *testing.T) {
	m := New(DefaultSettings())
	m.SetSize(120, 38)
	states := []func(){
		func() {},
		func() { m.details.Value = true },
		func() { m.recordState, m.recordFormat = "recording", "MP4" },
		func() { m.snapshotSaving = true },
	}
	for i, apply := range states {
		apply()
		page := m.RenderPreview()
		if got := lipgloss.Height(page); got != 38 {
			t.Fatalf("state %d rendered %d rows, want 38", i, got)
		}
		if got := lipgloss.Width(page); got > 119 {
			t.Fatalf("state %d rendered %d columns, want at most 119", i, got)
		}
	}
}

func findHit(t *testing.T, comp *lipgloss.Compositor, id string) (int, int) {
	t.Helper()
	if comp == nil {
		t.Fatal("dashboard hit map was not built")
	}
	for y := comp.Bounds().Min.Y; y < comp.Bounds().Max.Y; y++ {
		for x := comp.Bounds().Min.X; x < comp.Bounds().Max.X; x++ {
			if comp.Hit(x, y).ID() == id {
				return x, y
			}
		}
	}
	t.Fatalf("hit region %q was not rendered", id)
	return 0, 0
}

func BenchmarkDashboardView(b *testing.B) {
	m := New(DefaultSettings())
	m.SetSize(160, 45)
	b.ResetTimer()
	for range b.N {
		view := m.View()
		if lipgloss.Height(view.Content) != 45 {
			b.Fatal("dashboard height changed")
		}
	}
}

func BenchmarkDashboardLiveFrameSplice(b *testing.B) {
	runtime := NewRuntime(DefaultSettings(), filepath.Join(b.TempDir(), "settings.json"), b.TempDir())
	b.Cleanup(func() { _ = runtime.Close() })
	m := NewLive(DefaultSettings(), runtime, nil)
	m.SetSize(160, 45)
	m.liveColumns, m.liveRows = 154, 31
	line := "\x1b[38;2;210;80;150;48;2;18;20;26m" + strings.Repeat("▀", m.liveColumns) + "\x1b[0m"
	m.liveANSI = strings.Repeat(line+"\n", m.liveRows-1) + line
	m.refreshRender()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.refreshPreviewRender()
	}
}

func BenchmarkDashboardFullRefreshWithLiveFrame(b *testing.B) {
	runtime := NewRuntime(DefaultSettings(), filepath.Join(b.TempDir(), "settings.json"), b.TempDir())
	b.Cleanup(func() { _ = runtime.Close() })
	m := NewLive(DefaultSettings(), runtime, nil)
	m.SetSize(160, 45)
	m.liveColumns, m.liveRows = 154, 31
	line := "\x1b[38;2;210;80;150;48;2;18;20;26m" + strings.Repeat("▀", m.liveColumns) + "\x1b[0m"
	m.liveANSI = strings.Repeat(line+"\n", m.liveRows-1) + line
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.refreshRender()
	}
}
