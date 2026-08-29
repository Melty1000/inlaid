package supportreport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrepareDeterministicSchemaV2(t *testing.T) {
	now := time.Date(2026, time.August, 24, 15, 30, 0, 0, time.UTC)
	collector := fixtureCollector(now)
	collector.Record(Event{
		OccurredAt: now.Add(-2 * time.Second), Area: AreaCamera, Code: CodeFramesDropped,
		Severity: SeverityWarning, NativeCode: 23, Repeat: 2,
	})
	collector.Observe(Sample{
		ObservedAt: now.Add(-time.Second), SourceFPS: 29.973, ShownFPS: 29.825,
		GridColumns: 177, GridRows: 50,
		Capture:      CaptureHealth{Packets: 31, Decoded: 30, DroppedPackets: 1},
		Presentation: PresentationHealth{Accepted: 29, Dropped: 1},
		Queue:        QueueHealth{Active: true, InFlight: 1, HighWater: 2, Capacity: 120},
		Process:      ProcessHealth{HeapBytes: 8 << 20, ResidentBytes: 40 << 20, Goroutines: 18, GCCycles: 3},
	})
	current := fixtureCurrent("C922 Pro Stream Webcam")

	first, review, err := collector.Prepare(current, Include{CameraModel: true})
	if err != nil {
		t.Fatal(err)
	}
	second, secondReview, err := collector.Prepare(current, Include{CameraModel: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Content(), second.Content()) || review.Schema != secondReview.Schema ||
		review.Bytes != secondReview.Bytes || review.SHA256 != secondReview.SHA256 ||
		review.CameraModelIncluded != secondReview.CameraModelIncluded {
		t.Fatal("identical prepared snapshots were not deterministic")
	}
	wantDigest := "bbaf762c8610930be8c6d21d76f24605d6f020f8a308bb31b9375b6b0eb6aa4c"
	if review.SHA256 != wantDigest {
		t.Fatalf("schema v2 golden digest = %s, want %s", review.SHA256, wantDigest)
	}
	digest := sha256.Sum256(first.Content())
	if review.SHA256 != hex.EncodeToString(digest[:]) || review.Bytes != len(first.Content()) {
		t.Fatal("review does not describe the exact prepared bytes")
	}

	var report reportV2
	if err := json.Unmarshal(first.Content(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != SchemaV2 || report.CreatedUTC != "2026-08-24T15:30:00Z" || report.App.Version != "v0.2.0-beta.1" {
		t.Fatalf("report identity = %+v", report)
	}
	if report.Platform.OS != "windows" || report.Launch.Terminal != "windows-terminal" || report.Camera.Model != "C922 Pro Stream Webcam" {
		t.Fatalf("safe facts were not preserved: %+v %+v %+v", report.Platform, report.Launch, report.Camera)
	}
	if strings.Contains(string(first.Content()), `"launch_route"`) || strings.Contains(string(first.Content()), `"launcher"`) {
		t.Fatal("support report retained superseded launch-route reporting")
	}
	if len(report.RecentEvents) != 1 || report.RecentEvents[0].SecondsBeforeReport != 2 || len(report.RecentSamples) != 1 {
		t.Fatalf("bounded history shape = %+v %+v", report.RecentEvents, report.RecentSamples)
	}
	if report.RecentSamples[0].SourceFPS != 29.97 || report.RecentSamples[0].ShownFPS != 29.83 {
		t.Fatalf("rates were not normalized: %+v", report.RecentSamples[0])
	}
}

func TestPrepareRejectsToxicStringsAndUnknownFields(t *testing.T) {
	now := time.Date(2026, time.August, 24, 16, 0, 0, 0, time.UTC)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "EXAMPLE_PRIVATE_VALUE")
	t.Setenv("GITHUB_TOKEN", "EXAMPLE_GITHUB_VALUE")
	host := hostFacts{
		Platform: platformFacts{OS: `https://host.invalid`, Architecture: `C:\Users\Alice`, Version: `/home/alice`, GoVersion: "go1.26.0", LogicalCPUs: 8},
		Launch:   launchFacts{Terminal: "evil-terminal", TerminalVersion: "../../secret", ShellHint: `C:\private\shell.exe`, TrueColorHint: true},
	}
	collector := newCollector(BuildFacts{Version: `C:\Users\Alice\private`, Revision: "not-a-revision"}, host, func() time.Time { return now })
	current := fixtureCurrent(`Camera C:\Users\Alice\secret token=EXAMPLE_TOKEN_VALUE`)
	current.Camera.Backend = `https://evil.invalid`
	current.Camera.Requested.Format = `../../private`
	current.View.Look = `my-private-file.cube`
	current.Recording.FFmpeg.Version = `C:\tools\private.exe`
	prepared, review, err := collector.Prepare(current, Include{CameraModel: true})
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(prepared.Content()))
	for _, forbidden := range []string{
		"alice", "example_private_value", "example_github_value", "example_token_value",
		"private.exe", "private-file", "evil.invalid", `c:\\`, "/home/", `"device_id":`, `"serial":`,
	} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Fatalf("support report leaked %q:\n%s", forbidden, prepared.Content())
		}
	}
	if review.CameraModelIncluded {
		t.Fatal("toxic camera model was marked as included")
	}
	var report reportV2
	if err := json.Unmarshal(prepared.Content(), &report); err != nil {
		t.Fatal(err)
	}
	if report.App.Version != "unknown" || report.Platform.OS != "unknown" || report.Camera.Backend != "unknown" || report.Camera.Model != "" {
		t.Fatalf("invalid values did not fail closed: %+v %+v %+v", report.App, report.Platform, report.Camera)
	}
}

