package dashboard

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Melty1000/inlaid/internal/render"
	"github.com/charmbracelet/x/ansi"
)

type controlLayer struct {
	id      string
	content string
}

func (m Model) compose() *lipgloss.Compositor {
	w, h := max(m.width, 1), max(m.height, 1)
	if w < minimumDashboardWidth || h < minimumDashboardHeight {
		return m.composeResize(w, h)
	}
	root := lipgloss.NewLayer(canvas(m.theme, w, h))
	m.addHeader(root, w)

	footerY := h - 1
	transportY := footerY - 3
	saveY := transportY - 3
	cameraY := saveY - 3
	previewY := 2
	if m.details.Value {
		previewY = 3
	}
	previewHeight := max(cameraY-previewY, 3)
	if m.details.Value {
		m.addDetails(root, 2, w, w-4, previewHeight)
	}
	m.addPreview(root, 2, previewY, w-4, previewHeight)
	m.addCameraChannel(root, cameraY, w)
	m.addSaveChannel(root, saveY, w)
	m.addTransport(root, transportY, w)
	m.addFooter(root, footerY, w)
	return lipgloss.NewCompositor(root)
}

// previewAreaSize is the only live-grid authority. Legacy
// Columns/Rows settings are deliberately ignored so the image cannot stop at
// an invisible numeric ceiling while the preview still has space.
func (m Model) previewAreaSize() (int, int) {
	w, h := max(m.width, 1), max(m.height, 1)
	footerY := h - 1
	transportY := footerY - 3
	saveY := transportY - 3
	cameraY := saveY - 3
	previewY := 2
	if m.details.Value {
		previewY = 3
	}
	return max(w-4, 12), max(cameraY-previewY, 3)
}

func (m Model) composeResize(width, height int) *lipgloss.Compositor {
	root := lipgloss.NewLayer(canvas(m.theme, width, height))
	brand := lipgloss.NewStyle().Foreground(m.theme.Ink).Bold(true).Render("INLAID")
	title := lipgloss.NewStyle().Foreground(m.theme.Amber).Bold(true).Render("WINDOW TOO SMALL")
	message := fmt.Sprintf("Make the window at least %d columns wide and %d rows tall.", minimumDashboardWidth, minimumDashboardHeight)
	current := lipgloss.NewStyle().Foreground(m.theme.Muted).Render(fmt.Sprintf("Current space: %d × %d", width, height))
	hint := lipgloss.NewStyle().Foreground(m.theme.Cyan).Render("Maximize the window or press Ctrl+- to zoom out.")

	root.AddLayers(lipgloss.NewLayer(brand).X(2).Y(1))
	boxWidth := min(max(width-8, 1), 58)
	content := lipgloss.NewStyle().Width(boxWidth).Render(strings.Join([]string{title, "", message, current, "", hint}, "\n"))
	x := max((width-lipgloss.Width(content))/2, 0)
	y := max((height-lipgloss.Height(content))/2, 3)
	root.AddLayers(lipgloss.NewLayer(content).X(x).Y(y))
	return lipgloss.NewCompositor(root)
}

func (m Model) addHeader(root *lipgloss.Layer, width int) {
	brand := lipgloss.NewStyle().Foreground(m.theme.Ink).Bold(true).Render("INLAID")
	root.AddLayers(lipgloss.NewLayer(brand).X(2).Y(0).Z(2))

	state := m.statusChip()
	status := state.View(m.theme)

	detailsState := componentState{Focused: m.focusedID() == m.details.ID, Hovered: m.hover == m.details.ID, Pressed: m.pressed == m.details.ID, Compact: true}
	details := m.details.HeaderView(m.theme, detailsState)
	detailsX := width - lipgloss.Width(details) - 2
	statusX := detailsX - lipgloss.Width(status) - 2
	brandEnd := 2 + lipgloss.Width(brand)
	if statusX > brandEnd+1 {
		root.AddLayers(lipgloss.NewLayer(status).X(statusX).Y(0).Z(2))
	}
	if detailsX > brandEnd+1 {
		root.AddLayers(lipgloss.NewLayer(details).ID(m.details.ID).X(detailsX).Y(0).Z(3))
	}
	line := lipgloss.NewStyle().Foreground(m.theme.Line).Render(strings.Repeat("─", width-4))
	root.AddLayers(lipgloss.NewLayer(line).X(2).Y(1))
}

