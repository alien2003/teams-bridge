//go:build darwin

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLaunchdRoundTrip installs a LaunchAgent tbd-test-<random> with the real install code into
// the user's GUI domain, its plist in a temporary directory (never ~/Library/LaunchAgents), and the
// job's program a copy of this test binary (launchdProbe, see TestMain) under a path with a blank.
// It checks the launchd semantics the daemon relies on: RunAtLoad with the exact argv, no restart
// after a clean exit or SIGTERM, a restart after exit 1 and after SIGKILL, a re-install while it
// runs (the unload wait), a bootout that stays stopped, StartCommands, and the uninstall. Like
// cmd/tbd's TestBinaryLaunchAgent it runs only on CI (GITHUB_ACTIONS=true) or with
// TBD_TEST_LAUNCHD=1, as it loads a real job into the user's session; it skips without a GUI
// domain (no one logged in at the screen), or fails with TBD_REQUIRE_LAUNCHD=1.
func TestLaunchdRoundTrip(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" && os.Getenv("TBD_TEST_LAUNCHD") != "1" {
		t.Skip("loads a launchd job tbd-test-* into the GUI domain: CI only, or TBD_TEST_LAUNCHD=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r := ExecRunner{}
	env := DefaultEnv()
	domain, err := launchdDomain(env)
	if err != nil {
		t.Skip(err)
	}
	if out, err := r.Run(ctx, "launchctl", "print", domain); err != nil {
		msg := fmt.Sprintf("launchctl print %s: %v: %s", domain, err, firstLine(out))
		if os.Getenv("TBD_REQUIRE_LAUNCHD") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg + "; skipping the launchd round trip")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	realAgents := filepath.Join(userHome, "Library", "LaunchAgents")

	tmp := t.TempDir()
	env.LaunchAgentsDir = filepath.Join(tmp, "LaunchAgents")
	name := testName(t)
	// The plist must never reach ~/Library/LaunchAgents. Only this job's name is checked there:
	// cmd/tbd's TestBinaryLaunchAgent may write and remove its own tbd-test-* plist meanwhile.
	inRealAgents := func(when string) {
		t.Helper()
		if m, _ := filepath.Glob(filepath.Join(realAgents, name+"*")); len(m) > 0 {
			t.Errorf("%s: %q in %s", when, m, realAgents)
		}
	}
	inRealAgents("before install")
	target := domain + "/" + name
	exe := copyTestBinary(t, filepath.Join(tmp, "bin dir"))
	home := filepath.Join(tmp, launchdProbeMarker+" home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: name, Exe: exe, Home: home, ExtraArgs: []string{"--log-file", filepath.Join(home, "tbd.log")}}
	setMode := func(m string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, "mode"), []byte(m), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	type start struct {
		pid  int
		argv []string
	}
	record := func() (starts []start, terms map[int]bool) {
		terms = map[int]bool{}
		b, _ := os.ReadFile(filepath.Join(home, probeStartsFile))
		for _, ln := range strings.Split(string(b), "\n") {
			f := strings.SplitN(ln, " ", 3)
			switch {
			case len(f) == 3 && f[0] == "start":
				s := start{}
				s.pid, _ = strconv.Atoi(f[1])
				_ = json.Unmarshal([]byte(f[2]), &s.argv)
				starts = append(starts, s)
			case len(f) == 2 && f[0] == "term":
				pid, _ := strconv.Atoi(f[1])
				terms[pid] = true
			}
		}
		return starts, terms
	}
	waitStarts := func(n int, within time.Duration) start {
		t.Helper()
		for deadline := time.Now().Add(within); ; time.Sleep(100 * time.Millisecond) {
			if s, _ := record(); len(s) >= n {
				if len(s) > n {
					t.Fatalf("%d starts, want %d", len(s), n)
				}
				return s[n-1]
			}
			if time.Now().After(deadline) {
				s, _ := record()
				t.Fatalf("start %d did not happen within %s (%d starts)", n, within, len(s))
			}
		}
	}
	// noRestart waits longer than ThrottleInterval and expects still n starts.
	noRestart := func(n int, why string) {
		t.Helper()
		time.Sleep((LaunchdThrottle + 4) * time.Second)
		if s, _ := record(); len(s) != n {
			t.Fatalf("%s: %d starts, want %d (launchd restarted the job)", why, len(s), n)
		}
	}
	alive := func(pid int) bool { return syscall.Kill(pid, 0) == nil }
	waitExit := func(pid int) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); alive(pid); time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("probe pid %d still running", pid)
			}
		}
	}
	kickstart := func() {
		t.Helper()
		if out, err := r.Run(ctx, "launchctl", "kickstart", target); err != nil {
			t.Fatalf("launchctl kickstart: %v: %s", err, out)
		}
	}
	apply := func(what string, p Plan) {
		t.Helper()
		var log bytes.Buffer
		if err := Apply(ctx, p, r, &log, false); err != nil {
			t.Fatalf("%s: %v\n%s", what, err, log.String())
		}
		t.Logf("%s:\n%s", what, log.String())
	}

	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if t.Failed() {
			out, _ := r.Run(cctx, "launchctl", "print", target)
			t.Logf("launchctl print %s:\n%s", target, out)
			if job := parseLaunchdPrint(out); job.LastExit != "" && job.LastExit != "0" {
				if code, err := strconv.Atoi(job.LastExit); err == nil {
					e, _ := r.Run(cctx, "launchctl", "error", strconv.Itoa(code))
					t.Logf("launchctl error %d: %s", code, strings.TrimSpace(e))
				}
			}
			logs, _ := r.Run(cctx, "log", "show", "--last", "3m", "--style", "compact", "--predicate", `eventMessage CONTAINS "`+name+`"`)
			t.Logf("log show (%s):\n%s", name, tail(logs, 80))
			b, _ := os.ReadFile(filepath.Join(home, probeStartsFile))
			t.Logf("%s:\n%s", probeStartsFile, b)
		}
		if _, err := r.Run(cctx, "launchctl", "print", target); err == nil {
			t.Errorf("cleanup: %s still loaded; booting it out", target)
			if err := unloadJob(cctx, r, "darwin", target, func(s string) { t.Log(s) }); err != nil {
				t.Errorf("cleanup: %v; remove it with: launchctl bootout %s", err, target)
			}
		}
		if p, err := PlistFile(spec, env); err == nil {
			_ = os.Remove(p)
		}
		starts, _ := record()
		for _, s := range starts {
			if s.pid > 0 && alive(s.pid) {
				t.Errorf("cleanup: probe pid %d left running; killing it", s.pid)
				_ = syscall.Kill(s.pid, syscall.SIGKILL)
			}
		}
		inRealAgents("after the test")
	})

	// 1. Install: RunAtLoad starts the job with exactly the plist's argv.
	setMode("serve")
	plan, err := InstallPlan(spec, "darwin", env)
	if err != nil {
		t.Fatal(err)
	}
	plist := plan.Files[0].Path
	if !strings.HasPrefix(plist, env.LaunchAgentsDir+"/") {
		t.Fatalf("plist %s outside %s", plist, env.LaunchAgentsDir)
	}
	apply("install", plan)
	s1 := waitStarts(1, 30*time.Second)
	inRealAgents("after install")
	if want := spec.Args(); strings.Join(s1.argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv through launchd = %q, want %q", s1.argv, want)
	}
	if out, err := exec.CommandContext(ctx, "plutil", "-lint", plist).CombinedOutput(); err != nil {
		t.Errorf("plutil -lint: %v: %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "plutil", "-convert", "json", "-o", "-", plist).Output(); err != nil {
		t.Errorf("plutil -convert json: %v", err)
	} else {
		var pl struct {
			Label            string
			ProgramArguments []string
			KeepAlive        map[string]bool
			ThrottleInterval int
			ExitTimeOut      int
		}
		if err := json.Unmarshal(out, &pl); err != nil {
			t.Errorf("plist as JSON: %v\n%s", err, out)
		}
		want := append([]string{exe}, spec.Args()...)
		if pl.Label != name || strings.Join(pl.ProgramArguments, "\x00") != strings.Join(want, "\x00") ||
			pl.KeepAlive["SuccessfulExit"] || pl.ThrottleInterval != LaunchdThrottle || pl.ExitTimeOut != LaunchdExitTimeout {
			t.Errorf("plist as launchd reads it: %+v", pl)
		}
	}
	job, err := QueryLaunchd(ctx, r, name, env)
	if err != nil || !job.Loaded || job.PID != s1.pid {
		t.Fatalf("after install: %+v %v, want pid %d", job, err, s1.pid)
	}
	t.Logf("launchctl print: %+v", job)

	// 2. SIGTERM → exit 0 → stays stopped (what `tbd stop` relies on when it signals directly).
	if err := syscall.Kill(s1.pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitExit(s1.pid)
	noRestart(1, "after SIGTERM and exit 0")
	if _, terms := record(); !terms[s1.pid] {
		t.Error("the probe did not see SIGTERM")
	}

	// 3. exit 1 → restarted after ThrottleInterval.
	setMode("exit1")
	kickstart()
	s2 := waitStarts(2, 15*time.Second)
	waitExit(s2.pid) // it has read its mode: the next start may serve
	setMode("serve")
	s3 := waitStarts(3, 3*LaunchdThrottle*time.Second)

	// 4. SIGKILL (a crash) → restarted.
	if err := syscall.Kill(s3.pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	s4 := waitStarts(4, 3*LaunchdThrottle*time.Second)

	// 5. Re-install while it runs: the old job is booted out (SIGTERM) and gone before bootstrap.
	plan, _ = InstallPlan(spec, "darwin", env)
	apply("re-install", plan)
	waitStarts(5, 30*time.Second)
	if _, terms := record(); !terms[s4.pid] {
		t.Error("re-install did not stop the running job with SIGTERM")
	}

	// 6. A clean exit 0 by itself → not restarted.
	s5, _ := record()
	if err := syscall.Kill(s5[4].pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitExit(s5[4].pid)
	setMode("exit0")
	kickstart()
	waitStarts(6, 15*time.Second)
	noRestart(6, "after exit 0")

	// 7. bootout (`tbd stop` of the launchd job) → SIGTERM, unloaded, never respawned.
	setMode("serve")
	kickstart()
	s7 := waitStarts(7, 15*time.Second)
	c := LaunchdBootout(target)
	if out, err := r.Run(ctx, c[0], c[1:]...); err != nil {
		t.Logf("bootout: %v: %s (advisory)", err, out)
	}
	waitExit(s7.pid)
	if _, terms := record(); !terms[s7.pid] {
		t.Error("bootout did not send SIGTERM")
	}
	if job, err := QueryLaunchd(ctx, r, name, env); err != nil || job.Loaded {
		t.Errorf("after bootout: %+v %v", job, err)
	}
	noRestart(7, "after bootout")

	// 8. StartCommands on an unloaded job (bootstrap; no enable, as it is not disabled), then
	// uninstall.
	cs, err := StartCommands(ctx, r, name, "darwin", env)
	if err != nil || len(cs) != 1 || cs[0][1] != "bootstrap" {
		t.Fatalf("StartCommands: %q %v", cs, err)
	}
	for _, c := range cs {
		if out, err := r.Run(ctx, c[0], c[1:]...); err != nil {
			t.Fatalf("%v: %v: %s", c, err, out)
		}
	}
	s8 := waitStarts(8, 30*time.Second)
	un, err := UninstallPlan(spec, "darwin", env)
	if err != nil {
		t.Fatal(err)
	}
	apply("uninstall", un)
	waitExit(s8.pid)
	if job, err := QueryLaunchd(ctx, r, name, env); err != nil || job.Loaded {
		t.Errorf("after uninstall: %+v %v", job, err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Errorf("plist still there: %v", err)
	}
	if err := CheckInstalled(ctx, r, name, "darwin", env); err == nil {
		t.Error("CheckInstalled after uninstall")
	}
	// Nothing ran `launchctl enable`, so launchd keeps no override for the label (D26).
	if out, err := r.Run(ctx, "launchctl", "print-disabled", domain); err != nil {
		t.Errorf("launchctl print-disabled: %v: %s", err, firstLine(out))
	} else if strings.Contains(out, `"`+name+`"`) {
		t.Errorf("launchd keeps an override for %s:\n%s", name, out)
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
