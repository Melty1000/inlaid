//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Melty1000/inlaid/internal/pathownership"
	"golang.org/x/sys/windows/registry"
)

type fakeRegistryValueKey struct {
	deleteErr error
	closeErr  error
	deleted   []string
}

type fakeRawRegistryKey struct {
	data      []byte
	valueType uint32
	valueErr  error
	closeErr  error
}

func (key *fakeRawRegistryKey) GetValue(_ string, buffer []byte) (int, uint32, error) {
	if key.valueErr != nil {
		return 0, key.valueType, key.valueErr
	}
	if buffer == nil {
		return len(key.data), key.valueType, nil
	}
	if len(buffer) < len(key.data) {
		return len(key.data), key.valueType, registry.ErrShortBuffer
	}
	copy(buffer, key.data)
	return len(key.data), key.valueType, nil
}

func (key *fakeRawRegistryKey) Close() error { return key.closeErr }

func rawUTF16(units ...uint16) []byte {
	data := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(data[index*2:], unit)
	}
	return data
}

func (key *fakeRegistryValueKey) DeleteValue(name string) error {
	key.deleted = append(key.deleted, name)
	return key.deleteErr
}

func (key *fakeRegistryValueKey) Close() error { return key.closeErr }

func emptyMarkerValues() map[string]registryValueSnapshot {
	values := make(map[string]registryValueSnapshot, len(markerNames))
	for _, name := range markerNames {
		values[name] = registryValueSnapshot{}
	}
	return values
}

func testRegistryState(path string, present bool, valueType uint32) registryState {
	state := registryState{
		PathPresent: present, PathType: valueType, Path: path,
		MarkerValues: emptyMarkerValues(),
	}
	if present {
		value, err := registryStringSnapshot(path, valueType)
		if err != nil {
			panic(err)
		}
		state.PathData = value.Data
	}
	return state
}

func setTestPathState(state *registryState, value registryValueSnapshot) {
	decoded, ok := decodeRegistryString(value)
	if !ok {
		panic("invalid test PATH value")
	}
	state.PathPresent = true
	state.PathType = value.Type
	state.Path = decoded
	state.PathData = append(state.PathData[:0], value.Data...)
}

const testClaimToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func writeClaimFixture(t *testing.T, path, token string) {
	writeClaimFixtureWithPhase(t, path, token, claimPhaseActive)
}

