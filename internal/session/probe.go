package session

import (
	_ "embed" // ProbeJS
	"time"
)

// Probe schedule (BRIEF §5: every 15 min).
const (
	// ProbeInterval is the normal period of the page probe.
	ProbeInterval = 15 * time.Minute
	// ProbeRetryInterval is the delay after a failed or off-page probe.
	ProbeRetryInterval = time.Minute
	// ProbeFailLimit consecutive probe errors make the Detector answer ProbeRestart.
	ProbeFailLimit = 3
)

// ProbeJS is the expression for Runtime.evaluate in the main frame (returnByValue; silent is
// recommended). Evaluating it in an isolated world of the main frame (Page.createIsolatedWorld)
// keeps page scripts from tampering with the built-ins it uses (String, Set, querySelectorAll);
// it works in the page's own world too. It has no side effects and evaluates to a ProbeResult:
//
//	{href: location.href, signin: <sign-in prompt visible>, why: "<heuristic code>"}
//
// Only visible elements that intersect the viewport and are not inside aria-hidden/inert
// subtrees count. The heuristics, in order (why = the first that matched):
//
//   - "login-form": Entra ID (AAD) sign-in fields anywhere: input[name=loginfmt], input[name=passwd],
//     #i0116, #i0118.
//   - "account-picker": the Entra ID account picker (#tilesHolder).
//   - "login-iframe": an iframe of at least 200×200 px whose src is on a Microsoft login host
//     (hidden token-renewal iframes are 0×0 and never count).
//   - "signin-button": a button/link whose whole label is a sign-in label ("Sign in",
//     "Sign in again", "Use another account", Russian equivalents …) inside a modal dialog, an
//     alert/status region, or anywhere on a sparse page (≤ 20 rendered interactive elements —
//     buttons, links, inputs, tabs, menu/tree/list items, editable areas: a login or landing
//     page, not the Teams app shell). A bot card with a "Sign in" button in an open chat is
//     therefore not a sign-in prompt. Sparseness counts rendered elements even inside
//     aria-hidden subtrees: a modal dialog makes Fluent UI mark the app shell behind it
//     aria-hidden, and the shell's chat list (which quotes message text) must not turn into
//     "the text of a sparse page".
//   - "credential-input": a visible email/password input inside a modal dialog or on a sparse page.
//   - "signed-out-text": a phrase such as "You're signed out", "Your session has expired",
//     "Sign in again" in the visible text of a modal dialog or of a sparse page (not in toasts,
//     which may quote message text).
//
// Labels and phrases are English and Russian, compared case-insensitively with ’ folded to '
// and ё to е. On a page outside Teams the Detector fires on the URL anyway, so the heuristics
// matter for Teams' own signed-out UI on the Teams origin, and there they lean to "not signed
// out": a missed prompt is caught later by a redirect or the 401/403 detector, a false one would
// page the phone.
//
//go:embed probe.js
var ProbeJS string

// ProbeResult is what ProbeJS evaluates to.
type ProbeResult struct {
	Href   string `json:"href"`
	Signin bool   `json:"signin"`
	Why    string `json:"why,omitempty"`
}

// ProbeAction tells the caller what to do after Detector.ProbeResult.
type ProbeAction int

const (
	// ProbeOK: Teams is healthy. Probe again after ProbeInterval.
	ProbeOK ProbeAction = iota
	// ProbeRetry: the probe was inconclusive (evaluation error, or detector (1)'s grace period
	// is running). Probe again after ProbeRetryInterval.
	ProbeRetry
	// ProbeOffPage: the main frame is not on a web page (about:blank, chrome-error://).
	// Navigate to TeamsURL and probe again after ProbeRetryInterval. No alert.
	ProbeOffPage
	// ProbeExpired: the session expired (alert emitted, debounced). Navigate to TeamsURL again
	// ("keep retrying") and probe after ProbeInterval.
	ProbeExpired
	// ProbeRestart: ProbeFailLimit probes in a row failed. Restart the browser and pass
	// ErrUnresponsive (or an error wrapping it) to FailureTracker.Stopped.
	ProbeRestart
)

// Renavigate reports whether the caller should navigate the main frame to TeamsURL.
func (a ProbeAction) Renavigate() bool { return a == ProbeOffPage || a == ProbeExpired }

// Delay is when to run the next probe (0 for ProbeRestart: the new run schedules its own).
func (a ProbeAction) Delay() time.Duration {
	switch a {
	case ProbeOK, ProbeExpired:
		return ProbeInterval
	case ProbeRetry, ProbeOffPage:
		return ProbeRetryInterval
	}
	return 0
}

func (a ProbeAction) String() string {
	switch a {
	case ProbeOK:
		return "ok"
	case ProbeRetry:
		return "retry"
	case ProbeOffPage:
		return "off-page"
	case ProbeExpired:
		return "expired"
	case ProbeRestart:
		return "restart"
	}
	return "unknown"
}
