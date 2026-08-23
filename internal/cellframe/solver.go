package cellframe

import (
	"errors"
	"fmt"
	"math"
)

const (
	maxPixelSquaredError = uint64(3 * 255 * 255)
	maxFormulaFactor     = uint64(2 * 3 * 255 * 255)
	maxCells             = 1_000_000
	maxFrameBuffers      = 64
	maxPooledCells       = 4_000_000
)

// Mode selects the spatial partition family.
type Mode uint8

const (
	// ModeDetailed evaluates all eight complement-equivalent 2x2 partitions.
	ModeDetailed Mode = iota
	// ModeSoft fixes the upper-half-block partition (top versus bottom).
	ModeSoft

	// Detailed and Soft are concise aliases for ModeDetailed and ModeSoft.
	Detailed = ModeDetailed
	Soft     = ModeSoft
)

// Config fixes solver geometry and bounded output ownership.
type Config struct {
	Columns int
	Rows    int
	Mode    Mode
	// Transform optionally maps the two solved colors in every cell. The
	// spatial mask and temporal deadband are selected in source color space;
	// transformed colors, reconstruction error, and visual hash are emitted.
	Transform ColorTransform
	// Buffers is the maximum number of simultaneously live CellFrames. Zero
	// selects three. Solve returns ErrFramePoolExhausted instead of allocating.
	Buffers int
}

// RGB24 is an interleaved RGB byte image. Width and Height must be exactly
// 2*Columns and 2*Rows; Stride may include right-side padding.
type RGB24 struct {
	Pix           []byte
	Width, Height int
	Stride        int
}

// PlanarRGB is an RGB byte image in three independent planes. Its dimensions
// must be exactly 2*Columns and 2*Rows. Each stride may include padding.
type PlanarRGB struct {
	R, G, B                   []byte
	Width, Height             int
	RStride, GStride, BStride int
}

// SampleStats are sufficient statistics for all pixels represented by one
// quadrant: channel sums and the sum of R^2+G^2+B^2. Upstream area reducers can
// therefore preserve exact least-squares error without handing pixels to the
// solver. The zero value is empty and invalid as a final quadrant.
type SampleStats struct {
	Count      uint64
	SumR       uint64
	SumG       uint64
	SumB       uint64
	SumSquares uint64
}

// Add adds one RGB sample. Callers should reuse and zero their statistics
// buffers between frames.
func (s *SampleStats) Add(c RGB) {
	r, g, b := uint64(c.R()), uint64(c.G()), uint64(c.B())
	s.Count++
	s.SumR += r
	s.SumG += g
	s.SumB += b
	s.SumSquares += r*r + g*g + b*b
}

// AddRGB adds one unpacked RGB sample.
func (s *SampleStats) AddRGB(r, g, b uint8) { s.Add(NewRGB(r, g, b)) }

// StatisticsFrame is cell-major sufficient-statistics input. Each cell has
// four adjacent entries in TL, TR, BL, BR order. Columns and Rows must match
// the Solver and len(Quadrants) must equal 4*Columns*Rows.
type StatisticsFrame struct {
	Quadrants     []SampleStats
	Columns, Rows int
}

// Solver performs fixed-geometry spatial least-squares fitting. It is safe for
// concurrent base solves; a DeadbandPolicy is single-stream state and is not.
type Solver struct {
	columns   int
	rows      int
	mode      Mode
	transform ColorTransform
	pool      *framePool
}

// NewSolver validates config and preallocates every output buffer.
func NewSolver(cfg Config) (*Solver, error) {
	if cfg.Columns <= 0 || cfg.Rows <= 0 {
		return nil, errors.New("cellframe: columns and rows must be positive")
	}
	if cfg.Columns > maxCells/cfg.Rows {
		return nil, fmt.Errorf("cellframe: grid must not exceed %d cells", maxCells)
	}
	if cfg.Mode != ModeDetailed && cfg.Mode != ModeSoft {
		return nil, errors.New("cellframe: invalid solver mode")
	}
	if cfg.Buffers == 0 {
		cfg.Buffers = 3
	}
	if cfg.Buffers < 1 || cfg.Buffers > maxFrameBuffers || cfg.Columns*cfg.Rows > maxPooledCells/cfg.Buffers {
		return nil, errors.New("cellframe: invalid output buffer count")
	}
	return &Solver{
		columns:   cfg.Columns,
		rows:      cfg.Rows,
		mode:      cfg.Mode,
		transform: cfg.Transform,
		pool:      newFramePool(cfg.Columns, cfg.Rows, cfg.Buffers),
	}, nil
}

// Columns returns the fixed terminal width.
func (s *Solver) Columns() int { return s.columns }