func writeClaimFixtureWithPhase(t *testing.T, path, token, phase string) {
	t.Helper()
	data, err := json.Marshal(transactionClaim{Schema: transactionClaimSchema, Token: token, Phase: phase})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transactionClaimPath(path), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeSnapshotFixture(t *testing.T, original, expected registryState) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	data, err := json.Marshal(pathSnapshot{
		Schema: pathSnapshotSchema, ClaimToken: testClaimToken,
		UserSID:  "S-1-5-21-2167388485-3820163381-827165627-1001",
		Original: original, Expected: expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	writeClaimFixture(t, path, testClaimToken)
	return path
}

func TestNormalizeUserSID(t *testing.T) {
	const value = "S-1-5-21-2167388485-3820163381-827165627-1001"
	got, err := normalizeUserSID(value)
	if err != nil || got != value {
		t.Fatalf("normalizeUserSID() = %q, %v", got, err)
	}
	if _, err := normalizeUserSID(`..\Environment`); err == nil {
		t.Fatal("registry-path text was accepted as a SID")
	}
	if got := userRegistryPath(value, `Environment`); got != value+`\Environment` {
		t.Fatalf("userRegistryPath() = %q", got)
	}
}

func TestRawPathValidationAcceptsLegalRepresentationsAndRejectsMalformedData(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
		ok   bool
	}{
		{name: "zero-byte empty", data: nil, want: "", ok: true},
		{name: "unterminated", data: rawUTF16('A', 'B'), want: "AB", ok: true},
		{name: "one terminator", data: rawUTF16('A', 0), want: "A", ok: true},
		{name: "multiple trailing terminators", data: rawUTF16('A', 0, 0, 0), want: "A", ok: true},
		{name: "surrogate pair", data: rawUTF16(0xd83d, 0xde80), want: "🚀", ok: true},
		{name: "embedded NUL followed by data", data: rawUTF16('A', 0, 'B'), ok: false},
		{name: "odd byte length", data: []byte{'A'}, ok: false},
		{name: "unpaired high surrogate", data: rawUTF16(0xd800), ok: false},
		{name: "unpaired low surrogate", data: rawUTF16(0xdc00), ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := decodeRegistryString(registryValueSnapshot{Present: true, Type: registry.EXPAND_SZ, Data: test.data})
			if ok != test.ok || got != test.want {
				t.Fatalf("decode = %q, %v; want %q, %v", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestReadUserPathSnapshotsExactRawBytesAndRejectsMalformedData(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	raw := rawUTF16('%', 'X', '%', '\\', 'b', 'i', 'n', 0, 0)
	value, valueType, present, data, err := readUserPathWithOpen(sid, func(path string) (registryRawValueKey, error) {
		if path != userRegistryPath(sid, `Environment`) {
			t.Fatalf("opened path = %q", path)
		}
		return &fakeRawRegistryKey{data: raw, valueType: registry.EXPAND_SZ}, nil
	})
	if err != nil || !present || valueType != registry.EXPAND_SZ || value != `%X%\bin` || !bytes.Equal(data, raw) {
		t.Fatalf("raw PATH snapshot = %q type=%d present=%v data=%v err=%v", value, valueType, present, data, err)
	}

	malformed := rawUTF16('A', 0, 'B')
	_, _, _, _, err = readUserPathWithOpen(sid, func(string) (registryRawValueKey, error) {
		return &fakeRawRegistryKey{data: malformed, valueType: registry.SZ}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed PATH read error = %v", err)
	}
}

func TestPathEqualityUsesTargetTokenEnvironmentNotProcessEnvironment(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	t.Setenv("USERPROFILE", `C:\ProcessUser`)
	expand, err := environmentExpanderForUserSIDWithSource(sid, func() (string, []string, error) {
		return sid, []string{
			`USERPROFILE=C:\TargetUser`,
			`A=%B%`,
			`B=C:\Nested`,
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pathownership.PlanApply(`%USERPROFILE%\Apps\Inlaid`, true, pathownership.Marker{}, `C:\TargetUser\Apps\Inlaid`, expand)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Path != `%USERPROFILE%\Apps\Inlaid` || plan.Marker.Owned || plan.Warn == "" {
		t.Fatalf("target-user equality plan = %+v", plan)
	}
	if got := expand(`%A%`); got != `%B%` {
		t.Fatalf("expansion was recursive instead of one bounded pass: %q", got)
	}
	if _, err := environmentExpanderForUserSIDWithSource(sid, func() (string, []string, error) {
		return "S-1-5-18", nil, nil
	}); err == nil {
		t.Fatal("environment from a different token SID was accepted")
	}
}

func TestInjectedFailureRequiresTestBuild(t *testing.T) {
	previous := testHooks
	t.Cleanup(func() { testHooks = previous })
	state := filepath.Join(t.TempDir(), "state.json")
	testHooks = "false"
	if err := run("fail", "", "", state, ""); err != nil {
		t.Fatalf("production helper accepted the failure hook: %v", err)
	}
	testHooks = "true"
	if err := run("fail", "", "", state, ""); err == nil {
		t.Fatal("test helper did not inject the requested failure")
	}
}

func TestFailureDiagnosticRequiresTestBuild(t *testing.T) {
	previous := testHooks
	t.Cleanup(func() { testHooks = previous })
	state := filepath.Join(t.TempDir(), "state.json")
	testHooks = "false"
	path, err := writeTestFailureDiagnostic(state, "apply", errors.New("expected failure"))
	if err != nil || path != "" {
		t.Fatalf("production diagnostic = %q, %v", path, err)
	}
	matches, err := filepath.Glob(state + ".*.test-error.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("production build emitted diagnostics: %v", matches)
	}
}

func TestFailureDiagnosticIsExactAndExclusive(t *testing.T) {
	previous := testHooks
	t.Cleanup(func() { testHooks = previous })
	testHooks = "true"
	state := filepath.Join(t.TempDir(), "state.json")
	path, err := writeTestFailureDiagnostic(state, "apply", errors.New("wrapped operation: exact cause"))
	if err != nil {
		t.Fatal(err)
	}
	wantPath := state + fmt.Sprintf(".apply.%d.test-error.json", os.Getpid())
	if path != wantPath {
		t.Fatalf("diagnostic path = %q; want %q", path, wantPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got testFailureDiagnostic
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != (testFailureDiagnostic{Schema: 1, Action: "apply", Error: "wrapped operation: exact cause"}) {
		t.Fatalf("diagnostic = %+v", got)
	}
	original := append([]byte(nil), data...)
	if _, err := writeTestFailureDiagnostic(state, "apply", errors.New("replacement")); err == nil || !strings.Contains(err.Error(), "exclusively create") {
		t.Fatalf("second diagnostic write error = %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, original) {
		t.Fatalf("second diagnostic write changed the first record: %q", data)
	}
}

func TestFailureDiagnosticRejectsUnsafeInputs(t *testing.T) {
	previous := testHooks
	t.Cleanup(func() { testHooks = previous })
	testHooks = "true"
	if path, err := writeTestFailureDiagnostic("relative-state.json", "apply", errors.New("failure")); err == nil || path != "" {
		t.Fatalf("relative diagnostic state = %q, %v", path, err)
	}
	state := filepath.Join(t.TempDir(), "state.json")
	if path, err := writeTestFailureDiagnostic(state, "unknown", errors.New("failure")); err != nil || path != "" {
		t.Fatalf("unsupported diagnostic action = %q, %v", path, err)
	}
	if path, err := writeTestFailureDiagnostic(state, "apply", nil); err != nil || path != "" {
		t.Fatalf("nil diagnostic failure = %q, %v", path, err)
	}
}

func TestPreflightRejectsStaleSameProductTransactionState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "clean.json")
	if err := run("preflight", "", "", state, ""); err != nil {
		t.Fatalf("clean transaction preflight failed: %v", err)
	}
	if _, err := readTransactionClaim(state); err != nil {
		t.Fatalf("clean transaction preflight did not publish its claim: %v", err)
	}
	if err := removeTransactionClaim(state); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		suffix  string
		message string
	}{
		{"", "stale transaction state"},
		{".partial", "unpublished transaction snapshot"},
		{".claim", "transaction claim is malformed"},
		{".claim.partial", "stale transaction state"},
	}
	for _, test := range tests {
		t.Run("stale"+test.suffix, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "inlaid-path-product-code.json")
			candidate := state + test.suffix
			if err := os.WriteFile(candidate, []byte("stale"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := run("preflight", "", "", state, "")
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("stale transaction preflight error = %v", err)
			}
			if data, readErr := os.ReadFile(candidate); readErr != nil || string(data) != "stale" {
				t.Fatalf("preflight changed stale state: %q, %v", data, readErr)
			}
		})
	}
}

func TestWriteSnapshotNeverOverwritesExistingState(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	state := testRegistryState(`C:\existing`, true, registry.EXPAND_SZ)
	snapshot := pathSnapshot{
		Schema: pathSnapshotSchema, ClaimToken: testClaimToken,
		UserSID: sid, Original: state, Expected: state,
	}

	t.Run("full snapshot", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state.json")
		writeClaimFixtureWithPhase(t, state, testClaimToken, claimPhasePreflight)
		original := []byte("existing-full-state")
		if err := os.WriteFile(state, original, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeSnapshot(state, snapshot); err == nil || !strings.Contains(err.Error(), "stale transaction state") {
			t.Fatalf("existing full snapshot write error = %v", err)
		}
		data, err := os.ReadFile(state)
		if err != nil || string(data) != string(original) {
			t.Fatalf("existing full snapshot changed: %q, %v", data, err)
		}
		if _, err := os.Lstat(state + ".partial"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("full-state rejection published or consumed a partial snapshot: %v", err)
		}
	})

	t.Run("partial snapshot", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state.json")
		writeClaimFixtureWithPhase(t, state, testClaimToken, claimPhasePreflight)
		partial := state + ".partial"
		original := []byte("existing-partial-state")
		if err := os.WriteFile(partial, original, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeSnapshot(state, snapshot); err == nil || !strings.Contains(err.Error(), "stale transaction state") {
			t.Fatalf("existing partial snapshot write error = %v", err)
		}
		data, err := os.ReadFile(partial)
		if err != nil || string(data) != string(original) {
			t.Fatalf("existing partial snapshot changed: %q, %v", data, err)
		}
		if _, err := os.Lstat(state); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial-state rejection published or consumed a final snapshot: %v", err)
		}
	})
}

func TestPreflightClaimAllowsCurrentTransactionSnapshotPublication(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := prepareTransactionClaim(statePath); err != nil {
		t.Fatal(err)
	}
	token, err := readTransactionClaim(statePath)
	if err != nil {
		t.Fatal(err)
	}
	state := testRegistryState(`C:\Original`, true, registry.EXPAND_SZ)
	snapshot := pathSnapshot{
		Schema: pathSnapshotSchema, ClaimToken: token,
		UserSID: sid, Original: state, Expected: state,
	}
	if err := writeSnapshot(statePath, snapshot); err != nil {
		t.Fatalf("publish after preflight = %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("published snapshot is missing: %v", err)
	}
	if got, err := readTransactionClaim(statePath); err != nil || got != token {
		t.Fatalf("published claim = %q, %v", got, err)
	}
}

func TestPublishSnapshotNeverReplacesRacingFinalState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	partial := state + ".partial"
	partialBytes := []byte("current-transaction-partial")
	finalBytes := []byte("racing-final-state")
	if err := os.WriteFile(partial, partialBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, finalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishSnapshotNoReplace(partial, state); err == nil {
		t.Fatal("snapshot publication replaced a racing final state")
	}
	gotFinal, finalErr := os.ReadFile(state)
	gotPartial, partialErr := os.ReadFile(partial)
	if finalErr != nil || string(gotFinal) != string(finalBytes) {
		t.Fatalf("racing final state changed: %q, %v", gotFinal, finalErr)
	}
	if partialErr != nil || string(gotPartial) != string(partialBytes) {
		t.Fatalf("failed publication consumed partial state: %q, %v", gotPartial, partialErr)
	}
}

func TestRollbackAndCommitNeverConsumePartialState(t *testing.T) {
	for _, action := range []string{"rollback", "commit"} {
		t.Run(action, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state.json")
			partial := state + ".partial"
			original := []byte("unpublished-transaction-state")
			if err := os.WriteFile(partial, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := run(action, "", "", state, ""); err == nil {
				t.Fatalf("%s accepted partial-only state", action)
			}
			data, err := os.ReadFile(partial)
			if err != nil || string(data) != string(original) {
				t.Fatalf("%s consumed partial state: %q, %v", action, data, err)
			}
		})
	}
}

func TestTransactionTeardownFailuresRemainSafelyRestartable(t *testing.T) {
	original := testRegistryState(`C:\Original`, true, registry.EXPAND_SZ)
	expected := testRegistryState(`C:\Expected`, true, registry.EXPAND_SZ)
	t.Run("preflight claim without snapshot is safely removable", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state.json")
		if err := prepareTransactionClaim(state); err != nil {
			t.Fatal(err)
		}
		if err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
			readState: func(string) (registryState, error) {
				t.Fatal("pre-snapshot rollback accessed registry state")
				return registryState{}, nil
			},
		}); err != nil {
			t.Fatalf("pre-snapshot rollback = %v", err)
		}
		for _, candidate := range []string{state, state + ".partial", transactionClaimPath(state), transactionClaimTransitionPath(state)} {
			if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pre-snapshot rollback retained %s: %v", candidate, err)
			}
		}
	})

	t.Run("published preflight snapshot rolls back idempotently", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state.json")
		writeClaimFixtureWithPhase(t, state, testClaimToken, claimPhasePreflight)
		data, err := json.Marshal(pathSnapshot{
			Schema: pathSnapshotSchema, ClaimToken: testClaimToken,
			UserSID:  "S-1-5-21-2167388485-3820163381-827165627-1001",
			Original: original, Expected: expected,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(state, data, 0o600); err != nil {
			t.Fatal(err)
		}
		reads := 0
		if err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
			readState: func(string) (registryState, error) {
				reads++
				return cloneRegistryState(original), nil
			},
			writePath: func(string, registryValueSnapshot) error {
				t.Fatal("idempotent preflight rollback wrote PATH")
				return nil
			},
			deletePath: func(string) error {
				t.Fatal("idempotent preflight rollback deleted PATH")
				return nil
			},
			restoreMarker: func(string, bool, map[string]registryValueSnapshot) error {
				t.Fatal("idempotent preflight rollback wrote provenance")
				return nil
			},
		}); err != nil || reads != 3 {
			t.Fatalf("idempotent preflight rollback = %v, reads=%d", err, reads)
		}
	})

	t.Run("active transition failure retains preflight snapshot for rollback", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state.json")
		writeClaimFixtureWithPhase(t, state, testClaimToken, claimPhasePreflight)
		data, err := json.Marshal(pathSnapshot{
			Schema: pathSnapshotSchema, ClaimToken: testClaimToken,
			UserSID:  "S-1-5-21-2167388485-3820163381-827165627-1001",
			Original: original, Expected: expected,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(state, data, 0o600); err != nil {
			t.Fatal(err)
		}
		claimBefore, err := os.ReadFile(transactionClaimPath(state))
		if err != nil {
			t.Fatal(err)
		}
		const otherToken = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
		if err := transitionTransactionClaimToActive(state, otherToken); err == nil || !strings.Contains(err.Error(), "different PATH transaction claim") {
			t.Fatalf("active transition under racing token = %v", err)
		}
		claimAfter, err := os.ReadFile(transactionClaimPath(state))
		if err != nil || !bytes.Equal(claimAfter, claimBefore) {
			t.Fatalf("racing transition overwrote claim: %q, %v", claimAfter, err)
		}
		transitionResidue := transactionClaimTransitionPath(state)
		if err := os.Mkdir(transitionResidue, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := transitionTransactionClaimToActive(state, testClaimToken); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("active transition with protected residue = %v", err)
		}
		claim, err := readTransactionClaimState(state)
		if err != nil || claim.Phase != claimPhasePreflight {
			t.Fatalf("claim after failed active transition = %+v, %v", claim, err)
		}
		if _, err := os.Stat(state); err != nil {
			t.Fatalf("failed active transition lost snapshot: %v", err)
		}
		if err := os.Remove(transitionResidue); err != nil {
			t.Fatal(err)
		}
		if err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
			readState: func(string) (registryState, error) { return cloneRegistryState(original), nil },
		}); err != nil {
			t.Fatalf("rollback after failed active transition = %v", err)
		}
	})

	t.Run("claim transition only resumes authenticated residue", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state.json")
		writeClaimFixtureWithPhase(t, state, testClaimToken, claimPhasePreflight)
		transition := transactionClaimTransitionPath(state)
		malformed := []byte("foreign transition residue")
		if err := os.WriteFile(transition, malformed, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := transitionTransactionClaimToActive(state, testClaimToken); err == nil || !strings.Contains(err.Error(), "not authenticated") {
			t.Fatalf("transition accepted foreign residue: %v", err)
		}
		got, err := os.ReadFile(transition)
		if err != nil || !bytes.Equal(got, malformed) {
			t.Fatalf("transition consumed foreign residue: %q, %v", got, err)
		}
		claim, err := readTransactionClaimState(state)
		if err != nil || claim.Phase != claimPhasePreflight {
			t.Fatalf("foreign residue changed claim: %+v, %v", claim, err)
		}
		if err := os.Remove(transition); err != nil {
			t.Fatal(err)
		}
		writeClaimFixtureWithPhase(t, state+".transition", testClaimToken, claimPhaseActive)
		if err := os.Rename(transactionClaimPath(state+".transition"), transition); err != nil {
			t.Fatal(err)
		}
		if err := transitionTransactionClaimToActive(state, testClaimToken); err != nil {
			t.Fatalf("resume authenticated active transition = %v", err)
		}
		claim, err = readTransactionClaimState(state)
		if err != nil || claim.Phase != claimPhaseActive {
			t.Fatalf("resumed active claim = %+v, %v", claim, err)
		}
		if _, err := os.Lstat(transition); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("resumed transition retained residue: %v", err)
		}
	})

	t.Run("cleanup transition failure retains active authenticated state", func(t *testing.T) {
		state := writeSnapshotFixture(t, original, expected)
		transitionErr := errors.New("cleanup transition denied")
		operations := defaultTransactionTeardownOperations()
		operations.markCleanup = func(string, string) error { return transitionErr }
		removed := false
		operations.removeSnapshot = func(string) error { removed = true; return nil }
		operations.removeClaim = func(string) error { removed = true; return nil }
		if err := completeTransactionTeardown(state, testClaimToken, operations); !errors.Is(err, transitionErr) || removed {
			t.Fatalf("cleanup transition failure = %v, removed=%v", err, removed)
		}
		claim, err := readTransactionClaimState(state)
		if err != nil || claim.Phase != claimPhaseActive {
			t.Fatalf("active claim after transition failure = %+v, %v", claim, err)
		}
		registryReads := 0
		if err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
			readState: func(string) (registryState, error) {
				registryReads++
				return cloneRegistryState(original), nil
			},
			broadcast: func() {},
		}); err != nil {
			t.Fatalf("rollback restart after transition failure = %v", err)
		}
		if registryReads == 0 {
			t.Fatal("active-claim restart skipped required registry verification")
		}
	})

	t.Run("snapshot deletion failure is recovered by later preflight", func(t *testing.T) {
		state := writeSnapshotFixture(t, original, expected)
		removeErr := errors.New("snapshot delete denied")
		operations := defaultTransactionTeardownOperations()
		operations.removeSnapshot = func(string) error { return removeErr }
		if err := completeTransactionTeardown(state, testClaimToken, operations); !errors.Is(err, removeErr) {
			t.Fatalf("snapshot deletion failure = %v", err)
		}
		claim, err := readTransactionClaimState(state)
		if err != nil || claim.Phase != claimPhaseCleanup {
			t.Fatalf("cleanup claim after snapshot failure = %+v, %v", claim, err)
		}
		if _, err := os.Stat(state); err != nil {
			t.Fatalf("snapshot deletion failure lost snapshot: %v", err)
		}
		if err := prepareTransactionClaim(state); err != nil {
			t.Fatalf("later preflight after snapshot deletion failure = %v", err)
		}
		if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("later preflight retained snapshot: %v", err)
		}
		fresh, err := readTransactionClaimState(state)
		if err != nil || fresh.Phase != claimPhasePreflight || fresh.Token == testClaimToken {
			t.Fatalf("later preflight claim = %+v, %v", fresh, err)
		}
	})

	t.Run("later preflight does not consume replaced cleanup snapshot", func(t *testing.T) {
		state := writeSnapshotFixture(t, original, expected)
		if err := transitionTransactionClaimToCleanup(state, testClaimToken); err != nil {
			t.Fatal(err)
		}
		foreign := []byte("foreign replacement")
		if err := os.WriteFile(state, foreign, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := prepareTransactionClaim(state); err == nil || !strings.Contains(err.Error(), "does not match its claim") {
			t.Fatalf("later preflight accepted replaced cleanup snapshot: %v", err)
		}
		got, err := os.ReadFile(state)
		if err != nil || !bytes.Equal(got, foreign) {
			t.Fatalf("later preflight consumed replaced cleanup snapshot: %q, %v", got, err)
		}
		claim, err := readTransactionClaimState(state)
		if err != nil || claim.Phase != claimPhaseCleanup || claim.Token != testClaimToken {
			t.Fatalf("later preflight changed cleanup claim: %+v, %v", claim, err)
		}
	})

	t.Run("claim deletion failure is recovered by later preflight", func(t *testing.T) {
		state := writeSnapshotFixture(t, original, expected)
		removeErr := errors.New("claim delete denied")
		operations := defaultTransactionTeardownOperations()
		operations.removeClaim = func(string) error { return removeErr }
		if err := completeTransactionTeardown(state, testClaimToken, operations); !errors.Is(err, removeErr) {
			t.Fatalf("claim deletion failure = %v", err)
		}
		if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("claim deletion failure retained snapshot: %v", err)
		}
		claim, err := readTransactionClaimState(state)
		if err != nil || claim.Phase != claimPhaseCleanup {
			t.Fatalf("cleanup claim after claim failure = %+v, %v", claim, err)
		}
		if err := prepareTransactionClaim(state); err != nil {
			t.Fatalf("later preflight after claim deletion failure = %v", err)
		}
		fresh, err := readTransactionClaimState(state)
		if err != nil || fresh.Phase != claimPhasePreflight || fresh.Token == testClaimToken {
			t.Fatalf("later preflight claim = %+v, %v", fresh, err)
		}
	})

	t.Run("active claim without snapshot never skips rollback", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state.json")
		writeClaimFixture(t, state, testClaimToken)
		if err := prepareTransactionClaim(state); err == nil || !strings.Contains(err.Error(), "stale active PATH transaction") {
			t.Fatalf("later preflight accepted active claim without snapshot: %v", err)
		}
		registryCalls := 0
		err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
			readState: func(string) (registryState, error) {
				registryCalls++
				return registryState{}, nil
			},
		})
		if err == nil || registryCalls != 0 || !strings.Contains(err.Error(), "active PATH transaction claim") {
			t.Fatalf("active claim-only rollback = %v, registry calls=%d", err, registryCalls)
		}
		if _, err := os.Stat(transactionClaimPath(state)); err != nil {
			t.Fatalf("active claim-only rollback consumed claim: %v", err)
		}
	})

	t.Run("later preflight never consumes active authenticated state", func(t *testing.T) {
		state := writeSnapshotFixture(t, original, expected)
		snapshotBefore, err := os.ReadFile(state)
		if err != nil {
			t.Fatal(err)
		}
		claimBefore, err := os.ReadFile(transactionClaimPath(state))
		if err != nil {
			t.Fatal(err)
		}
		if err := prepareTransactionClaim(state); err == nil || !strings.Contains(err.Error(), "stale active PATH transaction") {
			t.Fatalf("later preflight accepted active transaction: %v", err)
		}
		snapshotAfter, snapshotErr := os.ReadFile(state)
		claimAfter, claimErr := os.ReadFile(transactionClaimPath(state))
		if snapshotErr != nil || claimErr != nil || !bytes.Equal(snapshotAfter, snapshotBefore) || !bytes.Equal(claimAfter, claimBefore) {
			t.Fatalf("later preflight changed active transaction: snapshot=%v claim=%v", snapshotErr, claimErr)
		}
	})
}

