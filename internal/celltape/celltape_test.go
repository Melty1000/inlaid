package celltape

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func canonicalCell(mask uint8, n uint32) Cell {
	fg := RGB(0x102030 + n)
	if mask == 0 {
		return Cell{FG: fg, BG: fg}
	}
	return Cell{Mask: mask, FG: fg, BG: RGB(0xa0b0c0 + n)}
}

func sampleInput() Input {
	return Input{GeometryEpoch: 1, ConfigEpoch: 1, Columns: 2, Rows: 2, Config: []byte("a"), SourceNanos: 10,
		Cells: []Cell{canonicalCell(0, 1), canonicalCell(1, 2), canonicalCell(2, 3), canonicalCell(3, 4)}}
}

func makeTape(t *testing.T) (string, []Frame) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.celltape")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(context.Background(), f, Config{QueueCapacity: 16, KeyframeInterval: 8, Compression: CompressionFast})
	if err != nil {
		t.Fatal(err)
	}
	in := sampleInput()
	want := []Frame{}
	commit := func(in Input, host uint64) {
		t.Helper()
		if err := r.Submit(in, host); err != nil {
			t.Fatal(err)
		}
		want = append(want, Frame{Sequence: uint64(len(want) + 1), GeometryEpoch: in.GeometryEpoch, ConfigEpoch: in.ConfigEpoch, Columns: in.Columns, Rows: in.Rows, Config: append([]byte(nil), in.Config...), Cells: append([]Cell(nil), in.Cells...), SourceNanos: in.SourceNanos, HostNanos: host, Boundary: in.Boundary})
	}
	commit(in, 20)
	in.Cells[2] = canonicalCell(4, 9)
	in.SourceNanos = 30
	commit(in, 40)
	in.SourceNanos = 30
	commit(in, 50) // duplicate visual/source hold
	in = Input{GeometryEpoch: 2, ConfigEpoch: 2, Columns: 1, Rows: 2, Config: []byte("b"), Cells: []Cell{canonicalCell(5, 1), canonicalCell(6, 2)}, SourceNanos: 60, Boundary: BoundaryGap | BoundaryDiscontinuity}
	commit(in, 70)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return path, want
}

