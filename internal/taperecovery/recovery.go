package taperecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Melty1000/inlaid/internal/celltape"
)

const stagingSuffix = ".celltape.tmp"

// Engine owns one direct-child recovery directory and its safety limits.
type Engine struct {
	directory string
	options   Options
}

// New validates a recovery directory when it exists. A missing directory is a
// valid empty recovery set; this package never creates or removes entries.
func New(directory string, options Options) (*Engine, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("taperecovery: recovery directory is required")
	}
	options, err := options.normalized()
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Engine{directory: abs, options: options}, nil
		}
		return nil, fmt.Errorf("taperecovery: inspect recovery directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("taperecovery: recovery path is not a directory")
	}
	return &Engine{directory: abs, options: options}, nil
}

// Scan returns direct regular published tapes and closed crash-left staging
// tapes. On Unix it first finishes any interrupted retirement. A live Inlaid
// writer is omitted through the platform claim probe; no age heuristic is used.
func (e *Engine) Scan() ([]Candidate, error) {
	if e == nil {
		return nil, errors.New("taperecovery: nil engine")
	}
	directory, err := os.Open(e.directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Candidate{}, nil
		}
		return nil, err
	}
	entries, readErr := directory.ReadDir(e.options.MaxDirectoryEntries + 1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(entries) > e.options.MaxDirectoryEntries {
		return nil, fmt.Errorf("%w: recovery directory has more than %d entries", ErrLimit, e.options.MaxDirectoryEntries)
	}

	candidates := make([]Candidate, 0, min(len(entries), e.options.MaxCandidates))
	for _, entry := range entries {
		path := filepath.Join(e.directory, entry.Name())
		retirement, err := e.reconcileRetirement(path, entry.Name())
		if err != nil {
			if errors.Is(err, ErrBusy) {
				continue
			}
			return nil, fmt.Errorf("taperecovery: reconcile %s: %w", entry.Name(), err)
		}
		if retirement {
			continue
		}
		kind, ok := classifyName(entry.Name())
		if !ok {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("taperecovery: inspect %s: %w", filepath.Base(path), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if kind == Staging {
			busy, err := exclusiveProbe(path, true)
			if err != nil {
				return nil, fmt.Errorf("taperecovery: probe %s: %w", entry.Name(), err)
			}
			if busy {
				continue
			}
		}
		if len(candidates) == e.options.MaxCandidates {
			return nil, fmt.Errorf("%w: more than %d recovery candidates", ErrLimit, e.options.MaxCandidates)
		}
		candidates = append(candidates, Candidate{
			Path:       path,
			Kind:       kind,
			Size:       info.Size(),
			ModifiedAt: info.ModTime(),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].ModifiedAt.Equal(candidates[j].ModifiedAt) {
			return candidates[i].ModifiedAt.After(candidates[j].ModifiedAt)
		}
		return strings.ToLower(filepath.Base(candidates[i].Path)) < strings.ToLower(filepath.Base(candidates[j].Path))
	})
	return candidates, nil
}

// Reserve acquires cooperative ownership of a staging tape while its writer is
// still live. The reservation keeps the same object owned across writer close
// and publication.
func (e *Engine) Reserve(staging string) (Reservation, error) {
	if e == nil {
		return Reservation{}, errors.New("taperecovery: nil engine")
	}
	source, sourceKind, err := e.resolveCandidate(staging)
	if err != nil {
		return Reservation{}, err
	}
	if sourceKind != Staging {
		return Reservation{}, fmt.Errorf("%w: reservation source must be a staging tape", ErrNotCandidate)
	}
	if err = e.validateRegularSize(source); err != nil {
		return Reservation{}, err
	}
	lease, err := reserveClaim(source)
	if err != nil {
		return Reservation{}, err
	}
	if err = lease.verifyPath(source); err == nil {
		err = e.validateClaimedSize(lease)
	}
	if err != nil {
		return Reservation{}, errors.Join(err, lease.release())
	}
	return Reservation{Path: source, slot: &reservationSlot{claim: lease}}, nil
}

// PublishReserved consumes a live reservation, attaches a full operation
// handle to the same file identity, and publishes that exact object. It does
// not replay CellTape content; opening the returned Claim remains the single
// validation pass.
func (e *Engine) PublishReserved(reservation *Reservation, published string) (Claim, error) {
	if e == nil {
		return Claim{}, errors.New("taperecovery: nil engine")
	}
	if reservation == nil {
		return Claim{}, fmt.Errorf("%w: staging tape is not reserved", ErrIdentityChanged)
	}
	source, sourceKind, err := e.resolveCandidate(reservation.Path)
	if err != nil {
		return Claim{}, err
	}
	if sourceKind != Staging {
		return Claim{}, fmt.Errorf("%w: publish source must be a staging tape", ErrNotCandidate)
	}
	destination, destinationKind, err := e.resolveCandidate(published)
	if err != nil {
		return Claim{}, err
	}
	if destinationKind != Published {
		return Claim{}, fmt.Errorf("%w: publish destination must be a .celltape", ErrNotCandidate)
	}
	lease, err := reservation.take()
	if err != nil {
		return Claim{}, err
	}
	keepLease := false
	defer func() {
		if !keepLease {
			_ = lease.release()
		}
	}()
	if err = lease.attachOperation(source); err != nil {
		return Claim{}, fmt.Errorf("taperecovery: attach reserved tape: %w", err)
	}
	if err = lease.verifyPath(source); err != nil {
		return Claim{}, err
	}
	if err = validateReservedSize(lease); err != nil {
		return Claim{}, err
	}
	if err = lease.syncPath(source); err != nil {
		return Claim{}, fmt.Errorf("taperecovery: sync tape before publish: %w", err)
	}
	if err = lease.renamePath(source, destination); err != nil {
		return Claim{}, fmt.Errorf("taperecovery: publish claimed tape: %w", err)
	}
	if err = lease.verifyPath(destination); err != nil {
		return Claim{}, fmt.Errorf("taperecovery: verify published claim: %w", err)
	}
	keepLease = true
	return Claim{Path: destination, claim: lease}, nil
}

// Claim validates a candidate's complete committed prefix, validates the first
// frame config before modification, repairs a torn tail through CellTape, and
// durably renames a staging tape to a unique published recovery name. Files are
// retained at their source path on every failure before the final rename.
func (e *Engine) Claim(candidate Candidate) (Tape, error) {
	return e.ClaimContext(context.Background(), candidate)
}

// ClaimContext is Claim with cancellation for full-tape validation and repair.
// Cancellation releases the claim and never gets classified as tail damage.
func (e *Engine) ClaimContext(ctx context.Context, candidate Candidate) (Tape, error) {
	if e == nil {
		return Tape{}, errors.New("taperecovery: nil engine")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Tape{}, err
	}
	path, kind, err := e.resolveCandidate(candidate.Path)
	if err != nil {
		return Tape{}, err
	}
	if candidate.Kind != kind {
		return Tape{}, fmt.Errorf("%w: candidate kind changed", ErrNotCandidate)
	}
	if kind == Staging {
		busy, probeErr := exclusiveProbe(path, true)
		if probeErr != nil {
			return Tape{}, probeErr
		}
		if busy {
			return Tape{}, ErrBusy
		}
	}
	lease, err := e.acquireLease(path)
	if err != nil {
		return Tape{}, err
	}
	keepLease := false
	defer func() {
		if !keepLease {
			_ = lease.release()
		}
	}()
	verify := func(stage, currentPath string) error {
		if verifyErr := lease.verifyPath(currentPath); verifyErr != nil {
			return fmt.Errorf("taperecovery: %s: %w", stage, verifyErr)
		}
		return nil
	}
	// Validate the durable prefix and its config before RepairTail can truncate
	// anything. A bad header, first record, or config therefore leaves all bytes
	// exactly where they were.
	tape, recovery, err := e.loadClaimContext(ctx, lease, path, kind, true)
	if err != nil {
		return Tape{}, err
	}
	if err = verify("verify validated tape", path); err != nil {
		return Tape{}, err
	}
	if recovery.DiscardedBytes > 0 {
		if err = verify("verify tape before repair", path); err != nil {
			return Tape{}, err
		}
		replay, repairErr := lease.openReplayContext(ctx, path, celltape.OpenOptions{
			Limits: e.options.TapeLimits, RepairTail: true,
		})
		if repairErr != nil {
			return Tape{}, fmt.Errorf("taperecovery: repair tail: %w", repairErr)
		}
		repaired := replay.Recovery()
		closeErr := replay.Close()
		if closeErr != nil {
			return Tape{}, fmt.Errorf("taperecovery: close repaired tape: %w", closeErr)
		}
		tape.ValidBytes = repaired.ValidBytes
		tape.Records = repaired.ValidRecords
		tape.RepairedBytes = repaired.DiscardedBytes
		tape.Size = repaired.ValidBytes
		if err = verify("verify repaired tape", path); err != nil {
			return Tape{}, err
		}
	}
	if err = ctx.Err(); err != nil {
		return Tape{}, err
	}
	// A crash can leave an otherwise complete committed prefix in the system
	// cache. Flush file data before the final write-through rename so a
	// successful Claim is a durability boundary even without a truncation.
	if err = verify("verify tape before sync", path); err != nil {
		return Tape{}, err
	}
	if err = lease.syncPath(path); err != nil {
		return Tape{}, fmt.Errorf("taperecovery: sync claimed tape: %w", err)
	}
	if err = verify("verify synced tape", path); err != nil {
		return Tape{}, err
	}
	if err = ctx.Err(); err != nil {
		return Tape{}, err
	}

	if kind == Staging {
		destination := e.recoveredPath(path)
		if err = verify("verify tape before publish", path); err != nil {
			return Tape{}, err
		}
		if err = lease.renamePath(path, destination); err != nil {
			return Tape{}, fmt.Errorf("taperecovery: publish recovered tape: %w", err)
		}
		if err = verify("verify published tape", destination); err != nil {
			return Tape{}, err
		}
		tape.RecoveredFrom = path
		tape.Path = destination
	}
	tape.Claim = Claim{Path: tape.Path, claim: lease}
	keepLease = true
	return tape, nil
}

// Load validates an already published tape without repairing or renaming it.
// A damaged tail returns ErrDamagedTail and leaves every byte untouched.
func (e *Engine) Load(path string) (Tape, error) {
	return e.LoadContext(context.Background(), path)
}

// LoadContext validates a published tape without modifying it and honors
// cancellation while scanning its committed records.
func (e *Engine) LoadContext(ctx context.Context, path string) (Tape, error) {
	if e == nil {
		return Tape{}, errors.New("taperecovery: nil engine")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Tape{}, err
	}
	resolved, kind, err := e.resolveCandidate(path)
	if err != nil {
		return Tape{}, err
	}
	if kind != Published {
		return Tape{}, fmt.Errorf("%w: Load requires a published .celltape", ErrNotCandidate)
	}
	if err = e.validateRegularSize(resolved); err != nil {
		return Tape{}, err
	}
	tape, _, err := e.loadContext(ctx, resolved, Published, false)
	return tape, err
}

func (e *Engine) loadContext(ctx context.Context, path string, kind Kind, allowTail bool) (Tape, celltape.Recovery, error) {
	replay, err := celltape.OpenContext(ctx, path, celltape.OpenOptions{Limits: e.options.TapeLimits})
	if err != nil {
		return Tape{}, celltape.Recovery{}, fmt.Errorf("taperecovery: open tape: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Tape{}, celltape.Recovery{}, errors.Join(err, replay.Close())
	}
	return e.loadReplay(path, kind, allowTail, info.Size(), replay)
}

func (e *Engine) loadClaimContext(
	ctx context.Context,
	lease *claimLease,
	path string,
	kind Kind,
	allowTail bool,
) (Tape, celltape.Recovery, error) {
	replay, err := lease.openReplayContext(ctx, path, celltape.OpenOptions{Limits: e.options.TapeLimits})
	if err != nil {
		return Tape{}, celltape.Recovery{}, fmt.Errorf("taperecovery: open claimed tape: %w", err)
	}
	size, err := lease.fileSize()
	if err != nil {
		return Tape{}, celltape.Recovery{}, errors.Join(err, replay.Close())
	}
	return e.loadReplay(path, kind, allowTail, size, replay)
}

func (e *Engine) loadReplay(
	path string,
	kind Kind,
	allowTail bool,
	size int64,
	replay *celltape.Replay,
) (Tape, celltape.Recovery, error) {
	recovery := replay.Recovery()
	if recovery.TailError != nil && !allowTail {
		_ = replay.Close()
		return Tape{}, recovery, fmt.Errorf("%w: %v", ErrDamagedTail, recovery.TailError)
	}
	summary := replay.Summary()
	closeErr := replay.Close()
	if summary.Records == 0 {
		return Tape{}, recovery, errors.Join(ErrNoFrames, closeErr)
	}
	if closeErr != nil {
		return Tape{}, recovery, closeErr
	}
	config, err := validateVersionedConfig(summary.Config, e.options.MaxConfigBytes)
	if err != nil {
		return Tape{}, recovery, err
	}
	return Tape{
		Claim:            Claim{Path: path},
		SourceKind:       kind,
		Size:             size,
		ValidBytes:       recovery.ValidBytes,
		Records:          recovery.ValidRecords,
		Columns:          summary.FirstColumns,
		Rows:             summary.FirstRows,
		GeometryEpoch:    summary.FirstGeometryEpoch,
		ConfigEpoch:      summary.FirstConfigEpoch,
		FirstSourceNanos: summary.FirstSourceNanos,
		FirstHostNanos:   summary.FirstHostNanos,
		Config:           config,
	}, recovery, nil
}

func validateVersionedConfig(raw []byte, maximum int) (VersionedConfig, error) {
	if len(raw) == 0 || len(raw) > maximum {
		return VersionedConfig{}, fmt.Errorf("%w: config length %d exceeds 1..%d", ErrConfig, len(raw), maximum)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return VersionedConfig{}, fmt.Errorf("%w: config must be one JSON object", ErrConfig)
	}
	versionRaw, ok := object["version"]
	if !ok {
		return VersionedConfig{}, fmt.Errorf("%w: missing version", ErrConfig)
	}
	var version uint32
	if err := json.Unmarshal(versionRaw, &version); err != nil || version != ConfigVersion {
		return VersionedConfig{}, fmt.Errorf("%w: unsupported version %s", ErrConfig, strings.TrimSpace(string(versionRaw)))
	}
	return VersionedConfig{Version: version, Raw: append(json.RawMessage(nil), raw...)}, nil
}

func (e *Engine) resolveCandidate(path string) (string, Kind, error) {
	if strings.TrimSpace(path) == "" {
		return "", 0, ErrNotCandidate
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.directory, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", 0, err
	}
	abs = filepath.Clean(abs)
	relative, err := filepath.Rel(e.directory, abs)
	if err != nil || filepath.IsAbs(relative) || filepath.Dir(relative) != "." || relative == "." || relative == ".." {
		return "", 0, fmt.Errorf("%w: path escapes recovery directory", ErrNotCandidate)
	}
	kind, ok := classifyName(filepath.Base(abs))
	if !ok {
		return "", 0, ErrNotCandidate
	}
	return abs, kind, nil
}

func (e *Engine) validateRegularSize(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: tape is not a regular file", ErrNotCandidate)
	}
	if info.Size() < celltape.FileHeaderBytes || info.Size() > e.options.MaxFileBytes {
		return fmt.Errorf("%w: tape size %d is outside %d..%d", ErrLimit, info.Size(), celltape.FileHeaderBytes, e.options.MaxFileBytes)
	}
	return nil
}

func (e *Engine) acquireLease(path string) (*claimLease, error) {
	if err := e.validateRegularSize(path); err != nil {
		return nil, err
	}
	lease, err := acquireClaim(path)
	if err != nil {
		return nil, err
	}
	if err = lease.verifyPath(path); err == nil {
		err = e.validateClaimedSize(lease)
	}
	if err != nil {
		return nil, errors.Join(err, lease.release())
	}
	return lease, nil
}

func (e *Engine) validateClaimedSize(lease *claimLease) error {
	size, err := lease.fileSize()
	if err != nil {
		return err
	}
	if size < celltape.FileHeaderBytes || size > e.options.MaxFileBytes {
		return fmt.Errorf("%w: claimed tape size %d is outside %d..%d", ErrLimit, size, celltape.FileHeaderBytes, e.options.MaxFileBytes)
	}
	return nil
}

func validateReservedSize(lease *claimLease) error {
	size, err := lease.fileSize()
	if err != nil {
		return err
	}
	if size < celltape.FileHeaderBytes {
		return fmt.Errorf("%w: reserved tape size %d is below %d", ErrLimit, size, celltape.FileHeaderBytes)
	}
	return nil
}

func classifyName(name string) (Kind, bool) {
	lower := strings.ToLower(name)
	if strings.HasPrefix(name, ".") && strings.HasSuffix(lower, stagingSuffix) {
		return Staging, true
	}
	if strings.HasSuffix(lower, ".celltape") {
		return Published, true
	}
	return 0, false
}

func (e *Engine) recoveredPath(staging string) string {
	name := filepath.Base(staging)
	stem := strings.TrimPrefix(name[:len(name)-len(stagingSuffix)], ".")
	lower := strings.ToLower(stem)
	if split := strings.LastIndex(lower, ".celltape."); split >= 0 {
		identifier := stem[split+len(".celltape."):]
		stem = stem[:split] + ".recovered-" + identifier
	} else {
		stem += ".recovered"
	}
	return filepath.Join(e.directory, stem+".celltape")
}
