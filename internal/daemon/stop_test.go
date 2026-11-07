package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/proto"
	"github.com/alien2003/teams-bridge/internal/service"
)

// The test binary doubles as a lock holder for the stop tests: with TBD_TEST_LOCK_HELPER=<home> it
// takes home's lock, prints "locked" and then, in mode "obey", stops on SIGTERM or the stop event
// like `tbd run` does, or, in mode "ignore", ignores both until it is killed (or two minutes pass).
// Mode "legacy" plays a tbd from before `tbd stop`: it holds the lock without a stop event (and
// stops on SIGTERM, as such a tbd did on Unix).
const (
	helperHomeEnv = "TBD_TEST_LOCK_HELPER"
	helperModeEnv = "TBD_TEST_LOCK_HELPER_MODE"
)

func TestMain(m *testing.M) {
	if home := os.Getenv(helperHomeEnv); home != "" {
		os.Exit(lockHelper(home, os.Getenv(helperModeEnv)))
	}
	os.Exit(m.Run())
}

func lockHelper(home, mode string) int {
	ctx := context.Background()
	if mode == "ignore" {
		signal.Ignore(syscall.SIGTERM) // (a later Notify would undo this)
	} else {
		var cancel context.CancelFunc
		ctx, cancel = signal.NotifyContext(ctx, syscall.SIGTERM)
		defer cancel()
	}
	l, err := AcquireLock(home)
	if err != nil {
		fmt.Println("lock:", err)
		return 2
	}
	defer l.Release()
	if mode == "legacy" && l.stop != nil {
		l.stop.close() // its only handle: the named event is gone, as before D20
		l.stop = nil
	}
	stop := l.StopRequested()
	if mode == "ignore" {
		stop = nil
	}
	fmt.Println("locked")
	select {
	case <-stop:
	case <-ctx.Done():
	case <-time.After(2 * time.Minute):
		return 3
	}
	return 0
}

// helper starts the lock holder and waits until it holds home's lock. exited closes when it ends.
func helper(t *testing.T, home, mode string) (cmd *exec.Cmd, exited <-chan struct{}, exitErr func() error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return helperAt(t, self, home, mode)
}

// helperAt is helper with the test binary at exe (a copy of it, say).
func helperAt(t *testing.T, exe, home, mode string) (cmd *exec.Cmd, exited <-chan struct{}, exitErr func() error) {
	t.Helper()
	cmd = exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), helperHomeEnv+"="+home, helperModeEnv+"="+mode)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	done := make(chan struct{})
	var werr error
	go func() { werr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	if strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper: %q", line)
	}
	return cmd, done, func() error { <-done; return werr }
}

// noSystemd answers `systemctl --user show -p MainPID` with 0, so Stop takes the signal path.
type noSystemd struct{}

func (noSystemd) Run(context.Context, string, ...string) (string, error) { return "0\n", nil }

func TestStopEventName(t *testing.T) {
	a := StopEventName(0x1234abcd, 42)
	if !regexp.MustCompile(`^Local\\tbd-stop-[0-9a-f]{16}$`).MatchString(a) {
		t.Fatalf("name %q", a)
	}
	if a != StopEventName(0x1234abcd, 42) {
		t.Error("not deterministic")
	}
	if a == StopEventName(0x1234abcd, 43) || a == StopEventName(0x1234abce, 42) {
		t.Error("different files share a name")
	}
}

func TestParseProcLocks(t *testing.T) {
	const text = `1: POSIX  ADVISORY  WRITE 900 00:2d:777 0 EOF
2: FLOCK  ADVISORY  WRITE 1234 08:20:5678 0 EOF
2: -> FLOCK  ADVISORY  WRITE 999 08:20:5678 0 EOF
3: FLOCK  ADVISORY  WRITE 4321 00:31:5678 0 EOF
4: FLOCK  ADVISORY  WRITE 55 08:20:56789 0 EOF
5: OFDLCK ADVISORY  READ  -1 00:06:5678 0 EOF
garbage
`
	if got := fmt.Sprint(parseProcLocks(text, 5678)); got != "[1234 4321]" {
		t.Errorf("owners of 5678 = %s", got)
	}
	if got := parseProcLocks(text, 1); len(got) != 0 {
		t.Errorf("owners of 1 = %v", got)
	}
}

