package taperecovery

import (
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

// Directory returns the absolute recovery directory.
func (e *Engine) Directory() string {
	if e == nil {
		return ""
	}
	return e.directory
}

// Scan returns direct regular published tapes and closed crash-left staging
// tapes. An active Windows writer is omitted only after an exclusive-sharing
// probe reports a sharing violation; no age heuristic is used.
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
		kind, ok := classifyName(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("taperecovery: inspect %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(e.directory, entry.Name())
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

// Claim validates a candidate's complete committed prefix, validates the first
// frame config before modification, repairs a torn tail through CellTape, and
// durably renames a staging tape to a unique published recovery name. Files are
// retained at their source path on every failure before the final rename.
func (e *Engine) Claim(candidate Candidate) (Tape, error) {
	if e == nil {
		return Tape{}, errors.New("taperecovery: nil engine")
	}
	path, kind, err := e.resolveCandidate(candidate.Path)
	if err != nil {
		return Tape{}, err
	}
	if candidate.Kind != kind {
		return Tape{}, fmt.Errorf("%w: candidate kind changed", ErrNotCandidate)
	}
	if err = e.validateRegularSize(path); err != nil {
		return Tape{}, err
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
	lease, err := acquireClaim(path)
	if err != nil {
		return Tape{}, err
	}
	keepLease := false
	defer func() {
		if !keepLease {
			_ = lease.release()
		}
	}()

	// Validate the durable prefix and its config before RepairTail can truncate
	// anything. A bad header, first record, or config therefore leaves all bytes
	// exactly where they were.
	tape, recovery, err := e.load(path, kind, true)
	if err != nil {
		return Tape{}, err
	}
	if recovery.DiscardedBytes > 0 {
		replay, repairErr := celltape.Open(path, celltape.OpenOptions{
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
	}
	// A crash can leave an otherwise complete committed prefix in the system
	// cache. Flush file data before the final write-through rename so a
	// successful Claim is a durability boundary even without a truncation.
	if err = syncTape(path); err != nil {
		return Tape{}, fmt.Errorf("taperecovery: sync claimed tape: %w", err)
	}

	if kind == Staging {
		destination := e.recoveredPath(path)
		if err = renameDurable(path, destination); err != nil {
			return Tape{}, fmt.Errorf("taperecovery: publish recovered tape: %w", err)
		}
		tape.RecoveredFrom = path
		tape.Path = destination
	}
	tape.claim = lease
	keepLease = true
	return tape, nil
}

func syncTape(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

// Load validates an already published tape without repairing or renaming it.
// A damaged tail returns ErrDamagedTail and leaves every byte untouched.
func (e *Engine) Load(path string) (Tape, error) {
	if e == nil {
		return Tape{}, errors.New("taperecovery: nil engine")
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
	tape, _, err := e.load(resolved, Published, false)
	return tape, err
}

func (e *Engine) load(path string, kind Kind, allowTail bool) (Tape, celltape.Recovery, error) {
	replay, err := celltape.Open(path, celltape.OpenOptions{Limits: e.options.TapeLimits})
	if err != nil {
		return Tape{}, celltape.Recovery{}, fmt.Errorf("taperecovery: open tape: %w", err)
	}
	recovery := replay.Recovery()
	if recovery.TailError != nil && !allowTail {
		_ = replay.Close()
		return Tape{}, recovery, fmt.Errorf("%w: %v", ErrDamagedTail, recovery.TailError)
	}
	first, nextErr := replay.Next()
	closeErr := replay.Close()
	if nextErr != nil {
		if errors.Is(nextErr, io.EOF) {
			return Tape{}, recovery, ErrNoFrames
		}
		return Tape{}, recovery, errors.Join(fmt.Errorf("taperecovery: read first frame: %w", nextErr), closeErr)
	}
	if closeErr != nil {
		return Tape{}, recovery, closeErr
	}
	config, err := validateVersionedConfig(first.Config, e.options.MaxConfigBytes)
	if err != nil {
		return Tape{}, recovery, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Tape{}, recovery, err
	}
	return Tape{
		Path:             path,
		SourceKind:       kind,
		Size:             info.Size(),
		ValidBytes:       recovery.ValidBytes,
		Records:          recovery.ValidRecords,
		Columns:          first.Columns,
		Rows:             first.Rows,
		GeometryEpoch:    first.GeometryEpoch,
		ConfigEpoch:      first.ConfigEpoch,
		FirstSourceNanos: first.SourceNanos,
		FirstHostNanos:   first.HostNanos,
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
