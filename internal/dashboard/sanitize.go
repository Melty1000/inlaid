package dashboard

import (
	"strings"
	"unicode"
)

// safeTerminalText removes control and formatting code points from untrusted
// labels before they enter Lip Gloss. Camera names, filesystem errors, and
// tool diagnostics originate outside the TUI and must never become terminal
// escape sequences. Rendered ANSI camera frames deliberately do not pass this fence.
func safeTerminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\r' || r == '\n' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, value)
}
