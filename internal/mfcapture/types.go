// Package mfcapture provides the production Windows camera boundary: stable
// Media Foundation device selection, native timestamped MJPEG samples, reduced
// planar Microsoft WIC decode, and bounded latest-frame delivery.
package mfcapture

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxDimension        = 8192
	maxQueueDepth       = 32
	defaultMaxPacket    = 16 << 20
	defaultPacketPool   = 64 << 20
	maximumPacketPool   = 1 << 30
	defaultMaxFrame     = 64 << 20
	defaultMaxPool      = 256 << 20
	defaultCloseTimeout = 3 * time.Second
	defaultDecodeErrors = 3
)

// Device is a Media Foundation video-capture device. ID is the stable symbolic
// link that must be retained for subsequent Open calls; Name is presentation
// text only.
type Device struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// Mode is the native MJPEG mode selected for a capture session. The rational
// frame rate is retained exactly because many cameras advertise NTSC-derived
// rates such as 30000/1001 rather than an integer 30 fps.
type Mode struct {
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	FPSNumerator   uint32 `json:"fps_numerator"`
	FPSDenominator uint32 `json:"fps_denominator"`
	Format         string `json:"format"`
}

// FPS returns the exact rational frame rate as a presentation value.
func (m Mode) FPS() float64 {
	if m.FPSDenominator == 0 {
		return 0
	}
	return float64(m.FPSNumerator) / float64(m.FPSDenominator)
}

// NominalFPS returns the nearest integer cadence used for watchdog and camera
// control timing. Mode retains the exact rational value for verification.
func (m Mode) NominalFPS() int {
	if m.FPSDenominator == 0 {
		return 0
	}
	return int((uint64(m.FPSNumerator) + uint64(m.FPSDenominator)/2) / uint64(m.FPSDenominator))
}

