//go:build !windows

package browser

import (
	"log/slog"
	"os/exec"
	"syscall"
	"time"
)

// configureSysProcAttr puts the browser in its own process group, so the whole tree (Chromium's
// zygote, GPU and utility children) can be signalled at once, and on Linux asks the kernel to kill
// the browser when the daemon dies (Pdeathsig), so a crashed daemon leaves no orphan holding the
// profile.
func configureSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setPdeathsig(cmd.SysProcAttr)
}

// platformLaunchArgs are the extra switches for this OS. Without a GPU (headless, WSL) the GPU
// probe only logs errors and slows the start. The sandbox stays on: see LaunchOptions and
// docs/DECISIONS.md.
func platformLaunchArgs(headless bool) []string {
	if headless {
		return []string{"--disable-gpu"}
	}
	return nil
}

// procTree is the browser's process group. Setpgid made the browser the group leader, so the
// group id is its PID, also after the leader has exited.
type procTree struct {
	cmd *exec.Cmd
	pid int
}

func newProcTree(cmd *exec.Cmd, _ *slog.Logger) *procTree {
	return &procTree{cmd: cmd, pid: cmd.Process.Pid}
}

// kill SIGKILLs the whole process group, falling back to the process alone.
func (t *procTree) kill() {
	if err := syscall.Kill(-t.pid, syscall.SIGKILL); err != nil {
		_ = t.cmd.Process.Kill()
	}
}

// sweep runs after the leader exited: children normally follow within milliseconds; whatever is
// left of the group after a short wait is killed.
func (t *procTree) sweep() {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(-t.pid, 0) != nil {
			return // the group is gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-t.pid, syscall.SIGKILL)
}

// release frees OS resources; a process group needs none.
func (t *procTree) release() {}

// browserTempKeys is empty: the browser keeps the caller's TMPDIR. Chromium puts its singleton
// socket under it, and a deep TMPDIR (such as <home>/tmp in a test's temp dir) overflows the
// 108-byte socket path limit, which makes the browser abort at start.
var browserTempKeys []string
