//go:build !windows

package recording

import "os"

func lowerEncoderPriority(_ *os.Process) error {
	return nil
}
