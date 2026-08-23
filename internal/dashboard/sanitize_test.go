package dashboard

import "testing"

func TestSafeTerminalTextStripsEscapeAndFormattingControls(t *testing.T) {
	t.Parallel()
	input := "Camera\x1b]8;;https://evil.invalid\aCLICK\x1b]8;;\a\nName\u202eexe"
	if got, want := safeTerminalText(input), "Camera]8;;https://evil.invalidCLICK]8;; Nameexe"; got != want {
		t.Fatalf("safeTerminalText() = %q, want %q", got, want)
	}
}
