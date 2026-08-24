//go:build linux || darwin

package taperecovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixRetireNeverUnlinksATombstoneReplacement(t *testing.T) {
	directory := t.TempDir()
	path := createClosedTape(t, directory, true, validConfig, 1)
	engine := mustEngine(t, directory, Options{})
	claimed, err := engine.Claim(Candidate{Path: path, Kind: Published})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claimed.Release() })

	replacementPath := filepath.Join(directory, "retirement-replacement.celltape")
	replacement := []byte("replacement must survive")
	if err = os.WriteFile(replacementPath, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	displaced := path + ".displaced"
	err = claimed.claim.retirePathBeforeUnlink(path, func(tombstone string) error {
		if renameErr := os.Rename(tombstone, displaced); renameErr != nil {
			return renameErr
		}
		return os.Rename(replacementPath, tombstone)
	})
	if !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("Retire(tombstone replacement) error = %v, want ErrIdentityChanged", err)
	}
	assertFileBytes(t, path, replacement)
	if _, err = os.Stat(displaced); err != nil {
		t.Fatalf("claimed object was lost after tombstone replacement: %v", err)
	}

	claimed.Path = displaced
	if err = claimed.Retire(); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, path, replacement)
}

func TestUnixScanRetiresCrashLeftRetirementTombstone(t *testing.T) {
	directory := t.TempDir()
	path := createClosedTape(t, directory, true, validConfig, 1)
	engine := mustEngine(t, directory, Options{})
	claimed, err := engine.Claim(Candidate{Path: path, Kind: Published})
	if err != nil {
		t.Fatal(err)
	}
	tombstone, err := privateRetirementPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = claimed.claim.renamePath(path, tombstone); err != nil {
		t.Fatal(err)
	}
	if err = claimed.Release(); err != nil {
		t.Fatal(err)
	}

	candidates, err := engine.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v, want no duplicate recovery candidate", candidates)
	}
	if _, err = os.Lstat(tombstone); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retirement tombstone remains after reconciliation: %v", err)
	}
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired tape was republished: %v", err)
	}
}

func TestUnixScanPreservesBusyRetirementTombstone(t *testing.T) {
	directory := t.TempDir()
	path := createClosedTape(t, directory, true, validConfig, 1)
	engine := mustEngine(t, directory, Options{})
	tombstone, err := privateRetirementPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, tombstone); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireClaim(tombstone)
	if err != nil {
		t.Fatal(err)
	}

	candidates, err := engine.Scan()
	if err != nil {
		_ = lease.release()
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		_ = lease.release()
		t.Fatalf("candidates = %+v, want busy tombstone hidden", candidates)
	}
	if _, err = os.Lstat(tombstone); err != nil {
		_ = lease.release()
		t.Fatalf("busy retirement tombstone was not preserved: %v", err)
	}
	if err = lease.release(); err != nil {
		t.Fatal(err)
	}

	candidates, err = engine.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates after retry = %+v, want no duplicate recovery candidate", candidates)
	}
	if _, err = os.Lstat(tombstone); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retirement tombstone remains after retry: %v", err)
	}
}
