package render

import (
	"image"
	"image/color"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/mosaic"
)

func TestFitCells(t *testing.T) {
	tests := []struct {
		name                      string
		sourceWidth, sourceHeight int
		maxColumns, maxRows       int
		wantColumns, wantRows     int
	}{
		{"sixteen-nine width limited", 1920, 1080, 80, 30, 80, 22},
		{"sixteen-nine height limited", 1920, 1080, 80, 20, 71, 20},
		{"four-three height limited", 640, 480, 80, 24, 64, 24},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			columns, rows := FitCells(test.sourceWidth, test.sourceHeight, test.maxColumns, test.maxRows)
			if columns != test.wantColumns || rows != test.wantRows {
				t.Fatalf("FitCells() = %dx%d, want %dx%d", columns, rows, test.wantColumns, test.wantRows)
			}
		})
	}
}

func TestRenderHasRequestedVisibleDimensions(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 16; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 10), G: uint8(y * 20), B: 80, A: 255})
		}
	}
	renderer, err := New(Config{Columns: 8, Rows: 3, Symbols: mosaic.Half, Threshold: 128})
	if err != nil {
		t.Fatal(err)
	}
	plain := stripSGR(renderer.Render(img))
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want 3", len(lines))
	}
	for index, line := range lines {
		if width := utf8.RuneCountInString(line); width != 8 {
			t.Fatalf("line %d width = %d, want 8", index, width)
		}
	}
}

var sgrPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripSGR(value string) string {
	return sgrPattern.ReplaceAllString(value, "")
}
