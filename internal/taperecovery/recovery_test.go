package taperecovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/Melty1000/inlaid/internal/celltape"
)

var validConfig = []byte(`{"version":1,"view":{"framing":"whole"},"output":{"format":"mp4"}}`)

var testTapeSequence atomic.Uint64

func TestMissingRecoveryDirectoryIsAnEmptySet(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "not-created")
	engine := mustEngine(t, directory, Options{})
	candidates, err := engine.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("Scan(missing) = %+v, want empty", candidates)
	}
	if _, err = os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery engine created its missing directory: %v", err)
	}
}

func TestPublishedTapeScanClaimAndLoad(t *testing.T) {
	directory := t.TempDir()
	path := createClosedTape(t, directory, true, validConfig, 2)
	if err := os.WriteFile(filepath.Join(directory, "unrelated.txt"), []byte("ignore"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := mustEngine(t, directory, Options{})
	candidates, err := engine.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Kind != Published || candidates[0].Path != path {
		t.Fatalf("Scan() = %+v, want one published tape %s", candidates, path)
	}

	claimed, err := engine.Claim(candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := claimed.Release(); err != nil {
			t.Errorf("release claim: %v", err)
		}
	})
	if claimed.Path != path || claimed.RecoveredFrom != "" || claimed.SourceKind != Published ||
		claimed.Records != 2 || claimed.RepairedBytes != 0 || claimed.Columns != 2 || claimed.Rows != 1 ||
		claimed.Config.Version != ConfigVersion || !bytes.Equal(claimed.Config.Raw, validConfig) {
		t.Fatalf("Claim() = %+v", claimed)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("published tape was not retained: %v", err)
	}

	loaded, err := engine.Load(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Path != path || loaded.Records != claimed.Records || !bytes.Equal(loaded.Config.Raw, validConfig) {
		t.Fatalf("Load() = %+v, claimed = %+v", loaded, claimed)
	}
	loaded.Config.Raw[0] = '!'
	if claimed.Config.Raw[0] == '!' {
		t.Fatal("Load config aliases earlier claimed metadata")
	}
}

func TestPublishedClaimIsExclusiveAcrossEnginesAndProcesses(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows kernel claim lease is the product contract")
	}
	directory := t.TempDir()
	path := createClosedTape(t, directory, true, validConfig, 1)
	first := mustEngine(t, directory, Options{})
	second := mustEngine(t, directory, Options{})
	candidate := Candidate{Path: path, Kind: Published}

	claimed, err := first.Claim(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Path != path {
		t.Fatalf("published claim changed logical path: got %s want %s", claimed.Path, path)
	}
	if _, err = second.Claim(candidate); !errors.Is(err, ErrBusy) {
		_ = claimed.Release()
		t.Fatalf("second engine Claim() error = %v, want ErrBusy", err)
	}
	runClaimHelper(t, directory, path, "busy")
	if err = claimed.Release(); err != nil {
		t.Fatalf("release first claim: %v", err)
	}
	if err = claimed.Release(); err != nil {
		t.Fatalf("second release was not idempotent: %v", err)
	}

	// The helper deliberately exits without Release after a successful claim.
	// The kernel must drop ownership at process exit so the unchanged canonical
	// path remains crash-discoverable by another engine.
	runClaimHelper(t, directory, path, "success-and-exit")
	reclaimed, err := second.Claim(candidate)
	if err != nil {
		t.Fatalf("claim after owner process exit: %v", err)
	}
	if reclaimed.Path != path {
		_ = reclaimed.Release()
		t.Fatalf("reclaimed path = %s, want original %s", reclaimed.Path, path)
	}
	if err = reclaimed.Release(); err != nil {
		t.Fatalf("release reclaimed tape: %v", err)
	}
}

func TestPublishedClaimSubprocessHelper(t *testing.T) {
	mode := os.Getenv("INLAID_CLAIM_HELPER_MODE")
	if mode == "" {
		return
	}
	directory := os.Getenv("INLAID_CLAIM_HELPER_DIRECTORY")
	path := os.Getenv("INLAID_CLAIM_HELPER_PATH")
	engine := mustEngine(t, directory, Options{})
	tape, err := engine.Claim(Candidate{Path: path, Kind: Published})
	switch mode {
	case "busy":
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("subprocess Claim() error = %v, want ErrBusy", err)
		}
	case "success-and-exit":
		if err != nil {
			t.Fatalf("subprocess Claim() error = %v", err)
		}
		// Intentionally do not Release: process teardown must release ownership.
		_ = tape
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func runClaimHelper(t *testing.T, directory, path, mode string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestPublishedClaimSubprocessHelper$")
	command.Env = append(os.Environ(),
		"INLAID_CLAIM_HELPER_MODE="+mode,
		"INLAID_CLAIM_HELPER_DIRECTORY="+directory,
		"INLAID_CLAIM_HELPER_PATH="+path,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("claim helper (%s): %v\n%s", mode, err, output)
	}
}

func TestByteTruncatedStagingIsRepairedAndDurablyClaimed(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("mandatory writer-sharing recovery is Windows-owned")
	}
	directory := t.TempDir()
	staging := createClosedTape(t, directory, false, validConfig, 2)
	before, err := os.Stat(staging)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(staging, before.Size()-1); err != nil {
		t.Fatal(err)
	}

	engine := mustEngine(t, directory, Options{})
	candidates, err := engine.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Kind != Staging {
		t.Fatalf("Scan() = %+v, want one staging tape", candidates)
	}
	claimed, err := engine.Claim(candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := claimed.Release(); err != nil {
			t.Errorf("release claim: %v", err)
		}
	})
	if claimed.Path == staging || filepath.Ext(claimed.Path) != ".celltape" || claimed.RecoveredFrom != staging ||
		claimed.SourceKind != Staging || claimed.Records != 1 || claimed.RepairedBytes <= 0 || claimed.Size != claimed.ValidBytes {
		t.Fatalf("recovered metadata = %+v", claimed)
	}
	if _, err = os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging path survived successful atomic claim: %v", err)
	}
	info, err := os.Stat(claimed.Path)
	if err != nil {
		t.Fatalf("claimed path is missing: %v", err)
	}
	if info.Size() != claimed.ValidBytes {
		t.Fatalf("claimed size = %d, valid bytes = %d", info.Size(), claimed.ValidBytes)
	}
	loaded, err := engine.Load(claimed.Path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Records != 1 || loaded.ValidBytes != claimed.ValidBytes {
		t.Fatalf("Load(recovered) = %+v", loaded)
	}
}

func TestLiveOpenStagingIsIgnoredUntilWriterCloses(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("mandatory writer-sharing recovery is Windows-owned")
	}
	directory := t.TempDir()
	final := filepath.Join(directory, "live.celltape")
	recorder, err := celltape.Create(context.Background(), final, celltape.Config{QueueCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	staging := recorder.StagingPath()
	if err = recorder.Submit(testInput(validConfig, 0), 0); err != nil {
		_ = recorder.Close()
		t.Fatal(err)
	}
	engine := mustEngine(t, directory, Options{})
	candidates, err := engine.Scan()
	if err != nil {
		_ = recorder.Close()
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		_ = recorder.Close()
		t.Fatalf("Scan exposed live staging file: %+v", candidates)
	}
	forged := Candidate{Path: staging, Kind: Staging}
	if _, err = engine.Claim(forged); !errors.Is(err, ErrBusy) {
		_ = recorder.Close()
		t.Fatalf("Claim(live) error = %v, want ErrBusy", err)
	}
	if _, err = os.Stat(staging); err != nil {
		_ = recorder.Close()
		t.Fatalf("live staging file changed: %v", err)
	}
	if err = recorder.Close(); err != nil {
		t.Fatal(err)
	}
	candidates, err = engine.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Path != staging || candidates[0].Kind != Staging {
		t.Fatalf("closed staging file was not discoverable: %+v", candidates)
	}
}

func TestMalformedOversizedAndFailedClaimsNeverDeleteFiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("staging claims are conservatively disabled off Windows")
	}
	directory := t.TempDir()
	malformed := filepath.Join(directory, ".bad.celltape.1.celltape.tmp")
	malformedBytes := bytes.Repeat([]byte{0xa5}, celltape.FileHeaderBytes)
	if err := os.WriteFile(malformed, malformedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	engine := mustEngine(t, directory, Options{})
	if _, err := engine.Claim(Candidate{Path: malformed, Kind: Staging}); err == nil {
		t.Fatal("malformed tape claim succeeded")
	}
	assertFileBytes(t, malformed, malformedBytes)

	badConfig := createClosedTape(t, directory, true, []byte(`{"version":2}`), 1)
	badConfigBefore, err := os.ReadFile(badConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Claim(Candidate{Path: badConfig, Kind: Published}); !errors.Is(err, ErrConfig) {
		t.Fatalf("unsupported config error = %v, want ErrConfig", err)
	}
	assertFileBytes(t, badConfig, badConfigBefore)

	largeConfig := createClosedTape(t, directory, true, []byte(`{"version":1,"padding":"abcdefghijklmnopqrstuvwxyz"}`), 1)
	largeBefore, err := os.ReadFile(largeConfig)
	if err != nil {
		t.Fatal(err)
	}
	smallConfigEngine := mustEngine(t, directory, Options{MaxConfigBytes: 16})
	if _, err = smallConfigEngine.Claim(Candidate{Path: largeConfig, Kind: Published}); !errors.Is(err, ErrConfig) {
		t.Fatalf("oversized config error = %v, want ErrConfig", err)
	}
	assertFileBytes(t, largeConfig, largeBefore)

	oversized := filepath.Join(directory, "oversized.celltape")
	oversizedBytes := bytes.Repeat([]byte{0x5a}, 129)
	if err = os.WriteFile(oversized, oversizedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	smallFileEngine := mustEngine(t, directory, Options{MaxFileBytes: 128})
	if _, err = smallFileEngine.Claim(Candidate{Path: oversized, Kind: Published}); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized tape error = %v, want ErrLimit", err)
	}
	assertFileBytes(t, oversized, oversizedBytes)

	// A destination collision fails the final durable rename but retains both
	// the staging tape and the pre-existing published file.
	collisionStaging := createClosedTape(t, directory, false, validConfig, 1)
	destination := engine.recoveredPath(collisionStaging)
	if err = os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Claim(Candidate{Path: collisionStaging, Kind: Staging}); err == nil {
		t.Fatal("claim unexpectedly replaced its destination")
	}
	if _, err = os.Stat(collisionStaging); err != nil {
		t.Fatalf("failed claim removed staging tape: %v", err)
	}
	assertFileBytes(t, destination, []byte("existing"))
}

func TestLoadRejectsTailWithoutModificationAndScanBoundsCount(t *testing.T) {
	directory := t.TempDir()
	path := createClosedTape(t, directory, true, validConfig, 1)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("torn")); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	engine := mustEngine(t, directory, Options{})
	if _, err = engine.Load(path); !errors.Is(err, ErrDamagedTail) {
		t.Fatalf("Load(torn) error = %v, want ErrDamagedTail", err)
	}
	assertFileBytes(t, path, before)

	if err = os.WriteFile(filepath.Join(directory, "one.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "two.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bounded := mustEngine(t, directory, Options{MaxDirectoryEntries: 2, MaxCandidates: 2})
	if _, err = bounded.Scan(); !errors.Is(err, ErrLimit) {
		t.Fatalf("bounded Scan error = %v, want ErrLimit", err)
	}
	assertFileBytes(t, path, before)
}

func createClosedTape(t *testing.T, directory string, publish bool, config []byte, frames int) string {
	t.Helper()
	final := filepath.Join(directory, fmt.Sprintf("%s-%d.celltape", uniqueName(t), testTapeSequence.Add(1)))
	recorder, err := celltape.Create(context.Background(), final, celltape.Config{
		QueueCapacity: 4, KeyframeInterval: 120, Compression: celltape.CompressionNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < frames; index++ {
		if err = recorder.Submit(testInput(config, uint64(index)), uint64(index)); err != nil {
			_ = recorder.Close()
			t.Fatal(err)
		}
	}
	if err = recorder.Close(); err != nil {
		t.Fatal(err)
	}
	path := recorder.StagingPath()
	if publish {
		if err = celltape.Publish(path, final); err != nil {
			t.Fatal(err)
		}
		path = final
	}
	return path
}

func testInput(config []byte, stamp uint64) celltape.Input {
	color := celltape.RGB(0x112233 + stamp)
	return celltape.Input{
		GeometryEpoch: 1,
		ConfigEpoch:   1,
		Columns:       2,
		Rows:          1,
		Config:        append([]byte(nil), config...),
		Cells: []celltape.Cell{
			{Mask: 0, FG: color, BG: color},
			{Mask: 0, FG: color, BG: color},
		},
		SourceNanos: stamp,
	}
}

func uniqueName(t *testing.T) string {
	return stringsForName(t.Name())
}

func stringsForName(value string) string {
	var result []byte
	for i := 0; i < len(value); i++ {
		character := value[i]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			result = append(result, character)
		} else {
			result = append(result, '-')
		}
	}
	return string(result)
}

func mustEngine(t *testing.T, directory string, options Options) *Engine {
	t.Helper()
	engine, err := New(directory, options)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preserved file %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("preserved file %s changed (%d bytes -> %d bytes)", path, len(want), len(got))
	}
}
