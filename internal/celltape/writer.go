package celltape

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type acceptedState struct {
	have                       bool
	sequence                   uint64
	geometryEpoch, configEpoch uint64
	columns, rows              uint32
	config                     []byte
	sourceNanos, hostNanos     uint64
	framesSinceKey             uint32
}

type job struct {
	buffer              *jobBuffer
	hostNanos, sequence uint64
	forceKey            bool
}

type bufferState uint8

const (
	bufferFree bufferState = iota
	bufferPrepared
	bufferQueued
)

// jobBuffer is one of exactly QueueCapacity reusable producer buffers. It is
// never exposed directly: a generation-tagged reservation prevents an old
// prepared token from acting on the buffer after it has been recycled.
type jobBuffer struct {
	mu         sync.Mutex
	generation uint64
	state      bufferState
	input      Input
	config     []byte
	cells      []Cell
}

type reservation struct {
	recorder   *Recorder
	buffer     *jobBuffer
	generation uint64
}

// bufferPool is a bounded LIFO free list. Reusing the hottest buffer keeps the
// retained cell backing proportional to the producer's actual high-water
// depth; a FIFO free channel eventually warmed every configured stall slot
// even when the disk worker was never more than one frame behind.
type bufferPool struct {
	mu        sync.Mutex
	free      []*jobBuffer
	maximum   int
	highWater int
}

func (p *bufferPool) take() *jobBuffer {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.free) == 0 {
		return nil
	}
	last := len(p.free) - 1
	buffer := p.free[last]
	p.free[last] = nil
	p.free = p.free[:last]
	p.highWater = max(p.highWater, p.maximum-len(p.free))
	return buffer
}

func (p *bufferPool) put(buffer *jobBuffer) {
	if buffer == nil {
		return
	}
	p.mu.Lock()
	p.free = append(p.free, buffer)
	p.mu.Unlock()
}

func (p *bufferPool) available() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.free)
}

func (p *bufferPool) capacity() int { return p.maximum }

func (p *bufferPool) pressure() QueuePressure {
	p.mu.Lock()
	defer p.mu.Unlock()
	return QueuePressure{
		InFlight:  p.maximum - len(p.free),
		HighWater: p.highWater,
		Capacity:  p.maximum,
	}
}

// concurrentSyncSink is deliberately package-private. Create wraps its os.File
// with this marker because os.File documents all of its methods as safe for
// concurrent use. Caller-provided sinks used through New retain serialized
// Write/Sync behavior unless this package created them.
type concurrentSyncSink interface {
	Sink
	celltapeConcurrentSyncSafe()
}

type fileSink struct {
	*os.File
}

func (*fileSink) celltapeConcurrentSyncSafe() {}