func (m Model) statusChip() StatusChip {
	if m.recordState == "saving" {
		return StatusChip{Label: "SAVING " + m.recordFormat + "…", Tone: "warn"}
	}
	if m.recordState == "starting" {
		return StatusChip{Label: "STARTING " + m.recordFormat + "…", Tone: "warn"}
	}
	if m.recordState == "recording" {
		label := "RECORDING " + m.recordFormat + " · " + m.elapsed()
		if m.paused {
			label += " · PREVIEW PAUSED"
		}
		return StatusChip{Label: label, Tone: "danger"}
	}
	if m.paused {
		return StatusChip{Label: "PREVIEW PAUSED · CAMERA ON", Tone: "warn"}
	}
	switch m.cameraState {
	case "finding":
		return StatusChip{Label: "FINDING CAMERAS…", Tone: "info"}
	case "connecting":
		return StatusChip{Label: "CONNECTING…", Tone: "info"}
	case "live":
		fps := m.shownFPS
		if fps <= 0 {
			fps = m.sourceFPS
		}
		if fps > 0 {
			return StatusChip{Label: fmt.Sprintf("LIVE · %.1f FPS", fps), Tone: "good"}
		}
		return StatusChip{Label: "LIVE", Tone: "good"}
	case "unavailable":
		return StatusChip{Label: "CAMERA UNAVAILABLE", Tone: "danger"}
	default:
		return StatusChip{Label: "DEMO", Tone: "accent"}
	}
}

func (m Model) addDetails(root *lipgloss.Layer, y, width, previewWidth, previewHeight int) {
	columns, rows := m.liveColumns, m.liveRows
	if columns <= 0 || rows <= 0 {
		columns, rows = m.previewGeometry(previewWidth, previewHeight)
	}
	framing := "Fill view"
	if m.view.Value() == "SHOW WHOLE CAMERA" {
		framing = "Whole view"
	}
	cameraFPS := m.sourceFPS
	if cameraFPS <= 0 {
		cameraFPS = m.captureFPS
	}
	fps := "Starting"
	if m.shownFPS > 0 && cameraFPS > 0 {
		fps = fmt.Sprintf("%.1f shown / %.1f camera FPS", m.shownFPS, cameraFPS)
	} else if m.shownFPS > 0 {
		fps = fmt.Sprintf("%.1f shown FPS", m.shownFPS)
	} else if cameraFPS > 0 {
		fps = fmt.Sprintf("%.1f camera FPS", cameraFPS)
	}
	captureWidth, captureHeight := m.captureWidth, m.captureHeight
	if captureWidth <= 0 || captureHeight <= 0 {
		captureWidth, captureHeight = m.settings.CaptureWidth, m.settings.CaptureHeight
	}
	look := ""
	if !strings.EqualFold(m.colorLook.Value(), "NONE") && m.lookStrength.Value > 0 {
		look = fmt.Sprintf("  ·  %s look %d%%", safeTerminalText(m.colorLook.Value()), m.lookStrength.Value)
	}
	details := fmt.Sprintf(
		"Camera %s  ·  Terminal %d×%d  ·  %s  ·  %s%s  ·  %d skipped",
		friendlyCameraSize(captureWidth, captureHeight),
		columns,
		rows,
		framing, fps, look, m.dropped,
	)
	if m.technicalError != "" {
		details += "  ·  Last error: " + safeTerminalText(m.technicalError)
	}
	style := lipgloss.NewStyle().Foreground(m.theme.Muted)
	root.AddLayers(lipgloss.NewLayer(style.Width(width - 4).Render(ansi.Truncate(details, width-6, "…"))).X(2).Y(y).Z(2))
}

func friendlyCameraSize(width, height int) string {
	switch {
	case width == 3840 && height == 2160:
		return "4K"
	case width == 1920 && height == 1080:
		return "1080p"
	case width == 1280 && height == 720:
		return "720p"
	case width > 0 && height > 0:
		return fmt.Sprintf("%d×%d", width, height)
	default:
		return "starting"
	}
}

