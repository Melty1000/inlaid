package celltape

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	FormatVersion     = 1
	PackedCellBytes   = 7
	FileHeaderBytes   = 64
	ChunkHeaderBytes  = 64
	CommitFooterBytes = 24
)

var (
	ErrClosed           = errors.New("celltape recorder is closed")
	ErrQueueSaturated   = errors.New("celltape producer queue saturated")
	ErrPreparedDone     = errors.New("celltape prepared state is already committed or aborted")
	ErrTimingRegression = errors.New("celltape timestamp regressed")
	ErrEpochRegression  = errors.New("celltape epoch regressed or changed without advancing")
	ErrCorrupt          = errors.New("corrupt celltape")
	ErrUnsupported      = errors.New("unsupported celltape format")
)

// RGB is a canonical 24-bit 0xRRGGBB color.
type RGB uint32

// Cell is the stable seven-byte wire cell: mask, foreground RGB24, background
// RGB24. Values above 24 bits are rejected.
type Cell struct {
	Mask uint8
	FG   RGB
	BG   RGB
}

func CellFromPacked(v uint64) (Cell, error) {
	if v>>52 != 0 {
		return Cell{}, fmt.Errorf("packed cell has reserved bits set")
	}
	c := Cell{Mask: uint8(v & 0x0f), FG: RGB((v >> 4) & 0xffffff), BG: RGB((v >> 28) & 0xffffff)}
	if err := validateCell(c); err != nil {
		return Cell{}, err
	}
	return c, nil
}

func (c Cell) Packed() uint64 {
	return uint64(c.Mask&0x0f) | uint64(c.FG&0xffffff)<<4 | uint64(c.BG&0xffffff)<<28
}

type BoundaryFlags uint8

const (
	BoundaryGap BoundaryFlags = 1 << iota
	BoundaryDiscontinuity
)

// Input is a prepared terminal state. SourceNanos is a monotonic source-clock
// offset, not wall time. Host acceptance time is supplied only to Commit.
type Input struct {
	GeometryEpoch uint64
	ConfigEpoch   uint64
	Columns       uint32
	Rows          uint32
	Config        []byte
	Cells         []Cell
	SourceNanos   uint64
	Boundary      BoundaryFlags
}

type Frame struct {
	Sequence      uint64
	GeometryEpoch uint64
	ConfigEpoch   uint64
	Columns       uint32
	Rows          uint32
	Config        []byte
	Cells         []Cell
	SourceNanos   uint64
	HostNanos     uint64
	Boundary      BoundaryFlags
}

type Compression uint8

const (
	CompressionNone Compression = iota
	CompressionFast
)

type Limits struct {
	MaxColumns     uint32
	MaxRows        uint32
	MaxCells       uint32
	MaxConfigBytes uint32
	MaxChunkBytes  uint32
}

func DefaultLimits() Limits {
	return Limits{MaxColumns: 4096, MaxRows: 4096, MaxCells: 4 * 1024 * 1024, MaxConfigBytes: 64 * 1024, MaxChunkBytes: 64 * 1024 * 1024}
}

func (l Limits) normalized() (Limits, error) {
	d := DefaultLimits()
	if l.MaxColumns == 0 {
		l.MaxColumns = d.MaxColumns
	}
	if l.MaxRows == 0 {
		l.MaxRows = d.MaxRows
	}
	if l.MaxCells == 0 {
		l.MaxCells = d.MaxCells
	}
	if l.MaxConfigBytes == 0 {
		l.MaxConfigBytes = d.MaxConfigBytes
	}
	if l.MaxChunkBytes == 0 {
		l.MaxChunkBytes = d.MaxChunkBytes
	}
	if l.MaxColumns > 1<<20 || l.MaxRows > 1<<20 || l.MaxCells > 1<<28 || l.MaxConfigBytes > 16<<20 || l.MaxChunkBytes > 1<<30 {
		return Limits{}, fmt.Errorf("celltape limits exceed hard safety caps")
	}
	return l, nil
}

type Config struct {
	Limits           Limits
	QueueCapacity    int
	KeyframeInterval uint32
	DurabilityWindow time.Duration
	Compression      Compression
}

