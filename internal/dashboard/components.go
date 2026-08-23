package dashboard

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type componentState struct {
	Focused bool
	Hovered bool
	Pressed bool
	Compact bool
}

// Button keeps a stable three-row mouse target. The permanent rounded border
// makes clickability obvious; border weight and color provide the interaction
// feedback without requiring a custom background color.
type Button struct {
	ID      string
	Label   string
	Glyph   string
	Tone    string
	Active  bool
	Enabled bool
}

func (b Button) View(theme Theme, state componentState, width int) string {
	width = max(width, 8)
	ink := theme.Ink
	lineColor := theme.Line
	if b.Tone == "danger" {
		ink = theme.Red
	}
	if b.Active {
		lineColor = ink
	}
	if state.Hovered {
		lineColor = theme.Cyan
	}
	if state.Focused {
		lineColor = theme.Violet
	}
	if state.Pressed {
		lineColor, ink = theme.Cyan, theme.Cyan
	}
	if !b.Enabled {
		ink, lineColor = theme.Faint, theme.Faint
	}

	edgeStyle := lipgloss.NewStyle().Foreground(lineColor)
	labelStyle := lipgloss.NewStyle().Foreground(ink).Bold(b.Active || state.Focused || state.Pressed)
	label := strings.TrimSpace(strings.TrimSpace(safeTerminalText(b.Glyph + " " + b.Label)))
	if state.Pressed {
		label = "◆ " + label
	} else if state.Focused {
		label = "▸ " + label
	}
	label = ansi.Truncate(label, width-4, "")

	topLeft, topRight, bottomLeft, bottomRight, horizontal, vertical := "╭", "╮", "╰", "╯", "─", "│"
	if state.Pressed {
		topLeft, topRight, bottomLeft, bottomRight, horizontal, vertical = "┏", "┓", "┗", "┛", "━", "┃"
	}
	top := edgeStyle.Render(topLeft + strings.Repeat(horizontal, width-2) + topRight)
	middle := edgeStyle.Render(vertical) + labelStyle.Width(width-2).Align(lipgloss.Center).Render(label) + edgeStyle.Render(vertical)
	bottom := edgeStyle.Render(bottomLeft + strings.Repeat(horizontal, width-2) + bottomRight)
	return strings.Join([]string{top, middle, bottom}, "\n")
}

// Toggle makes boolean state visible in words as well as color.
type Toggle struct {
	ID      string
	Label   string
	Value   bool
	Enabled bool
}

func (t Toggle) View(theme Theme, state componentState) string {
	value := "OFF"
	tone := theme.Muted
	if t.Value {
		value = "ON"
		tone = theme.Green
	}
	return inlineControl(theme, state, t.Label, value, tone, t.Enabled)
}

// HeaderView is deliberately quieter than a channel control while keeping the
// same fixed-width mouse target and explicit ON/OFF state.
func (t Toggle) HeaderView(theme Theme, state componentState) string {
	value := "OFF"
	valueColor := theme.Muted
	if t.Value {
		value = "ON "
		valueColor = theme.Green
	}
	labelColor := theme.Muted
	marker, markerColor := " ", theme.Faint
	if state.Hovered && t.Enabled {
		marker, markerColor = "›", theme.Cyan
	}
	if state.Focused && t.Enabled {
		marker, markerColor = "▸", theme.Violet
	}
	if state.Pressed && t.Enabled {
		marker, markerColor = "◆", theme.Cyan
	}
	if !t.Enabled {
		labelColor, valueColor, markerColor = theme.Faint, theme.Faint, theme.Faint
	}
	markerStyle := lipgloss.NewStyle().Foreground(markerColor).Bold(state.Focused || state.Pressed)
	labelStyle := lipgloss.NewStyle().Foreground(labelColor)
	valueStyle := lipgloss.NewStyle().Foreground(valueColor).Bold(t.Value)
	return markerStyle.Render(marker) + labelStyle.Render(t.Label+" ") + valueStyle.Render(value)
}

// SegmentedControl exposes a small, mouse-friendly set without a nested menu.
type SegmentedControl struct {
	ID      string
	Label   string
	Options []string
	Index   int
	Enabled bool
}

func (s *SegmentedControl) Move(delta int) {
	if !s.Enabled || len(s.Options) == 0 {
		return
	}
	s.Index = wrap(s.Index+delta, len(s.Options))
}

func (s SegmentedControl) Value() string {
	if len(s.Options) == 0 {
		return "—"
	}
	return s.Options[wrap(s.Index, len(s.Options))]
}

