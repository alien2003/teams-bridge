package daemon

import (
	"fmt"
	"os"
	"syscall"
)

// checkLockOwner cross-checks pid with /proc/locks: when the kernel lists any flock on f's inode,
// pid must be among its holders. An unreadable /proc/locks, or no entry, leaves the PID file as
// the only evidence.
func checkLockOwner(f *os.File, pid int) error {
	owners, ok := kernelLockOwners(f)
	if !ok || len(owners) == 0 {
		return nil
	}
	for _, o := range owners {
		if o == pid {
			return nil
		}
	}
	return fmt.Errorf("the kernel lists pid(s) %v", owners)
}

// kernelLockOwners lists the PIDs that /proc/locks says hold a flock on f (ok is false when
// /proc/locks is unreadable).
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
