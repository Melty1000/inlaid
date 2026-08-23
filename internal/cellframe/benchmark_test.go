package cellframe

import "testing"

func BenchmarkDetailed177x50(b *testing.B) { benchmarkDetailed(b, 177, 50) }

func BenchmarkDetailed240x67(b *testing.B) { benchmarkDetailed(b, 240, 67) }

func benchmarkDetailed(b *testing.B, columns, rows int) {
	solver, err := NewSolver(Config{Columns: columns, Rows: rows, Mode: ModeDetailed, Buffers: 1})
	if err != nil {
		b.Fatal(err)
	}
	input := patternedRGB24(columns, rows)
	warm, err := solver.SolveRGB24(input, SourceMeta{})
	if err != nil {
		b.Fatal(err)
	}
	warm.Release()
	b.ReportAllocs()
	b.SetBytes(int64(len(input.Pix)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		frame, solveErr := solver.SolveRGB24(input, SourceMeta{SourceSequence: uint64(i)})
		if solveErr != nil {
			b.Fatal(solveErr)
		}
		frame.Release()
	}
}
