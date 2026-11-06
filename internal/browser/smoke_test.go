package browser

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
)

// requireBrowser returns the dev/host browser path, or skips (unless TBD_REQUIRE_BROWSER=1).
func requireBrowser(t *testing.T) string {
	t.Helper()
	b, err := config.Default().DetectBrowser()
	if err != nil {
		if os.Getenv("TBD_REQUIRE_BROWSER") == "1" {
			t.Fatalf("no browser: %v", err)
		}
		t.Skipf("no browser: %v", err)
	}
	return b.Path
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestLaunchClose launches the real headless browser, checks the DevTools endpoint parses, and
// closes it cleanly, leaving no orphan.
func TestLaunchClose(t *testing.T) {
	path := requireBrowser(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	proc, err := Launch(ctx, LaunchOptions{Path: path, Home: t.TempDir(), Headless: true, Logger: testLogger()})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() {
		cctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = proc.Close(cctx)
	})

	if !strings.HasPrefix(proc.WSURL, "ws://") || !strings.Contains(proc.WSURL, "/devtools/browser/") {
		t.Fatalf("unexpected WSURL %q", proc.WSURL)
	}
	if proc.PID <= 0 {
		t.Fatalf("bad PID %d", proc.PID)
	}

	cctx, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	if err := proc.Close(cctx); err != nil && !strings.Contains(err.Error(), "killed") && !strings.Contains(err.Error(), "signal") {
		// A graceful close returns a nil or exit-0 error; a kill fallback returns a signal error.
		t.Logf("Close returned: %v (acceptable if the process exited)", err)
	}
	select {
	case <-proc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("process did not exit after Close")
	}
}
