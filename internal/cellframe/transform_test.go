package cellframe

import "testing"

type fixedTransform struct{ color RGB }

func (transform fixedTransform) TransformRGB(RGB) RGB { return transform.color }

func TestSolverColorTransformChangesColorsErrorAndHash(t *testing.T) {
	input := RGB24{
		Pix:    []byte{10, 20, 30, 10, 20, 30, 10, 20, 30, 10, 20, 30},
		Width:  2,
		Height: 2,
		Stride: 6,
	}
	plainSolver := mustSolver(t, Config{Columns: 1, Rows: 1, Mode: ModeDetailed, Buffers: 1})
	filteredSolver := mustSolver(t, Config{
		Columns:   1,
		Rows:      1,
		Mode:      ModeDetailed,
		Buffers:   1,
		Transform: fixedTransform{color: NewRGB(1, 2, 3)},
	})
	plain, err := plainSolver.SolveRGB24(input, SourceMeta{GeometryEpoch: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Release()
	filtered, err := filteredSolver.SolveRGB24(input, SourceMeta{GeometryEpoch: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer filtered.Release()

	cell, _ := filtered.Cell(0)
	if cell.Foreground() != NewRGB(1, 2, 3) || cell.Background() != NewRGB(1, 2, 3) {
		t.Fatalf("filtered cell colors = %#06x/%#06x", cell.Foreground().Packed(), cell.Background().Packed())
	}
	const wantError = uint64(4 * (9*9 + 18*18 + 27*27))
	if got := filtered.ReconstructionError(); got != wantError {
		t.Fatalf("filtered error = %d, want %d", got, wantError)
	}
	if filtered.Hash() == plain.Hash() {
		t.Fatal("transformed visual hash did not change")
	}
}

func TestNilTransformPreservesExistingResult(t *testing.T) {
	input := patternedRGB24(8, 4)
	a := mustSolver(t, Config{Columns: 8, Rows: 4, Mode: ModeDetailed, Buffers: 1})
	b := mustSolver(t, Config{Columns: 8, Rows: 4, Mode: ModeDetailed, Buffers: 1, Transform: nil})
	first, err := a.SolveRGB24(input, SourceMeta{GeometryEpoch: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := b.SolveRGB24(input, SourceMeta{GeometryEpoch: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if first.Hash() != second.Hash() || first.ReconstructionError() != second.ReconstructionError() {
		t.Fatal("nil transform changed solver result")
	}
}

func TestTransformRunsAfterDeadbandChoice(t *testing.T) {
	solver := mustSolver(t, Config{
		Columns:   1,
		Rows:      1,
		Mode:      ModeDetailed,
		Buffers:   1,
		Transform: fixedTransform{color: NewRGB(1, 2, 3)},
	})
	policy, err := NewDeadbandPolicy(1, 1, DeadbandConfig{
		MaxCellErrorIncrease: 1,
		SceneCutMSE:          100,
		MaxAge:               2,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := RGB24{
		Pix:    []byte{10, 20, 30, 10, 20, 30, 10, 20, 30, 10, 20, 30},
		Width:  2,
		Height: 2,
		Stride: 6,
	}
	frame, err := solver.SolveRGB24WithPolicy(input, SourceMeta{SourceSequence: 1}, policy)
	if err != nil {
		t.Fatal(err)
	}
	frame.Release()
	// Temporal state must remain in source color space. Otherwise a filter
	// change would contaminate deadband and scene-cut decisions.
	if got := policy.cells[0].Background(); got != NewRGB(10, 20, 30) {
		t.Fatalf("deadband stored transformed color %#06x", got.Packed())
	}
}

func BenchmarkDetailed177x50WithColorTransform(b *testing.B) {
	solver, err := NewSolver(Config{
		Columns:   177,
		Rows:      50,
		Mode:      ModeDetailed,
		Buffers:   1,
		Transform: fixedTransform{color: NewRGB(80, 120, 160)},
	})
	if err != nil {
		b.Fatal(err)
	}
	input := patternedRGB24(177, 50)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		frame, solveErr := solver.SolveRGB24(input, SourceMeta{SourceSequence: uint64(index)})
		if solveErr != nil {
			b.Fatal(solveErr)
		}
		frame.Release()
	}
}
