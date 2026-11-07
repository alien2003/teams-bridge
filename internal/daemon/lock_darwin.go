package daemon

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// sZomb is SZOMB of <sys/proc.h>: a process that has exited and not been reaped.
const sZomb = 5

// checkLockOwner checks that pid is a plausible holder of f's lock (holderPlausible): alive, not a
// zombie, and started no later than the lock file was written. kern.proc.pid answers for any
// process of any user, and needs no lsof.
func checkLockOwner(f *os.File, pid int) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return holderPlausible(false, time.Time{}, false, st.ModTime())
	}
	tv := kp.Proc.P_starttime
	started := time.Unix(int64(tv.Sec), int64(tv.Usec)*1000)
	return holderPlausible(true, started, kp.Proc.P_stat == sZomb, st.ModTime())
}
