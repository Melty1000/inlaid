package cellframe

import (
	"errors"
)

// DeadbandConfig bounds optional temporal cell reuse. A previous cell may be
// kept only while its current-frame error is within MaxCellErrorIncrease of the
// spatial optimum, for no more than MaxAge consecutive output frames. A mean
// squared channel error at or above SceneCutMSE disables all reuse immediately.
type DeadbandConfig struct {
	MaxCellErrorIncrease uint64
	SceneCutMSE          uint32
	MaxAge               uint16
}

// DeadbandPolicy is explicit single-stream temporal state. It is intentionally
// separate from Solver so the base spatial solution remains pure and directly
// testable. It must not be used concurrently.
type DeadbandPolicy struct {
	columns int
	rows    int
	config  DeadbandConfig
	cells   []Cell
	ages    []uint16

	initialized   bool
	sceneCut      bool
	geometryEpoch uint64
	lastSequence  uint64
}

// NewDeadbandPolicy preallocates state for fixed geometry.
func NewDeadbandPolicy(columns, rows int, cfg DeadbandConfig) (*DeadbandPolicy, error) {
	if columns <= 0 || rows <= 0 || columns > maxCells/rows {
		return nil, errors.New("cellframe: invalid deadband geometry")
	}
	if cfg.MaxAge == 0 {
		return nil, errors.New("cellframe: deadband MaxAge must be positive")
	}
	if cfg.SceneCutMSE == 0 || uint64(cfg.SceneCutMSE) > 255*255 {
		return nil, errors.New("cellframe: deadband SceneCutMSE must be between 1 and 65025")
	}
	count := columns * rows
	return &DeadbandPolicy{
		columns: columns,
		rows:    rows,
		config:  cfg,
		cells:   make([]Cell, count),
		ages:    make([]uint16, count),
	}, nil
}

// Reset forgets all temporal state. The next solve is exactly the base spatial
// solution.
func (p *DeadbandPolicy) Reset() {
	if p == nil {
		return
	}
	p.initialized = false
	p.sceneCut = false
}

func (p *DeadbandPolicy) prepare(source spatialSource, meta SourceMeta) {
	p.sceneCut = false
	if !p.initialized || p.geometryEpoch != meta.GeometryEpoch || meta.SourceSequence <= p.lastSequence {
		p.initialized = false
		return
	}
	var errorSum uint64
	cellIndex := 0
	for y := 0; y < p.rows; y++ {
		for x := 0; x < p.columns; x++ {
			quadrants := source.cell(p.columns, x, y, cellIndex)
			errorSum += cellErrorUnchecked(p.cells[cellIndex], quadrants)
			cellIndex++
		}
	}
	// SceneCutMSE is per RGB channel, not per RGB vector.
	threshold := uint64(p.config.SceneCutMSE) * source.totalSamples * 3
	if errorSum >= threshold {
		p.initialized = false
		p.sceneCut = true
	}
}

func (p *DeadbandPolicy) choose(index int, quadrants [4]SampleStats, optimum Cell, optimumError uint64) (Cell, uint64) {
	if p.initialized && !p.sceneCut && p.ages[index] < p.config.MaxAge {
		previous := p.cells[index]
		previousError := cellErrorUnchecked(previous, quadrants)
		if previousError >= optimumError && previousError-optimumError <= p.config.MaxCellErrorIncrease {
			p.ages[index]++
			return previous, previousError
		}
	}
	p.cells[index] = optimum
	p.ages[index] = 0
	return optimum, optimumError
}

func (p *DeadbandPolicy) finish(meta SourceMeta) {
	p.geometryEpoch = meta.GeometryEpoch
	p.lastSequence = meta.SourceSequence
	p.initialized = true
	p.sceneCut = false
}
