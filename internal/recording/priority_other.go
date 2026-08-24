//go:build !windows && !darwin && !linux

package recording

import "os"

func lowerEncoderPriority(_ *os.Process) error {
	return nil
}
