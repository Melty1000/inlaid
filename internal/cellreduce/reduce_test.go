package cellreduce

import (
	"image/color"
	"testing"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

func TestRGB24ExactQuadrantsAndMirror(t *testing.T) {
	t.Parallel()
	pixels := []byte{
		255, 0, 0, 0, 255, 0,
		0, 0, 255, 255, 255, 255,
	}
	reducer, err := New(Geometry{Columns: 1, Rows: 1})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := reducer.ReduceRGB24(RGB24{Pix: pixels, Width: 2, Height: 2, Stride: 6})
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range [][3]uint64{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 255}} {
		got := frame.Quadrants[index]
		if got.Count != 1 || got.SumR != want[0] || got.SumG != want[1] || got.SumB != want[2] {
			t.Fatalf("quadrant %d = %+v", index, got)
		}
	}

	mirrored, err := New(Geometry{Columns: 1, Rows: 1, Mirror: true})
	if err != nil {
		t.Fatal(err)
	}
	frame, err = mirrored.ReduceRGB24(RGB24{Pix: pixels, Width: 2, Height: 2, Stride: 6})
	if err != nil {
		t.Fatal(err)
	}
	if frame.Quadrants[0].SumG != 255 || frame.Quadrants[1].SumR != 255 || frame.Quadrants[2].SumR != 255 || frame.Quadrants[3].SumB != 255 {
		t.Fatalf("mirrored quadrants = %+v", frame.Quadrants)
	}
}

func TestAreaReductionPreservesAllSourceSamples(t *testing.T) {
	t.Parallel()
	pixels := make([]byte, 4*4*3)
	for i := 0; i < 16; i++ {
		pixels[i*3] = uint8(i)
	}
	reducer, err := New(Geometry{Columns: 1, Rows: 1})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := reducer.ReduceRGB24(RGB24{Pix: pixels, Width: 4, Height: 4, Stride: 12})
	if err != nil {
		t.Fatal(err)
	}
	var count, sum uint64
	for _, stats := range frame.Quadrants {
		count += stats.Count
		sum += stats.SumR
	}
	if count != 16 || sum != 120 {
		t.Fatalf("aggregate count/sum = %d/%d, want 16/120", count, sum)
	}
}

func TestYCbCrMatchesStandardConversion(t *testing.T) {
	t.Parallel()
	y := []byte{40, 80, 120, 160}
	cb, cr := []byte{90}, []byte{200}
	reducer, err := New(Geometry{Columns: 1, Rows: 1})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := reducer.ReduceYCbCr(YCbCr{
		Y: y, Cb: cb, Cr: cr, Width: 2, Height: 2, YStride: 2,
		ChromaWidth: 1, ChromaHeight: 1, CbStride: 1, CrStride: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, luminance := range y {
		r, g, b := color.YCbCrToRGB(luminance, cb[0], cr[0])
		got := frame.Quadrants[i]
		if got.Count != 1 || got.SumR != uint64(r) || got.SumG != uint64(g) || got.SumB != uint64(b) {
			t.Fatalf("quadrant %d = %+v, want %d,%d,%d", i, got, r, g, b)
		}
	}
}

func TestUpscaleNeverLeavesEmptyQuadrants(t *testing.T) {
	t.Parallel()
	reducer, err := New(Geometry{Columns: 3, Rows: 2})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := reducer.ReduceRGB24(RGB24{Pix: []byte{10, 20, 30}, Width: 1, Height: 1, Stride: 3})
	if err != nil {
		t.Fatal(err)
	}
	for i, stats := range frame.Quadrants {
		if stats.Count != 1 {
			t.Fatalf("quadrant %d count = %d", i, stats.Count)
		}
	}
	solver, err := cellframe.NewSolver(cellframe.Config{Columns: 3, Rows: 2, Mode: cellframe.Detailed})
	if err != nil {
		t.Fatal(err)
	}
	cellFrame, err := solver.SolveStatistics(frame, cellframe.SourceMeta{})
	if err != nil {
		t.Fatal(err)
	}
	cellFrame.Release()
}

func TestFitGeometryBoundsExtremeTerminal(t *testing.T) {
	t.Parallel()
	geometry := FitGeometry(1920, 1080, 10_000, 4_000, true)
	if geometry.Columns*geometry.Rows > MaxCells {
		t.Fatalf("geometry exceeds bound: %+v", geometry)
	}
	whole := FitGeometry(1920, 1080, 80, 30, false)
	if whole.Columns != 80 || whole.Rows != 22 {
		t.Fatalf("whole geometry = %+v, want 80x22", whole)
	}
}

func BenchmarkYCbCr480x270To240x67(b *testing.B) {
	source := YCbCr{
		Y: make([]byte, 480*270), Cb: make([]byte, 240*270), Cr: make([]byte, 240*270),
		Width: 480, Height: 270, YStride: 480, ChromaWidth: 240, ChromaHeight: 270,
		CbStride: 240, CrStride: 240,
	}
	reducer, err := New(Geometry{Columns: 240, Rows: 67, Fill: true})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(source.Y) + len(source.Cb) + len(source.Cr)))
	for b.Loop() {
		if _, err := reducer.ReduceYCbCr(source); err != nil {
			b.Fatal(err)
		}
	}
}
