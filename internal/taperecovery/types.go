// Package taperecovery discovers, validates, repairs, and durably claims
// CellTape files left in a recording recovery directory.
package taperecovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Melty1000/inlaid/internal/celltape"
)

const (
	// ConfigVersion is the recording configuration envelope understood by this
	// recovery implementation.
	ConfigVersion uint32 = 1
)

var (
	// ErrBusy means a tape cannot be claimed because another live handle has
	// incompatible sharing. Scan silently omits busy staging tapes.
	ErrBusy = errors.New("taperecovery: tape is busy")
	// ErrLimit means a configured resource bound was exceeded.
	ErrLimit = errors.New("taperecovery: resource limit exceeded")
	// ErrNotCandidate means a path is outside the recovery directory or does
	// not have a supported direct-child tape name.
	ErrNotCandidate = errors.New("taperecovery: path is not a recovery candidate")
	// ErrNoFrames means a tape has no complete committed frame.
	ErrNoFrames = errors.New("taperecovery: tape has no durable frames")
	// ErrDamagedTail means Load found a damaged suffix. Claim is the only API
	// that may repair it.
	ErrDamagedTail = errors.New("taperecovery: tape has a damaged tail")
	// ErrConfig means the first frame's configuration envelope is malformed.
	ErrConfig = errors.New("taperecovery: invalid versioned config")
)

// Options bounds all input selected by the recovery directory. Zero values
// select conservative defaults.
type Options struct {
	MaxDirectoryEntries int
	MaxCandidates       int
	MaxFileBytes        int64
	MaxConfigBytes      int
	// TapeLimits are passed to celltape.Open. Zero fields select CellTape's
	// own defaults. MaxConfigBytes above independently bounds the actual first
	// frame config without rejecting a tape whose header declares a larger cap.
	TapeLimits celltape.Limits
}

func (o Options) normalized() (Options, error) {
	if o.MaxDirectoryEntries == 0 {
		o.MaxDirectoryEntries = 4096
	}
	if o.MaxCandidates == 0 {
		o.MaxCandidates = 256
	}
	if o.MaxFileBytes == 0 {
		o.MaxFileBytes = 64 << 30
	}
	if o.MaxConfigBytes == 0 {
		o.MaxConfigBytes = 64 << 10
	}
	if o.MaxDirectoryEntries < 1 || o.MaxDirectoryEntries > 1_000_000 ||
		o.MaxCandidates < 1 || o.MaxCandidates > o.MaxDirectoryEntries ||
		o.MaxFileBytes < celltape.FileHeaderBytes || o.MaxFileBytes > 1<<50 ||
		o.MaxConfigBytes < 1 || o.MaxConfigBytes > 16<<20 {
		return Options{}, fmt.Errorf("%w: invalid recovery options", ErrLimit)
	}
	return o, nil
}

// Kind identifies how a candidate entered the recovery directory.
type Kind uint8

const (
	// Published is an already published *.celltape file.
	Published Kind = iota + 1
	// Staging is a hidden crash-left *.celltape.tmp file.
	Staging
)

func (k Kind) String() string {
	switch k {
	case Published:
		return "published"
	case Staging:
		return "staging"
	default:
		return "unknown"
	}
}

// Candidate is immutable Scan output. Claim revalidates its path, kind,
// regular-file status, size, and live-writer state before any repair.
type Candidate struct {
	Path       string
	Kind       Kind
	Size       int64
	ModifiedAt time.Time
}

// VersionedConfig is a validated copy of the first committed frame's JSON
// configuration envelope. Raw remains available for dashboard-specific decode.
type VersionedConfig struct {
	Version uint32
	Raw     json.RawMessage
}

// Tape is durable claimed metadata sufficient to present a recovery choice and
// pass Path to the offline CellTape exporter. RecoveredFrom is set only when a
// staging tape was atomically renamed. RepairedBytes is the discarded damaged
// suffix; no file is ever deleted by this package.
type Tape struct {
	Path             string
	RecoveredFrom    string
	SourceKind       Kind
	Size             int64
	ValidBytes       int64
	Records          uint64
	RepairedBytes    int64
	Columns          uint32
	Rows             uint32
	GeometryEpoch    uint64
	ConfigEpoch      uint64
	FirstSourceNanos uint64
	FirstHostNanos   uint64
	Config           VersionedConfig
	claim            *claimLease
}

// Release gives up this process's recovery ownership. Claim ownership is
// backed by the operating system, so it is also released automatically if the
// process exits unexpectedly. Release is idempotent and remains safe when a
// Tape value has been copied.
func (t *Tape) Release() error {
	if t == nil || t.claim == nil {
		return nil
	}
	return t.claim.release()
}