func TestDeleteUserPathReportsUnexpectedRegistryErrors(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"

	t.Run("missing environment key", func(t *testing.T) {
		err := deleteUserPathWithOpen(sid, func(string) (registryValueKey, error) {
			return nil, registry.ErrNotExist
		})
		if err != nil {
			t.Fatalf("missing environment key was not treated as already absent: %v", err)
		}
	})

	t.Run("open failure", func(t *testing.T) {
		err := deleteUserPathWithOpen(sid, func(string) (registryValueKey, error) {
			return nil, errors.New("access denied")
		})
		if err == nil || !strings.Contains(err.Error(), "open current-user environment") {
			t.Fatalf("unexpected open error = %v", err)
		}
	})

	t.Run("missing PATH value", func(t *testing.T) {
		key := &fakeRegistryValueKey{deleteErr: registry.ErrNotExist}
		err := deleteUserPathWithOpen(sid, func(path string) (registryValueKey, error) {
			if path != userRegistryPath(sid, `Environment`) {
				t.Fatalf("opened registry path = %q", path)
			}
			return key, nil
		})
		if err != nil || len(key.deleted) != 1 || key.deleted[0] != "Path" {
			t.Fatalf("missing PATH delete = %v, %#v", err, key.deleted)
		}
	})

	t.Run("delete failure", func(t *testing.T) {
		key := &fakeRegistryValueKey{deleteErr: errors.New("access denied")}
		err := deleteUserPathWithOpen(sid, func(string) (registryValueKey, error) { return key, nil })
		if err == nil || !strings.Contains(err.Error(), "restore absent current-user PATH") {
			t.Fatalf("unexpected delete error = %v", err)
		}
	})

	t.Run("delete and close failures", func(t *testing.T) {
		key := &fakeRegistryValueKey{
			deleteErr: errors.New("delete denied"),
			closeErr:  errors.New("close failed"),
		}
		err := deleteUserPathWithOpen(sid, func(string) (registryValueKey, error) { return key, nil })
		if err == nil || !strings.Contains(err.Error(), "delete denied") || !strings.Contains(err.Error(), "close failed") {
			t.Fatalf("combined delete/close error = %v", err)
		}
	})

	t.Run("close failure", func(t *testing.T) {
		key := &fakeRegistryValueKey{closeErr: errors.New("close failed")}
		err := deleteUserPathWithOpen(sid, func(string) (registryValueKey, error) { return key, nil })
		if err == nil || !strings.Contains(err.Error(), "close current-user environment") {
			t.Fatalf("unexpected close error = %v", err)
		}
	})
}

