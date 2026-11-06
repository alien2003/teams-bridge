//go:build !windows

package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeBrowser writes a shell script that records its arguments, prints a DevTools line (on a port
// nothing listens on, so a graceful close cannot work) and then runs body.
func fakeBrowser(t *testing.T, body string) (path, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	path = filepath.Join(dir, "browser.sh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TBD_FAKE_ARGS\"\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, argsFile
}

func TestLaunchArgsAndStalePortFile(t *testing.T) {
	path, argsFile := fakeBrowser(t, `echo "DevTools listening on ws://127.0.0.1:1/devtools/browser/fake" >&2
sleep 1 & wait`)
	home := t.TempDir()
	stale := filepath.Join(ProfileDir(home), "DevToolsActivePort")
	if err := os.MkdirAll(ProfileDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("9\n/devtools/browser/stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ua := UserAgent(157)
	p, err := Launch(ctx, LaunchOptions{
		Path: path, Home: home, Headless: true, UserAgent: ua, ExtraArgs: []string{"--extra"},
		Env: append(os.Environ(), "TBD_FAKE_ARGS="+argsFile), Logger: testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(ctx)
	if p.WSURL != "ws://127.0.0.1:1/devtools/browser/fake" {
		t.Errorf("WSURL %q: the stale DevToolsActivePort or the wrong line was used", p.WSURL)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale DevToolsActivePort not removed: %v", err)
	}

	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + ProfileDir(home),
		"--profile-directory=tbd",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-crash-reporter",
		"--crash-dumps-dir=" + CrashesDir(home),
		"--headless=new",
		"--user-agent=" + ua,
		"--disable-gpu",
		"--disable-features=msImplicitSignin",
		"--extra",
		"about:blank",
	}
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Errorf("args =\n%s\nwant\n%s", strings.Join(args, "\n"), strings.Join(want, "\n"))
	}
	for _, a := range args {
		if a == "--no-sandbox" || a == "--disable-dev-shm-usage" {
			t.Errorf("unexpected switch %s", a)
		}
	}
}

// TestCloseKillsUnresponsiveBrowser: a browser that ignores Browser.close is killed, with its
// whole process group, when the Close context ends.
func TestCloseKillsUnresponsiveBrowser(t *testing.T) {
	path, argsFile := fakeBrowser(t, `echo "DevTools listening on ws://127.0.0.1:1/devtools/browser/fake" >&2
sleep 300 &
echo $! > "$TBD_FAKE_ARGS.child"
wait`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := Launch(ctx, LaunchOptions{
		Path: path, Home: t.TempDir(), Env: append(os.Environ(), "TBD_FAKE_ARGS="+argsFile), Logger: testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var child int
	for i := 0; i < 100 && child == 0; i++ {
		if b, err := os.ReadFile(argsFile + ".child"); err == nil {
			_, _ = fmt.Sscan(strings.TrimSpace(string(b)), &child)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("fake browser did not start its child")
	}

	cctx, ccancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer ccancel()
	start := time.Now()
	_ = p.Close(cctx)
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("Close took %s", d)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("process still running after Close")
	}
	// The child in the same process group is gone too (allow the kernel a moment to reap it).
	for i := 0; i < 50; i++ {
		if syscall.Kill(child, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("child %d of the browser survived Close", child)
}

func TestLaunchSandboxHint(t *testing.T) {
	path, argsFile := fakeBrowser(t, `echo "[1:1:FATAL:zygote_host_impl_linux.cc(1)] No usable sandbox! Update your kernel" >&2
exit 1`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := Launch(ctx, LaunchOptions{
		Path: path, Home: t.TempDir(), Env: append(os.Environ(), "TBD_FAKE_ARGS="+argsFile), Logger: testLogger(),
	})
	if err == nil || !strings.Contains(err.Error(), "--no-sandbox") {
		t.Fatalf("want a --no-sandbox hint, got %v", err)
	}
}
