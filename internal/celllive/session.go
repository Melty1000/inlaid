// Package celllive turns reduced, timestamped camera planes into the one
// canonical CellFrame shared by terminal presentation and recording.
package celllive

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/cellreduce"
	"github.com/Melty1000/inlaid/internal/cellrender"
	"github.com/Melty1000/inlaid/internal/mfcapture"
)

const (
	defaultTargetFPS      = 30
	maxSyntheticDimension = 8192
)

// ViewConfig is intentionally terminal-native: dimensions are cells, not
// camera or export pixels.
type ViewConfig struct {
	Version uint64

	MaxColumns  int
	MaxRows     int
	Fill        bool
	Mirror      bool
	Paused      bool
	Mode        cellframe.Mode
	Transform   cellframe.ColorTransform
	TransformID string

	TargetFPS int
}

// Result owns one CellFrame lease. The receiver must Release Frame. ANSI is
// only the terminal projection of that same frame; it is never parsed by
// recording or snapshot code.
type Result struct {
	Version uint64
	Frame   *cellframe.CellFrame
	ANSI    string

	Columns, Rows int
	Sequence      uint64
	SourceFPS     float64
	ShownFPS      float64
	Dropped       uint64
	SolveDuration time.Duration
}

// SourceInfo is the real camera mode behind a session. It is presentation
// truth for the dashboard, not the preferred mode originally requested.
type SourceInfo struct {
	Width, Height int
	FPS           float64
	Format        string
	Backend       string
}

// Session owns capture, reduction, solving, and every frame it has not handed
// to a receiver.
type Session struct {
	Results <-chan Result
	Errors  <-chan error

	results   chan Result
	errors    chan error
	updates   chan ViewConfig
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	source    SourceInfo
}

// StartMediaFoundation opens the measured Windows native-MJPEG/WIC path.
func StartMediaFoundation(parent context.Context, camera mfcapture.Config, view ViewConfig) (*Session, error) {
	if parent == nil {
		parent = context.Background()
	}
	source, err := mfcapture.Open(parent, camera)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	session := newSession(cancel)
	mode := source.Mode()
	session.source = SourceInfo{Width: mode.Width, Height: mode.Height, FPS: mode.FPS(), Format: mode.Format, Backend: "Media Foundation"}
	go session.runMediaFoundation(ctx, source, normalizeView(view))
	return session, nil
}

// StartSynthetic runs a deterministic terminal-native source without a camera.
func StartSynthetic(parent context.Context, width, height, fps int, view ViewConfig) (*Session, error) {
	if width <= 0 || height <= 0 || width > maxSyntheticDimension || height > maxSyntheticDimension || fps <= 0 || fps > 240 {
		return nil, errors.New("celllive: invalid synthetic source")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	session := newSession(cancel)
	session.source = SourceInfo{Width: width, Height: height, FPS: float64(fps), Format: "synthetic", Backend: "Demo"}
	go session.runSynthetic(ctx, width, height, fps, normalizeView(view))
	return session, nil
}

// SourceInfo returns the immutable source mode selected before frame delivery
// starts.
func (s *Session) SourceInfo() SourceInfo {
	if s == nil {
		return SourceInfo{}
	}
	return s.source
}

func newSession(cancel context.CancelFunc) *Session {
	results := make(chan Result, 1)
	errorsCh := make(chan error, 2)
	return &Session{
		Results: results, Errors: errorsCh,
		results: results, errors: errorsCh,
		updates: make(chan ViewConfig, 1), cancel: cancel, done: make(chan struct{}),
	}
}

// Update installs only the newest pending view state and never blocks input.
func (s *Session) Update(view ViewConfig) {
	if s == nil {
		return
	}
	view = normalizeView(view)
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.updates <- view:
		return
	default:
	}
	select {
	case <-s.updates:
	default:
	}
	select {
	case s.updates <- view:
	case <-s.done:
	default:
	}
}

// Close is idempotent, cancels native capture, and releases any unconsumed
// canonical result.
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.cancel()
		<-s.done
		for result := range s.results {
			if result.Frame != nil {
				result.Frame.Release()
			}
		}
	})
}

