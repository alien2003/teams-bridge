package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/alien2003/teams-bridge/internal/config"
)

// DefaultLoginSettle is how long the main frame must stay on the Teams origin before Login
// considers the user signed in. Teams first loads its shell on its own origin and only then (from
// script) redirects a signed-out user to the Microsoft login host, so the first navigation alone
// proves nothing.
const DefaultLoginSettle = 10 * time.Second

// ErrNotSignedIn is returned by Login when the window was closed before the page had settled on
// the Teams origin.
var ErrNotSignedIn = errors.New("browser: the window was closed before Teams finished signing in; run `tbd login` again")

// LoginOptions configure Login.
type LoginOptions struct {
	Path       string
	Home       string
	TeamsURL   string
	LoginHosts []string
	ExtraArgs  []string
	// ImplicitSignin: see LaunchOptions.ImplicitSignin.
	ImplicitSignin bool
	// UserAgent, if set, is passed as --user-agent and applied to the page together with the client
	// hints for Version, so the interactive sign-in presents the same platform as the daemon will
	// (Conditional Access evaluates the device platform at sign-in).
	UserAgent string
	Version   config.Version
	// Settle overrides DefaultLoginSettle (tests).
	Settle time.Duration
	Logger *slog.Logger

	// OnReady, if set, is called with the browser's DevTools websocket URL once the window is up,
	// so a test can drive it (for example, close it once OnSignedIn fired).
	OnReady func(wsURL string)
	// OnSignedIn, if set, is called once, when Login first considers the user signed in.
	OnSignedIn func()

	headless bool // tests only: run the same flow without a window
}

// Login runs `tbd login`: it launches the same profile in a visible window (no --headless),
// navigates to the Teams URL, logs once the main frame has settled on the Teams origin (not a login
// host), and then waits for the user to close the window: the browser process exits, or no page is
// left (on macOS closing the last window does not quit the browser), in which case Login closes
// the browser itself. It returns nil if the user was signed in by then, ErrNotSignedIn otherwise.
// If ctx is cancelled it closes the browser and returns ctx.Err().
func Login(ctx context.Context, o LoginOptions) error {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	settle := o.Settle
	if settle <= 0 {
		settle = DefaultLoginSettle
	}

	proc, err := Launch(ctx, LaunchOptions{
		Path: o.Path, Home: o.Home, Headless: o.headless, UserAgent: o.UserAgent, ExtraArgs: o.ExtraArgs, ImplicitSignin: o.ImplicitSignin, Logger: log,
	})
	if err != nil {
		return loginLaunchHint(runtime.GOOS, o.headless, os.Getenv("SSH_CONNECTION"), err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = proc.Close(cctx)
	}()

	pageCtx, cancelPage, err := openPage(ctx, proc.WSURL)
	if err != nil {
		return err
	}
	defer cancelPage()

	// "On Teams" = the teams_url origin or one of the domains Teams moves users between (the same
	// set the session detector uses).
	origins := []string{originOf(o.TeamsURL)}
	for _, x := range config.DefaultPermissionOrigins {
		origins = append(origins, originOf(x))
	}
	st := &loginState{settle: settle, teamsOrigins: origins, loginHosts: o.LoginHosts, log: log, onSignedIn: o.OnSignedIn}
	navs := chromedp.Events(pageCtx, page.FrameNavigated)
	go func() {
		for ev, err := range navs {
			if err != nil {
				return
			}
			if ev.Frame == nil || ev.Frame.ParentID != "" {
				continue
			}
			st.navigated(ev.Frame.URL)
		}
	}()
	defer st.stop()

	if err := chromedp.Do(pageCtx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		if o.UserAgent != "" {
			var meta *emulation.UserAgentMetadata
			if looksLikeChromiumUA(o.UserAgent) && o.Version.Full != "" {
				meta = UserAgentMetadata(o.Version.Full)
			}
			if _, err := cdp.Call(ctx, t, emulation.SetUserAgentOverride, uaOverrideFor(runtime.GOOS, o.UserAgent, meta)); err != nil {
				return fmt.Errorf("setUserAgentOverride: %w", err)
			}
		}
		if _, err := cdp.Call(ctx, t, page.Navigate, page.NavigateParams{URL: o.TeamsURL}); err != nil {
			return err
		}
		if runtime.GOOS == "darwin" && !o.headless {
			// A browser started from Terminal opens its window behind it; best effort.
			if _, err := cdp.Call(ctx, t, page.BringToFront, cdp.Empty{}); err != nil {
				log.Debug("bringToFront failed", "err", err)
			}
		}
		return nil
	})); err != nil {
		return err
	}
	noPages := watchPages(pageCtx, log)
	log.Info("sign in to Teams in the browser window; close the window (or quit the browser) when Teams has loaded")

	if o.OnReady != nil {
		o.OnReady(proc.WSURL)
	}

	select {
	case <-proc.Done():
	case <-noPages:
		log.Debug("no page left in the login browser; closing it")
	case <-ctx.Done():
		return ctx.Err()
	}
	if !st.signedIn() {
		return ErrNotSignedIn
	}
	return nil
}

