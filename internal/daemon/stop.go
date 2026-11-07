package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/alien2003/teams-bridge/internal/service"
)

// DefaultStopTimeout is how long `tbd stop` waits for a graceful stop before it ends the daemon.
// The daemon itself gives the outbox (daemon_stopping) up to stopTimeout, also 20 s.
const DefaultStopTimeout = 20 * time.Second

// goneGrace is how long Stop, after a failed request or force, waits for the lock to turn out
// released (the kernel drops it as the exiting holder's handles close).
const goneGrace = time.Second

// StopEventName is the Windows named event that `tbd stop` sets to ask the holder of a lock file
// to stop, given the file's volume serial number and file index. The name lives in the session's
// Local\ namespace and carries only a truncated hash, so it reveals no path or user name.
func StopEventName(volume uint32, index uint64) string {
	h := sha256.Sum256(fmt.Appendf(nil, "tbd.lock\x00%08x\x00%016x", volume, index))
	return `Local\tbd-stop-` + hex.EncodeToString(h[:8])
}

// StopOptions configure Stop.
type StopOptions struct {
	Home    string
	Timeout time.Duration // graceful phase; 0 → DefaultStopTimeout
	Out     io.Writer
	// Runner asks systemd (Linux) or launchd (macOS) whether the holder is the service's main
	// process; nil → exec.
	Runner service.Runner
	// Name is the service's name; default service.DefaultName.
	Name string

	// Test hooks; zero values mean production behaviour.
	poll    time.Duration
	settle  time.Duration
	request func(ctx context.Context, pid int) (how string, err error) // how == "": could not ask
	force   func(pid int) error
	goos    string       // default runtime.GOOS
	env     *service.Env // default service.DefaultEnv()
}

// Stop stops the process that holds home's lock: it asks it to stop gracefully (the SIGTERM path:
// daemon_stopping per D13, browser closed, lock released), waits up to Timeout for the lock to be
// released, and only then ends the process forcibly. It acts only on a verified lock holder
// (LockHolder); with no holder it reports that and returns nil.
func Stop(ctx context.Context, o StopOptions) error {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	settle := o.settle
	if settle <= 0 {
		settle = holderSettle
	}
	request, force := o.request, o.force
	if request == nil {
		request = func(ctx context.Context, pid int) (string, error) { return requestStop(ctx, o, pid) }
	}
	if force == nil {
		force = func(pid int) error { return forceStop(o.Home, pid) }
	}

	pid, err := lockHolder(o.Home, settle)
	if err != nil {
		return err
	}
	if pid == 0 {
		fmt.Fprintf(out, "tbd is not running (nothing holds %s).\n", LockPath(o.Home))
		return nil
	}
	// A step that fails because the holder is already gone (it was stopping anyway: a second
	// `tbd stop`, or the daemon's own shutdown ending right at the deadline) is no failure: the
	// goal, a released lock, is reached. errNotHolder from the re-check, os.ErrProcessDone, or a
	// PID that no longer opens all end here; only a holder that still holds the lock is an error.
	goneMeanwhile := func(err error, format string) error {
		if done, note, werr := waitReleased(ctx, o.Home, pid, goneGrace, o.poll); werr == nil && done {
			fmt.Fprintf(out, "tbd (pid %d) stopped in the meantime%s.\n", pid, note)
			return nil
		}
		return fmt.Errorf(format, pid, err)
	}
	how, err := request(ctx, pid)
	if err != nil {
		return goneMeanwhile(err, "asking tbd (pid %d) to stop: %w")
	}
	if how != "" {
		fmt.Fprintf(out, "Stopping tbd (pid %d, %s); waiting up to %s.\n", pid, how, timeout)
	} else {
		fmt.Fprintf(out, "tbd (pid %d) does not listen for `tbd stop` (a tbd from before this version, or one in another Windows session); it is ended if it still runs after %s.\n", pid, timeout)
	}
	start := time.Now()
	if done, note, err := waitReleased(ctx, o.Home, pid, timeout, o.poll); err != nil {
		return err
	} else if done {
		fmt.Fprintf(out, "Stopped after %s%s.\n", time.Since(start).Round(100*time.Millisecond), note)
		return nil
	}
	fmt.Fprintf(out, "tbd (pid %d) did not stop within %s; ending it forcibly (no daemon_stopping alert reaches the phone).\n", pid, timeout)
	if err := force(pid); err != nil {
		return goneMeanwhile(err, "ending tbd (pid %d): %w")
	}
	if done, note, err := waitReleased(ctx, o.Home, pid, 15*time.Second, o.poll); err != nil {
		return err
	} else if done {
		fmt.Fprintf(out, "Ended%s.\n", note)
		return nil
	}
	return fmt.Errorf("tbd (pid %d) still holds %s after it was ended", pid, LockPath(o.Home))
}

