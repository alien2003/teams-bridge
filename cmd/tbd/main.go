// Command tbd is the Teams Bridge daemon: it keeps Microsoft Teams web open in a headless
// Edge/Chromium, captures its notifications and pushes them, end-to-end encrypted, to the paired
// Android app through Firebase Cloud Messaging.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/daemon"
	"github.com/alien2003/teams-bridge/internal/proto"
)

// version is set by the build (-ldflags "-X main.version=...").
var version = "dev"

const usage = `tbd — Teams Bridge daemon

Usage:
  tbd [--home DIR] [--log-level LEVEL] <command> [flags]

Commands:
  login                 open Teams in a browser window with the daemon's profile and sign in
  pair [--new]          create (or show) the pairing code and QR, then send pairing pings
  run [--once] [--timeout D]
                        run the daemon (--once: exit after the first notification is delivered)
  stop [--timeout D]    stop the running daemon (gracefully; forcibly after D, default 20s)
  start                 start the installed service now (schtasks /run, systemctl --user start)
  status                show configuration, pairing and daemon state
  test-push             send a test notification to the paired phone
  install [--dry-run]   start tbd automatically at logon (systemd user unit / scheduled task)
  uninstall [--dry-run] remove that again
  version               print the version

The data directory is --home, else $TBD_HOME, else %LOCALAPPDATA%\tbd (Windows) or
~/.local/share/tbd (Linux). config.toml lives inside it.

Signing in again (or pair --new) while the daemon runs: tbd stop, tbd login, tbd start.
`

type globals struct {
	home     string
	logLevel string
	stdout   io.Writer
	stderr   io.Writer
	stdin    io.Reader
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	g := &globals{stdout: stdout, stderr: stderr, stdin: stdin}
	fs := flag.NewFlagSet("tbd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	fs.StringVar(&g.home, "home", "", "data directory")
	fs.StringVar(&g.logLevel, "log-level", "", "debug, info, warn or error (overrides config)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	cmd, rest := fs.Arg(0), fs.Args()[1:]
	var err error
	switch cmd {
	case "login":
		err = g.cmdLogin(rest)
	case "pair":
		err = g.cmdPair(rest)
	case "run":
		err = g.cmdRun(rest)
	case "stop":
		err = g.cmdStop(rest)
	case "start":
		err = g.cmdStart(rest)
	case "status":
		err = g.cmdStatus(rest)
	case "test-push":
		err = g.cmdTestPush(rest)
	case "install":
		err = g.cmdInstall(rest, false)
	case "uninstall":
		err = g.cmdInstall(rest, true)
	case "version", "--version", "-version":
		fmt.Fprintf(stdout, "tbd %s (protocol v%d, %s, %s/%s)\n", version, proto.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "tbd: unknown command %q\n\n", cmd)
		fs.Usage()
		return 2
	}
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintln(stderr, "tbd:", err)
		return 2
	case errors.Is(err, flag.ErrHelp):
		return 0
	default:
		fmt.Fprintln(stderr, "tbd:", err)
		var ce interface{ ExitCode() int }
		if errors.As(err, &ce) {
			return ce.ExitCode()
		}
		return 1
	}
}

type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

// subFlags parses a subcommand's flags; it also accepts --home/--log-level after the command.
func (g *globals) subFlags(name string, args []string, define func(fs *flag.FlagSet)) error {
	fs := flag.NewFlagSet("tbd "+name, flag.ContinueOnError)
	fs.SetOutput(g.stderr)
	fs.StringVar(&g.home, "home", g.home, "data directory")
	fs.StringVar(&g.logLevel, "log-level", g.logLevel, "log level")
	if define != nil {
		define(fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError{fmt.Sprintf("%s: unexpected argument %q", name, fs.Arg(0))}
	}
	return nil
}

// load resolves the home, loads config.toml and builds the logger.
func (g *globals) load() (string, *config.Config, *slog.Logger, error) {
	home, err := config.ResolveHome(g.home)
	if err != nil {
		return "", nil, nil, err
	}
	cfg, _, err := config.Load(home)
	if err != nil {
		return "", nil, nil, err
	}
	level := cfg.LogLevel
	if g.logLevel != "" {
		level = g.logLevel
	}
	return home, cfg, newLogger(g.stderr, level), nil
}

func newLogger(w io.Writer, level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l}))
}