func TestIsTbdImage(t *testing.T) {
	for _, c := range []struct {
		image string
		want  bool
	}{
		{`C:\Users\u\AppData\Local\Programs\tbd\tbd.exe`, true},
		{`C:\x\TBD.EXE`, true},
		{`tbd.exe`, true},
		{`C:\x\tbd-dev.exe`, false},
		{`C:\Windows\System32\notepad.exe`, false},
		{`C:\x\daemon.test.exe`, false},
		{`C:\x\tbd.exe.bak`, false},
		{``, false},
	} {
		if got := isTbdImage(c.image); got != c.want {
			t.Errorf("isTbdImage(%q) = %v", c.image, got)
		}
	}
}

func TestLockHolder(t *testing.T) {
	home := t.TempDir()
	if pid, err := LockHolder(home); pid != 0 || err != nil {
		t.Fatalf("no lock file: %d %v", pid, err)
	}
	l, err := AcquireLock(home)
	if err != nil {
		t.Fatal(err)
	}
	if pid, err := LockHolder(home); pid != os.Getpid() || err != nil {
		t.Fatalf("held by us: %d %v", pid, err)
	}
	// The OS check LockHolder relies on accepts the real holder (lock_linux_test.go and
	// lock_darwin_test.go test what it rejects).
	if err := checkLockOwner(l.f, os.Getpid()); err != nil {
		t.Errorf("checkLockOwner(own pid): %v", err)
	}
	l.Release()
	if pid, err := LockHolder(home); pid != 0 || err != nil {
		t.Fatalf("released: %d %v", pid, err)
	}
}

