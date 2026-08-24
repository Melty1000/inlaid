//go:build darwin || linux

package recording

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lowerEncoderPriority(process *os.Process) error {
	if process == nil {
		return nil
	}
	current, err := unix.Getpriority(unix.PRIO_PROCESS, process.Pid)
	if err != nil {
		return fmt.Errorf("read encoder process priority: %w", err)
	}
	if current >= 19 {
		return nil
	}
	target := min(current+5, 19)
	if err := unix.Setpriority(unix.PRIO_PROCESS, process.Pid, target); err != nil {
		return fmt.Errorf("lower encoder process priority: %w", err)
	}
	return nil
}
