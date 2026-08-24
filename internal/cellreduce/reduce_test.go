package cellreduce

import (
	"image/color"
	"math"
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

func TestYCbCrMirrorMatchesExactHorizontalReflection(t *testing.T) {
	t.Parallel()
	source := YCbCr{
		Y: []byte{
			10, 30, 70, 110,
			150, 170, 210, 250,
		},
		Cb: []byte{80, 100, 120, 140}, Cr: []byte{160, 180, 200, 220},
		Width: 4, Height: 2, YStride: 4,
		ChromaWidth: 2, ChromaHeight: 2, CbStride: 2, CrStride: 2,
	}
	plain, err := New(Geometry{Columns: 2, Rows: 1})
	if err != nil {
		t.Fatal(err)
	}
	plainFrame, err := plain.ReduceYCbCr(source)
	if err != nil {
		t.Fatal(err)
	}
	mirrored, err := New(Geometry{Columns: 2, Rows: 1, Mirror: true})
	if err != nil {
		t.Fatal(err)
	}
	mirroredFrame, err := mirrored.ReduceYCbCr(source)
	if err != nil {
		t.Fatal(err)
	}
	for cellX := range 2 {
		for quadrant := range 4 {
			got := mirroredFrame.Quadrants[cellX*4+quadrant]
			want := plainFrame.Quadrants[(1-cellX)*4+(quadrant^1)]
			if got != want {
				t.Fatalf("cell %d quadrant %d = %+v, want %+v", cellX, quadrant, got, want)
			}
		}
	}
}

func TestYCbCrSteadyLayoutReusesPlansAndAllocatesNothing(t *testing.T) {
	source := YCbCr{
		Y: make([]byte, 8*4), Cb: make([]byte, 4*4), Cr: make([]byte, 4*4),
		Width: 8, Height: 4, YStride: 8,
		ChromaWidth: 4, ChromaHeight: 4, CbStride: 4, CrStride: 4,
	}
	reducer, err := New(Geometry{Columns: 4, Rows: 2, Mirror: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reducer.ReduceYCbCr(source); err != nil {
		t.Fatal(err)
	}
	columns := &reducer.sampling.columns[0]
	rows := &reducer.sampling.rows[0]
	chromaX := &reducer.ycbcr.chromaX[0]
	sourceRows := &reducer.ycbcr.sourceRows[0]
	if _, err := reducer.ReduceYCbCr(source); err != nil {
		t.Fatal(err)
	}
	if columns != &reducer.sampling.columns[0] || rows != &reducer.sampling.rows[0] ||
		chromaX != &reducer.ycbcr.chromaX[0] || sourceRows != &reducer.ycbcr.sourceRows[0] {
		t.Fatal("steady source layout replaced a cached sampling plan")
	}
	allocations := testing.AllocsPerRun(100, func() {
		if _, reduceErr := reducer.ReduceYCbCr(source); reduceErr != nil {
			panic(reduceErr)
		}
	})
	if allocations != 0 {
		t.Fatalf("steady YCbCr reduction allocations = %.2f, want 0", allocations)
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
	extreme := FitGeometry(1920, 1080, math.MaxInt, 1, true)
	if extreme.Columns != MaxCells || extreme.Rows != 1 {
		t.Fatalf("extreme geometry = %+v, want %dx1", extreme, MaxCells)
	}
}

func TestRGB24RejectsOverflowingStrideWithoutPanic(t *testing.T) {
	reducer, err := New(Geometry{Columns: 1, Rows: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Three rows at this stride wrap to offset 2 in 64-bit arithmetic. A
	// wrapped size check would accept five bytes, then panic while sampling.
	stride := int(^uint(0)/3 + 1)
	if _, err := reducer.ReduceRGB24(RGB24{
		Pix: make([]byte, 5), Width: 1, Height: 4, Stride: stride,
	}); err == nil {
		t.Fatal("overflowing RGB24 stride was accepted")
	}
}

func BenchmarkYCbCr480x270To240x67(b *testing.B) {
	benchmarkYCbCr480x270To240x67(b, false)
}

func BenchmarkYCbCr480x270To240x67Mirrored(b *testing.B) {
	benchmarkYCbCr480x270To240x67(b, true)
}

func benchmarkYCbCr480x270To240x67(b *testing.B, mirror bool) {
	source := YCbCr{
		Y: make([]byte, 480*270), Cb: make([]byte, 240*270), Cr: make([]byte, 240*270),
		Width: 480, Height: 270, YStride: 480, ChromaWidth: 240, ChromaHeight: 270,
		CbStride: 240, CrStride: 240,
	}
	reducer, err := New(Geometry{Columns: 240, Rows: 67, Fill: true, Mirror: mirror})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := reducer.ReduceYCbCr(source); err != nil {
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
