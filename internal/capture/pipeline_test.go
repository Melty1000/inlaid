package capture

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var errFakeDamagedJPEG = errors.New("damaged jpeg")

type fakeDecoder struct {
	releases atomic.Int32
	closes   atomic.Int32
	block    <-chan struct{}
	decoded  chan struct{}
	failures atomic.Int32
}

type closeWarningSource struct {
	*SyntheticSource
	warning error
}

func (s *closeWarningSource) Close() error {
	_ = s.SyntheticSource.Close()
	return s.warning
}

func (d *fakeDecoder) Decode(ctx context.Context, packet Packet) (*Frame, error) {
	if d.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.block:
		}
	}
	if d.failures.Load() > 0 {
		d.failures.Add(-1)
		return nil, errFakeDamagedJPEG
	}
	if d.decoded != nil {
		close(d.decoded)
	}
	return &Frame{PTS: packet.PTS, release: func() { d.releases.Add(1) }}, nil
}

func (d *fakeDecoder) Close() error { d.closes.Add(1); return nil }

func TestCompletedSourceCloseWarningIsSafeToRetry(t *testing.T) {
	synthetic, err := NewSyntheticSource(1)
	if err != nil {
		t.Fatal(err)
	}
	warning := errors.New("flush callback was not delivered")
	source := &closeWarningSource{SyntheticSource: synthetic, warning: warning}
	session, err := StartPipeline(context.Background(), DefaultConfig(), source, &fakeDecoder{})
	if err != nil {
		t.Fatal(err)
	}
	err = session.Close()
	if !errors.Is(err, warning) {
		t.Fatalf("Close error = %v, want completed-source warning", err)
	}
	if errors.Is(err, ErrShutdownUncertain) {
		t.Fatalf("Close error = %v, completed source was mislabeled uncertain", err)
	}
}

func TestCloseDeadlineDrainsCompletedComponents(t *testing.T) {
	decoderWarning := errors.New("decoder close warning")
	sourceWarning := errors.New("source close warning")
	results := make(chan closeResult, 2)
	results <- closeResult{name: "decoder", err: decoderWarning}
	results <- closeResult{name: "source", err: sourceWarning}

	err := resolveCloseDeadline(results, 2, nil, 100*time.Millisecond)
	if !errors.Is(err, decoderWarning) || !errors.Is(err, sourceWarning) {
		t.Fatalf("close error = %v, want both completed warnings", err)
	}
	if errors.Is(err, ErrShutdownUncertain) {
		t.Fatalf("close error = %v, completed components were mislabeled uncertain", err)
	}
}

type stuckDecoder struct {
	started       chan struct{}
	release       chan struct{}
	frameReleases atomic.Int32
}

func (d *stuckDecoder) Decode(context.Context, Packet) (*Frame, error) {
	close(d.started)
	<-d.release
	return &Frame{release: func() { d.frameReleases.Add(1) }}, nil
}

func (d *stuckDecoder) Close() error {
	<-d.release
	return nil
}

