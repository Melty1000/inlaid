//go:build windows

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"github.com/Melty1000/inlaid/internal/pathownership"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var testHooks = "false"

const (
	markerKey              = `Software\Inlaid\Installer`
	pathSnapshotSchema     = 3
	transactionClaimSchema = 3
	claimPhasePreflight    = "preflight"
	claimPhaseActive       = "active"
	claimPhaseCleanup      = "cleanup"
)

var markerNames = []string{
	"Schema",
	"NormalizedProgramDirectory",
	"InsertedSegment",
	"Owned",
	"PathValueExistedBeforeOwnership",
	"Component",
}

type registryValueKey interface {
	DeleteValue(string) error
	Close() error
}

type snapshotRestoreOperations struct {
	readState     func(string) (registryState, error)
	writePath     func(string, registryValueSnapshot) error
	deletePath    func(string) error
	restoreMarker func(string, bool, map[string]registryValueSnapshot) error
	broadcast     func()
}

type registryTransactionOperations struct {
	readState   func(string) (registryState, error)
	writePath   func(string, registryValueSnapshot) error
	deletePath  func(string) error
	writeMarker func(string, pathownership.Marker) error
}

type transactionFinalizeOperations struct {
	readState func(string) (registryState, error)
}

type registryState struct {
	PathPresent     bool                             `json:"path_present"`
	PathType        uint32                           `json:"path_type"`
	Path            string                           `json:"path"`
	PathData        []byte                           `json:"path_data,omitempty"`
	MarkerKeyExists bool                             `json:"marker_key_exists"`
	MarkerValues    map[string]registryValueSnapshot `json:"marker_values"`
}

type pathSnapshot struct {
	Schema     int           `json:"schema"`
	ClaimToken string        `json:"claim_token"`
	UserSID    string        `json:"user_sid"`
	Original   registryState `json:"original"`
	Expected   registryState `json:"expected"`
}

type transactionClaim struct {
	Schema int    `json:"schema"`
	Token  string `json:"token"`
	Phase  string `json:"phase"`
}

type transactionTeardownOperations struct {
	markCleanup    func(string, string) error
	removeSnapshot func(string) error
	removeClaim    func(string) error
}

type registryValueSnapshot struct {
	Present bool   `json:"present"`
	Type    uint32 `json:"type"`
	Data    []byte `json:"data,omitempty"`
}

type testFailureDiagnostic struct {
	Schema int    `json:"schema"`
	Action string `json:"action"`
	Error  string `json:"error"`
}

func main() {
	action := flag.String("action", "", "preflight, apply, uninstall, finalize, rollback, commit, or fail")
	programDir := flag.String("program-dir", "", "resolved program directory used by PATH policy")
	installDir := flag.String("install-dir", "", "actual MSI install directory")
	stateFile := flag.String("state-file", "", "transaction snapshot path")
	userSID := flag.String("user-sid", "", "SID of the per-user MSI owner")
	flag.Parse()
	if err := run(*action, *programDir, *installDir, *stateFile, *userSID); err != nil {
		if _, diagnosticErr := writeTestFailureDiagnostic(*stateFile, *action, err); diagnosticErr != nil {
			fmt.Fprintln(os.Stderr, "Inlaid PATH helper test diagnostic:", diagnosticErr)
		}
		fmt.Fprintln(os.Stderr, "Inlaid PATH helper:", err)
		os.Exit(1)
	}
}

func writeTestFailureDiagnostic(stateFile, action string, runErr error) (string, error) {
	if testHooks != "true" || runErr == nil {
		return "", nil
	}
	switch action {
	case "preflight", "apply", "uninstall", "finalize", "rollback", "commit", "fail":
	default:
		return "", nil
	}
	if strings.TrimSpace(stateFile) == "" || !filepath.IsAbs(stateFile) {
		return "", errors.New("test diagnostic state path must be absolute")
	}
	diagnosticPath := fmt.Sprintf("%s.%s.%d.test-error.json", stateFile, action, os.Getpid())
	data, err := json.Marshal(testFailureDiagnostic{Schema: 1, Action: action, Error: runErr.Error()})
	if err != nil {
		return "", fmt.Errorf("encode test failure diagnostic: %w", err)
	}
	file, err := os.OpenFile(diagnosticPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("exclusively create test failure diagnostic: %w", err)
	}
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = fmt.Errorf("short write: %d of %d bytes", written, len(data))
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return "", fmt.Errorf("write test failure diagnostic: %w", writeErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close test failure diagnostic: %w", closeErr)
	}
	return diagnosticPath, nil
}

func run(action, programDir, installDir, stateFile, userSID string) error {
	if strings.TrimSpace(stateFile) == "" || !filepath.IsAbs(stateFile) {
		return errors.New("transaction state path must be absolute")
	}
	switch action {
	case "preflight":
		return prepareTransactionClaim(stateFile)
	case "rollback":
		return restoreSnapshot(stateFile)
	case "commit":
		return removeTransactionFile(stateFile)
	case "finalize":
		return finalizeTransactionState(stateFile)
	case "fail":
		if testHooks != "true" {
			return nil
		}
		return errors.New("injected failure after PATH mutation")
	case "apply", "uninstall":
	default:
		return fmt.Errorf("unsupported action %q", action)
	}
	userSID, err := normalizeUserSID(userSID)
	if err != nil {
		return err
	}

	expandEnvironment, err := environmentExpanderForUserSID(userSID)
	if err != nil {
		return err
	}
	actual, err := pathownership.NormalizeProgramDirectory(installDir, expandEnvironment)
	if err != nil {
		return fmt.Errorf("validate MSI install directory: %w", err)
	}
	requested, err := pathownership.NormalizeProgramDirectory(programDir, expandEnvironment)
	if err != nil {
		return err
	}
	if testHooks != "true" {
		if err := validateProgramDirectoryMatch(actual, requested); err != nil {
			return err
		}
	}

	claimToken, err := requireTransactionClaimPhase(stateFile, claimPhasePreflight)
	if err != nil {
		return err
	}
	original, marker, err := captureRegistryState(userSID)
	if err != nil {
		return err
	}
	var plan pathownership.Plan
	if action == "apply" {
		plan, err = pathownership.PlanApply(original.Path, original.PathPresent, marker, programDir, expandEnvironment)
	} else {
		plan, err = pathownership.PlanUninstall(original.Path, original.PathPresent, marker, programDir, expandEnvironment)
	}
	if err != nil {
		return err
	}
	expected, err := expectedRegistryState(original, plan)
	if err != nil {
		return err
	}
	snapshot := pathSnapshot{
		Schema: pathSnapshotSchema, ClaimToken: claimToken,
		UserSID: userSID, Original: original, Expected: expected,
	}
	if err := writeSnapshot(stateFile, snapshot); err != nil {
		return err
	}
	if err := transitionTransactionClaimToActive(stateFile, claimToken); err != nil {
		return err
	}
	if err := executeRegistryTransaction(userSID, original, expected, plan, registryTransactionOperations{
		readState: readRegistryState, writePath: writeUserPath, deletePath: deleteUserPath, writeMarker: writeMarker,
	}); err != nil {
		return err
	}
	broadcastEnvironmentChange()
	if plan.Warn != "" {
		fmt.Fprintln(os.Stderr, "Inlaid PATH helper warning:", plan.Warn)
	}
	return nil
}

