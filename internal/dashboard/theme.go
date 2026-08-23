package dashboard

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme uses the terminal's own background. Color is reserved for readable
// state and focus signals, following the restrained palette used in Charm's
// Bubble Tea examples rather than painting a second dark theme over the user's.
type Theme struct {
	Canvas    color.Color
	Surface   color.Color
	SurfaceHi color.Color
	Line      color.Color
	Ink       color.Color
	Muted     color.Color
	Faint     color.Color
	Violet    color.Color
	Cyan      color.Color
	Green     color.Color
	Red       color.Color
	Amber     color.Color
}

func DefaultTheme() Theme {
	return ThemeForBackground(true)
}

// ThemeForBackground follows the same light/dark adaptation used by Bubbles:
// the user's foreground and background remain untouched while secondary and
// state colors keep their contrast on either terminal theme.
func ThemeForBackground(isDark bool) Theme {
	terminalBackground := lipgloss.NoColor{}
	lightDark := lipgloss.LightDark(isDark)
	return Theme{
		Canvas:    terminalBackground,
		Surface:   terminalBackground,
		SurfaceHi: terminalBackground,
		Line:      lightDark(lipgloss.Color("#C7C3C7"), lipgloss.Color("#5C5C5C")),
		Ink:       lipgloss.NoColor{},
		Muted:     lightDark(lipgloss.Color("#4D4D4D"), lipgloss.Color("#A49FA5")),
		Faint:     lightDark(lipgloss.Color("#8E8E8E"), lipgloss.Color("#626262")),
		Violet:    lightDark(lipgloss.Color("#634BD0"), lipgloss.Color("#874BFD")),
		Cyan:      lightDark(lipgloss.Color("#AD3E76"), lipgloss.Color("#F25D94")),
		Green:     lightDark(lipgloss.Color("#047A50"), lipgloss.Color("#73F59F")),
		Red:       lightDark(lipgloss.Color("#C40046"), lipgloss.Color("#FF5F87")),
		Amber:     lightDark(lipgloss.Color("#755E00"), lipgloss.Color("#FDFF8C")),
	}
}
