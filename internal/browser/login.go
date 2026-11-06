package browser

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
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
	// UserAgent, if set, is passed as --user-agent, so the interactive sign-in presents the same
	// user agent as the daemon will (Conditional Access evaluates the device platform at sign-in).
	UserAgent string
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
// host), and then waits for the user to close the window (the browser process exits). It returns
// nil if the user was signed in by then, ErrNotSignedIn otherwise. If ctx is cancelled it closes the
// browser and returns ctx.Err().
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
		Path: o.Path, Home: o.Home, Headless: o.headless, UserAgent: o.UserAgent, ExtraArgs: o.ExtraArgs, Logger: log,
	})
	if err != nil {
		return err
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

	st := &loginState{settle: settle, teamsOrigin: originOf(o.TeamsURL), loginHosts: o.LoginHosts, log: log, onSignedIn: o.OnSignedIn}
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
		_, err := cdp.Call(ctx, t, page.Navigate, page.NavigateParams{URL: o.TeamsURL})
		return err
	})); err != nil {
		return err
	}
	log.Info("sign in to Teams in the browser window; close the window when Teams has loaded")

	if o.OnReady != nil {
		o.OnReady(proc.WSURL)
	}

	select {
	case <-proc.Done():
		if !st.signedIn() {
			return ErrNotSignedIn
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// loginState decides "signed in": the main frame has been on the Teams origin, and not on a login
// host, for the settle time without another main-frame navigation.
type loginState struct {
	settle      time.Duration
	teamsOrigin string
	loginHosts  []string
	log         *slog.Logger
	onSignedIn  func()

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
	onTeams := originOf(rawURL) == l.teamsOrigin && !matchesAny(hostOf(rawURL), l.loginHosts)
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