func TestRestoreOriginallyAbsentPathIsExactAndRetryable(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	original := testRegistryState("", false, registry.EXPAND_SZ)
	expected := testRegistryState(`C:\Programs\Inlaid`, true, registry.EXPAND_SZ)
	markerValues, err := markerValuesForPlan(pathownership.Marker{
		Present: true, Valid: true, Owned: true,
		NormalizedProgramDirectory: `C:\Programs\Inlaid`, InsertedSegment: `C:\Programs\Inlaid`,
	})
	if err != nil {
		t.Fatal(err)
	}
	expected.MarkerKeyExists = true
	expected.MarkerValues = markerValues

	t.Run("absent is restored as absent", func(t *testing.T) {
		path := writeSnapshotFixture(t, original, expected)
		current := cloneRegistryState(expected)
		deleted := false
		markerRestored := false
		broadcast := false
		err := restoreSnapshotWithOperations(path, snapshotRestoreOperations{
			readState: func(string) (registryState, error) { return cloneRegistryState(current), nil },
			writePath: func(string, registryValueSnapshot) error {
				t.Fatal("absent PATH restoration attempted to write PATH")
				return nil
			},
			deletePath: func(gotSID string) error {
				deleted = gotSID == sid
				current.PathPresent = false
				current.PathType = registry.EXPAND_SZ
				current.Path = ""
				current.PathData = nil
				return nil
			},
			restoreMarker: func(gotSID string, existed bool, _ map[string]registryValueSnapshot) error {
				markerRestored = gotSID == sid && !existed
				current.MarkerKeyExists = original.MarkerKeyExists
				current.MarkerValues = emptyMarkerValues()
				return nil
			},
			broadcast: func() { broadcast = true },
		})
		if err != nil || !deleted || !markerRestored || !broadcast {
			t.Fatalf("restore result = %v, deleted=%v marker=%v broadcast=%v", err, deleted, markerRestored, broadcast)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("successful restore retained snapshot: %v", err)
		}
		if _, err := os.Stat(transactionClaimPath(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("successful restore retained claim: %v", err)
		}
	})

	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "open failure", err: errors.New("open current-user environment: access denied")},
		{name: "delete failure", err: errors.New("restore absent current-user PATH: access denied")},
		{name: "close failure", err: errors.New("close current-user environment after PATH removal: access denied")},
	} {
		t.Run(failure.name+" retains snapshot", func(t *testing.T) {
			path := writeSnapshotFixture(t, original, expected)
			err := restoreSnapshotWithOperations(path, snapshotRestoreOperations{
				readState:  func(string) (registryState, error) { return cloneRegistryState(expected), nil },
				writePath:  func(string, registryValueSnapshot) error { return nil },
				deletePath: func(string) error { return failure.err },
				restoreMarker: func(string, bool, map[string]registryValueSnapshot) error {
					t.Fatal("marker restoration continued after PATH restoration failed")
					return nil
				},
				broadcast: func() { t.Fatal("failed restoration broadcast an environment change") },
			})
			if !errors.Is(err, failure.err) {
				t.Fatalf("restore error = %v", err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("failed restoration consumed snapshot: %v", err)
			}
			if _, err := os.Stat(transactionClaimPath(path)); err != nil {
				t.Fatalf("failed restoration consumed claim: %v", err)
			}
		})
	}
}

