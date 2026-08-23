// THESIS: Inlaid is a camera-first terminal tool; it refuses the generic settings dashboard.
// OWN-WORLD: The user's terminal canvas, adaptive Charm-like signal colors, inline controls, bordered transport.
// STORY: See the camera and every consequential control together, then shape and capture without navigation.
// FIRST VIEWPORT: A wide live preview dominates above horizontal CAMERA and SAVE channels; transport anchors the bottom.
package dashboard

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	minimumDashboardWidth  = 80
	minimumDashboardHeight = 24
	// MaximumDashboardWidth/Height are defensive allocation bounds for both a
	// real terminal resize report and the deterministic render-preview CLI.
	// They remain well above practical Windows Terminal grids.
	MaximumDashboardWidth  = 600
	MaximumDashboardHeight = 200
	// Transport controls are destructive toggles: a double-click on RECORD
	// must not be interpreted as START followed immediately by STOP. The same
	// guard prevents duplicate snapshots and pause/resume flicker.
	transportRepeatGuard = 750 * time.Millisecond
)

type uiTickMsg time.Time
type previewMsg struct{ PreviewUpdate }
type runtimeMsg struct{ RuntimeEvent }
type previewClosedMsg struct{}
type runtimeClosedMsg struct{}
type releasePressMsg struct {
	ID         string
	Generation uint64
}

type keyMap struct {
	Next, Previous, Left, Right, Activate key.Binding
	Quit, Record, Snapshot, Live          key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Next:     key.NewBinding(key.WithKeys("tab", "down"), key.WithHelp("tab", "next")),
		Previous: key.NewBinding(key.WithKeys("shift+tab", "up"), key.WithHelp("shift+tab", "previous")),
		Left:     key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/→", "change")),
		Right:    key.NewBinding(key.WithKeys("right", "l")),
		Activate: key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "activate")),
		Quit:     key.NewBinding(key.WithKeys("q", "esc", "ctrl+c"), key.WithHelp("q", "quit")),
		Record:   key.NewBinding(key.WithKeys("r")),
		Snapshot: key.NewBinding(key.WithKeys("s")),
		Live:     key.NewBinding(key.WithKeys("p")),
	}
}

type Model struct {
	theme    Theme
	settings Settings
	keys     keyMap
	preview  PreviewSource // deterministic fallback used only without a runtime
	runtime  RuntimeClient

	width, height int
	focus         int
	hover         string
	pressed       string
	mousePressID  string
	pressSequence uint64
	lastTransport string
	transportAt   time.Time
	now           time.Time
	rendered      string
	hitMap        *lipgloss.Compositor
	renderLines   []string
	previewPrefix []string
	previewSuffix []string
	previewCacheY int
	previewCacheW int
	previewCacheH int

	camera       Select
	view         SegmentedControl
	mirror       Toggle
	mosaicStyle  SegmentedControl
	colorLook    Select
	lookStrength Stepper
	details      Toggle
	saveAs, size SegmentedControl
	fps          Stepper
	quality      SegmentedControl
	transport    []Button
	focusOrder   []string

	viewVersion           uint64
	liveANSI              string
	liveColumns, liveRows int
	sourceFPS, shownFPS   float64
	dropped               uint64
	renderDuration        time.Duration
	sequence              uint64

	cameraState    string // finding, connecting, live, unavailable, demo
	cameraName     string
	cameraError    string
	captureWidth   int
	captureHeight  int
	captureFPS     float64
	captureBackend string
	paused         bool
	recordState    string // idle, starting, recording, saving, error
	recordFormat   string
	recordStarted  time.Time
	snapshotSaving bool
	quitting       bool

	flashUntil      time.Time
	toast           string
	toastUntil      time.Time
	persistentError string
	technicalError  string
}

func New(cfg Settings) Model { return newModel(cfg, nil, nil) }

// NewLive joins the one-page dashboard to the real camera/media runtime.
func NewLive(cfg Settings, runtime RuntimeClient, startupErr error) Model {
	return newModel(cfg, runtime, startupErr)
}

