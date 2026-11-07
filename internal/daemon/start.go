package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/alien2003/teams-bridge/internal/service"
)

// StartOptions configure Start.
type StartOptions struct {
	Home   string
	Name   string         // default service.DefaultName
	Runner service.Runner // nil → service.ExecRunner
	Wait   time.Duration  // how long to wait for the started daemon to take the lock; 0 → 20 s

	goos string // test hook: the target OS (default runtime.GOOS)
}

// Start starts the installed service now (`schtasks /run /tn tbd`, `systemctl --user start
// tbd.service`) and waits until a daemon holds home's lock. When one already does, it says so and
// starts nothing: a second daemon would only fail on the lock.
func Start(ctx context.Context, out io.Writer, o StartOptions) error {
	if out == nil {
		out = io.Discard
	}
	if held, pid := Held(o.Home); held {
		fmt.Fprintf(out, "tbd is already running (pid %s).\n", pid)
		return nil
	}
	name, goos, r := o.Name, o.goos, o.Runner
	if name == "" {
		name = service.DefaultName
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if r == nil {
		r = service.ExecRunner{}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := service.CheckInstalled(cctx, r, name, goos); err != nil {
		if errors.Is(err, service.ErrNotInstalled) {
			return fmt.Errorf("tbd is not installed as a service: run `tbd install` (or `tbd run` in a terminal) (%v)", err)
		}
		return err
	}
	c, err := service.StartCommand(name, goos)
	if err != nil {
		return err
	}
	line := strings.Join(c, " ")
	fmt.Fprintf(out, "Running: %s\n", line)
	if res, err := r.Run(cctx, c[0], c[1:]...); err != nil {
		return fmt.Errorf("%s: %w: %s", line, err, strings.TrimSpace(res))
	}
	wait := o.Wait
	if wait <= 0 {
		wait = 20 * time.Second
	}
	for deadline := time.Now().Add(wait); ; {
		if held, pid := Held(o.Home); held {
			fmt.Fprintf(out, "Started (pid %s).\n", pid)
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	logs := "journalctl --user -u " + name + ".service"
	if goos == "windows" {
		logs = LogFile(o.Home)
	}
	return fmt.Errorf("the service was started, but no daemon took %s within %s: see `tbd status` and %s", LockPath(o.Home), wait, logs)
}
