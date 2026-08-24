package recording

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"strings"

	"github.com/Melty1000/inlaid/internal/celltape"
)

var (
	// ErrCellTapeEmpty means that no committed terminal state can be exported.
	ErrCellTapeEmpty = errors.New("celltape contains no committed frames")
	// ErrCellTapeTail means that a CRC-checked prefix exists but the tape ends
	// in an incomplete or corrupt record. Claim it through taperecovery before
	// export; this package never mutates the canonical tape.
	ErrCellTapeTail = errors.New("celltape has a recoverable damaged tail")
	// ErrCellTapeAmplification means the requested fixed-rate derivative would
	// exceed the caller's explicit automatic-work budget. The canonical tape is
	// never removed by this package.
	ErrCellTapeAmplification = errors.New("celltape output frame budget exceeded")
)

// CellTapeExportConfig describes an offline derivative of a canonical tape.
// Writer configures the existing MP4/GIF encoder. Its CellColumns and CellRows
// are filled from the tape for the tiny fixed-grid fast path. When a tape
// contains grid changes they are cleared and Writer receives a directly
// rendered reusable full canvas instead.
//
// EndHostNanos is the elapsed host-clock time at which recording stopped. It
// is deliberately separate from the last changed frame: a quiet final image
// must still be held for the time the user was recording. When it is zero, a
// recovered tape is exported through one frame interval after its last commit.
type CellTapeExportConfig struct {
	TapePath        string
	EndHostNanos    uint64
	MaxOutputFrames uint64
	TapeLimits      celltape.Limits
	Writer          Config
}

// CellTapeExportReport makes the CFR timing conversion explicit. The tape is
// never removed by ExportCellTape, whether export succeeds or fails.
type CellTapeExportReport struct {
	Output               string
	Columns              uint32
	Rows                 uint32
	CommittedStates      uint64
	EncodedFrames        uint64
	RequestedHostNanos   uint64
	EncodedDurationNanos uint64
	VariableGeometry     bool
	GeometryChanges      uint64
	Recovery             celltape.Recovery
}

type cellTapePlan struct {
	columns, rows uint32
	records       uint64
	lastHostNanos uint64
	variable      bool
	changes       uint64
}

// ExportCellTape verifies the committed tape prefix, maps its monotonic host
// timestamps onto a fixed-rate output without shortening wall-clock duration,
// and sends only the exact 2C-by-2R terminal quadrant raster to FFmpeg. No ANSI
// is parsed and no full-resolution Go image is constructed.
func ExportCellTape(ctx context.Context, cfg CellTapeExportConfig) (CellTapeExportReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var report CellTapeExportReport
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if strings.TrimSpace(cfg.TapePath) == "" {
		return report, errors.New("celltape path is required")
	}

	replay, err := celltape.OpenContext(ctx, cfg.TapePath, celltape.OpenOptions{Limits: cfg.TapeLimits})
	if err != nil {
		return report, fmt.Errorf("open celltape: %w", err)
	}
	defer replay.Close()
	return ExportCellTapeReplay(ctx, replay, cfg)
}