func newModel(cfg Settings, runtime RuntimeClient, startupErr error) Model {
	cfg.normalize()
	lookOptions := []string{"NONE", "WARM", "COOL", "MONO"}
	if !containsFold(lookOptions, cfg.ColorLook) {
		// Keep a saved custom look selected while its .cube file is discovered.
		// The runtime reconciles the same name as soon as the catalog is ready.
		lookOptions = append(lookOptions, cfg.ColorLook)
	}
	selectedSizeIndex := min(sizeIndex(cfg.RecordingWidth), 1)
	formatIndex := indexOfFold([]string{"MP4", "GIF"}, cfg.SaveFormat)
	if formatIndex == 1 {
		selectedSizeIndex = sizeIndex(cfg.GIFwidth)
		selectedSizeIndex = min(selectedSizeIndex, 1)
	}
	fps := cfg.RecordingFPS
	fpsMax := 60
	if formatIndex == 1 {
		fps = min(cfg.GIFfps, 30)
		fpsMax = 30
	}
	sizeOptions := []string{"720p", "1080p"}

	cameraOptions := []string{"DEMO"}
	cameraState := "demo"
	if runtime != nil {
		cameraOptions = []string{"FINDING CAMERAS…"}
		cameraState = "finding"
	}
	m := Model{
		theme: DefaultTheme(), settings: cfg, keys: defaultKeyMap(), preview: newDemoPreview(), runtime: runtime,
		width: 120, height: 38, cameraState: cameraState, recordState: "idle", viewVersion: 1,
		camera:       Select{ID: "camera.selector", Options: cameraOptions, MaxValueWidth: 28, Enabled: runtime == nil},
		view:         SegmentedControl{ID: "camera.view", Label: "VIEW", Options: []string{"FILL WINDOW", "SHOW WHOLE CAMERA"}, Index: indexOfFold([]string{"fill", "whole"}, cfg.Framing), Enabled: true},
		mirror:       Toggle{ID: "camera.mirror", Label: "MIRROR", Value: cfg.Mirror, Enabled: true},
		mosaicStyle:  SegmentedControl{ID: "camera.detail", Label: "DETAIL", Options: []string{"SOFT", "BALANCED", "CRISP"}, Index: symbolIndex(cfg.Symbols), Enabled: true},
		colorLook:    Select{ID: "camera.color-look", Label: "COLOR LOOK", Options: lookOptions, Index: indexOfFold(lookOptions, cfg.ColorLook), MaxValueWidth: 18, Enabled: true},
		lookStrength: Stepper{ID: "camera.look-strength", Label: "MIX", Value: cfg.LookStrength, Min: 0, Max: 100, Step: 10, Suffix: "%", Enabled: true},
		details:      Toggle{ID: "details", Label: "DETAILS", Enabled: true},
		saveAs:       SegmentedControl{ID: "save.format", Label: "SAVE AS", Options: []string{"MP4", "GIF"}, Index: formatIndex, Enabled: true},
		size:         SegmentedControl{ID: "save.size", Label: "SIZE", Options: sizeOptions, Index: selectedSizeIndex, Enabled: true},
		fps:          Stepper{ID: "save.fps", Label: "FPS", Value: fps, Min: 1, Max: fpsMax, Step: 1, Enabled: true},
		quality:      SegmentedControl{ID: "save.quality", Label: "QUALITY", Options: []string{"STANDARD", "HIGH"}, Index: indexOfFold([]string{"standard", "high"}, cfg.ExportQuality), Enabled: true},
		transport: []Button{
			{ID: "transport.live", Label: "PAUSE PREVIEW", Glyph: "II", Enabled: runtime == nil},
			{ID: "transport.record", Label: "RECORD", Glyph: "●", Tone: "danger"},
			{ID: "transport.snapshot", Label: "SNAPSHOT", Glyph: "◆"},
			{ID: "transport.open", Label: "OPEN FOLDER", Glyph: "↗", Enabled: true},
		},
	}
	if startupErr != nil {
		m.persistentError = "Settings could not be read. Safe defaults are active."
	}
	m.focusOrder = []string{
		m.camera.ID, m.view.ID, m.mirror.ID, m.mosaicStyle.ID, m.colorLook.ID, m.lookStrength.ID, m.details.ID,
		m.saveAs.ID, m.size.ID, m.fps.ID, m.quality.ID,
		"transport.live", "transport.record", "transport.snapshot", "transport.open",
	}
	m.syncEnabled()
	m.now = time.Now()
	m.refreshRender()
	return m
}

func (m Model) Init() tea.Cmd {
	if m.runtime != nil {
		m.runtime.Start(m.viewOptions())
		return tea.Batch(waitPreview(m.runtime), waitRuntime(m.runtime), uiTick(), tea.RequestBackgroundColor)
	}
	return tea.Batch(uiTick(), tea.RequestBackgroundColor)
}

func uiTick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return uiTickMsg(t) })
}

func waitPreview(runtime RuntimeClient) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-runtime.Previews()
		if !ok {
			return previewClosedMsg{}
		}
		return previewMsg{update}
	}
}