func (c Config) normalized() (Config, error) {
	var err error
	c.Limits, err = c.Limits.normalized()
	if err != nil {
		return Config{}, err
	}
	if c.QueueCapacity == 0 {
		c.QueueCapacity = 8
	}
	if c.QueueCapacity < 1 || c.QueueCapacity > 65536 {
		return Config{}, fmt.Errorf("queue capacity must be 1..65536")
	}
	if c.KeyframeInterval == 0 {
		c.KeyframeInterval = 120
	}
	if c.KeyframeInterval > 1<<20 {
		return Config{}, fmt.Errorf("keyframe interval is too large")
	}
	if c.DurabilityWindow < 0 || c.DurabilityWindow > time.Minute {
		return Config{}, fmt.Errorf("durability window must be between 0 and 1 minute")
	}
	if c.Compression > CompressionFast {
		return Config{}, fmt.Errorf("unknown compression mode")
	}
	return c, nil
}

type SizeReport struct {
	WorstFrameBytes  uint64
	WorstBytesHour   uint64
	DurabilityWindow time.Duration
	QueueCapacity    int
}

// Preflight returns a hard upper bound. The writer chooses a keyframe whenever
// a delta would be larger, so compression can only reduce this bound.
func Preflight(columns, rows, configBytes, fps uint32, cfg Config) (SizeReport, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return SizeReport{}, err
	}
	count, err := checkedCellCount(columns, rows, cfg.Limits)
	if err != nil {
		return SizeReport{}, err
	}
	if configBytes > cfg.Limits.MaxConfigBytes {
		return SizeReport{}, fmt.Errorf("config length exceeds limit")
	}
	if fps == 0 || fps > 1000 {
		return SizeReport{}, fmt.Errorf("FPS must be 1..1000")
	}
	payload := uint64(8+10+configBytes+10) + uint64(count)*PackedCellBytes
	frameBytes := uint64(ChunkHeaderBytes+CommitFooterBytes) + payload
	if payload > uint64(cfg.Limits.MaxChunkBytes) {
		return SizeReport{}, fmt.Errorf("keyframe exceeds maximum chunk length")
	}
	framesHour, ok := mul64(uint64(fps), 3600)
	if !ok {
		return SizeReport{}, fmt.Errorf("FPS size estimate overflow")
	}
	hour, ok := mul64(frameBytes, framesHour)
	if !ok || hour > math.MaxUint64-FileHeaderBytes {
		return SizeReport{}, fmt.Errorf("hourly size estimate overflow")
	}
	return SizeReport{WorstFrameBytes: frameBytes, WorstBytesHour: hour + FileHeaderBytes, DurabilityWindow: cfg.DurabilityWindow, QueueCapacity: cfg.QueueCapacity}, nil
}

func validateCell(c Cell) error {
	if c.Mask > 7 {
		return fmt.Errorf("cell mask %d is not canonical", c.Mask)
	}
	if c.FG > 0xffffff || c.BG > 0xffffff {
		return fmt.Errorf("cell RGB exceeds 24 bits")
	}
	if (c.Mask == 0) != (c.FG == c.BG) {
		return fmt.Errorf("cell mask/color pair is not canonical")
	}
	return nil
}

func checkedCellCount(columns, rows uint32, l Limits) (uint32, error) {
	if columns == 0 || rows == 0 || columns > l.MaxColumns || rows > l.MaxRows {
		return 0, fmt.Errorf("geometry %dx%d is outside limits", columns, rows)
	}
	n := uint64(columns) * uint64(rows)
	if n > uint64(l.MaxCells) {
		return 0, fmt.Errorf("cell count %d exceeds limit", n)
	}
	return uint32(n), nil
}

func mul64(a, b uint64) (uint64, bool) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, false
	}
	return a * b, true
}

type Sink interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

// New constructs a recorder on a caller-owned sink. Create is the usual file API.
func New(ctx context.Context, sink Sink, cfg Config) (*Recorder, error) {
	return newRecorder(ctx, sink, cfg)
}
