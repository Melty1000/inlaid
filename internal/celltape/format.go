package celltape

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"time"
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

var fileMagic = [8]byte{'C', 'E', 'L', 'L', 'T', 'A', 'P', 'E'}
var chunkMagic = [4]byte{'C', 'T', 'C', 'H'}
var commitMagic = [4]byte{'C', 'T', 'O', 'K'}

const (
	kindKeyframe = 1
	kindDelta    = 2
	codecRaw     = 0
	codecFlate   = 1
	deltaRuns    = 1
	deltaBitmap  = 2
)

type fileHeader struct{ cfg Config }

func marshalFileHeader(cfg Config) []byte {
	b := make([]byte, FileHeaderBytes)
	copy(b, fileMagic[:])
	binary.LittleEndian.PutUint16(b[8:10], FormatVersion)
	binary.LittleEndian.PutUint16(b[10:12], FileHeaderBytes)
	binary.LittleEndian.PutUint32(b[16:20], cfg.Limits.MaxColumns)
	binary.LittleEndian.PutUint32(b[20:24], cfg.Limits.MaxRows)
	binary.LittleEndian.PutUint32(b[24:28], cfg.Limits.MaxCells)
	binary.LittleEndian.PutUint32(b[28:32], cfg.Limits.MaxConfigBytes)
	binary.LittleEndian.PutUint32(b[32:36], cfg.Limits.MaxChunkBytes)
	binary.LittleEndian.PutUint32(b[36:40], cfg.KeyframeInterval)
	binary.LittleEndian.PutUint32(b[40:44], uint32(cfg.QueueCapacity))
	binary.LittleEndian.PutUint32(b[44:48], PackedCellBytes)
	binary.LittleEndian.PutUint64(b[48:56], uint64(cfg.DurabilityWindow))
	binary.LittleEndian.PutUint32(b[60:64], crc32.Checksum(b[:60], castagnoli))
	return b
}

