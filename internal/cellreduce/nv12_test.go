package cellreduce

import "testing"

func TestNV12KnownColorimetry(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		name       string
		colorRange ColorRange
		matrix     ColorMatrix
		y, cb, cr  byte
		want       [3]byte
		tolerance  byte
	}{
		{name: "full 601 black", colorRange: ColorRangeFull, matrix: ColorMatrixBT601, y: 0, cb: 128, cr: 128, want: [3]byte{0, 0, 0}},
		{name: "full 601 middle gray", colorRange: ColorRangeFull, matrix: ColorMatrixBT601, y: 128, cb: 128, cr: 128, want: [3]byte{128, 128, 128}},
		{name: "full 601 white", colorRange: ColorRangeFull, matrix: ColorMatrixBT601, y: 255, cb: 128, cr: 128, want: [3]byte{255, 255, 255}},
		{name: "full 601 red", colorRange: ColorRangeFull, matrix: ColorMatrixBT601, y: 76, cb: 85, cr: 255, want: [3]byte{254, 0, 0}, tolerance: 1},
		{name: "full 709 red", colorRange: ColorRangeFull, matrix: ColorMatrixBT709, y: 54, cb: 99, cr: 255, want: [3]byte{254, 0, 0}, tolerance: 1},
		{name: "video 601 black", colorRange: ColorRangeVideo, matrix: ColorMatrixBT601, y: 16, cb: 128, cr: 128, want: [3]byte{0, 0, 0}},
		{name: "video 601 middle gray", colorRange: ColorRangeVideo, matrix: ColorMatrixBT601, y: 126, cb: 128, cr: 128, want: [3]byte{128, 128, 128}},
		{name: "video 601 white", colorRange: ColorRangeVideo, matrix: ColorMatrixBT601, y: 235, cb: 128, cr: 128, want: [3]byte{255, 255, 255}},
		{name: "video 601 red", colorRange: ColorRangeVideo, matrix: ColorMatrixBT601, y: 81, cb: 90, cr: 240, want: [3]byte{254, 0, 0}, tolerance: 1},
		{name: "video 709 red", colorRange: ColorRangeVideo, matrix: ColorMatrixBT709, y: 63, cb: 102, cr: 240, want: [3]byte{255, 1, 0}, tolerance: 1},
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			reducer, err := New(Geometry{Columns: 1, Rows: 1})
			if err != nil {
				t.Fatal(err)
			}
			frame, err := reducer.ReduceNV12(NV12{
				Y:     []byte{fixture.y, fixture.y, fixture.y, fixture.y},
				UV:    []byte{fixture.cb, fixture.cr},
				Width: 2, Height: 2, YStride: 2, UVStride: 2,
				Range: fixture.colorRange, Matrix: fixture.matrix,
			})
			if err != nil {
				t.Fatal(err)
			}
			for index, stats := range frame.Quadrants {
				if stats.Count != 1 || !nearByte(byte(stats.SumR), fixture.want[0], fixture.tolerance) ||
					!nearByte(byte(stats.SumG), fixture.want[1], fixture.tolerance) ||
					!nearByte(byte(stats.SumB), fixture.want[2], fixture.tolerance) {
					t.Fatalf("quadrant %d = (%d,%d,%d) count %d, want %v +/- %d", index, stats.SumR, stats.SumG, stats.SumB, stats.Count, fixture.want, fixture.tolerance)
				}
			}
		})
	}
}

func TestNV12AcceptsPaddedStridesAndKeepsChromaPairs(t *testing.T) {
	t.Parallel()
	reducer, err := New(Geometry{Columns: 2, Rows: 1})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := reducer.ReduceNV12(NV12{
		Y: []byte{
			16, 235, 81, 81, 3, 4,
			126, 16, 81, 81, 5, 6,
		},
		UV:    []byte{128, 128, 90, 240, 7, 8},
		Width: 4, Height: 2, YStride: 6, UVStride: 6,
		Range: ColorRangeVideo, Matrix: ColorMatrixBT601,
	})
	if err != nil {
		t.Fatal(err)
	}
	wants := [][3]byte{
		{0, 0, 0}, {255, 255, 255}, {128, 128, 128}, {0, 0, 0},
		{254, 0, 0}, {254, 0, 0}, {254, 0, 0}, {254, 0, 0},
	}
	for index, want := range wants {
		stats := frame.Quadrants[index]
		if stats.Count != 1 || !nearByte(byte(stats.SumR), want[0], 1) || !nearByte(byte(stats.SumG), want[1], 1) || !nearByte(byte(stats.SumB), want[2], 1) {
			t.Fatalf("quadrant %d = (%d,%d,%d) count %d, want %v +/- 1", index, stats.SumR, stats.SumG, stats.SumB, stats.Count, want)
		}
	}
}