func waitRuntime(runtime RuntimeClient) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-runtime.Events()
		if !ok {
			return runtimeClosedMsg{}
		}
		return runtimeMsg{event}
	}
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.BackgroundColorMsg:
		m.theme = ThemeForBackground(msg.IsDark())
		m.refreshRender()
		return m, nil
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		m.pushView()
		return m, nil
	case uiTickMsg:
		previousNow := m.now
		m.now = time.Time(msg)
		dirty := false
		if m.runtime == nil && !m.paused {
			m.sequence++
			dirty = true
		}
		if m.toast != "" && !m.now.Before(m.toastUntil) {
			m.toast = ""
			dirty = true
		}
		if m.recordState == "recording" && elapsedSecond(previousNow, m.recordStarted) != elapsedSecond(m.now, m.recordStarted) {
			dirty = true
		}
		if previousNow.Before(m.flashUntil) != m.now.Before(m.flashUntil) {
			dirty = true
		}
		// Live preview messages already refresh the screen. Rebuilding the full
		// compositor on every clock tick creates a periodic 10+ ms stall for no
		// visible change.
		if dirty {
			m.refreshRender()
		}
		return m, uiTick()
	case previewMsg:
		accepted := msg.Version == m.viewVersion
		if accepted {
			m.liveANSI, m.liveColumns, m.liveRows = msg.ANSI, msg.Columns, msg.Rows
			m.sequence, m.sourceFPS, m.shownFPS = msg.Sequence, msg.SourceFPS, msg.ShownFPS
			m.dropped, m.renderDuration = msg.Dropped, msg.RenderDuration
			m.refreshPreviewRender()
		}
		msg.acknowledgeRendered(accepted)
		return m, waitPreview(m.runtime)
	case runtimeMsg:
		m.now = time.Now()
		cmd := m.applyRuntimeEvent(msg.RuntimeEvent)
		m.refreshRender()
		if cmd != nil {
			return m, cmd
		}
		return m, waitRuntime(m.runtime)
	case previewClosedMsg, runtimeClosedMsg:
		return m, nil
	case releasePressMsg:
		if msg.Generation == m.pressSequence && msg.ID == m.pressed {
			m.pressed = ""
			m.refreshRender()
		}
		return m, nil
	case tea.MouseMsg:
		dirty, cmd := m.handleMouse(msg)
		if dirty {
			m.refreshRender()
		}
		return m, cmd
	case tea.KeyPressMsg:
		// Windows Terminal reports held-key repeats. Repeated activation keys
		// must not toggle recording off or fire duplicate transport actions.
		repeated := msg.Key().IsRepeat
		switch {
		case key.Matches(msg, m.keys.Quit):
			if m.recordState == "recording" || m.recordState == "starting" {
				m.quitting, m.recordState = true, "saving"
				m.runtime.StopRecording()
				m.refreshRender()
				return m, nil
			}
			if m.recordState == "saving" {
				m.quitting = true
				m.refreshRender()
				return m, nil
			}
			return m, tea.Quit
		case key.Matches(msg, m.keys.Next):
			m.moveFocus(1)
		case key.Matches(msg, m.keys.Previous):
			m.moveFocus(-1)
		case key.Matches(msg, m.keys.Left):
			m.adjust(m.focusedID(), -1)
		case key.Matches(msg, m.keys.Right):
			m.adjust(m.focusedID(), 1)
		case key.Matches(msg, m.keys.Activate):
			if repeated {
				return m, nil
			}
			cmd := m.activatePressed(m.focusedID())
			m.refreshRender()
			return m, cmd
		case key.Matches(msg, m.keys.Record):
			if repeated {
				return m, nil
			}
			cmd := m.activatePressed("transport.record")
			m.refreshRender()
			return m, cmd
		case key.Matches(msg, m.keys.Snapshot):
			if repeated {
				return m, nil
			}
			cmd := m.activatePressed("transport.snapshot")
			m.refreshRender()
			return m, cmd
		case key.Matches(msg, m.keys.Live):
			if repeated {
				return m, nil
			}
			cmd := m.activatePressed("transport.live")
			m.refreshRender()
			return m, cmd
		}
		m.refreshRender()
		return m, nil
	}
	return m, nil
}

