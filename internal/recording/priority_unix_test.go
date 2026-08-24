//go:build darwin || linux

package recording

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLowerEncoderPriorityUsesUnixNiceness(t *testing.T) {
	if os.Getenv("INLAID_PRIORITY_HELPER") == "1" {
		time.Sleep(30 * time.Second)
		return
	}

	command := startPriorityHelper(t)
	before := processPriority(t, command.Process)

	if err := lowerEncoderPriority(command.Process); err != nil {
		t.Fatalf("lowerEncoderPriority() error = %v", err)
	}
	after := processPriority(t, command.Process)
	if after < before || before < 19 && after == before {
		t.Fatalf("niceness changed from %d to %d; want a lower or unchanged-at-minimum priority", before, after)
	}
}

func TestLowerEncoderPriorityNeverPromotesAlreadyNicedProcess(t *testing.T) {
	command := startPriorityHelper(t)
	before := processPriority(t, command.Process)
	if before >= 19 {
		t.Skip("process is already at the minimum priority")
	}
	alreadyNiced := min(max(before+1, 10), 19)
	if err := unix.Setpriority(unix.PRIO_PROCESS, command.Process.Pid, alreadyNiced); err != nil {
		t.Fatalf("prepare already-niced helper: %v", err)
	}
	before = processPriority(t, command.Process)

	if err := lowerEncoderPriority(command.Process); err != nil {
		t.Fatalf("lowerEncoderPriority() error = %v", err)
	}
	after := processPriority(t, command.Process)
	if after < before {
		t.Fatalf("niceness changed from %d to %d; encoder process was promoted", before, after)
	}
}

func startPriorityHelper(t *testing.T) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=TestLowerEncoderPriorityUsesUnixNiceness")
	command.Env = append(os.Environ(), "INLAID_PRIORITY_HELPER=1")
	if err := command.Start(); err != nil {
		t.Fatalf("start priority helper: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	return command
}

func processPriority(t *testing.T, process *os.Process) int {
	t.Helper()
	priority, err := unix.Getpriority(unix.PRIO_PROCESS, process.Pid)
	if err != nil {
		t.Fatalf("read priority helper niceness: %v", err)
	}
	return priority
}
