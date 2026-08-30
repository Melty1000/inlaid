package supportreport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Collector struct {
	mu    sync.RWMutex
	build buildV1
	host  hostFacts
	clock func() time.Time

	events      [MaxRecentEvents]Event
	eventStart  int
	eventCount  int
	samples     [MaxRecentSamples]Sample
	sampleStart int
	sampleCount int
}

type buildV1 struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
}

type cameraV1 struct {
	Model         string    `json:"model"`
	ModelIncluded bool      `json:"model_included"`
	Backend       string    `json:"backend"`
	Requested     ModeFacts `json:"requested"`
	Selected      ModeFacts `json:"selected"`
	Downsample    int       `json:"downsample"`
	PixelLayout   string    `json:"pixel_layout"`
	ColorRange    string    `json:"color_range"`
	ColorMatrix   string    `json:"color_matrix"`
	Permission    string    `json:"permission"`
	DeviceCount   int       `json:"device_count"`
}

type viewV1 struct {
	GridColumns int    `json:"grid_columns"`
	GridRows    int    `json:"grid_rows"`
	Framing     string `json:"framing"`
	Mirror      bool   `json:"mirror"`
	Detail      string `json:"detail"`
	Look        string `json:"look"`
	LookMix     int    `json:"look_mix"`
	TargetFPS   int    `json:"target_fps"`
}

type ffmpegV1 struct {
	Available   bool   `json:"available"`
	Origin      string `json:"origin"`
	Version     string `json:"version"`
	HasExitCode bool   `json:"has_exit_code"`
	ExitCode    int    `json:"exit_code"`
}

type recordingV1 struct {
	State          string   `json:"state"`
	Format         string   `json:"format"`
	Width          int      `json:"width"`
	Height         int      `json:"height"`
	FPS            int      `json:"fps"`
	Quality        string   `json:"quality"`
	DurationMillis int64    `json:"duration_millis"`
	Result         Code     `json:"result"`
	FFmpeg         ffmpegV1 `json:"ffmpeg"`
}

type currentV1 struct {
	DistributionMode string `json:"distribution_mode"`
	Camera           cameraV1
	View             viewV1
	Recording        recordingV1
}

type eventV1 struct {
	SecondsBeforeReport int64    `json:"seconds_before_report"`
	Area                Area     `json:"area"`
	Code                Code     `json:"code"`
	Severity            Severity `json:"severity"`
	NativeCode          uint64   `json:"native_code"`
	Repeat              uint32   `json:"repeat"`
}

type sampleV1 struct {
	SecondsBeforeReport int64              `json:"seconds_before_report"`
	SourceFPS           float64            `json:"source_fps"`
	ShownFPS            float64            `json:"shown_fps"`
	GridColumns         int                `json:"grid_columns"`
	GridRows            int                `json:"grid_rows"`
	Capture             CaptureHealth      `json:"capture"`
	Presentation        PresentationHealth `json:"presentation"`
	Queue               QueueHealth        `json:"recording_queue"`
	Process             ProcessHealth      `json:"process"`
}

type privacyV1 struct {
	ContainsCameraMedia bool `json:"contains_camera_media"`
	ContainsPaths       bool `json:"contains_paths"`
	ContainsDeviceIDs   bool `json:"contains_device_ids"`
	ContainsEnvironment bool `json:"contains_environment_dump"`
	ContainsRawErrors   bool `json:"contains_raw_errors"`
	UploadsData         bool `json:"uploads_data"`
}

type reportV2 struct {
	Schema           string        `json:"schema"`
	CreatedUTC       string        `json:"created_utc"`
	App              buildV1       `json:"app"`
	Platform         platformFacts `json:"platform"`
	Launch           launchFacts   `json:"launch"`
	DistributionMode string        `json:"distribution_mode"`
	Camera           cameraV1      `json:"camera"`
	View             viewV1        `json:"view"`
	Recording        recordingV1   `json:"last_recording"`
	RecentEvents     []eventV1     `json:"recent_events"`
	RecentSamples    []sampleV1    `json:"recent_samples"`
	Privacy          privacyV1     `json:"privacy"`
}

func New(build BuildFacts) *Collector {
	return newCollector(build, collectHostFacts(), time.Now)
}

func newCollector(build BuildFacts, host hostFacts, clock func() time.Time) *Collector {
	if clock == nil {
		clock = time.Now
	}
	version := safeToken(build.Version, 64)
	if version == "" {
		version = "unknown"
	}
	return &Collector{
		build: buildV1{Version: version, Revision: safeRevision(build.Revision), Modified: build.Modified},
		host:  sanitizeHost(host), clock: clock,
	}
}

func (c *Collector) Record(event Event) {
	if c == nil {
		return
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = c.clock()
	}
	event.OccurredAt = event.OccurredAt.UTC()
	event.Area = safeArea(event.Area)
	event.Code = safeCode(event.Code)
	event.Severity = safeSeverity(event.Severity)
	if event.Repeat == 0 {
		event.Repeat = 1
	}

	c.mu.Lock()
	index := (c.eventStart + c.eventCount) % MaxRecentEvents
	if c.eventCount == MaxRecentEvents {
		index = c.eventStart
		c.eventStart = (c.eventStart + 1) % MaxRecentEvents
	} else {
		c.eventCount++
	}
	c.events[index] = event
	c.mu.Unlock()
}

func (c *Collector) Observe(sample Sample) {
	if c == nil {
		return
	}
	if sample.ObservedAt.IsZero() {
		sample.ObservedAt = c.clock()
	}
	sample.ObservedAt = sample.ObservedAt.UTC()
	sample = safeSample(sample)

	c.mu.Lock()
	index := (c.sampleStart + c.sampleCount) % MaxRecentSamples
	if c.sampleCount == MaxRecentSamples {
		index = c.sampleStart
		c.sampleStart = (c.sampleStart + 1) % MaxRecentSamples
	} else {
		c.sampleCount++
	}
	c.samples[index] = sample
	c.mu.Unlock()
}

