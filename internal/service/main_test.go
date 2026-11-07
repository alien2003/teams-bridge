package service

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// taskProbeMarker is part of the --home directory name of the opt-in Windows task probe
// (TestSchtasksRoundTrip with TBD_TEST_TASK_RUN=1); probeArgvFile is what the probe writes there.
// launchdProbeMarker names the home of the macOS LaunchAgent probe (TestLaunchdRoundTrip).
const (
	taskProbeMarker    = "tbd-argv-probe"
	probeArgvFile      = "argv.json"
	launchdProbeMarker = "tbd-launchd-probe"
)

// TestMain lets this test binary stand in for tbd in the service probes: started by Task Scheduler
// or launchd as `<copy of the binary> run --home <dir whose name contains a probe marker> …`, it
// plays its part (see recordArgv, launchdProbe) and exits instead of running tests. Any other
// invocation runs the tests.
func TestMain(m *testing.M) {
	if a := os.Args; len(a) >= 4 && a[1] == "run" && a[2] == "--home" {
		switch base := filepath.Base(a[3]); {
		case strings.Contains(base, taskProbeMarker):
			os.Exit(recordArgv(a[3], a[1:]))
		case strings.Contains(base, launchdProbeMarker):
			os.Exit(launchdProbe(a[3], a[1:]))
		}
	}
	os.Exit(m.Run())
}

// launchdProbe is one start of the LaunchAgent probe. It reads <home>/mode, appends "start <pid>
// <argv as JSON>" to <home>/starts.log, then acts as the mode says: "exit0" and "exit1" exit with that status at
// once; "serve" (the default) runs until SIGTERM, appends "term <pid>" and exits 0, as tbd does.
func launchdProbe(home string, args []string) int {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	b, err := json.Marshal(args)
	if err != nil {
		return 2
	}
	appendLine := func(s string) error {
		f, err := os.OpenFile(filepath.Join(home, probeStartsFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(f, s)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	// The mode first: once the start line is there, the test may already write the next mode.
	mode, _ := os.ReadFile(filepath.Join(home, "mode"))
	if appendLine(fmt.Sprintf("start %d %s", os.Getpid(), b)) != nil {
		return 3
	}
	switch strings.TrimSpace(string(mode)) {
	case "exit0":
		return 0
	case "exit1":
		return 1
	}
	select {
	case <-term:
		if appendLine(fmt.Sprintf("term %d", os.Getpid())) != nil {
			return 4
		}
		return 0
	case <-time.After(10 * time.Minute):
		return 5
	}
}

// probeStartsFile is launchdProbe's record inside its home.
const probeStartsFile = "starts.log"

// TestArgvProbeSelf runs this binary the way the task probe's action does (no toolchain involved) and
// checks the recorded argv, so the opt-in Windows probe cannot fail on a bug of its own.
func TestArgvProbeSelf(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), taskProbeMarker+" home")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--home", dir, "x y"}
	if out, err := exec.Command(self, want...).CombinedOutput(); err != nil {
		t.Fatalf("probe: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, probeArgvFile))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(b, &got); err != nil || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("recorded %q (%v), want %q", got, err, want)
	}
}

// recordArgv writes args as JSON to dir/probeArgvFile atomically (temp file + rename), so the test
// never reads a partial file.
func recordArgv(dir string, args []string) int {
	b, err := json.Marshal(args)
	if err != nil {
		return 2
	}
	tmp := filepath.Join(dir, probeArgvFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return 3
	}
	if err := os.Rename(tmp, filepath.Join(dir, probeArgvFile)); err != nil {
		return 4
	}
	return 0
}
