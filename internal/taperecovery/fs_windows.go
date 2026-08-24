//go:build windows

package taperecovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/Melty1000/inlaid/internal/celltape"
	"golang.org/x/sys/windows"
)

func (*Engine) reconcileRetirement(_ string, _ string) (bool, error) {
	return false, nil
}

// claimLease owns one exact file identity with a distant byte lock. Recovery
// claims use one restrictive handle for both ownership and mutation. A live
// writer handoff begins with a broadly shared lock handle, then attaches a
// restrictive operation handle before publication, replay, and retirement.
type claimLease struct {
	handle     windows.Handle
	lockHandle windows.Handle
	identity   fileIdentity
	mu         sync.RWMutex
	once       sync.Once
	released   bool
	err        error
}

type fileIdentity struct {
	volume    uint32
	indexHigh uint32
	indexLow  uint32
}

var claimOffset = windows.Overlapped{Offset: 0xffffffff, OffsetHigh: 0x7fffffff}

func acquireClaim(path string) (*claimLease, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
		windows.FILE_SHARE_READ,
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
	identity, err := identityFromHandle(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
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
	return &claimLease{handle: handle, lockHandle: handle, identity: identity}, nil
}

func reserveClaim(path string) (*claimLease, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
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
	identity, err := identityFromHandle(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
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
	return &claimLease{lockHandle: handle, identity: identity}, nil
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
	if l.handle != 0 {
		return errors.New("taperecovery: reservation already has an operation handle")
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	var handle windows.Handle
	for {
		handle, err = windows.CreateFile(
			name,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
			windows.FILE_SHARE_READ,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if err == nil {
			break
		}
		if !isBusyError(err) {
			return fmt.Errorf("%w: open reserved path: %v", ErrIdentityChanged, err)
		}
		if !time.Now().Before(deadline) {
			return ErrBusy
		}
		time.Sleep(time.Millisecond)
	}
	identity, err := identityFromHandle(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return err
	}
	if identity != l.identity {
		_ = windows.CloseHandle(handle)
		return fmt.Errorf("%w: %s", ErrIdentityChanged, path)
	}
	l.handle = handle
	return nil
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
	handle := l.handle
	if handle == 0 {
		handle = l.lockHandle
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return 0, err
	}
	size := int64(uint64(information.FileSizeHigh)<<32 | uint64(information.FileSizeLow))
	if size < 0 {
		return 0, errors.New("taperecovery: claimed tape size exceeds signed range")
	}
	return size, nil
}

func (l *claimLease) openReplayContext(ctx context.Context, path string, options celltape.OpenOptions) (*celltape.Replay, error) {
	if l == nil {
		return nil, fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.RLock()
	if l.released {
		l.mu.RUnlock()
		return nil, fmt.Errorf("%w: claim was released", ErrIdentityChanged)
	}
	if l.handle == 0 {
		l.mu.RUnlock()
		return nil, fmt.Errorf("%w: claim has no operation handle", ErrIdentityChanged)
	}
	if err := l.verifyPathLocked(path); err != nil {
		l.mu.RUnlock()
		return nil, err
	}
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	err := windows.DuplicateHandle(process, l.handle, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS)
	l.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicate), path)
	if file == nil {
		_ = windows.CloseHandle(duplicate)
		return nil, errors.New("taperecovery: duplicate claim handle is invalid")
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
		overlapped := claimOffset
		unlockErr := windows.UnlockFileEx(l.lockHandle, 0, 1, 0, &overlapped)
		var operationCloseErr error
		if l.handle != 0 && l.handle != l.lockHandle {
			operationCloseErr = windows.CloseHandle(l.handle)
		}
		lockCloseErr := windows.CloseHandle(l.lockHandle)
		l.released = true
		l.err = errors.Join(unlockErr, operationCloseErr, lockCloseErr)
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
	identity, err := identityFromPath(path)
	if err != nil {
		return fmt.Errorf("%w: inspect %s: %v", ErrIdentityChanged, path, err)
	}
	if identity != l.identity {
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
	if l.released {
		return fmt.Errorf("%w: claim was released", ErrIdentityChanged)
	}
	if l.handle == 0 {
		return fmt.Errorf("%w: claim has no operation handle", ErrIdentityChanged)
	}
	if err := l.verifyPathLocked(path); err != nil {
		return err
	}
	return windows.FlushFileBuffers(l.handle)
}

type fileRenameInformation struct {
	replaceIfExists uint32
	rootDirectory   windows.Handle
	fileNameLength  uint32
	fileName        [1]uint16
}

func (l *claimLease) renamePath(source, destination string) error {
	if l == nil {
		return fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return fmt.Errorf("%w: claim was released", ErrIdentityChanged)
	}
	if l.handle == 0 {
		return fmt.Errorf("%w: claim has no operation handle", ErrIdentityChanged)
	}
	if err := l.verifyPathLocked(source); err != nil {
		return err
	}
	name, err := windows.UTF16FromString(destination)
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	var layout fileRenameInformation
	offset := int(unsafe.Offsetof(layout.fileName))
	buffer := make([]byte, offset+len(name)*2)
	information := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.fileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice(&information.fileName[0], len(name)), name)
	if err = windows.SetFileInformationByHandle(
		l.handle,
		windows.FileRenameInfo,
		&buffer[0],
		uint32(len(buffer)),
	); err != nil {
		return err
	}
	if err = windows.FlushFileBuffers(l.handle); err != nil {
		return err
	}
	return l.verifyPathLocked(destination)
}

func (l *claimLease) retirePath(path string) error {
	if l == nil {
		return fmt.Errorf("%w: claim is missing", ErrIdentityChanged)
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return fmt.Errorf("%w: claim was released", ErrIdentityChanged)
	}
	if l.handle == 0 {
		l.mu.Unlock()
		return fmt.Errorf("%w: claim has no operation handle", ErrIdentityChanged)
	}
	if err := l.verifyPathLocked(path); err != nil {
		l.mu.Unlock()
		return err
	}
	deleteFile := byte(1)
	err := windows.SetFileInformationByHandle(
		l.handle,
		windows.FileDispositionInfo,
		&deleteFile,
		uint32(unsafe.Sizeof(deleteFile)),
	)
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return l.release()
}

func identityFromPath(path string) (fileIdentity, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fileIdentity{}, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fileIdentity{}, err
	}
	identity, identityErr := identityFromHandle(handle)
	closeErr := windows.CloseHandle(handle)
	return identity, errors.Join(identityErr, closeErr)
}

func identityFromHandle(handle windows.Handle) (fileIdentity, error) {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{
		volume:    information.VolumeSerialNumber,
		indexHigh: information.FileIndexHigh,
		indexLow:  information.FileIndexLow,
	}, nil
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
