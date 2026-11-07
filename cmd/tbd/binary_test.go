package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/browser"
	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/daemon"
	"github.com/alien2003/teams-bridge/internal/fcm/fcmtest"
	"github.com/alien2003/teams-bridge/internal/proto"
	"github.com/alien2003/teams-bridge/internal/service"
	"github.com/alien2003/teams-bridge/test/faketeams"
)

// These tests drive the real, separately built binary: dist/tbd on Linux (mise run test sets
// TBD_EXE) and dist\tbd.exe on the Windows host (mise run test-win), BRIEF §7.

func tbdExe(t *testing.T) string {
	t.Helper()
	exe := os.Getenv("TBD_EXE")
	if exe == "" {
		name := "tbd"
		if runtime.GOOS == "windows" {
			name = "tbd.exe"
		}
		exe = filepath.Join("..", "..", "dist", name)
	}
	if _, err := os.Stat(exe); err != nil {
		if os.Getenv("TBD_REQUIRE_BROWSER") == "1" {
			t.Fatalf("binary %s missing (TBD_EXE): %v", exe, err)
		}
		t.Skipf("binary %s not built (set TBD_EXE): %v", exe, err)
	}
	abs, _ := filepath.Abs(exe)
	return abs
}

func needBrowser(t *testing.T) {
	t.Helper()
	if _, err := config.Default().DetectBrowser(); err != nil {
		if os.Getenv("TBD_REQUIRE_BROWSER") == "1" {
			t.Fatalf("no browser: %v", err)
		}
		t.Skipf("no browser: %v", err)
	}
}

// fixture: faketeams + FCM mock + a data dir with the vectors' secret and NO browser setting, so
// the binary must auto-detect it (msedge.exe on Windows).
func fixture(t *testing.T) (*faketeams.Server, *fcmtest.Server, string) {
	t.Helper()
	teams := faketeams.New()
	t.Cleanup(teams.Close)
	srv := fcmtest.NewServer(t, fcmtest.WithSecret(proto.VectorSecret))
	home := filepath.Join(t.TempDir(), "tbd home") // a space, like many Windows user names
	cfg := config.Default()
	cfg.Secret = proto.VectorSecret.Code()
	cfg.TeamsURL = teams.TeamsURL()
	cfg.LoginHosts = []string{teams.LoginHost()}
	cfg.APIHosts = []string{teams.APIHostPattern()}
	srv.SetupHome(home, cfg)
	return teams, srv, home
}

