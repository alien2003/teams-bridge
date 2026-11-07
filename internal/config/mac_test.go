package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The macOS code paths are plain functions of GOOS, so these tests run on every OS (D26). Paths
// are built with filepath, so on a Windows host "/Applications" reads `\Applications`.

// macApp is <dir>/<app>/Contents/MacOS/<exe>.
func macApp(dir, app, exe string) string { return filepath.Join(dir, app, "Contents", "MacOS", exe) }

var sysApps = filepath.Join(string(filepath.Separator), "Applications")

// infoPlist is an XML Info.plist in the shape Chromium writes, with decoys in a nested dictionary.
func infoPlist(exe, version string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDocumentTypes</key>
	<array>
		<dict>
			<key>CFBundleExecutable</key>
			<string>decoy</string>
			<key>CFBundleShortVersionString</key>
			<string>1.2.3.4</string>
		</dict>
	</array>
	<key>CFBundleDisplayName</key>
	<string>Microsoft Edge</string>
	<key>CFBundleExecutable</key>
	<string>` + exe + `</string>
	<key>CFBundleShortVersionString</key>
	<string>` + version + `</string>
	<key>LSMinimumSystemVersion</key>
	<string>12.0</string>
</dict>
</plist>
`
}

type macFS struct {
	files  map[string]bool   // regular files
	plists map[string]string // readable files and their contents
}

func (m *macFS) env(goos string, home string, homeErr error) env {
	return env{
		getenv:   func(string) string { return "" },
		lookPath: func(n string) (string, error) { return m.look(n) },
		exists:   func(p string) bool { return m.files[p] },
		readFile: func(p string) ([]byte, error) {
			if s, ok := m.plists[p]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
		homeDir: func() (string, error) { return home, homeErr },
		goos:    goos,
		starts:  []string{root("repo", "x")},
	}
}

func (m *macFS) look(n string) (string, error) {
	p := root("usr", "local", "bin", n)
	if m.files[p] {
		return p, nil
	}
	return "", errors.New("not found")
}

func TestDetectBrowserDarwinOrder(t *testing.T) {
	home := root("Users", "u")
	userApps := filepath.Join(home, "Applications")
	m := &macFS{files: map[string]bool{}, plists: map[string]string{}}
	e := m.env("darwin", home, nil)
	c := Default()
	check := func(step, want, src string) {
		t.Helper()
		b, err := c.detectBrowser(e)
		if want == "" {
			if !errors.Is(err, ErrNoBrowser) {
				t.Fatalf("%s: want ErrNoBrowser, got %+v %v", step, b, err)
			}
			return
		}
		if err != nil || b.Path != want || b.Source != src {
			t.Fatalf("%s: got %+v %v, want %s (%s)", step, b, err, want, src)
		}
	}

	check("nothing", "", "")
	dev := root("repo", DevBrowserRel)
	m.files[dev] = true
	check("dev wrapper last", dev, "repo .tools (dev)")
	brew := root("usr", "local", "bin", "chromium")
	m.files[brew] = true
	check("PATH before the dev wrapper", brew, "PATH")

	chrome := macApp(sysApps, "Google Chrome.app", "Google Chrome")
	m.files[chrome] = true
	check("a bundle beats PATH", chrome, "macos-standard")
	chromium := macApp(userApps, "Chromium.app", "Chromium")
	m.files[chromium] = true
	check("Chromium in ~/Applications beats Chrome in /Applications", chromium, "macos-standard")
	userEdge := macApp(userApps, "Microsoft Edge.app", "Microsoft Edge")
	m.files[userEdge] = true
	check("Edge in ~/Applications beats Chromium", userEdge, "macos-standard")
	edge := macApp(sysApps, "Microsoft Edge.app", "Microsoft Edge")
	m.files[edge] = true
	check("/Applications beats ~/Applications for the same browser", edge, "macos-standard")

	// Without a usable home directory ~/Applications is not searched.
	delete(m.files, edge)
	for _, bad := range []env{m.env("darwin", "", errors.New("no home")), m.env("darwin", "rel", nil)} {
		e = bad
		check("no home: Chrome in /Applications", chrome, "macos-standard")
	}

	// Linux never looks at bundles; Windows neither.
	e = m.env("linux", home, nil)
	check("linux ignores bundles", brew, "PATH")
	e = m.env("windows", home, nil)
	check("windows ignores bundles and PATH", dev, "repo .tools (dev)")
}

func TestDetectBrowserAppBundle(t *testing.T) {
	bundle := filepath.Join(sysApps, "Microsoft Edge Beta.app")
	exe := macApp(sysApps, "Microsoft Edge Beta.app", "Microsoft Edge Beta")
	m := &macFS{
		files:  map[string]bool{exe: true},
		plists: map[string]string{filepath.Join(bundle, "Contents", "Info.plist"): infoPlist("Microsoft Edge Beta", "158.0.4400.10")},
	}
	e := m.env("darwin", root("Users", "u"), nil)
	for _, v := range []string{bundle, bundle + string(filepath.Separator)} {
		c := Default()
		c.Browser = v
		if b, err := c.detectBrowser(e); err != nil || b.Path != exe || b.Source != "config" {
			t.Errorf("config %q: got %+v %v, want %s", v, b, err, exe)
		}
		c.Browser = ""
		e.getenv = func(k string) string { return map[string]string{"TBD_BROWSER": v}[k] }
		if b, err := c.detectBrowser(e); err != nil || b.Path != exe || b.Source != "TBD_BROWSER" {
			t.Errorf("TBD_BROWSER %q: got %+v %v, want %s", v, b, err, exe)
		}
		e.getenv = func(string) string { return "" }
	}

	// CFBundleExecutable names the executable even when it differs from the bundle's name.
	odd := filepath.Join(root("Users", "u", "Applications"), "Teams Browser.app")
	oddExe := filepath.Join(odd, "Contents", "MacOS", "Microsoft Edge")
	m.files[oddExe] = true
	m.plists[filepath.Join(odd, "Contents", "Info.plist")] = infoPlist("Microsoft Edge", "157.0.4341.42")
	c := Default()
	c.Browser = odd
	if b, err := c.detectBrowser(e); err != nil || b.Path != oddExe {
		t.Errorf("CFBundleExecutable: got %+v %v, want %s", b, err, oddExe)
	}

	// No Info.plist (or a hostile CFBundleExecutable): the bundle's name without .app.
	plain := filepath.Join(sysApps, "Chromium.app")
	plainExe := macApp(sysApps, "Chromium.app", "Chromium")
	m.files[plainExe] = true
	c.Browser = plain
	if b, err := c.detectBrowser(e); err != nil || b.Path != plainExe {
		t.Errorf("no Info.plist: got %+v %v, want %s", b, err, plainExe)
	}
	m.plists[filepath.Join(plain, "Contents", "Info.plist")] = infoPlist("../../../evil", "157.0.4341.42")
	if b, err := c.detectBrowser(e); err != nil || b.Path != plainExe {
		t.Errorf("CFBundleExecutable with a path: got %+v %v, want %s", b, err, plainExe)
	}

	// A bundle without its executable, a regular file named .app, and Linux.
	c.Browser = filepath.Join(sysApps, "Missing.app")
	if _, err := c.detectBrowser(e); err == nil || !strings.Contains(err.Error(), "Missing.app") {
		t.Errorf("missing bundle: %v", err)
	}
	file := root("opt", "odd.app")
	m.files[file] = true
	c.Browser = file
	if b, err := c.detectBrowser(e); err != nil || b.Path != file {
		t.Errorf("a regular file named .app is used as is: %+v %v", b, err)
	}
	c.Browser = bundle
	if _, err := c.detectBrowser(m.env("linux", root("home", "u"), nil)); err == nil {
		t.Error("linux must not resolve .app bundles")
	}
	for _, p := range []string{".app", root("x", ".app"), root("x", "Edge"), root("x", "Edge.App")} {
		got := resolveAppBundle(p, nil)
		if strings.EqualFold(filepath.Ext(p), ".app") && filepath.Base(p) != ".app" {
			if want := filepath.Join(p, "Contents", "MacOS", "Edge"); got != want {
				t.Errorf("resolveAppBundle(%q) = %q, want %q", p, got, want)
			}
		} else if got != p {
			t.Errorf("resolveAppBundle(%q) = %q, want it unchanged", p, got)
		}
	}
}

func TestPlistString(t *testing.T) {
	good := infoPlist("Microsoft Edge", "157.0.4341.42")
	for _, tc := range []struct {
		name, raw, key, want string
		ok                   bool
	}{
		{"top-level key", good, "CFBundleShortVersionString", "157.0.4341.42", true},
		{"nested decoy is skipped", good, "CFBundleExecutable", "Microsoft Edge", true},
		{"missing key", good, "CFBundleVersion", "", false},
		{"only nested", `<plist><dict><key>A</key><dict><key>K</key><string>v</string></dict></dict></plist>`, "K", "", false},
		{"non-string value", `<plist><dict><key>K</key><integer>3</integer></dict></plist>`, "K", "", false},
		{"key after a nested dict", `<plist><dict><key>A</key><dict><key>K</key><string>no</string></dict><key>K</key><string> yes </string></dict></plist>`, "K", "yes", true},
		{"escaped text", `<plist><dict><key>K</key><string>a &amp; b</string></dict></plist>`, "K", "a & b", true},
		{"binary plist", "bplist00\xd1\x01\x02_\x10\x1aCFBundleShortVersionString", "CFBundleShortVersionString", "", false},
		{"not XML", "{ CFBundleShortVersionString = 157.0.4341.42; }", "CFBundleShortVersionString", "", false},
		{"truncated", good[:len(good)/2], "LSMinimumSystemVersion", "", false},
		{"empty", "", "K", "", false},
	} {
		got, ok := plistString([]byte(tc.raw), tc.key)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %q %v, want %q %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBrowserVersionDarwin(t *testing.T) {
	ctx := context.Background()
	exe := macApp(sysApps, "Microsoft Edge.app", "Microsoft Edge")
	plist := filepath.Join(sysApps, "Microsoft Edge.app", "Contents", "Info.plist")
	reads := 0
	files := map[string]string{}
	readFile := func(p string) ([]byte, error) {
		reads++
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
	runs := 0
	runOut, runErr := "Microsoft Edge 157.0.4341.43 \n", error(nil)
	run := func(_ context.Context, p string) ([]byte, error) {
		runs++
		return []byte(runOut), runErr
	}
	check := func(name, path, goos, want string, wantRuns int) {
		t.Helper()
		reads, runs = 0, 0
		v, err := browserVersion(ctx, path, goos, readFile, run)
		if err != nil || v.Full != want || runs != wantRuns {
			t.Errorf("%s: got %+v %v (runs %d), want %s (runs %d)", name, v, err, runs, want, wantRuns)
		}
	}

	files[plist] = infoPlist("Microsoft Edge", "157.0.4341.42")
	check("Info.plist, no process", exe, "darwin", "157.0.4341.42", 0)
	files[plist] = "bplist00\x00\x01"
	check("binary plist falls back to --version", exe, "darwin", "157.0.4341.43", 1)
	files[plist] = `<plist><dict><key>CFBundleName</key><string>Microsoft Edge</string></dict></plist>`
	check("missing key falls back", exe, "darwin", "157.0.4341.43", 1)
	files[plist] = infoPlist("Microsoft Edge", "157.0.4341")
	check("three-part version falls back", exe, "darwin", "157.0.4341.43", 1)
	files[plist] = infoPlist("Microsoft Edge", "157.0.4341.42 beta")
	check("trailing text falls back", exe, "darwin", "157.0.4341.43", 1)
	delete(files, plist)
	check("unreadable falls back", exe, "darwin", "157.0.4341.43", 1)

	files[plist] = infoPlist("Microsoft Edge", "157.0.4341.42")
	check("linux never reads Info.plist", exe, "linux", "157.0.4341.43", 1)
	if reads != 0 {
		t.Errorf("linux read %d files", reads)
	}
	brew := root("usr", "local", "bin", "chromium")
	check("not inside a bundle", brew, "darwin", "157.0.4341.43", 1)
	if reads != 0 {
		t.Errorf("a path outside a bundle read %d files", reads)
	}

	delete(files, plist)
	runErr = errors.New("exit status 1")
	reads, runs = 0, 0
	if _, err := browserVersion(ctx, exe, "darwin", readFile, run); err == nil || !strings.Contains(err.Error(), "--version") {
		t.Errorf("both sources failing: %v", err)
	}
	runOut, runErr = "Microsoft Edge\n", nil
	if _, err := browserVersion(ctx, exe, "darwin", readFile, run); err == nil || !strings.Contains(err.Error(), "no version") {
		t.Errorf("--version without a version: %v", err)
	}
}
