package celltape

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

func TestPrepareCellFrameCopiesOnceAndRoundTrips(t *testing.T) {
	frame := solvedCellFrame(t, 7, 3, 10*time.Nanosecond)
	want := tapeCells(t, frame)
	config := []byte("original")
	path := filepath.Join(t.TempDir(), "direct.celltape")
	recorder, err := Create(context.Background(), path, Config{QueueCapacity: 1})
	if err != nil {
		frame.Release()
		t.Fatal(err)
	}
	prepared, err := recorder.PrepareCellFrame(frame, 4, config, BoundaryGap)
	if err != nil {
		frame.Release()
		recorder.Close()
		t.Fatal(err)
	}
	config[0] = 'X'
	frame.Release() // the tape owns its one canonical copy now
	if err = prepared.Commit(20); err != nil {
		recorder.Close()
		t.Fatal(err)
	}
	staging := recorder.StagingPath()
	if err = recorder.Close(); err != nil {
		t.Fatal(err)
	}

	replay, err := Open(staging, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	got, err := replay.Next()
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Config) != "original" || got.ConfigEpoch != 4 || got.SourceNanos != 10 || got.HostNanos != 20 || got.Boundary != BoundaryGap {
		t.Fatalf("metadata = %+v", got)
	}
	if len(got.Cells) != len(want) {
		t.Fatalf("cell count = %d, want %d", len(got.Cells), len(want))
	}
	for i := range want {
		if got.Cells[i] != want[i] {
			t.Fatalf("cell %d = %#v, want %#v", i, got.Cells[i], want[i])
		}
	}
}

func TestPrepareCellFrameAbortReusesBufferAndRejectsStaleToken(t *testing.T) {
	recorder, err := New(context.Background(), &memorySink{}, Config{QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	frame := solvedCellFrame(t, 12, 4, time.Nanosecond)
	defer frame.Release()

	first, err := recorder.PrepareCellFrame(frame, 1, []byte("config"), 0)
	if err != nil {
		t.Fatal(err)
	}
	stale := first
	backing := &recorder.pool[0].cells[0]
	first.Abort()
	first.Abort()

	second, err := recorder.PrepareCellFrame(frame, 1, []byte("config"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := &recorder.pool[0].cells[0]; got != backing {
		t.Fatal("aborted producer buffer was not reused")
	}
	if err = stale.Commit(1); !errors.Is(err, ErrPreparedDone) {
		t.Fatalf("stale token commit = %v, want %v", err, ErrPreparedDone)
	}
	if err = second.Commit(1); err != nil {
		t.Fatal(err)
	}
	if err = recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if len(recorder.free) != cap(recorder.free) {
		t.Fatalf("recycled buffers = %d, want %d", len(recorder.free), cap(recorder.free))
	}
}

func TestPrepareCellFrameSaturationAndCloseRecycle(t *testing.T) {
	frame := solvedCellFrame(t, 2, 2, time.Nanosecond)
	defer frame.Release()
	recorder, err := New(context.Background(), &memorySink{}, Config{QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := recorder.PrepareCellFrame(frame, 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recorder.PrepareCellFrame(frame, 1, nil, 0); !errors.Is(err, ErrQueueSaturated) {
		t.Fatalf("saturation = %v, want %v", err, ErrQueueSaturated)
	}
	prepared.Abort()
	if err = recorder.Close(); !errors.Is(err, ErrQueueSaturated) {
		t.Fatalf("close = %v, want saturation", err)
	}
	if len(recorder.free) != 1 {
		t.Fatalf("free buffers after abort/close = %d, want 1", len(recorder.free))
	}

	closed, err := New(context.Background(), &memorySink{}, Config{QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := closed.PrepareCellFrame(frame, 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err = pending.Commit(1); !errors.Is(err, ErrClosed) {
		t.Fatalf("commit after close = %v, want %v", err, ErrClosed)
	}
	if len(closed.free) != 1 {
		t.Fatalf("close-path recycled buffers = %d, want 1", len(closed.free))
	}
}

func TestPrepareCellFrameWorkerFailureDrainsEveryBuffer(t *testing.T) {
	sink := &blockingSink{started: make(chan struct{}), release: make(chan struct{})}
	recorder, err := New(context.Background(), sink, Config{QueueCapacity: 3})
	if err != nil {
		t.Fatal(err)
	}
	frame := solvedCellFrame(t, 3, 2, time.Nanosecond)
	defer frame.Release()
	sink.block = true
	if err = recorder.SubmitCellFrame(frame, 1, nil, 0, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not reach gated sink")
	}
	if err = recorder.SubmitCellFrame(frame, 1, nil, 0, 2); err != nil {
		t.Fatal(err)
	}
	if err = recorder.SubmitCellFrame(frame, 1, nil, 0, 3); err != nil {
		t.Fatal(err)
	}
	sink.memorySink.mu.Lock()
	sink.memorySink.closed = true
	sink.memorySink.mu.Unlock()
	close(sink.release)
	select {
	case <-recorder.Done():
	case <-time.After(time.Second):
		t.Fatal("worker failure did not stop recorder")
	}
	if len(recorder.free) != cap(recorder.free) {
		t.Fatalf("failure/drain recycled %d buffers, want %d", len(recorder.free), cap(recorder.free))
	}
	if err = recorder.Close(); err == nil {
		t.Fatal("sink failure was not returned by Close")
	}
}

func TestPrepareCellFrameSteadyStateDoesNotAllocate(t *testing.T) {
	frame := solvedCellFrame(t, 24, 8, time.Nanosecond)
	defer frame.Release()
	recorder, err := New(context.Background(), &memorySink{}, Config{QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	warm, err := recorder.PrepareCellFrame(frame, 1, []byte("x"), 0)
	if err != nil {
		t.Fatal(err)
	}
	warm.Abort()
	allocs := testing.AllocsPerRun(1000, func() {
		prepared, prepareErr := recorder.PrepareCellFrame(frame, 1, []byte("x"), 0)
		if prepareErr != nil {
			panic(prepareErr)
		}
		prepared.Abort()
	})
	if allocs != 0 {
		t.Fatalf("steady-state allocations = %.2f, want 0", allocs)
	}
}

func solvedCellFrame(tb testing.TB, columns, rows int, pts time.Duration) *cellframe.CellFrame {
	tb.Helper()
	solver, err := cellframe.NewSolver(cellframe.Config{Columns: columns, Rows: rows, Mode: cellframe.ModeDetailed, Buffers: 1})
	if err != nil {
		tb.Fatal(err)
	}
	width, height := columns*2, rows*2
	pixels := make([]byte, width*height*3)
	var state uint32 = 0x13579bdf
	for i := range pixels {
		state = state*1664525 + 1013904223
		pixels[i] = byte(state >> 24)
	}
	frame, err := solver.SolveRGB24(cellframe.RGB24{Pix: pixels, Width: width, Height: height, Stride: width * 3}, cellframe.SourceMeta{GeometryEpoch: 1, SourceSequence: 1, PTS: pts})
	if err != nil {
		tb.Fatal(err)
	}
	return frame
}

func tapeCells(tb testing.TB, frame *cellframe.CellFrame) []Cell {
	tb.Helper()
	cells := make([]Cell, frame.Len())
	for i := range cells {
		cell, ok := frame.Cell(i)
		if !ok {
			tb.Fatalf("cell %d unavailable", i)
		}
		packed := cell.Packed()
		cells[i] = Cell{Mask: uint8(packed & 0x0f), FG: RGB((packed >> 4) & 0xffffff), BG: RGB((packed >> 28) & 0xffffff)}
	}
	return cells
}
