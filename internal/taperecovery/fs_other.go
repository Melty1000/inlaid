//go:build linux || darwin

package taperecovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Melty1000/inlaid/internal/celltape"
	"golang.org/x/sys/unix"
)

// claimLease combines an advisory cross-process lock with the identity of the
// opened file. Inlaid writers reserve their staging tape before publishing any
// recording state, so another Inlaid process cannot recover it while it is live.
type claimLease struct {
	file     *os.File
	identity os.FileInfo
	mu       sync.RWMutex
	once     sync.Once
	released bool
	err      error
}

func acquireClaim(path string) (*claimLease, error) {
	return openClaim(path)
}

func reserveClaim(path string) (*claimLease, error) {
	return openClaim(path)
}

func openClaim(path string) (*claimLease, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	if err = lockClaim(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	identity, err := file.Stat()
	if err != nil {
		_ = unlockClaim(file)
		_ = file.Close()
		return nil, err
	}
	return &claimLease{file: file, identity: identity}, nil
}

func (l *claimLease) attachOperation(path string) error {
	if l == nil {
		return fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return fmt.Errorf("%w: claim was released", ErrIdentityChanged)
	}
	if l.file == nil {
		return fmt.Errorf("%w: claim has no operation file", ErrIdentityChanged)
	}
	return l.verifyPathLocked(path)
}

func (l *claimLease) fileSize() (int64, error) {
	if l == nil {
		return 0, fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.released {
		return 0, fmt.Errorf("%w: claim was released", ErrIdentityChanged)
	}
	if l.file == nil {
		return 0, fmt.Errorf("%w: claim has no operation file", ErrIdentityChanged)
	}
	info, err := l.file.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (l *claimLease) openReplayContext(ctx context.Context, path string, options celltape.OpenOptions) (*celltape.Replay, error) {
	if l == nil {
		return nil, fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.RLock()
	if l.released || l.file == nil {
		l.mu.RUnlock()
		return nil, fmt.Errorf("%w: claim has no operation file", ErrIdentityChanged)
	}
	if err := l.verifyPathLocked(path); err != nil {
		l.mu.RUnlock()
		return nil, err
	}
	descriptor, err := unix.Dup(int(l.file.Fd()))
	l.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("taperecovery: duplicate claim file is invalid")
	}
	return celltape.OpenOwnedFileContext(ctx, file, options)
}

func (l *claimLease) release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		unlockErr := unlockClaim(l.file)
		closeErr := l.file.Close()
		l.err = errors.Join(unlockErr, closeErr)
		l.released = true
	})
	return l.err
}

func (l *claimLease) verifyPath(path string) error {
	if l == nil {
		return fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.released {
		return fmt.Errorf("%w: claim was released", ErrIdentityChanged)
	}
	return l.verifyPathLocked(path)
}

func (l *claimLease) verifyPathLocked(path string) error {
	identity, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect %s: %v", ErrIdentityChanged, path, err)
	}
	if !os.SameFile(l.identity, identity) {
		return fmt.Errorf("%w: %s", ErrIdentityChanged, path)
	}
	return nil
}