// A momentary try-lock (tbd status, or LockHolder itself) while the file still names an old,
// maybe reused PID is not a holder.
func TestLockHolderIgnoresMomentaryProbe(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(LockPath(home), []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(LockPath(home), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(30*time.Millisecond, func() { unlockFile(f) })
	if pid, err := lockHolder(home, 300*time.Millisecond); pid != 0 || err != nil {
		t.Fatalf("momentary probe taken for holder: %d %v", pid, err)
	}
	// Held for good by a process the file does not name: never a PID to signal.
	if err := lockFile(f); err != nil {
		t.Fatal(err)
	}
	defer unlockFile(f)
	if err := os.WriteFile(LockPath(home), nil, 0o600); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	if pid, err := lockHolder(home, 10*time.Millisecond); pid != 0 || !errors.Is(err, ErrHolderUnknown) {
		t.Fatalf("empty pid file: %d %v", pid, err)
	}
}

func TestStopNotRunning(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	if err := Stop(context.Background(), StopOptions{Home: home, Out: &out}); err != nil || !strings.Contains(out.String(), "not running") {
		t.Fatalf("no lock file: %v %q", err, out.String())
	}
	// A stale lock file (a crashed daemon): its PID is never signalled.
	if err := os.WriteFile(LockPath(home), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	called := false
	o := StopOptions{Home: home, Out: &out, request: func(context.Context, int) (string, error) { called = true; return "x", nil }}
	if err := Stop(context.Background(), o); err != nil || called || !strings.Contains(out.String(), "not running") {
		t.Fatalf("stale lock file: %v called=%v %q", err, called, out.String())
	}
}

// TestStopPhases drives Stop's graceful and forced phases with fake mechanisms against a lock held
// in this process.
func TestStopPhases(t *testing.T) {
	ctx := context.Background()
	t.Run("graceful", func(t *testing.T) {
		home := t.TempDir()
		l, err := AcquireLock(home)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		o := StopOptions{Home: home, Out: &out, poll: 10 * time.Millisecond, settle: 10 * time.Millisecond,
			request: func(_ context.Context, pid int) (string, error) {
				if pid != os.Getpid() {
					t.Errorf("asked pid %d", pid)
				}
				time.AfterFunc(50*time.Millisecond, l.Release)
				return "fake", nil
			},
			force: func(int) error { t.Error("forced although it stopped"); return nil }}
		if err := Stop(ctx, o); err != nil || !strings.Contains(out.String(), "Stopped after") {
			t.Fatalf("%v\n%s", err, out.String())
		}
	})
	t.Run("forced after the timeout", func(t *testing.T) {
		home := t.TempDir()
		l, err := AcquireLock(home)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		forced := false
		o := StopOptions{Home: home, Out: &out, Timeout: 200 * time.Millisecond, poll: 10 * time.Millisecond, settle: 10 * time.Millisecond,
			request: func(context.Context, int) (string, error) { return "", nil },
			force:   func(int) error { forced = true; l.Release(); return nil }}
		start := time.Now()
		if err := Stop(ctx, o); err != nil || !forced || time.Since(start) < 200*time.Millisecond {
			t.Fatalf("%v forced=%v after %s\n%s", err, forced, time.Since(start), out.String())
		}
		for _, want := range []string{"does not listen", "ending it forcibly", "Ended."} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output lacks %q:\n%s", want, out.String())
			}
		}
	})
	// The holder lets go of the lock while Stop is still acting on it: the stop succeeded.
	t.Run("gone during the request", func(t *testing.T) {
		home := t.TempDir()
		l, err := AcquireLock(home)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		o := StopOptions{Home: home, Out: &out, poll: 10 * time.Millisecond, settle: 10 * time.Millisecond,
			request: func(_ context.Context, pid int) (string, error) {
				l.Release()
				return "", fmt.Errorf("pid %d %w", pid, errNotHolder)
			},
			force: func(int) error { t.Error("forced although it stopped"); return nil }}
		if err := Stop(ctx, o); err != nil || !strings.Contains(out.String(), "stopped in the meantime") {
			t.Fatalf("%v\n%s", err, out.String())
		}
	})
	t.Run("gone during the force", func(t *testing.T) {
		home := t.TempDir()
		l, err := AcquireLock(home)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		o := StopOptions{Home: home, Out: &out, Timeout: 50 * time.Millisecond, poll: 10 * time.Millisecond, settle: 10 * time.Millisecond,
			request: func(context.Context, int) (string, error) { return "fake", nil },
			force:   func(int) error { l.Release(); return os.ErrProcessDone }}
		if err := Stop(ctx, o); err != nil || !strings.Contains(out.String(), "stopped in the meantime") {
			t.Fatalf("%v\n%s", err, out.String())
		}
	})
	t.Run("taken over during the force", func(t *testing.T) {
		home := t.TempDir()
		f, err := os.OpenFile(LockPath(home), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := lockFile(f); err != nil {
			t.Fatal(err)
		}
		defer unlockFile(f)
		if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		o := StopOptions{Home: home, Out: &out, Timeout: 50 * time.Millisecond, poll: 10 * time.Millisecond, settle: 10 * time.Millisecond,
			request: func(context.Context, int) (string, error) { return "fake", nil },
			force: func(pid int) error { // a new daemon (another PID) holds the directory now
				_, _ = f.WriteAt([]byte(strconv.Itoa(pid+1)+"\n"), 0)
				return fmt.Errorf("pid %d %w", pid, errNotHolder)
			}}
		if err := Stop(ctx, o); err != nil || !strings.Contains(out.String(), "another tbd (pid "+strconv.Itoa(os.Getpid()+1)) {
			t.Fatalf("%v\n%s", err, out.String())
		}
	})
	t.Run("force refused", func(t *testing.T) {
		home := t.TempDir()
		l, err := AcquireLock(home)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Release()
		o := StopOptions{Home: home, Timeout: 50 * time.Millisecond, poll: 10 * time.Millisecond, settle: 10 * time.Millisecond,
			request: func(context.Context, int) (string, error) { return "fake", nil },
			force:   func(int) error { return errors.New("not tbd.exe") }}
		if err := Stop(ctx, o); err == nil || !strings.Contains(err.Error(), "not tbd.exe") {
			t.Fatalf("got %v", err)
		}
	})
}

// TestStopHelperGraceful stops a separate lock holder with the real mechanism (SIGTERM, or the
// Windows stop event).
func TestStopHelperGraceful(t *testing.T) {
	home := filepath.Join(t.TempDir(), "tbd home")
	cmd, exited, exitErr := helper(t, home, "obey")
	var out bytes.Buffer
	if err := Stop(context.Background(), StopOptions{Home: home, Out: &out, Runner: noSystemd{}}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Stopping tbd (pid "+strconv.Itoa(cmd.Process.Pid)) || !strings.Contains(out.String(), "Stopped after") {
		t.Errorf("output:\n%s", out.String())
	}
	select {
	case <-exited:
		if err := exitErr(); err != nil {
			t.Errorf("helper exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper still running")
	}
}

// TestStopHelperIgnoring: a holder that ignores the request is killed after the timeout on Unix;
// on Windows it is not tbd.exe (it is daemon.test.exe), so it must not be ended at all.
func TestStopHelperIgnoring(t *testing.T) {
	home := t.TempDir()
	_, exited, _ := helper(t, home, "ignore")
	var out bytes.Buffer
	err := Stop(context.Background(), StopOptions{Home: home, Out: &out, Timeout: time.Second, Runner: noSystemd{}})
	if runtime.GOOS == "windows" {
		if err == nil || !strings.Contains(err.Error(), "not tbd.exe") {
			t.Fatalf("got %v\n%s", err, out.String())
		}
		if held, _ := Held(home); !held {
			t.Fatal("the helper was ended although it is not tbd.exe")
		}
		return
	}
	if err != nil || !strings.Contains(out.String(), "ending it forcibly") || !strings.Contains(out.String(), "Ended.") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("helper still running")
	}
}

// systemdFake plays tbd.service with the helper as its main process: `stop --no-block` sends the
// SIGTERM that systemd would.
type systemdFake struct {
	mu    sync.Mutex
	pid   int
	calls []string
}

func (f *systemdFake) Run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, line)
	f.mu.Unlock()
	switch line {
	case "systemctl --user show -p MainPID --value tbd.service":
		return strconv.Itoa(f.pid) + "\n", nil
	case "systemctl --user stop --no-block tbd.service":
		p, _ := os.FindProcess(f.pid)
		return "", p.Signal(syscall.SIGTERM)
	}
	return "", fmt.Errorf("unexpected %s", line)
}

func TestStopThroughSystemd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd: Linux only")
	}
	home := t.TempDir()
	cmd, _, _ := helper(t, home, "obey")
	f := &systemdFake{pid: cmd.Process.Pid}
	var out bytes.Buffer
	if err := Stop(context.Background(), StopOptions{Home: home, Out: &out, Runner: f}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "systemctl --user stop --no-block tbd.service") || len(f.calls) != 2 {
		t.Errorf("calls %q\n%s", f.calls, out.String())
	}
}

