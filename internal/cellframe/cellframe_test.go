package cellframe

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestComplementFormsHaveOneCanonicalCell(t *testing.T) {
	colors := []RGB{
		NewRGB(0, 0, 0),
		NewRGB(255, 255, 255),
		NewRGB(240, 17, 93),
	}
	for mask := uint8(0); mask < 16; mask++ {
		for _, foreground := range colors {
			for _, background := range colors {
				got := NewCell(mask, foreground, background)
				complement := NewCell(mask^0x0f, background, foreground)
				if got != complement {
					t.Fatalf("mask %04b complement differs: %#x != %#x", mask, got.Packed(), complement.Packed())
				}
				if !got.IsCanonical() || got.Mask() > 7 {
					t.Fatalf("mask %04b produced non-canonical cell %#x", mask, got.Packed())
				}
				if foreground == background && (got.Mask() != 0 || got.Foreground() != got.Background()) {
					t.Fatalf("equal colors did not collapse: mask=%d fg=%#x bg=%#x", got.Mask(), got.Foreground(), got.Background())
				}
			}
		}
	}

	foreground, background := NewRGB(1, 2, 3), NewRGB(4, 5, 6)
	rawComplement := uint64(0x0a) | uint64(foreground.Packed())<<4 | uint64(background.Packed())<<28
	decoded, err := CellFromPacked(rawComplement)
	if err != nil {
		t.Fatal(err)
	}
	if want := NewCell(0x0a, foreground, background); decoded != want {
		t.Fatalf("CellFromPacked() = %#x, want %#x", decoded.Packed(), want.Packed())
	}
	if _, err := CellFromPacked(uint64(1) << 60); err == nil {
		t.Fatal("CellFromPacked accepted reserved bits")
	}

	wantRunes := []rune{' ', '▘', '▝', '▀', '▖', '▌', '▞', '▛'}
	for mask, want := range wantRunes {
		if got := NewCell(uint8(mask), foreground, background).QuadrantRune(); got != want {
			t.Errorf("mask %d rune = %q, want %q", mask, got, want)
		}
	}
}

func TestDetailedSolverDeterministicEdgesAndTies(t *testing.T) {
	solver := mustSolver(t, Config{Columns: 1, Rows: 1, Mode: ModeDetailed, Buffers: 2})
	tests := []struct {
		name   string
		pixels [4]RGB
		want   Cell
	}{
		{
			name:   "all black chooses solid mask zero",
			pixels: [4]RGB{},
			want:   NewCell(0, NewRGB(0, 0, 0), NewRGB(0, 0, 0)),
		},
		{
			name: "all white chooses solid mask zero",
			pixels: [4]RGB{
				NewRGB(255, 255, 255), NewRGB(255, 255, 255),
				NewRGB(255, 255, 255), NewRGB(255, 255, 255),
			},
			want: NewCell(0, NewRGB(255, 255, 255), NewRGB(255, 255, 255)),
		},
		{
			// These colors have equal integer BT.601 luma (77), so a luma
			// edge test cannot separate them. RGB least squares must.
			name: "saturated equal-luma vertical edge",
			pixels: [4]RGB{
				NewRGB(255, 0, 0), NewRGB(0, 131, 0),
				NewRGB(255, 0, 0), NewRGB(0, 131, 0),
			},
			want: NewCell(0x05, NewRGB(255, 0, 0), NewRGB(0, 131, 0)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := rgb24ForCell(test.pixels)
			for sequence := uint64(1); sequence <= 20; sequence++ {
				frame, err := solver.SolveRGB24(input, SourceMeta{GeometryEpoch: 9, SourceSequence: sequence})
				if err != nil {
					t.Fatal(err)
				}
				cell, ok := frame.Cell(0)
				if !ok || cell != test.want {
					frame.Release()
					t.Fatalf("cell = %#x, want %#x", cell.Packed(), test.want.Packed())
				}
				frame.Release()
			}
		})
	}
}

