// Package browser launches a headless (or windowed) Edge/Chromium with a persistent profile, parses
// its DevTools endpoint, and drives one Teams page over CDP with chromedp: the Windows Edge user
// agent and client hints, the notifications permission, the notification hook (internal/hook) in
// every document and service worker, and the signals the daemon needs (bindings, main-frame
// navigations, responses, disconnect). It never writes anything into the page.
package browser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp/remote"
)

// DefaultStartTimeout is how long Launch waits for the DevTools endpoint to appear.
const DefaultStartTimeout = 30 * time.Second

// closeGrace is how long Close waits for the browser to exit after Browser.close before it kills
// the process tree, unless the caller's context ends first.
const closeGrace = 10 * time.Second

// LaunchOptions configure Launch.
type LaunchOptions struct {
	Path     string // browser executable (from config.DetectBrowser)
	Home     string // data dir; profile = Home/profile, crash dumps = Home/crashes
	Headless bool   // add --headless=new
	// UserAgent, if set, becomes the browser-wide default user agent (--user-agent). Pass the same
	// string the session overrides per page (UserAgent(major), or config user_agent): the
	// per-target CDP override cannot reach a service worker's main-script fetch, nor the worker's
	// first requests before it is hooked, which would otherwise carry the real (headless, and on
	// Linux X11) user agent. Empty means no switch.
	UserAgent    string
	ExtraArgs    []string      // extra switches (config browser_args), appended last
	Env          []string      // environment; nil means os.Environ()
	Logger       *slog.Logger  // nil means slog.Default()
	StartTimeout time.Duration // 0 means DefaultStartTimeout
}

// Process is a running browser.
type Process struct {
	WSURL string // ws://127.0.0.1:<port>/devtools/browser/<id>
	PID   int

	cmd        *exec.Cmd
	tree       *procTree // OS-specific handle on the process tree (a job object on Windows)
	log        *slog.Logger
	done       chan struct{}
	closed     chan struct{} // closed by Close to stop it being called twice
	stderrDone chan struct{} // closed when stderr reached EOF

	mu        sync.Mutex
	err       error
	noSandbox bool // stderr said Chromium's sandbox is unusable
}

// ProfileDir is the persistent profile directory inside home.
func ProfileDir(home string) string { return filepath.Join(home, "profile") }

// CrashesDir is the crash-dump directory inside home.
func CrashesDir(home string) string { return filepath.Join(home, "crashes") }

var wsLineRE = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

// Launch starts the browser and returns once its DevTools endpoint is known. It keeps draining the
// browser's stderr into the logger at debug level. Call Close to stop it; the caller must do so
// (e.g. via t.Cleanup) so no orphan browser is left behind.
func Launch(ctx context.Context, o LaunchOptions) (*Process, error) {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	if o.Path == "" {
		return nil, errors.New("browser: no executable path")
	}
	profile := ProfileDir(o.Home)
	crashes := CrashesDir(o.Home)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, fmt.Errorf("browser: profile dir: %w", err)
	}
	if err := os.MkdirAll(crashes, 0o700); err != nil {
		return nil, fmt.Errorf("browser: crashes dir: %w", err)
	}

	// A DevToolsActivePort left in the persistent profile by an earlier (killed or crashed) browser
	// would make the fallback below report a dead or foreign port.
	if err := os.Remove(filepath.Join(profile, "DevToolsActivePort")); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn("could not remove a stale DevToolsActivePort", "err", err)
	}

	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-crash-reporter",
		"--crash-dumps-dir=" + crashes,
	}
	if o.Headless {
		args = append(args, "--headless=new")
	}
	if o.UserAgent != "" {
		args = append(args, "--user-agent="+o.UserAgent)
	}
	// Chromium's sandbox stays on everywhere: the browser holds a production Teams session. Where
	// it cannot work (containers without user namespaces), add --no-sandbox via browser_args.
	args = append(args, platformLaunchArgs(o.Headless)...)
	args = append(args, o.ExtraArgs...)
	args = append(args, "about:blank")

	cmd := exec.Command(o.Path, args...)
	cmd.Env = o.Env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	configureSysProcAttr(cmd)

	// Our own pipe rather than cmd.StderrPipe: Wait closes a StderrPipe as soon as the main process
	// exits, which can drop its last lines (such as a fatal "No usable sandbox"). With an *os.File
	// as Stderr, the reader sees everything up to EOF, i.e. until the browser and its children have
	// all closed their copy.
	stderr, stderrW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("browser: stderr pipe: %w", err)
	}
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		stderr.Close()
		stderrW.Close()
		return nil, fmt.Errorf("browser: start %s: %w", o.Path, err)
	}
	stderrW.Close()

	p := &Process{
		PID:        cmd.Process.Pid,
		cmd:        cmd,
		log:        log,
		done:       make(chan struct{}),
		closed:     make(chan struct{}),
		stderrDone: make(chan struct{}),
	}
	p.tree = newProcTree(cmd, log)

	// Scan stderr for the DevTools line, then keep draining it in the background.
	wsCh := make(chan string, 1)
	go p.scanStderr(stderr, wsCh)

	// Wait on the process in the background.
	go func() {
		werr := cmd.Wait()
		p.mu.Lock()
		if p.err == nil {
			p.err = werr
		}
		p.mu.Unlock()
		close(p.done)
	}()

	timeout := o.StartTimeout
	if timeout <= 0 {
		timeout = DefaultStartTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	// The DevTools URL comes from stderr, or, as a fallback, from the DevToolsActivePort file.
	portFileCh := make(chan string, 1)
	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()
	go pollDevToolsActivePort(pollCtx, profile, portFileCh)

	select {
	case ws := <-wsCh:
		p.WSURL = ws
	case ws := <-portFileCh:
		p.WSURL = ws
	case <-p.done:
		p.tree.release()
		// Let the scanner see the last lines before deciding which error to report.
		select {
		case <-p.stderrDone:
		case <-time.After(2 * time.Second):
		}
		if p.sandboxFailed() {
			return nil, fmt.Errorf("browser: exited before the DevTools endpoint appeared (%w): Chromium reports "+
				"no usable sandbox here; if this is a container without user namespaces, set "+
				`browser_args = ["--no-sandbox"] in config.toml`, p.Err())
		}
		return nil, fmt.Errorf("browser: exited before the DevTools endpoint appeared: %w", p.Err())
	case <-deadline.C:
		p.killNow()
		<-p.done
		p.tree.release()
		return nil, fmt.Errorf("browser: timed out after %s waiting for the DevTools endpoint", timeout)
	case <-ctx.Done():
		p.killNow()
		<-p.done
		p.tree.release()
		return nil, ctx.Err()
	}
	log.Debug("browser DevTools endpoint", "ws", p.WSURL, "pid", p.PID)
	return p, nil
}