func (m Model) addPreview(root *lipgloss.Layer, x, y, width, height int) {
	width, height = max(width, 12), max(height, 3)
	maxRows := height - 2
	interior, _, _ := m.previewInterior(width, height)

	borderColor := m.theme.Line
	if m.clockNow().Before(m.flashUntil) {
		borderColor = m.theme.Cyan
	}
	border := lipgloss.NewStyle().Foreground(borderColor)
	fillStyle := lipgloss.NewStyle()
	result := make([]string, 0, height)
	result = append(result, border.Render("┌"+strings.Repeat("─", width-2)+"┐"))
	for row := 0; row < maxRows; row++ {
		result = append(result, border.Render("│")+fillStyle.Render(interior[row])+border.Render("│"))
	}
	result = append(result, border.Render("└"+strings.Repeat("─", width-2)+"┘"))
	root.AddLayers(lipgloss.NewLayer(strings.Join(result, "\n")).X(x).Y(y).Z(1))

	fill := m.view.Value() == "FILL WINDOW"
	labelText := "DEMO"
	if m.runtime != nil {
		labelText = strings.ToUpper(shorten(safeTerminalText(m.cameraName), 28))
		if strings.TrimSpace(labelText) == "" {
			labelText = "CAMERA"
		}
	}
	label := lipgloss.NewStyle().Foreground(m.theme.Violet).Bold(true).Render(" " + labelText + " ")
	root.AddLayers(lipgloss.NewLayer(label).X(x + 2).Y(y).Z(3))
	modeLabel := "FILL WINDOW"
	if !fill {
		modeLabel = "SHOW WHOLE CAMERA"
	}
	mode := lipgloss.NewStyle().Foreground(m.theme.Cyan).Bold(true).Render(" " + modeLabel + " ")
	modeX := x + width - lipgloss.Width(mode) - 2
	if modeX > x+lipgloss.Width(label)+2 {
		root.AddLayers(lipgloss.NewLayer(mode).X(modeX).Y(y).Z(3))
	}
	if m.paused {
		stopped := lipgloss.NewStyle().Foreground(m.theme.Amber).Bold(true).Render(" PREVIEW PAUSED · P TO RESUME ")
		root.AddLayers(lipgloss.NewLayer(stopped).X(x + (width-lipgloss.Width(stopped))/2).Y(y + height/2).Z(4))
	}
}

// previewInterior is the defensive projection used by the full compositor and
// by demo, status, or malformed live content. The trusted live-frame splice
// shares this geometry but can skip its ANSI parsing and row allocations.
func (m Model) previewInterior(width, height int) (interior []string, columns, rows int) {
	width, height = max(width, 12), max(height, 3)
	maxColumns, maxRows := width-2, height-2
	columns, rows = m.liveColumns, m.liveRows
	if m.runtime == nil {
		columns, rows = m.previewGeometry(width, height)
	}
	if columns <= 0 || rows <= 0 || columns > maxColumns || rows > maxRows {
		columns, rows = m.previewGeometry(width, height)
	}
	fill := m.view.Value() == "FILL WINDOW"
	frame := m.liveANSI
	if m.runtime == nil {
		frame = m.preview.Frame(columns, rows, m.sequence, m.symbolName(), m.visualDetail(), fill, m.mirror.Value)
	} else if frame == "" {
		frame = centeredMessage(columns, rows, m.previewMessage())
	}
	frameLines := strings.Split(frame, "\n")
	for len(frameLines) < rows {
		frameLines = append(frameLines, "")
	}

	leftPad := max((maxColumns-columns)/2, 0)
	rightPad := max(maxColumns-columns-leftPad, 0)
	topPad := max((maxRows-rows)/2, 0)
	bottomPad := max(maxRows-rows-topPad, 0)
	blankLine := strings.Repeat(" ", maxColumns)
	interior = make([]string, 0, maxRows)
	for range topPad {
		interior = append(interior, blankLine)
	}
	for row := 0; row < rows; row++ {
		content := ansi.Truncate(frameLines[row], columns, "")
		contentPad := max(columns-lipgloss.Width(content), 0)
		interior = append(interior, strings.Repeat(" ", leftPad)+content+strings.Repeat(" ", contentPad+rightPad))
	}
	for range bottomPad {
		interior = append(interior, blankLine)
	}
	for len(interior) < maxRows {
		interior = append(interior, blankLine)
	}

	return interior, columns, rows
}

func (m Model) previewMessage() string {
	switch m.cameraState {
	case "finding":
		return "FINDING CAMERAS…"
	case "connecting":
		return "CONNECTING TO CAMERA…"
	case "unavailable":
		return "CAMERA UNAVAILABLE"
	default:
		return "WAITING FOR CAMERA…"
	}
}

