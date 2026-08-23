package mfcapture

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// temporaryCaptureError marks a recoverable source-level packet loss. Native
// sources use it when one sample is malformed but the stream can be rearmed.
type temporaryCaptureError struct{ err error }

func (e temporaryCaptureError) Error() string { return e.err.Error() }
func (e temporaryCaptureError) Unwrap() error { return e.err }
func (temporaryCaptureError) Temporary() bool { return true }

// IsTemporary reports whether an Errors event describes a dropped sample from
// which the session continued. A non-temporary event terminates frame delivery.
func IsTemporary(err error) bool {
	var transient interface{ Temporary() bool }
	return errors.As(err, &transient) && transient.Temporary()
}

// Session owns a packet source, decoder, and bounded latest-frame queue.
type Session struct {
	Frames <-chan *Frame
	Errors <-chan error

	selectedMode Mode
	frames       chan *Frame
	errors       chan error
	source       Source
	decoder      Decoder
	cancel       context.CancelFunc
	done         chan struct{}
	closeOnce    sync.Once
	closeErr     error
	stats        counters
}

// StartPipeline starts the source/decoder seam used by Open and deterministic
// synthetic tests. QueueDepth controls the jitter/latest queue; when full, the
// oldest queued frame is released before the newest is published.
func StartPipeline(parent context.Context, cfg Config, source Source, decoder Decoder) (*Session, error) {
	normalized, err := normalize(cfg, false)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, errors.New("packet source is required")
	}
	if decoder == nil {
		return nil, errors.New("planar decoder is required")
	}
	ctx, cancel := context.WithCancel(parent)
	frames := make(chan *Frame, normalized.QueueDepth)
	errorsCh := make(chan error, 4)
	session := &Session{
		Frames:       frames,
		Errors:       errorsCh,
		selectedMode: Mode{Width: normalized.Width, Height: normalized.Height, FPSNumerator: uint32(normalized.FPS), FPSDenominator: 1, Format: "MJPG"},
		frames:       frames,
		errors:       errorsCh,
		source:       source,
		decoder:      decoder,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	go session.run(ctx, normalized)
	return session, nil
}

func (s *Session) run(ctx context.Context, cfg Config) {
	defer close(s.done)
	defer close(s.frames)
	defer close(s.errors)
	defer func() {
		if err := s.decoder.Close(); err != nil {
			s.report(fmt.Errorf("close decoder: %w", err))
		}
		if err := s.source.Close(); err != nil {
			s.report(fmt.Errorf("close source: %w", err))
		}
	}()

	packets := s.source.Packets()
	sourceErrors := s.source.Errors()
	watchdog := time.NewTimer(frameWatchdogTimeout(cfg.FPS, true))
	defer watchdog.Stop()
	consecutiveDecodeErrors := 0
	var sequence uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-watchdog.C:
			timeout := frameWatchdogTimeout(cfg.FPS, sequence == 0)
			s.report(fmt.Errorf("no decodable camera frame arrived for %s", timeout))
			return
		case err, ok := <-sourceErrors:
			if !ok {
				sourceErrors = nil
				continue
			}
			if err != nil {
				s.report(fmt.Errorf("capture source: %w", err))
				if IsTemporary(err) {
					continue
				}
				return
			}
		case packet, ok := <-packets:
			if !ok {
				return
			}
			s.stats.packets.Add(1)
			if len(packet.Data) == 0 || len(packet.Data) > cfg.MaxPacketBytes {
				packet.Release()
				s.stats.droppedPackets.Add(1)
				s.report(temporaryCaptureError{err: fmt.Errorf("native MJPEG packet length %d is outside 1..%d", len(packet.Data), cfg.MaxPacketBytes)})
				continue
			}
			frame, err := s.decoder.Decode(ctx, packet)
			// Decoder.Decode is synchronous with respect to Packet.Data. Native
			// byte storage can be reused as soon as it returns, independently of
			// the longer-lived planar Frame lease.
			packet.Release()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.stats.decodeErrors.Add(1)
				consecutiveDecodeErrors++
				if consecutiveDecodeErrors >= cfg.MaxConsecutiveErrors {
					s.report(fmt.Errorf("decode native MJPEG failed %d consecutive times: %w", consecutiveDecodeErrors, err))
					return
				}
				s.report(temporaryCaptureError{err: fmt.Errorf("decode native MJPEG: %w", err)})
				continue
			}
			if frame == nil {
				s.stats.decodeErrors.Add(1)
				consecutiveDecodeErrors++
				if consecutiveDecodeErrors >= cfg.MaxConsecutiveErrors {
					s.report(fmt.Errorf("decoder returned a nil frame %d consecutive times", consecutiveDecodeErrors))
					return
				}
				s.report(temporaryCaptureError{err: errors.New("decoder returned a nil frame")})
				continue
			}
			consecutiveDecodeErrors = 0
			sequence++
			frame.Sequence = sequence
			s.stats.decoded.Add(1)
			resetTimer(watchdog, frameWatchdogTimeout(cfg.FPS, false))
			s.publishLatest(frame)
		}
	}
}

