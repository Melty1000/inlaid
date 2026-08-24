package taperecovery

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestClaimContextCancellationLeavesTapeUntouched(t *testing.T) {
	directory := t.TempDir()
	path := createClosedTape(t, directory, true, validConfig, 2)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	engine := mustEngine(t, directory, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.ClaimContext(ctx, Candidate{Path: path, Kind: Published})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ClaimContext error = %v, want context cancellation", err)
	}
	assertFileBytes(t, path, before)
}

func TestPublishedClaimIsExclusiveAcrossEnginesAndProcesses(t *testing.T) {
	if !supportsCooperativeClaims() {
		t.Skip("cross-process recovery claims are not implemented on this OS")
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

func TestClaimedTapeDetectsPathReplacement(t *testing.T) {
	directory := t.TempDir()
	path := createClosedTape(t, directory, true, validConfig, 1)
	engine := mustEngine(t, directory, Options{})
	claimed, err := engine.Claim(Candidate{Path: path, Kind: Published})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := claimed.Release(); err != nil {
			t.Errorf("release claim: %v", err)
		}
	})
	if err = claimed.VerifyIdentity(); err != nil {
		t.Fatalf("fresh claim identity: %v", err)
	}

	replacementPath := filepath.Join(directory, "replacement.celltape")
	replacement := []byte("different file object")
	if err = os.WriteFile(replacementPath, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	claimedAtReplacement := claimed
	claimedAtReplacement.Path = replacementPath
	if err = claimedAtReplacement.VerifyIdentity(); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("VerifyIdentity(replaced path) error = %v, want ErrIdentityChanged", err)
	}
	assertFileBytes(t, replacementPath, replacement)

	if runtime.GOOS == "windows" {
		if replaceErr := os.Rename(replacementPath, path); replaceErr == nil {
			t.Fatal("claimed path was replaced despite delete-sharing pin")
		}
		assertFileBytes(t, replacementPath, replacement)
		if renameErr := os.Rename(path, path+".displaced"); renameErr == nil {
			t.Fatal("claimed path was renamed despite delete-sharing pin")
		}
		if removeErr := os.Remove(path); removeErr == nil {
			t.Fatal("claimed path was removed despite delete-sharing pin")
		}
		if writer, writeErr := os.OpenFile(path, os.O_WRONLY, 0); writeErr == nil {
			_ = writer.Close()
			t.Fatal("claimed tape accepted a second write handle")
		}
		if truncateErr := os.Truncate(path, celltape.FileHeaderBytes); truncateErr == nil {
			t.Fatal("claimed tape was truncated through its pathname")
		}
	} else if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		displaced := path + ".displaced"
		if err = os.Rename(path, displaced); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(replacementPath, path); err != nil {
			t.Fatal(err)
		}
		if replay, openErr := claimed.OpenReplayContext(context.Background(), celltape.OpenOptions{}); !errors.Is(openErr, ErrIdentityChanged) {
			if replay != nil {
				_ = replay.Close()
			}
			t.Fatalf("OpenReplayContext(replaced path) error = %v, want ErrIdentityChanged", openErr)
		}
		if err = claimed.Retire(); !errors.Is(err, ErrIdentityChanged) {
			t.Fatalf("Retire(replaced path) error = %v, want ErrIdentityChanged", err)
		}
		assertFileBytes(t, path, replacement)
		claimed.Path = displaced
		if err = claimed.Retire(); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(displaced); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Retire left claimed object at displaced path: %v", err)
		}
		assertFileBytes(t, path, replacement)
		return
	}

	if err = claimed.Retire(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Retire left claimed path: %v", err)
	}
	assertFileBytes(t, replacementPath, replacement)
	if err = claimed.VerifyIdentity(); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("VerifyIdentity(after Retire) error = %v, want ErrIdentityChanged", err)
	}
}

