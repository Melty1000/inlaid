package supportreport

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

func safeToken(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit {
		return ""
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._+-", r)) {
			return ""
		}
	}
	return value
}

func safeDottedVersion(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit || value[0] == '.' || value[len(value)-1] == '.' {
		return ""
	}
	previousDot := false
	for _, r := range value {
		if r == '.' {
			if previousDot {
				return ""
			}
			previousDot = true
			continue
		}
		if r < '0' || r > '9' {
			return ""
		}
		previousDot = false
	}
	return value
}

func safeCameraModel(value string) string {
	if !utf8.ValidString(value) {
		return ""
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{
		`\`, "/", "..", "file://", "http://", "https://",
		"usb#", "vid_", "pid_", "device_id", "serial=", "token=", "secret=", "@",
	} {
		if strings.Contains(lower, forbidden) {
			return ""
		}
	}
	if containsGUID(lower) {
		return ""
	}
	var builder strings.Builder
	space := false
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ""
		}
		if unicode.IsSpace(r) {
			space = builder.Len() > 0
			continue
		}
		if space {
			builder.WriteByte(' ')
			space = false
		}
		builder.WriteRune(r)
	}
	result := strings.TrimSpace(builder.String())
	runes := []rune(result)
	if len(runes) == 0 || len(runes) > maxCameraNameRune {
		return ""
	}
	return result
}

func containsGUID(value string) bool {
	for start := 0; start+36 <= len(value); start++ {
		candidate := value[start : start+36]
		valid := true
		for index, r := range candidate {
			switch index {
			case 8, 13, 18, 23:
				valid = r == '-'
			default:
				valid = (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
			}
			if !valid {
				break
			}
		}
		if valid {
			return true
		}
	}
	return false
}

func safeArea(value Area) Area {
	switch value {
	case AreaCamera, AreaPreview, AreaRecording, AreaRecovery, AreaSettings, AreaLooks:
		return value
	default:
		return AreaUnknown
	}
}

func safeCode(value Code) Code {
	switch value {
	case CodeDiscoveryFailed, CodePermissionDenied, CodeCameraBusy, CodeUnsupportedMode,
		CodeStreamStalled, CodeStreamEnded, CodeShutdownUncertain, CodeDecodeFailed,
		CodeFramesDropped, CodePreviewSlow, CodeQueuePressure, CodeFFmpegMissing,
		CodeFFmpegFailed, CodeDiskFull, CodeSaveFailed, CodeRecoveryFailed,
		CodeSettingsFailed, CodeLookRejected, CodeCompleted:
		return value
	default:
		return CodeUnknown
	}
}

func safeSeverity(value Severity) Severity {
	switch value {
	case SeverityInfo, SeverityWarning, SeverityError:
		return value
	default:
		return SeverityUnknown
	}
}

func safeMode(value ModeFacts) ModeFacts {
	value.Width = boundedInt(value.Width, 0, 8192)
	value.Height = boundedInt(value.Height, 0, 8192)
	if value.FPSNumerator > 240_000 || value.FPSDenominator > 100_000 {
		value.FPSNumerator, value.FPSDenominator = 0, 0
	}
	switch strings.ToUpper(strings.TrimSpace(value.Format)) {
	case "MJPG", "NV12", "YUY2", "UYVY", "RGB24", "RGBA", "DEMO":
		value.Format = strings.ToUpper(strings.TrimSpace(value.Format))
	default:
		value.Format = "unknown"
	}
	return value
}

func safeCurrent(value Current, include Include) currentV1 {
	model := ""
	if include.CameraModel {
		model = safeCameraModel(value.Camera.Model)
	}
	camera := cameraV1{
		Model: model, ModelIncluded: model != "",
		Backend:   safeChoice(value.Camera.Backend, "unknown", "media-foundation", "v4l2", "avfoundation", "demo"),
		Requested: safeMode(value.Camera.Requested), Selected: safeMode(value.Camera.Selected),
		Downsample:  boundedChoice(value.Camera.Downsample, 0, 2, 4, 8),
		PixelLayout: safeChoice(value.Camera.PixelLayout, "unknown", "planar-ycbcr", "nv12", "demo"),
		ColorRange:  safeChoice(value.Camera.ColorRange, "unknown", "full", "limited"),
		ColorMatrix: safeChoice(value.Camera.ColorMatrix, "unknown", "bt601", "bt709", "bt2020"),
		Permission:  safeChoice(value.Camera.Permission, "unknown", "granted", "denied", "not-requested", "restricted"),
		DeviceCount: boundedInt(value.Camera.DeviceCount, 0, 128),
	}
	view := viewV1{
		GridColumns: boundedInt(value.View.GridColumns, 0, 600),
		GridRows:    boundedInt(value.View.GridRows, 0, 200),
		Framing:     safeChoice(value.View.Framing, "unknown", "fill", "whole"),
		Mirror:      value.View.Mirror,
		Detail:      safeChoice(value.View.Detail, "unknown", "soft", "balanced", "crisp"),
		Look:        safeChoice(value.View.Look, "unknown", "none", "built-in", "custom"),
		LookMix:     boundedInt(value.View.LookMix, 0, 100),
		TargetFPS:   boundedInt(value.View.TargetFPS, 0, 240),
	}
	recording := recordingV1{
		State:  safeChoice(value.Recording.State, "unknown", "idle", "starting", "recording", "saving", "error"),
		Format: safeChoice(value.Recording.Format, "unknown", "none", "mp4", "gif"),
		Width:  boundedInt(value.Recording.Width, 0, 8192), Height: boundedInt(value.Recording.Height, 0, 8192),
		FPS:            boundedInt(value.Recording.FPS, 0, 240),
		Quality:        safeChoice(value.Recording.Quality, "unknown", "standard", "high"),
		DurationMillis: boundedInt64(value.Recording.DurationMillis, 0, 10*365*24*60*60*1000),
		Result:         safeCode(value.Recording.Result),
		FFmpeg: ffmpegV1{
			Available:   value.Recording.FFmpeg.Available,
			Origin:      safeChoice(value.Recording.FFmpeg.Origin, "unknown", "bundled", "environment", "path", "unavailable"),
			Version:     safeToken(value.Recording.FFmpeg.Version, 64),
			HasExitCode: value.Recording.FFmpeg.HasExitCode,
			ExitCode:    boundedInt(value.Recording.FFmpeg.ExitCode, -1<<20, 1<<20),
		},
	}
	return currentV1{
		DistributionMode: safeChoice(value.DistributionMode, "unknown", "installed", "portable", "source", "explicit-test"),
		Camera:           camera, View: view, Recording: recording,
	}
}

func safeSample(value Sample) Sample {
	value.SourceFPS = safeRate(value.SourceFPS)
	value.ShownFPS = safeRate(value.ShownFPS)
	value.GridColumns = boundedInt(value.GridColumns, 0, 600)
	value.GridRows = boundedInt(value.GridRows, 0, 200)
	value.Queue.InFlight = boundedInt(value.Queue.InFlight, 0, 4096)
	value.Queue.HighWater = boundedInt(value.Queue.HighWater, 0, 4096)
	value.Queue.Capacity = boundedInt(value.Queue.Capacity, 0, 4096)
	value.Process.Goroutines = boundedInt(value.Process.Goroutines, 0, 1_000_000)
	return value
}

func safeRate(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1000 {
		return 0
	}
	return math.Round(value*100) / 100
}

func safeChoice(value, fallback string, choices ...string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, choice := range choices {
		if value == choice {
			return value
		}
	}
	return fallback
}

func boundedChoice(value int, fallback int, choices ...int) int {
	for _, choice := range choices {
		if value == choice {
			return value
		}
	}
	return fallback
}

func boundedInt(value, minimum, maximum int) int {
	if value < minimum || value > maximum {
		return 0
	}
	return value
}

func boundedInt64(value, minimum, maximum int64) int64 {
	if value < minimum || value > maximum {
		return 0
	}
	return value
}