// frameWatchdogTimeout is deliberately based on decoded frames, below every
// presentation pause or cadence gate. A paused UI therefore cannot trip it,
// while a silent driver stall or an endless run of malformed samples cannot
// leave the dashboard claiming LIVE forever.
func frameWatchdogTimeout(fps int, initial bool) time.Duration {
	if fps < 1 {
		fps = 1
	}
	frames, floor := 12, 3*time.Second
	if initial {
		frames, floor = 24, 8*time.Second
	}
	window := time.Duration(frames) * time.Second / time.Duration(fps)
	return max(window, floor)
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func (s *Session) publishLatest(next *Frame) {
	select {
	case s.frames <- next:
		return
	default:
	}
	select {
	case old := <-s.frames:
		s.stats.droppedFrames.Add(1)
		old.Release()
	default:
	}
	select {
	case s.frames <- next:
	default:
		s.stats.droppedFrames.Add(1)
		next.Release()
	}
}

func (s *Session) report(err error) {
	publishBoundedError(s.errors, err)
}

// publishBoundedError keeps diagnostic traffic bounded without allowing a
// recoverable dropped-frame notice to hide the error that actually ended the
// camera stream. Transient events may be dropped when the queue is full; a
// terminal event evicts one older item and is always given the slot.
func publishBoundedError(destination chan error, err error) {
	if err == nil {
		return
	}
	select {
	case destination <- err:
		return
	default:
	}
	if IsTemporary(err) {
		return
	}
	select {
	case <-destination:
	default:
	}
	select {
	case destination <- err:
	default:
	}
}

func (s *Session) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	stats := s.stats.snapshot()
	if source, ok := s.source.(interface{ DroppedPackets() uint64 }); ok {
		stats.DroppedPackets += source.DroppedPackets()
	}
	return stats
}

// Mode returns the native MJPEG geometry and exact rational frame rate chosen
// for this session.
func (s *Session) Mode() Mode {
	if s == nil {
		return Mode{}
	}
	return s.selectedMode
}

// Close cancels capture, flushes the native asynchronous reader through Source,
// waits for ownership shutdown, and releases any still-queued pooled frames.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.cancel()
		if err := s.source.Close(); err != nil {
			s.closeErr = err
		}
		<-s.done
		for frame := range s.frames {
			frame.Release()
		}
	})
	return s.closeErr
}

// SyntheticSource is a deterministic bounded packet source for tests and
// diagnostics that exercise the production pipeline without a camera.
type SyntheticSource struct {
	packets   chan Packet
	errors    chan error
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
}

func NewSyntheticSource(depth int) (*SyntheticSource, error) {
	if depth < 1 || depth > maxQueueDepth {
		return nil, fmt.Errorf("synthetic source depth must be within 1..%d", maxQueueDepth)
	}
	return &SyntheticSource{packets: make(chan Packet, depth), errors: make(chan error, 1)}, nil
}

func (s *SyntheticSource) Packets() <-chan Packet { return s.packets }
func (s *SyntheticSource) Errors() <-chan error   { return s.errors }

func (s *SyntheticSource) Push(packet Packet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("synthetic source is closed")
	}
	select {
	case s.packets <- packet:
		return nil
	default:
		return errors.New("synthetic source packet queue is full")
	}
}

func (s *SyntheticSource) Fail(err error) error {
	if err == nil {
		return errors.New("synthetic source failure cannot be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("synthetic source is closed")
	}
	select {
	case s.errors <- err:
		return nil
	default:
		return errors.New("synthetic source error queue is full")
	}
}

func (s *SyntheticSource) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.packets)
		for packet := range s.packets {
			packet.Release()
		}
		close(s.errors)
		s.mu.Unlock()
	})
	return nil
}
