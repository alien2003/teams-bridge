package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrLocked means another tbd process holds the data directory.
var ErrLocked = errors.New("another tbd process is using this data directory")

// Lock is an exclusive, advisory lock on <home>/tbd.lock, released when the process exits.
type Lock struct{ f *os.File }

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
		pid := readPID(LockPath(home))
		if pid != "" {
			return nil, fmt.Errorf("%w (pid %s)", ErrLocked, pid)
		}
		return nil, ErrLocked
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
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
