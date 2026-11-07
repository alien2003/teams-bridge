//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/daemon"
)

// hupIgnored skips a test when this process was started with SIGHUP ignored: so would be every
// child it starts. (Never signal.Ignore here: the runtime cannot restore the default afterwards,
// and every later child would inherit SIG_IGN.) The nohup case is TestBinarySIGHUP's second part.
func hupIgnored(t *testing.T) {
	t.Helper()
	if signal.Ignored(syscall.SIGHUP) {
		t.Skip("the test process was started with SIGHUP ignored")
	}
}

// TestStopSignals: SIGHUP stops tbd like SIGINT and SIGTERM.
func TestStopSignals(t *testing.T) {
	hupIgnored(t)
	if !slices.Contains(stopSignals(), os.Signal(syscall.SIGHUP)) {
		t.Errorf("SIGHUP missing: %v", stopSignals())
	}
}

var browserPID = regexp.MustCompile(`msg="browser started" pid=([0-9]+)`)

// TestBinarySIGHUP: a closed Terminal window or a dropped SSH session (SIGHUP) shuts `tbd run`
// down gracefully: daemon_stopping is pushed, it exits 0, and the browser's process group is gone.
// On macOS nothing else would end that browser (no Pdeathsig). Started with SIGHUP ignored, as by
// nohup, tbd keeps running.
func TestBinarySIGHUP(t *testing.T) {
	hupIgnored(t)
	exe := tbdExe(t)
	needBrowser(t)
	_, srv, home := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	start := func(ignoreHUP bool) (*exec.Cmd, *syncBuffer, <-chan error) {
		t.Helper()
		args := []string{"--home", home, "run"}
		cmd := exec.CommandContext(ctx, exe, args...)
		if ignoreHUP {
			cmd = exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", `trap "" HUP; exec "$0" "$@"`, exe}, args...)...)
		}
		cmd.Env = withoutEnv(os.Environ(), "TBD_BROWSER", "TBD_HOME")
		logs := &syncBuffer{}
		cmd.Stdout, cmd.Stderr = logs, logs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		if !waitUntil(60*time.Second, func() bool { return strings.Contains(logs.String(), `msg="session attached"`) }) {
			t.Fatalf("the daemon did not attach\n%s", logs.String())
		}
		return cmd, logs, exited
	}
	groupGone := func(logs string) bool {
		m := browserPID.FindStringSubmatch(logs)
		if m == nil {
			t.Fatalf("no browser pid in the log\n%s", logs)
		}
		pgid, _ := strconv.Atoi(m[1])
		return waitUntil(15*time.Second, func() bool { return syscall.Kill(-pgid, 0) == syscall.ESRCH })
	}

	// 1. SIGHUP: a graceful stop.
	cmd, logs, exited := start(false)
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("tbd run exited with %v after SIGHUP\n%s", err, logs.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("tbd run still running after SIGHUP\n%s", logs.String())
	}
	l := logs.String()
	if !strings.Contains(l, `msg="tbd stopped"`) {
		t.Errorf("no clean shutdown in the log:\n%s", l)
	}
	if !waitUntil(5*time.Second, func() bool { return countStopping(t, srv) == 1 }) {
		t.Errorf("%d daemon_stopping pushes, want 1\n%s", countStopping(t, srv), l)
	}
	if !groupGone(l) {
		t.Errorf("the browser's process group outlived tbd\n%s", l)
	}
	if held, _ := daemon.Held(home); held {
		t.Error("lock still held")
	}

	// 2. Started with SIGHUP ignored (nohup): it keeps running; SIGTERM still stops it.
	cmd, logs, exited = start(true)
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		t.Fatalf("tbd run started with SIGHUP ignored exited (%v) on SIGHUP\n%s", err, logs.String())
	case <-time.After(3 * time.Second):
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("tbd run exited with %v after SIGTERM\n%s", err, logs.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("tbd run still running after SIGTERM\n%s", logs.String())
	}
	if !groupGone(logs.String()) {
		t.Errorf("the browser's process group outlived tbd\n%s", logs.String())
	}
}
