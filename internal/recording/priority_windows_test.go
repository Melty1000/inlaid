//go:build windows

package recording

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestLowerEncoderPriorityUsesBelowNormalClass(t *testing.T) {
	if os.Getenv("INLAID_PRIORITY_HELPER") == "1" {
		time.Sleep(30 * time.Second)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=TestLowerEncoderPriorityUsesBelowNormalClass")
	command.Env = append(os.Environ(), "INLAID_PRIORITY_HELPER=1")
	if err := command.Start(); err != nil {
		t.Fatalf("start priority helper: %v", err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()

	if err := lowerEncoderPriority(command.Process); err != nil {
		t.Fatalf("lowerEncoderPriority() error = %v", err)
	}
	var priority uint32
	var priorityErr error
	if err := command.Process.WithHandle(func(handle uintptr) {
		priority, priorityErr = windows.GetPriorityClass(windows.Handle(handle))
	}); err != nil {
		t.Fatalf("access priority helper handle: %v", err)
	}
	if priorityErr != nil {
		t.Fatalf("query priority helper: %v", priorityErr)
	}
	if priority != windows.BELOW_NORMAL_PRIORITY_CLASS {
		t.Fatalf("priority class = %#x, want below-normal %#x", priority, uint32(windows.BELOW_NORMAL_PRIORITY_CLASS))
	}
}
