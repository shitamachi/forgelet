package executor

import (
	"syscall"
	"time"
)

// Becoming a subreaper lets the executor reap grandchildren after their shell
// exits, both as a container PID 1 and during local execution.
func prepareProcessReaping() error {
	const prSetChildSubreaper = 36
	_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func reapProcessGroup(group int) {
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-group, &status, syscall.WNOHANG, nil)
		if err == syscall.ECHILD {
			return
		}
		if err != nil && err != syscall.EINTR {
			return
		}
		if pid == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
}