func centeredMessage(columns, rows int, message string) string {
	lines := make([]string, max(rows, 1))
	for i := range lines {
		lines[i] = strings.Repeat(" ", max(columns, 1))
	}
	row := len(lines) / 2
	message = ansi.Truncate(message, columns, "…")
	left := max((columns-lipgloss.Width(message))/2, 0)
	lines[row] = strings.Repeat(" ", left) + message + strings.Repeat(" ", max(columns-left-lipgloss.Width(message), 0))
	return strings.Join(lines, "\n")
}

func (m Model) previewGeometry(width, height int) (int, int) {
	maxColumns, maxRows := max(width-2, 1), max(height-2, 1)
	if m.view.Value() == "FILL WINDOW" {
		return maxColumns, maxRows
	}
	return render.FitCells(m.settings.CaptureWidth, m.settings.CaptureHeight, maxColumns, maxRows)
}

func (m Model) addCameraChannel(root *lipgloss.Layer, y, width int) {
	m.addChannelSurface(root, y, width, "CAMERA", m.theme.Cyan)
	compact := width < 110
	state := func(id string, forceFull bool) componentState {
		return componentState{Focused: m.focusedID() == id, Hovered: m.hover == id, Pressed: m.pressed == id, Compact: compact && !forceFull}
	}

	row0 := []controlLayer{
		{m.camera.ID, m.camera.View(m.theme, state(m.camera.ID, false))},
		{m.view.ID, m.view.View(m.theme, state(m.view.ID, true))},
	}
	row1 := []controlLayer{
		{m.mirror.ID, m.mirror.View(m.theme, state(m.mirror.ID, false))},
		{m.mosaicStyle.ID, m.mosaicStyle.View(m.theme, state(m.mosaicStyle.ID, false))},
		{m.colorLook.ID, m.colorLook.View(m.theme, state(m.colorLook.ID, false))},
		{m.lookStrength.ID, m.lookStrength.View(m.theme, state(m.lookStrength.ID, false))},
	}
	m.addControlRow(root, row0, 11, y, width-2)
	m.addControlRow(root, row1, 3, y+1, width-2)
}

func (m Model) addSaveChannel(root *lipgloss.Layer, y, width int) {
	m.addChannelSurface(root, y, width, "SAVE", m.theme.Violet)
	compact := width < 110
	state := func(id string, forceFull bool) componentState {
		return componentState{Focused: m.focusedID() == id, Hovered: m.hover == id, Pressed: m.pressed == id, Compact: compact && !forceFull}
	}
	row0 := []controlLayer{
		{m.saveAs.ID, m.saveAs.View(m.theme, state(m.saveAs.ID, true))},
		{m.size.ID, m.size.View(m.theme, state(m.size.ID, true))},
		{m.fps.ID, m.fps.View(m.theme, state(m.fps.ID, false))},
	}
	row1 := []controlLayer{
		{m.quality.ID, m.quality.View(m.theme, state(m.quality.ID, true))},
	}
	m.addControlRow(root, row0, 9, y, width-2)
	m.addControlRow(root, row1, 3, y+1, width-2)
}

func (m Model) addChannelSurface(root *lipgloss.Layer, y, width int, label string, tone color.Color) {
	row := strings.Repeat(" ", width-4)
	separator := lipgloss.NewStyle().Foreground(m.theme.Line).Render(strings.Repeat("─", width-4))
	root.AddLayers(lipgloss.NewLayer(strings.Join([]string{row, row, separator}, "\n")).X(2).Y(y))
	title := lipgloss.NewStyle().Foreground(tone).Bold(true).Render(label)
	root.AddLayers(lipgloss.NewLayer(title).X(3).Y(y).Z(2))
}

func (m Model) addControlRow(root *lipgloss.Layer, controls []controlLayer, x, y, limit int) {
	for _, control := range controls {
		w := lipgloss.Width(control.content)
		if x+w > limit {
			remaining := limit - x
			if remaining < 8 {
				break
			}
			control.content = ansi.Truncate(control.content, remaining, "…")
			w = lipgloss.Width(control.content)
		}
		root.AddLayers(lipgloss.NewLayer(control.content).ID(control.id).X(x).Y(y).Z(3))
		x += w + 1
	}
}