func TestApplyFailureRollbackRetainsSafeEmptyMarkerKeyAndRetrySnapshot(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	const program = `C:\Programs\Inlaid`
	original := testRegistryState(`C:\Original`, true, registry.EXPAND_SZ)
	expected := cloneRegistryState(original)
	markerValues, err := markerValuesForPlan(pathownership.Marker{
		Present: true, Valid: true, Owned: true,
		NormalizedProgramDirectory: program, InsertedSegment: program,
		PathValueExistedBeforeOwnership: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	expected.MarkerKeyExists = true
	expected.MarkerValues = markerValues
	state := writeSnapshotFixture(t, original, expected)
	current := cloneRegistryState(expected)
	restoreErr := errors.New("remove installer-owned provenance values: access denied")

	operations := snapshotRestoreOperations{
		readState: func(string) (registryState, error) { return cloneRegistryState(current), nil },
		writePath: func(string, registryValueSnapshot) error {
			t.Fatal("marker-only rollback attempted to write PATH")
			return nil
		},
		deletePath: func(string) error {
			t.Fatal("marker-only rollback attempted to delete PATH")
			return nil
		},
		restoreMarker: func(gotSID string, keyExisted bool, _ map[string]registryValueSnapshot) error {
			if gotSID != sid || keyExisted {
				t.Fatalf("restore marker inputs = %q, existed=%v", gotSID, keyExisted)
			}
			if restoreErr != nil {
				return restoreErr
			}
			current.MarkerKeyExists = true
			current.MarkerValues = emptyMarkerValues()
			return nil
		},
		broadcast: func() {},
	}
	if err := restoreSnapshotWithOperations(state, operations); !errors.Is(err, restoreErr) {
		t.Fatalf("failed marker rollback = %v", err)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("failed marker rollback consumed snapshot: %v", err)
	}
	if _, err := os.Stat(transactionClaimPath(state)); err != nil {
		t.Fatalf("failed marker rollback consumed claim: %v", err)
	}

	restoreErr = nil
	if err := restoreSnapshotWithOperations(state, operations); err != nil {
		t.Fatalf("retried marker rollback = %v", err)
	}
	if !current.MarkerKeyExists || markerHasPresentValues(current) {
		t.Fatal("retried apply-failure rollback did not leave a safe empty marker key")
	}
}

func TestRollbackPreservesOriginallyPresentEmptyMarkerKey(t *testing.T) {
	original := testRegistryState(`C:\Original`, true, registry.EXPAND_SZ)
	original.MarkerKeyExists = true
	expected := cloneRegistryState(original)
	markerValues, err := markerValuesForPlan(pathownership.Marker{
		Present: true, Valid: true, Owned: true,
		NormalizedProgramDirectory:      `C:\Programs\Inlaid`,
		InsertedSegment:                 `C:\Programs\Inlaid`,
		PathValueExistedBeforeOwnership: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	expected.MarkerValues = markerValues
	state := writeSnapshotFixture(t, original, expected)
	current := cloneRegistryState(expected)
	err = restoreSnapshotWithOperations(state, snapshotRestoreOperations{
		readState: func(string) (registryState, error) { return cloneRegistryState(current), nil },
		restoreMarker: func(_ string, keyExisted bool, values map[string]registryValueSnapshot) error {
			if !keyExisted {
				t.Fatal("present-empty marker key was treated as absent")
			}
			current.MarkerKeyExists = true
			current.MarkerValues = cloneRegistryState(registryState{MarkerValues: values}).MarkerValues
			return nil
		},
		broadcast: func() {},
	})
	if err != nil || !current.MarkerKeyExists {
		t.Fatalf("present-empty marker rollback = %v, state=%+v", err, current)
	}
	for _, name := range markerNames {
		if current.MarkerValues[name].Present {
			t.Fatalf("present-empty marker retained %s", name)
		}
	}
}

func TestRestorePreservesPresentPathValueKind(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	original := testRegistryState(`%USERPROFILE%\bin`, true, registry.SZ)
	expected := testRegistryState(`%USERPROFILE%\bin;C:\Programs\Inlaid`, true, registry.SZ)
	path := writeSnapshotFixture(t, original, expected)
	current := cloneRegistryState(expected)
	wrote := false
	err := restoreSnapshotWithOperations(path, snapshotRestoreOperations{
		readState: func(string) (registryState, error) { return cloneRegistryState(current), nil },
		writePath: func(gotSID string, value registryValueSnapshot) error {
			decoded, ok := decodeRegistryString(value)
			wrote = gotSID == sid && ok && decoded == `%USERPROFILE%\bin` && value.Type == registry.SZ
			setTestPathState(&current, value)
			return nil
		},
		deletePath: func(string) error {
			t.Fatal("present PATH restoration attempted to delete PATH")
			return nil
		},
		restoreMarker: func(string, bool, map[string]registryValueSnapshot) error { return nil },
		broadcast:     func() {},
	})
	if err != nil || !wrote {
		t.Fatalf("present PATH kind restore = %v, wrote=%v", err, wrote)
	}
}

func TestRollbackRestoresExactRawPathBytesAndRetainsSnapshotOnFailure(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	original := testRegistryState(`C:\Original`, true, registry.EXPAND_SZ)
	original.PathData = rawUTF16('C', ':', '\\', 'O', 'r', 'i', 'g', 'i', 'n', 'a', 'l', 0, 0, 0)
	expected := testRegistryState(`C:\Expected`, true, registry.EXPAND_SZ)

	t.Run("exact restoration", func(t *testing.T) {
		state := writeSnapshotFixture(t, original, expected)
		current := cloneRegistryState(expected)
		var written registryValueSnapshot
		err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
			readState: func(string) (registryState, error) { return cloneRegistryState(current), nil },
			writePath: func(gotSID string, value registryValueSnapshot) error {
				if gotSID != sid {
					t.Fatalf("write SID = %q", gotSID)
				}
				written = registryValueSnapshot{Present: value.Present, Type: value.Type, Data: bytes.Clone(value.Data)}
				setTestPathState(&current, value)
				return nil
			},
			restoreMarker: func(string, bool, map[string]registryValueSnapshot) error { return nil },
			broadcast:     func() {},
		})
		if err != nil || written.Type != original.PathType || !bytes.Equal(written.Data, original.PathData) {
			t.Fatalf("exact raw rollback = type %d data=%v err=%v", written.Type, written.Data, err)
		}
	})

	t.Run("write failure retains retry state", func(t *testing.T) {
		state := writeSnapshotFixture(t, original, expected)
		writeErr := errors.New("raw RegSetValueExW failure")
		err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
			readState: func(string) (registryState, error) { return cloneRegistryState(expected), nil },
			writePath: func(string, registryValueSnapshot) error { return writeErr },
			broadcast: func() { t.Fatal("failed raw restore broadcast an environment change") },
		})
		if !errors.Is(err, writeErr) {
			t.Fatalf("raw restore failure = %v", err)
		}
		if _, err := os.Stat(state); err != nil {
			t.Fatalf("raw restore failure consumed snapshot: %v", err)
		}
		if _, err := os.Stat(transactionClaimPath(state)); err != nil {
			t.Fatalf("raw restore failure consumed claim: %v", err)
		}
	})
}

