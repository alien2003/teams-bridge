// Package service generates and installs the autostart definition of the tbd daemon: a systemd
// user unit on Linux, a Task Scheduler task on Windows (BRIEF §5 "install", DECISIONS D5), a
// launchd LaunchAgent on macOS (D26).
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

// DefaultName is the unit / task / LaunchAgent name used by `tbd install`. Tests must never use it.
const DefaultName = "tbd"

// TestNameEnv names the environment variable through which tests make install, uninstall, start,
// stop and status act on a service tbd-test-<12 hex digits> instead of DefaultName (NameFromEnv).
const TestNameEnv = "TBD_TEST_SERVICE_NAME"

var testNameRE = regexp.MustCompile(`^tbd-test-[0-9a-f]{12}$`)

// NameFromEnv is the service name the commands act on: DefaultName, or the value of TestNameEnv
// when it is set, which must then be a test name (tbd-test-<12 hex digits>): no value of it can
// reach the real "tbd" or any other job of the user.
func NameFromEnv(getenv func(string) string) (string, error) {
	v := ""
	if getenv != nil {
		v = getenv(TestNameEnv)
	}
	if v == "" {
		return DefaultName, nil
	}
	if !testNameRE.MatchString(v) {
		return "", fmt.Errorf("%s=%q: only tbd-test-<12 hex digits> is accepted (it is for tests)", TestNameEnv, v)
	}
	return v, nil
}

// DefaultDescription describes the unit / task.
const DefaultDescription = "Teams Bridge daemon (tbd): Teams notifications to your phone"

// ProjectURL is written as the unit's Documentation=.
const ProjectURL = "https://github.com/alien2003/teams-bridge"

// Spec describes what to autostart.
type Spec struct {
	// Name of the systemd unit (without ".service"), scheduled task or LaunchAgent label. Default
	// "tbd".
	Name string
	// Exe is the absolute path of the tbd binary.
	Exe string
	// Home is the data directory. When set, the service runs `tbd run --home <Home>`, so it uses
	// the same data directory as the installer whatever its own environment is. Required on
	// Windows (the task XML is kept there) and macOS (launchd's stdio file is kept there).
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

// Plan is everything an install or uninstall does, in this order: create Dirs, write Files
// (parents created), unload the launchd jobs in Unload, run Commands, remove RemoveFiles (missing
// ones are fine), run PostCommands; then print Notes.
type Plan struct {
	// OS is the target OS; it selects how commands are quoted when printed.
	OS string
	// Dirs are created (mode 0700) when missing.
	Dirs  []string
	Files []File
	// Unload are launchd service targets (gui/<uid>/<label>) to boot out when loaded, waiting until
	// launchd has removed each (macOS; see unloadJob).
	Unload       []string
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
	// UID is the user's numeric id (macOS: the launchd domain gui/<UID>). 0 (root) is refused.
	UID int
	// LaunchAgentsDir (macOS) overrides <HomeDir>/Library/LaunchAgents, for tests.
	LaunchAgentsDir string
	// Quarantined (macOS) reports whether a file carries com.apple.quarantine; nil means no.
	Quarantined func(path string) bool
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
	return Env{Getenv: os.Getenv, HomeDir: h, ReadFile: os.ReadFile, UID: os.Getuid(), Quarantined: quarantined}
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

// InstallPlan computes what installing spec on goos ("linux", "windows" or "darwin") does.
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
	case "darwin":
		return darwinInstall(s, env)
	}
	return Plan{}, fmt.Errorf("service: install is supported on linux, windows and darwin, not %s", goos)
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
	case "darwin":
		return darwinUninstall(s, env)
	}
	return Plan{}, fmt.Errorf("service: uninstall is supported on linux, windows and darwin, not %s", goos)
}

// ErrNotInstalled means the autostart definition does not exist (no `tbd install` yet).
var ErrNotInstalled = errors.New("service: not installed")

// StartCommand is the command that starts the installed daemon now: `schtasks /run /tn <name>` on
// Windows, `systemctl --user start <name>.service` on Linux (WSL included). On macOS the command
// depends on launchd's state: see StartCommands.
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
	case "darwin":
		return nil, errors.New("service: on darwin the start command depends on launchd's state (StartCommands)")
	}
	return nil, fmt.Errorf("service: start is supported on linux, windows and darwin, not %s", goos)
}

// StartCommands are the commands that start the installed daemon now. On Linux and Windows that is
// StartCommand. On macOS a loaded LaunchAgent is kickstarted (`launchctl kickstart
// gui/<uid>/<name>`); one that is not loaded (after `tbd stop`, which boots it out) is enabled and
// bootstrapped again from its plist; a missing plist is ErrNotInstalled.
func StartCommands(ctx context.Context, r Runner, name, goos string, env Env) ([][]string, error) {
	if goos != "darwin" {
		c, err := StartCommand(name, goos)
		if err != nil {
			return nil, err
		}
		return [][]string{c}, nil
	}
	s, err := Spec{Name: name}.normalize(goos, false)
	if err != nil {
		return nil, err
	}
	if err := CheckInstalled(ctx, r, s.Name, goos, env); err != nil {
		return nil, err
	}
	job, err := QueryLaunchd(ctx, r, s.Name, env)
	if err != nil {
		return nil, err
	}
	target, _ := LaunchdTarget(s.Name, env)
	if job.Loaded {
		return [][]string{launchctl("kickstart", target)}, nil
	}
	plist, err := PlistFile(s, env)
	if err != nil {
		return nil, err
	}
	domain, _ := launchdDomain(env)
	return [][]string{launchctl("enable", target), launchctl("bootstrap", domain, plist)}, nil
}

// CheckInstalled returns nil when the autostart definition named name exists on goos, an error
// wrapping ErrNotInstalled when it does not, and another error when that cannot be told. On macOS
// that is the LaunchAgent's plist (whether launchd has it loaded is QueryLaunchd's business).
func CheckInstalled(ctx context.Context, r Runner, name, goos string, env Env) error {
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
	case "darwin":
		plist, err := PlistFile(s, env)
		if err != nil {
			return err
		}
		if _, err := os.Stat(plist); errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: no %s", ErrNotInstalled, plist)
		} else if err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("service: supported on linux, windows and darwin, not %s", goos)
}
