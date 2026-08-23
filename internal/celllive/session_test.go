package celllive

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/cellreduce"
	"github.com/Melty1000/inlaid/internal/mfcapture"
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

	soft := detailed
	soft.Mode = cellframe.ModeSoft
	// The session installs the new view before the next source frame arrives.
	// update must compare against the active solver, not that pending view.
	state.view = soft
	if err := state.update(soft, 640, 480); err != nil {
		t.Fatal(err)
	}
	if state.solver.Mode() != cellframe.ModeSoft || state.geometryEpoch != epoch+1 {
		t.Fatalf("mode=%v epoch=%d, want soft epoch %d", state.solver.Mode(), state.geometryEpoch, epoch+1)
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

func TestNormalizeViewBoundsForgedResizeBeforeGeometry(t *testing.T) {
	view := normalizeView(ViewConfig{MaxColumns: math.MaxInt, MaxRows: 1})
	if view.MaxColumns != cellreduce.MaxCells {
		t.Fatalf("max columns = %d, want %d", view.MaxColumns, cellreduce.MaxCells)
	}
	geometry := cellreduce.FitGeometry(1920, 1080, view.MaxColumns, view.MaxRows, view.Fill)
	if geometry.Columns*geometry.Rows > cellreduce.MaxCells {
		t.Fatalf("geometry exceeds bound: %+v", geometry)
	}
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
	if !mfcapture.IsTemporary(got) {
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
		if got := <-session.Errors; !mfcapture.IsTemporary(got) {
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

func (planarTestDecoder) Decode(_ context.Context, packet mfcapture.Packet) (*mfcapture.Frame, error) {
	y := make([]byte, 16)
	cb := make([]byte, 16)
	cr := make([]byte, 16)
	for index := range y {
		y[index], cb[index], cr[index] = 128, 128, 128
	}
	return &mfcapture.Frame{
		Y:                    mfcapture.Plane{Pix: y, Width: 4, Height: 4, Stride: 4},
		Cb:                   mfcapture.Plane{Pix: cb, Width: 4, Height: 4, Stride: 4},
		Cr:                   mfcapture.Plane{Pix: cr, Width: 4, Height: 4, Stride: 4},
		ReaderTimestamp100ns: packet.ReaderTimestamp100ns,
		SampleTimestamp100ns: packet.SampleTimestamp100ns,
	}, nil
}

func (planarTestDecoder) Close() error { return nil }

func TestMediaPipelineReportsTemporaryErrorAndContinuesToFrame(t *testing.T) {
	source, err := mfcapture.NewSyntheticSource(2)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := mfcapture.StartPipeline(context.Background(), mfcapture.Config{
		Width: 4, Height: 4, FPS: 30, Lowres: 1,
		QueueDepth: 1, PacketQueueDepth: 1, MaxPacketBytes: 1024,
		MaxFrameBytes: 1024, MaxPoolBytes: 1 << 20, MaxConsecutiveErrors: 3,
		CloseTimeout: 100 * time.Millisecond,
	}, source, planarTestDecoder{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := newSession(cancel)
	go session.runMediaFoundation(ctx, capture, normalizeView(ViewConfig{
		MaxColumns: 2, MaxRows: 2, Fill: true, TargetFPS: 30,
	}))
	defer session.Close()

	if err := source.Fail(temporaryTestError{}); err != nil {
		t.Fatal(err)
	}
	if err := source.Push(mfcapture.Packet{Data: []byte{1}}); err != nil {
		t.Fatal(err)
	}

	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	gotTemporary, gotFrame := false, false
	for !gotTemporary || !gotFrame {
		select {
		case got := <-session.Errors:
			if mfcapture.IsTemporary(got) {
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
