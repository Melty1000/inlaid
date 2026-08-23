//go:build windows

package recording

import "golang.org/x/sys/windows"

// publishMedia makes a completed same-volume stage visible in one filesystem
// operation. MOVEFILE_WRITE_THROUGH asks Windows not to return before the move
// has reached the filesystem; REPLACE_EXISTING preserves Config.Overwrite.
func publishMedia(stage, output string, overwrite bool) error {
	from, err := windows.UTF16PtrFromString(stage)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(output)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if overwrite {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(from, to, flags)
}
