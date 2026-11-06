//go:build windows

package daemon

import (
	"os"

	"golang.org/x/sys/windows"
)

// Windows byte-range locks are mandatory: locking byte 0 would also stop other processes (tbd
// status) from reading the PID written there. Lock one byte far beyond the end of the file instead.
func lockRange() *windows.Overlapped { return &windows.Overlapped{OffsetHigh: 0x7fffffff} }

func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRange())
}

func unlockFile(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
}
