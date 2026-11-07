package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrLocked means another tbd process holds the data directory.
var ErrLocked = errors.New("another tbd process is using this data directory")

// LockedError is what AcquireLock returns while another process holds the lock. Its message names
// the fix, since every command that needs the data directory (run, login, …) refuses with it.
type LockedError struct{ PID string }

func (e *LockedError) Error() string {
	who := "tbd is already running for this data directory"
	if e.PID != "" {
		who += " (pid " + e.PID + ")"
	}
	return who + ": run `tbd stop`, then this command again (`tbd start` starts the daemon again)"
}

// Is makes errors.Is(err, ErrLocked) hold.
func (e *LockedError) Is(target error) bool { return target == ErrLocked }

// Lock is an exclusive, advisory lock on <home>/tbd.lock, released when the process exits. On
// Windows the holder also listens for `tbd stop` (see StopRequested).
type Lock struct {
	f    *os.File
	stop *stopListener // nil where stopping is a signal (Unix), or when the listener failed
	// StopErr is why the Windows stop listener could not be set up (then only a forced stop works).
	StopErr error
}

// LockPath is the lock file inside home.
func LockPath(home string) string { return filepath.Join(home, "tbd.lock") }

// AcquireLock takes the single-instance lock: two daemons (or a daemon and `tbd login`) on one
// browser profile would fight over it.
func AcquireLock(home string) (*Lock, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(LockPath(home), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, &LockedError{PID: readPID(LockPath(home))}
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	l := &Lock{f: f}
	l.stop, l.StopErr = listenStop(f)
	return l, nil
}

// StopRequested is closed when `tbd stop` asks the holder to stop (Windows: a named event bound to
// this lock file). It is nil on Unix, where `tbd stop` sends SIGTERM, which the signal context
// already handles; a nil channel never fires in a select.
func (l *Lock) StopRequested() <-chan struct{} {
	if l == nil || l.stop == nil {
		return nil
	}
	return l.stop.done
}

// Release drops the lock.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	if l.stop != nil {
		l.stop.close()
		l.stop = nil
	}
	unlockFile(l.f)
	l.f.Close()
	l.f = nil
}

// Held reports whether some process currently holds the lock (used by `tbd status`).
func Held(home string) (bool, string) {
	f, err := os.OpenFile(LockPath(home), os.O_RDWR, 0)
	if err != nil {
		return false, ""
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return true, readPID(LockPath(home))
	}
	unlockFile(f)
	return false, ""
}

func readPID(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// holderSettle is how long the lock must stay held, by the same PID, before LockHolder believes the
// PID file. `tbd status` (Held) and LockHolder itself take the lock for a moment to test it, and
// during that moment the file still names the previous holder, maybe a PID reused since: a single
// failed try-lock is no proof that the PID in the file holds it.
const holderSettle = 150 * time.Millisecond

// ErrHolderUnknown means the lock is held but its holder could not be identified with certainty.
var ErrHolderUnknown = errors.New("cannot tell which process holds the lock")

// LockHolder returns the PID of the process that holds the lock, or 0 when nobody does. The PID
// comes from the lock file and is trusted only when (1) two try-locks holderSettle apart both fail
// with the same PID in the file and (2) the OS check agrees (checkLockOwner): on Linux /proc/locks
// lists a lock on that file's inode by that PID (when it lists any lock on it); on macOS, whose
// kernel names no flock owner, the PID is alive and its process started no later than the PID
// file was written (holderPlausible). Anything less is ErrHolderUnknown: `tbd stop` must never
// signal a process that does not hold the lock.
func LockHolder(home string) (int, error) { return lockHolder(home, holderSettle) }

func lockHolder(home string, settle time.Duration) (int, error) {
	f, err := os.OpenFile(LockPath(home), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	free := func() bool {
		if lockFile(f) != nil {
			return false
		}
		unlockFile(f)
		return true
	}
	for range 5 {
		if free() {
			return 0, nil
		}
		pid := parsePID(readPID(LockPath(home)))
		time.Sleep(settle)
		if free() {
			return 0, nil // a momentary probe, not a holder
		}
		if pid == 0 || parsePID(readPID(LockPath(home))) != pid {
			continue // being written, or a new holder: look again
		}
		if err := checkLockOwner(f, pid); err != nil {
			return 0, fmt.Errorf("%w: %s names pid %d, but %v", ErrHolderUnknown, LockPath(home), pid, err)
		}
		return pid, nil
	}
	return 0, fmt.Errorf("%w: %s does not name a stable pid", ErrHolderUnknown, LockPath(home))
}

// holderPlausible is macOS's check of the PID a settled lock file names (the kernel does not
// expose flock owners there: F_GETLK reports pid -1 for them). The holder writes its PID right
// after taking the lock, so it started before the file was written; a process that started later
// merely reused the PID of a holder that is gone. A second of slack covers clock granularity.
// written is the lock file's modification time.
func holderPlausible(alive bool, started time.Time, zombie bool, written time.Time) error {
	switch {
	case !alive:
		return errors.New("no such process")
	case zombie:
		return errors.New("that process has exited")
	case started.IsZero() || written.IsZero():
		return errors.New("its start time is unknown")
	case started.After(written.Add(time.Second)):
		return fmt.Errorf("that process started %s after the file was written (a reused pid)", started.Sub(written).Round(time.Millisecond))
	}
	return nil
}

func parsePID(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// parseProcLocks returns the PIDs that hold a FLOCK on inode ino, from the text of /proc/locks
// ("1: FLOCK  ADVISORY  WRITE 1234 08:20:5678 0 EOF"; waiters are marked "->" and skipped). The
// device is ignored on purpose: on btrfs and overlayfs stat's st_dev differs from the device
// /proc/locks prints, and a matching inode on another device can only add PIDs, not hide ours.
func parseProcLocks(text string, ino uint64) []int {
	var pids []int
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 6 || f[1] == "->" || f[1] != "FLOCK" {
			continue
		}
		id := strings.Split(f[5], ":")
		if len(id) != 3 {
			continue
		}
		if n, err := strconv.ParseUint(id[2], 10, 64); err != nil || n != ino {
			continue
		}
		if pid, err := strconv.Atoi(f[4]); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// stopListener is the Windows stop event's watcher: done closes when the event is set; close
// stops watching and frees the handles.
type stopListener struct {
	done  chan struct{}
	close func()
}
