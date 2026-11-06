package session

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/proto"
)

// Detector defaults.
const (
	DefaultBurstThreshold = 3
	DefaultBurstWindow    = 60 * time.Second
	// DefaultLoginGrace is how long the main frame may stay away from Teams after a navigation to
	// a login host before detector (1) fires. Teams bounces through login.microsoftonline.com on
	// a normal start (silent SSO with the persistent cookie) and comes back within seconds.
	DefaultLoginGrace = 30 * time.Second
	// LoginLoopLimit bounces through a login host within LoginLoopWindow fire detector (1) at
	// once, although the main frame came back to Teams each time: a Teams ⇄ login redirect loop
	// never stays on the login page long enough for the grace period. A normal start bounces
	// once or twice (teams.microsoft.com, then teams.cloud.microsoft).
	LoginLoopLimit  = 4
	LoginLoopWindow = 5 * time.Minute
)

// DetectorConfig configures a Detector. Zero values mean the documented defaults.
type DetectorConfig struct {
	// TeamsURL is config.toml's teams_url; its origin counts as "on Teams".
	TeamsURL string
	// TeamsOrigins are further origins that count as "on Teams" (Teams moves between
	// teams.microsoft.com and teams.cloud.microsoft). nil means config.DefaultPermissionOrigins;
	// an empty non-nil slice means only the TeamsURL origin.
	TeamsOrigins []string
	// LoginHosts: a main-frame navigation to one of these hosts (or a subdomain of one; "*.x"
	// works too) means the session expired. nil means config.DefaultLoginHosts.
	LoginHosts []string
	// APIHosts are host patterns whose API 401/403 bursts mean the session expired; "*.x"
	// matches x and its subdomains, a plain "x" only x. nil means config.DefaultAPIHosts.
	APIHosts []string
	// BurstThreshold failures within BurstWindow fire detector (2). Defaults 3 and 60 s.
	BurstThreshold int
	BurstWindow    time.Duration
	// LoginGrace: 0 means DefaultLoginGrace; negative fires detector (1) on the navigation itself.
	LoginGrace time.Duration
	// Alerts debounces what the Detector emits. nil means a private NewDebouncer(0, Now); share
	// one Debouncer with the FailureTracker. emit then receives only alerts that passed: do not
	// wrap emit with the same Debouncer (Wrap, Allow) as well, or the second check drops them.
	Alerts *Debouncer
	// Now and AfterFunc are the clock; nil means time.Now and time.AfterFunc.
	Now       func() time.Time
	AfterFunc func(d time.Duration, f func()) (stop func() bool)
	// Logger nil means slog.Default().
	Logger *slog.Logger
}

// Detector implements the four session-expiry detectors of BRIEF §5. Its methods are safe for
// concurrent use; emit is called outside its lock and should not block for long (queue the FCM
// send). It is called synchronously from the method that detected the expiry, except for
// detector (1)'s grace period, which ends on a timer goroutine (AfterFunc). Call Close on
// shutdown so that no timer emits after the alert queue is gone.
//
// Wiring (the daemon):
//
//	(1) Page.frameNavigated with frame.ParentID == ""  → MainFrameNavigated(frame.URL)
//	(2) Network.responseReceived                      → Response(resp.URL, int(resp.Status), string(ev.Type))
//	(3) every ProbeAction.Delay()                     → ProbeResult(Evaluate[ProbeResult](ProbeJS))
//	(4) browser process exit / CDP disconnect         → BrowserLost(err); restart via FailureTracker
//
// The first probe of a run can go ProbeRetryInterval after the initial navigation; after that
// the previous action's Delay() sets the pace.
//
// Detectors (1)–(3) emit proto.KindSessionExpired, debounced through Alerts. Detector (4) emits
// nothing itself: a single loss only restarts the browser; FailureTracker emits
// proto.KindBrowserDead after consecutive failed runs.
//
// Recovery: after an expiry Expired() stays true until a probe sees a healthy Teams page; the
// probe answers ProbeExpired meanwhile, which tells the caller to navigate to TeamsURL again
// every ProbeInterval ("keep retrying").
type Detector struct {
	emit      func(Alert)
	alerts    *Debouncer
	now       func() time.Time
	afterFunc func(time.Duration, func()) func() bool
	log       *slog.Logger

	teams     map[string]bool // origins that count as "on Teams"
	login     []hostPattern
	api       []hostPattern
	threshold int
	window    time.Duration
	grace     time.Duration

	mu           sync.Mutex
	closed       bool
	expired      bool
	loginPending bool
	loginHost    string
	loginGen     uint64
	loginStop    func() bool
	bounces      []time.Time // starts of recent login grace periods (redirect-loop guard)
	fails        []authFailure
	probeErrs    int
}