type pipelineState struct {
	view          ViewConfig
	geometry      cellreduce.Geometry
	geometryEpoch uint64
	mode          cellframe.Mode
	transformID   string
	reducer       *cellreduce.Reducer
	solver        *cellframe.Solver
	synthetic     []cellframe.SampleStats
	stats         rateStats
}

func (p *pipelineState) update(view ViewConfig, sourceWidth, sourceHeight int) error {
	geometry := cellreduce.FitGeometry(sourceWidth, sourceHeight, view.MaxColumns, view.MaxRows, view.Fill)
	geometry.Mirror = view.Mirror
	changed := p.reducer == nil || geometry != p.geometry || view.Mode != p.mode || view.TransformID != p.transformID
	p.view = view
	if !changed {
		return nil
	}
	reducer, err := cellreduce.New(geometry)
	if err != nil {
		return err
	}
	solver, err := cellframe.NewSolver(cellframe.Config{
		// Eight bounded leases cover the producer, latest-wins handoffs, the
		// runtime's current snapshot, and an active CellTape reservation without
		// allocating on the steady video path.
		Columns: geometry.Columns, Rows: geometry.Rows, Mode: view.Mode, Transform: view.Transform, Buffers: 8,
	})
	if err != nil {
		return err
	}
	p.geometry, p.mode, p.transformID, p.reducer, p.solver = geometry, view.Mode, view.TransformID, reducer, solver
	p.synthetic = nil
	p.geometryEpoch++
	return nil
}

func (s *Session) runMediaFoundation(ctx context.Context, source *mfcapture.Session, view ViewConfig) {
	defer s.finish()
	defer source.Close()
	state := pipelineState{view: view}
	frames, sourceErrors := source.Frames, source.Errors
	var cadence cadenceGate
	for frames != nil || sourceErrors != nil {
		select {
		case <-ctx.Done():
			return
		case next := <-s.updates:
			state.view = next
			cadence.reset()
		case err, ok := <-sourceErrors:
			if !ok {
				sourceErrors = nil
				continue
			}
			if err != nil && ctx.Err() == nil {
				s.publishError(err)
			}
		case frame, ok := <-frames:
			if !ok {
				frames = nil
				continue
			}
			if frame == nil {
				continue
			}
			state.stats.observeSource(frame.Sequence, time.Now())
			pts := sourcePTS(frame)
			if state.view.Paused || !cadence.due(pts, state.view.TargetFPS) {
				frame.Release()
				continue
			}
			if err := state.update(state.view, frame.Y.Width, frame.Y.Height); err != nil {
				frame.Release()
				s.publishError(err)
				return
			}
			started := time.Now()
			statistics, err := state.reducer.ReduceYCbCr(cellreduce.YCbCr{
				Y: frame.Y.Pix, Cb: frame.Cb.Pix, Cr: frame.Cr.Pix,
				Width: frame.Y.Width, Height: frame.Y.Height, YStride: frame.Y.Stride,
				ChromaWidth: frame.Cb.Width, ChromaHeight: frame.Cb.Height,
				CbStride: frame.Cb.Stride, CrStride: frame.Cr.Stride,
			})
			if err != nil {
				frame.Release()
				s.publishError(err)
				return
			}
			canonical, err := state.solver.SolveStatistics(statistics, cellframe.SourceMeta{
				GeometryEpoch: state.geometryEpoch, SourceSequence: frame.Sequence,
				CapturedAt: time.Now(), PTS: pts,
			})
			frame.Release()
			if err != nil {
				s.publishError(err)
				return
			}
			ansi, err := cellrender.ANSI(canonical)
			if err != nil {
				canonical.Release()
				s.publishError(err)
				return
			}
			duration := time.Since(started)
			state.stats.observeShown(time.Now())
			captureStats := source.Stats()
			s.publish(Result{
				Version: state.view.Version, Frame: canonical, ANSI: ansi,
				Columns: canonical.Columns(), Rows: canonical.Rows(), Sequence: canonical.SourceSequence(),
				SourceFPS: state.stats.sourceFPS, ShownFPS: state.stats.shownFPS,
				Dropped:       captureStats.DroppedPackets + captureStats.DroppedFrames + captureStats.DecodeErrors + state.stats.presentationDropped,
				SolveDuration: duration,
			}, &state.stats)
		}
	}
}

