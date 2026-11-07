//go:build darwin

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/browser"
	"github.com/alien2003/teams-bridge/internal/daemon"
	"github.com/alien2003/teams-bridge/internal/service"
)

// TestBinaryLaunchAgent drives the real commands against a real LaunchAgent on a macOS runner:
// `tbd install` (with TBD_TEST_SERVICE_NAME=tbd-test-<random>, so it writes
// ~/Library/LaunchAgents/tbd-test-….plist and never touches a job named tbd) runs the daemon
// under launchd against faketeams, the FCM mock and the runner's browser; `tbd status` shows the
// job; `tbd stop` boots it out gracefully (daemon_stopping) and it stays stopped; `tbd start`
// bootstraps it again; a SIGKILLed daemon is restarted by launchd and gets its profile back (from
// a browser the old one left behind, if launchd did not end it); `tbd uninstall` removes it all. It runs only on CI
// (GITHUB_ACTIONS=true) or with TBD_TEST_LAUNCHD=1, as it writes into ~/Library/LaunchAgents.
func TestBinaryLaunchAgent(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" && os.Getenv("TBD_TEST_LAUNCHD") != "1" {
		t.Skip("writes ~/Library/LaunchAgents/tbd-test-*.plist: CI only, or TBD_TEST_LAUNCHD=1")
	}
	exe := tbdExe(t)
	needBrowser(t)
	r := service.ExecRunner{}
	senv := service.DefaultEnv()
	domain := "gui/" + strconv.Itoa(senv.UID)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if out, err := r.Run(ctx, "launchctl", "print", domain); err != nil {
		if os.Getenv("TBD_REQUIRE_LAUNCHD") == "1" {
			t.Fatalf("launchctl print %s: %v: %.200s", domain, err, out)
		}
		t.Skipf("no GUI domain %s: %v", domain, err)
	}
	_, srv, home := fixture(t)
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := "tbd-test-" + hex.EncodeToString(b)
	target := domain + "/" + name
	plist, err := service.PlistFile(service.Spec{Name: name}, senv)
	if err != nil {
		t.Fatal(err)
	}
	env := append(withoutEnv(os.Environ(), "TBD_BROWSER", "TBD_HOME", service.TestNameEnv), service.TestNameEnv+"="+name)
	tbd := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, exe, append([]string{"--home", home}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	logFile := daemon.LogFile(home)
	readLog := func() string { b, _ := os.ReadFile(logFile); return string(b) }
	attached := func(n int) bool { return strings.Count(readLog(), `msg="session attached"`) >= n }
	profile := browser.ProfileDir(home)
	// "--" ends pgrep's and pkill's options: the pattern itself starts with "--". Exit status 1 is
	// "no such process"; anything else (a bad invocation) fails the test instead of passing as none.
	pattern := regexp.QuoteMeta("--user-data-dir=" + profile)
	browserLeft := func() bool {
		err := exec.Command("pgrep", "-f", "--", pattern).Run()
		var ee *exec.ExitError
		switch {
		case err == nil:
			return true
		case errors.As(err, &ee) && ee.ExitCode() == 1:
			return false
		}
		t.Fatalf("pgrep -f -- %s: %v", pattern, err)
		return false
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := r.Run(context.Background(), "launchctl", "print", target)
			stderr, _ := os.ReadFile(filepath.Join(home, service.StderrLog))
			t.Logf("launchctl print %s:\n%s\n%s:\n%s\n%s:\n%s", target, out, service.StderrLog, stderr, logFile, readLog())
		}
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		un, err := service.UninstallPlan(service.Spec{Name: name}, "darwin", senv)
		if err == nil {
			err = service.Apply(cctx, un, r, io.Discard, false)
		}
		if _, perr := r.Run(cctx, "launchctl", "print", target); perr == nil {
			t.Errorf("%s still loaded (%v); remove it with: launchctl bootout %s", target, err, target)
		}
		if _, serr := os.Stat(plist); serr == nil {
			t.Errorf("%s left behind", plist)
			_ = os.Remove(plist)
		}
		if held, _ := daemon.Held(home); held {
			_ = exec.Command(exe, "--home", home, "stop", "--timeout", "5s").Run()
		}
		if waitUntil(15*time.Second, func() bool { return !browserLeft() }); browserLeft() {
			t.Errorf("a browser on %s is still running", profile)
			_ = exec.Command("pkill", "-KILL", "-f", "--", pattern).Run()
		}
	})

	// 1. install: the LaunchAgent runs the daemon, which attaches.
	if out, err := tbd("install"); err != nil || !strings.Contains(out, "launchctl bootstrap "+domain) {
		t.Fatalf("tbd install: %v\n%s", err, out)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(120*time.Second, func() bool { return attached(1) }) {
		t.Fatalf("the LaunchAgent's daemon did not attach\n%s", readLog())
	}
	_, pid := daemon.Held(home)
	job, err := service.QueryLaunchd(ctx, r, name, senv)
	if err != nil || strconv.Itoa(job.PID) != pid {
		t.Fatalf("launchd job %+v (%v), lock holder %s", job, err, pid)
	}

	// 2. status shows the job.
	if out, err := tbd("status"); err != nil || !strings.Contains(out, "launchd:") || !strings.Contains(out, target+" loaded, running (pid "+pid+")") {
		t.Errorf("tbd status: %v\n%s", err, out)
	}

	// 3. stop: through launchd, graceful, and the job stays stopped.
	out, err := tbd("stop")
	if err != nil || !strings.Contains(out, "launchctl bootout "+target) || !strings.Contains(out, "Stopped after") {
		t.Fatalf("tbd stop: %v\n%s\n%s", err, out, readLog())
	}
	if !waitUntil(10*time.Second, func() bool { return countStopping(t, srv) == 1 }) {
		t.Errorf("%d daemon_stopping pushes, want 1", countStopping(t, srv))
	}
	time.Sleep((service.LaunchdThrottle + 5) * time.Second)
	if held, p := daemon.Held(home); held {
		t.Fatalf("launchd started the daemon again after tbd stop (pid %s)", p)
	}
	if job, err := service.QueryLaunchd(ctx, r, name, senv); err != nil || job.Loaded {
		t.Errorf("after tbd stop: %+v %v", job, err)
	}
	if out, err := tbd("status"); err != nil || !strings.Contains(out, "installed, not loaded (tbd start)") {
		t.Errorf("tbd status after stop: %v\n%s", err, out)
	}

	// 4. start: bootstrapped again.
	if out, err := tbd("start"); err != nil || !strings.Contains(out, "launchctl bootstrap "+domain) || !strings.Contains(out, "Started (pid") {
		t.Fatalf("tbd start: %v\n%s\n%s", err, out, readLog())
	}
	if !waitUntil(90*time.Second, func() bool { return attached(2) }) {
		t.Fatalf("the daemon did not attach after tbd start\n%s", readLog())
	}

	// 5. A crash (SIGKILL): launchd restarts it; the new daemon ends the browser left behind.
	_, pid = daemon.Held(home)
	n, _ := strconv.Atoi(pid)
	if n <= 0 {
		t.Fatalf("lock holder %q", pid)
	}
	if err := syscall.Kill(n, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(120*time.Second, func() bool { return attached(3) }) {
		t.Fatalf("launchd did not restart the killed daemon\n%s", readLog())
	}
	// Measured for D26: whether the old browser outlived the daemon (then the new one must have
	// ended it) or launchd ended it with the job. Either way the new daemon got its profile.
	if strings.Contains(readLog(), "killed a browser left by a previous tbd") {
		t.Log("the restarted daemon ended the browser the killed one left behind")
	} else {
		t.Log("no stale browser to end: launchd took it down with the killed job")
	}

	// 6. uninstall: unloaded (graceful stop), plist gone, no browser left.
	if out, err := tbd("uninstall"); err != nil || !strings.Contains(out, "launchctl bootout "+target) {
		t.Fatalf("tbd uninstall: %v\n%s", err, out)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Errorf("plist after uninstall: %v", err)
	}
	if _, err := r.Run(ctx, "launchctl", "print", target); err == nil {
		t.Error("job still loaded after uninstall")
	}
	if held, p := daemon.Held(home); held {
		t.Errorf("daemon (pid %s) still running after uninstall", p)
	}
	if !waitUntil(20*time.Second, func() bool { return !browserLeft() }) {
		t.Error("browser still running after uninstall")
	}
	if !waitUntil(10*time.Second, func() bool { return countStopping(t, srv) >= 2 }) {
		t.Errorf("%d daemon_stopping pushes, want 2 (stop, uninstall)", countStopping(t, srv))
	}
	// install and start ran no `launchctl enable` (the job was never disabled), so launchd keeps
	// no lasting override for the label (D26).
	if out, err := r.Run(ctx, "launchctl", "print-disabled", domain); err != nil {
		t.Errorf("launchctl print-disabled %s: %v: %.200s", domain, err, out)
	} else if strings.Contains(out, `"`+name+`"`) {
		t.Errorf("launchd keeps an override for %s:\n%s", name, out)
	}
}
