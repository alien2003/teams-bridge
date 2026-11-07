//go:build !windows

package browser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain adds a helper mode: TBD_TEST_HELPER=hold-browser launches the real browser on
// TBD_TEST_HOME, prints "pid=<browser pid>" and blocks, standing in for a daemon that is about to
// be SIGKILLed (TestLaunchAfterDaemonKilled).
func TestMain(m *testing.M) {
	if os.Getenv("TBD_TEST_HELPER") == "hold-browser" {
		ctx := context.Background()
		p, err := Launch(ctx, LaunchOptions{
			Path: os.Getenv("TBD_TEST_BROWSER"), Home: os.Getenv("TBD_TEST_HOME"), Headless: true, Logger: testLogger(),
		})
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		fmt.Printf("pid=%d\n", p.PID)
		select {}
	}
	os.Exit(m.Run())
}

// orphanScript stands in for a browser left behind: it points $1/SingletonLock at itself, starts a
// child in its process group, records the child's PID and waits. %s is its SIGTERM trap: by
// default it reaps the child before it exits, so no zombie of the group is left for the test to
// trip over (a real orphan belongs to launchd or init, which reap it).
const orphanScript = `p="$1"
ln -s "host-$$" "$p/SingletonLock"
trap %s TERM
sleep 300 &
c=$!
echo $c > "$p/orphan.child"
wait`

type orphan struct {
	pid, child int
	done       chan struct{}
}

