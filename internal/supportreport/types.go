// Package supportreport creates bounded, local-only support reports from
// explicitly typed facts. It has no upload or telemetry capability.
package supportreport

import (
	"errors"
	"time"
)

const (
	SchemaV1          = "inlaid.support-report.v1"
	MaxReportBytes    = 256 << 10
	MaxRecentEvents   = 128
	MaxRecentSamples  = 120
	reportDirectory   = "support-reports"
	maxCameraNameRune = 96
)

var ErrReportExists = errors.New("support report already exists")

type Area string

const (
	AreaUnknown   Area = "unknown"
	AreaCamera    Area = "camera"
	AreaPreview   Area = "preview"
	AreaRecording Area = "recording"
	AreaRecovery  Area = "recovery"
	AreaSettings  Area = "settings"
	AreaLooks     Area = "looks"
)

type Code string

const (
	CodeUnknown           Code = "unknown"
	CodeDiscoveryFailed   Code = "discovery_failed"
	CodePermissionDenied  Code = "permission_denied"
	CodeCameraBusy        Code = "camera_busy"
	CodeUnsupportedMode   Code = "unsupported_mode"
	CodeStreamStalled     Code = "stream_stalled"
	CodeStreamEnded       Code = "stream_ended"
	CodeShutdownUncertain Code = "shutdown_uncertain"
	CodeDecodeFailed      Code = "decode_failed"
	CodeFramesDropped     Code = "frames_dropped"
	CodePreviewSlow       Code = "preview_slow"
	CodeQueuePressure     Code = "queue_pressure"
	CodeFFmpegMissing     Code = "ffmpeg_missing"
	CodeFFmpegFailed      Code = "ffmpeg_failed"
	CodeDiskFull          Code = "disk_full"
	CodeSaveFailed        Code = "save_failed"
	CodeRecoveryFailed    Code = "recovery_failed"
	CodeSettingsFailed    Code = "settings_failed"
	CodeLookRejected      Code = "look_rejected"
	CodeCompleted         Code = "completed"
)

type Severity string

const (
	SeverityUnknown Severity = "unknown"
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type BuildFacts struct {
	Version  string
	Revision string
	Modified bool
}

type Include struct {
	CameraModel bool
}

type Event struct {
	OccurredAt time.Time
	Area       Area
	Code       Code
	Severity   Severity
	NativeCode uint64
	Repeat     uint32
}

type CaptureHealth struct {
	Packets         uint64 `json:"packets"`
	Decoded         uint64 `json:"decoded"`
	DroppedPackets  uint64 `json:"dropped_packets"`
	DroppedFrames   uint64 `json:"dropped_frames"`
	DecodeErrors    uint64 `json:"decode_errors"`
	TemporaryErrors uint64 `json:"temporary_errors"`
}

type PresentationHealth struct {
	Accepted uint64 `json:"accepted"`
	Dropped  uint64 `json:"dropped"`
}

type QueueHealth struct {
	Active    bool `json:"active"`
	InFlight  int  `json:"in_flight"`
	HighWater int  `json:"high_water"`
	Capacity  int  `json:"capacity"`
}

type ProcessHealth struct {
	HeapBytes     uint64 `json:"heap_bytes"`
	ResidentBytes uint64 `json:"resident_bytes"`
	Goroutines    int    `json:"goroutines"`
	GCCycles      uint32 `json:"gc_cycles"`
}

type Sample struct {
	ObservedAt   time.Time
	SourceFPS    float64
	ShownFPS     float64
	GridColumns  int
	GridRows     int
	Capture      CaptureHealth
	Presentation PresentationHealth
	Queue        QueueHealth
	Process      ProcessHealth
}

type ModeFacts struct {
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	FPSNumerator   uint32 `json:"fps_numerator"`
	FPSDenominator uint32 `json:"fps_denominator"`
	Format         string `json:"format"`
}

type CameraFacts struct {
	Model       string
	Backend     string
	Requested   ModeFacts
	Selected    ModeFacts
	Downsample  int
	PixelLayout string
	ColorRange  string
	ColorMatrix string
	Permission  string
	DeviceCount int
}

type ViewFacts struct {
	GridColumns int
	GridRows    int
	Framing     string
	Mirror      bool
	Detail      string
	Look        string
	LookMix     int
	TargetFPS   int
}

type FFmpegFacts struct {
	Available   bool
	Origin      string
	Version     string
	HasExitCode bool
	ExitCode    int
}

type RecordingFacts struct {
	State          string
	Format         string
	Width          int
	Height         int
	FPS            int
	Quality        string
	DurationMillis int64
	Result         Code
	FFmpeg         FFmpegFacts
}

type Current struct {
	Camera    CameraFacts
	View      ViewFacts
	Recording RecordingFacts
}

type Review struct {
	Schema              string
	Bytes               int
	SHA256              string
	CameraModelIncluded bool
	Includes            []string
	Excludes            []string
}

type Prepared struct {
	data      []byte
	createdAt time.Time
	digest    [32]byte
}

func (p Prepared) Content() []byte {
	return append([]byte(nil), p.data...)
}

type Saved struct {
	Path   string
	Bytes  int
	SHA256 string
}
