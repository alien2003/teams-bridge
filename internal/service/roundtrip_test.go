package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testName is tbd-test-<random>: round trips never touch the real "tbd".
func testName(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	n := "tbd-test-" + hex.EncodeToString(b)
	if n == DefaultName || !strings.HasPrefix(n, "tbd-test-") {
		t.Fatalf("refusing name %q", n)
	}
	return n
}

// snapshot records every path under root with size, mode, mtime and link target ("" if absent).
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == root {
			return filepath.SkipAll
		}
		if err != nil {
			return err
		}
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		target, _ := os.Readlink(p)
		fmt.Fprintf(&b, "%s %v %d %d %s\n", p, st.Mode(), st.Size(), st.ModTime().UnixNano(), target)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestSystemdRoundTrip installs, checks and uninstalls a unit tbd-test-<random> with the real
// systemd user manager, in runtime mode only: the unit file lives under $XDG_CONFIG_HOME (inside
// the repository under mise) or a temporary directory, the links in $XDG_RUNTIME_DIR (tmpfs). The
// unit is never started, and ~/.config/systemd must be unchanged afterwards.
func TestSystemdRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only (systemd user manager)")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("systemctl not found; skipping the systemd round trip")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := ExecRunner{}
	mgr, err := SystemdManagerEnv(ctx, r)
	if err != nil {
		t.Skipf("systemctl --user does not work here (%v); skipping the systemd round trip", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	realCfg := filepath.Join(home, ".config", "systemd")
	before := snapshot(t, realCfg)

	runtimeDir := mgr["XDG_RUNTIME_DIR"]
	if runtimeDir == "" {
		runtimeDir = os.Getenv("XDG_RUNTIME_DIR")
	}
	if !strings.HasPrefix(runtimeDir, "/") {
		t.Skip("no XDG_RUNTIME_DIR; skipping the systemd round trip")
	}
	runtimeUnits := filepath.Join(runtimeDir, "systemd", "user")
	_, statErr := os.Stat(runtimeUnits)
	runtimeUnitsExisted := statErr == nil

	// BRIEF §7: under mise XDG_CONFIG_HOME is inside the repository; otherwise use a temp dir. It must
	// not be a directory the manager searches, or the runtime link would be refused.
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if !strings.HasPrefix(configHome, "/") || configHome == filepath.Join(home, ".config") || configHome == mgr["XDG_CONFIG_HOME"] {
		configHome = filepath.Join(t.TempDir(), "config")
	}
	unitPath, err := r.Run(ctx, "systemctl", "--user", "show", "-p", "UnitPath", "--value")
	if err != nil {
		t.Fatalf("systemctl --user show -p UnitPath: %v: %s", err, unitPath)
	}
	for _, d := range strings.Fields(unitPath) {
		if d == filepath.Join(configHome, "systemd", "user") {
			t.Skipf("%s is in the manager's unit path; refusing to use it", d)
		}
	}
	createdDirs := []string{filepath.Join(configHome, "systemd", "user"), filepath.Join(configHome, "systemd")}
	var preexisting []string
	for _, d := range createdDirs {
		if _, err := os.Stat(d); err == nil {
			preexisting = append(preexisting, d)
		}
	}

	name := testName(t)
	unitName := name + ".service"
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin dir")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A harmless executable (systemd-analyze verify checks that ExecStart exists); never started anyway.
	exe := filepath.Join(binDir, "tbd")
	if err := os.Symlink("/bin/true", exe); err != nil {
		t.Fatal(err)
	}
	dataHome := filepath.Join(tmp, "data home")
	spec := Spec{Name: name, Exe: exe, Home: dataHome, Description: "tbd round-trip test unit (never started)", Runtime: true}
	env := Env{Getenv: func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return configHome
		}
		return ""
	}, HomeDir: home}

	plan, err := InstallPlan(spec, "linux", env)
	if err != nil {
		t.Fatal(err)
	}
	unitFile := plan.Files[0].Path
	if !strings.HasPrefix(unitFile, configHome+"/") {
		t.Fatalf("unit file %s outside %s", unitFile, configHome)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = r.Run(cctx, "systemctl", "--user", "--runtime", "disable", unitName)
		_ = os.Remove(unitFile)
		_, _ = r.Run(cctx, "systemctl", "--user", "daemon-reload")
		for _, l := range []string{filepath.Join(runtimeUnits, unitName), filepath.Join(runtimeUnits, "default.target.wants", unitName)} {
			if _, err := os.Lstat(l); err == nil {
				t.Errorf("cleanup: %s was left behind; removing it", l)
				_ = os.Remove(l)
			}
		}
		if !runtimeUnitsExisted {
			_ = os.Remove(filepath.Join(runtimeUnits, "default.target.wants"))
			_ = os.Remove(runtimeUnits) // only succeeds when empty
		}
		for _, d := range createdDirs {
			keep := false
			for _, p := range preexisting {
				keep = keep || p == d
			}
			if !keep {
				_ = os.Remove(d) // only when empty
			}
		}
		if after := snapshot(t, realCfg); after != before {
			t.Errorf("%s changed:\n--- before\n%s--- after\n%s", realCfg, before, after)
		}
	})

	var log bytes.Buffer
	if err := Apply(ctx, plan, r, &log, false); err != nil {
		t.Fatalf("install: %v\n%s", err, log.String())
	}
	t.Logf("install:\n%s", log.String())

	if state, err := UnitState(ctx, r, name); err != nil || state != "enabled-runtime" {
		t.Errorf("is-enabled after install = %q, %v; want enabled-runtime", state, err)
	}
	show, err := r.Run(ctx, "systemctl", "--user", "show", "-p", "LoadState", "-p", "ActiveState", "-p", "FragmentPath", "-p", "ExecStart", unitName)
	if err != nil {
		t.Fatalf("systemctl show: %v: %s", err, show)
	}
	for _, want := range []string{
		"LoadState=loaded\n",
		"ActiveState=inactive\n",
		"FragmentPath=" + filepath.Join(runtimeUnits, unitName) + "\n",
		// systemd parsed the quoted ExecStart back into the exact argv.
		"path=" + exe + " ;",
		"argv[]=" + exe + " run --home " + dataHome + " ;",
	} {
		if !strings.Contains(show, want) {
			t.Errorf("systemctl show lacks %q:\n%s", want, show)
		}
	}
	if _, err := exec.LookPath("systemd-analyze"); err == nil {
		if out, err := r.Run(ctx, "systemd-analyze", "--user", "verify", unitFile); err != nil {
			t.Errorf("systemd-analyze --user verify: %v\n%s", err, out)
		} else if strings.TrimSpace(out) != "" {
			t.Logf("systemd-analyze --user verify:\n%s", out)
		}
	} else {
		t.Log("systemd-analyze not found; skipping offline verification")
	}

	un, err := UninstallPlan(spec, "linux", env)
	if err != nil {
		t.Fatal(err)
	}
	log.Reset()
	if err := Apply(ctx, un, r, &log, false); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, log.String())
	}
	t.Logf("uninstall:\n%s", log.String())
	if state, err := UnitState(ctx, r, name); err != nil || state != "not-found" {
		t.Errorf("is-enabled after uninstall = %q, %v; want not-found", state, err)
	}
	if _, err := os.Stat(unitFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unit file still there: %v", err)
	}
	for _, l := range []string{filepath.Join(runtimeUnits, unitName), filepath.Join(runtimeUnits, "default.target.wants", unitName)} {
		if _, err := os.Lstat(l); err == nil {
			t.Errorf("runtime link %s left by uninstall", l)
		}
	}
}

