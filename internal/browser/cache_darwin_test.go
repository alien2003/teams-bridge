//go:build darwin

package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/test/faketeams"
)

// TestDarwinDiskCacheStaysInHome runs the real browser with a home under the real
// ~/Library/Application Support (where the default data directory lives) and checks that its disk
// cache lands in <home>/cache, not in ~/Library/Caches/<same name>, outside the data directory,
// where Chromium maps the cache of a profile under Application Support by default (D26), and that
// the empty tree the browser still creates there is gone once it has exited (Process.Close). It
// writes under the real ~/Library, so it runs only on GitHub's runners (GITHUB_ACTIONS=true), and
// removes both paths.
func TestDarwinDiskCacheStaysInHome(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("writes under the real ~/Library: runs on GitHub's macOS runners only")
	}
	path := requireBrowser(t)
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := "tbd-test-" + hex.EncodeToString(b)
	home := filepath.Join(userHome, "Library", "Application Support", name)
	caches := filepath.Join(userHome, "Library", "Caches", name)
	t.Cleanup(func() {
		_ = os.RemoveAll(home)
		_ = os.RemoveAll(caches)
	})

	srv := faketeams.New()
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	proc, err := Launch(ctx, LaunchOptions{Path: path, Home: home, Headless: true, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := Attach(ctx, proc.WSURL, SessionOptions{TeamsURL: srv.TeamsURL(), Logger: testLogger()}, Handlers{})
	if err != nil {
		_ = proc.Close(ctx)
		t.Fatal(err)
	}
	if _, ok := srv.WaitRequest(30*time.Second, func(r faketeams.Request) bool { return r.Path == "/api/v1/poll" }); !ok {
		t.Error("the page never loaded")
	}
	time.Sleep(2 * time.Second) // let the cache write its first entries
	if _, err := os.Lstat(caches); err == nil {
		t.Logf("while the browser runs, %s exists", caches)
	}
	sess.Close()
	if err := proc.Close(ctx); err != nil {
		t.Logf("Close: %v", err)
	}

	if _, err := os.Lstat(caches); err == nil {
		var found []string
		_ = filepath.WalkDir(caches, func(p string, d fs.DirEntry, err error) error {
			if err == nil && len(found) < 20 {
				found = append(found, p)
			}
			return nil
		})
		t.Errorf("after the browser exited, %s is still there (a file in it despite --disk-cache-dir?): %q", caches, found)
	}
	n := 0
	_ = filepath.WalkDir(DiskCacheDir(home), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	if n == 0 {
		t.Errorf("no file in %s: the disk cache went elsewhere", DiskCacheDir(home))
	}
	t.Logf("%d files in the disk cache dir", n)
}