func (c *Collector) Prepare(current Current, include Include) (Prepared, Review, error) {
	if c == nil {
		return Prepared{}, Review{}, errors.New("support report collector is nil")
	}
	now := c.clock().UTC().Truncate(time.Second)

	c.mu.RLock()
	build, host := c.build, c.host
	events := make([]Event, c.eventCount)
	for index := range events {
		events[index] = c.events[(c.eventStart+index)%MaxRecentEvents]
	}
	samples := make([]Sample, c.sampleCount)
	for index := range samples {
		samples[index] = c.samples[(c.sampleStart+index)%MaxRecentSamples]
	}
	c.mu.RUnlock()

	safe := safeCurrent(current, include)
	report := reportV2{
		Schema: SchemaV2, CreatedUTC: now.Format(time.RFC3339), App: build,
		Platform: host.Platform, Launch: host.Launch, DistributionMode: safe.DistributionMode,
		Camera: safe.Camera, View: safe.View, Recording: safe.Recording,
		RecentEvents: make([]eventV1, 0, len(events)), RecentSamples: make([]sampleV1, 0, len(samples)),
		Privacy: privacyV1{},
	}
	for _, event := range events {
		report.RecentEvents = append(report.RecentEvents, eventV1{
			SecondsBeforeReport: relativeSeconds(now, event.OccurredAt),
			Area:                event.Area, Code: event.Code, Severity: event.Severity,
			NativeCode: event.NativeCode, Repeat: event.Repeat,
		})
	}
	for _, sample := range samples {
		report.RecentSamples = append(report.RecentSamples, sampleV1{
			SecondsBeforeReport: relativeSeconds(now, sample.ObservedAt),
			SourceFPS:           sample.SourceFPS, ShownFPS: sample.ShownFPS,
			GridColumns: sample.GridColumns, GridRows: sample.GridRows,
			Capture: sample.Capture, Presentation: sample.Presentation,
			Queue: sample.Queue, Process: sample.Process,
		})
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return Prepared{}, Review{}, fmt.Errorf("encode support report: %w", err)
	}
	data = append(data, '\n')
	if len(data) > MaxReportBytes {
		return Prepared{}, Review{}, fmt.Errorf("support report exceeds %d KiB", MaxReportBytes>>10)
	}
	if err := validateFieldPolicy(data); err != nil {
		return Prepared{}, Review{}, err
	}
	digest := sha256.Sum256(data)
	digestText := hex.EncodeToString(digest[:])
	prepared := Prepared{data: append([]byte(nil), data...), createdAt: now, digest: digest}
	review := Review{
		Schema: SchemaV2, Bytes: len(data), SHA256: digestText,
		CameraModelIncluded: safe.Camera.ModelIncluded,
		Includes:            []string{"app build", "operating system", "terminal and shell hints", "camera mode", "view settings", "performance counters", "recent typed events"},
		Excludes:            []string{"camera media", "filesystem paths", "camera IDs and serials", "environment dump", "raw errors", "uploads"},
	}
	return prepared, review, nil
}

func relativeSeconds(now, observed time.Time) int64 {
	if observed.IsZero() || observed.After(now) {
		return 0
	}
	seconds := int64(now.Sub(observed) / time.Second)
	return boundedInt64(seconds, 0, 30*24*60*60)
}

func sanitizeHost(host hostFacts) hostFacts {
	host.Platform.OS = safeChoice(host.Platform.OS, "unknown", "windows", "linux", "darwin")
	host.Platform.Architecture = safeToken(host.Platform.Architecture, 32)
	host.Platform.Version = safeToken(host.Platform.Version, 32)
	host.Platform.Distribution = safeToken(host.Platform.Distribution, 32)
	host.Platform.Kernel = safeToken(host.Platform.Kernel, 64)
	host.Platform.GoVersion = safeToken(host.Platform.GoVersion, 32)
	host.Platform.LogicalCPUs = boundedInt(host.Platform.LogicalCPUs, 1, 4096)
	host.Launch.Terminal = safeChoice(host.Launch.Terminal, "unknown", "unknown", "windows-terminal", "wezterm", "kitty", "alacritty", "vscode", "apple-terminal", "iterm2", "hyper", "rio")
	host.Launch.TerminalVersion = safeToken(host.Launch.TerminalVersion, 32)
	host.Launch.ShellHint = safeChoice(host.Launch.ShellHint, "unknown", "unknown", "sh", "bash", "zsh", "fish", "pwsh", "powershell", "cmd", "nu", "xonsh", "tcsh")
	return host
}

func validateFieldPolicy(data []byte) error {
	for _, forbidden := range []string{
		`"device_id"`, `"serial"`, `"path"`, `"username"`, `"hostname"`,
		`"environment"`, `"command"`, `"frame"`, `"pixels"`, `"ansi"`,
		`"token"`, `"secret"`, `"url"`,
	} {
		if bytes.Contains(bytes.ToLower(data), []byte(forbidden)) {
			return fmt.Errorf("support report rejected forbidden field %s", forbidden)
		}
	}
	for _, forbidden := range []string{`:\\`, `:\/`, `\\\\`, `/home/`, `/users/`, `/dev/`, `file://`, `http://`, `https://`} {
		if bytes.Contains(bytes.ToLower(data), []byte(forbidden)) {
			return errors.New("support report rejected private path or URL data")
		}
	}
	return nil
}

func safeRevision(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 7 || len(value) > 64 {
		return ""
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return ""
		}
	}
	return value
}
