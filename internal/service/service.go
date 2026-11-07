// Package service generates and installs the autostart definition of the tbd daemon: a systemd
// user unit on Linux, a Task Scheduler task on Windows (BRIEF §5 "install", DECISIONS D5).
//
// Everything is first computed as a Plan (files to write, commands to run, files to remove, notes)
// by pure functions of a Spec and an Env, so `tbd install --dry-run` prints exactly what a real run
// would do and the tests stay hermetic. Apply prints or executes a Plan.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
)

// DefaultName is the unit / task name used by `tbd install`. Tests must never use it.
const DefaultName = "tbd"

// DefaultDescription describes the unit / task.
const DefaultDescription = "Teams Bridge daemon (tbd): Teams notifications to your phone"

// ProjectURL is written as the unit's Documentation=.
const ProjectURL = "https://github.com/alien2003/teams-bridge"

// Spec describes what to autostart.
type Spec struct {
	// Name of the systemd unit (without ".service") or scheduled task. Default "tbd".
	Name string
	// Exe is the absolute path of the tbd binary.
	Exe string
	// Home is the data directory. When set, the service runs `tbd run --home <Home>`, so it uses
	// the same data directory as the installer whatever its own environment is. Required on
	// Windows (the task XML is kept there).
	Home string
	// Description of the unit / task. Default DefaultDescription.
	Description string
	// ExtraArgs are appended after `run [--home <Home>]`.
	ExtraArgs []string
	// NoStart registers the autostart without starting the daemon now.
	NoStart bool
	// Runtime (Linux only, for tests): link and enable the unit in $XDG_RUNTIME_DIR/systemd/user
	// (tmpfs, gone at reboot) instead of enabling it persistently; never starts it.
	Runtime bool
}

