package config

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Browser is a detected browser executable.
type Browser struct {
	Path   string
	Source string // "config", "TBD_BROWSER", "windows-standard", "macos-standard", "PATH", "repo .tools (dev)"
}

// ErrNoBrowser means detection found nothing.
var ErrNoBrowser = errors.New("no Edge/Chromium found: install Microsoft Edge or Chromium, or set `browser` in config.toml")

type env struct {
	getenv   func(string) string
	lookPath func(string) (string, error)
	exists   func(string) bool
	readFile func(string) ([]byte, error)
	homeDir  func() (string, error)
	goos     string
	starts   []string // directories to search upwards for .tools/edge (cwd, executable dir)
}

func defaultEnv() env {
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	return env{
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		exists: func(p string) bool {
			st, err := os.Stat(p)
			return err == nil && !st.IsDir()
		},
		readFile: os.ReadFile,
		homeDir:  os.UserHomeDir,
		goos:     runtime.GOOS,
		starts:   starts,
	}
}

// LinuxCandidates are looked up on PATH, in order.
var LinuxCandidates = []string{"microsoft-edge", "microsoft-edge-stable", "chromium", "chromium-browser", "google-chrome"}

// MacCandidates are the application bundles looked for on macOS, in order (the same priority as
// LinuxCandidates): App is the bundle directory, Exe the executable in its Contents/MacOS. Each is
// tried in /Applications, then in ~/Applications. Beta, Dev and Canary channels are not
// auto-detected; set `browser` for them.
var MacCandidates = []struct{ App, Exe string }{
	{"Microsoft Edge.app", "Microsoft Edge"},
	{"Chromium.app", "Chromium"},
	{"Google Chrome.app", "Google Chrome"},
}

// macAppDirs are the system-wide application folder and the user's own one (under home).
func macAppDirs(home string) []string {
	dirs := []string{filepath.Join(string(filepath.Separator), "Applications")}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	return dirs
}

// macBundleExe is the executable of the application bundle app inside dir.
func macBundleExe(dir, app, exe string) string {
	return filepath.Join(dir, app, "Contents", "MacOS", exe)
}

// DevBrowserRel is the dev-only browser wrapper, relative to the repository root (BRIEF §1.4).
var DevBrowserRel = filepath.Join(".tools", "edge", "tbd-edge")

// DetectBrowser finds the browser: config → $TBD_BROWSER → the standard places (msedge.exe under
// Program Files/LOCALAPPDATA on Windows, the MacCandidates bundles on macOS) → LinuxCandidates on
// PATH (not on Windows) → .tools/edge in the repository (dev only). On macOS a `.app` bundle given
// in config or TBD_BROWSER stands for the executable inside it.
func (c *Config) DetectBrowser() (Browser, error) { return c.detectBrowser(defaultEnv()) }

