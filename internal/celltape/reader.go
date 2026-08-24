package celltape

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
)

type OpenOptions struct {
	Limits     Limits
	RepairTail bool
}

type Recovery struct {
	ValidBytes     int64
	ValidRecords   uint64
	DiscardedBytes int64
	TailError      error
}

// Summary is metadata collected while Open validates the committed prefix.
// Config is the first committed frame's configuration. No cell backing escapes
// the validation pass, so callers can plan recovery/export without replaying
// the tape merely to rediscover its shape and duration.
type Summary struct {
	Records                                         uint64
	FirstColumns, FirstRows                         uint32
	FirstGeometryEpoch, FirstConfigEpoch            uint64
	FirstSourceNanos, FirstHostNanos, LastHostNanos uint64
	Config                                          []byte
	VariableGeometry                                bool
	GeometryChanges                                 uint64
	MaxColumns, MaxRows, MaxCells                   uint32
}

// Replay is a deterministic cursor over the CRC-checked committed prefix.
type Replay struct {
	file     *os.File
	header   fileHeader
	recovery Recovery
	summary  Summary
	offset   int64
	prev     Frame
}

func Open(path string, opts OpenOptions) (*Replay, error) {
	return OpenContext(context.Background(), path, opts)
}

// OpenContext validates a tape's committed prefix while honoring cancellation.
// Cancellation is never treated as a damaged tail and therefore never repairs
// or truncates the file.
func OpenContext(ctx context.Context, path string, opts OpenOptions) (*Replay, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mode := os.O_RDONLY
	if opts.RepairTail {
		mode = os.O_RDWR
	}
	f, err := openReplayFile(path, mode)
	if err != nil {
		return nil, err
	}
	return OpenOwnedFileContext(ctx, f, opts)
}

// OpenOwnedFileContext validates an already-open tape and takes ownership of
// file. Replay.Close closes it. This lets a recovery claim use a duplicated
// handle without reopening mutable content by pathname.
func OpenOwnedFileContext(ctx context.Context, file *os.File, opts OpenOptions) (_ *Replay, err error) {
	if file == nil {
		return nil, errors.New("celltape: open file is required")
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, file.Close())
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < FileHeaderBytes {
		return nil, fmt.Errorf("%w: truncated file header", ErrCorrupt)
	}
	hb := make([]byte, FileHeaderBytes)
	if _, err = file.ReadAt(hb, 0); err != nil {
		return nil, err
	}
	h, err := parseFileHeader(hb, opts.Limits)
	if err != nil {
		return nil, err
	}
	recovery, summary, scanErr := scan(ctx, file, info.Size(), h)
	if scanErr != nil {
		return nil, scanErr
	}
	if opts.RepairTail && recovery.DiscardedBytes > 0 {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if err = file.Truncate(recovery.ValidBytes); err != nil {
			return nil, err
		}
		if err = file.Sync(); err != nil {
			return nil, err
		}
	}
	return &Replay{file: file, header: h, recovery: recovery, summary: summary, offset: FileHeaderBytes}, nil
}

func (r *Replay) Recovery() Recovery { return r.recovery }
func (r *Replay) Summary() Summary {
	if r == nil {
		return Summary{}
	}
	summary := r.summary
	summary.Config = append([]byte(nil), summary.Config...)
	return summary
}
func (r *Replay) Close() error {
	if r == nil || r.file == nil {
		return nil
	}
	return r.file.Close()
}
func (r *Replay) Rewind() {
	if r != nil {
		r.offset = FileHeaderBytes
		r.prev = Frame{}
	}
}

func (r *Replay) Next() (Frame, error) {
	f, err := r.nextBorrowed()
	if err != nil {
		return Frame{}, err
	}
	return cloneFrame(f), nil
}

