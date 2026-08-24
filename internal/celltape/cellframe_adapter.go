package celltape

import (
	"errors"
	"fmt"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

// PreparedCellFrame is a generation-tagged, allocation-free producer token.
// It is safe to copy: exactly one copy can commit or abort the reservation and
// stale copies cannot act on a recycled queue buffer.
type PreparedCellFrame struct {
	reservation reservation
}

// PrepareCellFrame reserves one bounded queue buffer and copies the canonical
// cells directly from a live CellFrame lease into that reusable storage. The
// caller must keep frame leased for the duration of this call. Commit only
// after the same state has been accepted for terminal display; otherwise Abort.
func (r *Recorder) PrepareCellFrame(frame *cellframe.CellFrame, configEpoch uint64, config []byte, boundary BoundaryFlags) (PreparedCellFrame, error) {
	if frame == nil || !frame.Valid() {
		return PreparedCellFrame{}, errors.New("celltape: cellframe lease is not valid")
	}
	columns, rows := frame.Columns(), frame.Rows()
	if columns <= 0 || rows <= 0 || uint64(columns) > uint64(^uint32(0)) || uint64(rows) > uint64(^uint32(0)) {
		return PreparedCellFrame{}, errors.New("celltape: cellframe geometry is out of range")
	}
	if frame.SourcePTS() < 0 {
		return PreparedCellFrame{}, errors.New("celltape: negative source PTS")
	}
	if boundary & ^(BoundaryGap|BoundaryDiscontinuity) != 0 {
		return PreparedCellFrame{}, errors.New("celltape: unknown boundary flags")
	}
	if r == nil {
		return PreparedCellFrame{}, ErrClosed
	}
	count, err := checkedCellCount(uint32(columns), uint32(rows), r.cfg.Limits)
	if err != nil {
		return PreparedCellFrame{}, err
	}
	if int(count) != frame.Len() {
		return PreparedCellFrame{}, fmt.Errorf("celltape: cellframe count %d does not match geometry %d", frame.Len(), count)
	}
	if len(config) > int(r.cfg.Limits.MaxConfigBytes) {
		return PreparedCellFrame{}, errors.New("celltape: config exceeds limit")
	}

	prepared, err := r.reserve()
	if err != nil {
		return PreparedCellFrame{}, err
	}
	buffer := prepared.buffer
	buffer.mu.Lock()
	buffer.config = append(buffer.config[:0], config...)
	if cap(buffer.cells) < frame.Len() {
		buffer.cells = make([]Cell, frame.Len())
	} else {
		buffer.cells = buffer.cells[:frame.Len()]
	}
	for i := range buffer.cells {
		cell, ok := frame.Cell(i)
		if !ok || !cell.IsCanonical() {
			buffer.mu.Unlock()
			prepared.abort()
			return PreparedCellFrame{}, fmt.Errorf("celltape: cellframe cell %d is unavailable or noncanonical", i)
		}
		packed := cell.Packed()
		buffer.cells[i] = Cell{
			Mask: uint8(packed & 0x0f),
			FG:   RGB((packed >> 4) & 0xffffff),
			BG:   RGB((packed >> 28) & 0xffffff),
		}
	}
	buffer.input = Input{
		GeometryEpoch: frame.GeometryEpoch(),
		ConfigEpoch:   configEpoch,
		Columns:       uint32(columns),
		Rows:          uint32(rows),
		Config:        buffer.config,
		Cells:         buffer.cells,
		SourceNanos:   uint64(frame.SourcePTS()),
		Boundary:      boundary,
	}
	buffer.mu.Unlock()
	return PreparedCellFrame{reservation: prepared}, nil
}

// Commit accepts the prepared terminal state at a monotonic host-clock offset.
func (p PreparedCellFrame) Commit(hostNanos uint64) error {
	return p.reservation.commit(hostNanos)
}

// Abort idempotently returns the reserved queue buffer without recording it.
func (p PreparedCellFrame) Abort() { p.reservation.abort() }

// SubmitCellFrame is the one-call convenience form for callers that have
// already crossed their terminal-display acceptance boundary.
func (r *Recorder) SubmitCellFrame(frame *cellframe.CellFrame, configEpoch uint64, config []byte, boundary BoundaryFlags, hostNanos uint64) error {
	prepared, err := r.PrepareCellFrame(frame, configEpoch, config, boundary)
	if err != nil {
		return err
	}
	if err = prepared.Commit(hostNanos); err != nil {
		prepared.Abort()
	}
	return err
}