func (m *Model) applyRuntimeEvent(event RuntimeEvent) tea.Cmd {
	switch event.Kind {
	case RuntimeFindingCameras:
		m.cameraState = "finding"
	case RuntimeDevicesFound:
		m.setDevices(event.Devices, event.Device)
	case RuntimeConnecting:
		m.cameraState, m.cameraName, m.cameraError = "connecting", event.Device, ""
		m.captureWidth, m.captureHeight, m.captureFPS, m.captureBackend = 0, 0, 0, ""
		m.persistentError = ""
		m.liveANSI = ""
	case RuntimeCameraLive:
		m.cameraState = "live"
		if strings.EqualFold(event.Device, "DEMO") {
			m.cameraState = "demo"
		}
		m.cameraName, m.cameraError = event.Device, ""
		m.captureWidth, m.captureHeight, m.captureFPS, m.captureBackend = event.Width, event.Height, event.FPS, event.Backend
		m.technicalError = ""
		m.persistentError = ""
	case RuntimeCameraError:
		m.cameraState, m.cameraError = "unavailable", friendlyError(event.Err, "Camera unavailable — close other apps using it, then choose the camera again.")
		m.technicalError = errorText(event.Err)
		m.persistentError = m.cameraError
	case RuntimeRecordingStarting:
		m.recordState, m.recordFormat = "starting", strings.ToUpper(event.Format)
	case RuntimeRecordingStarted:
		m.recordState, m.recordFormat, m.recordStarted = "recording", strings.ToUpper(event.Format), m.clockNow()
	case RuntimeRecordingSaving:
		m.recordState = "saving"
	case RuntimeRecordingSaved:
		m.recordState = "idle"
		m.notify("Saved " + strings.ToUpper(event.Format) + " · " + shortPath(event.Path))
		if m.quitting {
			return tea.Quit
		}
	case RuntimeRecordingError:
		m.recordState = "error"
		m.technicalError = errorText(event.Err)
		m.persistentError = friendlyError(event.Err, "Recording stopped — the file could not be written.")
		if m.quitting {
			return tea.Quit
		}
	case RuntimeRecoveryStarting:
		m.recordState, m.recordFormat = "saving", "RECOVERY"
		m.persistentError = ""
	case RuntimeRecoverySaved:
		m.recordState = "idle"
		if event.Count == 1 {
			m.notify("Recovered recording · " + shortPath(event.Path))
		} else {
			m.notify(fmt.Sprintf("Recovered %d recordings · %s", event.Count, shortPath(event.Path)))
		}
		if m.quitting {
			return tea.Quit
		}
	case RuntimeRecoveryError:
		m.recordState = "error"
		m.technicalError = errorText(event.Err)
		if event.Count > 0 {
			m.persistentError = "A recovered recording was saved, but another previous recording still needs attention. Its CellTape was kept."
		} else {
			m.persistentError = "A previous recording could not be recovered. Its CellTape was kept so you can try again."
		}
		if m.quitting {
			return tea.Quit
		}
	case RuntimeSnapshotSaving:
		m.snapshotSaving = true
	case RuntimeSnapshotSaved:
		m.snapshotSaving = false
		m.flashUntil = m.clockNow().Add(500 * time.Millisecond)
		m.notify("Snapshot saved · " + shortPath(event.Path))
	case RuntimeSnapshotError:
		m.snapshotSaving = false
		m.technicalError = errorText(event.Err)
		m.persistentError = friendlyError(event.Err, "Snapshot could not be saved.")
	case RuntimeFolderOpened:
		m.notify("Opened output folder")
	case RuntimeFolderError:
		m.technicalError = errorText(event.Err)
		m.persistentError = friendlyError(event.Err, "The output folder could not be opened.")
	case RuntimeSettingsError:
		m.technicalError = errorText(event.Err)
		m.persistentError = "Settings could not be saved. Your current session is still running."
	case RuntimeLooksFound:
		m.setLooks(event.Looks)
		m.pushView()
		if event.Err != nil {
			m.technicalError = errorText(event.Err)
			m.notify("Some .cube looks were skipped · Details shows why")
		}
	}
	m.syncEnabled()
	return nil
}

func (m Model) View() tea.View {
	var view tea.View
	view.AltScreen = true
	view.MouseMode = tea.MouseModeAllMotion
	view.WindowTitle = "Inlaid"
	view.SetContent(m.rendered)
	return view
}

func (m *Model) SetSize(width, height int) {
	m.width = min(max(width, 1), MaximumDashboardWidth)
	m.height = min(max(height, 1), MaximumDashboardHeight)
	m.refreshRender()
}
func (m Model) RenderPreview() string {
	if m.sequence == 0 {
		m.sequence = 42
	}
	if m.now.IsZero() {
		m.now = time.Now()
	}
	return m.compose().Render()
}

func (m *Model) refreshRender() {
	comp := m.compose()
	m.hitMap = comp
	m.rendered = comp.Render()
	m.cachePreviewRows()
	m.stitchPreviewRows()
}

// cachePreviewRows extracts the stable dashboard shell around the camera
// interior. Live frames can then replace only those rows without rerunning the
// Lip Gloss compositor or rebuilding the mouse hit map 30 times per second.
func (m *Model) cachePreviewRows() {
	m.renderLines = strings.Split(m.rendered, "\n")
	m.previewPrefix, m.previewSuffix = nil, nil
	m.previewCacheY, m.previewCacheW, m.previewCacheH = 0, 0, 0
	if m.runtime == nil || m.width < minimumDashboardWidth || m.height < minimumDashboardHeight {
		return
	}
	boxWidth, boxHeight := m.previewAreaSize()
	previewY := 2
	if m.details.Value {
		previewY = 3
	}
	rows, columns := boxHeight-2, boxWidth-2
	lineY, interiorX := previewY+1, 3
	if rows <= 0 || columns <= 0 || lineY < 0 || lineY+rows > len(m.renderLines) {
		return
	}
	m.previewPrefix = make([]string, rows)
	m.previewSuffix = make([]string, rows)
	for row := 0; row < rows; row++ {
		line := m.renderLines[lineY+row]
		lineWidth := ansi.StringWidth(line)
		if lineWidth < interiorX+columns {
			m.previewPrefix, m.previewSuffix = nil, nil
			return
		}
		m.previewPrefix[row] = ansi.Cut(line, 0, interiorX)
		m.previewSuffix[row] = ansi.Cut(line, interiorX+columns, lineWidth)
		m.renderLines[lineY+row] = m.previewPrefix[row] + strings.Repeat(" ", columns) + m.previewSuffix[row]
	}
	m.previewCacheY, m.previewCacheW, m.previewCacheH = lineY, boxWidth, boxHeight
}