func TestNV12FillConvertsOnlyCroppedSamples(t *testing.T) {
	t.Parallel()
	reducer, err := New(Geometry{Columns: 1, Rows: 1, Fill: true})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := reducer.ReduceNV12(NV12{
		Y: []byte{
			16, 16, 235, 16, 16, 16,
			16, 16, 235, 16, 16, 16,
		},
		UV:    []byte{0, 0, 128, 128, 255, 255},
		Width: 6, Height: 2, YStride: 6, UVStride: 6,
		Range: ColorRangeVideo, Matrix: ColorMatrixBT709,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, stats := range frame.Quadrants {
		if stats.Count != 1 || stats.SumR != 255 || stats.SumG != 255 || stats.SumB != 255 {
			t.Fatalf("quadrant %d = %+v, want one white contributing sample", index, stats)
		}
	}
}

func TestNV12RequiresValidMetadataAndBuffers(t *testing.T) {
	t.Parallel()
	valid := NV12{
		Y: make([]byte, 8), UV: make([]byte, 4),
		Width: 2, Height: 4, YStride: 2, UVStride: 2,
		Range: ColorRangeFull, Matrix: ColorMatrixBT601,
	}
	tests := []struct {
		name   string
		mutate func(*NV12)
	}{
		{name: "missing range", mutate: func(source *NV12) { source.Range = 0 }},
		{name: "unknown range", mutate: func(source *NV12) { source.Range = ColorRange(255) }},
		{name: "missing matrix", mutate: func(source *NV12) { source.Matrix = 0 }},
		{name: "unknown matrix", mutate: func(source *NV12) { source.Matrix = ColorMatrix(255) }},
		{name: "short luma stride", mutate: func(source *NV12) { source.YStride = 1 }},
		{name: "short chroma stride", mutate: func(source *NV12) {
			source.Width = 4
			source.YStride = 4
			source.Y = make([]byte, 16)
			source.UVStride = 3
		}},
		{name: "truncated luma", mutate: func(source *NV12) { source.Y = source.Y[:7] }},
		{name: "truncated chroma", mutate: func(source *NV12) { source.UV = source.UV[:3] }},
		{name: "overflowing luma stride", mutate: func(source *NV12) { source.YStride = int(^uint(0)/3 + 1); source.Y = make([]byte, 5) }},
		{name: "overflowing chroma stride", mutate: func(source *NV12) {
			source.Height = 8
			source.Y = make([]byte, 16)
			source.UVStride = int(^uint(0)/3 + 1)
			source.UV = make([]byte, 5)
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			source := valid
			test.mutate(&source)
			reducer, err := New(Geometry{Columns: 1, Rows: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reducer.ReduceNV12(source); err == nil {
				t.Fatal("invalid NV12 source was accepted")
			}
		})
	}
}

func TestNV12SteadyLayoutAllocatesNothing(t *testing.T) {
	source := NV12{
		Y: make([]byte, 10*4), UV: make([]byte, 10*2),
		Width: 8, Height: 4, YStride: 10, UVStride: 10,
		Range: ColorRangeVideo, Matrix: ColorMatrixBT709,
	}
	reducer, err := New(Geometry{Columns: 4, Rows: 2, Mirror: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reducer.ReduceNV12(source); err != nil {
		t.Fatal(err)
	}
	columns := &reducer.sampling.columns[0]
	rows := &reducer.sampling.rows[0]
	if _, err := reducer.ReduceNV12(source); err != nil {
		t.Fatal(err)
	}
	if columns != &reducer.sampling.columns[0] || rows != &reducer.sampling.rows[0] {
		t.Fatal("steady source layout replaced the cached sampling plan")
	}
	allocations := testing.AllocsPerRun(100, func() {
		if _, reduceErr := reducer.ReduceNV12(source); reduceErr != nil {
			panic(reduceErr)
		}
	})
	if allocations != 0 {
		t.Fatalf("steady NV12 reduction allocations = %.2f, want 0", allocations)
	}
}

func BenchmarkNV12480x270To240x67(b *testing.B) {
	source := NV12{
		Y: make([]byte, 480*270), UV: make([]byte, 480*135),
		Width: 480, Height: 270, YStride: 480, UVStride: 480,
		Range: ColorRangeVideo, Matrix: ColorMatrixBT709,
	}
	reducer, err := New(Geometry{Columns: 240, Rows: 67, Fill: true})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := reducer.ReduceNV12(source); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(source.Y) + len(source.UV)))
	for b.Loop() {
		if _, err := reducer.ReduceNV12(source); err != nil {
			b.Fatal(err)
		}
	}
}

func nearByte(got, want, tolerance byte) bool {
	if got > want {
		return got-want <= tolerance
	}
	return want-got <= tolerance
}
