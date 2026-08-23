// Package cellrender contains terminal and raster projections of the canonical
// CellFrame. It never interprets ANSI; ANSI is an output-only derivative.
package cellrender

import (
	"errors"
	"image"
	"image/color"
	"strconv"
	"strings"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

// ANSI serializes a canonical frame with one combined truecolor SGR sequence
// per changed cell and one reset per row. The returned string has no trailing
// newline. Recording code must consume CellFrame directly, never parse this
// presentation string back into data.
func ANSI(frame *cellframe.CellFrame) (string, error) {
	if frame == nil || !frame.Valid() || frame.Columns() <= 0 || frame.Rows() <= 0 {
		return "", errors.New("cellrender: frame is not live")
	}
	var builder strings.Builder
	// Typical truecolor cell: 30-40 ASCII bytes plus one three-byte glyph.
	// Grow once so this remains one steady allocation regardless of cell count.
	builder.Grow(frame.Len() * 38)
	var previous cellframe.Cell
	havePrevious := false
	for y := 0; y < frame.Rows(); y++ {
		for x := 0; x < frame.Columns(); x++ {
			cell, ok := frame.CellAt(x, y)
			if !ok || !cell.IsCanonical() {
				return "", errors.New("cellrender: frame contains an invalid cell")
			}
			if !havePrevious || cell.Foreground() != previous.Foreground() || cell.Background() != previous.Background() {
				appendStyle(&builder, cell.Foreground(), cell.Background())
				previous, havePrevious = cell, true
			}
			builder.WriteRune(cell.QuadrantRune())
		}
		builder.WriteString("\x1b[0m")
		havePrevious = false
		if y+1 < frame.Rows() {
			builder.WriteByte('\n')
		}
	}
	return builder.String(), nil
}

// CanvasRGBA projects exactly the canonical terminal quadrants onto a video
// or snapshot canvas with nearest-neighbor scaling and centered black padding.
// It never invents sub-cell detail or parses the ANSI presentation.
func CanvasRGBA(frame *cellframe.CellFrame, width, height int, destination *image.RGBA) (*image.RGBA, error) {
	if frame == nil || !frame.Valid() {
		return nil, errors.New("cellrender: frame is not live")
	}
	contentWidth, contentHeight, originX, originY, err := CanvasGeometry(frame.Columns(), frame.Rows(), width, height)
	if err != nil {
		return nil, err
	}
	if destination == nil || destination.Bounds() != image.Rect(0, 0, width, height) {
		destination = image.NewRGBA(image.Rect(0, 0, width, height))
	} else {
		for i := range destination.Pix {
			destination.Pix[i] = 0
		}
	}
	// RGBA zero is transparent; exports are deliberately opaque black outside
	// the terminal-shaped image.
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			destination.SetRGBA(x, y, color.RGBA{A: 0xff})
		}
	}
	sourceWidth, sourceHeight := frame.Columns()*2, frame.Rows()*2
	for y := 0; y < contentHeight; y++ {
		sourceY := y * sourceHeight / contentHeight
		cellY, quadrantY := sourceY/2, sourceY&1
		for x := 0; x < contentWidth; x++ {
			sourceX := x * sourceWidth / contentWidth
			cellX, quadrantX := sourceX/2, sourceX&1
			cell, ok := frame.CellAt(cellX, cellY)
			if !ok {
				return nil, errors.New("cellrender: frame contains an invalid cell")
			}
			value := cell.ColorAt(quadrantY*2 + quadrantX)
			destination.SetRGBA(originX+x, originY+y, color.RGBA{R: value.R(), G: value.G(), B: value.B(), A: 0xff})
		}
	}
	return destination, nil
}

func appendStyle(builder *strings.Builder, foreground, background cellframe.RGB) {
	builder.WriteString("\x1b[38;2;")
	appendByte(builder, foreground.R())
	builder.WriteByte(';')
	appendByte(builder, foreground.G())
	builder.WriteByte(';')
	appendByte(builder, foreground.B())
	builder.WriteString(";48;2;")
	appendByte(builder, background.R())
	builder.WriteByte(';')
	appendByte(builder, background.G())
	builder.WriteByte(';')
	appendByte(builder, background.B())
	builder.WriteByte('m')
}

func appendByte(builder *strings.Builder, value uint8) {
	var scratch [3]byte
	digits := strconv.AppendUint(scratch[:0], uint64(value), 10)
	_, _ = builder.Write(digits)
}

// TinyRGBA returns the canonical 2C-by-2R quadrant raster. It contains exactly
// the information visible in the terminal and is the preferred FFmpeg input:
// FFmpeg can nearest-neighbor scale this tiny image far more cheaply than Go
// can construct a 1080p frame for every timestamp.
func TinyRGBA(frame *cellframe.CellFrame, destination *image.RGBA) (*image.RGBA, error) {
	if frame == nil || !frame.Valid() {
		return nil, errors.New("cellrender: frame is not live")
	}
	width, height := frame.Columns()*2, frame.Rows()*2
	if destination == nil || destination.Bounds() != image.Rect(0, 0, width, height) {
		destination = image.NewRGBA(image.Rect(0, 0, width, height))
	}
	for y := 0; y < frame.Rows(); y++ {
		for x := 0; x < frame.Columns(); x++ {
			cell, ok := frame.CellAt(x, y)
			if !ok || !cell.IsCanonical() {
				return nil, errors.New("cellrender: frame contains an invalid cell")
			}
			for quadrant := 0; quadrant < 4; quadrant++ {
				color := cell.ColorAt(quadrant)
				offset := destination.PixOffset(x*2+(quadrant&1), y*2+quadrant/2)
				destination.Pix[offset] = color.R()
				destination.Pix[offset+1] = color.G()
				destination.Pix[offset+2] = color.B()
				destination.Pix[offset+3] = 0xff
			}
		}
	}
	return destination, nil
}

// CanvasGeometry returns the integer-nearest scaling and centered padding for
// a terminal-shaped raster. Cell aspect remains 1:2. The returned content size
// never exceeds the canvas and never adds CellFrame detail.
func CanvasGeometry(columns, rows, width, height int) (contentWidth, contentHeight, x, y int, err error) {
	if columns <= 0 || rows <= 0 || width <= 0 || height <= 0 {
		return 0, 0, 0, 0, errors.New("cellrender: dimensions must be positive")
	}
	cellWidth := min(width/columns, height/(rows*2))
	if cellWidth < 1 {
		return 0, 0, 0, 0, errors.New("cellrender: canvas cannot represent every terminal cell")
	}
	contentWidth, contentHeight = columns*cellWidth, rows*cellWidth*2
	return contentWidth, contentHeight, (width - contentWidth) / 2, (height - contentHeight) / 2, nil
}