func validateProgramDirectoryMatch(actual, requested string) error {
	equal, err := pathownership.EqualOrdinalIgnoreCase(actual, requested)
	if err != nil {
		return fmt.Errorf("compare PATH program directory with the actual MSI install directory: %w", err)
	}
	if !equal {
		return errors.New("PATH program directory differs from the actual MSI install directory")
	}
	return nil
}

func ensureTransactionStateAbsent(path string) error {
	if err := ensureSnapshotStateAbsent(path); err != nil {
		return err
	}
	for _, candidate := range []string{transactionClaimPath(path), transactionClaimTransitionPath(path)} {
		if err := ensureTransactionPathAbsent(candidate); err != nil {
			return err
		}
	}
	return nil
}

func ensureSnapshotStateAbsent(path string) error {
	for _, candidate := range []string{path, path + ".partial"} {
		if err := ensureTransactionPathAbsent(candidate); err != nil {
			return err
		}
	}
	return nil
}

func ensureTransactionPathAbsent(candidate string) error {
	_, err := os.Lstat(candidate)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect transaction state %s: %w", candidate, err)
	}
	return fmt.Errorf("refusing to start with stale transaction state: %s", candidate)
}

func transactionClaimPath(path string) string {
	return path + ".claim"
}

func transactionClaimTransitionPath(path string) string {
	return transactionClaimPath(path) + ".partial"
}

