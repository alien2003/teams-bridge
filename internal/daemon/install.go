package daemon

import (
	"context"
	"io"
	"path/filepath"
	"runtime"

	"github.com/alien2003/teams-bridge/internal/service"
)

// InstallOptions configure Install.
type InstallOptions struct {
	Home      string // resolved data dir; always passed to the service (its environment lacks TBD_HOME)
	Exe       string // absolute path of this tbd binary
	Name      string // default service.DefaultName
	Uninstall bool
	DryRun    bool
	Runner    service.Runner // nil → service.ExecRunner
}

// LogFile is where the daemon of the Windows scheduled task and of the macOS LaunchAgent logs:
// under conhost --headless stderr goes nowhere, and launchd neither keeps a journal nor rotates its
// stdio file (D26). (systemd keeps stderr in the journal.)
func LogFile(home string) string { return filepath.Join(home, "tbd.log") }

// Install writes and enables (or removes) the autostart definition, or prints it with DryRun.
func Install(ctx context.Context, out io.Writer, o InstallOptions) error {
	r := o.Runner
	if r == nil {
		r = service.ExecRunner{}
	}
	spec := service.Spec{Name: o.Name, Exe: o.Exe, Home: o.Home}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		spec.ExtraArgs = []string{"--log-file", LogFile(o.Home)}
	}
	env := service.HostEnv(ctx, r)
	var plan service.Plan
	var err error
	if o.Uninstall {
		plan, err = service.UninstallPlan(spec, runtime.GOOS, env)
	} else {
		plan, err = service.InstallPlan(spec, runtime.GOOS, env)
	}
	if err != nil {
		return err
	}
	return service.Apply(ctx, plan, r, out, o.DryRun)
}
