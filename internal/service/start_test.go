package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStartCommand(t *testing.T) {
	for _, c := range []struct{ name, goos, want string }{
		{"", "linux", "systemctl --user start tbd.service"},
		{"tbd-test-x", "linux", "systemctl --user start tbd-test-x.service"},
		{"", "windows", "schtasks /run /tn tbd"},
		{"tbd-test-x", "windows", "schtasks /run /tn tbd-test-x"},
	} {
		got, err := StartCommand(c.name, c.goos)
		if err != nil || strings.Join(got, " ") != c.want {
			t.Errorf("StartCommand(%q, %s) = %q, %v; want %q", c.name, c.goos, got, err, c.want)
		}
	}
	if _, err := StartCommand("bad name", "linux"); err == nil {
		t.Error("invalid name accepted")
	}
	// On darwin the command depends on launchd's state: StartCommands (launchd_test.go).
	if _, err := StartCommand("", "darwin"); err == nil || !strings.Contains(err.Error(), "StartCommands") {
		t.Errorf("darwin: %v", err)
	}
	if _, err := StartCommand("", "freebsd"); err == nil {
		t.Error("freebsd accepted")
	}
	if got := strings.Join(StopCommand("tbd"), " "); got != "systemctl --user stop --no-block tbd.service" {
		t.Errorf("StopCommand = %q", got)
	}
}

func TestCheckInstalled(t *testing.T) {
	ctx := context.Background()
	r := &fakeRunner{out: map[string]string{
		"systemctl --user is-enabled a.service": "enabled\n",
		"systemctl --user is-enabled b.service": "not-found\n",
		"systemctl --user is-enabled c.service": "Failed to connect to bus: No medium found\n",
		"schtasks /query /tn b":                 "ERROR: The system cannot find the file specified.\r\n",
	}, fail: map[string]bool{
		"systemctl --user is-enabled b.service": true, "systemctl --user is-enabled c.service": true, "schtasks /query /tn b": true,
	}}
	if err := CheckInstalled(ctx, r, "a", "linux", Env{}); err != nil {
		t.Errorf("linux a: %v", err)
	}
	if err := CheckInstalled(ctx, r, "b", "linux", Env{}); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("linux b: %v", err)
	}
	if err := CheckInstalled(ctx, r, "c", "linux", Env{}); err == nil || errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "Failed to connect") {
		t.Errorf("linux c (no manager) must be an error other than not-installed: %v", err)
	}
	if err := CheckInstalled(ctx, r, "a", "windows", Env{}); err != nil {
		t.Errorf("windows a: %v", err)
	}
	if err := CheckInstalled(ctx, r, "b", "windows", Env{}); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "cannot find") {
		t.Errorf("windows b: %v", err)
	}
}

func TestMainPID(t *testing.T) {
	ctx := context.Background()
	r := &fakeRunner{out: map[string]string{
		"systemctl --user show -p MainPID --value tbd.service": "4242\n",
		"systemctl --user show -p MainPID --value x.service":   "0\n",
		"systemctl --user show -p MainPID --value y.service":   "garbage\n",
	}}
	if pid, err := MainPID(ctx, r, "tbd"); pid != 4242 || err != nil {
		t.Errorf("tbd: %d %v", pid, err)
	}
	if pid, err := MainPID(ctx, r, "x"); pid != 0 || err != nil {
		t.Errorf("x: %d %v", pid, err)
	}
	if _, err := MainPID(ctx, r, "y"); err == nil {
		t.Error("garbage accepted")
	}
}