type authFailure struct {
	at     time.Time
	host   string
	status int
}

// NewDetector returns a Detector that emits alerts through cfg.Alerts to emit.
func NewDetector(cfg DetectorConfig, emit func(Alert)) *Detector {
	d := &Detector{
		emit:      emit,
		alerts:    cfg.Alerts,
		now:       orNow(cfg.Now),
		afterFunc: cfg.AfterFunc,
		log:       orDefault(cfg.Logger),
		teams:     map[string]bool{},
		threshold: cfg.BurstThreshold,
		window:    cfg.BurstWindow,
		grace:     cfg.LoginGrace,
	}
	if d.emit == nil {
		d.emit = func(Alert) {}
	}
	if d.alerts == nil {
		d.alerts = NewDebouncer(0, d.now)
	}
	if d.afterFunc == nil {
		d.afterFunc = func(dur time.Duration, f func()) func() bool { return time.AfterFunc(dur, f).Stop }
	}
	if d.threshold <= 0 {
		d.threshold = DefaultBurstThreshold
	}
	if d.window <= 0 {
		d.window = DefaultBurstWindow
	}
	if d.grace == 0 {
		d.grace = DefaultLoginGrace
	}
	origins := cfg.TeamsOrigins
	if origins == nil {
		origins = config.DefaultPermissionOrigins
	}
	for _, o := range append([]string{cfg.TeamsURL}, origins...) {
		if o := originOf(o); o != "" {
			d.teams[o] = true
		}
	}
	login := cfg.LoginHosts
	if login == nil {
		login = config.DefaultLoginHosts
	}
	d.login = compilePatterns(login, true)
	api := cfg.APIHosts
	if api == nil {
		api = config.DefaultAPIHosts
	}
	d.api = compilePatterns(api, false)
	return d
}

// Close stops the Detector for good: a pending login grace timer is cancelled and nothing is
// emitted afterwards. Call it when the daemon shuts down (or before dropping a Detector), so
// that a timer cannot raise an alert after daemon_stopping or after the send queue closed.
// The other methods keep working but emit nothing.
func (d *Detector) Close() {
	d.mu.Lock()
	d.closed = true
	d.cancelLoginLocked()
	d.mu.Unlock()
}

// Expired reports whether a session expiry was detected and no healthy probe has been seen since.
func (d *Detector) Expired() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.expired
}

// MainFrameNavigated is detector (1). Call it for every Page.frameNavigated of the main frame
// (not for iframes: Teams renews tokens in hidden login iframes all the time).
//
// A navigation to a login host starts a grace period (LoginGrace); if the main frame has not
// navigated back to a Teams origin when it ends, the session counts as expired. Intermediate
// hops (a federated IdP such as ADFS) do not end the grace period, so a sign-in page of the
// company's own IdP is caught as well. LoginLoopLimit grace periods started within
// LoginLoopWindow (a Teams ⇄ login redirect loop) count as expired at once.
func (d *Detector) MainFrameNavigated(rawURL string) {
	host := hostOf(rawURL)
	onTeams := d.teams[originOf(rawURL)]
	var a *Alert
	d.mu.Lock()
	switch {
	case matchHost(host, d.login):
		if d.closed {
			break // no timers after Close
		}
		if d.loginPending {
			break // keep the first deadline: a redirect loop between login pages must still fire
		}
		if d.grace < 0 {
			a = d.expireLocked(fmt.Sprintf("main frame navigated to %s", safeHost(host)))
			break
		}
		now := wall(d.now())
		cut := now.Add(-LoginLoopWindow)
		kept := d.bounces[:0]
		for _, b := range d.bounces {
			if !b.Before(cut) && !b.After(now) {
				kept = append(kept, b)
			}
		}
		d.bounces = append(kept, now)
		if n := len(d.bounces); n >= LoginLoopLimit {
			d.bounces = d.bounces[:0]
			a = d.expireLocked(fmt.Sprintf("main frame went to %s %d times within %s (a sign-in redirect loop)",
				safeHost(host), n, LoginLoopWindow))
			break
		}
		d.loginPending, d.loginHost = true, host
		d.loginGen++
		gen := d.loginGen
		d.loginStop = d.afterFunc(d.grace, func() { d.loginTimeout(gen) })
		d.log.Debug("session: main frame on a login host; waiting for a return to Teams",
			"host", host, "grace", d.grace)
	case onTeams:
		if d.loginPending {
			d.log.Debug("session: main frame returned to Teams within the grace period", "from", d.loginHost)
		}
		d.cancelLoginLocked()
	}
	d.mu.Unlock()
	d.deliver(a)
}