func TestIntegerLeastSquaresRoundsNearestWithLowerTie(t *testing.T) {
	solver := mustSolver(t, Config{Columns: 1, Rows: 1, Mode: ModeDetailed, Buffers: 1})
	stats := SampleStats{Count: 3, SumR: 2, SumSquares: 2} // samples 0, 1, 1
	frame, err := solver.SolveStatistics(StatisticsFrame{
		Quadrants: []SampleStats{stats, stats, stats, stats},
		Columns:   1,
		Rows:      1,
	}, SourceMeta{})
	if err != nil {
		t.Fatal(err)
	}
	defer frame.Release()
	cell, _ := frame.Cell(0)
	if got, want := cell.Background(), NewRGB(1, 0, 0); got != want {
		t.Fatalf("mean color = %#x, want nearest integer %#x", got.Packed(), want.Packed())
	}
	quadrants := [4]SampleStats{stats, stats, stats, stats}
	if got, want := referenceStatisticsCellError(cell, quadrants), uint64(4); got != want {
		t.Fatalf("error = %d, want %d", got, want)
	}

	// An exact half has two integer minimizers and must choose the lower one.
	if got := nearestIntegerLowerTie(1, 2); got != 0 {
		t.Fatalf("lower tie = %d, want 0", got)
	}
}

func TestDetailedMatchesBruteForceAndBeatsThresholdBaseline(t *testing.T) {
	solver := mustSolver(t, Config{Columns: 1, Rows: 1, Mode: ModeDetailed, Buffers: 1})
	palette := []RGB{
		NewRGB(0, 0, 0),
		NewRGB(255, 255, 255),
		NewRGB(255, 0, 0),
		NewRGB(0, 131, 0),
		NewRGB(0, 0, 255),
	}
	var sequence uint64
	for _, p0 := range palette {
		for _, p1 := range palette {
			for _, p2 := range palette {
				for _, p3 := range palette {
					sequence++
					pixels := [4]RGB{p0, p1, p2, p3}
					wantCell, wantError := referenceDetailed(pixels)
					frame, err := solver.SolveRGB24(rgb24ForCell(pixels), SourceMeta{SourceSequence: sequence})
					if err != nil {
						t.Fatal(err)
					}
					gotCell, _ := frame.Cell(0)
					gotError := referenceCellError(pixels, gotCell)
					if gotCell != wantCell || gotError != wantError {
						frame.Release()
						t.Fatalf("pixels %#v: got cell=%#x err=%d, brute cell=%#x err=%d", pixels, gotCell.Packed(), gotError, wantCell.Packed(), wantError)
					}
					for _, threshold := range []uint8{32, 64, 128, 192, 224} {
						baselineError := referenceThresholdError(pixels, threshold)
						if gotError > baselineError {
							frame.Release()
							t.Fatalf("pixels %#v threshold %d: solver error %d > baseline %d", pixels, threshold, gotError, baselineError)
						}
					}
					frame.Release()
				}
			}
		}
	}
}

func TestAggregatePartitionScoringMatchesReference(t *testing.T) {
	var state uint32 = 0x7f4a7c15
	for testCase := 0; testCase < 256; testCase++ {
		var quadrants [4]SampleStats
		for quadrant := range quadrants {
			samples := 1 + (testCase+quadrant)%7
			for range samples {
				state = state*1664525 + 1013904223
				quadrants[quadrant].AddRGB(byte(state>>24), byte(state>>16), byte(state>>8))
			}
		}
		for _, mode := range []Mode{ModeDetailed, ModeSoft} {
			gotCell, gotError := solveCell(quadrants, mode)
			wantCell, wantError := referenceStatisticsSolve(quadrants, mode)
			if gotCell != wantCell || gotError != wantError {
				t.Fatalf("case %d mode %d: got cell=%#x error=%d, want cell=%#x error=%d", testCase, mode, gotCell.Packed(), gotError, wantCell.Packed(), wantError)
			}
		}
	}
}