// waitReleased polls until pid no longer holds home's lock, for at most timeout. note is set when
// another process has taken the lock over in the meantime.
func waitReleased(ctx context.Context, home string, pid int, timeout, poll time.Duration) (done bool, note string, err error) {
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		held, p := Held(home)
		switch {
		case !held:
			return true, "", nil
		case p != "" && parsePID(p) != pid:
			return true, fmt.Sprintf("; another tbd (pid %s) holds the data directory now", p), nil
		}
		if time.Now().After(deadline) {
			return false, "", nil
		}
		select {
		case <-ctx.Done():
			return false, "", ctx.Err()
		case <-time.After(poll):
		}
	}
}

// requestStop asks the holder to stop gracefully. A holder that is the service's own process is
// stopped through its service manager, since a process that merely exits would be started again:
// on Linux tbd.service (Restart=always, 10 s later) with `systemctl --user stop --no-block`; on
// macOS the LaunchAgent (KeepAlive restarts it after SIGKILL or a non-zero exit) with `launchctl
// bootout`, which sends SIGTERM and never restarts a job whose removal is pending. Either would
// otherwise race `tbd login`. Any other holder (a `tbd run` in a terminal) gets SIGTERM.
func requestStop(ctx context.Context, o StopOptions, pid int) (string, error) {
	goos, name, r := o.goos, o.Name, o.Runner
	if goos == "" {
		goos = runtime.GOOS
	}
	if name == "" {
		name = service.DefaultName
	}
	if r == nil {
		r = service.ExecRunner{}
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch goos {
	case "linux":
		if main, err := service.MainPID(cctx, r, name); err == nil && main == pid {
			c := service.StopCommand(name)
			if out, err := r.Run(cctx, c[0], c[1:]...); err != nil {
				return "", fmt.Errorf("%s: %w: %s", strings.Join(c, " "), err, strings.TrimSpace(out))
			}
			return strings.Join(c, " "), nil
		}
	case "darwin":
		env := service.DefaultEnv()
		if o.env != nil {
			env = *o.env
		}
		job, err := service.QueryLaunchd(cctx, r, name, env)
		if err != nil || !job.Loaded || job.PID != pid {
			break
		}
		target, _ := service.LaunchdTarget(name, env)
		c := service.LaunchdBootout(target)
		line := strings.Join(c, " ")
		out, err := r.Run(cctx, c[0], c[1:]...)
		switch {
		case err == nil:
			return line, nil
		case cctx.Err() != nil:
			// bootout waits for the job to exit; the removal is under way in launchd all the same.
			return line + ", in progress", nil
		}
		// bootout refused: SIGTERM still stops it (exit 0 is not restarted), only a later forced
		// kill would be.
		how, serr := requestStopOS(o.Home, pid)
		if serr != nil {
			return "", fmt.Errorf("%s: %w: %s; SIGTERM: %w", line, err, strings.TrimSpace(out), serr)
		}
		return fmt.Sprintf("%s, as %s failed: %s", how, line, strings.TrimSpace(out)), nil
	}
	return requestStopOS(o.Home, pid)
}

// isTbdImage reports whether the image path names tbd.exe (by base name, case-insensitively). A
// renamed binary is not recognised on purpose: forcing is for tbd only, and a lock holder of
// another name (a test binary, say) is left alone.
func isTbdImage(image string) bool { return strings.EqualFold(baseName(image), "tbd.exe") }

// baseName is the last element of a Windows or Unix path, independently of the host OS.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// errNotHolder means the process to signal no longer holds the lock.
var errNotHolder = errors.New("no longer holds the data directory's lock")

// verifyHolder re-checks, right before a signal, that pid still holds home's lock.
func verifyHolder(home string, pid int) error {
	h, err := LockHolder(home)
	if err != nil {
		return err
	}
	if h != pid {
		return fmt.Errorf("pid %d %w", pid, errNotHolder)
	}
	return nil
}
