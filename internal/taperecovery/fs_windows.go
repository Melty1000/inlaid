//go:build windows

package taperecovery

import (
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

// claimLease keeps an advisory ownership byte locked far beyond any permitted
// CellTape size. CellTape readers and repairers never touch this byte, while a
// second recovery process attempting the same lock receives ErrBusy. Windows
// releases both the lock and handle if the owning process crashes.
type claimLease struct {
	handle windows.Handle
	once   sync.Once
	err    error
}

var claimOffset = windows.Overlapped{Offset: 0xffffffff, OffsetHigh: 0x7fffffff}

func acquireClaim(path string) (*claimLease, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		if isBusyError(err) {
			return nil, ErrBusy
		}
		return nil, err
	}
	overlapped := claimOffset
	err = windows.LockFileEx(
		handle,
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if err != nil {
		_ = windows.CloseHandle(handle)
		if isBusyError(err) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return &claimLease{handle: handle}, nil
}

func (l *claimLease) release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		overlapped := claimOffset
		unlockErr := windows.UnlockFileEx(l.handle, 0, 1, 0, &overlapped)
		closeErr := windows.CloseHandle(l.handle)
		l.err = errors.Join(unlockErr, closeErr)
	})
	return l.err
}

func isBusyError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_IO_PENDING)
}

// exclusiveProbe asks the Windows object manager for read/write access with a
// zero share mask. Sharing checks are symmetric: this fails while CellTape's
// live writer handle is open even though that original handle permits sharing.
func exclusiveProbe(path string, _ bool) (bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		if isBusyError(err) {
			return true, nil
		}
		return false, err
	}
	return false, windows.CloseHandle(handle)
}

// MoveFileEx without REPLACE_EXISTING preserves any pre-existing output. The
// WRITE_THROUGH flag does not return until the same-volume rename is durable.
func renameDurable(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
