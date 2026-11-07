//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
func unlockFile(f *os.File)     { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// listenStop has nothing to do on Unix: `tbd stop` sends SIGTERM, which `tbd run` and `tbd login`
// already turn into a graceful shutdown (signalContext).
func listenStop(*os.File) (*stopListener, error) { return nil, nil }

// kernelLockOwners lists the PIDs that /proc/locks says hold a flock on f (Linux; elsewhere, or
// when /proc/locks is unreadable, ok is false).
func kernelLockOwners(f *os.File) ([]int, bool) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return nil, false
	}
	b, err := os.ReadFile("/proc/locks")
	if err != nil {
		return nil, false
	}
	return parseProcLocks(string(b), uint64(st.Ino)), true
}
