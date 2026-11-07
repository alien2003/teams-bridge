//go:build !windows

package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TBD_FAKE_ARGS\"\nprintf '%s\\n' \"TMP=$TMP\" \"TEMP=$TEMP\" \"TMPDIR=$TMPDIR\" \"BREAKPAD_DUMP_LOCATION=$BREAKPAD_DUMP_LOCATION\" > \"$TBD_FAKE_ARGS.env\"\n" + body + "\n"
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
	envb, err := os.ReadFile(argsFile + ".env")
	if err != nil {
		t.Fatal(err)
	}
	breakpad := os.Getenv("BREAKPAD_DUMP_LOCATION")
	if runtime.GOOS == "darwin" {
		breakpad = CrashesDir(home)
	}
	if want := "TMP=" + os.Getenv("TMP") + "\nTEMP=" + os.Getenv("TEMP") + "\nTMPDIR=" + os.Getenv("TMPDIR") +
		"\nBREAKPAD_DUMP_LOCATION=" + breakpad + "\n"; string(envb) != want {
		t.Errorf("browser env (temp inherited outside Windows; crash dumps into the home on macOS):\n%s\nwant:\n%s", envb, want)
	}
	if _, err := os.Stat(TempDir(home)); !os.IsNotExist(err) {
		t.Errorf("a temp dir was created outside Windows: %v", err)
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
	}
	// The per-OS lists themselves are pinned in platform_test.go; this checks that Launch uses this
	// OS's list, in this place.
	want = append(want, platformLaunchArgsFor(runtime.GOOS, true, home)...)
	want = append(want,
		"--disable-features="+strings.Join(append([]string{"msImplicitSignin"}, platformDisabledFeaturesFor(runtime.GOOS)...), ","),
		"--extra",
		"about:blank",
	)
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Errorf("args =\n%s\nwant\n%s", strings.Join(args, "\n"), strings.Join(want, "\n"))
	}
	mock := false
	for _, a := range args {
		if a == "--no-sandbox" || a == "--disable-dev-shm-usage" {
			t.Errorf("unexpected switch %s", a)
		}
		mock = mock || a == "--use-mock-keychain"
	}
	if mock != (runtime.GOOS == "darwin") {
		t.Errorf("--use-mock-keychain present = %v on %s (macOS only, D26)", mock, runtime.GOOS)
	}
}

// TestLaunchImplicitSigninKeepsPlatformFeatures: implicit_signin drops msImplicitSignin, but the
// features this OS always disables (macOS: native notifications) stay.
func TestLaunchImplicitSigninKeepsPlatformFeatures(t *testing.T) {
	path, argsFile := fakeBrowser(t, `echo "DevTools listening on ws://127.0.0.1:1/devtools/browser/fake" >&2
sleep 1 & wait`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := Launch(ctx, LaunchOptions{
		Path: path, Home: t.TempDir(), ImplicitSignin: true,
		Env: append(os.Environ(), "TBD_FAKE_ARGS="+argsFile), Logger: testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(ctx)
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(a, "--disable-features=") {
			got = append(got, a)
		}
	}
	var want []string
	if f := platformDisabledFeaturesFor(runtime.GOOS); len(f) > 0 {
		want = []string{"--disable-features=" + strings.Join(f, ",")}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("--disable-features with implicit_signin: %q, want %q", got, want)
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