func TestLaunchFactsUsePresenceAndRecognizedValuesOnly(t *testing.T) {
	t.Setenv("WT_SESSION", "private-session-guid")
	t.Setenv("WT_PROFILE_ID", "private-profile-guid")
	t.Setenv("TERM_PROGRAM_VERSION", "1.24.1911.0")
	shell := "/private/PowerShell/pwsh"
	if runtime.GOOS == "windows" {
		shell = `C:\Program Files\PowerShell\7\pwsh.exe`
	}
	t.Setenv("SHELL", shell)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "private-cloud-secret")
	facts := collectLaunchFacts()
	data, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Terminal != "windows-terminal" || facts.TerminalVersion != "1.24.1911.0" || facts.ShellHint != "pwsh" {
		t.Fatalf("recognized launch facts = %+v", facts)
	}
	for _, forbidden := range []string{"private-session-guid", "private-profile-guid", "private-cloud-secret", "program files"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("launch facts leaked %q: %s", forbidden, data)
		}
	}
}

func TestLaunchFactsRejectCredentialShapedTerminalVersion(t *testing.T) {
	t.Setenv("WT_SESSION", "present")
	t.Setenv("TERM_PROGRAM_VERSION", "CREDENTIALSHAPEDVALUE12345")
	if version := collectLaunchFacts().TerminalVersion; version != "" {
		t.Fatalf("credential-shaped terminal version survived as %q", version)
	}
}

func TestCameraModelRejectsPathShapes(t *testing.T) {
	for _, value := range []string{`C:\Users\Alice\camera`, "/tmp/alice/camera", "/Volumes/Alice/Camera", "../../private"} {
		if got := safeCameraModel(value); got != "" {
			t.Fatalf("safeCameraModel(%q) = %q", value, got)
		}
	}
}