// Rows returns the fixed terminal height.
func (s *Solver) Rows() int { return s.rows }

// Mode returns the spatial partition mode.
func (s *Solver) Mode() Mode { return s.mode }

// SolveRGB24 solves an exact interleaved 2C-by-2R input with no temporal state.
func (s *Solver) SolveRGB24(input RGB24, meta SourceMeta) (*CellFrame, error) {
	source, err := s.rgb24Source(input)
	if err != nil {
		return nil, err
	}
	return s.solve(source, meta, nil)
}

// SolveRGB24WithPolicy applies an explicit bounded temporal deadband after the
// independently optimal spatial solve.
func (s *Solver) SolveRGB24WithPolicy(input RGB24, meta SourceMeta, policy *DeadbandPolicy) (*CellFrame, error) {
	source, err := s.rgb24Source(input)
	if err != nil {
		return nil, err
	}
	return s.solve(source, meta, policy)
}

// SolvePlanar solves an exact planar 2C-by-2R input with no temporal state.
func (s *Solver) SolvePlanar(input PlanarRGB, meta SourceMeta) (*CellFrame, error) {
	source, err := s.planarSource(input)
	if err != nil {
		return nil, err
	}
	return s.solve(source, meta, nil)
}

// SolvePlanarWithPolicy is the explicit temporal-policy form of SolvePlanar.
func (s *Solver) SolvePlanarWithPolicy(input PlanarRGB, meta SourceMeta, policy *DeadbandPolicy) (*CellFrame, error) {
	source, err := s.planarSource(input)
	if err != nil {
		return nil, err
	}
	return s.solve(source, meta, policy)
}

// SolveStatistics solves area-aggregated cell-major sufficient statistics.
func (s *Solver) SolveStatistics(input StatisticsFrame, meta SourceMeta) (*CellFrame, error) {
	source, err := s.statisticsSource(input)
	if err != nil {
		return nil, err
	}
	return s.solve(source, meta, nil)
}

// SolveStatisticsWithPolicy is the explicit temporal-policy form of
// SolveStatistics.
func (s *Solver) SolveStatisticsWithPolicy(input StatisticsFrame, meta SourceMeta, policy *DeadbandPolicy) (*CellFrame, error) {
	source, err := s.statisticsSource(input)
	if err != nil {
		return nil, err
	}
	return s.solve(source, meta, policy)
}

type sourceKind uint8

const (
	sourceRGB24 sourceKind = iota
	sourcePlanar
	sourceStatistics
)

type spatialSource struct {
	kind         sourceKind
	rgb          []byte
	r, g, b      []byte
	stride       int
	rStride      int
	gStride      int
	bStride      int
	statistics   []SampleStats
	totalSamples uint64
}

func (s *Solver) rgb24Source(input RGB24) (spatialSource, error) {
	wantWidth, wantHeight := s.columns*2, s.rows*2
	if input.Width != wantWidth || input.Height != wantHeight {
		return spatialSource{}, fmt.Errorf("cellframe: RGB24 dimensions are %dx%d, want exact %dx%d", input.Width, input.Height, wantWidth, wantHeight)
	}
	rowBytes := wantWidth * 3
	if input.Stride < rowBytes {
		return spatialSource{}, errors.New("cellframe: RGB24 stride is shorter than one row")
	}
	required, ok := requiredBytes(input.Stride, input.Height, rowBytes)
	if !ok || len(input.Pix) < required {
		return spatialSource{}, errors.New("cellframe: RGB24 pixel buffer is truncated")
	}
	return spatialSource{
		kind:         sourceRGB24,
		rgb:          input.Pix,
		stride:       input.Stride,
		totalSamples: uint64(wantWidth) * uint64(wantHeight),
	}, nil
}

func (s *Solver) planarSource(input PlanarRGB) (spatialSource, error) {
	wantWidth, wantHeight := s.columns*2, s.rows*2
	if input.Width != wantWidth || input.Height != wantHeight {
		return spatialSource{}, fmt.Errorf("cellframe: planar dimensions are %dx%d, want exact %dx%d", input.Width, input.Height, wantWidth, wantHeight)
	}
	if input.RStride < wantWidth || input.GStride < wantWidth || input.BStride < wantWidth {
		return spatialSource{}, errors.New("cellframe: planar stride is shorter than one row")
	}
	rRequired, rOK := requiredBytes(input.RStride, input.Height, wantWidth)
	gRequired, gOK := requiredBytes(input.GStride, input.Height, wantWidth)
	bRequired, bOK := requiredBytes(input.BStride, input.Height, wantWidth)
	if !rOK || !gOK || !bOK || len(input.R) < rRequired || len(input.G) < gRequired || len(input.B) < bRequired {
		return spatialSource{}, errors.New("cellframe: planar pixel buffer is truncated")
	}
	return spatialSource{
		kind:         sourcePlanar,
		r:            input.R,
		g:            input.G,
		b:            input.B,
		rStride:      input.RStride,
		gStride:      input.GStride,
		bStride:      input.BStride,
		totalSamples: uint64(wantWidth) * uint64(wantHeight),
	}, nil
}

