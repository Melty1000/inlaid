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

// Replay is a deterministic cursor over the CRC-checked committed prefix.
type Replay struct {
	file     *os.File
	header   fileHeader
	recovery Recovery
	offset   int64
	prev     Frame
}

func Open(path string, opts OpenOptions) (*Replay, error) {
	mode := os.O_RDONLY
	if opts.RepairTail {
		mode = os.O_RDWR
	}
	f, err := os.OpenFile(path, mode, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.Size() < FileHeaderBytes {
		f.Close()
		return nil, fmt.Errorf("%w: truncated file header", ErrCorrupt)
	}
	hb := make([]byte, FileHeaderBytes)
	if _, err = f.ReadAt(hb, 0); err != nil {
		f.Close()
		return nil, err
	}
	h, err := parseFileHeader(hb, opts.Limits)
	if err != nil {
		f.Close()
		return nil, err
	}
	recovery := scan(f, info.Size(), h)
	if opts.RepairTail && recovery.DiscardedBytes > 0 {
		if err = f.Truncate(recovery.ValidBytes); err != nil {
			f.Close()
			return nil, err
		}
		if err = f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
	}
	return &Replay{file: f, header: h, recovery: recovery, offset: FileHeaderBytes}, nil
}

func (r *Replay) Recovery() Recovery { return r.recovery }
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
	return cloneFrame(f), nil
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

func scan(r io.ReaderAt, size int64, h fileHeader) Recovery {
	off := int64(FileHeaderBytes)
	var prev Frame
	var records uint64
	for off < size {
		frame, n, err := readFrameAt(r, off, h, prev)
		if err != nil {
			return Recovery{ValidBytes: off, ValidRecords: records, DiscardedBytes: size - off, TailError: err}
		}
		prev, off, records = frame, off+n, records+1
	}
	return Recovery{ValidBytes: off, ValidRecords: records}
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