// taskExists reports whether schtasks knows the task, by exit status only: unlike QueryTask it does
// not depend on parsing the XML, so a parse problem can never be mistaken for "gone".
func taskExists(ctx context.Context, r Runner, name string) bool {
	_, err := r.Run(ctx, "schtasks", "/query", "/tn", name)
	return err == nil
}

// TestSchtasksRoundTrip creates a scheduled task tbd-test-<random> with the real install code,
// checks its definition with schtasks /query, deletes it with the real uninstall code and checks it
// is gone (BRIEF §7). By default the task is never run: its action points at whoami.exe, not tbd.
// With TBD_TEST_TASK_RUN=1 (opt-in) the action is a copy of this test binary under a path with a
// blank, the test starts the task once and checks the argv that arrived through Task Scheduler and
// conhost --headless (see TestMain); the binary only records its argv and exits.
func TestSchtasksRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only: creates and deletes a scheduled task tbd-test-<random>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r := ExecRunner{}
	probe := os.Getenv("TBD_TEST_TASK_RUN") == "1"
	tmp := t.TempDir()
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "whoami.exe")
	home := filepath.Join(tmp, "home dir")
	if probe {
		exe = copyTestBinary(t, filepath.Join(tmp, "bin dir"))
		home = filepath.Join(tmp, taskProbeMarker+" home")
	}
	if st, err := os.Stat(exe); err != nil || st.IsDir() {
		t.Skipf("no harmless executable at %s", exe)
	}
	name := testName(t)
	spec := Spec{Name: name, Exe: exe, Home: home, Description: "tbd round-trip test task; deleted by the test", NoStart: true}
	plan, err := InstallPlan(spec, "windows", DefaultEnv())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Commands {
		if len(c) > 1 && strings.EqualFold(c[1], "/run") {
			t.Fatalf("plan would run the task: %v", c)
		}
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		// By exit status, not QueryTask: an XML parse failure must never leave the task behind.
		if !taskExists(cctx, r, name) {
			return
		}
		t.Errorf("cleanup: task %s still exists; deleting it", name)
		if out, err := r.Run(cctx, "schtasks", "/delete", "/tn", name, "/f"); err != nil {
			t.Errorf("cleanup: schtasks /delete: %v: %s", err, decodeText([]byte(out)))
		}
		if taskExists(cctx, r, name) {
			t.Errorf("cleanup: task %s could not be deleted; remove it with: schtasks /delete /tn %s /f", name, name)
		}
	})

	var log bytes.Buffer
	if err := Apply(ctx, plan, r, &log, false); err != nil {
		t.Fatalf("install: %v\n%s", err, log.String())
	}
	t.Logf("install:\n%s", log.String())

	info, err := QueryTask(ctx, r, name)
	if err != nil {
		t.Fatalf("query after install: %v", err)
	}
	t.Logf("queried: %+v", info)
	if info.Command != TaskCommand {
		t.Errorf("Command = %q, want %q", info.Command, TaskCommand)
	}
	if want := TaskArguments(spec); info.Arguments != want {
		t.Errorf("Arguments = %q, want %q", info.Arguments, want)
	}
	if !strings.HasPrefix(info.Arguments, `--headless "`+exe+`" run --home "`) {
		t.Errorf("Arguments %q do not start with the conhost --headless form", info.Arguments)
	}
	if !info.HasLogonTrigger || info.ExecutionTimeLimit != "PT0S" || info.MultipleInstancesPolicy != "IgnoreNew" || info.ExecCount != 1 {
		t.Errorf("definition: %+v", info)
	}
	// What keeps the daemon alive on a laptop and unelevated (DECISIONS D5), as Windows stored it.
	if info.DisallowStartIfOnBatteries != "false" || info.StopIfGoingOnBatteries != "false" || info.RestartCount != TaskRestartCount ||
		info.LogonType != "InteractiveToken" || info.RunLevel == "HighestAvailable" {
		t.Errorf("settings/principal: %+v", info)
	}
	if probe {
		runTaskProbe(ctx, t, r, spec)
	}

	un, err := UninstallPlan(spec, "windows", DefaultEnv())
	if err != nil {
		t.Fatal(err)
	}
	log.Reset()
	if err := Apply(ctx, un, r, &log, false); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, log.String())
	}
	t.Logf("uninstall:\n%s", log.String())
	if taskExists(ctx, r, name) {
		t.Errorf("task %s still exists after uninstall", name)
	}
	if info, err := QueryTask(ctx, r, name); err == nil {
		t.Errorf("task still queryable after uninstall: %+v", info)
	}
	if _, err := os.Stat(TaskFile(spec)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("task XML not removed: %v", err)
	}
}