func (r *Replay) nextBorrowed() (Frame, error) {
	if r == nil || r.file == nil {
		return Frame{}, ErrClosed
	}
	if r.offset >= r.recovery.ValidBytes {
		return Frame{}, io.EOF
	}
	f, n, err := readFrameAt(r.file, r.offset, r.header, r.prev)
	if err != nil {
		return Frame{}, err
	}
	r.offset += n
	r.prev = f
	return f, nil
}

// Iterate is the encoder-facing API. It includes duplicate holds and explicit boundaries.
func (r *Replay) Iterate(ctx context.Context, yield func(Frame) error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		f, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = yield(f); err != nil {
			return err
		}
	}
}

// IterateBorrowed avoids the defensive cell/config clone made by Next. Slice
// fields in the yielded Frame are read-only and valid only until yield returns.
// Callers that need to retain a frame must copy it themselves.
func (r *Replay) IterateBorrowed(ctx context.Context, yield func(Frame) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		f, err := r.nextBorrowed()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = yield(f); err != nil {
			return err
		}
	}
}

// IterateBorrowedIntervals yields each state with the next state's host time.
// The current frame is read-only and valid only until yield returns. hasNext is
// false for the final state.
func (r *Replay) IterateBorrowedIntervals(ctx context.Context, yield func(frame Frame, nextHostNanos uint64, hasNext bool) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := r.nextBorrowed()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		next, nextErr := r.nextBorrowed()
		if errors.Is(nextErr, io.EOF) {
			return yield(current, 0, false)
		}
		if nextErr != nil {
			return nextErr
		}
		if err = yield(current, next.HostNanos, true); err != nil {
			return err
		}
		current = next
	}
}

func scan(ctx context.Context, r io.ReaderAt, size int64, h fileHeader) (Recovery, Summary, error) {
	off := int64(FileHeaderBytes)
	var prev Frame
	var records uint64
	var summary Summary
	for off < size {
		if err := ctx.Err(); err != nil {
			return Recovery{}, Summary{}, err
		}
		frame, n, err := readFrameAt(r, off, h, prev)
		if err != nil {
			return Recovery{ValidBytes: off, ValidRecords: records, DiscardedBytes: size - off, TailError: err}, summary, nil
		}
		if records == 0 {
			summary.FirstColumns = frame.Columns
			summary.FirstRows = frame.Rows
			summary.FirstGeometryEpoch = frame.GeometryEpoch
			summary.FirstConfigEpoch = frame.ConfigEpoch
			summary.FirstSourceNanos = frame.SourceNanos
			summary.FirstHostNanos = frame.HostNanos
			summary.Config = append([]byte(nil), frame.Config...)
		} else if frame.Columns != prev.Columns || frame.Rows != prev.Rows {
			summary.VariableGeometry = true
			summary.GeometryChanges++
		}
		summary.Records++
		summary.LastHostNanos = frame.HostNanos
		summary.MaxColumns = max(summary.MaxColumns, frame.Columns)
		summary.MaxRows = max(summary.MaxRows, frame.Rows)
		summary.MaxCells = max(summary.MaxCells, frame.Columns*frame.Rows)
		prev, off, records = frame, off+n, records+1
	}
	return Recovery{ValidBytes: off, ValidRecords: records}, summary, nil
}