func TestRollbackRestoresLegalRawTerminatorVariantsExactly(t *testing.T) {
	tests := []struct {
		name string
		path string
		data []byte
	}{
		{name: "zero-byte empty", path: "", data: nil},
		{name: "unterminated", path: `C:\Original`, data: rawUTF16('C', ':', '\\', 'O', 'r', 'i', 'g', 'i', 'n', 'a', 'l')},
		{name: "multiple terminators", path: `C:\Original`, data: rawUTF16('C', ':', '\\', 'O', 'r', 'i', 'g', 'i', 'n', 'a', 'l', 0, 0, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := testRegistryState(test.path, true, registry.SZ)
			original.PathData = bytes.Clone(test.data)
			expected := testRegistryState(`C:\Expected`, true, registry.SZ)
			state := writeSnapshotFixture(t, original, expected)
			current := cloneRegistryState(expected)
			var restored []byte
			err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
				readState: func(string) (registryState, error) { return cloneRegistryState(current), nil },
				writePath: func(_ string, value registryValueSnapshot) error {
					restored = bytes.Clone(value.Data)
					setTestPathState(&current, value)
					return nil
				},
				restoreMarker: func(string, bool, map[string]registryValueSnapshot) error { return nil },
				broadcast:     func() {},
			})
			if err != nil || !bytes.Equal(restored, test.data) {
				t.Fatalf("raw restoration = %v, err=%v", restored, err)
			}
		})
	}
}