// copyTestBinary copies the running test binary into dir (created) and returns the copy's path.
func copyTestBinary(t *testing.T, dir string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, filepath.Base(self))
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

// runTaskProbe starts the task once (its action is a copy of this test binary, see TestMain), waits
// for the argv it received through Task Scheduler and conhost --headless, compares it with
// spec.Args(), then waits until the copy is no longer running so the temporary directory can go.
func runTaskProbe(ctx context.Context, t *testing.T, r Runner, spec Spec) {
	t.Helper()
	if out, err := r.Run(ctx, "schtasks", "/run", "/tn", spec.Name); err != nil {
		t.Fatalf("schtasks /run: %v: %s", err, decodeText([]byte(out)))
	}
	argvFile := filepath.Join(spec.Home, probeArgvFile)
	var got []string
	for deadline := time.Now().Add(time.Minute); ; {
		b, err := os.ReadFile(argvFile)
		if err == nil {
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("%s: %v", argvFile, err)
			}
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatalf("the task did not start the probe: no %s after a minute", argvFile)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if want := spec.Args(); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv through Task Scheduler + conhost = %q, want %q", got, want)
	}
	// A running image cannot be opened for writing; once it can, the probe has exited.
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if f, err := os.OpenFile(spec.Exe, os.O_WRONLY, 0); err == nil {
			f.Close()
			return
		}
	}
	t.Logf("probe %s still running after 30 s", spec.Exe)
}
