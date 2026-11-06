package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp/remote"

	"github.com/alien2003/teams-bridge/internal/browser"
	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/fcm/fcmtest"
	"github.com/alien2003/teams-bridge/internal/proto"
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
	st, err := exec.Command(exe, "--home", home, "status").CombinedOutput()
	if err != nil || !strings.Contains(string(st), "1 sent in total") {
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

// TestBinaryLoginWindowed is BRIEF §7's `tbd login` against faketeams: a windowed run that the
// test closes over CDP once the page has been on teams_url long enough to count as signed in.
func TestBinaryLoginWindowed(t *testing.T) {
	if runtime.GOOS != "windows" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no display: the windowed login runs on the Windows host (and on Linux with WSLg)")
	}
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
	closeWindow(t, ws)
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

func closeWindow(t *testing.T, wsURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := remote.DialContext(ctx, wsURL)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer conn.Close()
	if err := conn.Write(ctx, &cdproto.Message{ID: 1, Method: cdproto.MethodType(cdpbrowser.CommandClose)}); err != nil {
		t.Fatalf("Browser.close: %v", err)
	}
	_ = conn.Read(ctx, &cdproto.Message{})
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
