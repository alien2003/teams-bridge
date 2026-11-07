//go:build !windows

package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLaunchdProbeSelf runs this binary the way launchd runs the LaunchAgent probe (see TestMain),
// so TestLaunchdRoundTrip, which runs only on macOS, cannot fail on a bug of the probe itself.
func TestLaunchdProbeSelf(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), launchdProbeMarker+" home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--home", home, "--log-file", filepath.Join(home, "tbd.log")}
	read := func() string { b, _ := os.ReadFile(filepath.Join(home, probeStartsFile)); return string(b) }
	for mode, want := range map[string]int{"exit0": 0, "exit1": 1} {
		if err := os.WriteFile(filepath.Join(home, "mode"), []byte(mode), 0o600); err != nil {
			t.Fatal(err)
		}
		err := exec.Command(self, args...).Run()
		var ee *exec.ExitError
		if code := 0; errors.As(err, &ee) {
			code = ee.ExitCode()
			if code != want {
				t.Errorf("%s: exit %d", mode, code)
			}
		} else if err != nil || want != 0 {
			t.Errorf("%s: %v", mode, err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "mode"), []byte("serve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(read(), "start "+pid+" "); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("serve did not record its start:\n%s", read())
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("serve after SIGTERM: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(read()), "\n")
	if len(lines) != 4 || !strings.HasSuffix(lines[0], ` ["run","--home","`+home+`","--log-file","`+filepath.Join(home, "tbd.log")+`"]`) || lines[3] != "term "+pid {
		t.Errorf("%s:\n%s", probeStartsFile, read())
	}
}
