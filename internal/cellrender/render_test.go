package cellrender

import (
	"strings"
	"testing"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

func solvedFrame(t testing.TB, columns, rows int, colors [][3]uint8) *cellframe.CellFrame {
	t.Helper()
	solver, err := cellframe.NewSolver(cellframe.Config{Columns: columns, Rows: rows, Mode: cellframe.Detailed})
	if err != nil {
		t.Fatal(err)
	}
	pixels := make([]byte, columns*2*rows*2*3)
	for i := range columns * 2 * rows * 2 {
		value := colors[i%len(colors)]
		copy(pixels[i*3:], value[:])
	}
	frame, err := solver.SolveRGB24(cellframe.RGB24{Pix: pixels, Width: columns * 2, Height: rows * 2, Stride: columns * 6}, cellframe.SourceMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestANSIHasExactGridAndNoTrailingNewline(t *testing.T) {
	t.Parallel()
	frame := solvedFrame(t, 2, 1, [][3]uint8{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 255}})
	defer frame.Release()
	ansi, err := ANSI(frame)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(ansi, "\n") || strings.Count(ansi, "\n") != 0 {
		t.Fatalf("unexpected row termination: %q", ansi)
	}
	if !strings.Contains(ansi, "\x1b[38;2;") || !strings.HasSuffix(ansi, "\x1b[0m") {
		t.Fatalf("missing truecolor style/reset: %q", ansi)
	}
}

func TestTinyRGBAMatchesCanonicalQuadrants(t *testing.T) {
	t.Parallel()
	frame := solvedFrame(t, 1, 1, [][3]uint8{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 255}})
	defer frame.Release()
	image, err := TinyRGBA(frame, nil)
	if err != nil {
		t.Fatal(err)
	}
	for quadrant, point := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		cell, _ := frame.Cell(0)
		want := cell.ColorAt(quadrant)
		got := image.RGBAAt(point[0], point[1])
		if got.R != want.R() || got.G != want.G() || got.B != want.B() || got.A != 255 {
			t.Fatalf("quadrant %d = %#v, want %#v", quadrant, got, want)
		}
	}
}

func TestCanvasGeometryPreservesTerminalCellAspect(t *testing.T) {
	t.Parallel()
	w, h, x, y, err := CanvasGeometry(240, 67, 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if w != 1920 || h != 1072 || x != 0 || y != 4 {
		t.Fatalf("geometry = %dx%d at %d,%d", w, h, x, y)
	}
}

func BenchmarkANSI240x67(b *testing.B) {
	frame := solvedFrame(b, 240, 67, [][3]uint8{{15, 20, 30}, {200, 30, 40}, {40, 190, 80}, {220, 220, 210}})
	defer frame.Release()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ANSI(frame); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTinyRGBA240x67(b *testing.B) {
	frame := solvedFrame(b, 240, 67, [][3]uint8{{15, 20, 30}, {200, 30, 40}, {40, 190, 80}, {220, 220, 210}})
	defer frame.Release()
	image, err := TinyRGBA(frame, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := TinyRGBA(frame, image); err != nil {
			b.Fatal(err)
		}
	}
}