func TestRGBPlanarAndStatisticsAreEquivalent(t *testing.T) {
	pixels := [4]RGB{
		NewRGB(3, 200, 70), NewRGB(240, 8, 100),
		NewRGB(40, 20, 255), NewRGB(210, 220, 4),
	}
	solver := mustSolver(t, Config{Columns: 1, Rows: 1, Mode: ModeDetailed, Buffers: 3})
	meta := SourceMeta{GeometryEpoch: 4, SourceSequence: 7, PTS: 17 * time.Millisecond}
	rgbFrame, err := solver.SolveRGB24(rgb24ForCell(pixels), meta)
	if err != nil {
		t.Fatal(err)
	}
	defer rgbFrame.Release()

	planar := planarForCell(pixels)
	planarFrame, err := solver.SolvePlanar(planar, meta)
	if err != nil {
		t.Fatal(err)
	}
	defer planarFrame.Release()

	stats := make([]SampleStats, 4)
	for i, pixel := range pixels {
		stats[i].Add(pixel)
	}
	statsFrame, err := solver.SolveStatistics(StatisticsFrame{Quadrants: stats, Columns: 1, Rows: 1}, meta)
	if err != nil {
		t.Fatal(err)
	}
	defer statsFrame.Release()

	wantCell, _ := rgbFrame.Cell(0)
	for name, frame := range map[string]*CellFrame{"planar": planarFrame, "statistics": statsFrame} {
		gotCell, _ := frame.Cell(0)
		if gotCell != wantCell || frame.GeometryEpoch() != meta.GeometryEpoch || frame.SourceSequence() != meta.SourceSequence || frame.SourcePTS() != meta.PTS {
			t.Errorf("%s differs: cell=%#x meta=%d/%d/%s; RGB cell=%#x",
				name, gotCell.Packed(), frame.GeometryEpoch(), frame.SourceSequence(), frame.SourcePTS(), wantCell.Packed())
		}
	}
}

func TestSoftIsFixedUpperHalfBlock(t *testing.T) {
	pixels := [4]RGB{
		NewRGB(255, 0, 0), NewRGB(0, 0, 255),
		NewRGB(0, 255, 0), NewRGB(255, 255, 255),
	}
	solver := mustSolver(t, Config{Columns: 1, Rows: 1, Mode: ModeSoft, Buffers: 1})
	frame, err := solver.SolveRGB24(rgb24ForCell(pixels), SourceMeta{})
	if err != nil {
		t.Fatal(err)
	}
	defer frame.Release()
	cell, _ := frame.Cell(0)
	want := NewCell(0x03, NewRGB(127, 0, 127), NewRGB(127, 255, 127))
	if cell != want || cell.QuadrantRune() != '▀' {
		t.Fatalf("soft cell = %#x %q, want %#x %q", cell.Packed(), cell.QuadrantRune(), want.Packed(), want.QuadrantRune())
	}
}

func TestExactDimensionsBoundsAndCellBounds(t *testing.T) {
	for index, config := range []Config{
		{},
		{Columns: maxCells + 1, Rows: 1, Mode: ModeDetailed},
		{Columns: 1, Rows: 1, Mode: Mode(99)},
		{Columns: 1, Rows: 1, Mode: ModeDetailed, Buffers: maxFrameBuffers + 1},
		{Columns: maxCells, Rows: 1, Mode: ModeDetailed, Buffers: maxPooledCells/maxCells + 1},
	} {
		if solver, err := NewSolver(config); err == nil {
			_ = solver
			t.Errorf("invalid solver config %d succeeded", index)
		}
	}

	solver := mustSolver(t, Config{Columns: 2, Rows: 1, Mode: ModeDetailed, Buffers: 1})
	valid := RGB24{Pix: make([]byte, 24), Width: 4, Height: 2, Stride: 12}
	tests := []RGB24{
		{Pix: valid.Pix, Width: 3, Height: 2, Stride: 12},
		{Pix: valid.Pix, Width: 4, Height: 1, Stride: 12},
		{Pix: valid.Pix, Width: 4, Height: 2, Stride: 11},
		{Pix: valid.Pix[:23], Width: 4, Height: 2, Stride: 12},
	}
	for index, input := range tests {
		if frame, err := solver.SolveRGB24(input, SourceMeta{}); err == nil {
			frame.Release()
			t.Errorf("invalid RGB case %d succeeded", index)
		}
	}

	planar := PlanarRGB{
		R: make([]byte, 8), G: make([]byte, 8), B: make([]byte, 8),
		Width: 4, Height: 2, RStride: 4, GStride: 4, BStride: 4,
	}
	planar.B = planar.B[:7]
	if frame, err := solver.SolvePlanar(planar, SourceMeta{}); err == nil {
		frame.Release()
		t.Error("truncated planar input succeeded")
	}

	badStats := make([]SampleStats, 8)
	for i := range badStats {
		badStats[i].AddRGB(1, 2, 3)
	}
	if frame, err := solver.SolveStatistics(StatisticsFrame{Quadrants: badStats[:7], Columns: 2, Rows: 1}, SourceMeta{}); err == nil {
		frame.Release()
		t.Error("wrong statistics count succeeded")
	}
	badStats[3] = SampleStats{}
	if frame, err := solver.SolveStatistics(StatisticsFrame{Quadrants: badStats, Columns: 2, Rows: 1}, SourceMeta{}); err == nil {
		frame.Release()
		t.Error("empty quadrant statistics succeeded")
	}

	frame, err := solver.SolveRGB24(valid, SourceMeta{})
	if err != nil {
		t.Fatal(err)
	}
	defer frame.Release()
	if frame.Columns() != 2 || frame.Rows() != 1 || frame.Len() != 2 {
		t.Fatalf("frame geometry = %dx%d len=%d", frame.Columns(), frame.Rows(), frame.Len())
	}
	for _, point := range [][2]int{{-1, 0}, {2, 0}, {0, -1}, {0, 1}} {
		if _, ok := frame.CellAt(point[0], point[1]); ok {
			t.Errorf("CellAt(%d,%d) unexpectedly succeeded", point[0], point[1])
		}
	}
	if _, ok := frame.Cell(-1); ok {
		t.Error("Cell(-1) unexpectedly succeeded")
	}
	if _, ok := frame.Cell(2); ok {
		t.Error("Cell(2) unexpectedly succeeded")
	}
}

