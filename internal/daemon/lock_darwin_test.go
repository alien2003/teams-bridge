package daemon

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLockOwnerDarwin runs holderPlausible on real processes (macOS runners): the holder itself is
// accepted; a process started after the lock file was written, a dead PID and a zombie are not.
func TestLockOwnerDarwin(t *testing.T) {
	home := t.TempDir()
	l, err := AcquireLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := checkLockOwner(l.f, os.Getpid()); err != nil {
		t.Fatalf("own pid: %v", err)
	}
	if pid, err := LockHolder(home); pid != os.Getpid() || err != nil {
		t.Fatalf("LockHolder: %d %v", pid, err)
	}

	// A process younger than the PID file: what a reused PID looks like.
	time.Sleep(1500 * time.Millisecond)
	younger := exec.Command("/bin/sleep", "30")
	if err := younger.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = younger.Process.Kill(); _ = younger.Wait() }()
	if err := checkLockOwner(l.f, younger.Process.Pid); err == nil || !strings.Contains(err.Error(), "reused pid") {
		t.Errorf("younger process: %v", err)
	}

	// Dead.
	dead := exec.Command("/usr/bin/true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	if err := checkLockOwner(l.f, dead.Process.Pid); err == nil || !strings.Contains(err.Error(), "no such process") {
		t.Errorf("dead pid: %v", err)
	}

	// A zombie (exited, not yet waited for): touch the file first so that only the zombie check
	// can reject it.
	now := time.Now().Add(time.Minute)
	if err := os.Chtimes(LockPath(home), now, now); err != nil {
		t.Fatal(err)
	}
	zombie := exec.Command("/usr/bin/true")
	if err := zombie.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zombie.Wait() }()
	time.Sleep(500 * time.Millisecond)
	if err := checkLockOwner(l.f, zombie.Process.Pid); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Errorf("zombie: %v", err)
	}
}