func prepareTransactionClaim(path string) error {
	if err := ensureTransactionPartialAbsent(path); err != nil {
		return err
	}
	claim, err := readTransactionClaimState(path)
	if err == nil {
		switch claim.Phase {
		case claimPhasePreflight, claimPhaseCleanup:
			if err := validateSnapshotForClaimIfPresent(path, claim.Token); err != nil {
				return err
			}
			if err := completeTransactionTeardown(path, claim.Token, defaultTransactionTeardownOperations()); err != nil {
				return fmt.Errorf("recover prior PATH transaction teardown during preflight: %w", err)
			}
		case claimPhaseActive:
			return errors.New("refusing to start with stale active PATH transaction state")
		default:
			return errors.New("refusing to start with malformed PATH transaction phase")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensureTransactionStateAbsent(path); err != nil {
		return err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("generate PATH transaction claim: %w", err)
	}
	claim = transactionClaim{Schema: transactionClaimSchema, Token: hex.EncodeToString(tokenBytes), Phase: claimPhasePreflight}
	data, err := json.Marshal(claim)
	if err != nil {
		return fmt.Errorf("encode PATH transaction claim: %w", err)
	}
	claimPath := transactionClaimPath(path)
	file, err := os.OpenFile(claimPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("exclusively create PATH transaction claim: %w", err)
	}
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = fmt.Errorf("short write: %d of %d bytes", written, len(data))
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write PATH transaction claim: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close PATH transaction claim: %w", closeErr)
	}
	return nil
}

func readTransactionClaim(path string) (string, error) {
	claim, err := readTransactionClaimState(path)
	if err != nil {
		return "", err
	}
	if claim.Phase == claimPhaseCleanup {
		return "", errors.New("PATH transaction claim is already in cleanup")
	}
	return claim.Token, nil
}

func requireTransactionClaimPhase(path, phase string) (string, error) {
	claim, err := readTransactionClaimState(path)
	if err != nil {
		return "", err
	}
	if claim.Phase != phase {
		return "", fmt.Errorf("PATH transaction claim phase is %q, want %q", claim.Phase, phase)
	}
	return claim.Token, nil
}

func readTransactionClaimState(path string) (transactionClaim, error) {
	claimPath := transactionClaimPath(path)
	info, err := os.Lstat(claimPath)
	if err != nil {
		return transactionClaim{}, fmt.Errorf("inspect PATH transaction claim: %w", err)
	}
	if !info.Mode().IsRegular() {
		return transactionClaim{}, errors.New("PATH transaction claim is not a regular file")
	}
	data, err := os.ReadFile(claimPath)
	if err != nil {
		return transactionClaim{}, fmt.Errorf("read PATH transaction claim: %w", err)
	}
	return decodeTransactionClaim(data)
}

func decodeTransactionClaim(data []byte) (transactionClaim, error) {
	var claim transactionClaim
	if err := json.Unmarshal(data, &claim); err != nil || claim.Schema != transactionClaimSchema ||
		(claim.Phase != claimPhasePreflight && claim.Phase != claimPhaseActive && claim.Phase != claimPhaseCleanup) {
		return transactionClaim{}, errors.New("PATH transaction claim is malformed")
	}
	token, err := hex.DecodeString(claim.Token)
	if err != nil || len(token) != 32 {
		return transactionClaim{}, errors.New("PATH transaction claim token is malformed")
	}
	return claim, nil
}

func captureRegistryState(userSID string) (registryState, pathownership.Marker, error) {
	state, err := readRegistryState(userSID)
	if err != nil {
		return registryState{}, pathownership.Marker{}, err
	}
	return state, markerFromRegistryState(state), nil
}

func readRegistryState(userSID string) (registryState, error) {
	path, pathType, pathPresent, pathData, err := readUserPath(userSID)
	if err != nil {
		return registryState{}, err
	}
	markerKeyExists, markerValues, err := snapshotMarkerValues(userSID)
	if err != nil {
		return registryState{}, err
	}
	return registryState{
		PathPresent: pathPresent, PathType: pathType, Path: path, PathData: pathData,
		MarkerKeyExists: markerKeyExists, MarkerValues: markerValues,
	}, nil
}

func expectedRegistryState(original registryState, plan pathownership.Plan) (registryState, error) {
	expected := cloneRegistryState(original)
	expected.PathPresent = plan.PathPresent
	expected.Path = plan.Path
	if !plan.PathPresent {
		expected.Path = ""
		expected.PathType = registry.EXPAND_SZ
		expected.PathData = nil
	} else if !original.PathPresent {
		expected.PathType = registry.EXPAND_SZ
	}
	if plan.PathPresent && (!original.PathPresent || plan.Path != original.Path) {
		pathValue, err := registryStringSnapshot(plan.Path, expected.PathType)
		if err != nil {
			return registryState{}, fmt.Errorf("encode expected current-user PATH: %w", err)
		}
		expected.PathData = pathValue.Data
	}
	values, err := markerValuesForPlan(plan.Marker)
	if err != nil {
		return registryState{}, err
	}
	expected.MarkerValues = values
	// A key cannot be deleted conditionally on still being empty. Retain an
	// installer key once it has existed so a racing foreign value/subkey is
	// never removed by this helper.
	expected.MarkerKeyExists = plan.Marker.Present || original.MarkerKeyExists
	return expected, nil
}

func executeRegistryTransaction(userSID string, original, expected registryState, plan pathownership.Plan, operations registryTransactionOperations) error {
	current, err := operations.readState(userSID)
	if err != nil {
		return fmt.Errorf("recheck PATH transaction state before mutation: %w", err)
	}
	if !registryStatesEqual(current, original) {
		return errors.New("PATH or provenance changed before transaction mutation")
	}

	if !pathStatesEqual(original, expected) {
		if expected.PathPresent {
			if err := operations.writePath(userSID, pathRegistryValue(expected)); err != nil {
				return err
			}
		} else if err := operations.deletePath(userSID); err != nil {
			return err
		}
	}
	afterPath, err := operations.readState(userSID)
	if err != nil {
		return fmt.Errorf("recheck PATH transaction state before provenance mutation: %w", err)
	}
	wantAfterPath := cloneRegistryState(original)
	wantAfterPath.PathPresent = expected.PathPresent
	wantAfterPath.PathType = expected.PathType
	wantAfterPath.Path = expected.Path
	wantAfterPath.PathData = bytes.Clone(expected.PathData)
	if !registryStatesEqual(afterPath, wantAfterPath) {
		return errors.New("PATH or provenance changed during transaction mutation")
	}

	if !markerStatesEqual(original, expected) {
		if err := operations.writeMarker(userSID, plan.Marker); err != nil {
			return err
		}
	}
	mutationExpected := expected
	if !plan.Marker.Present && original.MarkerKeyExists {
		mutationExpected.MarkerKeyExists = true
	}
	final, err := operations.readState(userSID)
	if err != nil {
		return fmt.Errorf("verify PATH transaction state after mutation: %w", err)
	}
	if !registryStatesEqual(final, mutationExpected) {
		return errors.New("PATH or provenance changed before transaction verification")
	}
	return nil
}

func cloneRegistryState(state registryState) registryState {
	clone := state
	clone.PathData = bytes.Clone(state.PathData)
	clone.MarkerValues = make(map[string]registryValueSnapshot, len(markerNames))
	for _, name := range markerNames {
		value := state.MarkerValues[name]
		value.Data = bytes.Clone(value.Data)
		clone.MarkerValues[name] = value
	}
	return clone
}

func registryStatesEqual(left, right registryState) bool {
	return pathStatesEqual(left, right) && markerStatesEqual(left, right)
}

func pathStatesEqual(left, right registryState) bool {
	return left.PathPresent == right.PathPresent && left.PathType == right.PathType && left.Path == right.Path &&
		bytes.Equal(left.PathData, right.PathData)
}

func pathRegistryValue(state registryState) registryValueSnapshot {
	return registryValueSnapshot{Present: state.PathPresent, Type: state.PathType, Data: bytes.Clone(state.PathData)}
}

func markerStatesEqual(left, right registryState) bool {
	if left.MarkerKeyExists != right.MarkerKeyExists {
		return false
	}
	return markerValuesEqual(left, right)
}

func markerValuesEqual(left, right registryState) bool {
	for _, name := range markerNames {
		if !registryValuesEqual(left.MarkerValues[name], right.MarkerValues[name]) {
			return false
		}
	}
	return true
}

func registryValuesEqual(left, right registryValueSnapshot) bool {
	return left.Present == right.Present && left.Type == right.Type && bytes.Equal(left.Data, right.Data)
}

func removeTransactionFile(path string) error {
	if err := ensureTransactionPartialAbsent(path); err != nil {
		return err
	}
	claim, err := readTransactionClaimState(path)
	if err != nil {
		return err
	}
	if claim.Phase == claimPhaseCleanup {
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			return completeTransactionTeardown(path, claim.Token, defaultTransactionTeardownOperations())
		} else if statErr != nil {
			return fmt.Errorf("inspect committed transaction snapshot during cleanup: %w", statErr)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read committed transaction snapshot: %w", err)
	}
	var snapshot pathSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Schema != pathSnapshotSchema ||
		snapshot.ClaimToken != claim.Token || !validSnapshotState(snapshot.Original) || !validSnapshotState(snapshot.Expected) {
		return errors.New("committed transaction snapshot does not match this transaction claim")
	}
	return completeTransactionTeardown(path, claim.Token, defaultTransactionTeardownOperations())
}

func defaultTransactionTeardownOperations() transactionTeardownOperations {
	return transactionTeardownOperations{
		markCleanup: transitionTransactionClaimToCleanup,
		removeSnapshot: func(path string) error {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove transaction state %s: %w", path, err)
			}
			return nil
		},
		removeClaim: removeTransactionClaim,
	}
}