func TestRoundTripDeterministicDeltaHoldAndResize(t *testing.T) {
	path, want := makeTape(t)
	r, err := Open(path, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.Recovery(); got.ValidRecords != uint64(len(want)) || got.TailError != nil {
		t.Fatalf("recovery = %+v", got)
	}
	var got []Frame
	if err = r.Iterate(context.Background(), func(f Frame) error { got = append(got, f); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replay mismatch\n got %#v\nwant %#v", got, want)
	}
	r.Rewind()
	again, err := r.Next()
	if err != nil || !reflect.DeepEqual(again, want[0]) {
		t.Fatalf("rewind = %#v, %v", again, err)
	}
}

func TestRecoveryAtEveryByteAndRepairTail(t *testing.T) {
	path, _ := makeTape(t)
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for cut := 0; cut <= len(full); cut++ {
		p := filepath.Join(t.TempDir(), "cut.celltape")
		if err = os.WriteFile(p, full[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		r, openErr := Open(p, OpenOptions{})
		if cut < FileHeaderBytes {
			if openErr == nil {
				r.Close()
				t.Fatalf("cut %d accepted truncated file header", cut)
			}
			continue
		}
		if openErr != nil {
			t.Fatalf("cut %d: %v", cut, openErr)
		}
		recovery := r.Recovery()
		if recovery.ValidBytes > int64(cut) {
			t.Fatalf("cut %d valid bytes %d", cut, recovery.ValidBytes)
		}
		for {
			_, e := r.Next()
			if errors.Is(e, io.EOF) {
				break
			}
			if e != nil {
				t.Fatalf("cut %d replay: %v", cut, e)
			}
		}
		r.Close()
	}
	p := filepath.Join(t.TempDir(), "repair.celltape")
	if err = os.WriteFile(p, full[:len(full)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(p, OpenOptions{RepairTail: true})
	if err != nil {
		t.Fatal(err)
	}
	recovery := r.Recovery()
	r.Close()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.DiscardedBytes == 0 || info.Size() != recovery.ValidBytes {
		t.Fatalf("repair recovery=%+v size=%d", recovery, info.Size())
	}
}

func TestCorruptCRCAndMalformedCanonicalCellAreDiscarded(t *testing.T) {
	path, _ := makeTape(t)
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), full...)
	corrupt[FileHeaderBytes+ChunkHeaderBytes] ^= 0x80
	p := filepath.Join(t.TempDir(), "crc.celltape")
	os.WriteFile(p, corrupt, 0o600)
	r, err := Open(p, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Recovery().TailError == nil || r.Recovery().ValidRecords != 0 {
		t.Fatalf("corrupt payload accepted: %+v", r.Recovery())
	}
	r.Close()
	bad := append([]byte(nil), full...)
	payload := FileHeaderBytes + ChunkHeaderBytes
	cellOffset := payload + 8 + 1 + 1 + 1
	bad[cellOffset] = 8
	storedLen := int(binary.LittleEndian.Uint32(bad[FileHeaderBytes+56 : FileHeaderBytes+60]))
	footer := payload + storedLen
	binary.LittleEndian.PutUint32(bad[footer+16:footer+20], crc32.Checksum(bad[payload:footer], castagnoli))
	binary.LittleEndian.PutUint32(bad[footer+20:footer+24], crc32.Checksum(bad[footer:footer+20], castagnoli))
	p = filepath.Join(t.TempDir(), "cell.celltape")
	os.WriteFile(p, bad, 0o600)
	r, err = Open(p, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Recovery().TailError == nil || r.Recovery().ValidRecords != 0 {
		t.Fatalf("noncanonical cell accepted: %+v", r.Recovery())
	}
	r.Close()
	header := append([]byte(nil), full...)
	header[16] ^= 1
	p = filepath.Join(t.TempDir(), "header.celltape")
	os.WriteFile(p, header, 0o600)
	if r, err = Open(p, OpenOptions{}); err == nil {
		r.Close()
		t.Fatal("bad header CRC accepted")
	}
}

func TestOversizedLengthAndNonCanonicalVarintAreDiscarded(t *testing.T) {
	path, _ := makeTape(t)
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oversized := append([]byte(nil), full...)
	header := oversized[FileHeaderBytes : FileHeaderBytes+ChunkHeaderBytes]
	binary.LittleEndian.PutUint32(header[56:60], DefaultLimits().MaxChunkBytes+1)
	binary.LittleEndian.PutUint32(header[60:64], crc32.Checksum(header[:60], castagnoli))
	p := filepath.Join(t.TempDir(), "oversized.celltape")
	if err = os.WriteFile(p, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(p, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Recovery().TailError == nil || r.Recovery().ValidRecords != 0 {
		t.Fatalf("oversized chunk accepted: %+v", r.Recovery())
	}
	r.Close()

	badVarint := append([]byte(nil), full...)
	payload := FileHeaderBytes + ChunkHeaderBytes
	badVarint[payload+8], badVarint[payload+9] = 0x81, 0x00
	storedLen := int(binary.LittleEndian.Uint32(badVarint[FileHeaderBytes+56 : FileHeaderBytes+60]))
	footer := payload + storedLen
	binary.LittleEndian.PutUint32(badVarint[footer+16:footer+20], crc32.Checksum(badVarint[payload:footer], castagnoli))
	binary.LittleEndian.PutUint32(badVarint[footer+20:footer+24], crc32.Checksum(badVarint[footer:footer+20], castagnoli))
	p = filepath.Join(t.TempDir(), "varint.celltape")
	if err = os.WriteFile(p, badVarint, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err = Open(p, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Recovery().TailError == nil || r.Recovery().ValidRecords != 0 {
		t.Fatalf("noncanonical varint accepted: %+v", r.Recovery())
	}
	r.Close()
}

func TestTimingAndEpochRegressions(t *testing.T) {
	s := &memorySink{}
	r, err := New(context.Background(), s, Config{QueueCapacity: 8})
	if err != nil {
		t.Fatal(err)
	}
	in := sampleInput()
	if err = r.Submit(in, 20); err != nil {
		t.Fatal(err)
	}
	in.SourceNanos = 9
	if err = r.Submit(in, 21); !errors.Is(err, ErrTimingRegression) {
		t.Fatalf("source regression = %v", err)
	}
	in = sampleInput()
	if err = r.Submit(in, 19); !errors.Is(err, ErrTimingRegression) {
		t.Fatalf("host regression = %v", err)
	}
	in = sampleInput()
	in.Columns, in.Rows = 1, 4
	if err = r.Submit(in, 21); !errors.Is(err, ErrEpochRegression) {
		t.Fatalf("geometry regression = %v", err)
	}
	in = sampleInput()
	in.Config = []byte("changed")
	if err = r.Submit(in, 21); !errors.Is(err, ErrEpochRegression) {
		t.Fatalf("config regression = %v", err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
}

type memorySink struct {
	mu sync.Mutex
	bytes.Buffer
	closed bool
}

func (s *memorySink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	return s.Buffer.Write(p)
}
func (s *memorySink) Sync() error  { return nil }
func (s *memorySink) Close() error { s.mu.Lock(); defer s.mu.Unlock(); s.closed = true; return nil }

type blockingSink struct {
	memorySink
	block   bool
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type failingSink struct {
	memorySink
	fail bool
}

type periodicSyncSink struct {
	memorySink
	syncMu    sync.Mutex
	syncCalls int
	blockAt   int
	failAt    int
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

func (*periodicSyncSink) celltapeConcurrentSyncSafe() {}

func (s *periodicSyncSink) Sync() error {
	s.syncMu.Lock()
	s.syncCalls++
	call := s.syncCalls
	s.syncMu.Unlock()
	if call == s.blockAt || call == s.failAt {
		s.startOnce.Do(func() { close(s.started) })
	}
	if call == s.blockAt {
		<-s.release
	}
	if call == s.failAt {
		return errTestDurabilitySync
	}
	return nil
}

var errTestDurabilitySync = errors.New("durability sync failed")

func (s *failingSink) Write(p []byte) (int, error) {
	if s.fail {
		return 0, errors.New("disk full")
	}
	return s.memorySink.Write(p)
}

func (s *blockingSink) Write(p []byte) (int, error) {
	if s.block {
		s.once.Do(func() { close(s.started) })
		<-s.release
	}
	return s.memorySink.Write(p)
}

func TestQueueSaturationFailsVisibly(t *testing.T) {
	s := &blockingSink{started: make(chan struct{}), release: make(chan struct{})}
	r, err := New(context.Background(), s, Config{QueueCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	s.block = true
	in := sampleInput()
	if err = r.Submit(in, 20); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not reach disk")
	}
	in.SourceNanos = 21
	if err = r.Submit(in, 21); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = r.Prepare(in)
	if !errors.Is(err, ErrQueueSaturated) {
		t.Fatalf("saturation = %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("saturation blocked producer")
	}
	close(s.release)
	if err = r.Close(); !errors.Is(err, ErrQueueSaturated) {
		t.Fatalf("close = %v", err)
	}
}

func TestPeriodicSyncStallDoesNotConsumeProducerQueue(t *testing.T) {
	s := &periodicSyncSink{
		blockAt: 2, // call 1 is the synchronous file-header boundary
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	r, err := New(context.Background(), s, Config{
		QueueCapacity:    2,
		DurabilityWindow: time.Millisecond,
		Compression:      CompressionFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	in := sampleInput()
	if err = r.Submit(in, 20); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.started:
	case <-time.After(time.Second):
		t.Fatal("periodic sync did not start")
	}

	// More than ten complete queue-loads pass through while Sync remains
	// blocked. The worker must continue writing and recycling the same two
	// producer buffers; increasing queue memory cannot make this test pass.
	waitForFreeBuffers(t, r, 2)
	// Sixty additional 30 FPS frames represent two seconds of capture through a
	// queue that can retain only two frames at any instant.
	const additional = 60
	for i := 1; i <= additional; i++ {
		in.SourceNanos = uint64(10 + i)
		if err = r.Submit(in, uint64(20+i)); err != nil {
			t.Fatalf("frame %d during sync stall: %v", i, err)
		}
		waitForFreeBuffers(t, r, 2)
	}
	if err = r.Err(); err != nil {
		t.Fatalf("recorder failed during routine sync stall: %v", err)
	}
	if cap(r.free) != 2 || len(r.pool) != 2 {
		t.Fatalf("producer bound changed: free capacity %d, pool %d", cap(r.free), len(r.pool))
	}

	// A crash snapshot taken during the stalled durability call still consists
	// only of complete, CRC-checked commits. Whether the newest commits reached
	// stable storage is the durability window's concern, not tape integrity.
	s.mu.Lock()
	snapshot := append([]byte(nil), s.Buffer.Bytes()...)
	s.mu.Unlock()
	path := filepath.Join(t.TempDir(), "sync-stall.celltape")
	if err = os.WriteFile(path, snapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	replay, err := Open(path, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	frames := 0
	var last Frame
	for {
		frame, nextErr := replay.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			replay.Close()
			t.Fatal(nextErr)
		}
		last = frame
		frames++
	}
	if err = replay.Close(); err != nil {
		t.Fatal(err)
	}
	if frames != additional+1 {
		t.Fatalf("committed crash prefix has %d frames, want %d", frames, additional+1)
	}
	if last.Sequence != additional+1 || last.SourceNanos != 10+additional || last.HostNanos != 20+additional {
		t.Fatalf("last committed state = sequence %d, source %d, host %d", last.Sequence, last.SourceNanos, last.HostNanos)
	}

	close(s.release)
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPeriodicSyncFailureFailsRecorder(t *testing.T) {
	s := &periodicSyncSink{
		failAt:  2, // call 1 is the synchronous file-header boundary
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	r, err := New(context.Background(), s, Config{
		QueueCapacity:    2,
		DurabilityWindow: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err = r.Submit(sampleInput(), 20); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Done():
	case <-time.After(time.Second):
		t.Fatal("periodic sync failure was not reported")
	}
	if !errors.Is(r.Err(), errTestDurabilitySync) {
		t.Fatalf("recorder error = %v, want durability failure", r.Err())
	}
	if err = r.Close(); !errors.Is(err, errTestDurabilitySync) {
		t.Fatalf("close = %v, want durability failure", err)
	}
}

func waitForFreeBuffers(t *testing.T, r *Recorder, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(r.free) != want {
		if time.Now().After(deadline) {
			t.Fatalf("free buffers = %d, want %d", len(r.free), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDiskFailureAndPrepareAbort(t *testing.T) {
	s := &failingSink{}
	r, err := New(context.Background(), s, Config{QueueCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Prepare(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	p.Abort()
	p.Abort()
	if err = p.Commit(20); !errors.Is(err, ErrPreparedDone) {
		t.Fatalf("commit after abort = %v", err)
	}
	s.fail = true
	if err = r.Submit(sampleInput(), 20); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Done():
	case <-time.After(time.Second):
		t.Fatal("disk failure was not reported")
	}
	if r.Err() == nil {
		t.Fatal("disk failure not visible through Err")
	}
	if err = r.Close(); err == nil || !errors.Is(err, r.Err()) {
		t.Fatalf("close = %v, recorder err = %v", err, r.Err())
	}
}

func TestPublishAndSizeReport(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "x.tmp")
	final := filepath.Join(dir, "x.celltape")
	if err := os.WriteFile(staging, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Publish(staging, final); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "y.tmp")
	os.WriteFile(other, []byte("y"), 0o600)
	if err := Publish(other, final); err == nil {
		t.Fatal("publish overwrote target")
	}
	outside := filepath.Join(t.TempDir(), "z")
	if err := Publish(other, outside); err == nil {
		t.Fatal("cross-directory publish accepted")
	}
	cfg := Config{QueueCapacity: 3, DurabilityWindow: 250 * time.Millisecond}
	report, err := Preflight(2, 2, 1, 30, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if report.WorstFrameBytes == 0 || report.WorstBytesHour <= report.WorstFrameBytes || report.DurabilityWindow != 250*time.Millisecond || report.QueueCapacity != 3 {
		t.Fatalf("report = %+v", report)
	}
}

func TestPublishAtomicallyRefusesConcurrentReplacement(t *testing.T) {
	directory := t.TempDir()
	final := filepath.Join(directory, "winner.celltape")
	staging := []string{
		filepath.Join(directory, "first.tmp"),
		filepath.Join(directory, "second.tmp"),
	}
	contents := [][]byte{[]byte("first"), []byte("second")}
	for index := range staging {
		if err := os.WriteFile(staging[index], contents[index], 0o600); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, len(staging))
	for _, path := range staging {
		go func() {
			<-start
			results <- Publish(path, final)
		}()
	}
	close(start)
	var succeeded int
	for range staging {
		if err := <-results; err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful concurrent publishes = %d, want exactly 1", succeeded)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, contents[0]) && !bytes.Equal(got, contents[1]) {
		t.Fatalf("published bytes = %q, want one complete contender", got)
	}
	remaining := 0
	for _, path := range staging {
		if _, err := os.Stat(path); err == nil {
			remaining++
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if remaining != 1 {
		t.Fatalf("remaining losing stages = %d, want 1", remaining)
	}
}

func TestCancellationAndIdempotentClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &memorySink{}
	r, err := New(ctx, s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-r.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop worker")
	}
	a := r.Close()
	b := r.Close()
	if !errors.Is(a, context.Canceled) || a.Error() != b.Error() {
		t.Fatalf("close errors %v / %v", a, b)
	}
}

func FuzzChunkRecoveryDoesNotPanic(f *testing.F) {
	path, _ := makeTapeForFuzz(f)
	seed, _ := os.ReadFile(path)
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(t.TempDir(), "fuzz.celltape")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		r, _ := Open(p, OpenOptions{})
		if r != nil {
			for {
				_, err := r.Next()
				if err != nil {
					break
				}
			}
			r.Close()
		}
	})
}

func makeTapeForFuzz(t testing.TB) (string, []Frame) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seed.celltape")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(context.Background(), file, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Submit(sampleInput(), 20); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	return path, nil
}