// loginLaunchHint adds a hint to a failed start of the login window on macOS over SSH, where the
// browser has no window server session to open a window in.
func loginLaunchHint(goos string, headless bool, sshConnection string, err error) error {
	if goos == "darwin" && !headless && sshConnection != "" {
		return fmt.Errorf("%w (this shell came in over SSH, where no browser window can open: run tbd login in Terminal on the Mac itself)", err)
	}
	return err
}

// pagePollInterval is how often watchPages re-counts the page targets without an event.
const pagePollInterval = 2 * time.Second

// watchPages returns a channel that is closed once the browser of ctx has no page target left (the
// user closed the last window or tab). It re-counts the browser's targets whenever a target is
// destroyed or crashes, and every pagePollInterval in case an event is missed; a failed count (the
// browser is going away) is ignored, since the process exit ends Login then.
func watchPages(ctx context.Context, log *slog.Logger) <-chan struct{} {
	gone := make(chan struct{})
	wake := make(chan struct{}, 1)
	if err := chromedp.ListenBrowser(ctx, func(ev any) {
		switch ev.(type) {
		case *target.EventTargetDestroyed, *target.EventTargetCrashed, *target.EventDetachedFromTarget:
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		log.Debug("cannot watch the browser's targets; waiting for the browser to exit", "err", err)
	}
	go func() {
		tick := time.NewTicker(pagePollInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-tick.C:
			}
			infos, err := chromedp.Targets(ctx)
			if err != nil {
				continue
			}
			if countPages(infos) == 0 {
				close(gone)
				return
			}
		}
	}()
	return gone
}

// countPages counts the page targets (tabs and windows; not workers, extensions or iframes).
func countPages(infos []*target.Info) int {
	n := 0
	for _, i := range infos {
		if i != nil && i.Type == "page" {
			n++
		}
	}
	return n
}

// loginState decides "signed in": the main frame has been on the Teams origin, and not on a login
// host, for the settle time without another main-frame navigation.
type loginState struct {
	settle       time.Duration
	teamsOrigins []string
	loginHosts   []string
	log          *slog.Logger
	onSignedIn   func()

	mu       sync.Mutex
	gen      int
	timer    *time.Timer
	in       bool
	notified bool
}

func (l *loginState) navigated(rawURL string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gen++
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	origin := originOf(rawURL)
	onTeams := false
	for _, o := range l.teamsOrigins {
		onTeams = onTeams || (origin != "" && origin == o)
	}
	onTeams = onTeams && !matchesAny(hostOf(rawURL), l.loginHosts)
	if !onTeams {
		if l.in {
			l.log.Info("not signed in (yet): the page left Teams for the sign-in page")
		}
		l.in = false
		return
	}
	gen := l.gen
	l.timer = time.AfterFunc(l.settle, func() {
		l.mu.Lock()
		if gen != l.gen || l.in {
			l.mu.Unlock()
			return
		}
		l.in = true
		first := !l.notified
		l.notified = true
		cb := l.onSignedIn
		l.mu.Unlock()
		l.log.Info("signed in; close the browser window to finish")
		if first && cb != nil {
			cb()
		}
	})
}

func (l *loginState) signedIn() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.in
}

func (l *loginState) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gen++
	if l.timer != nil {
		l.timer.Stop()
	}
}

func originOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func matchesAny(host string, hosts []string) bool {
	host = strings.ToLower(host)
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}