func completeTransactionTeardown(path, token string, operations transactionTeardownOperations) error {
	claim, err := readTransactionClaimState(path)
	if err != nil {
		return err
	}
	if claim.Token != token {
		return errors.New("transaction cleanup claim token changed")
	}
	if claim.Phase == claimPhasePreflight || claim.Phase == claimPhaseActive {
		if err := operations.markCleanup(path, token); err != nil {
			return err
		}
	} else if claim.Phase != claimPhaseCleanup {
		return errors.New("transaction cleanup claim phase is malformed")
	}
	if err := operations.removeSnapshot(path); err != nil {
		return err
	}
	return operations.removeClaim(path)
}

func transitionTransactionClaimToCleanup(path, token string) error {
	return transitionTransactionClaimPhase(path, token, []string{claimPhasePreflight, claimPhaseActive}, claimPhaseCleanup)
}

func transitionTransactionClaimToActive(path, token string) error {
	return transitionTransactionClaimPhase(path, token, []string{claimPhasePreflight}, claimPhaseActive)
}

func transitionTransactionClaimPhase(path, token string, fromPhases []string, toPhase string) error {
	claim, err := readTransactionClaimState(path)
	if err != nil {
		return err
	}
	if claim.Token != token {
		return errors.New("refusing to transition a different PATH transaction claim")
	}
	if claim.Phase == toPhase {
		return nil
	}
	allowed := false
	for _, phase := range fromPhases {
		if claim.Phase == phase {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("refusing PATH transaction claim transition from %q to %q", claim.Phase, toPhase)
	}
	claim.Phase = toPhase
	data, err := json.Marshal(claim)
	if err != nil {
		return fmt.Errorf("encode PATH transaction claim transition: %w", err)
	}
	temporary := transactionClaimTransitionPath(path)
	reuseTransition := false
	if info, statErr := os.Lstat(temporary); statErr == nil {
		if !info.Mode().IsRegular() {
			return errors.New("PATH transaction claim transition residue is not a regular file")
		}
		residueData, err := os.ReadFile(temporary)
		if err != nil {
			return fmt.Errorf("read PATH transaction claim transition residue: %w", err)
		}
		residue, err := decodeTransactionClaim(residueData)
		if err != nil || residue.Token != token {
			return errors.New("PATH transaction claim transition residue is not authenticated to this transaction")
		}
		if residue.Phase == toPhase {
			reuseTransition = true
		} else if err := os.Remove(temporary); err != nil {
			return fmt.Errorf("remove superseded authenticated PATH transaction claim transition: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect PATH transaction claim transition residue: %w", statErr)
	}
	if !reuseTransition {
		file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("exclusively create PATH transaction claim transition: %w", err)
		}
		written, writeErr := file.Write(data)
		if writeErr == nil && written != len(data) {
			writeErr = fmt.Errorf("short write: %d of %d bytes", written, len(data))
		}
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("write PATH transaction claim transition: %w", writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close PATH transaction claim transition: %w", closeErr)
		}
	}
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return fmt.Errorf("encode PATH transaction claim transition path: %w", err)
	}
	to, err := windows.UTF16PtrFromString(transactionClaimPath(path))
	if err != nil {
		return fmt.Errorf("encode PATH transaction claim destination path: %w", err)
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("publish PATH transaction claim transition: %w", err)
	}
	return nil
}

func validateSnapshotForClaimIfPresent(path, token string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read recoverable PATH transaction snapshot: %w", err)
	}
	var snapshot pathSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Schema != pathSnapshotSchema ||
		snapshot.ClaimToken != token || !validSnapshotState(snapshot.Original) || !validSnapshotState(snapshot.Expected) {
		return errors.New("recoverable PATH transaction snapshot does not match its claim")
	}
	return nil
}

func finalizeTransactionState(path string) error {
	return finalizeTransactionStateWithOperations(path, transactionFinalizeOperations{
		readState: readRegistryState,
	})
}

func finalizeTransactionStateWithOperations(path string, operations transactionFinalizeOperations) error {
	if err := ensureTransactionPartialAbsent(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read transaction snapshot for provenance finalization: %w", err)
	}
	var snapshot pathSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Schema != pathSnapshotSchema ||
		!validSnapshotState(snapshot.Original) || !validSnapshotState(snapshot.Expected) {
		return errors.New("transaction snapshot for provenance finalization is malformed")
	}
	claimToken, err := requireTransactionClaimPhase(path, claimPhaseActive)
	if err != nil {
		return err
	}
	if snapshot.ClaimToken == "" || snapshot.ClaimToken != claimToken {
		return errors.New("transaction snapshot for provenance finalization does not match this transaction claim")
	}
	if markerHasPresentValues(snapshot.Expected) {
		return errors.New("refusing provenance-key finalization for a transaction that retains provenance values")
	}
	userSID, err := normalizeUserSID(snapshot.UserSID)
	if err != nil {
		return errors.New("transaction snapshot owner SID is malformed")
	}
	current, err := operations.readState(userSID)
	if err != nil {
		return fmt.Errorf("read current state before provenance-key finalization: %w", err)
	}
	if !pathStatesEqual(current, snapshot.Expected) || !markerValuesEqual(current, snapshot.Expected) {
		return errors.New("refusing to finalize provenance changed outside this transaction")
	}
	// Preserve the now-empty installer key. RegDeleteKey cannot atomically
	// assert that no foreign value or subkey appeared after enumeration.
	return nil
}

func markerHasPresentValues(state registryState) bool {
	for _, name := range markerNames {
		if state.MarkerValues[name].Present {
			return true
		}
	}
	return false
}

func removeTransactionClaim(path string) error {
	transition := transactionClaimTransitionPath(path)
	if _, err := os.Lstat(transition); err == nil {
		return errors.New("refusing to delete PATH transaction claim while transition residue exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect PATH transaction claim transition residue %s: %w", transition, err)
	}
	claimPath := transactionClaimPath(path)
	if err := os.Remove(claimPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove transaction claim %s: %w", claimPath, err)
	}
	return nil
}

