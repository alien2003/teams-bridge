package daemon

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The kernel's view must be the one LockHolder cross-checks.
func TestKernelLockOwners(t *testing.T) {
	l, err := AcquireLock(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	owners, ok := kernelLockOwners(l.f)
	if !ok || fmt.Sprint(owners) != fmt.Sprint([]int{os.Getpid()}) {
		t.Errorf("/proc/locks owners = %v (%v), want [%d]", owners, ok, os.Getpid())
	}
	if err := checkLockOwner(l.f, os.Getppid()); err == nil || !strings.Contains(err.Error(), "the kernel lists pid(s)") {
		t.Errorf("parent pid accepted: %v", err)
	}
}
