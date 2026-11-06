package browser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWSLineRegexp(t *testing.T) {
	cases := map[string]string{
		"DevTools listening on ws://127.0.0.1:35353/devtools/browser/abc-123":                       "ws://127.0.0.1:35353/devtools/browser/abc-123",
		"[1:1:0101/120000.000:INFO] DevTools listening on ws://127.0.0.1:9/devtools/browser/x more": "ws://127.0.0.1:9/devtools/browser/x",
	}
	for line, want := range cases {
		m := wsLineRE.FindStringSubmatch(line)
		if m == nil || m[1] != want {
			t.Errorf("line %q: got %v, want %q", line, m, want)
		}
	}
	if wsLineRE.MatchString("nothing here") {
		t.Error("matched a line with no DevTools URL")
	}
}

func TestPollDevToolsActivePort(t *testing.T) {
	profile := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := make(chan string, 1)
	go pollDevToolsActivePort(ctx, profile, out)

	// Write the file a moment later, as the browser would.
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(profile, "DevToolsActivePort"), []byte("35123\n/devtools/browser/zzz\n"), 0o600)
	}()

	select {
	case ws := <-out:
		if ws != "ws://127.0.0.1:35123/devtools/browser/zzz" {
			t.Fatalf("got %q", ws)
		}
	case <-ctx.Done():
		t.Fatal("DevToolsActivePort fallback did not fire")
	}
}

func TestPollDevToolsActivePortPortOnly(t *testing.T) {
	profile := t.TempDir()
	if err := os.WriteFile(filepath.Join(profile, "DevToolsActivePort"), []byte("40000"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out := make(chan string, 1)
	go pollDevToolsActivePort(ctx, profile, out)
	select {
	case ws := <-out:
		if ws != "ws://127.0.0.1:40000/" {
			t.Fatalf("got %q", ws)
		}
	case <-ctx.Done():
		t.Fatal("no result for a port-only file")
	}
}

func TestProfileAndCrashesDir(t *testing.T) {
	if ProfileDir("/home/x") != filepath.Join("/home/x", "profile") {
		t.Error("ProfileDir")
	}
	if CrashesDir("/home/x") != filepath.Join("/home/x", "crashes") {
		t.Error("CrashesDir")
	}
}

func TestLaunchNoPath(t *testing.T) {
	if _, err := Launch(context.Background(), LaunchOptions{Home: t.TempDir()}); err == nil {
		t.Fatal("Launch without a path should fail")
	}
}