func (s *Session) runSynthetic(ctx context.Context, sourceWidth, sourceHeight, fps int, view ViewConfig) {
	defer s.finish()
	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()
	state := pipelineState{view: view}
	var cadence cadenceGate
	var sequence uint64
	for {
		select {
		case <-ctx.Done():
			return
		case next := <-s.updates:
			state.view = next
			cadence.reset()
		case now := <-ticker.C:
			sequence++
			state.stats.observeSource(sequence, now)
			pts := time.Duration(sequence-1) * time.Second / time.Duration(fps)
			if state.view.Paused || !cadence.due(pts, state.view.TargetFPS) {
				continue
			}
			if err := state.update(state.view, sourceWidth, sourceHeight); err != nil {
				s.publishError(err)
				return
			}
			started := time.Now()
			statistics := state.syntheticStatistics(sequence)
			canonical, err := state.solver.SolveStatistics(statistics, cellframe.SourceMeta{
				GeometryEpoch: state.geometryEpoch, SourceSequence: sequence,
				CapturedAt: now, PTS: pts,
			})
			if err != nil {
				s.publishError(err)
				return
			}
			ansi, err := cellrender.ANSI(canonical)
			if err != nil {
				canonical.Release()
				s.publishError(err)
				return
			}
			state.stats.observeShown(time.Now())
			s.publish(Result{
				Version: state.view.Version, Frame: canonical, ANSI: ansi,
				Columns: canonical.Columns(), Rows: canonical.Rows(), Sequence: sequence,
				SourceFPS: state.stats.sourceFPS, ShownFPS: state.stats.shownFPS,
				Dropped: state.stats.presentationDropped, SolveDuration: time.Since(started),
			}, &state.stats)
		}
	}
}

func (p *pipelineState) syntheticStatistics(sequence uint64) cellframe.StatisticsFrame {
	columns, rows := p.geometry.Columns, p.geometry.Rows
	needed := columns * rows * 4
	if cap(p.synthetic) < needed {
		p.synthetic = make([]cellframe.SampleStats, needed)
	} else {
		p.synthetic = p.synthetic[:needed]
		clear(p.synthetic)
	}
	phase := int(sequence % 512)
	for y := 0; y < rows; y++ {
		for x := 0; x < columns; x++ {
			displayX := x
			if p.geometry.Mirror {
				displayX = columns - 1 - x
			}
			for quadrant := 0; quadrant < 4; quadrant++ {
				px, py := displayX*2+(quadrant&1), y*2+quadrant/2
				r := uint8((px*3 + phase*2) & 0xff)
				g := uint8((py*7 + phase) & 0xff)
				b := uint8(((px+py)*5 - phase) & 0xff)
				p.synthetic[(y*columns+x)*4+quadrant].AddRGB(r, g, b)
			}
		}
	}
	return cellframe.StatisticsFrame{Quadrants: p.synthetic, Columns: columns, Rows: rows}
}

func sourcePTS(frame *mfcapture.Frame) time.Duration {
	timestamp := frame.ReaderTimestamp100ns
	if frame.SampleTimestamp100ns >= 0 {
		timestamp = frame.SampleTimestamp100ns
	}
	if timestamp < 0 || timestamp > math.MaxInt64/100 {
		return 0
	}
	return time.Duration(timestamp * 100)
}

type cadenceGate struct {
	initialized bool
	previous    time.Duration
	next        time.Duration
}

func (g *cadenceGate) reset() { *g = cadenceGate{} }

// due uses an anchored presentation clock rather than measuring from the last
// accepted source frame. Measuring from the last accepted frame turns 30 -> 24
// FPS into 15 FPS because one 33 ms frame is rejected and the next is 66 ms
// away. The anchored deadline produces the intended evenly distributed 24/30
// cadence and skips arbitrarily large timestamp jumps in O(1).
func (g *cadenceGate) due(current time.Duration, fps int) bool {
	if fps <= 0 {
		return true
	}
	interval := time.Second / time.Duration(fps)
	if interval <= 0 {
		return true
	}
	if !g.initialized || current < g.previous {
		g.initialized = true
		g.previous = current
		g.next = saturatingAdd(current, interval)
		return true
	}
	if current == g.previous {
		return false
	}
	g.previous = current
	tolerance := min(time.Millisecond, interval/10)
	if current < g.next && g.next-current > tolerance {
		return false
	}
	deadline := g.next
	currentWithTolerance := saturatingAdd(current, tolerance)
	if deadline <= currentWithTolerance {
		steps := (currentWithTolerance-deadline)/interval + 1
		if steps > time.Duration(math.MaxInt64-deadline)/interval {
			g.next = time.Duration(math.MaxInt64)
		} else {
			g.next = deadline + steps*interval
		}
	}
	return true
}