func ensureTransactionPartialAbsent(path string) error {
	partial := path + ".partial"
	_, err := os.Lstat(partial)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect transaction partial state %s: %w", partial, err)
	}
	return fmt.Errorf("refusing to consume an unpublished transaction snapshot while %s exists", partial)
}

func normalizeUserSID(value string) (string, error) {
	value = strings.TrimSpace(value)
	sid, err := windows.StringToSid(value)
	if err != nil || sid == nil {
		return "", errors.New("per-user MSI owner SID is invalid")
	}
	return sid.String(), nil
}

func userRegistryPath(userSID, suffix string) string {
	return userSID + `\` + suffix
}

type registryRawValueKey interface {
	GetValue(string, []byte) (int, uint32, error)
	Close() error
}

func readUserPath(userSID string) (value string, valueType uint32, present bool, data []byte, err error) {
	return readUserPathWithOpen(userSID, func(path string) (registryRawValueKey, error) {
		return registry.OpenKey(registry.USERS, path, registry.QUERY_VALUE)
	})
}

func readUserPathWithOpen(userSID string, open func(string) (registryRawValueKey, error)) (value string, valueType uint32, present bool, data []byte, err error) {
	key, err := open(userRegistryPath(userSID, `Environment`))
	if err != nil {
		return "", 0, false, nil, fmt.Errorf("open current-user environment: %w", err)
	}
	defer closeRawRegistryKey(&err, key, "close current-user environment after PATH read")
	valueSnapshot, err := snapshotRawRegistryValue(key, "Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", registry.EXPAND_SZ, false, nil, nil
	}
	if err != nil {
		return "", 0, false, nil, fmt.Errorf("read current-user PATH: %w", err)
	}
	value, ok := decodeRegistryString(valueSnapshot)
	if !ok {
		return "", 0, false, nil, fmt.Errorf("current-user PATH has malformed REG_SZ/REG_EXPAND_SZ data (type=%d bytes=%d)", valueSnapshot.Type, len(valueSnapshot.Data))
	}
	return value, valueSnapshot.Type, true, valueSnapshot.Data, nil
}

func writeUserPath(userSID string, value registryValueSnapshot) (err error) {
	if _, ok := decodeRegistryString(value); !value.Present || !ok {
		return errors.New("refusing to write malformed current-user PATH data")
	}
	key, err := registry.OpenKey(registry.USERS, userRegistryPath(userSID, `Environment`), registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open current-user environment for update: %w", err)
	}
	defer closeRegistryKey(&err, key, "close current-user environment after PATH update")
	if err = setRawRegistryValue(key, "Path", value.Type, value.Data); err != nil {
		return fmt.Errorf("write current-user PATH: %w", err)
	}
	return err
}

func snapshotRawRegistryValue(key registryRawValueKey, name string) (registryValueSnapshot, error) {
	const maxRegistryValueBytes = 1 << 20
	for attempt := 0; attempt < 4; attempt++ {
		size, valueType, err := key.GetValue(name, nil)
		if err != nil && !errors.Is(err, registry.ErrShortBuffer) {
			return registryValueSnapshot{}, err
		}
		if size < 0 || size > maxRegistryValueBytes {
			return registryValueSnapshot{}, fmt.Errorf("registry value size %d exceeds the bounded limit", size)
		}
		if size == 0 {
			return registryValueSnapshot{Present: true, Type: valueType}, nil
		}
		data := make([]byte, size)
		read, readType, readErr := key.GetValue(name, data)
		if errors.Is(readErr, registry.ErrShortBuffer) {
			continue
		}
		if readErr != nil {
			return registryValueSnapshot{}, readErr
		}
		if read < 0 || read > len(data) {
			return registryValueSnapshot{}, errors.New("registry value read returned an invalid byte count")
		}
		return registryValueSnapshot{Present: true, Type: readType, Data: data[:read]}, nil
	}
	return registryValueSnapshot{}, errors.New("registry value changed repeatedly while being snapshotted")
}

func closeRawRegistryKey(result *error, key registryRawValueKey, context string) {
	if err := key.Close(); err != nil {
		*result = errors.Join(*result, fmt.Errorf("%s: %w", context, err))
	}
}

func deleteUserPath(userSID string) error {
	return deleteUserPathWithOpen(userSID, func(path string) (registryValueKey, error) {
		return registry.OpenKey(registry.USERS, path, registry.SET_VALUE)
	})
}

func deleteUserPathWithOpen(userSID string, open func(string) (registryValueKey, error)) error {
	key, err := open(userRegistryPath(userSID, `Environment`))
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open current-user environment for PATH removal: %w", err)
	}
	deleteErr := key.DeleteValue("Path")
	if errors.Is(deleteErr, registry.ErrNotExist) {
		deleteErr = nil
	} else if deleteErr != nil {
		deleteErr = fmt.Errorf("restore absent current-user PATH: %w", deleteErr)
	}
	closeErr := key.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("close current-user environment after PATH removal: %w", closeErr)
	}
	return errors.Join(deleteErr, closeErr)
}

func snapshotMarkerValues(userSID string) (keyExists bool, values map[string]registryValueSnapshot, err error) {
	return snapshotMarkerValuesWithOpen(userSID, func(path string) (registryRawValueKey, error) {
		return registry.OpenKey(registry.USERS, path, registry.QUERY_VALUE)
	})
}

func snapshotMarkerValuesWithOpen(userSID string, open func(string) (registryRawValueKey, error)) (keyExists bool, values map[string]registryValueSnapshot, err error) {
	values = absentMarkerValues()
	key, err := open(userRegistryPath(userSID, markerKey))
	if errors.Is(err, registry.ErrNotExist) {
		return false, values, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("open PATH provenance marker for snapshot: %w", err)
	}
	defer closeRawRegistryKey(&err, key, "close PATH provenance marker after snapshot")
	for _, name := range markerNames {
		size, valueType, valueErr := key.GetValue(name, nil)
		if errors.Is(valueErr, registry.ErrNotExist) {
			values[name] = registryValueSnapshot{}
			continue
		}
		if valueErr != nil && !errors.Is(valueErr, registry.ErrShortBuffer) {
			return false, nil, fmt.Errorf("size PATH provenance value %s: %w", name, valueErr)
		}
		data := make([]byte, size)
		if size > 0 {
			read, readType, readErr := key.GetValue(name, data)
			if readErr != nil {
				return false, nil, fmt.Errorf("snapshot PATH provenance value %s: %w", name, readErr)
			}
			data, valueType = data[:read], readType
		}
		values[name] = registryValueSnapshot{Present: true, Type: valueType, Data: data}
	}
	return true, values, err
}

func absentMarkerValues() map[string]registryValueSnapshot {
	values := make(map[string]registryValueSnapshot, len(markerNames))
	for _, name := range markerNames {
		values[name] = registryValueSnapshot{}
	}
	return values
}

func markerFromRegistryState(state registryState) pathownership.Marker {
	if !state.MarkerKeyExists {
		return pathownership.Marker{}
	}
	present := 0
	for _, name := range markerNames {
		if state.MarkerValues[name].Present {
			present++
		}
	}
	if present == 0 {
		return pathownership.Marker{}
	}
	marker := pathownership.Marker{Present: true}
	schema, schemaOK := decodeRegistryDWORD(state.MarkerValues["Schema"])
	component, componentOK := decodeRegistryDWORD(state.MarkerValues["Component"])
	normalized, normalizedOK := decodeRegistryString(state.MarkerValues["NormalizedProgramDirectory"])
	inserted, insertedOK := decodeRegistryString(state.MarkerValues["InsertedSegment"])
	owned, ownedOK := decodeRegistryDWORD(state.MarkerValues["Owned"])
	pathExisted, pathExistedOK := decodeRegistryDWORD(state.MarkerValues["PathValueExistedBeforeOwnership"])
	marker.Valid = schemaOK && schema == pathownership.MarkerSchema && componentOK && component == 1 &&
		normalizedOK && normalized != "" && insertedOK && ownedOK && owned <= 1 &&
		pathExistedOK && pathExisted <= 1 &&
		((owned == 1 && inserted != "") || (owned == 0 && inserted == "" && pathExisted == 0))
	if marker.Valid {
		marker.NormalizedProgramDirectory = normalized
		marker.InsertedSegment = inserted
		marker.Owned = owned == 1
		marker.PathValueExistedBeforeOwnership = pathExisted == 1
	}
	return marker
}

func markerValuesForPlan(marker pathownership.Marker) (map[string]registryValueSnapshot, error) {
	values := absentMarkerValues()
	if !marker.Present {
		return values, nil
	}
	if !marker.Valid || marker.NormalizedProgramDirectory == "" ||
		(marker.Owned && marker.InsertedSegment == "") ||
		(!marker.Owned && (marker.InsertedSegment != "" || marker.PathValueExistedBeforeOwnership)) {
		return nil, errors.New("refusing to encode malformed PATH provenance marker")
	}
	var err error
	values["Schema"] = registryDWORDSnapshot(pathownership.MarkerSchema)
	values["Component"] = registryDWORDSnapshot(1)
	values["NormalizedProgramDirectory"], err = registryStringSnapshot(marker.NormalizedProgramDirectory, registry.SZ)
	if err != nil {
		return nil, fmt.Errorf("encode normalized PATH provenance: %w", err)
	}
	values["InsertedSegment"], err = registryStringSnapshot(marker.InsertedSegment, registry.SZ)
	if err != nil {
		return nil, fmt.Errorf("encode inserted PATH provenance: %w", err)
	}
	owned := uint32(0)
	if marker.Owned {
		owned = 1
	}
	values["Owned"] = registryDWORDSnapshot(owned)
	pathExisted := uint32(0)
	if marker.PathValueExistedBeforeOwnership {
		pathExisted = 1
	}
	values["PathValueExistedBeforeOwnership"] = registryDWORDSnapshot(pathExisted)
	return values, nil
}

func registryDWORDSnapshot(value uint32) registryValueSnapshot {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, value)
	return registryValueSnapshot{Present: true, Type: registry.DWORD, Data: data}
}

func registryStringSnapshot(value string, valueType uint32) (registryValueSnapshot, error) {
	if valueType != registry.SZ && valueType != registry.EXPAND_SZ {
		return registryValueSnapshot{}, fmt.Errorf("unsupported registry string type %d", valueType)
	}
	units, err := windows.UTF16FromString(value)
	if err != nil {
		return registryValueSnapshot{}, err
	}
	data := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(data[index*2:], unit)
	}
	return registryValueSnapshot{Present: true, Type: valueType, Data: data}, nil
}

func decodeRegistryDWORD(value registryValueSnapshot) (uint32, bool) {
	if !value.Present || value.Type != registry.DWORD || len(value.Data) != 4 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(value.Data), true
}

func decodeRegistryString(value registryValueSnapshot) (string, bool) {
	if !value.Present || (value.Type != registry.SZ && value.Type != registry.EXPAND_SZ) ||
		len(value.Data)%2 != 0 {
		return "", false
	}
	units := make([]uint16, len(value.Data)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(value.Data[index*2:])
	}
	contentEnd := len(units)
	for index, unit := range units {
		if unit == 0 {
			contentEnd = index
			for _, trailing := range units[index:] {
				if trailing != 0 {
					return "", false
				}
			}
			break
		}
	}
	content := units[:contentEnd]
	for index := 0; index < len(content); index++ {
		unit := content[index]
		switch {
		case unit >= 0xd800 && unit <= 0xdbff:
			if index+1 >= len(content) || content[index+1] < 0xdc00 || content[index+1] > 0xdfff {
				return "", false
			}
			index++
		case unit >= 0xdc00 && unit <= 0xdfff:
			return "", false
		}
	}
	return string(utf16.Decode(content)), true
}

func restoreMarkerValues(userSID string, keyExisted bool, values map[string]registryValueSnapshot) (err error) {
	if !keyExisted {
		if err := writeMarker(userSID, pathownership.Marker{}); err != nil {
			return err
		}
		// Preserve the empty key: deleting after checking emptiness would race
		// with foreign values or subkeys added between those operations.
		return nil
	}
	key, _, err := registry.CreateKey(registry.USERS, userRegistryPath(userSID, markerKey), registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("recreate PATH provenance marker: %w", err)
	}
	defer closeRegistryKey(&err, key, "close PATH provenance marker after restore")
	for _, name := range markerNames {
		value := values[name]
		if !value.Present {
			if err := key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
				return fmt.Errorf("restore absent PATH provenance value %s: %w", name, err)
			}
			continue
		}
		if err := setRawRegistryValue(key, name, value.Type, value.Data); err != nil {
			return fmt.Errorf("restore PATH provenance value %s: %w", name, err)
		}
	}
	return err
}

func setRawRegistryValue(key registry.Key, name string, valueType uint32, data []byte) error {
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var dataPointer uintptr
	if len(data) > 0 {
		dataPointer = uintptr(unsafe.Pointer(&data[0]))
	}
	setValue := windows.NewLazySystemDLL("advapi32.dll").NewProc("RegSetValueExW")
	result, _, _ := setValue.Call(
		uintptr(key), uintptr(unsafe.Pointer(namePointer)), 0, uintptr(valueType),
		dataPointer, uintptr(len(data)),
	)
	if result != 0 {
		return fmt.Errorf("RegSetValueExW returned %d", result)
	}
	return nil
}

func writeMarker(userSID string, marker pathownership.Marker) (err error) {
	if !marker.Present {
		key, err := registry.OpenKey(registry.USERS, userRegistryPath(userSID, markerKey), registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("open PATH provenance marker for removal: %w", err)
		}
		defer closeRegistryKey(&err, key, "close PATH provenance marker after removal")
		for _, name := range markerNames {
			if err := key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
				return fmt.Errorf("remove PATH provenance value %s: %w", name, err)
			}
		}
		return err
	}
	if !marker.Valid {
		return errors.New("refusing to write malformed PATH provenance marker")
	}
	key, _, err := registry.CreateKey(registry.USERS, userRegistryPath(userSID, markerKey), registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("create PATH provenance marker: %w", err)
	}
	defer closeRegistryKey(&err, key, "close PATH provenance marker after update")
	if err := key.SetDWordValue("Schema", pathownership.MarkerSchema); err != nil {
		return err
	}
	if err := key.SetDWordValue("Component", 1); err != nil {
		return err
	}
	if err := key.SetStringValue("NormalizedProgramDirectory", marker.NormalizedProgramDirectory); err != nil {
		return err
	}
	if err := key.SetStringValue("InsertedSegment", marker.InsertedSegment); err != nil {
		return err
	}
	owned := uint32(0)
	if marker.Owned {
		owned = 1
	}
	if err := key.SetDWordValue("Owned", owned); err != nil {
		return err
	}
	pathExisted := uint32(0)
	if marker.PathValueExistedBeforeOwnership {
		pathExisted = 1
	}
	if err := key.SetDWordValue("PathValueExistedBeforeOwnership", pathExisted); err != nil {
		return err
	}
	return err
}

func closeRegistryKey(result *error, key registry.Key, context string) {
	if err := key.Close(); err != nil {
		*result = errors.Join(*result, fmt.Errorf("%s: %w", context, err))
	}
}

func writeSnapshot(path string, snapshot pathSnapshot) error {
	claimToken, err := requireTransactionClaimPhase(path, claimPhasePreflight)
	if err != nil {
		return err
	}
	if snapshot.ClaimToken == "" || snapshot.ClaimToken != claimToken {
		return errors.New("refusing to publish a transaction snapshot under a different claim")
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode transaction snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create transaction snapshot directory: %w", err)
	}
	if err := ensureSnapshotStateAbsent(path); err != nil {
		return err
	}
	temporary := path + ".partial"
	name, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return fmt.Errorf("encode transaction partial path: %w", err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_WRITE,
		0,
		nil,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH,
		0,
	)
	if err != nil {
		return fmt.Errorf("exclusively create transaction partial snapshot: %w", err)
	}
	file := os.NewFile(uintptr(handle), temporary)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return errors.New("open transaction partial snapshot handle")
	}
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = fmt.Errorf("short write: %d of %d bytes", written, len(data))
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write transaction partial snapshot: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close transaction partial snapshot: %w", closeErr)
	}
	return publishSnapshotNoReplace(temporary, path)
}

func publishSnapshotNoReplace(temporary, path string) error {
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return fmt.Errorf("encode transaction partial path for publication: %w", err)
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode transaction snapshot path for publication: %w", err)
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("publish transaction snapshot without replacement: %w", err)
	}
	return nil
}

func restoreSnapshot(path string) error {
	return restoreSnapshotWithOperations(path, snapshotRestoreOperations{
		readState:     readRegistryState,
		writePath:     writeUserPath,
		deletePath:    deleteUserPath,
		restoreMarker: restoreMarkerValues,
		broadcast:     broadcastEnvironmentChange,
	})
}

func restoreSnapshotWithOperations(path string, operations snapshotRestoreOperations) error {
	if err := ensureTransactionPartialAbsent(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		claim, claimErr := readTransactionClaimState(path)
		if errors.Is(claimErr, os.ErrNotExist) {
			return nil
		}
		if claimErr != nil {
			return claimErr
		}
		if claim.Phase == claimPhaseActive {
			return errors.New("active PATH transaction claim exists without its authenticated rollback snapshot")
		}
		if claim.Phase != claimPhasePreflight && claim.Phase != claimPhaseCleanup {
			return errors.New("PATH transaction claim phase is malformed during rollback")
		}
		return completeTransactionTeardown(path, claim.Token, defaultTransactionTeardownOperations())
	}
	if err != nil {
		return fmt.Errorf("read transaction snapshot: %w", err)
	}
	var snapshot pathSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Schema != pathSnapshotSchema ||
		!validSnapshotState(snapshot.Original) || !validSnapshotState(snapshot.Expected) {
		return errors.New("transaction snapshot is malformed")
	}
	claim, err := readTransactionClaimState(path)
	if err != nil {
		return err
	}
	if snapshot.ClaimToken == "" || snapshot.ClaimToken != claim.Token {
		return errors.New("transaction snapshot was not published under this transaction claim")
	}
	if claim.Phase == claimPhaseCleanup {
		return completeTransactionTeardown(path, claim.Token, defaultTransactionTeardownOperations())
	}
	userSID, err := normalizeUserSID(snapshot.UserSID)
	if err != nil {
		return errors.New("transaction snapshot owner SID is malformed")
	}
	current, err := operations.readState(userSID)
	if err != nil {
		return fmt.Errorf("read current state before PATH rollback: %w", err)
	}
	if !registryStateWithinTransaction(current, snapshot.Original, snapshot.Expected) {
		return errors.New("refusing to overwrite PATH or provenance changed outside this transaction")
	}
	mutated := !registryStatesRestored(current, snapshot.Original)
	if !pathStatesEqual(current, snapshot.Original) {
		if snapshot.Original.PathPresent {
			if err := operations.writePath(userSID, pathRegistryValue(snapshot.Original)); err != nil {
				return err
			}
		} else if err := operations.deletePath(userSID); err != nil {
			return err
		}
	}
	afterPath, err := operations.readState(userSID)
	if err != nil {
		return fmt.Errorf("read current state during PATH rollback: %w", err)
	}
	if !pathStatesEqual(afterPath, snapshot.Original) ||
		!markerStateWithinTransaction(afterPath, snapshot.Original, snapshot.Expected) {
		return errors.New("refusing to overwrite provenance changed during PATH rollback")
	}
	if !markerStatesRestored(afterPath, snapshot.Original) {
		if err := operations.restoreMarker(userSID, snapshot.Original.MarkerKeyExists, snapshot.Original.MarkerValues); err != nil {
			return err
		}
	}
	final, err := operations.readState(userSID)
	if err != nil {
		return fmt.Errorf("verify current state after PATH rollback: %w", err)
	}
	if !registryStatesRestored(final, snapshot.Original) {
		return errors.New("PATH rollback did not restore the original state exactly")
	}
	if mutated {
		operations.broadcast()
	}
	return completeTransactionTeardown(path, claim.Token, defaultTransactionTeardownOperations())
}

func validSnapshotState(state registryState) bool {
	if state.PathType != registry.SZ && state.PathType != registry.EXPAND_SZ {
		return false
	}
	if state.PathPresent {
		decoded, ok := decodeRegistryString(pathRegistryValue(state))
		if !ok || decoded != state.Path {
			return false
		}
	} else if state.Path != "" || len(state.PathData) != 0 {
		return false
	}
	if len(state.MarkerValues) != len(markerNames) {
		return false
	}
	for _, name := range markerNames {
		if _, ok := state.MarkerValues[name]; !ok {
			return false
		}
	}
	return true
}

func registryStateWithinTransaction(current, original, expected registryState) bool {
	if !pathStatesEqual(current, original) && !pathStatesEqual(current, expected) {
		return false
	}
	return markerStateWithinTransaction(current, original, expected)
}

func markerStateWithinTransaction(current, original, expected registryState) bool {
	if current.MarkerKeyExists != original.MarkerKeyExists && current.MarkerKeyExists != expected.MarkerKeyExists {
		return false
	}
	for _, name := range markerNames {
		value := current.MarkerValues[name]
		if !registryValuesEqual(value, original.MarkerValues[name]) &&
			!registryValuesEqual(value, expected.MarkerValues[name]) {
			return false
		}
	}
	return true
}

func registryStatesRestored(current, original registryState) bool {
	return pathStatesEqual(current, original) && markerStatesRestored(current, original)
}

func markerStatesRestored(current, original registryState) bool {
	for _, name := range markerNames {
		if !registryValuesEqual(current.MarkerValues[name], original.MarkerValues[name]) {
			return false
		}
	}
	if original.MarkerKeyExists {
		return current.MarkerKeyExists
	}
	// Removing the last known value cannot safely be followed by a key
	// deletion: a foreign value/subkey may race into the key. An empty key is
	// therefore an accepted rollback residue for an originally absent key.
	return !current.MarkerKeyExists || !markerHasPresentValues(current)
}

func environmentExpanderForUserSID(userSID string) (pathownership.ExpandFunc, error) {
	return environmentExpanderForUserSIDWithSource(userSID, currentTokenUserEnvironment)
}

func currentTokenUserEnvironment() (string, []string, error) {
	token := windows.GetCurrentProcessToken()
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return "", nil, fmt.Errorf("read current process token owner for PATH expansion: %w", err)
	}
	if tokenUser.User.Sid == nil {
		return "", nil, errors.New("current process token has no user SID")
	}
	environment, err := token.Environ(false)
	if err != nil {
		return "", nil, fmt.Errorf("create target-user environment for PATH expansion: %w", err)
	}
	return tokenUser.User.Sid.String(), environment, nil
}

func environmentExpanderForUserSIDWithSource(userSID string, source func() (string, []string, error)) (pathownership.ExpandFunc, error) {
	tokenSID, environment, err := source()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(tokenSID, userSID) {
		return nil, errors.New("current process token does not belong to the supplied per-user MSI owner SID")
	}
	return environmentExpander(environment), nil
}

func environmentExpander(entries []string) pathownership.ExpandFunc {
	environment := map[string]string{}
	for _, entry := range entries {
		name, content, found := strings.Cut(entry, "=")
		if found && name != "" {
			environment[strings.ToUpper(name)] = content
		}
	}
	return func(value string) string {
		var result strings.Builder
		for position := 0; position < len(value); {
			start := strings.IndexByte(value[position:], '%')
			if start < 0 {
				result.WriteString(value[position:])
				break
			}
			start += position
			result.WriteString(value[position:start])
			endOffset := strings.IndexByte(value[start+1:], '%')
			if endOffset < 0 {
				result.WriteString(value[start:])
				break
			}
			end := start + 1 + endOffset
			name := value[start+1 : end]
			if replacement, found := environment[strings.ToUpper(name)]; found && name != "" {
				result.WriteString(replacement)
			} else {
				result.WriteString(value[start : end+1])
			}
			position = end + 1
		}
		return result.String()
	}
}

func broadcastEnvironmentChange() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	send := user32.NewProc("SendMessageTimeoutW")
	environment, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001a
		smtoAbortIfHung = 0x0002
	)
	var result uintptr
	_, _, _ = send.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(environment)), smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
}