func (s SegmentedControl) View(theme Theme, state componentState) string {
	if state.Compact {
		return inlineControl(theme, state, s.Label, "‹ "+s.Value()+" ›", theme.Ink, s.Enabled)
	}
	parts := make([]string, 0, len(s.Options))
	for i, option := range s.Options {
		style := lipgloss.NewStyle().Foreground(theme.Muted)
		option = safeTerminalText(option)
		text := " " + option + " "
		if i == s.Index {
			style = style.Foreground(theme.Cyan).Bold(true)
			text = "[" + option + "]"
		}
		parts = append(parts, style.Render(text))
	}
	label := lipgloss.NewStyle().Foreground(theme.Muted).Render(s.Label + " ")
	content := label + strings.Join(parts, lipgloss.NewStyle().Foreground(theme.Faint).Render("·"))
	return focusWrap(theme, state, content, s.Enabled)
}

// Select cycles a finite list inline; it intentionally avoids popovers and
// nested settings pages.
type Select struct {
	ID            string
	Label         string
	Options       []string
	Index         int
	MaxValueWidth int
	Enabled       bool
}

func (s *Select) Move(delta int) {
	if !s.Enabled || len(s.Options) == 0 {
		return
	}
	s.Index = wrap(s.Index+delta, len(s.Options))
}

func (s Select) Value() string {
	if len(s.Options) == 0 {
		return "—"
	}
	return s.Options[wrap(s.Index, len(s.Options))]
}

func (s Select) View(theme Theme, state componentState) string {
	value := safeTerminalText(s.Value())
	if s.MaxValueWidth > 0 {
		value = shorten(value, s.MaxValueWidth)
	}
	if state.Compact {
		limit := 14
		if s.MaxValueWidth > 0 {
			limit = min(limit, s.MaxValueWidth)
		}
		value = shorten(value, limit)
	}
	return inlineControl(theme, state, s.Label, "‹ "+value+" ›", theme.Ink, s.Enabled)
}

// Stepper makes numeric changes explicit without opening a form.
type Stepper struct {
	ID      string
	Label   string
	Value   int
	Min     int
	Max     int
	Step    int
	Suffix  string
	Enabled bool
}

func (s *Stepper) Move(delta int) {
	if !s.Enabled {
		return
	}
	step := s.Step
	if step <= 0 {
		step = 1
	}
	s.Value = min(max(s.Value+delta*step, s.Min), s.Max)
}

func (s Stepper) View(theme Theme, state componentState) string {
	value := fmt.Sprintf("− %d%s +", s.Value, s.Suffix)
	return inlineControl(theme, state, s.Label, value, theme.Ink, s.Enabled)
}

// StatusChip carries state with a label, never color alone.
type StatusChip struct {
	Label string
	Tone  string
}

func (s StatusChip) View(theme Theme) string {
	color := theme.Muted
	switch s.Tone {
	case "good":
		color = theme.Green
	case "info":
		color = theme.Cyan
	case "accent":
		color = theme.Violet
	case "warn":
		color = theme.Amber
	case "danger":
		color = theme.Red
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(s.Label)
}

func inlineControl(theme Theme, state componentState, label, value string, valueColor color.Color, enabled bool) string {
	label, value = safeTerminalText(label), safeTerminalText(value)
	labelStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	valueStyle := lipgloss.NewStyle().Foreground(valueColor).Bold(true)
	if !enabled {
		labelStyle = labelStyle.Foreground(theme.Faint)
		valueStyle = valueStyle.Foreground(theme.Faint)
	}
	content := labelStyle.Render(label+" ") + valueStyle.Render(value)
	return focusWrap(theme, state, content, enabled)
}

func focusWrap(theme Theme, state componentState, content string, enabled bool) string {
	prefix, suffix := "  ", " "
	if state.Compact {
		prefix, suffix = " ", ""
	}
	if !enabled {
		return prefix + content + suffix
	}
	if state.Pressed {
		marker := "◆ "
		if state.Compact {
			marker = "◆"
		}
		return lipgloss.NewStyle().Foreground(theme.Cyan).Bold(true).Render(marker) + content + suffix
	}
	if state.Focused {
		marker := "▸ "
		if state.Compact {
			marker = "▸"
		}
		return lipgloss.NewStyle().Foreground(theme.Violet).Bold(true).Render(marker) + content + suffix
	}
	if state.Hovered {
		marker := "› "
		if state.Compact {
			marker = "›"
		}
		return lipgloss.NewStyle().Foreground(theme.Cyan).Render(marker) + content + suffix
	}
	return lipgloss.NewStyle().Foreground(theme.Faint).Render(prefix) + content + suffix
}

func wrap(value, length int) int {
	if length <= 0 {
		return 0
	}
	value %= length
	if value < 0 {
		value += length
	}
	return value
}
