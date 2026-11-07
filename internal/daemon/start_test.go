package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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
	case line == "systemctl --user start tbd.service" || line == "schtasks /run /tn tbd":
		l, err := AcquireLock(f.home)
		f.lock = l
		return "", err
	}
	return "", errors.New("unexpected " + line)
}

func TestStart(t *testing.T) {
	ctx := context.Background()
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos+" not installed", func(t *testing.T) {
			f := &serviceFake{home: t.TempDir()}
			err := Start(ctx, nil, StartOptions{Home: f.home, Runner: f, goos: goos})
			if err == nil || !strings.Contains(err.Error(), "tbd install") || len(f.calls) != 1 {
				t.Fatalf("%v, calls %q", err, f.calls)
			}
		})
		t.Run(goos+" installed", func(t *testing.T) {
			f := &serviceFake{home: t.TempDir(), installed: true}
			var out bytes.Buffer
			if err := Start(ctx, &out, StartOptions{Home: f.home, Runner: f, goos: goos}); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			defer f.lock.Release()
			if !strings.Contains(out.String(), "Started (pid "+strconv.Itoa(os.Getpid())+")") || len(f.calls) != 2 {
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