func (m *Model) refreshPreviewRender() {
	if len(m.previewPrefix) == 0 || len(m.renderLines) == 0 {
		m.refreshRender()
		return
	}
	m.stitchPreviewRows()
}

func (m *Model) stitchPreviewRows() {
	if len(m.previewPrefix) == 0 || len(m.renderLines) == 0 {
		return
	}
	interior, _, _ := m.previewInterior(m.previewCacheW, m.previewCacheH)
	if len(interior) != len(m.previewPrefix) {
		return
	}
	var builder strings.Builder
	builder.Grow(len(m.rendered) + len(m.liveANSI))
	for lineIndex, line := range m.renderLines {
		if lineIndex > 0 {
			builder.WriteByte('\n')
		}
		row := lineIndex - m.previewCacheY
		if row >= 0 && row < len(interior) {
			builder.WriteString(m.previewPrefix[row])
			builder.WriteString(interior[row])
			builder.WriteString(m.previewSuffix[row])
			continue
		}
		builder.WriteString(line)
	}
	m.rendered = builder.String()
}

func (m *Model) handleMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	if m.hitMap == nil {
		m.refreshRender()
	}
	mouse := msg.Mouse()
	hit := m.hitMap.Hit(mouse.X, mouse.Y)
	id := hit.ID()
	dirty := id != m.hover
	m.hover = id

	switch event := msg.(type) {
	case tea.MouseClickMsg:
		if event.Mouse().Button != tea.MouseLeft || !m.canActivate(id) {
			return dirty, nil
		}
		m.setFocus(id)
		m.mousePressID, m.pressed = id, id
		m.pressSequence++ // invalidate a pulse from an earlier interaction
		return true, nil
	case tea.MouseMotionMsg:
		if m.mousePressID != "" {
			pressed := ""
			if id == m.mousePressID {
				pressed = id
			}
			if pressed != m.pressed {
				m.pressed = pressed
				dirty = true
			}
		}
		return dirty, nil
	case tea.MouseReleaseMsg:
		pressID := m.mousePressID
		m.mousePressID = ""
		if pressID == "" {
			return dirty, nil
		}
		if id != pressID || !m.canActivate(pressID) {
			m.pressed = ""
			m.pressSequence++
			return true, nil
		}
		m.setFocus(pressID)
		m.activateMouse(pressID, mouse.X-hit.Bounds().Min.X, hit.Bounds().Dx())
		return true, m.press(pressID)
	}
	return dirty, nil
}

func (m *Model) press(id string) tea.Cmd {
	if id == "" {
		return nil
	}
	m.pressed = id
	m.pressSequence++
	generation := m.pressSequence
	return tea.Tick(110*time.Millisecond, func(time.Time) tea.Msg {
		return releasePressMsg{ID: id, Generation: generation}
	})
}

func (m *Model) activatePressed(id string) tea.Cmd {
	if !m.canActivate(id) {
		return nil
	}
	cmd := m.press(id)
	m.activate(id)
	return cmd
}

func (m Model) clockNow() time.Time {
	if m.now.IsZero() {
		return time.Now()
	}
	return m.now
}

func elapsedSecond(now, started time.Time) int64 {
	if now.IsZero() || started.IsZero() || now.Before(started) {
		return 0
	}
	return int64(now.Sub(started) / time.Second)
}

func (m *Model) activateMouse(id string, localX, width int) {
	if !m.canActivate(id) {
		return
	}
	switch id {
	case m.camera.ID, m.colorLook.ID, m.fps.ID, m.lookStrength.ID:
		if localX < width/2 {
			m.adjust(id, -1)
		} else {
			m.adjust(id, 1)
		}
	case m.view.ID:
		m.chooseSegment(id, segmentIndexAt(m.view, localX, width))
	case m.mosaicStyle.ID:
		if m.width < 110 {
			if localX < width/2 {
				m.adjust(id, -1)
			} else {
				m.adjust(id, 1)
			}
		} else {
			m.chooseSegment(id, segmentIndexAt(m.mosaicStyle, localX, width))
		}
	case m.saveAs.ID:
		m.chooseSegment(id, segmentIndexAt(m.saveAs, localX, width))
	case m.size.ID:
		m.chooseSegment(id, segmentIndexAt(m.size, localX, width))
	case m.quality.ID:
		m.chooseSegment(id, segmentIndexAt(m.quality, localX, width))
	default:
		m.activate(id)
	}
}