// TestE2EStop stops the in-process daemon with Stop: the graceful path ends in daemon_stopping.
// On Unix Stop sends SIGTERM to this very test process, which is caught here like tbd run does.
func TestE2EStop(t *testing.T) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	defer signal.Stop(sig)
	h := start(t, Options{ProbeInterval: time.Hour})
	go func() {
		select {
		case <-sig:
			h.cancel()
		case <-time.After(2 * time.Minute):
		}
	}()
	h.waitFor("startup heartbeat", 60*time.Second, func(m proto.Message) bool { return m.T == proto.TypeHB })
	var out bytes.Buffer
	if err := Stop(context.Background(), StopOptions{Home: h.home, Out: &out, Runner: noSystemd{}}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Stopped after") {
		t.Errorf("output:\n%s", out.String())
	}
	select {
	case err := <-h.done:
		h.done <- err
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return")
	}
	h.waitFor("daemon_stopping", 5*time.Second, isAlert(proto.KindDaemonStopping))
	if runtime.GOOS == "windows" && !strings.Contains(h.logs.String(), "stop requested (tbd stop)") {
		t.Error("no stop-request log line")
	}
}

func TestHolderPlausible(t *testing.T) {
	w := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		alive, zombie bool
		started       time.Time
		want          string // "" = accepted
	}{
		{true, false, w.Add(-time.Hour), ""},
		{true, false, w, ""},
		{true, false, w.Add(900 * time.Millisecond), ""}, // within the slack
		{true, false, w.Add(1500 * time.Millisecond), "reused pid"},
		{false, false, w.Add(-time.Hour), "no such process"},
		{true, true, w.Add(-time.Hour), "exited"},
		{true, false, time.Time{}, "unknown"},
	} {
		err := holderPlausible(c.alive, c.started, c.zombie, w)
		if (c.want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c, err, c.want)
		}
	}
}

