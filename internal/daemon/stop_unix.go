//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// requestStopOS sends SIGTERM, which `tbd run` handles like Ctrl+C (signalContext).
func requestStopOS(home string, pid int) (string, error) {
	if err := signalHolder(home, pid, syscall.SIGTERM); err != nil {
		return "", err
	}
	return "SIGTERM", nil
}

// forceStop sends SIGKILL.
func forceStop(home string, pid int) error { return signalHolder(home, pid, syscall.SIGKILL) }

// signalHolder signals pid after verifying, with the process already pinned, that it holds the
// lock. os.FindProcess opens a pidfd on Linux (Go 1.23+), so the signal reaches the verified
// process even if it exited and its PID was reused in between; elsewhere the window between the
// check and kill(2) is as short as it can be.
func signalHolder(home string, pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer p.Release()
	if err := verifyHolder(home, pid); err != nil {
		return err
	}
	return p.Signal(sig)
}