// ExportCellTapeReplay consumes an already validated replay without reopening
// its pathname. The caller retains ownership and must close replay.
func ExportCellTapeReplay(ctx context.Context, replay *celltape.Replay, cfg CellTapeExportConfig) (CellTapeExportReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var report CellTapeExportReport
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if replay == nil {
		return report, errors.New("celltape replay is required")
	}
	if strings.TrimSpace(cfg.TapePath) == "" {
		return report, errors.New("celltape path is required")
	}
	report.Recovery = replay.Recovery()
	if report.Recovery.TailError != nil {
		return report, fmt.Errorf(
			"%w: %v (valid prefix: %d states; tape retained at %s)",
			ErrCellTapeTail,
			report.Recovery.TailError,
			report.Recovery.ValidRecords,
			cfg.TapePath,
		)
	}

	plan, err := inspectCellTape(ctx, replay, cfg.Writer.Width, cfg.Writer.Height)
	if err != nil {
		return report, err
	}
	report.Columns, report.Rows, report.CommittedStates = plan.columns, plan.rows, plan.records
	report.VariableGeometry, report.GeometryChanges = plan.variable, plan.changes

	endHostNanos := cfg.EndHostNanos
	if endHostNanos == 0 {
		period := ceilDiv(1_000_000_000, uint64(cfg.Writer.FPS))
		if period == 0 { // normalize will provide the user-facing FPS error.
			period = 1
		}
		if plan.lastHostNanos > math.MaxUint64-period {
			return report, errors.New("celltape duration is too large")
		}
		endHostNanos = plan.lastHostNanos + period
	}
	if endHostNanos < plan.lastHostNanos {
		return report, fmt.Errorf(
			"export end time %d ns precedes the final committed state at %d ns",
			endHostNanos,
			plan.lastHostNanos,
		)
	}

	frames, err := framesForDuration(endHostNanos, cfg.Writer.FPS)
	if err != nil {
		return report, err
	}
	report.EncodedFrames = frames
	if cfg.MaxOutputFrames > 0 && frames > cfg.MaxOutputFrames {
		return report, fmt.Errorf(
			"%w: requested %d frames, limit %d (CellTape retained at %s)",
			ErrCellTapeAmplification,
			frames,
			cfg.MaxOutputFrames,
			cfg.TapePath,
		)
	}
	report.RequestedHostNanos = endHostNanos
	report.EncodedDurationNanos = durationForFrames(frames, cfg.Writer.FPS)

	writerConfig := cfg.Writer
	// The canonical CellTape remains caller-owned on every error and is the
	// sole recovery source for offline export. A second hidden completed-media
	// stage would be redundant, untracked disk growth.
	writerConfig.DiscardCompletedStageOnFailure = true
	if plan.variable {
		writerConfig.CellColumns, writerConfig.CellRows = 0, 0
	} else {
		if writerConfig.CellColumns != 0 && writerConfig.CellColumns != int(plan.columns) {
			return report, fmt.Errorf("writer expects %d cell columns; tape contains %d", writerConfig.CellColumns, plan.columns)
		}
		if writerConfig.CellRows != 0 && writerConfig.CellRows != int(plan.rows) {
			return report, fmt.Errorf("writer expects %d cell rows; tape contains %d", writerConfig.CellRows, plan.rows)
		}
		writerConfig.CellColumns, writerConfig.CellRows = int(plan.columns), int(plan.rows)
	}

	replay.Rewind()
	writer, err := Start(ctx, writerConfig)
	if err != nil {
		return report, fmt.Errorf("start celltape export: %w", err)
	}
	report.Output = writer.Output()
	if plan.variable {
		err = emitCellTapeCanvasFrames(
			ctx,
			replay,
			frames,
			writerConfig.FPS,
			writerConfig.Width,
			writerConfig.Height,
			writer.WriteFrame,
		)
	} else {
		err = emitCellTapeFrames(ctx, replay, frames, writerConfig.FPS, writer.WriteFrame)
	}
	if err != nil {
		abortErr := writer.Abort()
		return report, errors.Join(fmt.Errorf("export celltape: %w", err), abortErr)
	}
	if err = ctx.Err(); err != nil {
		abortErr := writer.Abort()
		return report, errors.Join(fmt.Errorf("export celltape: %w", err), abortErr)
	}
	if err = writer.Close(); err != nil {
		return report, fmt.Errorf("finish celltape export: %w", err)
	}
	return report, nil
}

func inspectCellTape(ctx context.Context, replay *celltape.Replay, canvasWidth, canvasHeight int) (cellTapePlan, error) {
	if err := ctx.Err(); err != nil {
		return cellTapePlan{}, err
	}
	summary := replay.Summary()
	if summary.Records == 0 {
		return cellTapePlan{}, ErrCellTapeEmpty
	}
	if summary.MaxCells > maxTerminalCells {
		return cellTapePlan{}, fmt.Errorf("celltape grid exceeds the %d-cell export limit", maxTerminalCells)
	}
	if canvasWidth > 0 && canvasHeight > 0 &&
		(summary.MaxColumns > uint32(canvasWidth) || summary.MaxRows > uint32(canvasHeight/2)) {
		return cellTapePlan{}, fmt.Errorf(
			"celltape grid up to %dx%d cannot fit export canvas %dx%d",
			summary.MaxColumns,
			summary.MaxRows,
			canvasWidth,
			canvasHeight,
		)
	}
	return cellTapePlan{
		columns:       summary.FirstColumns,
		rows:          summary.FirstRows,
		records:       summary.Records,
		lastHostNanos: summary.LastHostNanos,
		variable:      summary.VariableGeometry,
		changes:       summary.GeometryChanges,
	}, nil
}

// emitCellTapeFrames is intentionally independent of FFmpeg so cadence and
// pixel identity can be tested deterministically. Replay has already passed
// the geometry preflight, so one tiny raster is safely reused for all writes.
func emitCellTapeFrames(
	ctx context.Context,
	replay *celltape.Replay,
	frameCount uint64,
	fps int,
	write func(*image.RGBA) error,
) error {
	var raster *image.RGBA
	return emitProjectedCellTapeFrames(ctx, replay, frameCount, fps, func(frame celltape.Frame) (*image.RGBA, error) {
		var err error
		raster, err = cellTapeTinyRGBA(frame, raster)
		return raster, err
	}, write)
}