// Config fixes the stable camera identity and requests a preferred native
// mode. Open never substitutes another device or a non-MJPEG subtype. When an
// exact native mode is unavailable, it deterministically chooses the closest
// usable MJPEG mode and exposes that choice through Session.Mode.
type Config struct {
	DeviceID string `json:"device_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FPS      int    `json:"fps"`
	// AllowVariableFrameRate preserves automatic exposure even when a camera
	// chooses an exposure longer than the negotiated frame interval. The
	// default false temporarily bounds exposure for the requested FPS.
	AllowVariableFrameRate bool          `json:"allow_variable_frame_rate"`
	Lowres                 int           `json:"lowres"`
	QueueDepth             int           `json:"queue_depth"`
	PacketQueueDepth       int           `json:"packet_queue_depth"`
	MaxPacketBytes         int           `json:"max_packet_bytes"`
	MaxPacketPoolBytes     int           `json:"max_packet_pool_bytes"`
	MaxFrameBytes          int           `json:"max_frame_bytes"`
	MaxPoolBytes           int           `json:"max_pool_bytes"`
	MaxConsecutiveErrors   int           `json:"max_consecutive_decode_errors"`
	CloseTimeout           time.Duration `json:"close_timeout"`
}

func DefaultConfig() Config {
	return Config{
		Width:                1920,
		Height:               1080,
		FPS:                  30,
		Lowres:               2,
		QueueDepth:           4,
		PacketQueueDepth:     2,
		MaxPacketBytes:       defaultMaxPacket,
		MaxPacketPoolBytes:   defaultPacketPool,
		MaxFrameBytes:        defaultMaxFrame,
		MaxPoolBytes:         defaultMaxPool,
		MaxConsecutiveErrors: defaultDecodeErrors,
		CloseTimeout:         defaultCloseTimeout,
	}
}

func normalize(cfg Config, requireDevice bool) (Config, error) {
	defaults := DefaultConfig()
	if cfg.Width == 0 {
		cfg.Width = defaults.Width
	}
	if cfg.Height == 0 {
		cfg.Height = defaults.Height
	}
	if cfg.FPS == 0 {
		cfg.FPS = defaults.FPS
	}
	if cfg.Lowres == 0 {
		cfg.Lowres = defaults.Lowres
	}
	if cfg.QueueDepth == 0 {
		cfg.QueueDepth = defaults.QueueDepth
	}
	if cfg.PacketQueueDepth == 0 {
		cfg.PacketQueueDepth = defaults.PacketQueueDepth
	}
	if cfg.MaxPacketBytes == 0 {
		cfg.MaxPacketBytes = defaults.MaxPacketBytes
	}
	if cfg.MaxPacketPoolBytes == 0 {
		// Hold a complete latest-wins packet queue plus the sample currently
		// being decoded when practical. The reservation is only an upper bound;
		// bucket storage is allocated lazily at the observed MJPEG sizes.
		wanted := int64(cfg.MaxPacketBytes) * int64(cfg.PacketQueueDepth+1)
		if wanted < defaultPacketPool {
			wanted = defaultPacketPool
		}
		if wanted > maximumPacketPool {
			wanted = maximumPacketPool
		}
		cfg.MaxPacketPoolBytes = int(wanted)
	}
	if cfg.MaxFrameBytes == 0 {
		cfg.MaxFrameBytes = defaults.MaxFrameBytes
	}
	if cfg.MaxPoolBytes == 0 {
		cfg.MaxPoolBytes = defaults.MaxPoolBytes
	}
	if cfg.MaxConsecutiveErrors == 0 {
		cfg.MaxConsecutiveErrors = defaults.MaxConsecutiveErrors
	}
	if cfg.CloseTimeout == 0 {
		cfg.CloseTimeout = defaults.CloseTimeout
	}
	if requireDevice && cfg.DeviceID == "" {
		return Config{}, errors.New("exact Media Foundation device ID is required")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxDimension || cfg.Height > maxDimension {
		return Config{}, fmt.Errorf("dimensions must be within 1..%d", maxDimension)
	}
	if cfg.FPS < 1 || cfg.FPS > 240 {
		return Config{}, errors.New("fps must be within 1..240")
	}
	if cfg.Lowres < 1 || cfg.Lowres > 3 {
		return Config{}, errors.New("lowres must be 1, 2, or 3")
	}
	if cfg.QueueDepth < 1 || cfg.QueueDepth > maxQueueDepth {
		return Config{}, fmt.Errorf("queue depth must be within 1..%d", maxQueueDepth)
	}
	if cfg.PacketQueueDepth < 1 || cfg.PacketQueueDepth > maxQueueDepth {
		return Config{}, fmt.Errorf("packet queue depth must be within 1..%d", maxQueueDepth)
	}
	if cfg.MaxPacketBytes < 1024 || cfg.MaxPacketBytes > 256<<20 {
		return Config{}, errors.New("max packet bytes must be within 1 KiB..256 MiB")
	}
	if cfg.MaxPacketPoolBytes < cfg.MaxPacketBytes || int64(cfg.MaxPacketPoolBytes) > maximumPacketPool {
		return Config{}, errors.New("max packet pool bytes must cover one max-size packet and be at most 1 GiB")
	}
	if cfg.MaxFrameBytes < 1024 || cfg.MaxFrameBytes > 512<<20 {
		return Config{}, errors.New("max frame bytes must be within 1 KiB..512 MiB")
	}
	if cfg.MaxPoolBytes < 1<<20 || int64(cfg.MaxPoolBytes) > 2<<30 {
		return Config{}, errors.New("max pool bytes must be within 1 MiB..2 GiB")
	}
	if cfg.MaxConsecutiveErrors < 1 || cfg.MaxConsecutiveErrors > 100 {
		return Config{}, errors.New("max consecutive decode errors must be within 1..100")
	}
	if cfg.CloseTimeout < 100*time.Millisecond || cfg.CloseTimeout > 30*time.Second {
		return Config{}, errors.New("close timeout must be within 100ms..30s")
	}
	reducedWidth := reducedDimension(cfg.Width, cfg.Lowres)
	reducedHeight := reducedDimension(cfg.Height, cfg.Lowres)
	// Three full-size 8-bit planes is a conservative allocation upper bound.
	if int64(reducedWidth)*int64(reducedHeight)*3 > int64(cfg.MaxFrameBytes) {
		return Config{}, fmt.Errorf("reduced planar frame exceeds %d-byte bound", cfg.MaxFrameBytes)
	}
	return cfg, nil
}

func reducedDimension(value, lowres int) int {
	divisor := 1 << lowres
	return (value + divisor - 1) / divisor
}

// Packet owns a complete native MJPEG IMFSample copy and the original Media
// Foundation timing/boundary metadata.
type Packet struct {
	Data                 []byte
	ReaderTimestamp100ns int64
	SampleTimestamp100ns int64
	SampleDuration100ns  int64
	DurationKnown        bool
	ReaderFlags          uint32
	SampleFlags          uint32
	BufferCount          uint32

	owner      *packetBuffer
	ownerToken uint64
}

// Release returns native MJPEG storage to its bounded source pool. Packets
// created by tests or alternate Sources without pooled ownership are no-ops.
// A Source transfers ownership after a successful channel send; the pipeline
// releases that ownership after Decode has synchronously finished with Data.
func (p Packet) Release() {
	if p.owner == nil || p.ownerToken == 0 {
		return
	}
	if p.owner.token.CompareAndSwap(p.ownerToken, 0) {
		p.owner.pool.release(p.owner)
	}
}

// packetPool reuses power-of-two byte buckets and never reserves more than
// maxBytes. Tokens make duplicate or stale Packet.Release calls harmless even
// after the same buffer has been leased to a later sample.
type packetPool struct {
	maxBytes int

	mu        sync.Mutex
	reserved  int
	free      map[int][]*packetBuffer
	closed    bool
	nextToken atomic.Uint64
}

type packetBuffer struct {
	data  []byte
	pool  *packetPool
	token atomic.Uint64
}

func newPacketPool(maxBytes int) *packetPool {
	return &packetPool{maxBytes: maxBytes, free: make(map[int][]*packetBuffer)}
}

func (p *packetPool) acquire(size int) (Packet, error) {
	if p == nil || p.maxBytes < 1 {
		return Packet{}, errors.New("native MJPEG packet pool is unavailable")
	}
	if size < 1 || size > p.maxBytes {
		return Packet{}, fmt.Errorf("native MJPEG packet requires %d bytes, pool bound is %d", size, p.maxBytes)
	}
	bucket := packetBucketSize(size)
	if bucket > p.maxBytes {
		// The final non-power-of-two range can still use its exact configured
		// bound without silently accepting more memory than requested.
		bucket = p.maxBytes
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return Packet{}, errors.New("native MJPEG packet pool is closed")
	}
	best := 0
	for capacity, buffers := range p.free {
		if len(buffers) > 0 && capacity >= bucket && (best == 0 || capacity < best) {
			best = capacity
		}
	}
	var buffer *packetBuffer
	if best != 0 {
		buffers := p.free[best]
		buffer = buffers[len(buffers)-1]
		p.free[best] = buffers[:len(buffers)-1]
	} else {
		if p.reserved+bucket > p.maxBytes {
			// A larger observed frame supersedes free undersized buckets. Drop
			// those reservations once so the pool converges on the new size.
			for capacity, buffers := range p.free {
				p.reserved -= capacity * len(buffers)
				delete(p.free, capacity)
			}
		}
		if p.reserved+bucket > p.maxBytes {
			p.mu.Unlock()
			return Packet{}, fmt.Errorf("native MJPEG packet pool is full: need %d bytes with %d/%d reserved", bucket, p.reserved, p.maxBytes)
		}
		buffer = &packetBuffer{data: make([]byte, bucket), pool: p}
		p.reserved += bucket
	}
	token := p.nextToken.Add(1)
	if token == 0 {
		token = p.nextToken.Add(1)
	}
	buffer.token.Store(token)
	p.mu.Unlock()
	return Packet{Data: buffer.data[:size], owner: buffer, ownerToken: token}, nil
}

func (p *packetPool) release(buffer *packetBuffer) {
	if p == nil || buffer == nil || buffer.pool != p {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.reserved -= cap(buffer.data)
		p.mu.Unlock()
		return
	}
	p.free[cap(buffer.data)] = append(p.free[cap(buffer.data)], buffer)
	p.mu.Unlock()
}

func (p *packetPool) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		for capacity, buffers := range p.free {
			p.reserved -= capacity * len(buffers)
		}
		p.free = nil
	}
	p.mu.Unlock()
}

func packetBucketSize(size int) int {
	bucket := 4 << 10
	for bucket < size {
		bucket <<= 1
	}
	return bucket
}

// Plane is one tightly bounded WIC output plane. Pix is valid until its Frame
// is released.
type Plane struct {
	Pix    []byte
	Width  int
	Height int
	Stride int
}

// Frame owns pooled reduced Y/Cb/Cr buffers. Call Release exactly once when the
// consumer is done; Release is idempotent for defensive shutdown paths.
type Frame struct {
	Y, Cb, Cr            Plane
	Sequence             uint64
	ReaderTimestamp100ns int64
	SampleTimestamp100ns int64
	SampleDuration100ns  int64
	DurationKnown        bool
	ReaderFlags          uint32
	SampleFlags          uint32
	SourceBufferCount    uint32

	releaseOnce sync.Once
	release     func()
}

func (f *Frame) Release() {
	if f == nil {
		return
	}
	f.releaseOnce.Do(func() {
		if f.release != nil {
			f.release()
		}
	})
}

// Source is the test seam and native packet-source ownership contract. A
// successful receive from Packets transfers one Packet lease to the consumer;
// the Source releases queued leases that were never received. Close must be
// idempotent and unblock/finish packet production.
type Source interface {
	Packets() <-chan Packet
	Errors() <-chan error
	Close() error
}

// Decoder is the test seam for pooled planar decoding. Decode must not retain
// Packet.Data after it returns. Close must be idempotent and unblock Decode.
type Decoder interface {
	Decode(context.Context, Packet) (*Frame, error)
	Close() error
}

type Stats struct {
	Packets        uint64
	Decoded        uint64
	DroppedPackets uint64
	DroppedFrames  uint64
	DecodeErrors   uint64
}

type counters struct {
	packets, decoded, droppedPackets, droppedFrames, decodeErrors atomic.Uint64
}

func (c *counters) snapshot() Stats {
	return Stats{Packets: c.packets.Load(), Decoded: c.decoded.Load(), DroppedPackets: c.droppedPackets.Load(), DroppedFrames: c.droppedFrames.Load(), DecodeErrors: c.decodeErrors.Load()}
}
