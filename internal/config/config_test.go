package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alien2003/teams-bridge/internal/proto"
)

func TestResolveHome(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	home := func() (string, error) { return "/home/u", nil }
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	cases := []struct {
		name, flag, goos string
		env              map[string]string
		want             string
	}{
		{"flag wins", "/x/flag", "linux", map[string]string{"TBD_HOME": "/x/env"}, abs("/x/flag")},
		{"env", "", "linux", map[string]string{"TBD_HOME": "/x/env"}, abs("/x/env")},
		{"linux default", "", "linux", nil, filepath.Join("/home/u", ".local", "share", "tbd")},
		{"xdg", "", "linux", map[string]string{"XDG_DATA_HOME": "/xdg"}, filepath.Join("/xdg", "tbd")},
		{"windows", "", "windows", map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`}, filepath.Join(`C:\Users\u\AppData\Local`, "tbd")},
	}
	for _, tc := range cases {
		got, err := resolveHome(tc.flag, env(tc.env), tc.goos, home)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	if _, err := resolveHome("", env(nil), "windows", home); err == nil {
		t.Error("windows without LOCALAPPDATA must fail")
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	home := t.TempDir()
	c, exists, err := Load(home)
	if err != nil || exists {
		t.Fatalf("missing file: %v %v", exists, err)
	}
	if c.TeamsURL != DefaultTeamsURL || c.HeartbeatHours != 6 || len(c.LoginHosts) != 3 || !c.Hidden() {
		t.Fatalf("defaults not applied: %+v", c)
	}
	s, _ := proto.NewSecret(nil)
	c.Secret = s.Code()
	c.FCMServiceAccount = "sa.json"
	if err := Save(home, c); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(Path(home))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode())
		}
	}
	c2, exists, err := Load(home)
	if err != nil || !exists {
		t.Fatal(err)
	}
	got, err := c2.PairingSecret()
	if err != nil || got != s {
		t.Fatalf("secret round trip: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "sa.json"), []byte(`{"type":"service_account"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := c2.ServiceAccountJSON(home); err != nil || !strings.Contains(string(b), "service_account") {
		t.Fatalf("sa path: %v", err)
	}
	c2.FCMServiceAccount = ` {"inline":true}`
	if b, err := c2.ServiceAccountJSON(home); err != nil || string(b) != `{"inline":true}` {
		t.Fatalf("sa inline: %q %v", b, err)
	}
}

func TestLoadRejects(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key": "teams_url = \"https://teams.microsoft.com/v2/\"\nbogus = 1\n",
		"bad secret":  "secret = \"nope\"\n",
		"bad url":     "teams_url = \"ftp://x\"\n",
		"bad level":   "log_level = \"loud\"\n",
		"bad toml":    "secret = \n",
	} {
		home := t.TempDir()
		if err := os.WriteFile(Path(home), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(home); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Default().PairingSecret(); !errors.Is(err, ErrNotPaired) {
		t.Error("empty secret must be ErrNotPaired")
	}
}

func TestPermissionOrigins(t *testing.T) {
	c := Default()
	if got := c.PermissionOrigins(); len(got) != 3 {
		t.Fatalf("default teams_url origin is already listed: %v", got)
	}
	c.TeamsURL = "http://127.0.0.1:8080/v2/"
	got := c.PermissionOrigins()
	if got[len(got)-1] != "http://127.0.0.1:8080" {
		t.Fatalf("%v", got)
	}
}

func TestDetectBrowserOrder(t *testing.T) {
	files := map[string]bool{}
	e := env{
		getenv: func(k string) string {
			return map[string]string{"TBD_BROWSER": "/env/edge", "ProgramFiles(x86)": `C:\PF86`, "LOCALAPPDATA": `C:\LAD`}[k]
		},
		lookPath: func(n string) (string, error) {
			p := "/usr/bin/" + n
			if files[p] {
				return p, nil
			}
			return "", errors.New("no")
		},
		exists: func(p string) bool { return files[p] },
		goos:   "linux",
		starts: []string{"/repo/internal/x"},
	}
	c := Default()
	check := func(want, src string) {
		t.Helper()
		b, err := c.detectBrowser(e)
		if want == "" {
			if !errors.Is(err, ErrNoBrowser) {
				t.Fatalf("want ErrNoBrowser, got %v %v", b, err)
			}
			return
		}
		if err != nil || b.Path != want || b.Source != src {
			t.Fatalf("got %+v %v, want %s (%s)", b, err, want, src)
		}
	}
	files["/repo/.tools/edge/tbd-edge"] = true
	e.getenv = func(string) string { return "" }
	check(filepath.Join("/repo", DevBrowserRel), "repo .tools (dev)")
	files["/usr/bin/chromium"] = true
	check("/usr/bin/chromium", "PATH")
	files["/usr/bin/microsoft-edge"] = true
	check("/usr/bin/microsoft-edge", "PATH")
	files["/env/edge"] = true
	e.getenv = func(k string) string { return map[string]string{"TBD_BROWSER": "/env/edge"}[k] }
	check("/env/edge", "TBD_BROWSER")
	files["/cfg/edge"] = true
	c.Browser = "/cfg/edge"
	check("/cfg/edge", "config")
	c.Browser = "/missing"
	if _, err := c.detectBrowser(e); err == nil {
		t.Fatal("missing configured browser must fail")
	}

	// Windows: standard paths, no PATH lookup.
	c.Browser = ""
	win := filepath.Join(`C:\PF86`, "Microsoft", "Edge", "Application", "msedge.exe")
	files = map[string]bool{win: true}
	e.goos = "windows"
	e.getenv = func(k string) string { return map[string]string{"ProgramFiles(x86)": `C:\PF86`}[k] }
	e.starts = nil
	check(win, "windows-standard")
	files = map[string]bool{}
	check("", "")
}

func TestVersions(t *testing.T) {
	v, ok := ParseVersion("Microsoft Edge 157.0.4341.42 unknown\n")
	if !ok || v.Major != 157 || v.Full != "157.0.4341.42" {
		t.Fatalf("%+v", v)
	}
	dir := t.TempDir()
	for _, d := range []string{"156.0.4314.58", "157.0.4341.42", "157.0.4341.9", "Locales", "SetupMetrics"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := versionFromDir(dir)
	if err != nil || got.Full != "157.0.4341.42" {
		t.Fatalf("%+v %v", got, err)
	}
}
