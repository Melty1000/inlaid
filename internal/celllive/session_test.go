package celllive

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Melty1000/inlaid/internal/capture"
	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/cellreduce"
)

func TestSyntheticProducesCanonicalANSIAndCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := StartSynthetic(ctx, 1920, 1080, 30, ViewConfig{
		MaxColumns: 80, MaxRows: 24, Fill: true, Mode: cellframe.Detailed, TargetFPS: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-session.Results:
		if result.Frame == nil || !result.Frame.Valid() || result.Columns != 80 || result.Rows != 24 || !strings.Contains(result.ANSI, "\x1b[38;2;") {
			t.Fatalf("result = %+v", result)
		}
		result.Frame.Release()
	case err := <-session.Errors:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	session.Close()
	session.Close()
}

func TestPausedSyntheticDoesNotPublishUntilResumed(t *testing.T) {
	session, err := StartSynthetic(context.Background(), 640, 480, 30, ViewConfig{
		MaxColumns: 20, MaxRows: 10, Fill: true, Paused: true, TargetFPS: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	select {
	case result := <-session.Results:
		result.Frame.Release()
		t.Fatal("paused session published a frame")
	case <-time.After(80 * time.Millisecond):
	}
	session.Update(ViewConfig{Version: 2, MaxColumns: 20, MaxRows: 10, Fill: true, TargetFPS: 30})
	select {
	case result := <-session.Results:
		if result.Version != 2 {
			t.Fatalf("version = %d", result.Version)
		}
		result.Frame.Release()
	case <-time.After(time.Second):
		t.Fatal("resume did not publish")
	}
}

func TestCadenceToleranceKeepsThirtyFPSFractionalTimestamps(t *testing.T) {
	interval := time.Second / 30
	if !cadenceDue(interval, interval*2, 30) {
		t.Fatal("exact 30 FPS timestamp was rejected")
	}
}

func TestCadenceAnchorsThirtyToTwentyFourWithoutCollapsingToFifteen(t *testing.T) {
	var gate cadenceGate
	accepted := 0
	for frame := 0; frame < 30; frame++ {
		pts := time.Duration(frame) * time.Second / 30
		if gate.due(pts, 24) {
			accepted++
		}
	}
	if accepted != 24 {
		t.Fatalf("accepted %d of 30 frames, want 24", accepted)
	}
}

func TestCadenceRejectsDuplicateTimestampAndRecoversFromClockReset(t *testing.T) {
	var gate cadenceGate
	if !gate.due(time.Second, 30) {
		t.Fatal("first timestamp was rejected")
	}
	if gate.due(time.Second, 30) {
		t.Fatal("duplicate timestamp was accepted")
	}
	if !gate.due(0, 30) {
		t.Fatal("clock reset did not re-anchor cadence")
	}
}

func TestPipelineStateRebuildsSolverWhenOnlyModeChanges(t *testing.T) {
	state := pipelineState{}
	detailed := normalizeView(ViewConfig{MaxColumns: 20, MaxRows: 10, Fill: true, Mode: cellframe.ModeDetailed})
	if err := state.update(detailed, 640, 480); err != nil {
		t.Fatal(err)
	}
	epoch := state.geometryEpoch
	reducer := state.reducer

	soft := detailed
	soft.Mode = cellframe.ModeSoft
	// The session installs the new view before the next source frame arrives.
	// update must compare against the active solver, not that pending view.
	state.view = soft
	if err := state.update(soft, 640, 480); err != nil {
		t.Fatal(err)
	}
	if state.solver.Mode() != cellframe.ModeSoft || state.geometryEpoch != epoch {
		t.Fatalf("mode=%v epoch=%d, want soft at unchanged geometry epoch %d", state.solver.Mode(), state.geometryEpoch, epoch)
	}
	if state.reducer != reducer {
		t.Fatal("mode-only update rebuilt the geometry-bound reducer")
	}
	resized := soft
	resized.MaxColumns--
	if err := state.update(resized, 640, 480); err != nil {
		t.Fatal(err)
	}
	if state.geometryEpoch != epoch+1 {
		t.Fatalf("resized epoch=%d, want %d", state.geometryEpoch, epoch+1)
	}
}

func BenchmarkPipelineStateModeOnlyUpdate240x67(b *testing.B) {
	state := pipelineState{}
	view := normalizeView(ViewConfig{MaxColumns: 240, MaxRows: 67, Fill: true, Mode: cellframe.ModeDetailed})
	if err := state.update(view, 480, 270); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if index&1 == 0 {
			view.Mode = cellframe.ModeSoft
		} else {
			view.Mode = cellframe.ModeDetailed
		}
		if err := state.update(view, 480, 270); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSyntheticStatisticsReuseBoundedBuffer(t *testing.T) {
	state := pipelineState{}
	view := normalizeView(ViewConfig{MaxColumns: 80, MaxRows: 24, Fill: true})
	if err := state.update(view, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	first := state.syntheticStatistics(1)
	firstAddress := &first.Quadrants[0]
	second := state.syntheticStatistics(2)
	if firstAddress != &second.Quadrants[0] {
		t.Fatal("steady synthetic frame allocated a replacement statistics buffer")
	}
	if second.Quadrants[0].Count != 1 {
		t.Fatalf("statistics were accumulated across frames: count=%d", second.Quadrants[0].Count)
	}
	if allocations := testing.AllocsPerRun(100, func() { state.syntheticStatistics(3) }); allocations != 0 {
		t.Fatalf("steady synthetic statistics allocations = %v, want 0", allocations)
	}
}

func TestPipelineCoreThirtyFPSBoundedHeapSoak(t *testing.T) {
	secondsText := os.Getenv("INLAID_PIPELINE_SOAK_SECONDS")
	if secondsText == "" {
		t.Skip("set INLAID_PIPELINE_SOAK_SECONDS to run the deterministic 30 FPS heap soak")
	}
	seconds, err := strconv.Atoi(secondsText)
	if err != nil || seconds <= 0 || seconds > 24*60*60 {
		t.Fatalf("INLAID_PIPELINE_SOAK_SECONDS=%q must be an integer from 1 through 86400", secondsText)
	}

	state := pipelineState{}
	view := normalizeView(ViewConfig{MaxColumns: 240, MaxRows: 67, Fill: true, Mode: cellframe.ModeDetailed})
	if err := state.update(view, 480, 270); err != nil {
		t.Fatal(err)
	}
	source := cellreduce.YCbCr{
		Y: make([]byte, 480*270), Cb: make([]byte, 240*270), Cr: make([]byte, 240*270),
		Width: 480, Height: 270, YStride: 480,
		ChromaWidth: 240, ChromaHeight: 270, CbStride: 240, CrStride: 240,
	}
	process := func(sequence uint64) {
		source.Y[0] = byte(sequence)
		statistics, reduceErr := state.reducer.ReduceYCbCr(source)
		if reduceErr != nil {
			t.Fatal(reduceErr)
		}
		frame, solveErr := state.solver.SolveStatistics(statistics, cellframe.SourceMeta{
			SourceSequence: sequence,
			PTS:            time.Duration(sequence) * time.Second / 30,
		})
		if solveErr != nil {
			t.Fatal(solveErr)
		}
		frame.Release()
	}
	process(0) // Populate every reusable plan and pool before measuring.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	frames := seconds * 30
	for index := 1; index <= frames; index++ {
		process(uint64(index))
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(&state)
	runtime.KeepAlive(source)

	const retainedHeapTolerance = 2 << 20
	if after.HeapAlloc > before.HeapAlloc+retainedHeapTolerance {
		t.Fatalf("retained heap grew by %d bytes after %d frames", after.HeapAlloc-before.HeapAlloc, frames)
	}
	t.Logf("%d logical frames (%s at 30 FPS): retained heap delta=%d bytes, live-object delta=%d",
		frames, time.Duration(seconds)*time.Second, int64(after.HeapAlloc)-int64(before.HeapAlloc),
		int64(after.Mallocs-after.Frees)-int64(before.Mallocs-before.Frees))
}

type temporaryTestError struct{}

func (temporaryTestError) Error() string   { return "one sample was dropped" }
func (temporaryTestError) Temporary() bool { return true }

func TestPublishErrorPreservesTemporaryClassificationAndIsBounded(t *testing.T) {
	session := newSession(func() {})
	for range 10 {
		session.publishError(temporaryTestError{})
	}
	got := <-session.Errors
	if !capture.IsTemporary(got) {
		t.Fatalf("temporary classification was lost: %v", got)
	}
	session.finish()
}

func TestPublishErrorPrioritizesTerminalFailureOverQueuedTransientNoise(t *testing.T) {
	session := newSession(func() {})
	session.publishError(temporaryTestError{})
	session.publishError(temporaryTestError{})
	session.publishError(context.Canceled)

	foundTerminal := false
	for range 2 {
		if got := <-session.Errors; !capture.IsTemporary(got) {
			foundTerminal = true
		}
	}
	if !foundTerminal {
		t.Fatal("terminal failure was hidden by transient errors")
	}
	session.finish()
}

func TestPublishReleasesCanonicalFrameEvictedByLatestQueue(t *testing.T) {
	solver, err := cellframe.NewSolver(cellframe.Config{Columns: 1, Rows: 1, Buffers: 2})
	if err != nil {
		t.Fatal(err)
	}
	statistics := cellframe.StatisticsFrame{Columns: 1, Rows: 1, Quadrants: []cellframe.SampleStats{
		{Count: 1}, {Count: 1}, {Count: 1}, {Count: 1},
	}}
	first, err := solver.SolveStatistics(statistics, cellframe.SourceMeta{SourceSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := solver.SolveStatistics(statistics, cellframe.SourceMeta{SourceSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	session := newSession(func() {})
	stats := rateStats{}
	session.publish(Result{Frame: first}, &stats)
	session.publish(Result{Frame: second}, &stats)
	if first.Valid() {
		t.Fatal("evicted canonical frame lease was not released")
	}
	if stats.presentationDropped != 1 {
		t.Fatalf("presentation drops = %d, want 1", stats.presentationDropped)
	}
	result := <-session.Results
	result.Frame.Release()
	session.finish()
}

type planarTestDecoder struct{}

func (planarTestDecoder) Decode(_ context.Context, packet capture.Packet) (*capture.Frame, error) {
	y := make([]byte, 16)
	cb := make([]byte, 16)
	cr := make([]byte, 16)
	for index := range y {
		y[index], cb[index], cr[index] = 128, 128, 128
	}
	return &capture.Frame{
		Layout: capture.PixelLayoutPlanarYCbCr,
		Range:  capture.ColorRangeFull,
		Matrix: capture.ColorMatrixBT601,
		Y:      capture.Plane{Pix: y, Width: 4, Height: 4, Stride: 4},
		Cb:     capture.Plane{Pix: cb, Width: 4, Height: 4, Stride: 4},
		Cr:     capture.Plane{Pix: cr, Width: 4, Height: 4, Stride: 4},
		PTS:    packet.PTS,
	}, nil
}

func (planarTestDecoder) Close() error { return nil }

type shutdownBlockingDecoder struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

func TestReduceCameraFrameUsesNV12ColorMetadata(t *testing.T) {
	reducer, err := cellreduce.New(cellreduce.Geometry{Columns: 1, Rows: 1})
	if err != nil {
		t.Fatal(err)
	}
	statistics, err := reduceCameraFrame(reducer, &capture.Frame{
		Layout: capture.PixelLayoutNV12,
		Range:  capture.ColorRangeVideo,
		Matrix: capture.ColorMatrixBT601,
		Y:      capture.Plane{Pix: []byte{81, 81, 81, 81}, Width: 2, Height: 2, Stride: 2},
		UV:     capture.Plane{Pix: []byte{90, 240}, Width: 1, Height: 1, Stride: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, sample := range statistics.Quadrants {
		if sample.Count != 1 || sample.SumR < 253 || sample.SumG > 1 || sample.SumB > 1 {
			t.Fatalf("quadrant %d = %+v, want red", index, sample)
		}
	}
}

func TestReduceCameraFrameRejectsMissingColorMetadata(t *testing.T) {
	reducer, err := cellreduce.New(cellreduce.Geometry{Columns: 1, Rows: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reduceCameraFrame(reducer, &capture.Frame{
		Layout: capture.PixelLayoutNV12,
		Y:      capture.Plane{Pix: make([]byte, 4), Width: 2, Height: 2, Stride: 2},
		UV:     capture.Plane{Pix: make([]byte, 2), Width: 1, Height: 1, Stride: 2},
	})
	if err == nil {
		t.Fatal("missing color metadata was accepted")
	}
}

func (d *shutdownBlockingDecoder) Decode(context.Context, capture.Packet) (*capture.Frame, error) {
	d.startedOnce.Do(func() { close(d.started) })
	<-d.release
	return nil, nil
}

func (d *shutdownBlockingDecoder) Close() error {
	<-d.release
	return nil
}

func TestMediaSessionClosePropagatesNativeShutdownTimeout(t *testing.T) {
	source, err := capture.NewSyntheticSource(1)
	if err != nil {
		t.Fatal(err)
	}
	decoder := &shutdownBlockingDecoder{started: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(decoder.release) }) })
	cfg := capture.DefaultConfig()
	cfg.CloseTimeout = 100 * time.Millisecond
	nativeSession, err := capture.StartPipeline(context.Background(), cfg, source, decoder)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := newSession(cancel)
	go session.runCamera(ctx, nativeSession, normalizeView(ViewConfig{
		MaxColumns: 2, MaxRows: 2, Fill: true, TargetFPS: 30,
	}))
	if err := source.Push(capture.Packet{Data: []byte{1}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-decoder.started:
	case <-time.After(time.Second):
		t.Fatal("decoder did not accept the packet")
	}

	started := time.Now()
	err = session.Close()
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Close took %s after native shutdown stalled", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "shutdown exceeded") {
		t.Fatalf("Close error = %v, want propagated native shutdown timeout", err)
	}
	releaseOnce.Do(func() { close(decoder.release) })
}

func TestMediaPipelineReportsTemporaryErrorAndContinuesToFrame(t *testing.T) {
	source, err := capture.NewSyntheticSource(2)
	if err != nil {
		t.Fatal(err)
	}
	nativeSession, err := capture.StartPipeline(context.Background(), capture.Config{
		Width: 4, Height: 4, FPS: 30, Downsample: 2,
		QueueDepth: 1, PacketQueueDepth: 1, MaxPacketBytes: 1024,
		MaxFrameBytes: 1024, MaxPoolBytes: 1 << 20, MaxConsecutiveErrors: 3,
		CloseTimeout: 100 * time.Millisecond,
	}, source, planarTestDecoder{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := newSession(cancel)
	go session.runCamera(ctx, nativeSession, normalizeView(ViewConfig{
		MaxColumns: 2, MaxRows: 2, Fill: true, TargetFPS: 30,
	}))
	defer session.Close()

	if err := source.Fail(temporaryTestError{}); err != nil {
		t.Fatal(err)
	}
	if err := source.Push(capture.Packet{Data: []byte{1}}); err != nil {
		t.Fatal(err)
	}

	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	gotTemporary, gotFrame := false, false
	for !gotTemporary || !gotFrame {
		select {
		case got := <-session.Errors:
			if capture.IsTemporary(got) {
				gotTemporary = true
			}
		case result := <-session.Results:
			if result.Frame != nil {
				gotFrame = true
				result.Frame.Release()
			}
		case <-timeout.C:
			t.Fatalf("temporary=%v frame=%v", gotTemporary, gotFrame)
		}
	}
}