// signalContext is cancelled on SIGINT/SIGTERM (Ctrl+C / Ctrl+Break on Windows).
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func (g *globals) cmdRun(args []string) (err error) {
	// First, before the parent can exit and its PID be reused: under the Windows task tbd follows
	// its console host, which is all that `schtasks /end` terminates (D20).
	pw, pwhy := daemon.WatchConsoleHost()
	defer pw.Close()
	var once bool
	var timeout time.Duration
	var logFile string
	if err := g.subFlags("run", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&once, "once", false, "exit after the first notification has been delivered")
		fs.DurationVar(&timeout, "timeout", 0, "give up after this long (0 = never)")
		fs.StringVar(&logFile, "log-file", "", "append the log to this file instead of stderr (the Windows task uses it)")
	}); err != nil {
		return err
	}
	if logFile != "" {
		f, ferr := openLogFile(logFile)
		if ferr != nil {
			return ferr
		}
		// Under the hidden Windows task the original stderr goes nowhere: a fatal error (bad
		// config, not paired, lock held) must end up in the log file too.
		defer func() {
			if err != nil {
				fmt.Fprintf(f, "time=%s level=ERROR msg=\"tbd run: exiting\" err=%q\n", time.Now().Format(time.RFC3339), err.Error())
			}
			f.Close()
		}()
		g.stderr = f
	}
	home, cfg, log, err := g.load()
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	if pw != nil {
		log.Info("watching the console host: tbd stops when it ends (schtasks /end)", "parent", pw.Image, "parent_pid", pw.PID)
		go func() {
			select {
			case <-pw.Done():
				log.Info("console host exited; stopping", "parent", pw.Image, "parent_pid", pw.PID)
				cancel()
			case <-ctx.Done():
			}
		}()
	} else {
		log.Debug("not watching the parent process", "why", pwhy)
	}
	return daemon.Run(ctx, daemon.Options{Home: home, Config: cfg, Logger: log, Once: once, Timeout: timeout, Version: version})
}

func (g *globals) cmdLogin(args []string) error {
	var timeout time.Duration
	if err := g.subFlags("login", args, func(fs *flag.FlagSet) {
		fs.DurationVar(&timeout, "timeout", 0, "give up after this long (0 = wait until the window is closed)")
	}); err != nil {
		return err
	}
	home, cfg, log, err := g.load()
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	if timeout > 0 {
		var c2 context.CancelFunc
		ctx, c2 = context.WithTimeout(ctx, timeout)
		defer c2()
	}
	fmt.Fprintf(g.stdout, "Opening %s in a browser window. Sign in, wait until Teams has loaded, then close the window.\n", cfg.TeamsURL)
	if err := daemon.Login(ctx, daemon.Options{Home: home, Config: cfg, Logger: log}); err != nil {
		return err
	}
	fmt.Fprintln(g.stdout, "Signed in. Start the daemon with `tbd start` (once installed with `tbd install`) or `tbd run`.")
	return nil
}