func saturatingAdd(value, delta time.Duration) time.Duration {
	if delta > 0 && value > time.Duration(math.MaxInt64)-delta {
		return time.Duration(math.MaxInt64)
	}
	return value + delta
}

// cadenceDue retains the small stateless test seam while production uses the
// stateful anchored cadenceGate above.
func cadenceDue(last, current time.Duration, fps int) bool {
	if fps <= 0 || current < last {
		return true
	}
	interval := time.Second / time.Duration(fps)
	if interval <= 0 {
		return true
	}
	gate := cadenceGate{initialized: true, previous: last, next: saturatingAdd(last, interval)}
	return gate.due(current, fps)
}

func normalizeView(view ViewConfig) ViewConfig {
	// Clamping each axis before FitGeometry prevents a forged MaxInt x 1 resize
	// from entering its final integer-adjustment loop hundreds of billions of
	// times. The product remains bounded independently by cellreduce.MaxCells.
	view.MaxColumns = min(max(view.MaxColumns, 1), cellreduce.MaxCells)
	view.MaxRows = min(max(view.MaxRows, 1), cellreduce.MaxCells)
	if view.TargetFPS <= 0 || view.TargetFPS > 240 {
		view.TargetFPS = defaultTargetFPS
	}
	if view.Mode != cellframe.ModeDetailed && view.Mode != cellframe.ModeSoft {
		view.Mode = cellframe.ModeDetailed
	}
	return view
}

func (s *Session) publish(result Result, stats *rateStats) {
	select {
	case s.results <- result:
		return
	default:
	}
	select {
	case old := <-s.results:
		if old.Frame != nil {
			old.Frame.Release()
		}
		stats.presentationDropped++
		result.Dropped++
	default:
	}
	select {
	case s.results <- result:
	case <-s.done:
		if result.Frame != nil {
			result.Frame.Release()
		}
	}
}

func (s *Session) publishError(err error) {
	if err == nil {
		return
	}
	wrapped := fmt.Errorf("cell-native live pipeline: %w", err)
	select {
	case s.errors <- wrapped:
		return
	default:
	}
	// Recoverable sample-loss notices must never permanently hide the error
	// that actually ended the stream. When the tiny diagnostic queue is full,
	// retain its bounded behavior but make room for a terminal error.
	if mfcapture.IsTemporary(err) {
		return
	}
	select {
	case <-s.errors:
	default:
	}
	select {
	case s.errors <- wrapped:
	default:
	}
}

func (s *Session) finish() {
	close(s.results)
	close(s.errors)
	close(s.done)
}

type rateStats struct {
	windowStarted       time.Time
	baseSequence        uint64
	lastSequence        uint64
	shown               uint64
	sourceFPS           float64
	shownFPS            float64
	presentationDropped uint64
}

func (s *rateStats) observeSource(sequence uint64, now time.Time) {
	if s.windowStarted.IsZero() {
		s.windowStarted = now
		if sequence > 0 {
			s.baseSequence = sequence - 1
		}
	}
	s.lastSequence = sequence
	s.closeWindow(now)
}

func (s *rateStats) observeShown(now time.Time) {
	s.shown++
	s.closeWindow(now)
}

func (s *rateStats) closeWindow(now time.Time) {
	elapsed := now.Sub(s.windowStarted)
	if s.windowStarted.IsZero() || elapsed < time.Second {
		return
	}
	s.sourceFPS = float64(s.lastSequence-s.baseSequence) / elapsed.Seconds()
	s.shownFPS = float64(s.shown) / elapsed.Seconds()
	s.windowStarted, s.baseSequence, s.shown = now, s.lastSequence, 0
}
