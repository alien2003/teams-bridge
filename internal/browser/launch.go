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
	"runtime"
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
	UserAgent string
	ExtraArgs []string // extra switches (config browser_args), appended last
	// ImplicitSignin keeps Edge's implicit sign-in of a new profile with the Windows account. Off by
	// default (msImplicitSignin is disabled): the daemon's profile should not be signed in to the
	// user's Microsoft account, open a sync-consent page, or touch OneAuth/WAM (measured, D14).
	ImplicitSignin bool
	Env            []string      // environment; nil means os.Environ()
	Logger         *slog.Logger  // nil means slog.Default()
	StartTimeout   time.Duration // 0 means DefaultStartTimeout
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

// ProfileDir is the persistent profile directory (Chromium's user data dir) inside home.
func ProfileDir(home string) string { return filepath.Join(home, "profile") }

// ProfileName is the profile inside ProfileDir (--profile-directory), instead of "Default".
const ProfileName = "tbd"

// DisabledEdgeFeatures are switched off unless LaunchOptions.ImplicitSignin (unknown feature names
// are ignored by Chromium and other browsers).
var DisabledEdgeFeatures = []string{"msImplicitSignin"}

// mergeDisableFeatures appends extra, folding our features into the user's own --disable-features
// switch if there is one: Chromium honours only the last occurrence of a switch.
func mergeDisableFeatures(extra, ours []string) []string {
	if len(ours) == 0 {
		return extra
	}
	const sw = "--disable-features="
	out := append([]string(nil), extra...)
	for i := len(out) - 1; i >= 0; i-- {
		if strings.HasPrefix(out[i], sw) {
			if v := strings.TrimPrefix(out[i], sw); v != "" {
				out[i] = sw + v + "," + strings.Join(ours, ",")
			} else {
				out[i] = sw + strings.Join(ours, ",")
			}
			return out
		}
	}
	return append([]string{sw + strings.Join(ours, ",")}, out...)
}

// CrashesDir is the crash-dump directory inside home.
func CrashesDir(home string) string { return filepath.Join(home, "crashes") }

// DiskCacheDir is the browser's disk cache inside home, passed as --disk-cache-dir on macOS: there
// Chromium maps a profile under ~/Library/Application Support to a cache under
// ~/Library/Caches/<the same relative path>, which would put the default data directory's cache
// outside it (docs/DECISIONS.md D26).
func DiskCacheDir(home string) string { return filepath.Join(home, "cache") }

// mappedCacheDir is where Chromium on macOS maps the cache of the profile dir profile (absolute):
// a profile under <userHome>/Library/Application Support/<rel> gets
// <userHome>/Library/Caches/<rel>; elsewhere the cache stays in the profile and this returns "".
// Even with --disk-cache-dir the browser creates that directory tree, empty (measured on GitHub's
// macOS runners, D26); removeEmptyMappedCache removes it once the browser has exited.
func mappedCacheDir(userHome, profile string) string {
	if userHome == "" || !filepath.IsAbs(profile) {
		return ""
	}
	lib := filepath.Join(userHome, "Library")
	rel, err := filepath.Rel(filepath.Join(lib, "Application Support"), filepath.Clean(profile))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.Join(lib, "Caches", rel)
}

// removeEmptyMappedCache removes the empty directory tree Chromium leaves at the mappedCacheDir of
// the profile ProfileName inside profile (Chromium's user data dir), for the user home userHome: the
// leaf and then each parent up to, not including, ~/Library/Caches, stopping at the first one
// that is not empty (os.Remove never removes a directory with anything in it); a missing one is
// skipped.
func removeEmptyMappedCache(userHome, profile string, log *slog.Logger) {
	abs, err := filepath.Abs(profile)
	if err != nil {
		return
	}
	dir := mappedCacheDir(userHome, filepath.Join(abs, ProfileName))
	if dir == "" {
		return
	}
	stop := filepath.Join(userHome, "Library", "Caches")
	for ; dir != stop && strings.HasPrefix(dir, stop+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Debug("left the browser's mapped cache dir", "err", err)
			return
		}
	}
}

// platformLaunchArgsFor returns the extra switches for goos (runtime.GOOS in Launch; a parameter so
// that the tests of every OS cover every list). The sandbox stays on everywhere: see LaunchOptions
// and docs/DECISIONS.md.
//
//   - Linux and other Unix: --disable-gpu headless (without a GPU, as headless and in WSL, the GPU
//     probe only logs errors and slows the start).
//   - Windows: nothing; msedge.exe runs with its sandbox and GPU as usual.
//   - macOS (D26): --disable-gpu headless, as on Linux; --use-mock-keychain always, for the
//     headless daemon and the windowed login alike, so the two always agree on the key of the
//     cookie store: without it Edge creates and reads "Microsoft Edge Safe Storage" in the user's
//     login keychain (the item the user's own Edge shares by name), which can prompt, or block
//     under launchd while the keychain is locked; --disk-cache-dir=<home>/cache always (see
//     DiskCacheDir). No --password-store: that switch is Linux-only.
func platformLaunchArgsFor(goos string, headless bool, home string) []string {
	var args []string
	switch goos {
	case "windows":
		return nil
	case "darwin":
		if headless {
			args = append(args, "--disable-gpu")
		}
		args = append(args, "--use-mock-keychain", "--disk-cache-dir="+DiskCacheDir(home))
	default:
		if headless {
			args = append(args, "--disable-gpu")
		}
	}
	return args
}