func readFrameAt(r io.ReaderAt, off int64, h fileHeader, prev Frame) (Frame, int64, error) {
	hb := make([]byte, ChunkHeaderBytes)
	if _, err := r.ReadAt(hb, off); err != nil {
		return Frame{}, 0, fmt.Errorf("%w: torn chunk header: %v", ErrCorrupt, err)
	}
	m, err := parseChunkHeader(hb, h.cfg.Limits.MaxChunkBytes)
	if err != nil {
		return Frame{}, 0, err
	}
	total := int64(ChunkHeaderBytes) + int64(m.storedLen) + CommitFooterBytes
	stored := make([]byte, int(m.storedLen))
	if _, err = r.ReadAt(stored, off+ChunkHeaderBytes); err != nil {
		return Frame{}, 0, fmt.Errorf("%w: torn chunk payload: %v", ErrCorrupt, err)
	}
	footer := make([]byte, CommitFooterBytes)
	if _, err = r.ReadAt(footer, off+ChunkHeaderBytes+int64(m.storedLen)); err != nil {
		return Frame{}, 0, fmt.Errorf("%w: missing commit footer: %v", ErrCorrupt, err)
	}
	if err = validateCommit(footer, m.sequence, stored); err != nil {
		return Frame{}, 0, err
	}
	raw, err := decompress(stored, m)
	if err != nil {
		return Frame{}, 0, err
	}
	frame, err := decodePayload(raw, m, h.cfg.Limits, prev)
	if err != nil {
		return Frame{}, 0, err
	}
	return frame, total, nil
}