func (d *Detector) loginTimeout(gen uint64) {
	d.mu.Lock()
	if !d.loginPending || gen != d.loginGen {
		d.mu.Unlock()
		return
	}
	host := d.loginHost
	d.loginPending, d.loginStop = false, nil
	a := d.expireLocked(fmt.Sprintf("main frame navigated to %s and did not return to Teams within %s",
		safeHost(host), d.grace))
	d.mu.Unlock()
	d.deliver(a)
}

func (d *Detector) cancelLoginLocked() {
	if d.loginStop != nil {
		d.loginStop()
	}
	d.loginGen++
	d.loginPending, d.loginHost, d.loginStop = false, "", nil
}

// Response is detector (2). Call it for every Network.responseReceived (it filters cheaply):
// rawURL and status of the response and the CDP resource type ("XHR", "Fetch", ...).
//
// Counted: 401 and 403 from a host matching APIHosts, for API requests only: resource type XHR,
// Fetch or EventSource, or an unknown type ("", "Other") whose path contains "/api/". Documents,
// scripts, images, styles, fonts, media, preflights and the rest never count. BurstThreshold
// such failures within BurstWindow (a sliding window; a failure exactly BurstWindow old still
// counts) fire session_expired and clear the window. Failures less than BurstCoalesce apart count
// once, so a burst of parallel requests that all hit an expiring token (followed by a silent
// refresh) is not an expiry. A 2xx in between does not reset the count: anonymous endpoints
// keep answering 200 when the session is dead.
func (d *Detector) Response(rawURL string, status int, resourceType string) {
	if status != 401 && status != 403 {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	host := normHost(u.Hostname())
	if !matchHost(host, d.api) || !apiRequest(resourceType, u.Path) {
		return
	}
	var a *Alert
	now := wall(d.now())
	d.mu.Lock()
	cut := now.Add(-d.window)
	kept := d.fails[:0]
	for _, f := range d.fails {
		if !f.at.Before(cut) && !f.at.After(now) {
			kept = append(kept, f)
		}
	}
	d.fails = kept
	// Parallel requests that hit an expiring access token together fail within milliseconds of
	// each other and are followed by a silent token refresh; they count as one failure.
	if n := len(d.fails); n > 0 && now.Sub(d.fails[n-1].at) < BurstCoalesce {
		d.log.Debug("session: API auth failure (same moment)", "status", status, "host", host, "count", n)
		d.mu.Unlock()
		return
	}
	d.fails = append(d.fails, authFailure{at: now, host: host, status: status})
	d.log.Debug("session: API auth failure", "status", status, "host", host, "count", len(d.fails))
	if len(d.fails) >= d.threshold {
		n := len(d.fails)
		d.fails = d.fails[:0]
		a = d.expireLocked(fmt.Sprintf("%d HTTP 401 or 403 responses from Teams API hosts within %s (last: %d from %s)",
			n, d.window, status, safeHost(host)))
	}
	d.mu.Unlock()
	d.deliver(a)
}

// BurstCoalesce: 401/403s closer together than this count as one failure for detector (2).
const BurstCoalesce = time.Second

// apiRequest reports whether a response of this CDP resource type and URL path is an API call.
func apiRequest(resourceType, path string) bool {
	switch strings.ToLower(resourceType) {
	case "xhr", "fetch", "eventsource":
		return true
	case "", "other":
		return strings.Contains(strings.ToLower(path), "/api/")
	}
	return false
}

// ProbeResult is detector (3). Call it with the outcome of evaluating ProbeJS in the main frame
// (r is ignored when err is not nil) and follow the returned action.
//
//   - main frame on a Teams origin and no sign-in prompt → ProbeOK (and Expired() turns false);
//   - a sign-in prompt visible (r.Signin), or the main frame on a login host or on any other
//     http(s) page outside Teams → session_expired, ProbeExpired; while detector (1)'s grace
//     period is running the probe defers to it (ProbeRetry), so a probe that lands in the middle
//     of a silent-SSO bounce does not fire;
//   - about:blank, chrome-error:// and other non-http(s) pages → ProbeOffPage (navigate to
//     TeamsURL again; no alert: a network outage looks like this and is not an expiry);
//   - an evaluation error or an empty href → ProbeRetry; ProbeFailLimit of them in a row →
//     ProbeRestart (the page or CDP is wedged: restart the browser and report
//     ErrUnresponsive to the FailureTracker).
func (d *Detector) ProbeResult(r ProbeResult, err error) ProbeAction {
	var a *Alert
	d.mu.Lock()
	act := d.probeLocked(r, err, &a)
	d.mu.Unlock()
	d.deliver(a)
	return act
}

func (d *Detector) probeLocked(r ProbeResult, err error, a **Alert) ProbeAction {
	if err != nil || r.Href == "" {
		d.probeErrs++
		if d.probeErrs >= ProbeFailLimit {
			d.log.Warn("session: page probe failed repeatedly; the browser needs a restart",
				"failures", d.probeErrs, "err", err)
			d.probeErrs = 0
			return ProbeRestart
		}
		d.log.Info("session: page probe failed", "failures", d.probeErrs, "err", err)
		return ProbeRetry
	}
	d.probeErrs = 0
	u, perr := url.Parse(r.Href)
	if perr != nil || origin(u) == "" {
		d.log.Info("session: main frame is not on a web page; navigating to Teams again", "scheme", schemeOf(u))
		return ProbeOffPage
	}
	host := normHost(u.Hostname())
	onTeams := d.teams[origin(u)]
	var reason string
	switch {
	case r.Signin:
		reason = fmt.Sprintf("probe: sign-in prompt (%s) visible on %s", safeWhy(r.Why), safeHost(host))
	case matchHost(host, d.login):
		reason = fmt.Sprintf("probe: main frame is on %s", safeHost(host))
	case !onTeams:
		reason = fmt.Sprintf("probe: main frame is on %s, outside Teams", safeHost(host))
	default:
		if d.expired {
			d.expired = false
			d.log.Info("session: Teams page is healthy again")
		}
		d.cancelLoginLocked() // the main frame is back on Teams even if we missed the navigation
		d.log.Debug("session: probe ok", "host", host)
		return ProbeOK
	}
	if onTeams {
		d.cancelLoginLocked() // a sign-in prompt on Teams itself: this probe decides
	} else if d.loginPending {
		d.log.Debug("session: probe off Teams during the login grace period", "host", host)
		return ProbeRetry
	}
	*a = d.expireLocked(reason)
	return ProbeExpired
}

func schemeOf(u *url.URL) string {
	if u == nil {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToLower(u.Scheme) {
		if b.Len() < 32 && (r == '-' || r == '+' || r == '.' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// probeWhy are the heuristic codes ProbeJS returns.
var probeWhy = map[string]bool{
	"login-form": true, "account-picker": true, "login-iframe": true, "signin-button": true,
	"credential-input": true, "signed-out-text": true,
}

// safeWhy passes a known heuristic code through; anything else came from somewhere else.
func safeWhy(s string) string {
	if probeWhy[s] {
		return s
	}
	return "unspecified"
}

// BrowserLost is detector (4): the browser process exited or the CDP connection broke. It never
// emits an alert itself — a single loss is handled by restarting the browser, and the restart
// loop's FailureTracker emits browser_dead after consecutive failed runs. It resets the per-run
// state (login grace timer, redirect-loop count, 401/403 window, probe failure count);
// Expired() is kept, since a new browser on the same profile has the same session.
func (d *Detector) BrowserLost(err error) {
	d.mu.Lock()
	d.cancelLoginLocked()
	d.bounces = d.bounces[:0]
	d.fails = d.fails[:0]
	d.probeErrs = 0
	d.mu.Unlock()
	d.log.Info("session: browser lost; the restart loop takes over", "err", err)
}

// expireLocked marks the session expired and returns the alert to deliver after unlocking.
func (d *Detector) expireLocked(reason string) *Alert {
	if d.expired {
		d.log.Debug("session: expiry detected again", "reason", reason)
	} else {
		d.log.Warn("session: Teams session expired", "reason", reason)
	}
	d.expired = true
	if d.closed {
		return nil
	}
	return &Alert{Kind: proto.KindSessionExpired, Reason: reason, At: d.now()}
}

func (d *Detector) deliver(a *Alert) {
	if a == nil {
		return
	}
	if !d.alerts.Allow(a.Kind) {
		d.log.Debug("session: alert suppressed (debounced)", "kind", a.Kind)
		return
	}
	d.log.Info("session: alert", "kind", a.Kind, "reason", a.Reason)
	d.emit(*a)
}