// startOrphan starts orphanScript with lockProfile's SingletonLock and --user-data-dir=argProfile,
// as its own process group's leader if leader is set. trap is the shell's SIGTERM action ("" for
// the default; a quoted empty string ignores SIGTERM in the whole group).
func startOrphan(t *testing.T, lockProfile, argProfile string, leader bool, trap string) *orphan {
	t.Helper()
	if trap == "" {
		trap = `'kill $c 2>/dev/null; wait $c; exit 0'`
	}
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf(orphanScript, trap), "fakebrowser", lockProfile, "--user-data-dir="+argProfile)
	if leader {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	o := &orphan{pid: cmd.Process.Pid, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(o.done) }()
	t.Cleanup(func() {
		if leader {
			_ = syscall.Kill(-o.pid, syscall.SIGKILL)
		}
		_ = syscall.Kill(o.pid, syscall.SIGKILL)
		if o.child > 0 {
			_ = syscall.Kill(o.child, syscall.SIGKILL)
		}
		<-o.done
	})
	for i := 0; i < 250 && o.child == 0; i++ {
		if b, err := os.ReadFile(filepath.Join(lockProfile, "orphan.child")); err == nil {
			o.child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if o.child == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if o.child == 0 {
		t.Fatal("the stand-in browser did not start")
	}
	return o
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestKillStaleBrowser(t *testing.T) {
	profile := t.TempDir()
	o := startOrphan(t, profile, profile, true, "")
	var logs lockedBuffer
	killStaleBrowser(profile, bufLogger(&logs))
	select {
	case <-o.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stale browser is still running")
	}
	if !groupGone(o.pid, 5*time.Second) || alive(o.child) {
		t.Error("a process of the stale browser's group survived")
	}
	if l := logs.String(); !strings.Contains(l, "killed a browser left by a previous tbd") || !strings.Contains(l, "pid="+strconv.Itoa(o.pid)) {
		t.Errorf("log:\n%s", l)
	}
}

// TestKillStaleBrowserIgnoresTERM: a group that ignores SIGTERM gets SIGKILL after the grace.
func TestKillStaleBrowserIgnoresTERM(t *testing.T) {
	profile := t.TempDir()
	o := startOrphan(t, profile, profile, true, "''")
	pid, ok := staleBrowserPID(profile, procArgs)
	if !ok || pid != o.pid {
		t.Fatalf("staleBrowserPID = %d, %v; want %d", pid, ok, o.pid)
	}
	start := time.Now()
	if err := endGroup(pid, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 300*time.Millisecond {
		t.Errorf("ended after %s, before the grace: did SIGTERM work despite the trap?", d)
	}
	<-o.done
}

func TestKillStaleBrowserLeavesOthersAlone(t *testing.T) {
	t.Run("another profile in argv", func(t *testing.T) {
		profile := t.TempDir()
		o := startOrphan(t, profile, profile+"2", true, "")
		killStaleBrowser(profile, testLogger())
		if !alive(o.pid) || !alive(o.child) {
			t.Error("a process of another profile was killed")
		}
	})
	t.Run("a prefix of the profile in argv", func(t *testing.T) {
		profile := t.TempDir()
		o := startOrphan(t, profile, filepath.Dir(profile), true, "")
		killStaleBrowser(profile, testLogger())
		if !alive(o.pid) {
			t.Error("a process with a different --user-data-dir was killed")
		}
	})
	t.Run("not a group leader", func(t *testing.T) {
		profile := t.TempDir()
		o := startOrphan(t, profile, profile, false, "")
		if _, ok := staleBrowserPID(profile, procArgs); ok {
			t.Error("a process that does not lead its group counts as a stale browser")
		}
		killStaleBrowser(profile, testLogger())
		if !alive(o.pid) {
			t.Error("killed")
		}
	})
	t.Run("dangling lock", func(t *testing.T) {
		profile := t.TempDir()
		c := exec.Command("/bin/sh", "-c", "exit 0")
		if err := c.Run(); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("host-"+strconv.Itoa(c.Process.Pid), filepath.Join(profile, "SingletonLock")); err != nil {
			t.Fatal(err)
		}
		if pid, ok := staleBrowserPID(profile, procArgs); ok {
			t.Errorf("a dead PID %d counts as a stale browser", pid)
		}
	})
	t.Run("no lock, or not a symlink", func(t *testing.T) {
		profile := t.TempDir()
		if _, ok := staleBrowserPID(profile, procArgs); ok {
			t.Error("no lock")
		}
		if err := os.WriteFile(filepath.Join(profile, "SingletonLock"), []byte("host-"+strconv.Itoa(os.Getppid())), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := staleBrowserPID(profile, procArgs); ok {
			t.Error("a regular file counts as the lock")
		}
	})
	t.Run("our own PID", func(t *testing.T) {
		profile := t.TempDir()
		if err := os.Symlink("host-"+strconv.Itoa(os.Getpid()), filepath.Join(profile, "SingletonLock")); err != nil {
			t.Fatal(err)
		}
		if _, ok := staleBrowserPID(profile, func(int) ([]string, error) { return []string{"--user-data-dir=" + profile}, nil }); ok {
			t.Error("the test process itself counts as a stale browser")
		}
	})
	t.Run("argv unreadable", func(t *testing.T) {
		profile := t.TempDir()
		startOrphan(t, profile, profile, true, "")
		if _, ok := staleBrowserPID(profile, func(int) ([]string, error) { return nil, errors.New("no") }); ok {
			t.Error("a process whose argv cannot be read counts as a stale browser")
		}
	})
}

// TestProcArgs reads the argv of a child with spaces in its arguments, as the profile under
// ~/Library/Application Support has (kern.procargs2 on macOS, /proc on Linux).
func TestProcArgs(t *testing.T) {
	dir := t.TempDir()
	arg := "--user-data-dir=" + filepath.Join(dir, "Application Support", "tbd", "profile")
	ready := filepath.Join(dir, "ready")
	// Two commands, so that the shell cannot exec sleep in its own place.
	const script = `: > "$2"; sleep 300; exit 0`
	cmd := exec.Command("/bin/sh", "-c", script, "fakebrowser", arg, ready, "")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for i := 0; i < 250; i++ {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := procArgs(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	// argv[0] is the shell's own business (macOS's /bin/sh re-executes the selected shell).
	want := []string{"-c", script, "fakebrowser", arg, ready, ""}
	if len(got) == 0 || !strings.HasSuffix(got[0], "sh") || strings.Join(got[1:], "|") != strings.Join(want, "|") {
		t.Errorf("procArgs = %q, want sh and %q", got, want)
	}
}

// TestLaunchAfterDaemonKilled: a daemon (here a helper process) is SIGKILLed while its browser
// runs; the next Launch on the same home must succeed, and nothing of the old browser's process
// group may survive. On Linux Pdeathsig ends the old browser with the helper; on macOS it is
// killStaleBrowser, whose log line must then appear.
func TestLaunchAfterDaemonKilled(t *testing.T) {
	path := requireBrowser(t)
	home := t.TempDir()
	helper := exec.Command(os.Args[0], "-test.run=^$")
	helper.Env = append(os.Environ(), "TBD_TEST_HELPER=hold-browser", "TBD_TEST_HOME="+home, "TBD_TEST_BROWSER="+path)
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			line <- sc.Text()
		}
		close(line)
	}()
	var old int
	select {
	case l := <-line:
		if _, err := fmt.Sscanf(l, "pid=%d", &old); err != nil {
			t.Fatalf("helper: %q", l)
		}
	case <-time.After(60 * time.Second):
		_ = helper.Process.Kill()
		t.Fatal("the helper's browser never started")
	}
	t.Cleanup(func() { _ = syscall.Kill(-old, syscall.SIGKILL) })
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()

	var logs lockedBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p, err := Launch(ctx, LaunchOptions{Path: path, Home: home, Headless: true, Logger: bufLogger(&logs)})
	if err != nil {
		t.Fatalf("Launch after the daemon was killed: %v", err)
	}
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 20*time.Second)
		defer cc()
		_ = p.Close(c)
	})
	if !groupGone(old, 15*time.Second) {
		t.Errorf("a process of the old browser's group %d is still running", old)
	}
	killed := strings.Contains(logs.String(), "killed a browser left by a previous tbd")
	t.Logf("old browser ended by: %s", map[bool]string{true: "killStaleBrowser", false: "Pdeathsig"}[killed])
	if runtime.GOOS == "darwin" && !killed {
		t.Errorf("no cleanup log line on macOS:\n%s", logs.String())
	}
}
