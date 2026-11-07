//go:build darwin

package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These run only on a Mac (GitHub's macOS runners, D26): the real bundle in /Applications, its
// real Info.plist, and the real home directory.

func TestDarwinRealBrowser(t *testing.T) {
	t.Setenv("TBD_BROWSER", "")
	b, err := Default().DetectBrowser()
	if err != nil {
		if os.Getenv("TBD_REQUIRE_BROWSER") == "1" {
			t.Fatalf("no browser: %v", err)
		}
		t.Skipf("no browser: %v", err)
	}
	t.Logf("detected %s via %s", b.Path, b.Source)
	if b.Source != "macos-standard" {
		t.Skipf("not an application bundle (%s): nothing macOS-specific to check", b.Source)
	}
	macos := filepath.Dir(b.Path)
	bundle := filepath.Dir(filepath.Dir(macos))
	if filepath.Base(macos) != "MacOS" || !strings.HasSuffix(bundle, ".app") {
		t.Fatalf("%s is not <bundle>.app/Contents/MacOS/<exe>", b.Path)
	}

	// The bundle directory itself, as a user would write it in config.toml, finds the same file.
	c := Default()
	c.Browser = bundle
	if got, err := c.DetectBrowser(); err != nil || got.Path != b.Path {
		t.Errorf("browser = %q: got %+v %v, want %s", bundle, got, err, b.Path)
	}

	// Info.plist (no process) and --version must agree.
	fromPlist, ok := versionFromInfoPlist(b.Path, os.ReadFile)
	if !ok {
		t.Fatalf("no four-part CFBundleShortVersionString in %s/Contents/Info.plist", bundle)
	}
	out, err := runVersion(context.Background(), b.Path)
	if err != nil {
		t.Fatalf("%s --version: %v", b.Path, err)
	}
	fromRun, ok := ParseVersion(string(out))
	if !ok || fromRun != fromPlist {
		t.Fatalf("Info.plist says %+v, --version says %q", fromPlist, strings.TrimSpace(string(out)))
	}
	if got, err := BrowserVersion(context.Background(), b.Path); err != nil || got != fromPlist {
		t.Fatalf("BrowserVersion: %+v %v, want %+v", got, err, fromPlist)
	}
	t.Logf("version %s", fromPlist.Full)
}

func TestDarwinDefaultHome(t *testing.T) {
	t.Setenv("TBD_HOME", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	h, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveHome("")
	if want := filepath.Join(h, "Library", "Application Support", "tbd"); err != nil || got != want {
		t.Fatalf("got %q %v, want %q", got, err, want)
	}
}