func (g *globals) cmdPair(args []string) error {
	var rotate, noPing, ascii, invert bool
	var pingFor time.Duration
	if err := g.subFlags("pair", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&rotate, "new", false, "replace the existing secret (every paired phone must pair again)")
		fs.BoolVar(&noPing, "no-ping", false, "do not send pairing pings")
		fs.BoolVar(&ascii, "ascii", false, "draw the QR code with ASCII characters")
		fs.BoolVar(&invert, "invert", false, "invert the QR code (light terminal background)")
		fs.DurationVar(&pingFor, "ping-for", 10*time.Minute, "how long to keep sending pairing pings")
	}); err != nil {
		return err
	}
	home, cfg, log, err := g.load()
	if err != nil {
		return err
	}
	if rotate {
		// A running daemon keeps sealing with the secret it started with: a new pairing would get
		// nothing until it restarts.
		if held, pid := daemon.Held(home); held {
			return fmt.Errorf("the daemon (pid %s) is running with the current pairing: run `tbd stop`, then `tbd pair --new`, then `tbd start`", pid)
		}
	}
	secret, err := cfg.PairingSecret()
	switch {
	case err == nil && !rotate:
		fmt.Fprintln(g.stdout, "Already paired; showing the existing code (use --new to replace it).")
	case err == nil || errors.Is(err, config.ErrNotPaired):
		if secret, err = proto.NewSecret(nil); err != nil {
			return err
		}
		cfg.Secret = secret.Code()
		if err := config.Save(home, cfg); err != nil {
			return err
		}
		fmt.Fprintf(g.stdout, "New pairing secret saved to %s\n", config.Path(home))
	default:
		return err
	}
	fmt.Fprintln(g.stdout, "\nScan this with Teams Bridge on the phone, or type the code:")
	fmt.Fprintln(g.stdout)
	if err := printQR(g.stdout, secret.QRText(), invert, ascii); err != nil {
		return err
	}
	fmt.Fprintf(g.stdout, "\n    %s\n\n", secret.DisplayCode())
	if noPing {
		return nil
	}
	sender, err := daemon.NewSender(cfg, home, log)
	if err != nil {
		fmt.Fprintf(g.stdout, "Not sending pairing pings: %v\nConfigure fcm_service_account in %s, then run `tbd test-push`.\n", err, config.Path(home))
		return nil
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, c2 := context.WithTimeout(ctx, pingFor)
	defer c2()
	go func() { // Enter ends the pinging early; a closed stdin (EOF) does not.
		if _, err := bufio.NewReader(g.stdin).ReadString('\n'); err == nil {
			c2()
		}
	}()
	fmt.Fprintf(g.stdout, "Sending a pairing ping every 20 s for up to %s. Press Enter once the phone shows \"Paired ✓\".\n", pingFor)
	n, err := daemon.PairPings(ctx, sender, secret, 20*time.Second, log)
	fmt.Fprintf(g.stdout, "Sent %d pairing ping(s).\n", n)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func (g *globals) cmdTestPush(args []string) error {
	if err := g.subFlags("test-push", args, nil); err != nil {
		return err
	}
	home, cfg, log, err := g.load()
	if err != nil {
		return err
	}
	secret, err := cfg.PairingSecret()
	if err != nil {
		return err
	}
	sender, err := daemon.NewSender(cfg, home, log)
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, c2 := context.WithTimeout(ctx, time.Minute)
	defer c2()
	name, err := daemon.SendMessage(ctx, sender, secret, proto.Message{T: proto.TypeMsg, Title: "Teams Bridge", Body: "Test notification from tbd", URL: cfg.TeamsURL, Tag: "tbd-test"})
	if err != nil {
		return err
	}
	fmt.Fprintf(g.stdout, "Sent (%s).\n", name)
	return nil
}

func (g *globals) cmdStatus(args []string) error {
	if err := g.subFlags("status", args, nil); err != nil {
		return err
	}
	home, cfg, _, err := g.load()
	if err != nil {
		return err
	}
	return daemon.PrintStatus(context.Background(), g.stdout, home, cfg, version)
}

func (g *globals) cmdStop(args []string) error {
	timeout := daemon.DefaultStopTimeout
	if err := g.subFlags("stop", args, func(fs *flag.FlagSet) {
		fs.DurationVar(&timeout, "timeout", timeout, "how long to wait for a graceful stop before ending the daemon")
	}); err != nil {
		return err
	}
	if timeout <= 0 {
		return usageError{"stop: --timeout must be positive"}
	}
	// No config needed: stopping must work even when config.toml is broken.
	home, err := config.ResolveHome(g.home)
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	return daemon.Stop(ctx, daemon.StopOptions{Home: home, Timeout: timeout, Out: g.stdout})
}

func (g *globals) cmdStart(args []string) error {
	if err := g.subFlags("start", args, nil); err != nil {
		return err
	}
	home, err := config.ResolveHome(g.home)
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	return daemon.Start(ctx, g.stdout, daemon.StartOptions{Home: home})
}

func (g *globals) cmdInstall(args []string, uninstall bool) error {
	var dry bool
	name := "install"
	if uninstall {
		name = "uninstall"
	}
	if err := g.subFlags(name, args, func(fs *flag.FlagSet) {
		fs.BoolVar(&dry, "dry-run", false, "print what would be written and executed, change nothing")
	}); err != nil {
		return err
	}
	home, _, _, err := g.load()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	return daemon.Install(ctx, g.stdout, daemon.InstallOptions{Home: home, Exe: exe, Uninstall: uninstall, DryRun: dry})
}
