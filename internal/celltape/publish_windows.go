//go:build windows

package celltape

import "golang.org/x/sys/windows"

// MoveFileEx without REPLACE_EXISTING is an atomic no-replace operation. The
// WRITE_THROUGH flag asks Windows not to return until the same-volume rename is
// reflected by the filesystem.
func publishTape(staging, final string) error {
	from, err := windows.UTF16PtrFromString(staging)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(final)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