func (l *claimLease) syncPath(path string) error {
	if l == nil {
		return fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.released || l.file == nil {
		return fmt.Errorf("%w: claim has no operation file", ErrIdentityChanged)
	}
	if err := l.verifyPathLocked(path); err != nil {
		return err
	}
	return syncFile(l.file)
}

func (l *claimLease) renamePath(source, destination string) error {
	if l == nil {
		return fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.file == nil {
		return fmt.Errorf("%w: claim has no operation file", ErrIdentityChanged)
	}
	if err := l.verifyPathLocked(source); err != nil {
		return err
	}
	if err := renameNoReplace(source, destination); err != nil {
		return err
	}
	if err := l.verifyPathLocked(destination); err != nil {
		return err
	}
	sourceDirectory := filepath.Dir(source)
	destinationDirectory := filepath.Dir(destination)
	if err := syncDirectory(destinationDirectory); err != nil {
		return err
	}
	if sourceDirectory != destinationDirectory {
		if err := syncDirectory(sourceDirectory); err != nil {
			return err
		}
	}
	if err := l.verifyPathLocked(destination); err != nil {
		return err
	}
	return nil
}

func (l *claimLease) retirePath(path string) error {
	return l.retirePathBeforeUnlink(path, nil)
}

func (l *claimLease) retirePathBeforeUnlink(path string, beforeUnlink func(string) error) error {
	if l == nil {
		return fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.Lock()
	if l.released || l.file == nil {
		l.mu.Unlock()
		return fmt.Errorf("%w: claim has no operation file", ErrIdentityChanged)
	}
	if err := l.verifyPathLocked(path); err != nil {
		l.mu.Unlock()
		return err
	}
	tombstone, err := privateRetirementPath(path)
	if err != nil {
		l.mu.Unlock()
		return err
	}
	if err = renameNoReplace(path, tombstone); err != nil {
		l.mu.Unlock()
		return err
	}
	restore := func(retireErr error) error {
		restoreErr := renameNoReplace(tombstone, path)
		if restoreErr == nil {
			restoreErr = syncDirectory(filepath.Dir(path))
		}
		l.mu.Unlock()
		if restoreErr != nil {
			restoreErr = fmt.Errorf("taperecovery: restore retirement tombstone %s: %w", tombstone, restoreErr)
		}
		return errors.Join(retireErr, restoreErr)
	}
	if err = l.verifyPathLocked(tombstone); err != nil {
		return restore(err)
	}
	if err = syncDirectory(filepath.Dir(tombstone)); err != nil {
		return restore(err)
	}
	if beforeUnlink != nil {
		if err = beforeUnlink(tombstone); err != nil {
			return restore(err)
		}
	}
	if err = l.verifyPathLocked(tombstone); err != nil {
		return restore(err)
	}
	if err = os.Remove(tombstone); err != nil {
		return restore(err)
	}
	if err = syncDirectory(filepath.Dir(tombstone)); err != nil {
		l.mu.Unlock()
		return err
	}
	l.mu.Unlock()
	return l.release()
}

func privateRetirementPath(path string) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), ".inlaid-retiring-"+hex.EncodeToString(nonce[:])), nil
}

func (*Engine) reconcileRetirement(path, name string) (bool, error) {
	const prefix = ".inlaid-retiring-"
	if !strings.HasPrefix(name, prefix) {
		return false, nil
	}
	nonce := strings.TrimPrefix(name, prefix)
	decoded, err := hex.DecodeString(nonce)
	if err != nil || len(decoded) != 16 || nonce != hex.EncodeToString(decoded) {
		return false, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return true, err
	}
	if !info.Mode().IsRegular() {
		return true, nil
	}
	lease, err := acquireClaim(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return true, err
	}
	if !os.SameFile(info, lease.identity) {
		identityErr := fmt.Errorf("%w: %s", ErrIdentityChanged, path)
		return true, errors.Join(identityErr, lease.release())
	}
	retireErr := lease.unlinkRetirement(path)
	releaseErr := lease.release()
	if retireErr != nil || releaseErr != nil {
		return true, errors.Join(retireErr, releaseErr)
	}
	return true, nil
}

func (l *claimLease) unlinkRetirement(path string) error {
	if l == nil {
		return fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.file == nil {
		return fmt.Errorf("%w: claim has no operation file", ErrIdentityChanged)
	}
	if err := l.verifyPathLocked(path); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := syncDirectory(directory); err != nil {
		return err
	}
	if err := l.verifyPathLocked(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func exclusiveProbe(path string, _ bool) (bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	if err = lockClaim(file); err != nil {
		_ = file.Close()
		if errors.Is(err, ErrBusy) {
			return true, nil
		}
		return false, err
	}
	return false, errors.Join(unlockClaim(file), file.Close())
}

func lockClaim(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrBusy
		}
		return err
	}
	return nil
}

func unlockClaim(file *os.File) error {
	if file == nil {
		return nil
	}
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