func decodePayload(raw []byte, m chunkMeta, l Limits, prev Frame) (Frame, error) {
	if m.sequence == 0 || (prev.Sequence == 0 && m.sequence != 1) || (prev.Sequence != 0 && m.sequence != prev.Sequence+1) {
		return Frame{}, fmt.Errorf("%w: non-contiguous sequence", ErrCorrupt)
	}
	if prev.Sequence != 0 && (m.sourceNanos < prev.SourceNanos || m.hostNanos < prev.HostNanos) {
		return Frame{}, fmt.Errorf("%w: timestamp regression", ErrCorrupt)
	}
	f := Frame{Sequence: m.sequence, GeometryEpoch: m.geometryEpoch, ConfigEpoch: m.configEpoch, SourceNanos: m.sourceNanos, HostNanos: m.hostNanos, Boundary: m.flags}
	if m.kind == kindKeyframe {
		if len(raw) < 8 {
			return Frame{}, fmt.Errorf("%w: short keyframe", ErrCorrupt)
		}
		f.Columns, f.Rows = binary.LittleEndian.Uint32(raw[:4]), binary.LittleEndian.Uint32(raw[4:8])
		n, err := checkedCellCount(f.Columns, f.Rows, l)
		if err != nil {
			return Frame{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		at := 8
		configLen, err := readUvarint(raw, &at)
		if err != nil || configLen > uint64(l.MaxConfigBytes) || configLen > uint64(len(raw)-at) {
			return Frame{}, fmt.Errorf("%w: config length", ErrCorrupt)
		}
		f.Config = append([]byte(nil), raw[at:at+int(configLen)]...)
		at += int(configLen)
		count, err := readUvarint(raw, &at)
		if err != nil || count != uint64(n) || count > uint64((len(raw)-at)/PackedCellBytes) || at+int(count)*PackedCellBytes != len(raw) {
			return Frame{}, fmt.Errorf("%w: keyframe cell length", ErrCorrupt)
		}
		f.Cells = make([]Cell, int(count))
		for i := range f.Cells {
			f.Cells[i] = unpackCell(raw[at+i*PackedCellBytes:])
			if err := validateCell(f.Cells[i]); err != nil {
				return Frame{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
			}
		}
		if prev.Sequence != 0 {
			if f.GeometryEpoch < prev.GeometryEpoch || (f.GeometryEpoch == prev.GeometryEpoch && (f.Columns != prev.Columns || f.Rows != prev.Rows)) {
				return Frame{}, fmt.Errorf("%w: geometry epoch", ErrCorrupt)
			}
			if f.ConfigEpoch < prev.ConfigEpoch || (f.ConfigEpoch == prev.ConfigEpoch && !equalBytes(f.Config, prev.Config)) {
				return Frame{}, fmt.Errorf("%w: config epoch", ErrCorrupt)
			}
		}
	} else {
		if prev.Sequence == 0 || m.geometryEpoch != prev.GeometryEpoch || m.configEpoch != prev.ConfigEpoch || m.flags&BoundaryDiscontinuity != 0 {
			return Frame{}, fmt.Errorf("%w: invalid delta boundary", ErrCorrupt)
		}
		f.Columns, f.Rows = prev.Columns, prev.Rows
		f.Config = append([]byte(nil), prev.Config...)
		f.Cells = append([]Cell(nil), prev.Cells...)
		if err := applyDelta(raw, &f, prev.Sequence); err != nil {
			return Frame{}, err
		}
	}
	return f, nil
}

func applyDelta(raw []byte, f *Frame, baseSequence uint64) error {
	if len(raw) < 1 {
		return fmt.Errorf("%w: short delta", ErrCorrupt)
	}
	mode, at := raw[0], 1
	base, err := readUvarint(raw, &at)
	if err != nil {
		return err
	}
	count, err := readUvarint(raw, &at)
	if err != nil {
		return err
	}
	changed, err := readUvarint(raw, &at)
	if err != nil {
		return err
	}
	if base != baseSequence || count != uint64(len(f.Cells)) || changed > count {
		return fmt.Errorf("%w: delta base/count", ErrCorrupt)
	}
	switch mode {
	case deltaRuns:
		idx, seen := 0, uint64(0)
		for seen < changed {
			skip, e := readUvarint(raw, &at)
			if e != nil {
				return e
			}
			run, e := readUvarint(raw, &at)
			if e != nil {
				return e
			}
			if run == 0 || skip > uint64(len(f.Cells)-idx) {
				return fmt.Errorf("%w: delta run", ErrCorrupt)
			}
			idx += int(skip)
			if run > uint64(len(f.Cells)-idx) || run > uint64((len(raw)-at)/PackedCellBytes) {
				return fmt.Errorf("%w: delta run length", ErrCorrupt)
			}
			for i := 0; i < int(run); i++ {
				f.Cells[idx+i] = unpackCell(raw[at+i*PackedCellBytes:])
				if err := validateCell(f.Cells[idx+i]); err != nil {
					return fmt.Errorf("%w: %v", ErrCorrupt, err)
				}
			}
			idx += int(run)
			at += int(run) * PackedCellBytes
			seen += run
			if seen > changed {
				return fmt.Errorf("%w: delta changed count", ErrCorrupt)
			}
		}
	case deltaBitmap:
		bmLen := (len(f.Cells) + 7) / 8
		if bmLen > len(raw)-at {
			return fmt.Errorf("%w: bitmap length", ErrCorrupt)
		}
		bm := raw[at : at+bmLen]
		at += bmLen
		if len(f.Cells)&7 != 0 && bmLen > 0 && bm[bmLen-1]&^byte((1<<uint(len(f.Cells)&7))-1) != 0 {
			return fmt.Errorf("%w: bitmap padding", ErrCorrupt)
		}
		seen := 0
		for _, v := range bm {
			seen += bits.OnesCount8(v)
		}
		if uint64(seen) != changed || seen > (len(raw)-at)/PackedCellBytes {
			return fmt.Errorf("%w: bitmap changed count", ErrCorrupt)
		}
		n := 0
		for i := range f.Cells {
			if bm[i/8]&(1<<uint(i&7)) != 0 {
				f.Cells[i] = unpackCell(raw[at+n*PackedCellBytes:])
				if err := validateCell(f.Cells[i]); err != nil {
					return fmt.Errorf("%w: %v", ErrCorrupt, err)
				}
				n++
			}
		}
		at += seen * PackedCellBytes
	default:
		return fmt.Errorf("%w: unknown delta mode", ErrCorrupt)
	}
	if at != len(raw) {
		return fmt.Errorf("%w: trailing delta bytes", ErrCorrupt)
	}
	return nil
}

func cloneFrame(f Frame) Frame {
	f.Config = append([]byte(nil), f.Config...)
	f.Cells = append([]Cell(nil), f.Cells...)
	return f
}
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