func TestBinaryRunOnce(t *testing.T) {
	exe := tbdExe(t)
	needBrowser(t)
	_, srv, home := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--home", home, "run", "--once", "--timeout", "60s")
	cmd.Env = withoutEnv(os.Environ(), "TBD_BROWSER", "TBD_HOME")
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	err := cmd.Run()
	logs := stderr.String()
	if err != nil {
		t.Fatalf("tbd run --once: %v\n%s", err, logs)
	}
	var got *proto.Message
	for _, m := range srv.Messages() {
		if m.DecryptErr != nil {
			t.Fatalf("undecryptable payload: %v", m.DecryptErr)
		}
		if m.Decrypted != nil && m.Decrypted.T == proto.TypeMsg {
			got = m.Decrypted
		}
	}
	if got == nil {
		t.Fatalf("no msg reached the FCM mock\n%s", logs)
	}
	t.Logf("delivered %q (tag %s, url %s)", got.Title, got.Tag, got.URL)
	if strings.Contains(logs, "Alice") || strings.Contains(logs, "from sw-") {
		t.Error("notification text in the info-level log")
	}
	if !strings.Contains(logs, `msg="browser started"`) {
		t.Errorf("no browser start in the log:\n%s", logs)
	}
	if runtime.GOOS == "windows" {
		// BRIEF §7: auto-detection found msedge.exe with no config; logs carry Windows paths.
		if !strings.Contains(logs, "source=windows-standard") || !strings.Contains(strings.ToLower(logs), `\msedge.exe`) {
			t.Errorf("msedge.exe was not auto-detected:\n%s", logs)
		}
		// slog quotes values with spaces (and then escapes the backslashes).
		if !strings.Contains(logs, "profile="+strconv.Quote(browser.ProfileDir(home))) || !strings.Contains(browser.ProfileDir(home), `:\`) {
			t.Errorf("log does not carry the Windows profile path %s:\n%s", browser.ProfileDir(home), logs)
		}
	}
	// --once exits after the first delivery, but the shutdown still flushes what is already queued
	// (nothing is dropped), so a second notification of the fake page may have gone out too.
	st, err := exec.Command(exe, "--home", home, "status").CombinedOutput()
	if err != nil || !regexp.MustCompile(`\([1-9][0-9]* sent in total\)`).Match(st) {
		t.Errorf("status after run: %v\n%s", err, st)
	}
}

func TestBinaryStatusInstallDryRunVersion(t *testing.T) {
	exe := tbdExe(t)
	_, _, home := fixture(t)
	out, err := exec.Command(exe, "--home", home, "status").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "paired:") || strings.Contains(string(out), proto.VectorSecret.Code()) {
		t.Fatalf("status: %v\n%s", err, out)
	}
	before, _ := os.ReadDir(home)
	out, err = exec.Command(exe, "--home", home, "install", "--dry-run").CombinedOutput()
	if err != nil {
		t.Fatalf("install --dry-run: %v\n%s", err, out)
	}
	want := "ExecStart="
	if runtime.GOOS == "windows" {
		want = "conhost.exe"
	}
	if !strings.Contains(string(out), want) || !strings.Contains(string(out), "run") {
		t.Fatalf("install --dry-run output lacks %q:\n%s", want, out)
	}
	after, _ := os.ReadDir(home)
	if len(after) != len(before) {
		t.Fatalf("install --dry-run changed the data dir: %v → %v", before, after)
	}
	out, err = exec.Command(exe, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "protocol v1") {
		t.Fatalf("version: %v %s", err, out)
	}
}

// TestBinaryLoginWindowed is BRIEF §7's `tbd login` against faketeams: a windowed run whose window
// the test closes over CDP (Target.closeTarget, as a user closing it; on macOS the browser keeps
// running without windows) once the page has been on teams_url long enough to count as signed in.
// It needs a GUI: the Windows host, Linux with WSLg, a macOS GUI session (faketeams.RequireGUI).
func TestBinaryLoginWindowed(t *testing.T) {
	faketeams.RequireGUI(t)
	exe := tbdExe(t)
	needBrowser(t)
	teams, _, home := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--home", home, "login")
	cmd.Env = withoutEnv(os.Environ(), "TBD_BROWSER", "TBD_HOME")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	ws := devToolsURL(t, ctx, browser.ProfileDir(home), done)
	if _, ok := teams.WaitRequest(40*time.Second, func(r faketeams.Request) bool { return r.Path == "/v2/app.js" }); !ok {
		t.Fatalf("the login window never loaded Teams\n%s", out.String())
	}
	time.Sleep(browser.DefaultLoginSettle + 2*time.Second) // "signed in" = stayed on Teams for the settle time
	cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
	if n, err := faketeams.ClosePageTargets(cctx, ws); err != nil || n == 0 {
		t.Fatalf("closing the login window: %d pages, %v\n%s", n, err, out.String())
	}
	ccancel()
	select {
	case err := <-done:
		if err != nil || !strings.Contains(out.String(), "Signed in") {
			t.Fatalf("tbd login: %v\n%s", err, out.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("tbd login did not exit after the window was closed\n%s", out.String())
	}
}

// devToolsURL waits for <profile>/DevToolsActivePort, which the browser writes for
// --remote-debugging-port=0.
func devToolsURL(t *testing.T, ctx context.Context, profile string, done <-chan error) string {
	t.Helper()
	for {
		if b, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); err == nil {
			sc := bufio.NewScanner(bytes.NewReader(b))
			var lines []string
			for sc.Scan() {
				lines = append(lines, strings.TrimSpace(sc.Text()))
			}
			if len(lines) >= 2 && lines[0] != "" && lines[1] != "" {
				return "ws://127.0.0.1:" + lines[0] + lines[1]
			}
		}
		select {
		case err := <-done:
			t.Fatalf("tbd login exited before the browser was up: %v", err)
		case <-ctx.Done():
			t.Fatal("no DevToolsActivePort")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func withoutEnv(env []string, keys ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		drop := false
		for _, k := range keys {
			drop = drop || strings.HasPrefix(strings.ToUpper(kv), k+"=")
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// syncBuffer is a goroutine-safe output sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitUntil polls cond every 200 ms for up to d.
func waitUntil(d time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// countStopping counts the daemon_stopping alerts the FCM mock received.
func countStopping(t *testing.T, srv *fcmtest.Server) int {
	t.Helper()
	n := 0
	for _, m := range srv.Messages() {
		if m.DecryptErr != nil {
			t.Fatalf("undecryptable payload: %v", m.DecryptErr)
		}
		if m.Decrypted != nil && m.Decrypted.T == proto.TypeAlert && m.Decrypted.Kind == proto.KindDaemonStopping {
			n++
		}
	}
	return n
}

func sawStopping(t *testing.T, srv *fcmtest.Server) bool {
	t.Helper()
	return countStopping(t, srv) > 0
}

func TestBinaryStopWithoutDaemon(t *testing.T) {
	exe := tbdExe(t)
	home := filepath.Join(t.TempDir(), "tbd home")
	cmd := exec.Command(exe, "stop")
	cmd.Env = append(withoutEnv(os.Environ(), "TBD_HOME"), "TBD_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "not running") {
		t.Fatalf("tbd stop: %v\n%s", err, out)
	}
	if _, err := os.Stat(home); err == nil {
		t.Error("tbd stop created the data directory")
	}
}

// TestBinaryStopRunningDaemon: `tbd stop` against a real `tbd run` (faketeams + FCM mock) takes the
// graceful path: daemon_stopping is pushed, the daemon exits 0 and the lock is free again.
func TestBinaryStopRunningDaemon(t *testing.T) {
	exe := tbdExe(t)
	needBrowser(t)
	_, srv, home := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, exe, "--home", home, "run")
	run.Env = withoutEnv(os.Environ(), "TBD_BROWSER", "TBD_HOME")
	logs := &syncBuffer{}
	run.Stdout, run.Stderr = logs, logs
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- run.Wait() }()
	if !waitUntil(60*time.Second, func() bool { return strings.Contains(logs.String(), `msg="session attached"`) }) {
		t.Fatalf("the daemon did not attach\n%s", logs.String())
	}
	if held, pid := daemon.Held(home); !held || pid != strconv.Itoa(run.Process.Pid) {
		t.Fatalf("lock: %v %q, want pid %d", held, pid, run.Process.Pid)
	}
	out, err := exec.Command(exe, "--home", home, "stop").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Stopped after") {
		t.Fatalf("tbd stop: %v\n%s\n%s", err, out, logs.String())
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("tbd run exited with %v\n%s", err, logs.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("tbd run still running after tbd stop\n%s", logs.String())
	}
	if held, _ := daemon.Held(home); held {
		t.Error("lock still held")
	}
	if !waitUntil(5*time.Second, func() bool { return sawStopping(t, srv) }) {
		t.Errorf("no daemon_stopping push\n%s", logs.String())
	}
	l := logs.String()
	if !strings.Contains(l, `msg="tbd stopped"`) {
		t.Errorf("no clean shutdown in the log:\n%s", l)
	}
	if runtime.GOOS == "windows" && !strings.Contains(l, "stop requested (tbd stop)") {
		t.Errorf("the stop event did not reach the daemon:\n%s", l)
	}
	if strings.Contains(l, "watching the console host") {
		t.Errorf("parent watch active under the test harness:\n%s", l)
	}
}

func TestBinaryStartWhileRunning(t *testing.T) {
	exe := tbdExe(t)
	home := t.TempDir()
	lock, err := daemon.AcquireLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	out, err := exec.Command(exe, "--home", home, "start").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "already running (pid "+strconv.Itoa(os.Getpid())+")") {
		t.Fatalf("tbd start: %v\n%s", err, out)
	}
}

// TestBinaryScheduledTaskEnd is the D20 scenario on the Windows host: tbd.exe runs from a
// scheduled task tbd-test-<random> whose action is the real install's (conhost.exe --headless
// tbd.exe run --home … --log-file …), against faketeams and the FCM mock; `schtasks /end` must stop
// it gracefully. (Started straight from a test, conhost --headless does not run its client: it
// finds standard handles and acts as a ConPTY server; only Task Scheduler starts it as in
// production.) Before that, `tbd stop` followed by `schtasks /run` (what `tbd start` runs) must
// bring it back. The task is created and deleted by this test, like the service round trip.
func TestBinaryScheduledTaskEnd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only: a scheduled task tbd-test-<random>")
	}
	exe := tbdExe(t)
	needBrowser(t)
	_, srv, home := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	r := service.ExecRunner{}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := "tbd-test-" + hex.EncodeToString(b)
	logFile := daemon.LogFile(home)
	spec := service.Spec{Name: name, Exe: exe, Home: home, ExtraArgs: []string{"--log-file", logFile},
		Description: "tbd stop test task; deleted by the test", NoStart: true}
	plan, err := service.InstallPlan(spec, "windows", service.DefaultEnv())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if held, _ := daemon.Held(home); held { // never leave a tbd.exe behind
			_ = exec.Command(exe, "--home", home, "stop", "--timeout", "5s").Run()
		}
		un, err := service.UninstallPlan(spec, "windows", service.DefaultEnv())
		if err == nil {
			err = service.Apply(cctx, un, r, io.Discard, false)
		}
		if _, qerr := r.Run(cctx, "schtasks", "/query", "/tn", name); qerr == nil {
			t.Errorf("task %s still exists (%v); remove it with: schtasks /delete /tn %s /f", name, err, name)
		}
	})
	var applyLog bytes.Buffer
	if err := service.Apply(ctx, plan, r, &applyLog, false); err != nil {
		t.Fatalf("create task: %v\n%s", err, applyLog.String())
	}
	readLog := func() string { b, _ := os.ReadFile(logFile); return string(b) }
	runTask := func(n int) { // the n-th start; MultipleInstancesPolicy IgnoreNew: only once the last instance ended
		t.Helper()
		if out, err := r.Run(ctx, "schtasks", "/run", "/tn", name); err != nil {
			t.Fatalf("schtasks /run: %v: %s", err, out)
		}
		if !waitUntil(90*time.Second, func() bool { return strings.Count(readLog(), `msg="session attached"`) >= n }) {
			t.Fatalf("start %d: the task's daemon did not attach\n%s", n, readLog())
		}
	}
	// 1. `tbd stop` ends the task instance (conhost exits with its client), so the task can run
	// again: that is what `tbd start` does.
	runTask(1)
	if out, err := exec.Command(exe, "--home", home, "stop").CombinedOutput(); err != nil || !strings.Contains(string(out), "Stopped after") {
		t.Fatalf("tbd stop: %v\n%s\n%s", err, out, readLog())
	}
	runTask(2)
	// 2. schtasks /end.
	_, pid := daemon.Held(home)
	if !strings.Contains(readLog(), "watching the console host") || !strings.Contains(readLog(), "parent=conhost.exe") {
		t.Fatalf("no parent watch on conhost\n%s", readLog())
	}
	if out, err := r.Run(ctx, "schtasks", "/end", "/tn", name); err != nil {
		t.Fatalf("schtasks /end: %v: %s", err, out)
	}
	if !waitUntil(60*time.Second, func() bool { held, _ := daemon.Held(home); return !held }) {
		t.Fatalf("tbd.exe (pid %s) still runs after schtasks /end\n%s", pid, readLog())
	}
	l := readLog()
	if !strings.Contains(l, "stop requested (tbd stop)") || !strings.Contains(l, "console host exited; stopping") || strings.Count(l, `msg="tbd stopped"`) != 2 {
		t.Errorf("not two graceful stops in the log:\n%s", l)
	}
	if !waitUntil(5*time.Second, func() bool { return countStopping(t, srv) == 2 }) {
		t.Errorf("%d daemon_stopping pushes, want 2\n%s", countStopping(t, srv), l)
	}
}
