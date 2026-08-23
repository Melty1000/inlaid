//go:build !windows

package celltape

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// A hard-link publish is atomic and cannot replace a destination that appears
// concurrently. The source is removed only after the destination directory is
// synced; a crash can therefore leave a harmless duplicate staging name, not a
// missing canonical tape.
func publishTape(staging, final string) error {
	if err := os.Link(staging, final); err != nil {
		return fmt.Errorf("publish celltape link: %w", err)
	}
	directory, err := os.Open(filepath.Dir(final))
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err = errors.Join(syncErr, closeErr); err != nil {
		return err
	}
	return os.Remove(staging)
}