func segmentIndexAt(control SegmentedControl, localX, width int) int {
	if len(control.Options) == 0 {
		return 0
	}
	optionWidth := len(control.Options) - 1
	for _, option := range control.Options {
		optionWidth += len([]rune(option)) + 2
	}
	start := width - optionWidth - 1
	if start < 0 || localX < start {
		return control.Index
	}
	cursor := start
	for index, option := range control.Options {
		end := cursor + len([]rune(option)) + 2
		if localX < end {
			return index
		}
		cursor = end + 1
	}
	return len(control.Options) - 1
}

func (m *Model) chooseSegment(id string, index int) {
	switch id {
	case m.view.ID:
		m.view.Index = min(max(index, 0), len(m.view.Options)-1)
		m.afterControlChange(true)
	case m.mosaicStyle.ID:
		m.mosaicStyle.Index = min(max(index, 0), len(m.mosaicStyle.Options)-1)
		m.afterControlChange(true)
	case m.saveAs.ID:
		m.storeFormatSettings()
		m.saveAs.Index = min(max(index, 0), len(m.saveAs.Options)-1)
		m.loadFormatSettings()
		m.afterControlChange(false)
	case m.size.ID:
		m.size.Index = min(max(index, 0), len(m.size.Options)-1)
		m.afterControlChange(false)
	case m.quality.ID:
		m.quality.Index = min(max(index, 0), len(m.quality.Options)-1)
		m.afterControlChange(false)
	}
}

func (m *Model) activate(id string) {
	if !m.canActivate(id) {
		return
	}
	if strings.HasPrefix(id, "transport.") && !m.allowTransportAction(id) {
		return
	}
	switch id {
	case m.camera.ID, m.view.ID, m.mosaicStyle.ID, m.colorLook.ID, m.lookStrength.ID, m.saveAs.ID, m.size.ID, m.quality.ID, m.fps.ID:
		m.adjust(id, 1)
	case m.mirror.ID:
		m.mirror.Value = !m.mirror.Value
		m.afterControlChange(true)
	case m.details.ID:
		m.details.Value = !m.details.Value
		m.pushView()
	case "transport.live":
		if m.cameraState == "unavailable" {
			m.cameraState = "connecting"
			m.persistentError = ""
			m.beginCameraSwitch(m.camera.Value())
			break
		}
		m.paused = !m.paused
		m.notify(map[bool]string{true: "Preview paused · recordings hold this frame", false: "Preview resumed"}[m.paused])
		m.pushView()
	case "transport.record":
		if m.recordState == "recording" {
			m.recordState = "saving"
			m.runtime.StopRecording()
		} else if m.recordState == "idle" || m.recordState == "error" {
			m.recordState, m.recordFormat = "starting", m.saveAs.Value()
			m.persistentError = ""
			m.runtime.StartRecording(m.recordOptions())
		}
	case "transport.snapshot":
		m.snapshotSaving = true
		m.persistentError = ""
		m.runtime.Snapshot(m.recordOptions())
	case "transport.open":
		if m.runtime != nil {
			m.runtime.OpenFolder()
		}
	}
	m.syncEnabled()
}

func (m *Model) allowTransportAction(id string) bool {
	now := m.clockNow()
	if id == m.lastTransport && !m.transportAt.IsZero() && now.Sub(m.transportAt) < transportRepeatGuard {
		return false
	}
	m.lastTransport, m.transportAt = id, now
	return true
}

func (m *Model) adjust(id string, delta int) {
	if !m.canActivate(id) {
		return
	}
	switch id {
	case m.camera.ID:
		m.camera.Move(delta)
		name := m.camera.Value()
		m.selectCameraSetting(name)
		m.cameraState, m.cameraName = "connecting", name
		m.persistentError = ""
		m.beginCameraSwitch(name)
		m.saveSettings()
	case m.view.ID:
		m.view.Move(delta)
		m.afterControlChange(true)
	case m.mosaicStyle.ID:
		m.mosaicStyle.Move(delta)
		m.afterControlChange(true)
	case m.colorLook.ID:
		m.colorLook.Move(delta)
		m.afterControlChange(true)
	case m.lookStrength.ID:
		m.lookStrength.Move(delta)
		m.afterControlChange(true)
	case m.saveAs.ID:
		m.storeFormatSettings()
		m.saveAs.Move(delta)
		m.loadFormatSettings()
		m.afterControlChange(false)
	case m.size.ID:
		m.size.Move(delta)
		if m.saveAs.Value() == "GIF" && m.size.Index > 1 {
			m.size.Index = 1
		}
		m.afterControlChange(false)
	case m.fps.ID:
		m.fps.Move(delta)
		m.afterControlChange(false)
	case m.quality.ID:
		m.quality.Move(delta)
		m.afterControlChange(false)
	}
	m.syncEnabled()
}

