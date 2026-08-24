// Package cellframe defines the canonical, renderer-independent terminal-cell
// representation used between image reduction and output encoders.
package cellframe

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	cellMaskBits = uint64(0x0f)
	colorMask    = uint64(0x00ffffff)
	cellBits     = uint64(52)
	cellMask     = uint64(1)<<cellBits - 1

	// PackedCellBytes is the minimum fixed wire width of Cell.Packed().
	PackedCellBytes = 7
)

var (
	// ErrFramePoolExhausted means every preallocated output frame is still
	// leased. Release an old frame (or configure more buffers) and retry.
	ErrFramePoolExhausted = errors.New("cellframe: output frame pool exhausted")
	// ErrReleasedFrame means an operation requiring ownership was attempted
	// after the final Release.
	ErrReleasedFrame = errors.New("cellframe: frame lease has been released")
)

// RGB is an immutable packed 24-bit sRGB color in 0xRRGGBB order.
type RGB uint32

// ColorTransform maps one normalized 8-bit sRGB code value to another. It is
// called only after the solver has selected a cell's spatial mask and colors.
// Implementations used by a Solver must be deterministic, safe for concurrent
// calls, and must not retain or mutate caller-owned state.
type ColorTransform interface {
	TransformRGB(RGB) RGB
}

// NewRGB constructs a packed color.
func NewRGB(r, g, b uint8) RGB {
	return RGB(uint32(r)<<16 | uint32(g)<<8 | uint32(b))
}

// RGBFromPacked constructs a color from the low 24 bits of packed.
func RGBFromPacked(packed uint32) RGB { return RGB(packed & uint32(colorMask)) }

// Packed returns the color as 0xRRGGBB.
func (c RGB) Packed() uint32 { return uint32(c) & uint32(colorMask) }

// R returns the red channel.
func (c RGB) R() uint8 { return uint8(c.Packed() >> 16) }

// G returns the green channel.
func (c RGB) G() uint8 { return uint8(c.Packed() >> 8) }

// B returns the blue channel.
func (c RGB) B() uint8 { return uint8(c.Packed()) }

// Cell is an immutable canonical 2x2-quadrant terminal cell. Bits in Mask use
// this order:
//
//	bit 0  top-left       bit 1  top-right
//	bit 2  bottom-left    bit 3  bottom-right
//
// Set quadrants use Foreground and clear quadrants use Background. A cell and
// its complemented mask with swapped colors are visually identical, so Cell
// stores only the representative whose bottom-right bit is clear (masks 0..7).
// Mask 0 and equal-color cells have one unique representation.
//
// Packed layout, least-significant bit first, is mask:4, foreground:24,
// background:24. The upper 12 bits are zero.
type Cell uint64

// NewCell constructs the unique canonical representation of a visual cell.
func NewCell(mask uint8, foreground, background RGB) Cell {
	mask &= 0x0f
	foreground = RGBFromPacked(foreground.Packed())
	background = RGBFromPacked(background.Packed())
	if mask&0x08 != 0 {
		mask ^= 0x0f
		foreground, background = background, foreground
	}
	if mask == 0 {
		foreground = background
	} else if foreground == background {
		mask = 0
		background = foreground
	}
	return Cell(uint64(mask) |
		uint64(foreground.Packed())<<4 |
		uint64(background.Packed())<<28)
}

// CellFromPacked validates the packed width and canonicalizes the represented
// visual cell. It accepts complement-form encodings for robust tape decoding.
func CellFromPacked(packed uint64) (Cell, error) {
	if packed&^cellMask != 0 {
		return 0, errors.New("cellframe: packed cell uses reserved bits")
	}
	mask := uint8(packed & cellMaskBits)
	fg := RGBFromPacked(uint32((packed >> 4) & colorMask))
	bg := RGBFromPacked(uint32((packed >> 28) & colorMask))
	return NewCell(mask, fg, bg), nil
}

// Packed returns the stable 52-bit wire representation.
func (c Cell) Packed() uint64 { return uint64(c) & cellMask }

// Mask returns the canonical quadrant mask in the range 0..7.
func (c Cell) Mask() uint8 { return uint8(c.Packed() & cellMaskBits) }

// Foreground returns the color used by set mask bits.
func (c Cell) Foreground() RGB {
	return RGBFromPacked(uint32((c.Packed() >> 4) & colorMask))
}

// Background returns the color used by clear mask bits.
func (c Cell) Background() RGB {
	return RGBFromPacked(uint32((c.Packed() >> 28) & colorMask))
}

// IsCanonical reports whether c is already the unique packed representation.
func (c Cell) IsCanonical() bool {
	if uint64(c)&^cellMask != 0 || c.Mask() > 7 {
		return false
	}
	return c == NewCell(c.Mask(), c.Foreground(), c.Background())
}

