package mfcapture

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

var errFakeDamagedJPEG = errors.New("damaged jpeg")

type fakeDecoder struct {
	releases atomic.Int32
	closes   atomic.Int32
	block    <-chan struct{}
	failures atomic.Int32
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
	return &Frame{ReaderTimestamp100ns: packet.ReaderTimestamp100ns, release: func() { d.releases.Add(1) }}, nil
}

func (d *fakeDecoder) Close() error { d.closes.Add(1); return nil }

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
		if err := source.Push(Packet{Data: []byte{1}, ReaderTimestamp100ns: int64(i)}); err != nil {
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
		if frame.ReaderTimestamp100ns != 3 {
			t.Fatalf("latest timestamp = %d", frame.ReaderTimestamp100ns)
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
	if err := source.Push(Packet{Data: []byte{1}, ReaderTimestamp100ns: 42}); err != nil {
		t.Fatal(err)
	}
	select {
	case frame, ok := <-session.Frames:
		if !ok {
			t.Fatal("pipeline stopped after transient source error")
		}
		if frame.ReaderTimestamp100ns != 42 {
			t.Fatalf("timestamp = %d", frame.ReaderTimestamp100ns)
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
	if err := source.Push(Packet{Data: []byte{1}, ReaderTimestamp100ns: 1}); err != nil {
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
	if err := source.Push(Packet{Data: []byte{1}, ReaderTimestamp100ns: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case frame, ok := <-session.Frames:
		if !ok {
			t.Fatal("pipeline stopped after an isolated decode error")
		}
		if frame.ReaderTimestamp100ns != 2 {
			t.Fatalf("timestamp = %d, want 2", frame.ReaderTimestamp100ns)
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
		if err := source.Push(Packet{Data: []byte{1}, ReaderTimestamp100ns: index}); err != nil {
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

func TestMediaEventClassification(t *testing.T) {
	if err := classifyMediaEvent(42, 0); err != nil {
		t.Fatalf("ordinary event = %v", err)
	}
	if err := classifyMediaEvent(mfEventNonFatalError, 0); err == nil || !IsTemporary(err) {
		t.Fatalf("nonfatal event = %v, want temporary", err)
	}
	for _, eventType := range []uint32{mfEventError, mfEventVideoCaptureDeviceRemoved, mfEventVideoCaptureDevicePreempted} {
		if err := classifyMediaEvent(eventType, 0); err == nil || IsTemporary(err) {
			t.Fatalf("event %d = %v, want terminal", eventType, err)
		}
	}
	if err := classifyMediaEvent(42, uintptr(0x80004005)); err == nil || IsTemporary(err) {
		t.Fatalf("failed event status = %v, want terminal", err)
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