func parseFileHeader(b []byte, readerLimits Limits) (fileHeader, error) {
	if len(b) != FileHeaderBytes {
		return fileHeader{}, fmt.Errorf("%w: truncated file header", ErrCorrupt)
	}
	if !bytes.Equal(b[:8], fileMagic[:]) {
		return fileHeader{}, fmt.Errorf("%w: bad file magic", ErrCorrupt)
	}
	if binary.LittleEndian.Uint16(b[8:10]) != FormatVersion {
		return fileHeader{}, fmt.Errorf("%w: version %d", ErrUnsupported, binary.LittleEndian.Uint16(b[8:10]))
	}
	if binary.LittleEndian.Uint16(b[10:12]) != FileHeaderBytes || binary.LittleEndian.Uint32(b[12:16]) != 0 || binary.LittleEndian.Uint32(b[56:60]) != 0 {
		return fileHeader{}, fmt.Errorf("%w: invalid file header fields", ErrCorrupt)
	}
	if crc32.Checksum(b[:60], castagnoli) != binary.LittleEndian.Uint32(b[60:64]) {
		return fileHeader{}, fmt.Errorf("%w: file header CRC32C", ErrCorrupt)
	}
	cfg := Config{Limits: Limits{
		MaxColumns: binary.LittleEndian.Uint32(b[16:20]), MaxRows: binary.LittleEndian.Uint32(b[20:24]), MaxCells: binary.LittleEndian.Uint32(b[24:28]),
		MaxConfigBytes: binary.LittleEndian.Uint32(b[28:32]), MaxChunkBytes: binary.LittleEndian.Uint32(b[32:36]),
	}, KeyframeInterval: binary.LittleEndian.Uint32(b[36:40]), QueueCapacity: int(binary.LittleEndian.Uint32(b[40:44])), DurabilityWindow: timeDuration(binary.LittleEndian.Uint64(b[48:56]))}
	if binary.LittleEndian.Uint32(b[44:48]) != PackedCellBytes {
		return fileHeader{}, fmt.Errorf("%w: cell width", ErrUnsupported)
	}
	var err error
	cfg, err = cfg.normalized()
	if err != nil {
		return fileHeader{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	r, err := readerLimits.normalized()
	if err != nil {
		return fileHeader{}, err
	}
	if cfg.Limits.MaxColumns > r.MaxColumns || cfg.Limits.MaxRows > r.MaxRows || cfg.Limits.MaxCells > r.MaxCells || cfg.Limits.MaxConfigBytes > r.MaxConfigBytes || cfg.Limits.MaxChunkBytes > r.MaxChunkBytes {
		return fileHeader{}, fmt.Errorf("%w: declared limits exceed reader limits", ErrCorrupt)
	}
	return fileHeader{cfg: cfg}, nil
}

func timeDuration(v uint64) time.Duration {
	if v > uint64(^uint64(0)>>1) {
		return time.Duration(^uint64(0) >> 1)
	}
	return time.Duration(v)
}

type chunkMeta struct {
	kind, codec                                                  uint8
	flags                                                        BoundaryFlags
	sequence, geometryEpoch, configEpoch, sourceNanos, hostNanos uint64
	rawLen, storedLen                                            uint32
}

func marshalChunkHeader(m chunkMeta) []byte {
	return marshalChunkHeaderInto(nil, m)
}

func marshalChunkHeaderInto(b []byte, m chunkMeta) []byte {
	if cap(b) < ChunkHeaderBytes {
		b = make([]byte, ChunkHeaderBytes)
	} else {
		b = b[:ChunkHeaderBytes]
		clear(b)
	}
	copy(b[:4], chunkMagic[:])
	binary.LittleEndian.PutUint16(b[4:6], ChunkHeaderBytes)
	b[6] = FormatVersion
	b[7] = m.kind
	b[8] = m.codec
	b[9] = byte(m.flags)
	binary.LittleEndian.PutUint64(b[12:20], m.sequence)
	binary.LittleEndian.PutUint64(b[20:28], m.geometryEpoch)
	binary.LittleEndian.PutUint64(b[28:36], m.configEpoch)
	binary.LittleEndian.PutUint64(b[36:44], m.sourceNanos)
	binary.LittleEndian.PutUint64(b[44:52], m.hostNanos)
	binary.LittleEndian.PutUint32(b[52:56], m.rawLen)
	binary.LittleEndian.PutUint32(b[56:60], m.storedLen)
	binary.LittleEndian.PutUint32(b[60:64], crc32.Checksum(b[:60], castagnoli))
	return b
}

func parseChunkHeader(b []byte, max uint32) (chunkMeta, error) {
	var m chunkMeta
	if len(b) != ChunkHeaderBytes || !bytes.Equal(b[:4], chunkMagic[:]) {
		return m, fmt.Errorf("%w: bad chunk header", ErrCorrupt)
	}
	if binary.LittleEndian.Uint16(b[4:6]) != ChunkHeaderBytes || b[6] != FormatVersion || b[10] != 0 || b[11] != 0 {
		return m, fmt.Errorf("%w: unsupported chunk header", ErrCorrupt)
	}
	if crc32.Checksum(b[:60], castagnoli) != binary.LittleEndian.Uint32(b[60:64]) {
		return m, fmt.Errorf("%w: chunk header CRC32C", ErrCorrupt)
	}
	m = chunkMeta{kind: b[7], codec: b[8], flags: BoundaryFlags(b[9]), sequence: binary.LittleEndian.Uint64(b[12:20]), geometryEpoch: binary.LittleEndian.Uint64(b[20:28]), configEpoch: binary.LittleEndian.Uint64(b[28:36]), sourceNanos: binary.LittleEndian.Uint64(b[36:44]), hostNanos: binary.LittleEndian.Uint64(b[44:52]), rawLen: binary.LittleEndian.Uint32(b[52:56]), storedLen: binary.LittleEndian.Uint32(b[56:60])}
	if (m.kind != kindKeyframe && m.kind != kindDelta) || (m.codec != codecRaw && m.codec != codecFlate) || m.flags & ^(BoundaryGap|BoundaryDiscontinuity) != 0 || m.rawLen > max || m.storedLen > max || m.rawLen == 0 || m.storedLen == 0 || (m.codec == codecRaw && m.rawLen != m.storedLen) {
		return chunkMeta{}, fmt.Errorf("%w: invalid or oversized chunk fields", ErrCorrupt)
	}
	return m, nil
}

func marshalCommit(sequence uint64, stored []byte) []byte {
	return marshalCommitInto(nil, sequence, stored)
}

func marshalCommitInto(b []byte, sequence uint64, stored []byte) []byte {
	if cap(b) < CommitFooterBytes {
		b = make([]byte, CommitFooterBytes)
	} else {
		b = b[:CommitFooterBytes]
		clear(b)
	}
	copy(b[:4], commitMagic[:])
	binary.LittleEndian.PutUint64(b[4:12], sequence)
	binary.LittleEndian.PutUint32(b[12:16], uint32(ChunkHeaderBytes+len(stored)+CommitFooterBytes))
	binary.LittleEndian.PutUint32(b[16:20], crc32.Checksum(stored, castagnoli))
	binary.LittleEndian.PutUint32(b[20:24], crc32.Checksum(b[:20], castagnoli))
	return b
}

func validateCommit(b []byte, sequence uint64, stored []byte) error {
	if len(b) != CommitFooterBytes || !bytes.Equal(b[:4], commitMagic[:]) || binary.LittleEndian.Uint64(b[4:12]) != sequence || binary.LittleEndian.Uint32(b[12:16]) != uint32(ChunkHeaderBytes+len(stored)+CommitFooterBytes) || binary.LittleEndian.Uint32(b[16:20]) != crc32.Checksum(stored, castagnoli) || binary.LittleEndian.Uint32(b[20:24]) != crc32.Checksum(b[:20], castagnoli) {
		return fmt.Errorf("%w: invalid chunk commit footer", ErrCorrupt)
	}
	return nil
}

func packCell(dst []byte, c Cell) error {
	if err := validateCell(c); err != nil {
		return err
	}
	dst[0] = c.Mask
	dst[1] = byte(c.FG >> 16)
	dst[2] = byte(c.FG >> 8)
	dst[3] = byte(c.FG)
	dst[4] = byte(c.BG >> 16)
	dst[5] = byte(c.BG >> 8)
	dst[6] = byte(c.BG)
	return nil
}

func unpackCell(src []byte) Cell {
	return Cell{Mask: src[0], FG: RGB(src[1])<<16 | RGB(src[2])<<8 | RGB(src[3]), BG: RGB(src[4])<<16 | RGB(src[5])<<8 | RGB(src[6])}
}

func appendUvarint(b []byte, v uint64) []byte {
	var scratch [10]byte
	n := binary.PutUvarint(scratch[:], v)
	return append(b, scratch[:n]...)
}

func readUvarint(b []byte, at *int) (uint64, error) {
	if *at >= len(b) {
		return 0, io.ErrUnexpectedEOF
	}
	start := *at
	v, n := binary.Uvarint(b[start:])
	if n <= 0 {
		return 0, fmt.Errorf("%w: invalid varint", ErrCorrupt)
	}
	var canonical [10]byte
	if binary.PutUvarint(canonical[:], v) != n || !bytes.Equal(canonical[:n], b[start:start+n]) {
		return 0, fmt.Errorf("%w: non-canonical varint", ErrCorrupt)
	}
	*at += n
	return v, nil
}

func encodeKeyframe(in Input) ([]byte, error) {
	return encodeKeyframeInto(nil, in)
}

func encodeKeyframeInto(b []byte, in Input) ([]byte, error) {
	b = append(b[:0], make([]byte, 8)...)
	binary.LittleEndian.PutUint32(b[0:4], in.Columns)
	binary.LittleEndian.PutUint32(b[4:8], in.Rows)
	b = appendUvarint(b, uint64(len(in.Config)))
	b = append(b, in.Config...)
	b = appendUvarint(b, uint64(len(in.Cells)))
	start := len(b)
	b = append(b, make([]byte, len(in.Cells)*PackedCellBytes)...)
	for i, c := range in.Cells {
		if err := packCell(b[start+i*PackedCellBytes:], c); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func encodeDelta(prev, next []Cell, base uint64) ([]byte, error) {
	encoded, _, _, err := encodeDeltaInto(nil, nil, prev, next, base)
	return encoded, err
}

func encodeDeltaInto(runs, bitmap []byte, prev, next []Cell, base uint64) (encoded, runsBuffer, bitmapBuffer []byte, err error) {
	runs = append(runs[:0], 0)
	runs[0] = deltaRuns
	runs = appendUvarint(runs, base)
	runs = appendUvarint(runs, uint64(len(next)))
	changed := 0
	for i := range next {
		if next[i] != prev[i] {
			changed++
		}
	}
	runs = appendUvarint(runs, uint64(changed))
	for i := 0; i < len(next); {
		skip := 0
		for i < len(next) && next[i] == prev[i] {
			i++
			skip++
		}
		if i == len(next) {
			break
		}
		start := i
		for i < len(next) && next[i] != prev[i] {
			i++
		}
		runs = appendUvarint(runs, uint64(skip))
		runs = appendUvarint(runs, uint64(i-start))
		off := len(runs)
		runs = append(runs, make([]byte, (i-start)*PackedCellBytes)...)
		for j := start; j < i; j++ {
			if err := packCell(runs[off+(j-start)*PackedCellBytes:], next[j]); err != nil {
				return nil, runs, bitmap, err
			}
		}
	}
	bitmap = append(bitmap[:0], 0)
	bitmap[0] = deltaBitmap
	bitmap = appendUvarint(bitmap, base)
	bitmap = appendUvarint(bitmap, uint64(len(next)))
	bitmap = appendUvarint(bitmap, uint64(changed))
	bmStart := len(bitmap)
	bitmap = append(bitmap, make([]byte, (len(next)+7)/8)...)
	cellsStart := len(bitmap)
	bitmap = append(bitmap, make([]byte, changed*PackedCellBytes)...)
	n := 0
	for i, c := range next {
		if c != prev[i] {
			bitmap[bmStart+i/8] |= 1 << uint(i&7)
			if err := packCell(bitmap[cellsStart+n*PackedCellBytes:], c); err != nil {
				return nil, runs, bitmap, err
			}
			n++
		}
	}
	if len(bitmap) < len(runs) {
		return bitmap, runs, bitmap, nil
	}
	return runs, runs, bitmap, nil
}

type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}

func maybeCompress(raw []byte, mode Compression) ([]byte, uint8) {
	var compressor fastCompressor
	return compressor.compress(raw, mode)
}

type fastCompressor struct {
	buffer cappedBuffer
	writer *flate.Writer
}

func (c *fastCompressor) compress(raw []byte, mode Compression) ([]byte, uint8) {
	if mode != CompressionFast || len(raw) < 64 {
		return raw, codecRaw
	}
	c.buffer.Reset()
	c.buffer.max = len(raw) - 1
	if c.writer == nil {
		writer, err := flate.NewWriter(&c.buffer, flate.BestSpeed)
		if err != nil {
			return raw, codecRaw
		}
		c.writer = writer
	} else {
		c.writer.Reset(&c.buffer)
	}
	_, e1 := c.writer.Write(raw)
	e2 := c.writer.Close()
	if e1 != nil || e2 != nil || c.buffer.Len() >= len(raw) {
		return raw, codecRaw
	}
	return c.buffer.Bytes(), codecFlate
}

func decompress(stored []byte, m chunkMeta) ([]byte, error) {
	if m.codec == codecRaw {
		return append([]byte(nil), stored...), nil
	}
	r := flate.NewReader(bytes.NewReader(stored))
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, int64(m.rawLen)+1))
	if err != nil || len(out) != int(m.rawLen) {
		return nil, fmt.Errorf("%w: compressed payload length", ErrCorrupt)
	}
	return out, nil
}