// QuadrantRune returns the Unicode glyph for the canonical mask. An encoder
// must set both truecolor foreground and background before writing the rune.
func (c Cell) QuadrantRune() rune {
	return [...]rune{' ', '▘', '▝', '▀', '▖', '▌', '▞', '▛'}[c.Mask()]
}

// ColorAt returns the reconstructed color for quadrant 0..3. For an invalid
// quadrant it returns the background color.
func (c Cell) ColorAt(quadrant int) RGB {
	if quadrant >= 0 && quadrant < 4 && c.Mask()&(1<<quadrant) != 0 {
		return c.Foreground()
	}
	return c.Background()
}

// SourceMeta identifies the source image and its presentation timestamp.
type SourceMeta struct {
	GeometryEpoch  uint64
	SourceSequence uint64
	PTS            time.Duration
}

// CellFrame is practically immutable: metadata and cells cannot be mutated
// through the public API while its lease is live. Solver frames use a bounded
// preallocated pool. Retain adds an owner; every owner must call Release. No
// method except Valid may be used after the final Release.
type CellFrame struct {
	pool *framePool
	refs atomic.Int32

	meta    SourceMeta
	columns int
	rows    int
	cells   []Cell
}

type framePool struct {
	mu      sync.Mutex
	frames  []CellFrame
	storage []Cell
	free    []*CellFrame
}

func newFramePool(columns, rows, buffers int) *framePool {
	cellCount := columns * rows
	p := &framePool{
		frames:  make([]CellFrame, buffers),
		storage: make([]Cell, cellCount*buffers),
		free:    make([]*CellFrame, buffers),
	}
	for i := range p.frames {
		f := &p.frames[i]
		f.pool = p
		f.columns = columns
		f.rows = rows
		f.cells = p.storage[i*cellCount : (i+1)*cellCount]
		p.free[i] = f
	}
	return p
}

func (p *framePool) acquire() *CellFrame {
	p.mu.Lock()
	last := len(p.free) - 1
	if last < 0 {
		p.mu.Unlock()
		return nil
	}
	f := p.free[last]
	p.free[last] = nil
	p.free = p.free[:last]
	p.mu.Unlock()
	f.refs.Store(1)
	return f
}

func (p *framePool) recycle(f *CellFrame) {
	p.mu.Lock()
	p.free = append(p.free, f)
	p.mu.Unlock()
}

// Valid reports whether at least one live lease owns the frame.
func (f *CellFrame) Valid() bool { return f != nil && f.refs.Load() > 0 }

// Retain adds one owner. It returns ErrReleasedFrame if the final lease has
// already been released.
func (f *CellFrame) Retain() error {
	if f == nil {
		return ErrReleasedFrame
	}
	for refs := f.refs.Load(); refs > 0; refs = f.refs.Load() {
		if f.refs.CompareAndSwap(refs, refs+1) {
			return nil
		}
	}
	return ErrReleasedFrame
}

// Release relinquishes one owner. Each initial lease and successful Retain must
// have exactly one matching Release. The final release makes all previous
// references invalid and returns solver-owned storage to its pool; calling any
// method on such a stale reference can interfere with a later pooled lease.
func (f *CellFrame) Release() {
	if f == nil {
		return
	}
	for refs := f.refs.Load(); refs > 0; refs = f.refs.Load() {
		if !f.refs.CompareAndSwap(refs, refs-1) {
			continue
		}
		if refs == 1 && f.pool != nil {
			f.pool.recycle(f)
		}
		return
	}
}

// GeometryEpoch returns the upstream geometry generation.
func (f *CellFrame) GeometryEpoch() uint64 { return f.meta.GeometryEpoch }

// SourceSequence returns the upstream source sequence.
func (f *CellFrame) SourceSequence() uint64 { return f.meta.SourceSequence }

// SourcePTS returns the media presentation timestamp.
func (f *CellFrame) SourcePTS() time.Duration { return f.meta.PTS }

// Columns returns the terminal width.
func (f *CellFrame) Columns() int { return f.columns }

// Rows returns the terminal height.
func (f *CellFrame) Rows() int { return f.rows }

// Len returns Columns times Rows.
func (f *CellFrame) Len() int { return len(f.cells) }

// Cell returns the cell at a row-major index.
func (f *CellFrame) Cell(index int) (Cell, bool) {
	if f == nil || index < 0 || index >= len(f.cells) {
		return 0, false
	}
	return f.cells[index], true
}

// CellAt returns the cell at column x, row y.
func (f *CellFrame) CellAt(x, y int) (Cell, bool) {
	if f == nil || x < 0 || x >= f.columns || y < 0 || y >= f.rows {
		return 0, false
	}
	return f.cells[y*f.columns+x], true
}

// CopyCells copies row-major canonical cells into dst and returns the number
// copied. It never exposes the frame's mutable backing storage.
func (f *CellFrame) CopyCells(dst []Cell) int { return copy(dst, f.cells) }