func (c *Config) detectBrowser(e env) (Browser, error) {
	if c.Browser != "" {
		p := e.explicit(c.Browser)
		if !e.exists(p) {
			return Browser{}, fmt.Errorf("config: browser %q does not exist", c.Browser)
		}
		return Browser{Path: p, Source: "config"}, nil
	}
	if v := e.getenv("TBD_BROWSER"); v != "" {
		p := e.explicit(v)
		if !e.exists(p) {
			return Browser{}, fmt.Errorf("TBD_BROWSER=%q does not exist", v)
		}
		return Browser{Path: p, Source: "TBD_BROWSER"}, nil
	}
	if e.goos == "darwin" {
		home := ""
		if e.homeDir != nil {
			if h, err := e.homeDir(); err == nil && filepath.IsAbs(h) {
				home = h
			}
		}
		for _, cand := range MacCandidates {
			for _, dir := range macAppDirs(home) {
				if p := macBundleExe(dir, cand.App, cand.Exe); e.exists(p) {
					return Browser{Path: p, Source: "macos-standard"}, nil
				}
			}
		}
	}
	if e.goos == "windows" {
		for _, base := range []string{e.getenv("ProgramFiles(x86)"), e.getenv("ProgramFiles"), e.getenv("LOCALAPPDATA")} {
			if base == "" {
				continue
			}
			p := filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe")
			if e.exists(p) {
				return Browser{Path: p, Source: "windows-standard"}, nil
			}
		}
	} else {
		for _, name := range LinuxCandidates {
			if p, err := e.lookPath(name); err == nil {
				return Browser{Path: p, Source: "PATH"}, nil
			}
		}
	}
	for _, start := range e.starts {
		for d := start; ; {
			p := filepath.Join(d, DevBrowserRel)
			if e.exists(p) {
				return Browser{Path: p, Source: "repo .tools (dev)"}, nil
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	return Browser{}, ErrNoBrowser
}

// explicit maps a configured browser path to the executable: on macOS a `.app` bundle directory
// (which is not a regular file) becomes the executable inside it; anything else is used as is.
func (e env) explicit(p string) string {
	if e.goos != "darwin" || e.exists(p) {
		return p
	}
	return resolveAppBundle(p, e.readFile)
}

// resolveAppBundle returns <bundle>/Contents/MacOS/<CFBundleExecutable> for a path ending in .app
// (the executable's name falls back to the bundle's name without .app), else p unchanged.
func resolveAppBundle(p string, readFile func(string) ([]byte, error)) string {
	b := filepath.Clean(p) // drops a trailing slash
	name := filepath.Base(b)
	if len(name) <= len(".app") || !strings.EqualFold(filepath.Ext(name), ".app") {
		return p
	}
	exe := strings.TrimSuffix(name, filepath.Ext(name))
	if readFile != nil {
		if raw, err := readFile(filepath.Join(b, "Contents", "Info.plist")); err == nil {
			if v, ok := plistString(raw, "CFBundleExecutable"); ok && v != "" && !strings.ContainsAny(v, `/\`) && v != "." && v != ".." {
				exe = v
			}
		}
	}
	return filepath.Join(b, "Contents", "MacOS", exe)
}

// plistString returns the string value of key in the top-level dictionary of an XML property
// list. A binary property list (bplist00), malformed XML, a missing key or a non-string value
// yields ok=false.
func plistString(raw []byte, key string) (string, bool) {
	if bytes.HasPrefix(raw, []byte("bplist")) {
		return "", false
	}
	d := xml.NewDecoder(bytes.NewReader(raw))
	var stack []string
	var lastKey string
	haveKey := false
	top := func() bool { return len(stack) == 2 && stack[0] == "plist" && stack[1] == "dict" }
	for {
		tok, err := d.Token()
		if err != nil { // io.EOF included: the key is not there
			return "", false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if top() {
				if t.Name.Local == "key" {
					var k string
					if err := d.DecodeElement(&k, &t); err != nil {
						return "", false
					}
					lastKey, haveKey = strings.TrimSpace(k), true
					continue
				}
				// A value of the top dictionary.
				if haveKey && lastKey == key {
					if t.Name.Local != "string" {
						return "", false
					}
					var v string
					if err := d.DecodeElement(&v, &t); err != nil {
						return "", false
					}
					return strings.TrimSpace(v), true
				}
				haveKey = false
			}
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
}

var versionRE = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)\.(\d+)`)

// Version is a browser version.
type Version struct {
	Full  string // e.g. 157.0.4341.42
	Major int
}

// ParseVersion extracts the first a.b.c.d version from s.
func ParseVersion(s string) (Version, bool) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	major, err := strconv.Atoi(m[1])
	if err != nil || major == 0 {
		return Version{}, false
	}
	return Version{Full: m[0], Major: major}, true
}

// BrowserVersion reads the version of the browser at path without starting a browser window:
// on Windows (where msedge.exe --version opens a window instead of printing) the version-named
// directory next to the executable; on macOS CFBundleShortVersionString from the bundle's
// Contents/Info.plist when path is <bundle>/Contents/MacOS/<exe> (no process, so no Gatekeeper
// scan and no keychain access), else, and on Linux, `--version`.
func BrowserVersion(ctx context.Context, path string) (Version, error) {
	return browserVersion(ctx, path, runtime.GOOS, os.ReadFile, runVersion)
}

func runVersion(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, "--version").Output()
}

func browserVersion(ctx context.Context, path, goos string, readFile func(string) ([]byte, error), run func(context.Context, string) ([]byte, error)) (Version, error) {
	if goos == "windows" {
		return versionFromDir(filepath.Dir(path))
	}
	if goos == "darwin" {
		if v, ok := versionFromInfoPlist(path, readFile); ok {
			return v, nil
		}
	}
	out, err := run(ctx, path)
	if err != nil {
		return Version{}, fmt.Errorf("%s --version: %w", path, err)
	}
	v, ok := ParseVersion(string(out))
	if !ok {
		return Version{}, fmt.Errorf("%s --version: no version in %q", path, strings.TrimSpace(string(out)))
	}
	return v, nil
}

// versionFromInfoPlist reads CFBundleShortVersionString of the bundle whose executable is exe
// (<bundle>/Contents/MacOS/<exe>). Chromium-based browsers write the full four-part version there,
// in XML. ok=false: not inside a bundle, unreadable, binary, no such key, or not a.b.c.d.
func versionFromInfoPlist(exe string, readFile func(string) ([]byte, error)) (Version, bool) {
	macos := filepath.Dir(exe)
	contents := filepath.Dir(macos)
	if filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" {
		return Version{}, false
	}
	raw, err := readFile(filepath.Join(contents, "Info.plist"))
	if err != nil {
		return Version{}, false
	}
	s, ok := plistString(raw, "CFBundleShortVersionString")
	if !ok {
		return Version{}, false
	}
	v, ok := ParseVersion(s)
	if !ok || v.Full != s {
		return Version{}, false
	}
	return v, true
}

func versionFromDir(dir string) (Version, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return Version{}, err
	}
	var vs []Version
	for _, e := range ents {
		if v, ok := ParseVersion(e.Name()); ok && e.IsDir() && v.Full == e.Name() {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return Version{}, fmt.Errorf("no version directory next to the browser in %s", dir)
	}
	sort.Slice(vs, func(i, j int) bool { return versionLess(vs[i].Full, vs[j].Full) })
	return vs[len(vs)-1], nil
}

func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range pa {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}