func (s *Solver) statisticsSource(input StatisticsFrame) (spatialSource, error) {
	if input.Columns != s.columns || input.Rows != s.rows {
		return spatialSource{}, fmt.Errorf("cellframe: statistics dimensions are %dx%d, want exact %dx%d", input.Columns, input.Rows, s.columns, s.rows)
	}
	want := 4 * s.columns * s.rows
	if len(input.Quadrants) != want {
		return spatialSource{}, fmt.Errorf("cellframe: statistics have %d quadrants, want %d", len(input.Quadrants), want)
	}
	var totalSamples uint64
	for _, stats := range input.Quadrants {
		if err := validateStats(stats); err != nil {
			return spatialSource{}, err
		}
		if totalSamples > math.MaxUint64-stats.Count {
			return spatialSource{}, errors.New("cellframe: statistics sample count overflows uint64")
		}
		totalSamples += stats.Count
	}
	// This bound keeps the integer least-squares formula and aggregate frame
	// error exact in uint64, even for adversarial-but-otherwise-valid input.
	if totalSamples > math.MaxUint64/maxFormulaFactor {
		return spatialSource{}, errors.New("cellframe: statistics contain too many samples")
	}
	return spatialSource{
		kind:         sourceStatistics,
		statistics:   input.Quadrants,
		totalSamples: totalSamples,
	}, nil
}

func requiredBytes(stride, height, lastRowBytes int) (int, bool) {
	if height <= 0 || stride < 0 || lastRowBytes < 0 {
		return 0, false
	}
	rows := height - 1
	if rows > 0 && stride > (math.MaxInt-lastRowBytes)/rows {
		return 0, false
	}
	return rows*stride + lastRowBytes, true
}

func validateStats(s SampleStats) error {
	if s.Count == 0 {
		return errors.New("cellframe: every quadrant must contain at least one sample")
	}
	if s.Count > math.MaxUint64/maxPixelSquaredError ||
		s.SumR > s.Count*255 || s.SumG > s.Count*255 || s.SumB > s.Count*255 ||
		s.SumSquares > s.Count*maxPixelSquaredError {
		return errors.New("cellframe: invalid or overflowing quadrant statistics")
	}
	return nil
}

func (s *Solver) solve(source spatialSource, meta SourceMeta, policy *DeadbandPolicy) (*CellFrame, error) {
	if policy != nil {
		if policy.columns != s.columns || policy.rows != s.rows {
			return nil, errors.New("cellframe: deadband policy geometry does not match solver")
		}
		policy.prepare(source, meta)
	}
	frame := s.pool.acquire()
	if frame == nil {
		return nil, ErrFramePoolExhausted
	}
	frame.meta = meta
	var totalError uint64
	cellIndex := 0
	for y := 0; y < s.rows; y++ {
		for x := 0; x < s.columns; x++ {
			quadrants := source.cell(s.columns, x, y, cellIndex)
			cell, cellError := solveCell(quadrants, s.mode)
			if policy != nil {
				cell, cellError = policy.choose(cellIndex, quadrants, cell, cellError)
			}
			if s.transform != nil {
				foreground, background := cell.Foreground(), cell.Background()
				if foreground == background {
					foreground = s.transform.TransformRGB(foreground)
					background = foreground
				} else {
					foreground = s.transform.TransformRGB(foreground)
					background = s.transform.TransformRGB(background)
				}
				cell = NewCell(cell.Mask(), foreground, background)
				cellError = cellErrorUnchecked(cell, quadrants)
			}
			frame.cells[cellIndex] = cell
			totalError += cellError
			cellIndex++
		}
	}
	if policy != nil {
		policy.finish(meta)
	}
	frame.reconstructionError = totalError
	frame.hash = visualHash(meta.GeometryEpoch, s.columns, s.rows, frame.cells)
	return frame, nil
}