func (p *Process) scanStderr(r io.ReadCloser, wsCh chan<- string) {
	defer close(p.stderrDone)
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sent := false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "No usable sandbox") {
			p.mu.Lock()
			p.noSandbox = true
			p.mu.Unlock()
		}
		if !sent {
			if m := wsLineRE.FindStringSubmatch(line); m != nil {
				select {
				case wsCh <- m[1]:
				default:
				}
				sent = true
			}
		}
		p.log.Debug("browser stderr", "line", line)
	}
	// Never stop reading (a line over the scanner's limit ends Scan): a full pipe would block the
	// browser on its next log write.
	_, _ = io.Copy(io.Discard, r)
}

// pollDevToolsActivePort watches <profile>/DevToolsActivePort as a fallback for the stderr line. The
// file holds the port on line 1 and the browser path (…/devtools/browser/<id>) on line 2.
func pollDevToolsActivePort(ctx context.Context, profile string, out chan<- string) {
	path := filepath.Join(profile, "DevToolsActivePort")
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			lines := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
			if len(lines) < 1 || lines[0] == "" {
				continue
			}
			port := strings.TrimSpace(lines[0])
			browserPath := "/"
			if len(lines) == 2 {
				browserPath = strings.TrimSpace(lines[1])
			}
			ws := "ws://127.0.0.1:" + port + browserPath
			select {
			case out <- ws:
			case <-ctx.Done():
			}
			return
		}
	}
}

func (p *Process) sandboxFailed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.noSandbox
}

// Done is closed when the browser process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Err returns the process's exit error once Done is closed.
func (p *Process) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Close shuts the browser down: a graceful CDP Browser.close, then, if the process is still alive
// after a grace period (10 s, or earlier when ctx ends), a kill of the whole process tree. Child
// processes that outlive the main process are swept up too. It is safe to call more than once and
// from several goroutines; every call returns once the browser has exited.
func (p *Process) Close(ctx context.Context) error {
	select {
	case <-p.closed:
		<-p.done
		return p.Err()
	default:
		close(p.closed)
	}
	defer p.tree.release()

	select {
	case <-p.done:
		p.tree.sweep()
		return p.Err() // already gone
	default:
	}

	if p.WSURL != "" {
		if err := p.gracefulClose(ctx); err != nil {
			p.log.Debug("graceful browser close failed; will kill", "err", err)
		}
	}

	grace := time.NewTimer(closeGrace)
	defer grace.Stop()
	select {
	case <-p.done:
		p.tree.sweep()
		return p.Err()
	case <-ctx.Done():
	case <-grace.C:
	}

	p.killNow()
	<-p.done
	return p.Err()
}

// gracefulClose sends Browser.close over a short-lived DevTools websocket connection.
func (p *Process) gracefulClose(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := remote.DialContext(dctx, p.WSURL)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	msg := &cdproto.Message{ID: 1, Method: cdproto.MethodType(browser.CommandClose)}
	if err := conn.Write(dctx, msg); err != nil {
		return fmt.Errorf("write Browser.close: %w", err)
	}
	// Give the browser a moment to act on it; the reply (or EOF) is enough.
	reply := &cdproto.Message{}
	_ = conn.Read(dctx, reply)
	return nil
}

func (p *Process) killNow() {
	p.mu.Lock()
	if p.err == nil {
		p.err = errors.New("browser: killed")
	}
	p.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		p.tree.kill()
	}
}