// syncWorker coalesces periodic durability requests. There is never more than
// one Sync in flight and the single result slot lets shutdown collect an I/O
// error even if the disk call finishes while the writer is exiting.
type syncWorker struct {
	sink     concurrentSyncSink
	request  chan struct{}
	result   chan error
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func startSyncWorker(sink concurrentSyncSink) *syncWorker {
	w := &syncWorker{
		sink:    sink,
		request: make(chan struct{}, 1),
		result:  make(chan error, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go w.run()
	return w
}

func (w *syncWorker) run() {
	defer close(w.done)
	for {
		select {
		case <-w.request:
			w.result <- w.sink.Sync()
		case <-w.stop:
			return
		}
	}
}

func (w *syncWorker) requestSync() bool {
	// The tape writer tracks the one-in-flight invariant. Keep the channel send
	// nonblocking as a defensive measure so durability can never stall ingestion.
	select {
	case w.request <- struct{}{}:
		return true
	default:
		return false
	}
}

func (w *syncWorker) close() error {
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
	select {
	case err := <-w.result:
		return err
	default:
		return nil
	}
}

// Recorder is a nonblocking producer facade over one bounded disk worker.
type Recorder struct {
	sink   Sink
	cfg    Config
	jobs   chan job
	free   bufferPool
	pool   []jobBuffer
	stop   chan struct{}
	done   chan struct{}
	errors chan error

	mu          sync.Mutex
	accepted    acceptedState
	closing     bool
	fatal       error
	closeOnce   sync.Once
	closeErr    error
	stagingPath string
}

type Prepared struct {
	reservation reservation
}

func newRecorder(ctx context.Context, sink Sink, cfg Config) (*Recorder, error) {
	if sink == nil {
		return nil, errors.New("celltape sink is nil")
	}
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	if err = writeFull(sink, marshalFileHeader(cfg)); err != nil {
		_ = sink.Close()
		return nil, fmt.Errorf("write celltape header: %w", err)
	}
	if err = sink.Sync(); err != nil {
		_ = sink.Close()
		return nil, fmt.Errorf("sync celltape header: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r := &Recorder{
		sink:   sink,
		cfg:    cfg,
		jobs:   make(chan job, cfg.QueueCapacity),
		pool:   make([]jobBuffer, cfg.QueueCapacity),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		errors: make(chan error, 1),
	}
	r.free.maximum = cfg.QueueCapacity
	r.free.free = make([]*jobBuffer, 0, cfg.QueueCapacity)
	for i := range r.pool {
		r.free.put(&r.pool[i])
	}
	go r.run(ctx)
	return r, nil
}

// Create creates a same-directory staging file. Close makes the tape durable
// but does not publish it; call Publish to atomically rename it on the volume.
func Create(ctx context.Context, finalPath string, cfg Config) (*Recorder, error) {
	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(finalPath)+".*.celltape.tmp")
	if err != nil {
		return nil, err
	}
	r, err := newRecorder(ctx, &fileSink{File: f}, cfg)
	if err != nil {
		_ = os.Remove(f.Name())
		return nil, err
	}
	r.stagingPath = f.Name()
	return r, nil
}

func (r *Recorder) StagingPath() string {
	if r == nil {
		return ""
	}
	return r.stagingPath
}

// QueuePressure returns a lock-consistent snapshot of bounded producer use.
func (r *Recorder) QueuePressure() QueuePressure {
	if r == nil {
		return QueuePressure{}
	}
	return r.free.pressure()
}
func (r *Recorder) Done() <-chan struct{} {
	if r == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return r.done
}
func (r *Recorder) Err() error {
	if r == nil {
		return ErrClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fatal
}

// Prepare validates and deep-copies a state only after reserving one of the
// fixed producer slots. Saturation permanently and visibly fails the recorder.
func (r *Recorder) Prepare(in Input) (*Prepared, error) {
	prepared, err := r.reserve()
	if err != nil {
		return nil, err
	}
	if err := validateInput(in, r.cfg.Limits); err != nil {
		prepared.abort()
		return nil, err
	}
	prepared.setInput(in, in.Cells)
	return &Prepared{reservation: prepared}, nil
}

func (r *Recorder) reserve() (reservation, error) {
	if r == nil {
		return reservation{}, ErrClosed
	}
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return reservation{}, ErrClosed
	}
	if r.fatal != nil {
		err := r.fatal
		r.mu.Unlock()
		return reservation{}, err
	}
	r.mu.Unlock()
	buffer := r.free.take()
	if buffer == nil {
		err := fmt.Errorf("%w (capacity %d)", ErrQueueSaturated, r.cfg.QueueCapacity)
		r.fail(err)
		return reservation{}, err
	}
	buffer.mu.Lock()
	buffer.generation++
	// Generation zero is reserved for the zero-value token. Wrapping would
	// require more than 584 years at one billion preparations per second.
	if buffer.generation == 0 {
		buffer.generation++
	}
	buffer.state = bufferPrepared
	buffer.input = Input{}
	generation := buffer.generation
	buffer.mu.Unlock()
	return reservation{recorder: r, buffer: buffer, generation: generation}, nil
}

func (p reservation) setInput(in Input, cells []Cell) {
	buffer := p.buffer
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.generation != p.generation || buffer.state != bufferPrepared {
		return
	}
	buffer.config = append(buffer.config[:0], in.Config...)
	if cap(buffer.cells) < len(cells) {
		buffer.cells = make([]Cell, len(cells))
	} else {
		buffer.cells = buffer.cells[:len(cells)]
	}
	copy(buffer.cells, cells)
	in.Config = buffer.config
	in.Cells = buffer.cells
	buffer.input = in
}

func validateInput(in Input, l Limits) error {
	n, err := checkedCellCount(in.Columns, in.Rows, l)
	if err != nil {
		return err
	}
	if int(n) != len(in.Cells) {
		return fmt.Errorf("cell count %d does not match geometry %d", len(in.Cells), n)
	}
	if len(in.Config) > int(l.MaxConfigBytes) {
		return fmt.Errorf("config exceeds limit")
	}
	if in.Boundary & ^(BoundaryGap|BoundaryDiscontinuity) != 0 {
		return fmt.Errorf("unknown boundary flags")
	}
	for _, c := range in.Cells {
		if err := validateCell(c); err != nil {
			return err
		}
	}
	return nil
}

// Commit accepts the prepared state at a monotonic host-clock offset. It never
// waits for disk or compression because Prepare already reserved its queue slot.
func (p *Prepared) Commit(hostNanos uint64) error {
	if p == nil {
		return ErrPreparedDone
	}
	return p.reservation.commit(hostNanos)
}

func (p reservation) commit(hostNanos uint64) error {
	if p.recorder == nil || p.buffer == nil || p.generation == 0 {
		return ErrPreparedDone
	}
	r := p.recorder
	buffer := p.buffer
	buffer.mu.Lock()
	if buffer.generation != p.generation || buffer.state != bufferPrepared {
		buffer.mu.Unlock()
		return ErrPreparedDone
	}
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		buffer.state = bufferFree
		buffer.input = Input{}
		buffer.mu.Unlock()
		r.free.put(buffer)
		return ErrClosed
	}
	if r.fatal != nil {
		err := r.fatal
		r.mu.Unlock()
		buffer.state = bufferFree
		buffer.input = Input{}
		buffer.mu.Unlock()
		r.free.put(buffer)
		return err
	}
	a := &r.accepted
	in := buffer.input
	if a.have {
		if in.SourceNanos < a.sourceNanos || hostNanos < a.hostNanos {
			r.mu.Unlock()
			buffer.state = bufferFree
			buffer.input = Input{}
			buffer.mu.Unlock()
			r.free.put(buffer)
			return ErrTimingRegression
		}
		if in.GeometryEpoch < a.geometryEpoch || (in.GeometryEpoch == a.geometryEpoch && (in.Columns != a.columns || in.Rows != a.rows)) {
			r.mu.Unlock()
			buffer.state = bufferFree
			buffer.input = Input{}
			buffer.mu.Unlock()
			r.free.put(buffer)
			return ErrEpochRegression
		}
		if in.ConfigEpoch < a.configEpoch || (in.ConfigEpoch == a.configEpoch && !bytes.Equal(in.Config, a.config)) {
			r.mu.Unlock()
			buffer.state = bufferFree
			buffer.input = Input{}
			buffer.mu.Unlock()
			r.free.put(buffer)
			return ErrEpochRegression
		}
	}
	sequence := a.sequence + 1
	force := !a.have || in.GeometryEpoch != a.geometryEpoch || in.ConfigEpoch != a.configEpoch || in.Boundary&BoundaryDiscontinuity != 0 || a.framesSinceKey+1 >= r.cfg.KeyframeInterval
	buffer.state = bufferQueued
	r.jobs <- job{buffer: buffer, hostNanos: hostNanos, sequence: sequence, forceKey: force}
	frames := a.framesSinceKey + 1
	if force {
		frames = 0
	}
	*a = acceptedState{have: true, sequence: sequence, geometryEpoch: in.GeometryEpoch, configEpoch: in.ConfigEpoch, columns: in.Columns, rows: in.Rows, config: append(a.config[:0], in.Config...), sourceNanos: in.SourceNanos, hostNanos: hostNanos, framesSinceKey: frames}
	r.mu.Unlock()
	buffer.mu.Unlock()
	return nil
}

// Abort is idempotent and releases the reserved producer slot.
func (p *Prepared) Abort() {
	if p == nil {
		return
	}
	p.reservation.abort()
}

func (p reservation) abort() {
	if p.recorder == nil || p.buffer == nil || p.generation == 0 {
		return
	}
	buffer := p.buffer
	buffer.mu.Lock()
	if buffer.generation != p.generation || buffer.state != bufferPrepared {
		buffer.mu.Unlock()
		return
	}
	buffer.state = bufferFree
	buffer.input = Input{}
	buffer.mu.Unlock()
	p.recorder.free.put(buffer)
}

func (r *Recorder) Submit(in Input, hostNanos uint64) error {
	p, err := r.Prepare(in)
	if err != nil {
		return err
	}
	if err = p.Commit(hostNanos); err != nil {
		p.Abort()
	}
	return err
}

func (r *Recorder) fail(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.fatal == nil {
		r.fatal = err
		select {
		case r.errors <- err:
		default:
		}
	} else {
		// Preserve every independently observed I/O failure. Errors only carries
		// the first notification, while Err and Close retain the complete cause.
		r.fatal = errors.Join(r.fatal, err)
	}
	r.mu.Unlock()
}

func (r *Recorder) run(ctx context.Context) {
	var durability *syncWorker
	if sink, ok := r.sink.(concurrentSyncSink); ok && r.cfg.DurabilityWindow > 0 {
		durability = startSyncWorker(sink)
	}
	defer func() {
		if durability != nil {
			if err := durability.close(); err != nil {
				r.fail(fmt.Errorf("write celltape: %w", err))
			}
		}
		close(r.done)
	}()
	var prev []Cell
	var prevSeq uint64
	var keyScratch, runScratch, bitmapScratch []byte
	var compressor fastCompressor
	var headerScratch [ChunkHeaderBytes]byte
	var commitScratch [CommitFooterBytes]byte
	lastSync := time.Now()
	syncInFlight := false
	process := func(j job) error {
		defer r.recycle(j.buffer)
		in := j.buffer.input
		var err error
		keyScratch, err = encodeKeyframeInto(keyScratch, in)
		if err != nil {
			return err
		}
		raw := keyScratch
		kind := uint8(kindKeyframe)
		if !j.forceKey && len(prev) == len(in.Cells) {
			var delta []byte
			delta, runScratch, bitmapScratch, err = encodeDeltaInto(runScratch, bitmapScratch, prev, in.Cells, prevSeq)
			if err != nil {
				return err
			}
			if len(delta) < len(raw) {
				raw = delta
				kind = kindDelta
			}
		}
		if len(raw) > int(r.cfg.Limits.MaxChunkBytes) {
			return fmt.Errorf("encoded chunk exceeds limit")
		}
		stored, codec := compressor.compress(raw, r.cfg.Compression)
		m := chunkMeta{kind: kind, codec: codec, flags: in.Boundary, sequence: j.sequence, geometryEpoch: in.GeometryEpoch, configEpoch: in.ConfigEpoch, sourceNanos: in.SourceNanos, hostNanos: j.hostNanos, rawLen: uint32(len(raw)), storedLen: uint32(len(stored))}
		if err := writeFull(r.sink, marshalChunkHeaderInto(headerScratch[:0], m)); err != nil {
			return err
		}
		if err := writeFull(r.sink, stored); err != nil {
			return err
		}
		if err := writeFull(r.sink, marshalCommitInto(commitScratch[:0], j.sequence, stored)); err != nil {
			return err
		}
		prev = append(prev[:0], in.Cells...)
		prevSeq = j.sequence
		if r.cfg.DurabilityWindow == 0 || time.Since(lastSync) >= r.cfg.DurabilityWindow {
			if durability != nil {
				if !syncInFlight {
					syncInFlight = durability.requestSync()
				}
			} else {
				if err := r.sink.Sync(); err != nil {
					return err
				}
				lastSync = time.Now()
			}
		}
		return nil
	}
	drain := func() {
		for {
			select {
			case j := <-r.jobs:
				r.recycle(j.buffer)
			default:
				return
			}
		}
	}
	for {
		select {
		case j := <-r.jobs:
			if err := process(j); err != nil {
				r.fail(fmt.Errorf("write celltape: %w", err))
				drain()
				return
			}
		case err := <-syncResult(durability):
			syncInFlight = false
			if err != nil {
				r.fail(fmt.Errorf("write celltape: %w", err))
				drain()
				return
			}
			lastSync = time.Now()
		case <-ctx.Done():
			r.fail(ctx.Err())
			drain()
			return
		case <-r.stop:
			for {
				select {
				case j := <-r.jobs:
					if err := process(j); err != nil {
						r.fail(fmt.Errorf("write celltape: %w", err))
						drain()
						return
					}
				default:
					return
				}
			}
		}
	}
}

func syncResult(worker *syncWorker) <-chan error {
	if worker == nil {
		return nil
	}
	return worker.result
}

func (r *Recorder) recycle(buffer *jobBuffer) {
	if buffer == nil {
		return
	}
	buffer.mu.Lock()
	buffer.input = Input{}
	buffer.state = bufferFree
	buffer.mu.Unlock()
	r.free.put(buffer)
}

func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closing = true
		r.mu.Unlock()
		close(r.stop)
		<-r.done
		syncErr := r.sink.Sync()
		closeErr := r.sink.Close()
		r.closeErr = errors.Join(r.Err(), syncErr, closeErr)
	})
	return r.closeErr
}

func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// Publish atomically makes a closed staging tape visible without replacing an
// existing destination. Both paths must be in the same directory, which makes
// same-volume behavior explicit and checkable. On Windows the rename is also a
// write-through durability boundary.
func Publish(staging, final string) error {
	a, err := filepath.Abs(staging)
	if err != nil {
		return err
	}
	b, err := filepath.Abs(final)
	if err != nil {
		return err
	}
	if filepath.Clean(filepath.Dir(a)) != filepath.Clean(filepath.Dir(b)) {
		return errors.New("celltape publish requires staging and final in the same directory")
	}
	return publishTape(a, b)
}
