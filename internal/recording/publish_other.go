//go:build !windows

package recording

import (
	"fmt"
	"os"
)

func publishMedia(stage, output string, overwrite bool) error {
	if overwrite {
		return os.Rename(stage, output)
	}
	// A hard-link publication is atomic and, unlike portable os.Rename, cannot
	// replace a destination created after Start's initial no-overwrite check.
	if err := os.Link(stage, output); err != nil {
		return fmt.Errorf("link completed recording into place: %w", err)
	}
	if err := os.Remove(stage); err != nil {
		return fmt.Errorf("remove published recording stage: %w", err)
	}
	return nil
}