func (s spatialSource) cell(columns, x, y, cellIndex int) [4]SampleStats {
	if s.kind == sourceStatistics {
		base := cellIndex * 4
		return [4]SampleStats{s.statistics[base], s.statistics[base+1], s.statistics[base+2], s.statistics[base+3]}
	}
	px, py := x*2, y*2
	if s.kind == sourceRGB24 {
		top := py*s.stride + px*3
		bottom := top + s.stride
		return [4]SampleStats{
			statsForRGB(s.rgb[top], s.rgb[top+1], s.rgb[top+2]),
			statsForRGB(s.rgb[top+3], s.rgb[top+4], s.rgb[top+5]),
			statsForRGB(s.rgb[bottom], s.rgb[bottom+1], s.rgb[bottom+2]),
			statsForRGB(s.rgb[bottom+3], s.rgb[bottom+4], s.rgb[bottom+5]),
		}
	}
	topR, topG, topB := py*s.rStride+px, py*s.gStride+px, py*s.bStride+px
	bottomR, bottomG, bottomB := topR+s.rStride, topG+s.gStride, topB+s.bStride
	return [4]SampleStats{
		statsForRGB(s.r[topR], s.g[topG], s.b[topB]),
		statsForRGB(s.r[topR+1], s.g[topG+1], s.b[topB+1]),
		statsForRGB(s.r[bottomR], s.g[bottomG], s.b[bottomB]),
		statsForRGB(s.r[bottomR+1], s.g[bottomG+1], s.b[bottomB+1]),
	}
}

func statsForRGB(r, g, b uint8) SampleStats {
	rr, gg, bb := uint64(r), uint64(g), uint64(b)
	return SampleStats{Count: 1, SumR: rr, SumG: gg, SumB: bb, SumSquares: rr*rr + gg*gg + bb*bb}
}

func solveCell(quadrants [4]SampleStats, mode Mode) (Cell, uint64) {
	if mode == ModeSoft {
		return fitPartition(quadrants, 0x03)
	}
	bestCell, bestError := fitPartition(quadrants, 0)
	for mask := uint8(1); mask < 8; mask++ {
		candidate, candidateError := fitPartition(quadrants, mask)
		// Masks are visited in canonical numeric order, so retaining the first
		// exact tie is deterministic and selects the smallest mask.
		if candidateError < bestError {
			bestCell, bestError = candidate, candidateError
		}
	}
	return bestCell, bestError
}

func fitPartition(quadrants [4]SampleStats, mask uint8) (Cell, uint64) {
	var foreground, background SampleStats
	for quadrant, stats := range quadrants {
		if mask&(1<<quadrant) != 0 {
			foreground.add(stats)
		} else {
			background.add(stats)
		}
	}
	fg := foreground.meanLower()
	bg := background.meanLower()
	if foreground.Count == 0 {
		fg = bg
	}
	if background.Count == 0 {
		bg = fg
	}
	cell := NewCell(mask, fg, bg)
	return cell, cellErrorUnchecked(cell, quadrants)
}

func (s *SampleStats) add(other SampleStats) {
	s.Count += other.Count
	s.SumR += other.SumR
	s.SumG += other.SumG
	s.SumB += other.SumB
	s.SumSquares += other.SumSquares
}

func (s SampleStats) meanLower() RGB {
	if s.Count == 0 {
		return 0
	}
	return NewRGB(
		nearestIntegerLowerTie(s.SumR, s.Count),
		nearestIntegerLowerTie(s.SumG, s.Count),
		nearestIntegerLowerTie(s.SumB, s.Count),
	)
}

func nearestIntegerLowerTie(sum, count uint64) uint8 {
	quotient, remainder := sum/count, sum%count
	// Both adjacent integers are optimal at an exact half; choose the lower
	// one so every platform and input path has the same packed result.
	if remainder > count-remainder {
		quotient++
	}
	return uint8(quotient)
}

// CellError returns exact total squared RGB-channel reconstruction error for a
// cell over four non-empty quadrant statistics.
func CellError(cell Cell, quadrants [4]SampleStats) (uint64, error) {
	for _, stats := range quadrants {
		if err := validateStats(stats); err != nil {
			return 0, err
		}
	}
	if !cell.IsCanonical() {
		return 0, errors.New("cellframe: CellError requires a canonical cell")
	}
	return cellErrorUnchecked(cell, quadrants), nil
}

func cellErrorUnchecked(cell Cell, quadrants [4]SampleStats) uint64 {
	var total uint64
	for quadrant, stats := range quadrants {
		total += stats.errorFor(cell.ColorAt(quadrant))
	}
	return total
}

func (s SampleStats) errorFor(c RGB) uint64 {
	r, g, b := uint64(c.R()), uint64(c.G()), uint64(c.B())
	meanSquares := r*r + g*g + b*b
	dot := r*s.SumR + g*s.SumG + b*s.SumB
	return s.SumSquares + s.Count*meanSquares - 2*dot
}