func TestRecentHistoryUsesFixedNewestRings(t *testing.T) {
	now := time.Date(2026, time.August, 24, 17, 0, 0, 0, time.UTC)
	collector := fixtureCollector(now)
	for index := 0; index < MaxRecentEvents+17; index++ {
		collector.Record(Event{OccurredAt: now.Add(-time.Duration(index) * time.Second), Area: AreaCamera, Code: CodeFramesDropped, Severity: SeverityWarning, NativeCode: uint64(index)})
	}
	for index := 0; index < MaxRecentSamples+13; index++ {
		collector.Observe(Sample{ObservedAt: now.Add(-time.Duration(index) * time.Second), Process: ProcessHealth{GCCycles: uint32(index)}})
	}
	prepared, _, err := collector.Prepare(fixtureCurrent("Camera"), Include{})
	if err != nil {
		t.Fatal(err)
	}
	var report reportV2
	if err := json.Unmarshal(prepared.Content(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.RecentEvents) != MaxRecentEvents || len(report.RecentSamples) != MaxRecentSamples {
		t.Fatalf("ring lengths = %d, %d", len(report.RecentEvents), len(report.RecentSamples))
	}
	if report.RecentEvents[0].NativeCode != 17 || report.RecentEvents[len(report.RecentEvents)-1].NativeCode != MaxRecentEvents+16 {
		t.Fatalf("event ring did not retain newest entries: %d..%d", report.RecentEvents[0].NativeCode, report.RecentEvents[len(report.RecentEvents)-1].NativeCode)
	}
	if report.RecentSamples[0].Process.GCCycles != 13 || report.RecentSamples[len(report.RecentSamples)-1].Process.GCCycles != MaxRecentSamples+12 {
		t.Fatal("sample ring did not retain newest entries")
	}
	if len(prepared.Content()) > MaxReportBytes {
		t.Fatalf("bounded rings produced %d bytes", len(prepared.Content()))
	}
}

func TestConcurrentRecordObserveAndPrepare(t *testing.T) {
	now := time.Date(2026, time.August, 24, 18, 0, 0, 0, time.UTC)
	collector := fixtureCollector(now)
	var wait sync.WaitGroup
	errorsFound := make(chan error, 32)
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for index := 0; index < 300; index++ {
				collector.Record(Event{Area: AreaPreview, Code: CodePreviewSlow, Severity: SeverityWarning, NativeCode: uint64(worker*1000 + index)})
				collector.Observe(Sample{SourceFPS: 30, ShownFPS: 29, Process: ProcessHealth{Goroutines: 20}})
				if index%40 == 0 {
					prepared, review, err := collector.Prepare(fixtureCurrent("Camera"), Include{CameraModel: true})
					if err != nil {
						errorsFound <- err
						continue
					}
					digest := sha256.Sum256(prepared.Content())
					if review.SHA256 != hex.EncodeToString(digest[:]) {
						errorsFound <- errors.New("prepared digest mismatch")
					}
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func TestSaveIsPrivateAtomicAndDoesNotOverwrite(t *testing.T) {
	now := time.Date(2026, time.August, 24, 19, 0, 0, 0, time.UTC)
	collector := fixtureCollector(now)
	prepared, review, err := collector.Prepare(fixtureCurrent("Camera"), Include{CameraModel: true})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	saved, err := collector.Save(root, prepared)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(saved.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, prepared.Content()) || saved.Bytes != review.Bytes || saved.SHA256 != review.SHA256 {
		t.Fatal("saved report differs from reviewed bytes")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(saved.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("report permissions = %o", info.Mode().Perm())
		}
	}
	if _, err := collector.Save(root, prepared); !errors.Is(err, ErrReportExists) {
		t.Fatalf("second save error = %v", err)
	}
	assertNoPartials(t, root)
}

func TestSaveCleansPartialAfterWriteFailure(t *testing.T) {
	now := time.Date(2026, time.August, 24, 20, 0, 0, 0, time.UTC)
	collector := fixtureCollector(now)
	prepared, _, err := collector.Prepare(fixtureCurrent("Camera"), Include{})
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("injected write failure")
	root := t.TempDir()
	_, err = savePrepared(root, prepared, func(file *os.File, data []byte) error {
		_, _ = file.Write(data[:min(len(data), 17)])
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("save error = %v", err)
	}
	assertNoPartials(t, root)
	matches, err := filepath.Glob(filepath.Join(root, reportDirectory, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("failed write published %v", matches)
	}
}

func fixtureCollector(now time.Time) *Collector {
	host := hostFacts{
		Platform: platformFacts{OS: "windows", Architecture: "amd64", Version: "10.0.26100", GoVersion: "go1.26.0", LogicalCPUs: 16},
		Launch:   launchFacts{Terminal: "windows-terminal", ShellHint: "unknown", TrueColorHint: true},
	}
	return newCollector(BuildFacts{Version: "v0.2.0-beta.1", Revision: "abcdef012345", Modified: true}, host, func() time.Time { return now })
}

func fixtureCurrent(model string) Current {
	return Current{
		Camera: CameraFacts{
			Model: model, Backend: "media-foundation", DeviceCount: 1, Permission: "granted",
			Requested:  ModeFacts{Width: 1920, Height: 1080, FPSNumerator: 30, FPSDenominator: 1, Format: "MJPG"},
			Selected:   ModeFacts{Width: 1920, Height: 1080, FPSNumerator: 30000, FPSDenominator: 1001, Format: "MJPG"},
			Downsample: 4, PixelLayout: "planar-ycbcr", ColorRange: "full", ColorMatrix: "bt601",
		},
		View: ViewFacts{GridColumns: 177, GridRows: 50, Framing: "fill", Mirror: true, Detail: "balanced", Look: "none", LookMix: 100, TargetFPS: 30},
		Recording: RecordingFacts{
			State: "idle", Format: "mp4", Width: 1920, Height: 1080, FPS: 30, Quality: "high",
			DurationMillis: 5000, Result: CodeCompleted,
			FFmpeg: FFmpegFacts{Available: true, Origin: "bundled", Version: "7.1.1"},
		},
	}
}

func assertNoPartials(t *testing.T, root string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, reportDirectory, "*.partial"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("partial files remain: %v", matches)
	}
}

func TestPreparedContentIsACopy(t *testing.T) {
	now := time.Date(2026, time.August, 24, 21, 0, 0, 0, time.UTC)
	collector := fixtureCollector(now)
	prepared, _, err := collector.Prepare(fixtureCurrent("Camera"), Include{})
	if err != nil {
		t.Fatal(err)
	}
	copyOne := prepared.Content()
	copyOne[0] = 'X'
	if bytes.Equal(copyOne, prepared.Content()) {
		t.Fatal("caller modified prepared report bytes")
	}
}

func ExampleCollector() {
	collector := New(BuildFacts{Version: "v0.2.0-beta.1"})
	collector.Record(Event{Area: AreaCamera, Code: CodeStreamStalled, Severity: SeverityError})
	prepared, review, err := collector.Prepare(Current{}, Include{})
	fmt.Println(err == nil, len(prepared.Content()) == review.Bytes)
	// Output: true true
}