func TestRollbackRejectsMalformedRawPathSnapshotBeforeRegistryAccess(t *testing.T) {
	original := testRegistryState(`C:\Original`, true, registry.EXPAND_SZ)
	original.PathData = rawUTF16('C', ':', '\\', 0, 'X')
	expected := testRegistryState(`C:\Expected`, true, registry.EXPAND_SZ)
	state := writeSnapshotFixture(t, original, expected)
	registryCalls := 0
	err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
		readState: func(string) (registryState, error) {
			registryCalls++
			return registryState{}, nil
		},
		writePath: func(string, registryValueSnapshot) error {
			registryCalls++
			return nil
		},
		deletePath: func(string) error { registryCalls++; return nil },
		restoreMarker: func(string, bool, map[string]registryValueSnapshot) error {
			registryCalls++
			return nil
		},
	})
	if err == nil || registryCalls != 0 {
		t.Fatalf("malformed snapshot rollback = %v, registry calls=%d", err, registryCalls)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("malformed snapshot was consumed: %v", err)
	}
}

func TestMarkerStateRejectsInconsistentFields(t *testing.T) {
	const program = `C:\Programs\Inlaid`
	valid, err := markerValuesForPlan(pathownership.Marker{
		Present: true, Valid: true, Owned: true,
		NormalizedProgramDirectory: program, InsertedSegment: program,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(map[string]registryValueSnapshot){
		"owned without inserted segment": func(values map[string]registryValueSnapshot) {
			values["InsertedSegment"], _ = registryStringSnapshot("", registry.SZ)
		},
		"unowned with inserted segment": func(values map[string]registryValueSnapshot) {
			values["Owned"] = registryDWORDSnapshot(0)
		},
		"unowned with prior-presence provenance": func(values map[string]registryValueSnapshot) {
			values["Owned"] = registryDWORDSnapshot(0)
			values["InsertedSegment"], _ = registryStringSnapshot("", registry.SZ)
			values["PathValueExistedBeforeOwnership"] = registryDWORDSnapshot(1)
		},
		"missing component identity": func(values map[string]registryValueSnapshot) {
			values["Component"] = registryValueSnapshot{}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			state := registryState{MarkerKeyExists: true, MarkerValues: cloneRegistryState(registryState{MarkerValues: valid}).MarkerValues}
			mutate(state.MarkerValues)
			marker := markerFromRegistryState(state)
			if !marker.Present || marker.Valid {
				t.Fatalf("inconsistent marker = %+v", marker)
			}
		})
	}
}

func TestRacingFinalSnapshotIsNeverConsumed(t *testing.T) {
	original := testRegistryState(`C:\Original`, true, registry.EXPAND_SZ)
	expected := testRegistryState(`C:\Expected`, true, registry.EXPAND_SZ)
	state := writeSnapshotFixture(t, original, expected)
	partial := state + ".partial"
	if err := os.WriteFile(partial, []byte("this transaction did not publish the final"), 0o600); err != nil {
		t.Fatal(err)
	}
	read := false
	err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
		readState: func(string) (registryState, error) {
			read = true
			return expected, nil
		},
	})
	if err == nil || read {
		t.Fatalf("racing final rollback = %v, read=%v", err, read)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("racing final snapshot was consumed: %v", err)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatalf("publishing transaction evidence was consumed: %v", err)
	}
}

func TestRollbackRejectsFinalSnapshotFromAnotherTransactionClaim(t *testing.T) {
	original := testRegistryState(`C:\Original`, true, registry.EXPAND_SZ)
	expected := testRegistryState(`C:\Expected`, true, registry.EXPAND_SZ)
	state := writeSnapshotFixture(t, original, expected)
	const racingToken = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	writeClaimFixture(t, state, racingToken)
	read := false
	err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
		readState: func(string) (registryState, error) {
			read = true
			return expected, nil
		},
	})
	if err == nil || read || !strings.Contains(err.Error(), "not published under this transaction claim") {
		t.Fatalf("racing final rollback = %v, read=%v", err, read)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("racing final snapshot was consumed: %v", err)
	}
	if _, err := os.Stat(transactionClaimPath(state)); err != nil {
		t.Fatalf("racing transaction claim was consumed: %v", err)
	}
}

