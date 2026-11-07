package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the testdata/*.golden files")

// macEnv is a synthetic Mac: user u, uid 501.
func macEnv() Env { return Env{HomeDir: "/Users/u", UID: 501} }

// golden compares got with testdata/<name>, or rewrites it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	file := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (run the test with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs\n--- got\n%s--- want\n%s", name, got, want)
	}
}

// macSpec has blanks in both paths, like the default data directory.
var macSpec = Spec{Exe: "/Users/u/bin dir/tbd", Home: "/Users/u/Library/Application Support/tbd",
	ExtraArgs: []string{"--log-file", "/Users/u/Library/Application Support/tbd/tbd.log"}}

func TestLaunchdDryRunGolden(t *testing.T) {
	p, err := InstallPlan(macSpec, "darwin", macEnv())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "launchd-install.golden", dryRun(t, p))
	ns := macSpec
	ns.NoStart = true
	p, err = InstallPlan(ns, "darwin", macEnv())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "launchd-nostart.golden", dryRun(t, p))
	u, err := UninstallPlan(Spec{}, "darwin", macEnv())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "launchd-uninstall.golden", dryRun(t, u))
	pl, err := LaunchdPlist(macSpec)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "launchd-tbd.plist.golden", pl)
}

func TestLaunchdPlans(t *testing.T) {
	p, err := InstallPlan(macSpec, "darwin", macEnv())
	if err != nil {
		t.Fatal(err)
	}
	if p.OS != "darwin" || len(p.Files) != 1 || p.Files[0].Path != "/Users/u/Library/LaunchAgents/tbd.plist" || p.Files[0].Mode != 0o644 {
		t.Fatalf("files: %+v", p.Files)
	}
	equalLines(t, "dirs", p.Dirs, []string{macSpec.Home})
	equalLines(t, "unload", p.Unload, []string{"gui/501/tbd"})
	// Not disabled: no enable, which launchd would keep as a lasting override.
	equalLines(t, "commands", cmdLines(p.Commands), []string{
		"launchctl bootstrap gui/501 /Users/u/Library/LaunchAgents/tbd.plist",
	})
	if p.BestEffort {
		t.Error("install is best effort")
	}
	ns := macSpec
	ns.NoStart = true
	p, _ = InstallPlan(ns, "darwin", macEnv())
	if len(p.Unload) != 0 {
		t.Errorf("no-start unloads %q", p.Unload)
	}
	if len(p.Commands) != 0 {
		t.Errorf("no-start runs %q", cmdLines(p.Commands))
	}
	// Disabled (launchctl disable): enabled again, before the bootstrap, with a note.
	env := macEnv()
	var asked []string
	env.LaunchdDisabled = func(n string) bool { asked = append(asked, n); return true }
	p, _ = InstallPlan(macSpec, "darwin", env)
	equalLines(t, "disabled", cmdLines(p.Commands), []string{
		"launchctl enable gui/501/tbd",
		"launchctl bootstrap gui/501 /Users/u/Library/LaunchAgents/tbd.plist",
	})
	equalLines(t, "asked", asked, []string{"tbd"})
	if !strings.Contains(strings.Join(p.Notes, "\n"), "gui/501/tbd was disabled in launchd") {
		t.Errorf("no note on the enable: %q", p.Notes)
	}
	p, _ = InstallPlan(ns, "darwin", env)
	equalLines(t, "disabled, no-start", cmdLines(p.Commands), []string{"launchctl enable gui/501/tbd"})

	u, err := UninstallPlan(Spec{Name: "tbd-test-x"}, "darwin", Env{HomeDir: "/Users/u", UID: 501, LaunchAgentsDir: "/tmp/la"})
	if err != nil {
		t.Fatal(err)
	}
	equalLines(t, "uninstall unload", u.Unload, []string{"gui/501/tbd-test-x"})
	equalLines(t, "uninstall removes", u.RemoveFiles, []string{"/tmp/la/tbd-test-x.plist"})
	if !u.BestEffort || len(u.Commands) != 0 || len(u.Files) != 0 {
		t.Errorf("uninstall: %+v", u)
	}

	// Refused: root, an unknown uid, no Home, no home directory for ~/Library, relative paths.
	for name, c := range map[string]struct {
		spec Spec
		env  Env
		want string
	}{
		"root":        {macSpec, Env{HomeDir: "/var/root"}, "not with sudo"},
		"unknown uid": {macSpec, Env{HomeDir: "/Users/u", UID: -1}, "unknown user id"},
		"no home":     {Spec{Exe: macSpec.Exe}, macEnv(), "data directory"},
		"no homedir":  {macSpec, Env{UID: 501}, "home directory"},
		"relative":    {Spec{Exe: "tbd", Home: macSpec.Home}, macEnv(), "not an absolute"},
		"relative LA": {macSpec, Env{HomeDir: "/Users/u", UID: 501, LaunchAgentsDir: "LaunchAgents"}, "not absolute"},
		"bad name":    {Spec{Name: "a/b", Exe: macSpec.Exe, Home: macSpec.Home}, macEnv(), "invalid name"},
		"not utf-8":   {Spec{Exe: "/Users/u/t\xffbd", Home: macSpec.Home}, macEnv(), "UTF-8"},
	} {
		if _, err := InstallPlan(c.spec, "darwin", c.env); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if _, err := UninstallPlan(Spec{}, "darwin", Env{HomeDir: "/var/root"}); err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Errorf("uninstall as root: %v", err)
	}
	if _, err := InstallPlan(Spec{Exe: "/x", Home: "/h", Runtime: true}, "darwin", macEnv()); err == nil {
		t.Error("Runtime accepted on darwin")
	}
}

func TestLaunchdPlistEscaping(t *testing.T) {
	spec := Spec{Name: "tbd-test-esc", Exe: `/Users/u/a&b <c> "d" 'e' %f $g/tbd`, Home: "/Users/u/h & h",
		ExtraArgs: []string{"--log-file", "/Users/u/h & h/tbd.log"}}
	pl, err := LaunchdPlist(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\t\t<string>/Users/u/a&amp;b &lt;c&gt; \"d\" 'e' %f $g/tbd</string>\n",
		"\t\t<string>/Users/u/h &amp; h</string>\n",
		"\t<string>/Users/u/h &amp; h/tbd.stderr.log</string>\n",
		"<key>Label</key>\n\t<string>tbd-test-esc</string>\n",
	} {
		if !strings.Contains(pl, want) {
			t.Errorf("plist lacks %q:\n%s", want, pl)
		}
	}
	// Well-formed XML whose ProgramArguments parse back into the exact argv.
	if got, want := plistArray(t, pl, "ProgramArguments"), append([]string{spec.Exe}, spec.Args()...); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("ProgramArguments parse back as %q, want %q", got, want)
	}
	// The printed commands keep '%', '$' and '&' literal (POSIX single quotes).
	p, err := InstallPlan(spec, "darwin", Env{HomeDir: "/Users/u", UID: 501, LaunchAgentsDir: "/Users/u/L A"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dryRun(t, p); !strings.Contains(got, "\n  launchctl bootstrap gui/501 '/Users/u/L A/tbd-test-esc.plist'\n") {
		t.Errorf("dry run:\n%s", got)
	}
}

func TestLaunchdQuarantineNote(t *testing.T) {
	env := macEnv()
	var asked []string
	env.Quarantined = func(p string) bool { asked = append(asked, p); return true }
	p, err := InstallPlan(macSpec, "darwin", env)
	if err != nil {
		t.Fatal(err)
	}
	last := p.Notes[len(p.Notes)-1]
	if !strings.Contains(last, "com.apple.quarantine") || !strings.Contains(last, "xattr -d com.apple.quarantine '/Users/u/bin dir/tbd'") {
		t.Errorf("note: %s", last)
	}
	equalLines(t, "asked", asked, []string{macSpec.Exe})
	env.Quarantined = func(string) bool { return false }
	p, _ = InstallPlan(macSpec, "darwin", env)
	for _, n := range p.Notes {
		if strings.Contains(n, "quarantine") {
			t.Errorf("quarantine note without the attribute: %s", n)
		}
	}
}

// launchdFake plays launchd for one job: print succeeds while it is loaded; after a bootout the
// job is still found by `pending` more prints (the removal is asynchronous), or forever with never.
type launchdFake struct {
	mu       sync.Mutex
	target   string
	loaded   bool
	removing bool
	pending  int
	never    bool
	noDomain bool
	disabled string // print-disabled's value for the job: "", "disabled", "enabled", "true", "fail"
	printOut string
	calls    []string
}

func (f *launchdFake) Run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	domain := f.target[:strings.LastIndexByte(f.target, '/')]
	switch line {
	case "launchctl print " + f.target:
		if f.loaded && f.removing && !f.never {
			if f.pending > 0 {
				f.pending--
			} else {
				f.loaded = false
			}
		}
		if f.loaded {
			return f.printOut, nil
		}
		return "Could not find service in domain for user gui: 501\n", errors.New("exit status 113")
	case "launchctl print " + domain:
		if f.noDomain {
			return "Bad request.\nCould not find domain for port\n", errors.New("exit status 5")
		}
		return domain + " = {\n}\n", nil
	case "launchctl print-disabled " + domain:
		if f.disabled == "fail" {
			return "Bad request.\n", errors.New("exit status 5")
		}
		out := "disabled services = {\n\t\"com.example.other\" => disabled\n"
		if f.disabled != "" {
			out += "\t\"" + f.target[strings.LastIndexByte(f.target, '/')+1:] + "\" => " + f.disabled + "\n"
		}
		return out + "}\n", nil
	case "launchctl bootout " + f.target:
		if !f.loaded {
			return "Boot-out failed: 3: No such process\n", errors.New("exit status 3")
		}
		f.removing = true
		return "", nil
	}
	return "", errors.New("unexpected " + line)
}

func TestApplyUnload(t *testing.T) {
	defer func(p, w time.Duration) { unloadPoll, unloadWait = p, w }(unloadPoll, unloadWait)
	unloadPoll, unloadWait = time.Millisecond, 50*time.Millisecond
	ctx := context.Background()
	const target = "gui/501/tbd-test-u"

	// Not loaded: no bootout.
	f := &launchdFake{target: target}
	if err := Apply(ctx, Plan{OS: "darwin", Unload: []string{target}}, f, nil, false); err != nil {
		t.Fatal(err)
	}
	equalLines(t, "not loaded", f.calls, []string{"launchctl print " + target})

	// Loaded: bootout, then poll until launchd no longer has it.
	f = &launchdFake{target: target, loaded: true, pending: 3}
	var out bytes.Buffer
	if err := Apply(ctx, Plan{OS: "darwin", Unload: []string{target}, Commands: [][]string{{"launchctl", "print", "gui/501"}}}, f, &out, false); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if f.loaded || f.calls[1] != "launchctl bootout "+target || f.calls[len(f.calls)-1] != "launchctl print gui/501" {
		t.Errorf("calls %q", f.calls)
	}
	if !strings.Contains(out.String(), "Running: launchctl bootout "+target+"\n") {
		t.Errorf("output:\n%s", out.String())
	}

	// Never leaves: an error naming the target, and the commands after it do not run.
	f = &launchdFake{target: target, loaded: true, never: true}
	err := Apply(ctx, Plan{OS: "darwin", Unload: []string{target}, Commands: [][]string{{"launchctl", "bootstrap", "x"}}}, f, nil, false)
	if err == nil || !strings.Contains(err.Error(), "still loaded") || strings.Contains(strings.Join(f.calls, "\n"), "bootstrap") {
		t.Errorf("never unloads: %v, calls %q", err, f.calls)
	}

	// BestEffort (uninstall) still removes the plist.
	dir := t.TempDir()
	plist := filepath.Join(dir, "tbd-test-u.plist")
	if err := os.WriteFile(plist, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f = &launchdFake{target: target, loaded: true, never: true}
	err = Apply(ctx, Plan{OS: "darwin", Unload: []string{target}, RemoveFiles: []string{plist}, BestEffort: true}, f, nil, false)
	if err == nil {
		t.Error("best effort hid the unload failure")
	}
	if _, serr := os.Stat(plist); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("plist kept: %v", serr)
	}
}

// launchdPrintSample is `launchctl print gui/501/tbd` of a running job, in the shape macOS prints
// it (synthetic values; nested blocks repeat "state" and "pid"-like keys on purpose).
const launchdPrintSample = `gui/501/tbd = {
	active count = 1
	path = /Users/u/Library/LaunchAgents/tbd.plist
	type = LaunchAgent
	state = running

	program = /Users/u/bin/tbd
	arguments = {
		/Users/u/bin/tbd
		run
		--home
		/Users/u/Library/Application Support/tbd
	}

	stdout path = /Users/u/Library/Application Support/tbd/tbd.stderr.log
	stderr path = /Users/u/Library/Application Support/tbd/tbd.stderr.log
	inherited environment = {
		SSH_AUTH_SOCK => /private/tmp/com.apple.launchd.x/Listeners
	}

	default environment = {
		PATH => /usr/bin:/bin:/usr/sbin:/sbin
	}

	domain = gui/501 [100005]
	asid = 100005
	minimum runtime = 10
	exit timeout = 30
	runs = 3
	pid = 4242
	immediate reason = speculative
	forks = 12
	execs = 1
	initialized = 1
	trampolined = 1
	started suspended = 0
	proxy started suspended = 0
	last exit code = 1

	event triggers = {
		x = {
			state = 0
			pid = 7
		}
	}

	spawn type = interactive (4)
	jetsam priority = 40
	properties = keepalive | runatload | inferred program
}
`

func TestParseLaunchdPrint(t *testing.T) {
	got := parseLaunchdPrint(launchdPrintSample)
	if want := (LaunchdJob{State: "running", PID: 4242, Runs: 3, LastExit: "1"}); got != want {
		t.Errorf("running: %+v, want %+v", got, want)
	}
	stopped := strings.NewReplacer("\tstate = running\n", "\tstate = not running\n", "\tpid = 4242\n", "",
		"\tlast exit code = 1\n", "\tlast exit code = (never exited)\n").Replace(launchdPrintSample)
	if got := parseLaunchdPrint(stopped); got.PID != 0 || got.State != "not running" || got.LastExit != "(never exited)" {
		t.Errorf("stopped: %+v", got)
	}
	if got := parseLaunchdPrint("garbage\n= {\n"); got != (LaunchdJob{}) {
		t.Errorf("garbage: %+v", got)
	}
}

// launchdDisabledSample is `launchctl print-disabled gui/501` in the shape macOS prints it
// (synthetic labels).
const launchdDisabledSample = `disabled services = {
	"com.example.off" => disabled
	"tbd" => enabled
	"tbd-test-0123456789ab" => disabled
	"com.example.old" => true
}

login item associations = {
	"tbd-test-aaaaaaaaaaaa" => disabled
}
`

func TestParseLaunchdDisabled(t *testing.T) {
	for label, want := range map[string]bool{
		"tbd": false, "tbd-test-0123456789ab": true, "com.example.old": true, "com.example.off": true,
		"tbd-test-aaaaaaaaaaaa": false, "tbd-test": false, "absent": false,
	} {
		if got := parseLaunchdDisabled(launchdDisabledSample, label); got != want {
			t.Errorf("%s: %v, want %v", label, got, want)
		}
	}
	f := &launchdFake{target: "gui/501/tbd", disabled: "disabled"}
	if d, err := LaunchdDisabled(context.Background(), f, "", macEnv()); err != nil || !d {
		t.Errorf("LaunchdDisabled: %v %v", d, err)
	}
	f.disabled = "fail"
	if _, err := LaunchdDisabled(context.Background(), f, "", macEnv()); err == nil {
		t.Error("print-disabled failure not reported")
	}
	if _, err := LaunchdDisabled(context.Background(), f, "", Env{UID: 0}); err == nil {
		t.Error("root accepted")
	}
}

func TestQueryLaunchdAndStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("darwin paths on disk need a Unix file system")
	}
	ctx := context.Background()
	const target = "gui/501/tbd"
	dir := t.TempDir()
	env := Env{HomeDir: "/Users/u", UID: 501, LaunchAgentsDir: dir}
	plist := filepath.Join(dir, "tbd.plist")

	f := &launchdFake{target: target}
	if _, err := StartCommands(ctx, f, "", "darwin", env); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("no plist: %v", err)
	}
	if got := LaunchdStatus(ctx, f, "", env); got != "not installed (tbd install)" {
		t.Errorf("status without plist: %q", got)
	}
	if err := os.WriteFile(plist, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckInstalled(ctx, f, "", "darwin", env); err != nil {
		t.Errorf("CheckInstalled: %v", err)
	}

	// Installed, not loaded (after tbd stop), not disabled: bootstrap only.
	job, err := QueryLaunchd(ctx, f, "", env)
	if err != nil || job.Loaded {
		t.Fatalf("not loaded: %+v %v", job, err)
	}
	cs, err := StartCommands(ctx, f, "", "darwin", env)
	if err != nil {
		t.Fatal(err)
	}
	equalLines(t, "start, not loaded", cmdLines(cs), []string{"launchctl bootstrap gui/501 " + plist})
	// Enabled only when launchd has it disabled, or cannot say.
	for _, d := range []string{"disabled", "true", "fail"} {
		cs, err = StartCommands(ctx, &launchdFake{target: target, disabled: d}, "", "darwin", env)
		if err != nil {
			t.Fatal(err)
		}
		equalLines(t, "start, not loaded, "+d, cmdLines(cs), []string{"launchctl enable " + target, "launchctl bootstrap gui/501 " + plist})
	}
	for _, d := range []string{"enabled", "false"} {
		cs, _ = StartCommands(ctx, &launchdFake{target: target, disabled: d}, "", "darwin", env)
		equalLines(t, "start, not loaded, "+d, cmdLines(cs), []string{"launchctl bootstrap gui/501 " + plist})
	}
	if got := LaunchdStatus(ctx, f, "", env); got != target+" installed, not loaded (tbd start)" {
		t.Errorf("status: %q", got)
	}

	// Loaded and running: kickstart.
	f = &launchdFake{target: target, loaded: true, printOut: launchdPrintSample}
	job, err = QueryLaunchd(ctx, f, "", env)
	if err != nil || !job.Loaded || job.PID != 4242 {
		t.Fatalf("loaded: %+v %v", job, err)
	}
	cs, _ = StartCommands(ctx, f, "", "darwin", env)
	equalLines(t, "start, loaded", cmdLines(cs), []string{"launchctl kickstart " + target})
	if got := LaunchdStatus(ctx, f, "", env); got != target+" loaded, running (pid 4242)" {
		t.Errorf("status: %q", got)
	}
	f.printOut = strings.Replace(launchdPrintSample, "\tpid = 4242\n", "", 1)
	if got := LaunchdStatus(ctx, f, "", env); got != target+" loaded, not running (last exit code 1)" {
		t.Errorf("status: %q", got)
	}

	// No GUI domain (only an SSH session): an error of its own, not "not loaded".
	f = &launchdFake{target: target, noDomain: true}
	if _, err := QueryLaunchd(ctx, f, "", env); !errors.Is(err, ErrNoGUIDomain) {
		t.Errorf("no domain: %v", err)
	}
	if _, err := StartCommands(ctx, f, "", "darwin", env); !errors.Is(err, ErrNoGUIDomain) {
		t.Errorf("start without domain: %v", err)
	}
	if got := LaunchdStatus(ctx, f, "", env); !strings.Contains(got, "no GUI domain") {
		t.Errorf("status: %q", got)
	}

	// Linux and Windows: StartCommand, nothing is asked.
	r := &fakeRunner{}
	cs, err = StartCommands(ctx, r, "", "linux", Env{})
	if err != nil || len(r.calls) != 0 {
		t.Fatalf("%v %q", err, r.calls)
	}
	equalLines(t, "linux", cmdLines(cs), []string{"systemctl --user start tbd.service"})
	if got := strings.Join(LaunchdBootout(target), " "); got != "launchctl bootout "+target {
		t.Errorf("bootout: %q", got)
	}
}

func TestNameFromEnv(t *testing.T) {
	get := func(v string) func(string) string {
		return func(k string) string {
			if k == TestNameEnv {
				return v
			}
			return ""
		}
	}
	if n, err := NameFromEnv(get("")); n != DefaultName || err != nil {
		t.Errorf("unset: %q %v", n, err)
	}
	if n, err := NameFromEnv(nil); n != DefaultName || err != nil {
		t.Errorf("nil: %q %v", n, err)
	}
	if n, err := NameFromEnv(get("tbd-test-0123456789ab")); n != "tbd-test-0123456789ab" || err != nil {
		t.Errorf("test name: %q %v", n, err)
	}
	for _, bad := range []string{"tbd", "tbd-test-", "tbd-test-0123456789AB", "tbd-test-0123456789abc", "x tbd-test-0123456789ab", "com.apple.Finder"} {
		if n, err := NameFromEnv(get(bad)); err == nil {
			t.Errorf("%q accepted as %q", bad, n)
		}
	}
}

// plistArray returns the strings of the top-level array under key, parsing pl as strict XML.
func plistArray(t *testing.T, pl, key string) []string {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(pl))
	var out []string
	var lastKey, text string
	depth, inArray := 0, false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, pl)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			depth++
			text = ""
			if v.Name.Local == "array" && depth == 3 && lastKey == key {
				inArray = true
			}
		case xml.CharData:
			text += string(v)
		case xml.EndElement:
			switch {
			case v.Name.Local == "key" && depth == 3:
				lastKey = text
			case v.Name.Local == "string" && inArray:
				out = append(out, text)
			case v.Name.Local == "array":
				inArray = false
			}
			depth--
		}
	}
}
