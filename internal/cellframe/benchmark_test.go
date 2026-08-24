package cellframe

import "testing"

func BenchmarkDetailed177x50(b *testing.B) { benchmarkDetailed(b, 177, 50) }

func BenchmarkDetailed240x67(b *testing.B) { benchmarkDetailed(b, 240, 67) }

func BenchmarkStatisticsDetailed240x67(b *testing.B) {
	const columns, rows = 240, 67
	solver, err := NewSolver(Config{Columns: columns, Rows: rows, Mode: ModeDetailed, Buffers: 1})
	if err != nil {
		b.Fatal(err)
	}
	input := StatisticsFrame{Columns: columns, Rows: rows, Quadrants: make([]SampleStats, columns*rows*4)}
	var state uint32 = 0x12345678
	for index := range input.Quadrants {
		for range 4 {
			state = state*1664525 + 1013904223
			input.Quadrants[index].AddRGB(byte(state>>24), byte(state>>16), byte(state>>8))
		}
	}
	warm, err := solver.SolveStatistics(input, SourceMeta{})
	if err != nil {
		b.Fatal(err)
	}
	warm.Release()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		frame, solveErr := solver.SolveStatistics(input, SourceMeta{SourceSequence: uint64(index)})
		if solveErr != nil {
			b.Fatal(solveErr)
		}
		frame.Release()
	}
}

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
