//go:build windows

package recording

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lowerEncoderPriority keeps background media work from competing at equal
// priority with the terminal renderer. The caller treats failure as best-effort
// because process-handle policy varies across managed Windows installations.
func lowerEncoderPriority(process *os.Process) error {
	if process == nil {
		return nil
	}
	var priorityErr error
	if err := process.WithHandle(func(handle uintptr) {
		priorityErr = windows.SetPriorityClass(windows.Handle(handle), windows.BELOW_NORMAL_PRIORITY_CLASS)
	}); err != nil {
		return fmt.Errorf("access encoder process handle: %w", err)
	}
	if priorityErr != nil {
		return fmt.Errorf("lower encoder process priority: %w", priorityErr)
	}
	return nil
}