// beginCameraSwitch advances Runtime's camera generation before advancing the
// view version. A preview already received by Bubble Tea keeps the old version
// and is rejected, while the old capture session cannot emit the new version
// after its generation has been invalidated. Clearing liveANSI also removes a
// frame rendered immediately before the user changed cameras.
func (m *Model) beginCameraSwitch(name string) {
	if m.runtime == nil {
		return
	}
	m.liveANSI = ""
	m.runtime.SelectCamera(name)
	m.pushView()
}

func (m *Model) selectCameraSetting(name string) {
	m.settings.Device = name
	// Runtime resolves the selected friendly name back to its stable Media
	// Foundation ID. Never let the previous camera's ID be saved alongside a
	// newly selected label while that asynchronous selection is in flight.
	m.settings.DeviceID = ""
	if strings.EqualFold(name, "DEMO") {
		m.settings.Device = ""
	}
}

func (m *Model) afterControlChange(updatePreview bool) {
	m.storeFormatSettings()
	m.settings.Mirror = m.mirror.Value
	m.settings.Symbols = m.symbolName()
	m.settings.Framing = map[bool]string{true: "fill", false: "whole"}[m.view.Value() == "FILL WINDOW"]
	m.settings.ColorLook = strings.ToLower(m.colorLook.Value())
	m.settings.LookStrength = m.lookStrength.Value
	m.settings.SaveFormat = strings.ToLower(m.saveAs.Value())
	m.settings.ExportQuality = strings.ToLower(m.quality.Value())
	if updatePreview {
		m.pushView()
	}
	m.saveSettings()
}

func (m *Model) storeFormatSettings() {
	w, h := m.outputSize()
	if m.saveAs.Value() == "GIF" {
		m.settings.GIFwidth, m.settings.GIFfps = w, min(m.fps.Value, 30)
	} else {
		m.settings.RecordingWidth, m.settings.RecordingHeight, m.settings.RecordingFPS = w, h, m.fps.Value
	}
}

func (m *Model) loadFormatSettings() {
	if m.saveAs.Value() == "GIF" {
		m.size.Options = []string{"720p", "1080p"}
		m.size.Index, m.fps.Value, m.fps.Max = min(sizeIndex(m.settings.GIFwidth), 1), min(m.settings.GIFfps, 30), 30
		m.notify("GIF is available at 720p or 1080p, up to 30 FPS")
	} else {
		m.size.Options = []string{"720p", "1080p"}
		m.size.Index, m.fps.Value, m.fps.Max = min(sizeIndex(m.settings.RecordingWidth), 1), m.settings.RecordingFPS, 60
	}
}

func (m *Model) pushView() {
	if m.runtime == nil {
		return
	}
	m.viewVersion++
	m.runtime.UpdateView(m.viewOptions())
}

func (m Model) viewOptions() ViewOptions {
	width, height := m.previewAreaSize()
	return ViewOptions{
		Version: m.viewVersion, MaxColumns: max(width-2, 1), MaxRows: max(height-2, 1),
		Fill: m.view.Value() == "FILL WINDOW", Mirror: m.mirror.Value, Paused: m.paused,
		Symbol: m.symbolName(), Detail: m.visualDetail(), TargetFPS: m.settings.RenderFPS,
		ColorLook: strings.ToLower(m.colorLook.Value()), LookStrength: m.lookStrength.Value,
	}
}

func (m Model) recordOptions() RecordOptions {
	w, h := m.outputSize()
	return RecordOptions{Format: strings.ToLower(m.saveAs.Value()), Quality: strings.ToLower(m.quality.Value()), Symbol: m.symbolName(), Detail: m.visualDetail(), Width: w, Height: h, FPS: m.fps.Value, Fill: m.view.Value() == "FILL WINDOW"}
}

func (m *Model) saveSettings() {
	if m.runtime != nil {
		m.runtime.Save(m.settings)
	}
}

func (m *Model) syncEnabled() {
	locked := m.recordState == "starting" || m.recordState == "recording" || m.recordState == "saving"
	m.camera.Enabled = m.runtime == nil || (!locked && m.cameraState != "finding" && m.cameraState != "connecting")
	m.view.Enabled, m.mirror.Enabled, m.mosaicStyle.Enabled = !locked, !locked, !locked
	// Color looks transform the canonical cells themselves, so they can change
	// safely during a recording and become part of the very next saved frame.
	lookLocked := m.recordState == "starting" || m.recordState == "saving"
	m.colorLook.Enabled = !lookLocked
	m.lookStrength.Enabled = !lookLocked && !strings.EqualFold(m.colorLook.Value(), "NONE")
	m.saveAs.Enabled, m.size.Enabled, m.fps.Enabled, m.quality.Enabled = !locked, !locked, !locked, !locked
	live := m.cameraState == "live" || m.cameraState == "demo"
	for i := range m.transport {
		switch m.transport[i].ID {
		case "transport.live":
			m.transport[i].Enabled = live || m.cameraState == "unavailable"
		case "transport.record":
			m.transport[i].Enabled = m.recordState == "recording" || (live && (m.recordState == "idle" || m.recordState == "error"))
		case "transport.snapshot":
			m.transport[i].Enabled = live && !locked && !m.snapshotSaving
		case "transport.open":
			m.transport[i].Enabled = true
		}
	}
}