// launchdFake plays the LaunchAgent gui/501/tbd with the helper as its process: bootout sends the
// SIGTERM that launchd would; with refuse, bootout fails and Stop falls back to SIGTERM itself.
type launchdFake struct {
	mu     sync.Mutex
	pid    int
	refuse bool
	calls  []string
}

func (f *launchdFake) Run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, line)
	f.mu.Unlock()
	switch line {
	case "launchctl print gui/501/tbd":
		return fmt.Sprintf("gui/501/tbd = {\n\tstate = running\n\tpid = %d\n}\n", f.pid), nil
	case "launchctl bootout gui/501/tbd":
		if f.refuse {
			return "Boot-out failed: 1: Operation not permitted\n", errors.New("exit status 1")
		}
		p, _ := os.FindProcess(f.pid)
		return "", p.Signal(syscall.SIGTERM)
	}
	return "", fmt.Errorf("unexpected %s", line)
}

func TestStopThroughLaunchd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launchd: Unix signals")
	}
	env := &service.Env{HomeDir: "/Users/u", UID: 501}
	for _, refuse := range []bool{false, true} {
		home := t.TempDir()
		cmd, exited, _ := helper(t, home, "obey")
		f := &launchdFake{pid: cmd.Process.Pid, refuse: refuse}
		var out bytes.Buffer
		if err := Stop(context.Background(), StopOptions{Home: home, Out: &out, Runner: f, goos: "darwin", env: env}); err != nil {
			t.Fatalf("refuse=%v: %v\n%s", refuse, err, out.String())
		}
		want, calls := "(pid "+strconv.Itoa(cmd.Process.Pid)+", launchctl bootout gui/501/tbd)", 2
		if refuse {
			want = ", SIGTERM, as launchctl bootout gui/501/tbd failed: Boot-out failed"
		}
		if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "Stopped after") || len(f.calls) != calls {
			t.Errorf("refuse=%v: calls %q\n%s", refuse, f.calls, out.String())
		}
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Fatal("helper still running")
		}
	}
	// A holder that is not the job's process (a tbd run in a terminal) gets SIGTERM directly.
	home := t.TempDir()
	cmd, _, _ := helper(t, home, "obey")
	f := &launchdFake{pid: cmd.Process.Pid + 1}
	var out bytes.Buffer
	if err := Stop(context.Background(), StopOptions{Home: home, Out: &out, Runner: f, goos: "darwin", env: env}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), ", SIGTERM)") || len(f.calls) != 1 {
		t.Errorf("calls %q\n%s", f.calls, out.String())
	}
}
