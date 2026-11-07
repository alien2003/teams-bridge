package browser

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/target"
)

// The per-OS switches, features and environment, pinned for every OS on every OS (Launch uses the
// runtime.GOOS entry; fakebrowser_test.go checks that on the OS it runs on).
func TestPlatformLaunchArgsFor(t *testing.T) {
	home := filepath.Join("/Users/u/Library/Application Support", "tbd")
	cache := "--disk-cache-dir=" + filepath.Join(home, "cache")
	cases := []struct {
		goos     string
		headless bool
		want     []string
	}{
		{"linux", true, []string{"--disable-gpu"}},
		{"linux", false, nil},
		{"freebsd", true, []string{"--disable-gpu"}},
		{"windows", true, nil},
		{"windows", false, nil},
		{"darwin", true, []string{"--disable-gpu", "--use-mock-keychain", cache}},
		{"darwin", false, []string{"--use-mock-keychain", cache}},
	}
	for _, c := range cases {
		got := platformLaunchArgsFor(c.goos, c.headless, home)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s headless=%v: %q, want %q", c.goos, c.headless, got, c.want)
		}
		for _, a := range got {
			if strings.HasPrefix(a, "--password-store") || a == "--no-sandbox" {
				t.Errorf("%s: unexpected %s", c.goos, a)
			}
		}
	}
	if DiskCacheDir(home) != filepath.Join(home, "cache") {
		t.Error("DiskCacheDir")
	}
}

func TestPlatformDisabledFeaturesFor(t *testing.T) {
	for goos, want := range map[string]string{
		"darwin":  "NativeNotifications,SystemNotifications",
		"linux":   "",
		"windows": "",
	} {
		if got := strings.Join(platformDisabledFeaturesFor(goos), ","); got != want {
			t.Errorf("%s: %q, want %q", goos, got, want)
		}
	}
	// Merged with the user's own switch like msImplicitSignin, never a second switch.
	ours := append(append([]string(nil), DisabledEdgeFeatures...), platformDisabledFeaturesFor("darwin")...)
	got := strings.Join(mergeDisableFeatures([]string{"--disable-features=A"}, ours), "|")
	if want := "--disable-features=A,msImplicitSignin,NativeNotifications,SystemNotifications"; got != want {
		t.Errorf("merged: %q, want %q", got, want)
	}
}

func TestPlatformBrowserEnvFor(t *testing.T) {
	home := "/Users/u/Library/Application Support/tbd"
	env := platformBrowserEnvFor("darwin", home)
	if len(env) != 1 || env["BREAKPAD_DUMP_LOCATION"] != CrashesDir(home) {
		t.Errorf("darwin: %v", env)
	}
	for _, goos := range []string{"linux", "windows"} {
		if env := platformBrowserEnvFor(goos, home); len(env) != 0 {
			t.Errorf("%s: %v", goos, env)
		}
	}
}

func TestUAOverrideFor(t *testing.T) {
	ua := UserAgent(150)
	meta := UserAgentMetadata("150.0.1.2")
	for goos, want := range map[string]string{"darwin": "Win32", "linux": "", "windows": ""} {
		p := uaOverrideFor(goos, ua, meta)
		if p.UserAgent != ua || p.UserAgentMetadata != meta || p.Platform != want {
			t.Errorf("%s: %+v, want platform %q", goos, p, want)
		}
	}
	// A non-Chromium UA override travels without metadata, and then without a platform either.
	if p := uaOverrideFor("darwin", "Mozilla/5.0 (Macintosh)", nil); p.Platform != "" || p.UserAgentMetadata != nil {
		t.Errorf("darwin without metadata: %+v", p)
	}
}

func TestLoginLaunchHint(t *testing.T) {
	base := errors.New("browser: exited before the DevTools endpoint appeared")
	if err := loginLaunchHint("darwin", false, "198.51.100.7 50000 203.0.113.9 22", base); !errors.Is(err, base) ||
		!strings.Contains(err.Error(), "Terminal on the Mac itself") {
		t.Errorf("darwin over SSH: %v", err)
	}
	for _, c := range []struct {
		goos     string
		headless bool
		ssh      string
	}{{"darwin", false, ""}, {"darwin", true, "x"}, {"linux", false, "x"}, {"windows", false, "x"}} {
		if err := loginLaunchHint(c.goos, c.headless, c.ssh, base); err != base {
			t.Errorf("%+v: %v, want the error unchanged", c, err)
		}
	}
}

func TestCountPages(t *testing.T) {
	infos := []*target.Info{
		{Type: "page"}, {Type: "service_worker"}, {Type: "iframe"}, nil, {Type: "page"}, {Type: "browser"},
	}
	if n := countPages(infos); n != 2 {
		t.Errorf("countPages = %d, want 2", n)
	}
	if countPages(nil) != 0 {
		t.Error("countPages(nil)")
	}
}

func TestLockPID(t *testing.T) {
	cases := map[string]int{
		"host-1234":              1234,
		"my-mac-mini.local-4242": 4242,
		"h-2":                    2,
	}
	for in, want := range cases {
		if got, ok := lockPID(in); !ok || got != want {
			t.Errorf("lockPID(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "host", "host-", "-12", "host-1", "host-0", "host-12a", "host-+12", "host-012"} {
		if got, ok := lockPID(in); ok {
			t.Errorf("lockPID(%q) = %d, want no PID", in, got)
		}
	}
}

// procArgs2 builds a kern.procargs2 buffer as macOS lays it out.
func procArgs2(argc int32, exe string, pad int, args []string, env []string) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(argc))
	b = append(b, exe...)
	b = append(b, make([]byte, 1+pad)...)
	for _, a := range args {
		b = append(append(b, a...), 0)
	}
	for _, e := range env {
		b = append(append(b, e...), 0)
	}
	return b
}

func TestParseProcArgs2(t *testing.T) {
	exe := "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"
	args := []string{exe, "--remote-debugging-port=0", "--user-data-dir=/Users/u/Library/Application Support/tbd/profile", ""}
	got, err := parseProcArgs2(procArgs2(int32(len(args)), exe, 7, args, []string{"HOME=/Users/u", "PATH=/usr/bin"}))
	if err != nil || strings.Join(got, "|") != strings.Join(args, "|") {
		t.Errorf("parse = %q, %v; want %q", got, err, args)
	}
	// The last argument may end the buffer without its NUL.
	b := procArgs2(2, "/bin/sleep", 0, []string{"sleep", "300"}, nil)
	if got, err := parseProcArgs2(b[:len(b)-1]); err != nil || strings.Join(got, "|") != "sleep|300" {
		t.Errorf("unterminated last argument: %q, %v", got, err)
	}
	bad := map[string][]byte{
		"short":       {1, 0},
		"negative":    procArgs2(-1, "/x", 0, nil, nil),
		"huge argc":   procArgs2(1<<20, "/x", 0, []string{"a"}, nil),
		"no exe end":  append(binary.LittleEndian.AppendUint32(nil, 1), "/no/nul"...),
		"truncated":   procArgs2(3, "/x", 0, []string{"a"}, nil),
		"argc no arg": procArgs2(1, "/x", 2, nil, nil),
	}
	for name, b := range bad {
		if got, err := parseProcArgs2(b); err == nil {
			t.Errorf("%s: parsed %q, want an error", name, got)
		}
	}
}