func (m Model) addTransport(root *lipgloss.Layer, y, width int) {
	buttons := append([]Button(nil), m.transport...)
	compact := width < 100
	for i := range buttons {
		switch buttons[i].ID {
		case "transport.live":
			if m.cameraState == "unavailable" {
				buttons[i].Label, buttons[i].Glyph, buttons[i].Active = "RETRY CAMERA", "↻", true
			} else if m.paused {
				buttons[i].Label, buttons[i].Glyph, buttons[i].Active = "RESUME PREVIEW", "▶", true
			} else {
				buttons[i].Label, buttons[i].Glyph = "PAUSE PREVIEW", "II"
			}
		case "transport.record":
			switch m.recordState {
			case "starting":
				buttons[i].Label, buttons[i].Active = "STARTING…", true
			case "recording":
				buttons[i].Label, buttons[i].Glyph, buttons[i].Active = "STOP & SAVE", "■", true
			case "saving":
				buttons[i].Label, buttons[i].Glyph, buttons[i].Active = "SAVING…", "■", true
			default:
				buttons[i].Label = "RECORD " + m.saveAs.Value()
			}
		case "transport.snapshot":
			if m.snapshotSaving {
				buttons[i].Label, buttons[i].Active = "SAVING…", true
			}
		case "transport.report":
			switch m.reportState {
			case "confirm":
				buttons[i].Label, buttons[i].Glyph, buttons[i].Active = "CREATE REPORT", "◆", true
			case "saving":
				buttons[i].Label, buttons[i].Glyph, buttons[i].Active = "CREATING…", "◆", true
			default:
				buttons[i].Label, buttons[i].Glyph = "REPORT", "?"
			}
		}
		if compact {
			if buttons[i].ID == "transport.record" && strings.HasPrefix(buttons[i].Label, "RECORD ") {
				buttons[i].Label = "REC " + strings.TrimPrefix(buttons[i].Label, "RECORD ")
			}
			switch buttons[i].Label {
			case "PAUSE PREVIEW":
				buttons[i].Label = "PAUSE"
			case "RESUME PREVIEW":
				buttons[i].Label = "RESUME"
			case "RETRY CAMERA":
				buttons[i].Label = "RETRY"
			case "OPEN FOLDER":
				buttons[i].Label = "FOLDER"
			case "SNAPSHOT":
				buttons[i].Label = "SNAP"
			case "CREATE REPORT":
				buttons[i].Label = "CREATE"
			}
		}
	}
	gap := 1
	available := width - 4 - gap*(len(buttons)-1)
	buttonWidth := max(available/len(buttons), 8)
	x := 2
	for i, button := range buttons {
		bw := buttonWidth
		if i == len(buttons)-1 {
			bw = width - 2 - x
		}
		state := componentState{Focused: m.focusedID() == button.ID, Hovered: m.hover == button.ID, Pressed: m.pressed == button.ID, Compact: compact}
		root.AddLayers(lipgloss.NewLayer(button.View(m.theme, state, bw)).ID(button.ID).X(x).Y(y).Z(3))
		x += bw + gap
	}
}

func (m Model) addFooter(root *lipgloss.Layer, y, width int) {
	message := "TAB move · arrows change · ENTER choose · R record · S snapshot · P pause · Q quit"
	if width < 100 {
		message = "TAB move · ←/→ change · ENTER choose · Q quit"
	}
	style := lipgloss.NewStyle().Foreground(m.theme.Muted)
	if m.reportState == "confirm" {
		message = "LOCAL JSON ONLY · app, OS, terminal, camera mode, and performance · no images, paths, IDs, or upload · press CREATE"
		if width < 100 {
			message = "LOCAL JSON ONLY · no images, paths, IDs, or upload · press CREATE"
		}
		style = style.Foreground(m.theme.Cyan)
	} else if m.persistentError != "" {
		message = "! " + safeTerminalText(m.persistentError)
		style = style.Foreground(m.theme.Red)
	} else if m.toast != "" {
		message = "◆ " + safeTerminalText(m.toast)
		style = style.Foreground(m.theme.Amber)
	}
	root.AddLayers(lipgloss.NewLayer(style.Render(ansi.Truncate(message, width-4, "…"))).X(2).Y(y).Z(3))
}

func (m Model) outputSize() (int, int) {
	switch m.size.Value() {
	case "720p":
		return 1280, 720
	default:
		return 1920, 1080
	}
}

func canvas(_ Theme, width, height int) string {
	// Keep the terminal's last column untouched. Writing into the physical
	// right margin can trigger autowrap and briefly scroll the whole alt-screen
	// by one row in Windows Terminal.
	width = max(width-1, 1)
	line := strings.Repeat(" ", width)
	lines := make([]string, height)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}
