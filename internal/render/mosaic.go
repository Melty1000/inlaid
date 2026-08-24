// Package render adapts Charmbracelet Mosaic for the deterministic dashboard
// demo. Live camera frames use the canonical cellframe/cellrender path.
package render

import (
	"fmt"
	"image"
	"math"
	"strings"

	"github.com/charmbracelet/x/mosaic"
)

// Config controls the upstream Mosaic renderer.
type Config struct {
	Columns   int
	Rows      int
	Symbols   mosaic.Symbol
	Threshold int
	Dither    bool
}

// Mosaic wraps a configured upstream Mosaic demo renderer.
type Mosaic struct {
	inner mosaic.Mosaic
}

// New constructs a renderer for an exact terminal-cell rectangle.
func New(cfg Config) (*Mosaic, error) {
	if cfg.Columns <= 0 || cfg.Rows <= 0 {
		return nil, fmt.Errorf("renderer columns and rows must be positive")
	}
	if cfg.Threshold < 0 || cfg.Threshold > 255 {
		return nil, fmt.Errorf("threshold must be between 0 and 255")
	}
	inner := mosaic.New().
		Width(cfg.Columns * 2).
		Height(cfg.Rows * 2).
		Scale(1).
		Symbol(cfg.Symbols).
		Threshold(cfg.Threshold).
		Dither(cfg.Dither)
	return &Mosaic{inner: inner}, nil
}

// Render turns an image into one ANSI truecolor frame without a trailing newline.
func (r *Mosaic) Render(img image.Image) string {
	return strings.TrimSuffix(r.inner.Render(img), "\n")
}

// FitCells preserves the source's visual aspect ratio for terminal cells that
// are approximately twice as tall as they are wide.
func FitCells(sourceWidth, sourceHeight, maxColumns, maxRows int) (columns, rows int) {
	if sourceWidth <= 0 || sourceHeight <= 0 || maxColumns <= 0 || maxRows <= 0 {
		return 1, 1
	}
	aspect := float64(sourceWidth) / float64(sourceHeight)
	columns = maxColumns
	rows = int(math.Floor(float64(columns) / (2 * aspect)))
	if rows < 1 {
		rows = 1
	}
	if rows > maxRows {
		rows = maxRows
		columns = int(math.Floor(float64(rows) * 2 * aspect))
	}
	columns = max(columns, 1)
	rows = max(rows, 1)
	return columns, rows
}