// Args is the daemon's command line after the executable.
func (s Spec) Args() []string {
	a := []string{"run"}
	if s.Home != "" {
		a = append(a, "--home", s.Home)
	}
	return append(a, s.ExtraArgs...)
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// normalize fills in defaults and validates the spec for goos. Uninstalling needs only the name
// (and Home on Windows), so the executable is checked only when install is set.
func (s Spec) normalize(goos string, install bool) (Spec, error) {
	if s.Name == "" {
		s.Name = DefaultName
	}
	if s.Description == "" {
		s.Description = DefaultDescription
	}
	if !nameRE.MatchString(s.Name) {
		return s, fmt.Errorf("service: invalid name %q (letters, digits, '.', '_', '-'; at most 100)", s.Name)
	}
	if strings.HasSuffix(s.Name, ".service") {
		return s, fmt.Errorf("service: name %q must not end in .service", s.Name)
	}
	if install && !isAbs(goos, s.Exe) {
		return s, fmt.Errorf("service: executable %q is not an absolute %s path", s.Exe, goos)
	}
	if s.Home != "" && !isAbs(goos, s.Home) {
		return s, fmt.Errorf("service: home %q is not an absolute %s path", s.Home, goos)
	}
	if hasControl(s.Description) {
		return s, errors.New("service: description contains control characters")
	}
	for _, a := range append([]string{s.Exe, s.Home}, s.ExtraArgs...) {
		if hasControl(a) {
			return s, fmt.Errorf("service: argument %q contains control characters", a)
		}
		// Task Scheduler expands %VARIABLE% in Command and Arguments and has no escape for '%'.
		if goos == "windows" && strings.Contains(a, "%") {
			return s, fmt.Errorf("service: argument %q contains '%%', which Task Scheduler would expand", a)
		}
	}
	if s.Runtime && goos != "linux" {
		return s, errors.New("service: Runtime is only supported on linux")
	}
	return s, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

var winAbsRE = regexp.MustCompile(`^(?:[A-Za-z]:\\|\\\\[^\\]+\\[^\\]+)`)

// isAbs reports whether p is absolute on goos, independently of the host OS.
func isAbs(goos, p string) bool {
	if goos == "windows" {
		return winAbsRE.MatchString(p)
	}
	return strings.HasPrefix(p, "/")
}

// File is a file a Plan writes.
type File struct {
	Path    string
	Content []byte
	Mode    os.FileMode
}

// Plan is everything an install or uninstall does, in this order: write Files (parents created),
// run Commands, remove RemoveFiles (missing ones are fine), run PostCommands; then print Notes.
type Plan struct {
	// OS is the target OS; it selects how commands are quoted when printed.
	OS           string
	Files        []File
	Commands     [][]string
	RemoveFiles  []string
	PostCommands [][]string
	Notes        []string
	// BestEffort (uninstall): attempt every step even after a failure and return all errors.
	BestEffort bool
}

// Env is what plan generation reads from the environment, so tests can be hermetic.
type Env struct {
	// Getenv reads the installer's environment; nil means an empty environment.
	Getenv func(string) string
	// HomeDir is the user's home directory.
	HomeDir string
	// ReadFile reads /proc/sys/kernel/osrelease for WSL detection; nil means "not readable".
	ReadFile func(string) ([]byte, error)
	// ManagerEnv is the environment of the systemd user manager (`systemctl --user
	// show-environment`). The manager, not the installer, decides where unit files are found, so
	// when it is known it takes precedence for XDG_CONFIG_HOME and HOME. nil means unknown.
	ManagerEnv map[string]string
}

func (e Env) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

// DefaultEnv is the process environment (no systemd manager environment).
func DefaultEnv() Env {
	h, _ := os.UserHomeDir()
	return Env{Getenv: os.Getenv, HomeDir: h, ReadFile: os.ReadFile}
}

// HostEnv is DefaultEnv plus, on Linux, the systemd user manager's environment when the manager
// is reachable. This is what `tbd install` should use.
func HostEnv(ctx context.Context, r Runner) Env {
	e := DefaultEnv()
	if runtime.GOOS == "linux" {
		if m, err := SystemdManagerEnv(ctx, r); err == nil {
			e.ManagerEnv = m
		}
	}
	return e
}

// InstallPlan computes what installing spec on goos ("linux" or "windows") does.
func InstallPlan(spec Spec, goos string, env Env) (Plan, error) {
	s, err := spec.normalize(goos, true)
	if err != nil {
		return Plan{}, err
	}
	switch goos {
	case "linux":
		return linuxInstall(s, env)
	case "windows":
		return windowsInstall(s, env)
	}
	return Plan{}, fmt.Errorf("service: install is supported on linux and windows, not %s", goos)
}

// UninstallPlan computes what uninstalling spec on goos does.
func UninstallPlan(spec Spec, goos string, env Env) (Plan, error) {
	s, err := spec.normalize(goos, false)
	if err != nil {
		return Plan{}, err
	}
	switch goos {
	case "linux":
		return linuxUninstall(s, env)
	case "windows":
		return windowsUninstall(s), nil
	}
	return Plan{}, fmt.Errorf("service: uninstall is supported on linux and windows, not %s", goos)
}

// ErrNotInstalled means the autostart definition does not exist (no `tbd install` yet).
var ErrNotInstalled = errors.New("service: not installed")

// StartCommand is the command that starts the installed daemon now: `schtasks /run /tn <name>` on
// Windows, `systemctl --user start <name>.service` on Linux (WSL included).
func StartCommand(name, goos string) ([]string, error) {
	s, err := Spec{Name: name}.normalize(goos, false)
	if err != nil {
		return nil, err
	}
	switch goos {
	case "linux":
		return systemctl("start", s.Name+".service"), nil
	case "windows":
		return schtasks("/run", "/tn", s.Name), nil
	}
	return nil, fmt.Errorf("service: start is supported on linux and windows, not %s", goos)
}

// CheckInstalled returns nil when the autostart definition named name exists on goos, an error
// wrapping ErrNotInstalled when it does not, and another error when that cannot be told.
func CheckInstalled(ctx context.Context, r Runner, name, goos string) error {
	s, err := Spec{Name: name}.normalize(goos, false)
	if err != nil {
		return err
	}
	if r == nil {
		r = ExecRunner{}
	}
	switch goos {
	case "linux":
		state, err := UnitState(ctx, r, s.Name)
		if err != nil {
			return err
		}
		if state == "not-found" {
			return fmt.Errorf("%w: systemctl --user knows no %s.service", ErrNotInstalled, s.Name)
		}
		return nil
	case "windows":
		// By exit status: schtasks' messages are localised.
		if out, err := r.Run(ctx, "schtasks", "/query", "/tn", s.Name); err != nil {
			return fmt.Errorf("%w: schtasks /query /tn %s: %s", ErrNotInstalled, s.Name, strings.TrimSpace(decodeText([]byte(out))))
		}
		return nil
	}
	return fmt.Errorf("service: supported on linux and windows, not %s", goos)
}