func TestUnixClaimValidationUsesTheHeldFile(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix advisory-lock identity test")
	}
	directory := t.TempDir()
	originalConfig := []byte(`{"version":1,"identity":"original"}`)
	replacementConfig := []byte(`{"version":1,"identity":"replacement"}`)
	original := createClosedTape(t, directory, true, originalConfig, 1)
	replacement := createClosedTape(t, directory, true, replacementConfig, 1)
	engine := mustEngine(t, directory, Options{})
	lease, err := engine.acquireLease(original)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.release() })

	displaced := original + ".displaced"
	if err = os.Rename(original, displaced); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, original); err != nil {
		t.Fatal(err)
	}
	if _, _, err = engine.loadClaimContext(context.Background(), lease, original, Published, true); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("loadClaimContext(replaced path) error = %v, want ErrIdentityChanged", err)
	}

	tape, _, err := engine.loadClaimContext(context.Background(), lease, displaced, Published, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tape.Config.Raw, originalConfig) {
		t.Fatalf("claimed config = %s, want exact held object %s", tape.Config.Raw, originalConfig)
	}
}

func TestPublishReservedPinsExactObjectAcrossProcesses(t *testing.T) {
	directory := t.TempDir()
	recorder, staging, _ := createOpenTape(t, directory, validConfig, 2)
	published := filepath.Join(directory, "finished.celltape")
	engine := mustEngine(t, directory, Options{})
	reservation, err := engine.Reserve(staging)
	if err != nil {
		t.Fatal(err)
	}
	if err = recorder.Close(); err != nil {
		_ = reservation.Release()
		t.Fatal(err)
	}
	claim, err := engine.PublishReserved(&reservation, published)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.Release() })
	if claim.Path != published {
		t.Fatalf("PublishReserved path = %s, want %s", claim.Path, published)
	}
	if err = claim.VerifyIdentity(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published staging path survived: %v", err)
	}
	if supportsCooperativeClaims() {
		runClaimHelper(t, directory, published, "busy")
	}
	replay, err := claim.OpenReplayContext(context.Background(), celltape.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = replay.Close(); err != nil {
		t.Fatal(err)
	}
	if supportsCooperativeClaims() {
		runClaimHelper(t, directory, published, "busy")
	}
	if runtime.GOOS == "windows" {
		if renameErr := os.Rename(published, published+".moved"); renameErr == nil {
			t.Fatal("published claim allowed pathname replacement")
		}
		if writer, writeErr := os.OpenFile(published, os.O_WRONLY, 0); writeErr == nil {
			_ = writer.Close()
			t.Fatal("published claim allowed a second writer")
		}
	}
	if err = claim.Release(); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := engine.Claim(Candidate{Path: published, Kind: Published})
	if err != nil {
		t.Fatalf("claim after PublishReserved release: %v", err)
	}
	if err = reclaimed.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishReservedAtomicallyRefusesDestinationRace(t *testing.T) {
	directory := t.TempDir()
	firstRecorder, first, _ := createOpenTape(t, directory, validConfig, 1)
	secondRecorder, second, _ := createOpenTape(t, directory, validConfig, 1)
	published := filepath.Join(directory, "winner.celltape")
	engine := mustEngine(t, directory, Options{})
	firstReservation, err := engine.Reserve(first)
	if err != nil {
		t.Fatal(err)
	}
	secondReservation, err := engine.Reserve(second)
	if err != nil {
		_ = firstReservation.Release()
		t.Fatal(err)
	}
	if err = errors.Join(firstRecorder.Close(), secondRecorder.Close()); err != nil {
		_ = firstReservation.Release()
		_ = secondReservation.Release()
		t.Fatal(err)
	}
	type result struct {
		source string
		claim  Claim
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, item := range []struct {
		source      string
		reservation Reservation
	}{{first, firstReservation}, {second, secondReservation}} {
		go func(item struct {
			source      string
			reservation Reservation
		}) {
			<-start
			claim, publishErr := engine.PublishReserved(&item.reservation, published)
			results <- result{source: item.source, claim: claim, err: publishErr}
		}(item)
	}
	close(start)
	one, two := <-results, <-results
	var winner, loser result
	if one.err == nil && two.err != nil {
		winner, loser = one, two
	} else if two.err == nil && one.err != nil {
		winner, loser = two, one
	} else {
		_ = one.claim.Release()
		_ = two.claim.Release()
		t.Fatalf("concurrent PublishReserved errors = %v, %v; want exactly one success", one.err, two.err)
	}
	t.Cleanup(func() { _ = winner.claim.Release() })
	if err := winner.claim.VerifyIdentity(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(loser.source); err != nil {
		t.Fatalf("losing staging tape was not preserved: %v", err)
	}
	if _, err := os.Stat(winner.source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("winning staging tape survived: %v", err)
	}
	if err := winner.claim.Retire(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishReservedDoesNotAddAContentValidationPass(t *testing.T) {
	directory := t.TempDir()
	staging := filepath.Join(directory, ".unvalidated.celltape.1.celltape.tmp")
	if err := os.WriteFile(staging, bytes.Repeat([]byte{0xa5}, celltape.FileHeaderBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(directory, "unvalidated.celltape")
	engine := mustEngine(t, directory, Options{})
	reservation, err := engine.Reserve(staging)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := engine.PublishReserved(&reservation, published)
	if err != nil {
		t.Fatalf("PublishReserved replayed content: %v", err)
	}
	defer claim.Release()
	if replay, openErr := celltape.Open(claim.Path, celltape.OpenOptions{}); openErr == nil {
		_ = replay.Close()
		t.Fatal("malformed tape unexpectedly passed the exporter's validation seam")
	}
}

func TestPublishReservedDoesNotApplyRecoverySizeLimit(t *testing.T) {
	directory := t.TempDir()
	staging := filepath.Join(directory, ".long-recording.celltape.1.celltape.tmp")
	initial := bytes.Repeat([]byte{0xa5}, celltape.FileHeaderBytes)
	if err := os.WriteFile(staging, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	engine := mustEngine(t, directory, Options{MaxFileBytes: int64(len(initial) + 1)})
	reservation, err := engine.Reserve(staging)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(staging, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		_ = reservation.Release()
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte("continued recording")); err != nil {
		_ = writer.Close()
		_ = reservation.Release()
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		_ = reservation.Release()
		t.Fatal(err)
	}
	published := filepath.Join(directory, "long-recording.celltape")
	claim, err := engine.PublishReserved(&reservation, published)
	if err != nil {
		t.Fatalf("PublishReserved applied the recovery size limit: %v", err)
	}
	defer claim.Release()
	info, err := os.Stat(published)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= engine.options.MaxFileBytes {
		t.Fatalf("published size = %d, want greater than recovery limit %d", info.Size(), engine.options.MaxFileBytes)
	}
}

func TestPublishReservedRefusesTruncatedReservation(t *testing.T) {
	directory := t.TempDir()
	staging := filepath.Join(directory, ".truncated.celltape.1.celltape.tmp")
	if err := os.WriteFile(staging, bytes.Repeat([]byte{0xa5}, celltape.FileHeaderBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := mustEngine(t, directory, Options{})
	reservation, err := engine.Reserve(staging)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(staging, celltape.FileHeaderBytes-1); err != nil {
		_ = reservation.Release()
		t.Fatal(err)
	}
	claim, err := engine.PublishReserved(&reservation, filepath.Join(directory, "truncated.celltape"))
	_ = claim.Release()
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("PublishReserved truncated error = %v, want ErrLimit", err)
	}
	if info, statErr := os.Stat(staging); statErr != nil || info.Size() != celltape.FileHeaderBytes-1 {
		t.Fatalf("truncated staging was not retained: info=%v err=%v", info, statErr)
	}
}

func TestPublishReservedRefusesAReplacementAtTheStagingPath(t *testing.T) {
	directory := t.TempDir()
	recorder, staging, _ := createOpenTape(t, directory, validConfig, 1)
	engine := mustEngine(t, directory, Options{})
	reservation, err := engine.Reserve(staging)
	if err != nil {
		t.Fatal(err)
	}
	if err = recorder.Close(); err != nil {
		_ = reservation.Release()
		t.Fatal(err)
	}
	displaced := filepath.Join(directory, "displaced-original")
	if err = os.Rename(staging, displaced); err != nil {
		_ = reservation.Release()
		t.Fatal(err)
	}
	replacement := bytes.Repeat([]byte{0xa7}, celltape.FileHeaderBytes)
	if err = os.WriteFile(staging, replacement, 0o600); err != nil {
		_ = reservation.Release()
		t.Fatal(err)
	}
	published := filepath.Join(directory, "must-not-publish.celltape")
	claim, err := engine.PublishReserved(&reservation, published)
	if !errors.Is(err, ErrIdentityChanged) {
		_ = claim.Release()
		t.Fatalf("PublishReserved replacement error = %v, want ErrIdentityChanged", err)
	}
	assertFileBytes(t, staging, replacement)
	if info, statErr := os.Stat(displaced); statErr != nil || info.Size() <= celltape.FileHeaderBytes {
		t.Fatalf("reserved original was not retained: info=%v err=%v", info, statErr)
	}
	if _, statErr := os.Stat(published); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("replacement was published: %v", statErr)
	}
}

func TestNormalStopKeepsStagingReservedAcrossProcesses(t *testing.T) {
	if !supportsCooperativeClaims() {
		t.Skip("cross-process recovery claims are not implemented on this OS")
	}
	directory := t.TempDir()
	recorder, staging, final := createOpenTape(t, directory, validConfig, 1)
	engine := mustEngine(t, directory, Options{})
	reservation, err := engine.Reserve(staging)
	if err != nil {
		_ = recorder.Close()
		t.Fatal(err)
	}
	helper := startInteractiveClaimHelper(t, directory, staging, final)
	helper.expect(t, "READY")
	helper.request(t, "CLAIM_STAGING", "BUSY_STAGING")

	if err = recorder.Close(); err != nil {
		_ = reservation.Release()
		t.Fatal(err)
	}
	// With the writer closed, the reservation is now the only owner. Recovery
	// must still lose; this is the former close-to-claim race window.
	helper.request(t, "CLAIM_STAGING", "BUSY_STAGING")

	claim, err := engine.PublishReserved(&reservation, final)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.Release() })
	helper.request(t, "CLAIM_STAGING", "MISSING_STAGING")
	helper.request(t, "CLAIM_PUBLISHED", "BUSY_PUBLISHED")
	if err = claim.VerifyIdentity(); err != nil {
		t.Fatal(err)
	}
	if err = claim.Retire(); err != nil {
		t.Fatal(err)
	}
	helper.request(t, "RELEASE", "RELEASED")
	helper.close(t)
}

func TestPublishedClaimSubprocessHelper(t *testing.T) {
	mode := os.Getenv("INLAID_CLAIM_HELPER_MODE")
	if mode == "" {
		return
	}
	directory := os.Getenv("INLAID_CLAIM_HELPER_DIRECTORY")
	path := os.Getenv("INLAID_CLAIM_HELPER_PATH")
	if mode == "interactive" {
		runInteractiveClaimHelper(t, directory, path, os.Getenv("INLAID_CLAIM_HELPER_PUBLISHED"))
		return
	}
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

func runInteractiveClaimHelper(t *testing.T, directory, staging, published string) {
	engine := mustEngine(t, directory, Options{})
	input := bufio.NewScanner(os.Stdin)
	output := bufio.NewWriter(os.Stdout)
	reply := func(message string) {
		if _, err := fmt.Fprintln(output, message); err != nil {
			t.Fatal(err)
		}
		if err := output.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	var held Tape
	heldClaim := false
	defer func() {
		if heldClaim {
			_ = held.Release()
		}
	}()
	reply("READY")
	for input.Scan() {
		command := strings.TrimSpace(input.Text())
		switch command {
		case "CLAIM_STAGING", "CLAIM_PUBLISHED":
			if heldClaim {
				reply("WON_" + strings.TrimPrefix(command, "CLAIM_"))
				continue
			}
			path, kind, label := staging, Staging, "STAGING"
			if command == "CLAIM_PUBLISHED" {
				path, kind, label = published, Published, "PUBLISHED"
			}
			tape, err := engine.Claim(Candidate{Path: path, Kind: kind})
			switch {
			case err == nil:
				held, heldClaim = tape, true
				reply("WON_" + label)
			case errors.Is(err, ErrBusy):
				reply("BUSY_" + label)
			case errors.Is(err, os.ErrNotExist):
				reply("MISSING_" + label)
			default:
				reply("ERROR_" + label + ": " + err.Error())
			}
		case "RELEASE":
			if heldClaim {
				if err := held.Release(); err != nil {
					reply("ERROR_RELEASE: " + err.Error())
					continue
				}
				heldClaim = false
			}
			reply("RELEASED")
		default:
			reply("ERROR_COMMAND: " + command)
		}
	}
	if err := input.Err(); err != nil {
		t.Fatal(err)
	}
}

type interactiveClaimHelper struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  *bufio.Scanner
	stderr  bytes.Buffer
	closed  bool
}

func startInteractiveClaimHelper(t *testing.T, directory, staging, published string) *interactiveClaimHelper {
	t.Helper()
	helper := &interactiveClaimHelper{}
	helper.command = exec.Command(os.Args[0], "-test.run=^TestPublishedClaimSubprocessHelper$")
	helper.command.Env = append(os.Environ(),
		"INLAID_CLAIM_HELPER_MODE=interactive",
		"INLAID_CLAIM_HELPER_DIRECTORY="+directory,
		"INLAID_CLAIM_HELPER_PATH="+staging,
		"INLAID_CLAIM_HELPER_PUBLISHED="+published,
	)
	input, err := helper.command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := helper.command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	helper.input = input
	helper.output = bufio.NewScanner(output)
	helper.command.Stderr = &helper.stderr
	if err = helper.command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { helper.stop() })
	return helper
}

func (h *interactiveClaimHelper) request(t *testing.T, command, want string) {
	t.Helper()
	if _, err := fmt.Fprintln(h.input, command); err != nil {
		t.Fatalf("write claim helper command: %v", err)
	}
	h.expect(t, want)
}

func (h *interactiveClaimHelper) expect(t *testing.T, want string) {
	t.Helper()
	result := make(chan string, 1)
	go func() {
		if h.output.Scan() {
			result <- h.output.Text()
			return
		}
		result <- ""
	}()
	select {
	case got := <-result:
		if got != want {
			t.Fatalf("claim helper response = %q, want %q (stderr: %s)", got, want, h.stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("claim helper timed out waiting for %q (stderr: %s)", want, h.stderr.String())
	}
}

func (h *interactiveClaimHelper) close(t *testing.T) {
	t.Helper()
	if h.closed {
		return
	}
	h.closed = true
	if err := h.input.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("claim helper exit: %v\n%s", err, h.stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = h.command.Process.Kill()
		t.Fatalf("claim helper did not exit\n%s", h.stderr.String())
	}
}

func (h *interactiveClaimHelper) stop() {
	if h == nil || h.closed {
		return
	}
	h.closed = true
	_ = h.input.Close()
	_ = h.command.Process.Kill()
	_ = h.command.Wait()
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
	if !supportsCooperativeClaims() {
		t.Skip("staging recovery claims are not implemented on this OS")
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
		t.Skip("a raw CellTape writer does not take Inlaid's cooperative Unix reservation")
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
	if !supportsCooperativeClaims() {
		t.Skip("staging recovery claims are not implemented on this OS")
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

func supportsCooperativeClaims() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "linux" || runtime.GOOS == "darwin"
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
	recorder, path, final := createOpenTape(t, directory, config, frames)
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if publish {
		if err := celltape.Publish(path, final); err != nil {
			t.Fatal(err)
		}
		path = final
	}
	return path
}

func createOpenTape(t *testing.T, directory string, config []byte, frames int) (*celltape.Recorder, string, string) {
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
	return recorder, recorder.StagingPath(), final
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