func (m Model) canActivate(id string) bool {
	switch id {
	case m.camera.ID:
		return m.camera.Enabled
	case m.view.ID:
		return m.view.Enabled
	case m.mirror.ID:
		return m.mirror.Enabled
	case m.mosaicStyle.ID:
		return m.mosaicStyle.Enabled
	case m.colorLook.ID:
		return m.colorLook.Enabled
	case m.lookStrength.ID:
		return m.lookStrength.Enabled
	case m.details.ID:
		return m.details.Enabled
	case m.saveAs.ID:
		return m.saveAs.Enabled
	case m.size.ID:
		return m.size.Enabled
	case m.fps.ID:
		return m.fps.Enabled
	case m.quality.ID:
		return m.quality.Enabled
	}
	for _, button := range m.transport {
		if button.ID == id {
			return button.Enabled
		}
	}
	return false
}

func (m *Model) moveFocus(delta int) {
	if len(m.focusOrder) == 0 {
		return
	}
	for range len(m.focusOrder) {
		m.focus = wrap(m.focus+delta, len(m.focusOrder))
		if m.canActivate(m.focusedID()) {
			return
		}
	}
}

func (m *Model) setFocus(id string) {
	for i, candidate := range m.focusOrder {
		if candidate == id && m.canActivate(id) {
			m.focus = i
			return
		}
	}
}
func (m Model) focusedID() string {
	if len(m.focusOrder) == 0 {
		return ""
	}
	return m.focusOrder[wrap(m.focus, len(m.focusOrder))]
}

func (m *Model) setDevices(devices []string, selected string) {
	options := append([]string(nil), devices...)
	if !containsFold(options, "DEMO") {
		options = append(options, "DEMO")
	}
	if len(options) == 0 {
		options = []string{"DEMO"}
	}
	m.camera.Options, m.camera.Enabled = options, true
	preferred := selected
	if preferred == "" {
		preferred = m.settings.Device
	}
	m.camera.Index = indexOfFold(options, preferred)
	if preferred == "" && len(devices) > 0 {
		m.camera.Index = 0
	}
	m.cameraName = m.camera.Value()
}

func (m *Model) setLooks(discovered []string) {
	options := []string{"NONE", "WARM", "COOL", "MONO"}
	for _, name := range discovered {
		name = safeTerminalText(name)
		if name != "" && !containsFold(options, name) {
			options = append(options, name)
		}
	}
	selected := m.colorLook.Value()
	if selected == "NONE" && !strings.EqualFold(m.settings.ColorLook, "none") {
		selected = m.settings.ColorLook
	}
	m.colorLook.Options = options
	m.colorLook.Index = indexOfFold(options, selected)
}

func (m *Model) notify(message string) {
	m.toast, m.toastUntil = message, m.clockNow().Add(4*time.Second)
}

func indexOfFold(options []string, value string) int {
	for i, option := range options {
		if strings.EqualFold(option, value) {
			return i
		}
	}
	return 0
}
func containsFold(options []string, value string) bool {
	for _, option := range options {
		if strings.EqualFold(option, value) {
			return true
		}
	}
	return false
}
func symbolIndex(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "half":
		return 0
	case "all":
		return 2
	default:
		return 1
	}
}
func (m Model) symbolName() string {
	switch m.mosaicStyle.Index {
	case 0:
		return "half"
	case 2:
		return "all"
	default:
		return "quarter"
	}
}

// LOOK is one plain-language preset: it controls both the block vocabulary
// and edge treatment so the main page does not expose two overlapping image
// quality controls.
func (m Model) visualDetail() string {
	switch m.mosaicStyle.Index {
	case 0:
		return "SMOOTH"
	case 2:
		return "SHARP"
	default:
		return "AUTO"
	}
}
func sizeIndex(width int) int {
	if width <= 1280 {
		return 0
	}
	if width >= 3000 {
		return 2
	}
	return 1
}
func friendlyError(err error, fallback string) string {
	return fallback
}
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return safeTerminalText(err.Error())
}
func shortPath(path string) string {
	path = safeTerminalText(path)
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '\\' || r == '/' })
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
func shorten(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:max(limit-1, 1)]) + "…"
}
func (m Model) elapsed() string {
	if m.recordStarted.IsZero() {
		return "00:00"
	}
	d := m.clockNow().Sub(m.recordStarted)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%02d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}