func TestLatestQueueDropsAndReleasesOldest(t *testing.T) {
	source, err := NewSyntheticSource(4)
	if err != nil {
		t.Fatal(err)
	}
	decoder := &fakeDecoder{}
	cfg := DefaultConfig()
	cfg.QueueDepth = 1
	session, err := StartPipeline(context.Background(), cfg, source, decoder)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := source.Push(Packet{Data: []byte{1}, PTS: time.Duration(i) * 100 * time.Nanosecond}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for session.Stats().Decoded < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := session.Stats(); got.Decoded != 3 || got.DroppedFrames != 2 {
		t.Fatalf("stats = %+v", got)
	}
	select {
	case frame := <-session.Frames:
		if frame.PTS != 300*time.Nanosecond {
			t.Fatalf("latest timestamp = %s", frame.PTS)
		}
		frame.Release()
	case <-time.After(time.Second):
		t.Fatal("no frame")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if got := decoder.releases.Load(); got != 3 {
		t.Fatalf("released = %d, want 3", got)
	}
}

func TestPipelineReturnsPooledPacketAfterSynchronousDecode(t *testing.T) {
	pool := newPacketPool(8 << 10)
	packet, err := pool.acquire(100)
	if err != nil {
		t.Fatal(err)
	}
	owner := packet.owner
	source, err := NewSyntheticSource(1)
	if err != nil {
		t.Fatal(err)
	}
	session, err := StartPipeline(context.Background(), DefaultConfig(), source, &fakeDecoder{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := source.Push(packet); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-session.Frames:
		frame.Release()
	case <-time.After(time.Second):
		t.Fatal("pooled packet was not decoded")
	}
	if token := owner.token.Load(); token != 0 {
		t.Fatalf("pipeline retained pooled packet token %d after Decode returned", token)
	}
}

func TestDecoderReleasesResultWhenCoordinatorStopsBeforeHandoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan Packet)
	results := make(chan decodeResult)
	decoder := &fakeDecoder{decoded: make(chan struct{})}
	go runDecoder(ctx, decoder, requests, results)

	requests <- Packet{Data: []byte{1}}
	<-decoder.decoded
	cancel()

	deadline := time.Now().Add(time.Second)
	for decoder.releases.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := decoder.releases.Load(); got != 1 {
		t.Fatalf("abandoned decode result releases = %d, want 1", got)
	}
}

func TestCloseIsIdempotentAndUnblocksDecode(t *testing.T) {
	source, _ := NewSyntheticSource(1)
	block := make(chan struct{})
	decoder := &fakeDecoder{block: block}
	session, err := StartPipeline(context.Background(), DefaultConfig(), source, decoder)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Push(Packet{Data: []byte{1}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- session.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close was stuck")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if got := decoder.closes.Load(); got != 1 {
		t.Fatalf("decoder closes = %d, want 1", got)
	}
}

func TestDirectSessionKeepsLatestFrameAndReleasesEveryLease(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DeviceID = "camera"
	cfg.QueueDepth = 1
	mode := Mode{Width: 1920, Height: 1080, FPSNumerator: 30, FPSDenominator: 1, Format: "NV12"}
	ready := make(chan struct{})
	var releases atomic.Int32
	session, err := startDirect(context.Background(), cfg, mode, func(ctx context.Context, session *Session) error {
		for range 3 {
			session.acceptFrame(&Frame{release: func() { releases.Add(1) }})
		}
		close(ready)
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-ready
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if got := releases.Load(); got != 3 {
		t.Fatalf("released frames = %d, want 3", got)
	}
	if stats := session.Stats(); stats.Decoded != 3 || stats.DroppedFrames != 2 {
		t.Fatalf("stats = %+v, want decoded=3 dropped_frames=2", stats)
	}
}

func TestDirectCloseIsBoundedWhenNativeRunnerStalls(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DeviceID = "camera"
	cfg.CloseTimeout = 100 * time.Millisecond
	mode := Mode{Width: 1920, Height: 1080, FPSNumerator: 30, FPSDenominator: 1, Format: "NV12"}
	started := make(chan struct{})
	release := make(chan struct{})
	session, err := startDirect(context.Background(), cfg, mode, func(context.Context, *Session) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	closeStarted := time.Now()
	err = session.Close()
	if elapsed := time.Since(closeStarted); elapsed > 500*time.Millisecond {
		t.Fatalf("direct Close took %s after native stall", elapsed)
	}
	if !errors.Is(err, ErrShutdownUncertain) {
		t.Fatalf("direct Close error = %v, want uncertain ownership classification", err)
	}

	close(release)
	if err = session.Close(); !errors.Is(err, ErrShutdownUncertain) {
		t.Fatalf("second Close error = %v, want remembered uncertain shutdown", err)
	}
}

func TestTimedOutDirectCloseDrainsFramesAfterNativeRunnerFinishes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DeviceID = "camera"
	cfg.QueueDepth = 1
	cfg.CloseTimeout = 100 * time.Millisecond
	mode := Mode{Width: 1920, Height: 1080, FPSNumerator: 30, FPSDenominator: 1, Format: "NV12"}
	started := make(chan struct{})
	releaseRunner := make(chan struct{})
	var releases atomic.Int32
	session, err := startDirect(context.Background(), cfg, mode, func(_ context.Context, session *Session) error {
		session.acceptFrame(&Frame{release: func() { releases.Add(1) }})
		close(started)
		<-releaseRunner
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err = session.Close(); !errors.Is(err, ErrShutdownUncertain) {
		t.Fatalf("Close error = %v, want uncertain ownership", err)
	}
	if releases.Load() != 0 {
		t.Fatal("queued frame was released before native ownership ended")
	}

	close(releaseRunner)
	deadline := time.Now().Add(time.Second)
	for releases.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("late queued frame releases = %d, want 1 without another Close", got)
	}
}

func TestCloseTimesOutWithoutReleasingPacketOwnedByStuckDecoder(t *testing.T) {
	pool := newPacketPool(8 << 10)
	packet, err := pool.acquire(100)
	if err != nil {
		t.Fatal(err)
	}
	owner := packet.owner
	source, _ := NewSyntheticSource(1)
	decoder := &stuckDecoder{started: make(chan struct{}), release: make(chan struct{})}
	cfg := DefaultConfig()
	cfg.CloseTimeout = 100 * time.Millisecond
	session, err := StartPipeline(context.Background(), cfg, source, decoder)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Push(packet); err != nil {
		t.Fatal(err)
	}
	select {
	case <-decoder.started:
	case <-time.After(time.Second):
		t.Fatal("decoder did not accept packet")
	}

	started := time.Now()
	err = session.Close()
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Close took %s after native stall", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "shutdown exceeded") {
		t.Fatalf("Close error = %v, want bounded shutdown error", err)
	}
	if !errors.Is(err, ErrShutdownUncertain) {
		t.Fatalf("Close error = %v, want uncertain ownership classification", err)
	}
	if token := owner.token.Load(); token == 0 {
		t.Fatal("packet was recycled while decoder still owned its data")
	}
	close(decoder.release)
	deadline := time.Now().Add(time.Second)
	for owner.token.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if token := owner.token.Load(); token != 0 {
		t.Fatalf("packet token %d was not released after decoder returned", token)
	}
	deadline = time.Now().Add(time.Second)
	for decoder.frameReleases.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if releases := decoder.frameReleases.Load(); releases != 1 {
		t.Fatalf("late frame releases = %d, want 1", releases)
	}
}

func TestSourceErrorStopsPipeline(t *testing.T) {
	source, _ := NewSyntheticSource(1)
	decoder := &fakeDecoder{}
	session, err := StartPipeline(context.Background(), DefaultConfig(), source, decoder)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("camera disconnected")
	if err := source.Fail(want); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-session.Errors:
		if !errors.Is(got, want) {
			t.Fatalf("error = %v", got)
		}
		if IsTemporary(got) {
			t.Fatalf("fatal error was classified as temporary: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no source error")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTemporarySourceErrorDoesNotStopPipeline(t *testing.T) {
	source, _ := NewSyntheticSource(1)
	decoder := &fakeDecoder{}
	session, err := StartPipeline(context.Background(), DefaultConfig(), source, decoder)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("startup packet was incomplete")
	if err := source.Fail(temporaryCaptureError{err: want}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-session.Errors:
		if !errors.Is(got, want) {
			t.Fatalf("error = %v", got)
		}
		if !IsTemporary(got) {
			t.Fatalf("error was not classified as temporary: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no transient source error")
	}
	if err := source.Push(Packet{Data: []byte{1}, PTS: 4200 * time.Nanosecond}); err != nil {
		t.Fatal(err)
	}
	select {
	case frame, ok := <-session.Frames:
		if !ok {
			t.Fatal("pipeline stopped after transient source error")
		}
		if frame.PTS != 4200*time.Nanosecond {
			t.Fatalf("timestamp = %s", frame.PTS)
		}
		frame.Release()
	case <-time.After(time.Second):
		t.Fatal("no frame after transient source error")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedDecodeErrorIsTemporaryAndDoesNotStopPipeline(t *testing.T) {
	source, _ := NewSyntheticSource(2)
	decoder := &fakeDecoder{}
	decoder.failures.Store(1)
	session, err := StartPipeline(context.Background(), DefaultConfig(), source, decoder)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := source.Push(Packet{Data: []byte{1}, PTS: 100 * time.Nanosecond}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-session.Errors:
		if !IsTemporary(got) {
			t.Fatalf("isolated decode error was fatal: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no isolated decode error")
	}
	if err := source.Push(Packet{Data: []byte{1}, PTS: 200 * time.Nanosecond}); err != nil {
		t.Fatal(err)
	}
	select {
	case frame, ok := <-session.Frames:
		if !ok {
			t.Fatal("pipeline stopped after an isolated decode error")
		}
		if frame.PTS != 200*time.Nanosecond {
			t.Fatalf("timestamp = %s, want 200ns", frame.PTS)
		}
		frame.Release()
	case <-time.After(time.Second):
		t.Fatal("no frame after isolated decode error")
	}
}

func TestConsecutiveDecodeErrorsBecomeFatal(t *testing.T) {
	source, _ := NewSyntheticSource(4)
	decoder := &fakeDecoder{}
	decoder.failures.Store(3)
	cfg := DefaultConfig()
	cfg.MaxConsecutiveErrors = 3
	session, err := StartPipeline(context.Background(), cfg, source, decoder)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for index := int64(1); index <= 3; index++ {
		if err := source.Push(Packet{Data: []byte{1}, PTS: time.Duration(index) * 100 * time.Nanosecond}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.After(time.Second)
	temporary := 0
	for {
		select {
		case got, ok := <-session.Errors:
			if !ok {
				t.Fatal("error channel closed without terminal decode error")
			}
			if IsTemporary(got) {
				temporary++
				continue
			}
			if temporary != 2 {
				t.Fatalf("temporary errors = %d, want 2", temporary)
			}
			if !errors.Is(got, errFakeDamagedJPEG) {
				t.Fatalf("terminal decode error = %v", got)
			}
			return
		case <-deadline:
			t.Fatal("no terminal decode error")
		}
	}
}

func TestTerminalErrorEvictsQueuedTemporaryNotice(t *testing.T) {
	destination := make(chan error, 1)
	temporary := temporaryCaptureError{err: errors.New("one frame was dropped")}
	fatal := errors.New("camera disconnected")
	publishBoundedError(destination, temporary)
	publishBoundedError(destination, fatal)
	select {
	case got := <-destination:
		if !errors.Is(got, fatal) || IsTemporary(got) {
			t.Fatalf("queued error = %v, want terminal disconnect", got)
		}
	default:
		t.Fatal("terminal error was dropped")
	}
}

func TestSessionReportCountsEveryTemporaryErrorAndNoTerminalErrors(t *testing.T) {
	session := &Session{errors: make(chan error, 1)}
	temporary := temporaryCaptureError{err: errors.New("one frame was dropped")}
	session.report(temporary)
	session.report(fmt.Errorf("capture source: %w", temporary))
	session.report(errors.New("camera disconnected"))

	if got := session.Stats().TemporaryErrors; got != 2 {
		t.Fatalf("temporary errors = %d, want 2", got)
	}
}

func TestFrameWatchdogTimeouts(t *testing.T) {
	if got := frameWatchdogTimeout(30, true); got != 8*time.Second {
		t.Fatalf("initial 30 fps watchdog = %s", got)
	}
	if got := frameWatchdogTimeout(30, false); got != 3*time.Second {
		t.Fatalf("steady 30 fps watchdog = %s", got)
	}
	if got := frameWatchdogTimeout(1, true); got != 24*time.Second {
		t.Fatalf("initial 1 fps watchdog = %s", got)
	}
	if got := frameWatchdogTimeout(1, false); got != 12*time.Second {
		t.Fatalf("steady 1 fps watchdog = %s", got)
	}
}

func TestConfigBounds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DeviceID = "stable"
	if _, err := normalize(cfg, true); err != nil {
		t.Fatal(err)
	}
	cfg.QueueDepth = maxQueueDepth + 1
	if _, err := normalize(cfg, true); err == nil {
		t.Fatal("expected queue bound error")
	}
	if _, err := normalize(DefaultConfig(), true); err == nil {
		t.Fatal("expected stable ID error")
	}
}
