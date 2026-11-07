package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/service"
)

// serviceFake answers the service commands Start runs; "start" takes the lock in this process the
// way the started daemon would.
type serviceFake struct {
	mu        sync.Mutex
	home      string
	installed bool
	calls     []string
	lock      *Lock
}

func (f *serviceFake) Run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, line)
	switch {
	case strings.Contains(line, "is-enabled"):
		if f.installed {
			return "enabled\n", nil
		}
		return "not-found\n", errors.New("exit status 4")
	case strings.Contains(line, "/query"):
		if f.installed {
			return "tbd  Ready\r\n", nil
		}
		return "ERROR: The system cannot find the file specified.\r\n", errors.New("exit status 1")
	case line == "launchctl print gui/501/tbd": // installed, but not loaded (after tbd stop)
		return "Could not find service \"tbd\" in domain for user gui: 501\n", errors.New("exit status 113")
	case line == "launchctl print gui/501", line == "launchctl enable gui/501/tbd":
		return "", nil
	case line == "systemctl --user start tbd.service" || line == "schtasks /run /tn tbd" ||
		strings.HasPrefix(line, "launchctl bootstrap gui/501 ") && strings.HasSuffix(line, "/tbd.plist"):
		l, err := AcquireLock(f.home)
		f.lock = l
		return "", err
	}
	return "", errors.New("unexpected " + line)
}

// macEnv is a synthetic Mac (uid 501) whose LaunchAgents directory is dir; with installed, it
// holds tbd.plist.
func macEnv(t *testing.T, installed bool) *service.Env {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("darwin paths on disk need a Unix file system")
	}
	dir := t.TempDir()
	if installed {
		if err := os.WriteFile(filepath.Join(dir, "tbd.plist"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &service.Env{HomeDir: "/Users/u", UID: 501, LaunchAgentsDir: dir}
}

func TestStart(t *testing.T) {
	ctx := context.Background()
	// Commands run when not installed / to start: launchd is asked first (print), then enabled and
	// bootstrapped (the job is not loaded); the plist's absence alone means not installed.
	calls := map[string][2]int{"linux": {1, 2}, "windows": {1, 2}, "darwin": {0, 4}}
	env := func(t *testing.T, goos string, installed bool) *service.Env {
		if goos != "darwin" {
			return nil
		}
		return macEnv(t, installed)
	}
	for _, goos := range []string{"linux", "windows", "darwin"} {
		t.Run(goos+" not installed", func(t *testing.T) {
			f := &serviceFake{home: t.TempDir()}
			err := Start(ctx, nil, StartOptions{Home: f.home, Runner: f, goos: goos, env: env(t, goos, false)})
			if err == nil || !strings.Contains(err.Error(), "tbd install") || len(f.calls) != calls[goos][0] {
				t.Fatalf("%v, calls %q", err, f.calls)
			}
		})
		t.Run(goos+" installed", func(t *testing.T) {
			f := &serviceFake{home: t.TempDir(), installed: true}
			var out bytes.Buffer
			if err := Start(ctx, &out, StartOptions{Home: f.home, Runner: f, goos: goos, env: env(t, goos, true)}); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			defer f.lock.Release()
			if !strings.Contains(out.String(), "Started (pid "+strconv.Itoa(os.Getpid())+")") || len(f.calls) != calls[goos][1] {
				t.Errorf("calls %q\n%s", f.calls, out.String())
			}
		})
	}
	t.Run("already running", func(t *testing.T) {
		f := &serviceFake{home: t.TempDir(), installed: true}
		l, err := AcquireLock(f.home)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Release()
		var out bytes.Buffer
		if err := Start(ctx, &out, StartOptions{Home: f.home, Runner: f, goos: "windows"}); err != nil || len(f.calls) != 0 ||
			!strings.Contains(out.String(), "already running (pid "+strconv.Itoa(os.Getpid())+")") {
			t.Fatalf("%v calls %q\n%s", err, f.calls, out.String())
		}
	})
	t.Run("started but no daemon", func(t *testing.T) {
		home := t.TempDir()
		r := hookRunner(func(string) (string, error) { return "", nil })
		err := Start(ctx, nil, StartOptions{Home: home, Runner: r, goos: "linux", Wait: 200 * time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), "journalctl --user -u tbd.service") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("darwin loaded: kickstart, but no daemon", func(t *testing.T) {
		home := t.TempDir()
		var lines []string
		r := hookRunner(func(line string) (string, error) { lines = append(lines, line); return "", nil })
		var out bytes.Buffer
		err := Start(ctx, &out, StartOptions{Home: home, Runner: r, goos: "darwin", env: macEnv(t, true), Wait: 200 * time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), filepath.Join(home, "tbd.log")+" and "+filepath.Join(home, "tbd.stderr.log")) {
			t.Fatalf("got %v", err)
		}
		if strings.Join(lines, "\n") != "launchctl print gui/501/tbd\nlaunchctl kickstart gui/501/tbd" || !strings.Contains(out.String(), "Running: launchctl kickstart gui/501/tbd\n") {
			t.Errorf("calls %q\n%s", lines, out.String())
		}
	})
}

type hookRunner func(line string) (string, error)

func (h hookRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	if strings.Contains(line, "is-enabled") {
		return "enabled\n", nil
	}
	return h(line)
}

// The lock error names the fix (tbd stop / tbd start) and still matches ErrLocked.
func TestLockedErrorNamesTheFix(t *testing.T) {
	home := t.TempDir()
	l, err := AcquireLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	_, err = AcquireLock(home)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("%v", err)
	}
	want := "tbd is already running for this data directory (pid " + strconv.Itoa(os.Getpid()) + "): run `tbd stop`, then this command again (`tbd start` starts the daemon again)"
	if err.Error() != want {
		t.Errorf("got  %s\nwant %s", err, want)
	}
}

func TestStatusLaunchdLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("darwin paths on disk need a Unix file system")
	}
	cfg := config.Default()
	cfg.Browser = filepath.Join(t.TempDir(), "no-browser") // no --version run
	r := hookRunner(func(line string) (string, error) {
		if line == "launchctl print gui/501/tbd-test-0123456789ab" {
			return "gui/501/tbd-test-0123456789ab = {\n\tstate = running\n\tpid = 4242\n}\n", nil
		}
		return "", errors.New("unexpected " + line)
	})
	env := &service.Env{HomeDir: "/Users/u", UID: 501, LaunchAgentsDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(env.LaunchAgentsDir, "tbd-test-0123456789ab.plist"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := StatusOptions{ServiceName: "tbd-test-0123456789ab", Runner: r, goos: "darwin", env: env}
	if err := PrintStatus(context.Background(), &out, t.TempDir(), cfg, "dev", o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\nlaunchd:         gui/501/tbd-test-0123456789ab loaded, running (pid 4242)\n") {
		t.Errorf("status:\n%s", out.String())
	}
	out.Reset()
	o.goos = "linux"
	if err := PrintStatus(context.Background(), &out, t.TempDir(), cfg, "dev", o); err != nil || strings.Contains(out.String(), "launchd") {
		t.Errorf("linux status: %v\n%s", err, out.String())
	}
}
