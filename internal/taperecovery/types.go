// Package taperecovery discovers, validates, repairs, and durably claims
// CellTape files left in a recording recovery directory.
package taperecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Melty1000/inlaid/internal/celltape"
)

const (
	// ConfigVersion is the recording configuration envelope understood by this
	// recovery implementation.
	ConfigVersion uint32 = 1
)

var (
	// ErrBusy means another live process owns the tape through the platform
	// claim mechanism. Scan silently omits busy staging tapes.
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
	// ErrIdentityChanged means a claimed path no longer names the file object
	// whose operating-system identity was captured when the claim was acquired.
	ErrIdentityChanged = errors.New("taperecovery: claimed tape identity changed")
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

// Reservation owns one live staging object before its writer closes. Copies
// share one release/transfer slot, so only one caller can publish or release
// the reservation.
type Reservation struct {
	Path string
	slot *reservationSlot
}

type reservationSlot struct {
	mu    sync.Mutex
	claim *claimLease
}

func (r *Reservation) take() (*claimLease, error) {
	if r == nil || r.slot == nil {
		return nil, fmt.Errorf("%w: staging tape is not reserved", ErrIdentityChanged)
	}
	r.slot.mu.Lock()
	defer r.slot.mu.Unlock()
	if r.slot.claim == nil {
		return nil, fmt.Errorf("%w: staging reservation was already consumed", ErrIdentityChanged)
	}
	claim := r.slot.claim
	r.slot.claim = nil
	return claim, nil
}

// Release gives up a reservation without publishing it.
func (r *Reservation) Release() error {
	if r == nil || r.slot == nil {
		return nil
	}
	r.slot.mu.Lock()
	claim := r.slot.claim
	r.slot.claim = nil
	r.slot.mu.Unlock()
	if claim == nil {
		return nil
	}
	return claim.release()
}

// Claim pins one exact CellTape object through export and retirement. Copies
// share ownership, and Release remains idempotent across those copies.
type Claim struct {
	Path  string
	claim *claimLease
}

// Release gives up this process's ownership. The operating system also releases
// it if the process exits unexpectedly.
func (c *Claim) Release() error {
	if c == nil || c.claim == nil {
		return nil
	}
	return c.claim.release()
}

// VerifyIdentity proves that Path still names the claimed file object. It
// fails closed after Release and for unclaimed values.
func (c *Claim) VerifyIdentity() error {
	if c == nil || c.claim == nil {
		return fmt.Errorf("%w: tape is not claimed", ErrIdentityChanged)
	}
	return c.claim.verifyPath(c.Path)
}

// Retire removes the claimed file object. A successful retirement also
// releases ownership.
func (c *Claim) Retire() error {
	if c == nil || c.claim == nil {
		return fmt.Errorf("%w: tape is not claimed", ErrIdentityChanged)
	}
	return c.claim.retirePath(c.Path)
}

// OpenReplayContext validates a duplicate of the claimed operation handle.
// The returned replay owns only that duplicate; closing it never releases the
// claim.
func (c *Claim) OpenReplayContext(ctx context.Context, options celltape.OpenOptions) (*celltape.Replay, error) {
	if c == nil || c.claim == nil {
		return nil, fmt.Errorf("%w: tape is not claimed", ErrIdentityChanged)
	}
	return c.claim.openReplayContext(ctx, c.Path, options)
}

// Tape is validated recovery metadata plus exact-object ownership.
// RecoveredFrom is set only when a staging tape was atomically renamed.
// RepairedBytes is the discarded damaged suffix.
type Tape struct {
	Claim
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
}