func TestInputReuseDoesNotAliasLiveFrames(t *testing.T) {
	solver := mustSolver(t, Config{Columns: 1, Rows: 1, Mode: ModeDetailed, Buffers: 3})
	pixels := [4]RGB{NewRGB(1, 2, 3), NewRGB(4, 5, 6), NewRGB(7, 8, 9), NewRGB(10, 11, 12)}
	input := rgb24ForCell(pixels)
	first, err := solver.SolveRGB24(input, SourceMeta{
		GeometryEpoch: 2, SourceSequence: 1, PTS: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := solver.SolveRGB24(input, SourceMeta{
		GeometryEpoch: 2, SourceSequence: 99, PTS: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCell, _ := first.Cell(0)
	for i := range input.Pix {
		input.Pix[i] = 255
	}
	if got, _ := first.Cell(0); got != wantCell {
		t.Fatalf("source buffer mutation aliased frame: cell %#x -> %#x", wantCell.Packed(), got.Packed())
	}
	third, err := solver.SolveRGB24(input, SourceMeta{GeometryEpoch: 2, SourceSequence: 100})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := third.Cell(0); got == wantCell {
		third.Release()
		t.Fatal("changed pixels did not change the solved cell")
	}
	third.Release()
	second.Release()
}

func TestFramePoolLeaseAndRetain(t *testing.T) {
	solver := mustSolver(t, Config{Columns: 1, Rows: 1, Mode: ModeDetailed, Buffers: 1})
	input := rgb24ForCell([4]RGB{})
	frame, err := solver.SolveRGB24(input, SourceMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if err := frame.Retain(); err != nil {
		t.Fatal(err)
	}
	if _, err := solver.SolveRGB24(input, SourceMeta{}); !errors.Is(err, ErrFramePoolExhausted) {
		t.Fatalf("pool exhaustion error = %v, want %v", err, ErrFramePoolExhausted)
	}
	frame.Release()
	if !frame.Valid() {
		t.Fatal("frame invalid after releasing only one of two owners")
	}
	if _, err := solver.SolveRGB24(input, SourceMeta{}); !errors.Is(err, ErrFramePoolExhausted) {
		t.Fatalf("retained buffer was reused: %v", err)
	}
	frame.Release()
	if frame.Valid() {
		t.Fatal("frame still valid after final release")
	}
	next, err := solver.SolveRGB24(input, SourceMeta{})
	if err != nil {
		t.Fatalf("released buffer was not reusable: %v", err)
	}
	next.Release()
}

func TestDeadbandIsBoundedAndSceneCutSafe(t *testing.T) {
	solver := mustSolver(t, Config{Columns: 1, Rows: 1, Mode: ModeDetailed, Buffers: 1})
	policy, err := NewDeadbandPolicy(1, 1, DeadbandConfig{
		MaxCellErrorIncrease: 12,
		SceneCutMSE:          100,
		MaxAge:               2,
	})
	if err != nil {
		t.Fatal(err)
	}

	solveGray := func(sequence uint64, epoch uint64, value uint8) Cell {
		color := NewRGB(value, value, value)
		frame, solveErr := solver.SolveRGB24WithPolicy(
			rgb24ForCell([4]RGB{color, color, color, color}),
			SourceMeta{GeometryEpoch: epoch, SourceSequence: sequence},
			policy,
		)
		if solveErr != nil {
			t.Fatal(solveErr)
		}
		cell, _ := frame.Cell(0)
		frame.Release()
		return cell
	}

	ten := NewCell(0, NewRGB(10, 10, 10), NewRGB(10, 10, 10))
	eleven := NewCell(0, NewRGB(11, 11, 11), NewRGB(11, 11, 11))
	if got := solveGray(1, 1, 10); got != ten {
		t.Fatalf("initial cell = %#x, want %#x", got.Packed(), ten.Packed())
	}
	if got := solveGray(2, 1, 11); got != ten {
		t.Fatalf("first deadband hold = %#x, want %#x", got.Packed(), ten.Packed())
	}
	if got := solveGray(3, 1, 11); got != ten {
		t.Fatalf("second deadband hold = %#x, want %#x", got.Packed(), ten.Packed())
	}
	if got := solveGray(4, 1, 11); got != eleven {
		t.Fatalf("MaxAge did not force refresh: %#x, want %#x", got.Packed(), eleven.Packed())
	}

	white := NewCell(0, NewRGB(255, 255, 255), NewRGB(255, 255, 255))
	if got := solveGray(5, 1, 255); got != white {
		t.Fatalf("scene cut retained stale cell: %#x, want %#x", got.Packed(), white.Packed())
	}

	// Geometry changes reset history even when the pixel delta is in-band.
	nearWhite := NewCell(0, NewRGB(254, 254, 254), NewRGB(254, 254, 254))
	if got := solveGray(6, 2, 254); got != nearWhite {
		t.Fatalf("geometry epoch retained stale cell: %#x, want %#x", got.Packed(), nearWhite.Packed())
	}
}

func TestSolverSteadyStateAllocations(t *testing.T) {
	solver := mustSolver(t, Config{Columns: 8, Rows: 4, Mode: ModeDetailed, Buffers: 1})
	input := patternedRGB24(8, 4)
	frame, err := solver.SolveRGB24(input, SourceMeta{})
	if err != nil {
		t.Fatal(err)
	}
	frame.Release()
	allocs := testing.AllocsPerRun(1000, func() {
		frame, solveErr := solver.SolveRGB24(input, SourceMeta{})
		if solveErr != nil {
			panic(solveErr)
		}
		frame.Release()
	})
	if allocs != 0 {
		t.Fatalf("steady-state allocations = %.2f, want 0", allocs)
	}
}

func mustSolver(t *testing.T, cfg Config) *Solver {
	t.Helper()
	solver, err := NewSolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return solver
}

func rgb24ForCell(pixels [4]RGB) RGB24 {
	pix := make([]byte, 12)
	order := [4]int{0, 1, 2, 3}
	for i, pixelIndex := range order {
		pixel := pixels[pixelIndex]
		offset := i * 3
		pix[offset], pix[offset+1], pix[offset+2] = pixel.R(), pixel.G(), pixel.B()
	}
	return RGB24{Pix: pix, Width: 2, Height: 2, Stride: 6}
}

func planarForCell(pixels [4]RGB) PlanarRGB {
	r, g, b := make([]byte, 4), make([]byte, 4), make([]byte, 4)
	for i, pixel := range pixels {
		r[i], g[i], b[i] = pixel.R(), pixel.G(), pixel.B()
	}
	return PlanarRGB{R: r, G: g, B: b, Width: 2, Height: 2, RStride: 2, GStride: 2, BStride: 2}
}

func patternedRGB24(columns, rows int) RGB24 {
	width, height := columns*2, rows*2
	pix := make([]byte, width*height*3)
	var state uint32 = 0x12345678
	for i := range pix {
		state = state*1664525 + 1013904223
		pix[i] = byte(state >> 24)
	}
	return RGB24{Pix: pix, Width: width, Height: height, Stride: width * 3}
}

func referenceDetailed(pixels [4]RGB) (Cell, uint64) {
	bestCell, bestError := referencePartition(pixels, 0)
	for mask := uint8(1); mask < 8; mask++ {
		cell, cellError := referencePartition(pixels, mask)
		if cellError < bestError {
			bestCell, bestError = cell, cellError
		}
	}
	return bestCell, bestError
}

func referencePartition(pixels [4]RGB, mask uint8) (Cell, uint64) {
	type group struct {
		count   uint64
		r, g, b uint64
	}
	var foreground, background group
	for quadrant, pixel := range pixels {
		group := &background
		if mask&(1<<quadrant) != 0 {
			group = &foreground
		}
		group.count++
		group.r += uint64(pixel.R())
		group.g += uint64(pixel.G())
		group.b += uint64(pixel.B())
	}
	mean := func(group group) RGB {
		if group.count == 0 {
			return 0
		}
		round := func(sum uint64) uint8 {
			quotient, remainder := sum/group.count, sum%group.count
			if remainder > group.count-remainder {
				quotient++
			}
			return uint8(quotient)
		}
		return NewRGB(round(group.r), round(group.g), round(group.b))
	}
	fg, bg := mean(foreground), mean(background)
	if foreground.count == 0 {
		fg = bg
	}
	if background.count == 0 {
		bg = fg
	}
	cell := NewCell(mask, fg, bg)
	return cell, referenceCellError(pixels, cell)
}

func referenceCellError(pixels [4]RGB, cell Cell) uint64 {
	var total uint64
	for quadrant, pixel := range pixels {
		candidate := cell.ColorAt(quadrant)
		dr := int64(pixel.R()) - int64(candidate.R())
		dg := int64(pixel.G()) - int64(candidate.G())
		db := int64(pixel.B()) - int64(candidate.B())
		total += uint64(dr*dr + dg*dg + db*db)
	}
	return total
}

func referenceStatisticsSolve(quadrants [4]SampleStats, mode Mode) (Cell, uint64) {
	if mode == ModeSoft {
		return referenceStatisticsPartition(quadrants, 0x03)
	}
	bestCell, bestError := referenceStatisticsPartition(quadrants, 0)
	for mask := uint8(1); mask < 8; mask++ {
		candidate, candidateError := referenceStatisticsPartition(quadrants, mask)
		if candidateError < bestError {
			bestCell, bestError = candidate, candidateError
		}
	}
	return bestCell, bestError
}

func referenceStatisticsPartition(quadrants [4]SampleStats, mask uint8) (Cell, uint64) {
	var foreground, background SampleStats
	for quadrant, stats := range quadrants {
		group := &background
		if mask&(1<<quadrant) != 0 {
			group = &foreground
		}
		group.Count += stats.Count
		group.SumR += stats.SumR
		group.SumG += stats.SumG
		group.SumB += stats.SumB
		group.SumSquares += stats.SumSquares
	}
	fg, bg := foreground.meanLower(), background.meanLower()
	if foreground.Count == 0 {
		fg = bg
	}
	if background.Count == 0 {
		bg = fg
	}
	cell := NewCell(mask, fg, bg)
	return cell, referenceStatisticsCellError(cell, quadrants)
}

func referenceStatisticsCellError(cell Cell, quadrants [4]SampleStats) uint64 {
	var total uint64
	for quadrant, stats := range quadrants {
		color := cell.ColorAt(quadrant)
		r, g, b := uint64(color.R()), uint64(color.G()), uint64(color.B())
		total += stats.SumSquares + stats.Count*(r*r+g*g+b*b) - 2*(r*stats.SumR+g*stats.SumG+b*stats.SumB)
	}
	return total
}

func referenceThresholdError(pixels [4]RGB, threshold uint8) uint64 {
	var mask uint8
	for quadrant, pixel := range pixels {
		// Integer BT.601 luma, rounded, matching a conventional threshold path.
		luma := (77*uint32(pixel.R()) + 150*uint32(pixel.G()) + 29*uint32(pixel.B()) + 128) >> 8
		if luma >= uint32(threshold) {
			mask |= 1 << quadrant
		}
	}
	_, cellError := referencePartition(pixels, mask)
	return cellError
}

func TestReferenceErrorRange(t *testing.T) {
	// Guard the test oracle against signed conversion mistakes at the extrema.
	pixels := [4]RGB{NewRGB(0, 0, 0), NewRGB(255, 255, 255), NewRGB(0, 0, 0), NewRGB(255, 255, 255)}
	_, got := referencePartition(pixels, 0)
	if got > uint64(len(pixels))*maxPixelSquaredError || got > math.MaxUint32 {
		t.Fatalf("reference error out of range: %d", got)
	}
}
