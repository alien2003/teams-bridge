package service

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// taskProbeMarker is part of the --home directory name of the opt-in Windows task probe
// (TestSchtasksRoundTrip with TBD_TEST_TASK_RUN=1); probeArgvFile is what the probe writes there.
const (
	taskProbeMarker = "tbd-argv-probe"
	probeArgvFile   = "argv.json"
)

// TestMain lets this test binary stand in for tbd in the task probe: started by Task Scheduler as
// `<copy of the binary> run --home <dir whose name contains taskProbeMarker>`, it records the argv it
// received in that directory and exits instead of running tests. Any other invocation runs the tests.
func TestMain(m *testing.M) {
	if a := os.Args; len(a) >= 4 && a[1] == "run" && a[2] == "--home" && strings.Contains(filepath.Base(a[3]), taskProbeMarker) {
		os.Exit(recordArgv(a[3], a[1:]))
	}
	os.Exit(m.Run())
}

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
