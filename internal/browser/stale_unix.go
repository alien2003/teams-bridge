//go:build !windows

package browser

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// staleGrace is how long killStaleBrowser waits after SIGTERM before it SIGKILLs the group.
const staleGrace = 5 * time.Second

// killStaleBrowser ends a browser that an earlier tbd left running on profile, so that Launch can
// start a new one there. On macOS there is no Pdeathsig: a daemon that crashed or was SIGKILLed
// leaves its headless browser holding the profile, the next browser hands over to it through
// Chromium's process singleton and exits, Launch fails ("exited before the DevTools endpoint
// appeared"), and under launchd's KeepAlive that would repeat forever. On Linux Pdeathsig makes
// this rare, but the check is the same.
//
// The process named by <profile>/SingletonLock (a symlink to "<host>-<pid>") is ended only if it is
// alive, leads its own process group (Launch starts the browser with Setpgid) and was started with
// exactly --user-data-dir=<profile>. tbd.lock guarantees that no other tbd uses this home, so such a
// process is ours. The group gets SIGTERM, then SIGKILL after staleGrace.
func killStaleBrowser(profile string, log *slog.Logger) {
	pid, ok := staleBrowserPID(profile, procArgs)
	if !ok {
		return
	}
	if err := endGroup(pid, staleGrace); err != nil {
		log.Warn("could not end a browser left by a previous tbd", "pid", pid, "err", err)
		return
	}
	log.Info("killed a browser left by a previous tbd", "pid", pid)
}

// staleBrowserPID returns the PID of a browser that still holds profile (see killStaleBrowser);
// args reads a process's argv.
func staleBrowserPID(profile string, args func(pid int) ([]string, error)) (int, bool) {
	target, err := os.Readlink(filepath.Join(profile, "SingletonLock"))
	if err != nil {
		return 0, false
	}
	pid, ok := lockPID(target)
	if !ok || pid == os.Getpid() {
		return 0, false
	}
	if syscall.Kill(pid, 0) != nil {
		return 0, false // gone (a dangling lock), or not ours to signal (EPERM)
	}
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid {
		return 0, false
	}
	argv, err := args(pid)
	if err != nil {
		return 0, false
	}
	want := "--user-data-dir=" + profile
	for _, a := range argv {
		if a == want {
			return pid, true
		}
	}
	return 0, false
}

// endGroup sends SIGTERM to the process group pgid, waits up to grace for it to go, then sends
// SIGKILL and waits a little longer.
func endGroup(pgid int, grace time.Duration) error {
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	if groupGone(pgid, grace) {
		return nil
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	if groupGone(pgid, 2*time.Second) {
		return nil
	}
	return errors.New("the process group is still there after SIGKILL")
}

// groupGone polls until no process of group pgid is left, for at most d.
func groupGone(pgid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