func TestTransactionRefusesConcurrentPathOrMarkerChanges(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	const program = `C:\Programs\Inlaid`
	original := testRegistryState(`C:\Original`, true, registry.EXPAND_SZ)
	plan, err := pathownership.PlanApply(original.Path, original.PathPresent, pathownership.Marker{}, program, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := expectedRegistryState(original, plan)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("changed before first write", func(t *testing.T) {
		concurrent := cloneRegistryState(original)
		concurrent.Path = `C:\Concurrent`
		concurrentValue, _ := registryStringSnapshot(concurrent.Path, concurrent.PathType)
		concurrent.PathData = concurrentValue.Data
		writes := 0
		err := executeRegistryTransaction(sid, original, expected, plan, registryTransactionOperations{
			readState:   func(string) (registryState, error) { return concurrent, nil },
			writePath:   func(string, registryValueSnapshot) error { writes++; return nil },
			deletePath:  func(string) error { writes++; return nil },
			writeMarker: func(string, pathownership.Marker) error { writes++; return nil },
		})
		if err == nil || writes != 0 {
			t.Fatalf("concurrent pre-write state = %v, writes=%d", err, writes)
		}
	})

	t.Run("marker changed after PATH write", func(t *testing.T) {
		current := cloneRegistryState(original)
		markerWrites := 0
		err := executeRegistryTransaction(sid, original, expected, plan, registryTransactionOperations{
			readState: func(string) (registryState, error) {
				return cloneRegistryState(current), nil
			},
			writePath: func(_ string, value registryValueSnapshot) error {
				setTestPathState(&current, value)
				current.MarkerKeyExists = true
				current.MarkerValues["Schema"] = registryDWORDSnapshot(99)
				return nil
			},
			deletePath:  func(string) error { t.Fatal("apply attempted to delete PATH"); return nil },
			writeMarker: func(string, pathownership.Marker) error { markerWrites++; return nil },
		})
		if err == nil || markerWrites != 0 || current.Path != expected.Path {
			t.Fatalf("mid-transaction marker race = %v, markerWrites=%d, state=%+v", err, markerWrites, current)
		}
	})
}

func TestRegistryTransactionDistinguishesAbsentFromPresentEmptyUninstall(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	const program = `C:\Programs\Inlaid`
	for _, test := range []struct {
		name        string
		priorExists bool
		wantDelete  bool
	}{
		{name: "originally absent", priorExists: false, wantDelete: true},
		{name: "originally present empty", priorExists: true, wantDelete: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := pathownership.Marker{
				Present: true, Valid: true, Owned: true,
				NormalizedProgramDirectory:      program,
				InsertedSegment:                 program,
				PathValueExistedBeforeOwnership: test.priorExists,
			}
			markerValues, err := markerValuesForPlan(marker)
			if err != nil {
				t.Fatal(err)
			}
			original := testRegistryState(program, true, registry.EXPAND_SZ)
			original.MarkerKeyExists = true
			original.MarkerValues = markerValues
			decoded := markerFromRegistryState(original)
			if !decoded.Valid || decoded.PathValueExistedBeforeOwnership != test.priorExists {
				t.Fatalf("decoded marker = %+v", decoded)
			}
			plan, err := pathownership.PlanUninstall(original.Path, original.PathPresent, decoded, program, nil)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := expectedRegistryState(original, plan)
			if err != nil {
				t.Fatal(err)
			}
			if expected.PathPresent == test.wantDelete {
				t.Fatalf("expected PATH presence = %v, want delete=%v", expected.PathPresent, test.wantDelete)
			}

			current := cloneRegistryState(original)
			deleted := false
			written := false
			err = executeRegistryTransaction(sid, original, expected, plan, registryTransactionOperations{
				readState: func(string) (registryState, error) { return cloneRegistryState(current), nil },
				writePath: func(_ string, value registryValueSnapshot) error {
					written = true
					setTestPathState(&current, value)
					return nil
				},
				deletePath: func(string) error {
					deleted = true
					current.PathPresent = false
					current.Path = ""
					current.PathType = registry.EXPAND_SZ
					current.PathData = nil
					return nil
				},
				writeMarker: func(string, pathownership.Marker) error {
					current.MarkerValues = emptyMarkerValues()
					return nil
				},
			})
			if err != nil || deleted != test.wantDelete || written == test.wantDelete {
				t.Fatalf("uninstall transaction = %v, deleted=%v written=%v", err, deleted, written)
			}
			if !current.MarkerKeyExists {
				t.Fatal("uninstall mutation deleted the marker key before MSI registry removal and commit")
			}
		})
	}
}

func TestUninstallFinalizerPreservesEmptyInstallerKeys(t *testing.T) {
	const sid = "S-1-5-21-2167388485-3820163381-827165627-1001"
	const program = `C:\Programs\Inlaid`
	marker := pathownership.Marker{
		Present: true, Valid: true, Owned: true,
		NormalizedProgramDirectory:      program,
		InsertedSegment:                 program,
		PathValueExistedBeforeOwnership: true,
	}
	values, err := markerValuesForPlan(marker)
	if err != nil {
		t.Fatal(err)
	}
	original := testRegistryState(program, true, registry.EXPAND_SZ)
	original.MarkerKeyExists = true
	original.MarkerValues = values
	plan, err := pathownership.PlanUninstall(original.Path, original.PathPresent, marker, program, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := expectedRegistryState(original, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !expected.MarkerKeyExists {
		t.Fatal("uninstall expected state did not retain the installer key")
	}
	state := writeSnapshotFixture(t, original, expected)
	current := cloneRegistryState(expected)
	reads := 0
	foreignValuePresent := false
	foreignSubkeyPresent := false
	operations := transactionFinalizeOperations{
		readState: func(gotSID string) (registryState, error) {
			if gotSID != sid {
				t.Fatalf("read SID = %q", gotSID)
			}
			reads++
			// Model foreign state arriving as the finalizer observes the empty
			// known-value set. The finalizer has no key-delete operation that can
			// erase either addition after this point.
			foreignValuePresent = true
			foreignSubkeyPresent = true
			return cloneRegistryState(current), nil
		},
	}
	if err := finalizeTransactionStateWithOperations(state, operations); err != nil {
		t.Fatalf("uninstall finalizer = %v", err)
	}
	if reads != 1 || !current.MarkerKeyExists || !foreignValuePresent || !foreignSubkeyPresent {
		t.Fatalf("finalizer reads=%d retained-key=%v foreign-value=%v foreign-subkey=%v", reads, current.MarkerKeyExists, foreignValuePresent, foreignSubkeyPresent)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("successful uninstall finalizer consumed snapshot before commit: %v", err)
	}
	if _, err := os.Stat(transactionClaimPath(state)); err != nil {
		t.Fatalf("successful uninstall finalizer consumed claim before commit: %v", err)
	}
}

func TestRollbackRefusesConcurrentThirdStateAndRetainsSnapshot(t *testing.T) {
	original := testRegistryState(`C:\Original`, true, registry.EXPAND_SZ)
	expected := testRegistryState(`C:\Expected`, true, registry.EXPAND_SZ)
	state := writeSnapshotFixture(t, original, expected)
	concurrent := cloneRegistryState(expected)
	concurrent.MarkerKeyExists = true
	concurrent.MarkerValues["Schema"] = registryDWORDSnapshot(99)
	writes := 0
	err := restoreSnapshotWithOperations(state, snapshotRestoreOperations{
		readState:     func(string) (registryState, error) { return cloneRegistryState(concurrent), nil },
		writePath:     func(string, registryValueSnapshot) error { writes++; return nil },
		deletePath:    func(string) error { writes++; return nil },
		restoreMarker: func(string, bool, map[string]registryValueSnapshot) error { writes++; return nil },
		broadcast:     func() { writes++ },
	})
	if err == nil || writes != 0 {
		t.Fatalf("concurrent rollback state = %v, writes=%d", err, writes)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("failed rollback consumed retry snapshot: %v", err)
	}
	if _, err := os.Stat(transactionClaimPath(state)); err != nil {
		t.Fatalf("failed rollback consumed retry claim: %v", err)
	}
}