// platformDisabledFeaturesFor are features switched off on goos whatever LaunchOptions say (merged
// into the same --disable-features switch as DisabledEdgeFeatures). On macOS: the native
// notification paths. worker.js still calls the original showNotification after it has captured a
// notification; on a Mac that would go to the Notification Center, i.e. a permission prompt for
// the browser that nobody answers, or real banners repeating Teams content on the Mac. Without them
// Chromium falls back to its own message center, which is invisible headless (what Linux headless
// already does). Capture is unaffected: the hook sees the call before the browser does.
func platformDisabledFeaturesFor(goos string) []string {
	if goos == "darwin" {
		return []string{"NativeNotifications", "SystemNotifications"}
	}
	return nil
}

// platformBrowserEnvFor are environment variables set for the browser on goos. On macOS Crashpad
// ignores --user-data-dir and --crash-dumps-dir and would write into the user's own
// ~/Library/Application Support/Microsoft Edge/Crashpad; BREAKPAD_DUMP_LOCATION points it at
// CrashesDir(home) instead (with --disable-crash-reporter nothing is uploaded either way).
func platformBrowserEnvFor(goos, home string) map[string]string {
	if goos == "darwin" {
		return map[string]string{"BREAKPAD_DUMP_LOCATION": CrashesDir(home)}
	}
	return nil
}

// TempDir is the browser's private temp directory on Windows (TMP and TEMP point there). Chromium
// creates scoped_dir* folders and Edge a cv_debug.log in the temp directory, and a browser that is
// killed never removes them: without this they would pile up in the user's own %TEMP%. Launch
// empties it first (only one browser per home runs at a time: tbd.lock). Elsewhere the browser
// keeps the caller's TMPDIR (see browserTempKeys in launch_unix.go).
func TempDir(home string) string { return filepath.Join(home, "tmp") }

// withEnv returns env with each key set to val, replacing earlier entries (case-insensitively, as
// Windows treats variable names).
func withEnv(env []string, val string, keys ...string) []string {
	out := make([]string, 0, len(env)+len(keys))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, k := range keys {
			if strings.EqualFold(name, k) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	for _, k := range keys {
		out = append(out, k+"="+val)
	}
	return out
}

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
	// A browser left running by an earlier daemon that died without closing it (on macOS there is
	// no Pdeathsig) holds the profile: the new one would hand over to it and exit.
	killStaleBrowser(profile, log)
	tmp := TempDir(o.Home)
	if len(browserTempKeys) > 0 {
		if err := os.RemoveAll(tmp); err != nil {
			log.Warn("could not empty the browser's temp dir", "err", err)
		}
		if err := os.MkdirAll(tmp, 0o700); err != nil {
			return nil, fmt.Errorf("browser: temp dir: %w", err)
		}
	}

	// A DevToolsActivePort left in the persistent profile by an earlier (killed or crashed) browser
	// would make the fallback below report a dead or foreign port.
	if err := os.Remove(filepath.Join(profile, "DevToolsActivePort")); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn("could not remove a stale DevToolsActivePort", "err", err)
	}

	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + profile,
		// A profile name of our own: on Windows, Edge mirrors each profile's preference MACs into
		// HKCU\Software\Microsoft\Edge\PreferenceMACs\<profile name>, whatever --user-data-dir says;
		// with the default name "Default" it overwrote the user's own Edge profile's entries
		// (measured, docs/DECISIONS.md D14).
		"--profile-directory=" + ProfileName,
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
	args = append(args, platformLaunchArgsFor(runtime.GOOS, o.Headless, o.Home)...)
	var disable []string
	if !o.ImplicitSignin {
		disable = append(disable, DisabledEdgeFeatures...)
	}
	disable = append(disable, platformDisabledFeaturesFor(runtime.GOOS)...)
	args = append(args, mergeDisableFeatures(o.ExtraArgs, disable)...)
	args = append(args, "about:blank")

	cmd := exec.Command(o.Path, args...)
	env := o.Env
	if env == nil {
		env = os.Environ()
	}
	if len(browserTempKeys) > 0 {
		env = withEnv(env, tmp, browserTempKeys...)
	}
	for k, v := range platformBrowserEnvFor(runtime.GOOS, o.Home) {
		env = withEnv(env, v, k)
	}
	cmd.Env = env
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
		if runtime.GOOS == "darwin" {
			if uh, err := os.UserHomeDir(); err == nil {
				removeEmptyMappedCache(uh, profile, log)
			}
		}
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