func emitCellTapeCanvasFrames(
	ctx context.Context,
	replay *celltape.Replay,
	frameCount uint64,
	fps int,
	width int,
	height int,
	write func(*image.RGBA) error,
) error {
	var canvas *image.RGBA
	return emitProjectedCellTapeFrames(ctx, replay, frameCount, fps, func(frame celltape.Frame) (*image.RGBA, error) {
		var err error
		canvas, err = cellTapeCanvasRGBA(frame, canvas, width, height)
		return canvas, err
	}, write)
}

func emitProjectedCellTapeFrames(
	ctx context.Context,
	replay *celltape.Replay,
	frameCount uint64,
	fps int,
	project func(celltape.Frame) (*image.RGBA, error),
	write func(*image.RGBA) error,
) error {
	if frameCount == 0 {
		return ErrCellTapeEmpty
	}
	if fps <= 0 {
		return errors.New("recording FPS must be positive")
	}
	var index uint64
	sawState := false
	err := replay.IterateBorrowedIntervals(ctx, func(frame celltape.Frame, nextHostNanos uint64, hasNext bool) error {
		sawState = true
		if index >= frameCount || hasNext && frameTimestamp(index, fps) >= nextHostNanos {
			return nil
		}
		current, err := project(frame)
		if err != nil {
			return err
		}
		for index < frameCount && (!hasNext || frameTimestamp(index, fps) < nextHostNanos) {
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = write(current); err != nil {
				return err
			}
			index++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !sawState {
		return ErrCellTapeEmpty
	}
	if index != frameCount {
		return fmt.Errorf("celltape emitted %d of %d planned frames", index, frameCount)
	}
	return nil
}

// cellTapeCanvasRGBA is the resize fallback. It performs the same centered
// terminal-cell geometry as Writer's tiny-raster filter, but projects each
// accepted grid directly into one reusable canvas. It is offline-only: the
// live path never pays for a full-resolution RGBA image.
func cellTapeCanvasRGBA(frame celltape.Frame, destination *image.RGBA, width, height int) (*image.RGBA, error) {
	columns, rows := int(frame.Columns), int(frame.Rows)
	if columns <= 0 || rows <= 0 || columns > math.MaxInt/rows || len(frame.Cells) != columns*rows {
		return nil, errors.New("celltape frame cell count does not match geometry")
	}
	contentWidth, contentHeight, originX, originY, err := cellCanvasGeometry(columns, rows, width, height)
	if err != nil {
		return nil, err
	}
	bounds := image.Rect(0, 0, width, height)
	if destination == nil || destination.Bounds() != bounds {
		destination = image.NewRGBA(bounds)
	}
	for _, cell := range frame.Cells {
		if err := validateTapeCell(cell); err != nil {
			return nil, err
		}
	}
	// Only the padding needs clearing: the content area is overwritten in full
	// below. This also erases pixels exposed by a geometry change without doing
	// a second full-canvas pass.
	fillOpaqueBlack(destination, image.Rect(0, 0, width, originY))
	fillOpaqueBlack(destination, image.Rect(0, originY+contentHeight, width, height))
	fillOpaqueBlack(destination, image.Rect(0, originY, originX, originY+contentHeight))
	fillOpaqueBlack(destination, image.Rect(originX+contentWidth, originY, width, originY+contentHeight))

	cellWidth := contentWidth / columns
	leftWidth := cellWidth / 2
	for cellY := 0; cellY < rows; cellY++ {
		topY := originY + cellY*cellWidth*2
		writeCellScanline(destination, frame.Cells[cellY*columns:(cellY+1)*columns], originX, topY, cellWidth, leftWidth, 0, 1)
		copyScanline(destination, originX, topY, contentWidth, cellWidth)

		bottomY := topY + cellWidth
		writeCellScanline(destination, frame.Cells[cellY*columns:(cellY+1)*columns], originX, bottomY, cellWidth, leftWidth, 2, 3)
		copyScanline(destination, originX, bottomY, contentWidth, cellWidth)
	}
	return destination, nil
}

func writeCellScanline(destination *image.RGBA, cells []celltape.Cell, x, y, cellWidth, leftWidth, leftQuadrant, rightQuadrant int) {
	offset := destination.PixOffset(x, y)
	for _, cell := range cells {
		left := cell.BG
		if cell.Mask&(1<<uint(leftQuadrant)) != 0 {
			left = cell.FG
		}
		fillPackedRGBA(destination.Pix[offset:offset+leftWidth*4], uint32(left))
		offset += leftWidth * 4

		right := cell.BG
		if cell.Mask&(1<<uint(rightQuadrant)) != 0 {
			right = cell.FG
		}
		rightWidth := cellWidth - leftWidth
		fillPackedRGBA(destination.Pix[offset:offset+rightWidth*4], uint32(right))
		offset += rightWidth * 4
	}
}

func copyScanline(destination *image.RGBA, x, y, width, copies int) {
	sourceOffset := destination.PixOffset(x, y)
	source := destination.Pix[sourceOffset : sourceOffset+width*4]
	for row := 1; row < copies; row++ {
		destinationOffset := destination.PixOffset(x, y+row)
		copy(destination.Pix[destinationOffset:destinationOffset+width*4], source)
	}
}

func fillOpaqueBlack(destination *image.RGBA, rectangle image.Rectangle) {
	rectangle = rectangle.Intersect(destination.Bounds())
	if rectangle.Empty() {
		return
	}
	for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
		start := destination.PixOffset(rectangle.Min.X, y)
		end := destination.PixOffset(rectangle.Max.X, y)
		row := destination.Pix[start:end]
		clear(row)
		for alpha := 3; alpha < len(row); alpha += 4 {
			row[alpha] = 0xff
		}
	}
}

func fillPackedRGBA(pixels []byte, packed uint32) {
	red, green, blue := uint8(packed>>16), uint8(packed>>8), uint8(packed)
	for offset := 0; offset < len(pixels); offset += 4 {
		pixels[offset+0] = red
		pixels[offset+1] = green
		pixels[offset+2] = blue
		pixels[offset+3] = 0xff
	}
}

func cellTapeTinyRGBA(frame celltape.Frame, destination *image.RGBA) (*image.RGBA, error) {
	columns, rows := int(frame.Columns), int(frame.Rows)
	if columns <= 0 || rows <= 0 || columns > math.MaxInt/2 || rows > math.MaxInt/2 || columns > math.MaxInt/rows {
		return nil, errors.New("celltape frame geometry is out of range")
	}
	if len(frame.Cells) != columns*rows {
		return nil, errors.New("celltape frame cell count does not match geometry")
	}
	bounds := image.Rect(0, 0, columns*2, rows*2)
	if destination == nil || destination.Bounds() != bounds {
		destination = image.NewRGBA(bounds)
	}
	for y := 0; y < rows; y++ {
		for x := 0; x < columns; x++ {
			cell := frame.Cells[y*columns+x]
			if err := validateTapeCell(cell); err != nil {
				return nil, err
			}
			for quadrant := 0; quadrant < 4; quadrant++ {
				color := cell.BG
				if cell.Mask&(1<<quadrant) != 0 {
					color = cell.FG
				}
				offset := destination.PixOffset(x*2+(quadrant&1), y*2+quadrant/2)
				packed := uint32(color)
				destination.Pix[offset+0] = uint8(packed >> 16)
				destination.Pix[offset+1] = uint8(packed >> 8)
				destination.Pix[offset+2] = uint8(packed)
				destination.Pix[offset+3] = 0xff
			}
		}
	}
	return destination, nil
}

func validateTapeCell(cell celltape.Cell) error {
	if cell.Mask > 7 || uint32(cell.FG) > 0xffffff || uint32(cell.BG) > 0xffffff ||
		(cell.Mask == 0) != (cell.FG == cell.BG) {
		return errors.New("celltape frame contains a noncanonical cell")
	}
	return nil
}

func framesForDuration(nanos uint64, fps int) (uint64, error) {
	if fps <= 0 || fps > 240 {
		return 0, errors.New("recording FPS must be between 1 and 240")
	}
	rate := uint64(fps)
	seconds, remainder := nanos/1_000_000_000, nanos%1_000_000_000
	if seconds > math.MaxUint64/rate {
		return 0, errors.New("celltape duration is too large")
	}
	frames := seconds * rate
	fraction := remainder * rate // remainder < 1e9 and rate <= 240.
	additional := ceilDiv(fraction, 1_000_000_000)
	if frames > math.MaxUint64-additional {
		return 0, errors.New("celltape duration is too large")
	}
	frames += additional
	if frames == 0 {
		frames = 1
	}
	return frames, nil
}

func frameTimestamp(index uint64, fps int) uint64 {
	rate := uint64(fps)
	return index/rate*1_000_000_000 + (index%rate)*1_000_000_000/rate
}

func durationForFrames(frames uint64, fps int) uint64 {
	rate := uint64(fps)
	seconds, remainder := frames/rate, frames%rate
	if seconds > math.MaxUint64/1_000_000_000 {
		return math.MaxUint64
	}
	nanos := seconds * 1_000_000_000
	fraction := ceilDiv(remainder*1_000_000_000, rate)
	if nanos > math.MaxUint64-fraction {
		return math.MaxUint64
	}
	return nanos + fraction
}

func ceilDiv(value, divisor uint64) uint64 {
	if divisor == 0 || value == 0 {
		return 0
	}
	return 1 + (value-1)/divisor
}
