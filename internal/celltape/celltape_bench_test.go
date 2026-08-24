package celltape

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var benchmarkEncodedBytes int
var benchmarkReplaySequence uint64

func BenchmarkEncodeDelta(b *testing.B) {
	prev := make([]Cell, 1920)
	next := make([]Cell, 1920)
	for i := range prev {
		prev[i] = canonicalCell(uint8(i%7+1), uint32(i))
		next[i] = prev[i]
	}
	for i := 0; i < len(next); i += 20 {
		next[i] = canonicalCell(1, uint32(i+10000))
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(next) * PackedCellBytes))
	for i := 0; i < b.N; i++ {
		if _, err := encodeDelta(prev, next, 1); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReplayIteration isolates the caller-copy cost removed from export.
// Both paths still decode and validate the same 300x84 state; Borrowed differs
// only by lending Replay's current state to the callback.
func BenchmarkReplayIteration(b *testing.B) {
	path := filepath.Join(b.TempDir(), "replay.celltape")
	file, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	recorder, err := New(context.Background(), file, Config{QueueCapacity: 1, Compression: CompressionNone})
	if err != nil {
		_ = file.Close()
		b.Fatal(err)
	}
	if err = recorder.Submit(Input{
		GeometryEpoch: 1,
		ConfigEpoch:   1,
		Columns:       300,
		Rows:          84,
		Config:        []byte("benchmark"),
		Cells:         noisyCells(300*84, 0x12345678),
	}, 0); err == nil {
		err = recorder.Close()
	}
	if err != nil {
		b.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	for _, benchmark := range []struct {
		name     string
		borrowed bool
	}{
		{name: "Copied"},
		{name: "Borrowed", borrowed: true},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			replay, openErr := Open(path, OpenOptions{})
			if openErr != nil {
				b.Fatal(openErr)
			}
			defer replay.Close()
			b.ReportAllocs()
			b.SetBytes(info.Size())
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				replay.Rewind()
				var iterateErr error
				if benchmark.borrowed {
					iterateErr = replay.IterateBorrowed(context.Background(), func(frame Frame) error {
						benchmarkReplaySequence = frame.Sequence
						return nil
					})
				} else {
					iterateErr = replay.Iterate(context.Background(), func(frame Frame) error {
						benchmarkReplaySequence = frame.Sequence
						return nil
					})
				}
				if iterateErr != nil {
					b.Fatal(iterateErr)
				}
			}
		})
	}
}

func BenchmarkPrepareCellFrame177x50(b *testing.B) { benchmarkPrepareCellFrame(b, 177, 50) }

func BenchmarkPrepareCellFrame300x84(b *testing.B) { benchmarkPrepareCellFrame(b, 300, 84) }

func BenchmarkWorkerChanging300x84Raw(b *testing.B) {
	benchmarkWorkerChanging(b, CompressionNone)
}

func BenchmarkWorkerChanging300x84CompressionFast(b *testing.B) {
	benchmarkWorkerChanging(b, CompressionFast)
}

// BenchmarkRecorder30FPS300x84CompressionFast is deliberately paced like the
// live producer and writes a real crash-recoverable tape. queue-depth reports
// whether the eight-buffer worker starts falling behind at 30 accepted FPS.
func BenchmarkRecorder30FPS300x84CompressionFast(b *testing.B) {
	path := filepath.Join(b.TempDir(), "paced.celltape")
	file, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	recorder, err := New(context.Background(), file, Config{
		QueueCapacity:    8,
		KeyframeInterval: 120,
		DurabilityWindow: time.Second,
		Compression:      CompressionFast,
	})
	if err != nil {
		b.Fatal(err)
	}
	first := noisyCells(300*84, 0x12345678)
	second := noisyCells(300*84, 0x9abcdef0)
	input := Input{GeometryEpoch: 1, ConfigEpoch: 1, Columns: 300, Rows: 84, Cells: first}
	const framePeriod = time.Second / 30
	next := time.Now()
	maxDepth := 0
	b.ReportAllocs()
	b.SetBytes(300 * 84 * PackedCellBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if wait := time.Until(next); wait > 0 {
			time.Sleep(wait)
		}
		next = next.Add(framePeriod)
		if i&1 == 0 {
			input.Cells = first
		} else {
			input.Cells = second
		}
		input.SourceNanos = uint64(i) * uint64(framePeriod)
		if err = recorder.Submit(input, uint64(i)*uint64(framePeriod)); err != nil {
			b.Fatalf("frame %d: %v", i, err)
		}
		if depth := recorder.free.capacity() - recorder.free.available(); depth > maxDepth {
			maxDepth = depth
		}
	}
	b.StopTimer()
	if err = recorder.Close(); err != nil {
		b.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(maxDepth), "queue-depth")
	if b.N > 0 {
		b.ReportMetric(float64(info.Size()-FileHeaderBytes)/float64(b.N), "tape-B/frame")
	}
}

func benchmarkWorkerChanging(b *testing.B, compression Compression) {
	const cells = 300 * 84
	prev := noisyCells(cells, 0x12345678)
	next := noisyCells(cells, 0x9abcdef0)
	var keyScratch, runScratch, bitmapScratch []byte
	var compressor fastCompressor
	b.ReportAllocs()
	b.SetBytes(cells * PackedCellBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		input := Input{Columns: 300, Rows: 84, Cells: next}
		var err error
		keyScratch, err = encodeKeyframeInto(keyScratch, input)
		if err != nil {
			b.Fatal(err)
		}
		var delta []byte
		delta, runScratch, bitmapScratch, err = encodeDeltaInto(runScratch, bitmapScratch, prev, next, 1)
		if err != nil {
			b.Fatal(err)
		}
		raw := keyScratch
		if len(delta) < len(raw) {
			raw = delta
		}
		stored, _ := compressor.compress(raw, compression)
		benchmarkEncodedBytes = len(stored)
		prev, next = next, prev
	}
}

func noisyCells(count int, seed uint32) []Cell {
	cells := make([]Cell, count)
	state := seed
	for i := range cells {
		state = state*1664525 + 1013904223
		fg := RGB(state & 0xffffff)
		state = state*1664525 + 1013904223
		bg := RGB(state & 0xffffff)
		cells[i] = Cell{Mask: uint8(i%7 + 1), FG: fg, BG: bg}
	}
	return cells
}

func benchmarkPrepareCellFrame(b *testing.B, columns, rows int) {
	frame := solvedCellFrame(b, columns, rows, time.Nanosecond)
	recorder, err := New(context.Background(), &memorySink{}, Config{QueueCapacity: 1})
	if err != nil {
		frame.Release()
		b.Fatal(err)
	}
	b.Cleanup(func() {
		frame.Release()
		_ = recorder.Close()
	})
	warm, err := recorder.PrepareCellFrame(frame, 1, []byte("benchmark"), 0)
	if err != nil {
		b.Fatal(err)
	}
	warm.Abort()
	b.ReportAllocs()
	b.SetBytes(int64(frame.Len() * PackedCellBytes))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prepared, prepareErr := recorder.PrepareCellFrame(frame, 1, []byte("benchmark"), 0)
		if prepareErr != nil {
			b.Fatal(prepareErr)
		}
		prepared.Abort()
	}
}
