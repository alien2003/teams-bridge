package session

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/proto"
)

var quiet = slog.New(slog.DiscardHandler)

type recorder struct {
	mu     sync.Mutex
	alerts []Alert
}

func (r *recorder) emit(a Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}

func (r *recorder) all() []Alert {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Alert(nil), r.alerts...)
}

func (r *recorder) count(kind string) int {
	n := 0
	for _, a := range r.all() {
		if a.Kind == kind {
			n++
		}
	}
	return n
}

const teamsURL = "https://teams.microsoft.com/v2/"

// newDetector builds a Detector on a fake clock with debouncing disabled (debouncing has its own
// tests), after applying mod to the config.
func newDetector(t *testing.T, mod func(*DetectorConfig)) (*Detector, *fakeClock, *recorder) {
	t.Helper()
	clk := newFakeClock()
	rec := &recorder{}
	cfg := DetectorConfig{
		TeamsURL:  teamsURL,
		Alerts:    NewDebouncer(-1, clk.Now),
		Now:       clk.Now,
		AfterFunc: clk.AfterFunc,
		Logger:    quiet,
	}
	if mod != nil {
		mod(&cfg)
	}
	return NewDetector(cfg, rec.emit), clk, rec
}

type navStep struct {
	nav     string
	advance time.Duration
}

func nav(u string) navStep         { return navStep{nav: u} }
func wait(d time.Duration) navStep { return navStep{advance: d} }
func checkAlerts(t *testing.T, rec *recorder, want int) {
	t.Helper()
	got := rec.all()
	if len(got) != want {
		t.Fatalf("got %d alerts %+v, want %d", len(got), got, want)
	}
	for _, a := range got {
		if a.Kind != proto.KindSessionExpired {
			t.Errorf("alert kind %q, want %q", a.Kind, proto.KindSessionExpired)
		}
		if a.Reason == "" || a.At.IsZero() {
			t.Errorf("alert %+v lacks reason or time", a)
		}
	}
}

func TestMainFrameNavigated(t *testing.T) {
	const login = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize?client_id=x&state=y"
	cases := []struct {
		name  string
		mod   func(*DetectorConfig)
		steps []navStep
		want  int
	}{
		{"login host and stays", nil, []navStep{nav(login), wait(31 * time.Second)}, 1},
		{"stays exactly the grace period", nil, []navStep{nav(login), wait(DefaultLoginGrace)}, 1},
		{"grace not over yet", nil, []navStep{nav(login), wait(29 * time.Second)}, 0},
		{"silent SSO bounce back to Teams", nil, []navStep{nav(login), wait(3 * time.Second), nav(teamsURL), wait(time.Hour)}, 0},
		{"bounce back to teams.cloud.microsoft", nil, []navStep{nav(login), wait(time.Second), nav("https://teams.cloud.microsoft/v2/"), wait(time.Hour)}, 0},
		{"login then a federated IdP page", nil, []navStep{nav(login), wait(2 * time.Second), nav("https://sts.contoso.example/adfs/ls/"), wait(40 * time.Second)}, 1},
		{"redirect loop keeps the first deadline", nil, []navStep{nav(login), wait(20 * time.Second), nav("https://login.live.com/oauth20"), wait(11 * time.Second)}, 1},
		{"login.live.com", nil, []navStep{nav("https://login.live.com/login.srf"), wait(time.Minute)}, 1},
		{"login.microsoft.com", nil, []navStep{nav("https://login.microsoft.com/x"), wait(time.Minute)}, 1},
		{"case and trailing dot", nil, []navStep{nav("https://LOGIN.MicrosoftOnline.COM./common"), wait(time.Minute)}, 1},
		{"explicit port", nil, []navStep{nav("https://login.microsoftonline.com:443/common"), wait(time.Minute)}, 1},
		{"subdomain of a login host", nil, []navStep{nav("https://device.login.microsoftonline.com/"), wait(time.Minute)}, 1},
		{"lookalike suffix", nil, []navStep{nav("https://login.microsoftonline.com.evil.example/"), wait(time.Minute)}, 0},
		{"no dot boundary", nil, []navStep{nav("https://xlogin.live.com/"), wait(time.Minute)}, 0},
		{"login host only in the query", nil, []navStep{nav("https://www.bing.com/?q=login.live.com"), wait(time.Minute)}, 0},
		{"other host", nil, []navStep{nav("https://www.bing.com/"), wait(time.Minute)}, 0},
		{"teams", nil, []navStep{nav(teamsURL), wait(time.Minute)}, 0},
		{"about:blank", nil, []navStep{nav("about:blank"), wait(time.Minute)}, 0},
		{"chrome error page", nil, []navStep{nav("chrome-error://chromewebdata/"), wait(time.Minute)}, 0},
		{"garbage", nil, []navStep{nav("::not a url::"), nav(""), wait(time.Minute)}, 0},
		{"two incidents", nil, []navStep{nav(login), wait(time.Minute), nav(teamsURL), nav(login), wait(time.Minute)}, 2},
		{"immediate (negative grace)", func(c *DetectorConfig) { c.LoginGrace = -1 }, []navStep{nav(login)}, 1},
		{"immediate, other host", func(c *DetectorConfig) { c.LoginGrace = -1 }, []navStep{nav("https://www.bing.com/")}, 0},
		{"custom grace", func(c *DetectorConfig) { c.LoginGrace = 5 * time.Second }, []navStep{nav(login), wait(5 * time.Second)}, 1},
		{"custom login host (E2E fake)", func(c *DetectorConfig) {
			c.TeamsURL, c.LoginHosts = "http://127.0.0.1:8080/", []string{"localhost"}
		}, []navStep{nav("http://localhost:8080/login"), wait(time.Minute)}, 1},
		{"custom login hosts replace the defaults", func(c *DetectorConfig) { c.LoginHosts = []string{"login.fake.test"} },
			[]navStep{nav(login), wait(time.Minute)}, 0},
		{"wildcard login pattern", func(c *DetectorConfig) { c.LoginHosts = []string{"*.idp.example"} },
			[]navStep{nav("https://a.idp.example/"), wait(time.Minute)}, 1},
		{"fake teams port must match to return", func(c *DetectorConfig) {
			c.TeamsURL, c.LoginHosts = "http://127.0.0.1:8080/", []string{"localhost"}
		}, []navStep{nav("http://localhost:8080/login"), nav("http://127.0.0.1:9999/"), wait(time.Minute)}, 1},
		{"Teams ⇄ login redirect loop fires at once", nil, []navStep{
			nav(login), wait(2 * time.Second), nav(teamsURL), wait(3 * time.Second),
			nav(login), wait(2 * time.Second), nav(teamsURL), wait(3 * time.Second),
			nav(login), wait(2 * time.Second), nav(teamsURL), wait(3 * time.Second),
			nav(login), // 4th bounce within 15 s: fires without waiting for the grace period
		}, 1},
		{"redirect loop fires once, then counts afresh", nil, []navStep{
			nav(login), nav(teamsURL), nav(login), nav(teamsURL), nav(login), nav(teamsURL), nav(login), nav(teamsURL),
			nav(login), nav(teamsURL), nav(login), nav(teamsURL), nav(login), nav(teamsURL), wait(time.Hour),
		}, 1},
		{"three bounces are a normal start", nil, []navStep{
			nav(login), wait(2 * time.Second), nav(teamsURL), nav("https://login.microsoftonline.com/common/reprocess"), nav("https://teams.cloud.microsoft/v2/"),
			nav(login), nav(teamsURL), wait(time.Hour),
		}, 0},
		{"login hops within one bounce are one bounce", nil, []navStep{
			nav(login), nav("https://login.microsoftonline.com/kmsi"), nav("https://login.microsoftonline.com/common/reprocess"),
			nav("https://login.live.com/"), wait(5 * time.Second), nav(teamsURL), wait(time.Hour),
		}, 0},
		{"bounces spread over more than the loop window", nil, []navStep{
			nav(login), nav(teamsURL), wait(2 * time.Minute), nav(login), nav(teamsURL), wait(2 * time.Minute),
			nav(login), nav(teamsURL), wait(2*time.Minute + time.Second), nav(login), nav(teamsURL), wait(time.Hour),
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, clk, rec := newDetector(t, tc.mod)
			for _, s := range tc.steps {
				if s.advance > 0 {
					clk.Advance(s.advance)
				} else {
					d.MainFrameNavigated(s.nav)
				}
			}
			checkAlerts(t, rec, tc.want)
			if got := d.Expired(); got != (tc.want > 0) {
				t.Errorf("Expired() = %v", got)
			}
		})
	}
}

func TestMainFrameNavigatedStopsTimer(t *testing.T) {
	d, clk, _ := newDetector(t, nil)
	d.MainFrameNavigated("https://login.microsoftonline.com/")
	if clk.pending() != 1 {
		t.Fatalf("pending timers = %d, want 1", clk.pending())
	}
	d.MainFrameNavigated(teamsURL)
	if clk.pending() != 0 {
		t.Fatalf("timer not stopped on return to Teams")
	}
}

func TestMainFrameNavigatedRealTimer(t *testing.T) {
	rec := &recorder{}
	d := NewDetector(DetectorConfig{TeamsURL: teamsURL, LoginGrace: 20 * time.Millisecond, Logger: quiet}, rec.emit)
	d.MainFrameNavigated("https://login.microsoftonline.com/")
	deadline := time.Now().Add(5 * time.Second)
	for rec.count(proto.KindSessionExpired) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.count(proto.KindSessionExpired) != 1 {
		t.Fatal("real AfterFunc did not fire")
	}
}

type resp struct {
	at     time.Duration // since the start of the case
	url    string
	status int
	typ    string
}

func TestResponseBursts(t *testing.T) {
	const (
		csa  = "https://teams.microsoft.com/api/csa/zz/api/v1/teams/users/me"
		chat = "https://zz.ng.msg.teams.microsoft.com/v1/users/ME/conversations?view=x"
		pres = "https://presence.teams.microsoft.com/v1/me/presence"
	)
	burst := func(u string, status int, typ string, n int, every time.Duration) []resp {
		var out []resp
		for i := range n {
			out = append(out, resp{time.Duration(i) * every, u, status, typ})
		}
		return out
	}
	cases := []struct {
		name string
		mod  func(*DetectorConfig)
		rs   []resp
		want int
	}{
		{"three 401 XHR", nil, burst(csa, 401, "XHR", 3, time.Second), 1},
		{"mixed 401 and 403, fetch, two hosts", nil, []resp{{0, chat, 401, "Fetch"}, {time.Second, pres, 403, "Fetch"}, {2 * time.Second, chat, 401, "Fetch"}}, 1},
		{"two only", nil, burst(csa, 401, "XHR", 2, time.Second), 0},
		{"200s", nil, burst(csa, 200, "XHR", 10, time.Second), 0},
		{"500s", nil, burst(csa, 500, "XHR", 10, time.Second), 0},
		{"404s", nil, burst(csa, 404, "XHR", 10, time.Second), 0},
		{"spread beyond the window", nil, burst(csa, 401, "XHR", 3, 40*time.Second), 0},
		{"exactly the window", nil, burst(csa, 401, "XHR", 3, 30*time.Second), 1},
		{"window slides", nil, []resp{{0, csa, 401, "XHR"}, {50 * time.Second, csa, 401, "XHR"}, {70 * time.Second, csa, 401, "XHR"}, {100 * time.Second, csa, 401, "XHR"}}, 1},
		{"a slow trickle never fires", nil, burst(csa, 401, "XHR", 20, 31*time.Second), 0},
		{"reset after firing", nil, burst(csa, 401, "XHR", 5, time.Second), 1},
		{"twice the threshold fires twice", nil, burst(csa, 401, "XHR", 6, time.Second), 2},
		{"subdomain via *.teams.microsoft.com", nil, burst(chat, 401, "XHR", 3, time.Second), 1},
		{"apex teams.cloud.microsoft", nil, burst("https://teams.cloud.microsoft/api/mt/part/zz-01/beta/users/x", 403, "Fetch", 3, time.Second), 1},
		{"login host 401s", nil, burst("https://login.microsoftonline.com/common/oauth2/v2.0/token", 401, "XHR", 5, time.Second), 0},
		{"graph 401s", nil, burst("https://graph.microsoft.com/v1.0/me", 401, "Fetch", 5, time.Second), 0},
		{"lookalike host", nil, burst("https://teams.microsoft.com.evil.example/api/x", 401, "XHR", 5, time.Second), 0},
		{"images", nil, burst("https://teams.microsoft.com/api/mt/profilepicture", 401, "Image", 5, time.Second), 0},
		{"documents", nil, burst("https://teams.microsoft.com/v2/", 401, "Document", 5, time.Second), 0},
		{"scripts", nil, burst("https://statics.teams.cdn.office.net/x.js", 403, "Script", 5, time.Second), 0},
		{"scripts on an API host", nil, burst("https://teams.microsoft.com/api/x.js", 403, "Script", 5, time.Second), 0},
		{"stylesheets and fonts", nil, append(burst(csa, 401, "Stylesheet", 3, time.Second), burst(csa, 401, "Font", 3, time.Second)...), 0},
		{"preflights", nil, burst(csa, 403, "Preflight", 5, time.Second), 0},
		{"event source", nil, burst(csa, 401, "EventSource", 3, time.Second), 1},
		{"lowercase type", nil, burst(csa, 401, "xhr", 3, time.Second), 1},
		{"Other with /api/", nil, burst(csa, 401, "Other", 3, time.Second), 1},
		{"unknown type with /api/", nil, burst(csa, 401, "", 3, time.Second), 1},
		{"Other without /api/", nil, burst(chat, 401, "Other", 5, time.Second), 0},
		{"2xx in between does not clear (BRIEF §5: three within 60 s)", nil, []resp{{0, csa, 401, "XHR"}, {time.Second, csa, 401, "XHR"}, {2 * time.Second, csa, 200, "XHR"}, {3 * time.Second, csa, 401, "XHR"}}, 1},
		{"a parallel burst counts once", nil, []resp{{0, csa, 401, "XHR"}, {10 * time.Millisecond, chat, 401, "XHR"}, {20 * time.Millisecond, pres, 401, "Fetch"}, {900 * time.Millisecond, csa, 401, "XHR"}}, 0},
		{"three separate moments fire", nil, []resp{{0, csa, 401, "XHR"}, {10 * time.Millisecond, chat, 401, "XHR"}, {1500 * time.Millisecond, csa, 401, "XHR"}, {3 * time.Second, pres, 403, "Fetch"}}, 1},
		{"custom threshold", func(c *DetectorConfig) { c.BurstThreshold = 5 }, burst(csa, 401, "XHR", 4, time.Second), 0},
		{"custom threshold reached", func(c *DetectorConfig) { c.BurstThreshold = 5 }, burst(csa, 401, "XHR", 5, time.Second), 1},
		{"custom window", func(c *DetectorConfig) { c.BurstWindow = 10 * time.Second }, burst(csa, 401, "XHR", 3, 6*time.Second), 0},
		{"custom API host (E2E fake)", func(c *DetectorConfig) { c.APIHosts = []string{"127.0.0.1"} },
			burst("http://127.0.0.1:8080/api/unauthorized", 401, "Fetch", 3, time.Second), 1},
		{"plain API pattern is exact", func(c *DetectorConfig) { c.APIHosts = []string{"teams.microsoft.com"} },
			burst(chat, 401, "XHR", 3, time.Second), 0},
		{"garbage URLs", nil, []resp{{0, "::", 401, "XHR"}, {0, "", 401, "XHR"}, {0, "%zz", 401, "XHR"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, clk, rec := newDetector(t, tc.mod)
			var at time.Duration
			for _, r := range tc.rs {
				clk.Advance(r.at - at)
				at = r.at
				d.Response(r.url, r.status, r.typ)
			}
			checkAlerts(t, rec, tc.want)
			for _, a := range rec.all() {
				if strings.Contains(a.Reason, "/") {
					t.Errorf("reason %q contains a URL path", a.Reason)
				}
			}
		})
	}
}

func TestProbeResult(t *testing.T) {
	type probe struct {
		r   ProbeResult
		err error
	}
	ok := func(href string) probe { return probe{r: ProbeResult{Href: href}} }
	signin := func(href, why string) probe { return probe{r: ProbeResult{Href: href, Signin: true, Why: why}} }
	fail := probe{err: errors.New("cannot find context with specified id")}
	cases := []struct {
		name    string
		mod     func(*DetectorConfig)
		probes  []probe
		want    []ProbeAction
		alerts  int
		expired bool
	}{
		{"healthy Teams", nil, []probe{ok("https://teams.microsoft.com/v2/")}, []ProbeAction{ProbeOK}, 0, false},
		{"healthy, other path and hash", nil, []probe{ok("https://teams.microsoft.com/_#/conversations/19:x@thread.v2?ctx=chat")}, []ProbeAction{ProbeOK}, 0, false},
		{"healthy, teams.cloud.microsoft", nil, []probe{ok("https://teams.cloud.microsoft/v2/")}, []ProbeAction{ProbeOK}, 0, false},
		{"healthy, uppercase host", nil, []probe{ok("https://Teams.Microsoft.com:443/v2/")}, []ProbeAction{ProbeOK}, 0, false},
		{"login page", nil, []probe{ok("https://login.microsoftonline.com/common/oauth2/authorize")}, []ProbeAction{ProbeExpired}, 1, true},
		{"sign-in prompt on Teams", nil, []probe{signin("https://teams.microsoft.com/v2/", "signin-button")}, []ProbeAction{ProbeExpired}, 1, true},
		{"sign-in prompt elsewhere", nil, []probe{signin("https://login.live.com/", "login-form")}, []ProbeAction{ProbeExpired}, 1, true},
		{"outside Teams", nil, []probe{ok("https://www.microsoft.com/en-us/microsoft-teams/log-in")}, []ProbeAction{ProbeExpired}, 1, true},
		{"federated IdP", nil, []probe{ok("https://sts.contoso.example/adfs/ls/")}, []ProbeAction{ProbeExpired}, 1, true},
		{"plain http Teams is not Teams", nil, []probe{ok("http://teams.microsoft.com/v2/")}, []ProbeAction{ProbeExpired}, 1, true},
		{"lookalike host", nil, []probe{ok("https://teams.microsoft.com.evil.example/v2/")}, []ProbeAction{ProbeExpired}, 1, true},
		{"about:blank", nil, []probe{ok("about:blank")}, []ProbeAction{ProbeOffPage}, 0, false},
		{"chrome error page", nil, []probe{ok("chrome-error://chromewebdata/")}, []ProbeAction{ProbeOffPage}, 0, false},
		{"data URL", nil, []probe{ok("data:text/html,hi")}, []ProbeAction{ProbeOffPage}, 0, false},
		{"unparsable href", nil, []probe{ok("http://[::1")}, []ProbeAction{ProbeOffPage}, 0, false},
		{"one error", nil, []probe{fail}, []ProbeAction{ProbeRetry}, 0, false},
		{"empty href", nil, []probe{{}}, []ProbeAction{ProbeRetry}, 0, false},
		{"three errors restart", nil, []probe{fail, fail, fail, fail}, []ProbeAction{ProbeRetry, ProbeRetry, ProbeRestart, ProbeRetry}, 0, false},
		{"a good probe resets the error count", nil, []probe{fail, fail, ok(teamsURL), fail, fail}, []ProbeAction{ProbeRetry, ProbeRetry, ProbeOK, ProbeRetry, ProbeRetry}, 0, false},
		{"off-page resets the error count", nil, []probe{fail, fail, ok("about:blank"), fail}, []ProbeAction{ProbeRetry, ProbeRetry, ProbeOffPage, ProbeRetry}, 0, false},
		{"errors never alert", nil, []probe{fail, fail, fail, fail, fail, fail}, []ProbeAction{ProbeRetry, ProbeRetry, ProbeRestart, ProbeRetry, ProbeRetry, ProbeRestart}, 0, false},
		{"recovery", nil, []probe{ok("https://login.live.com/"), ok(teamsURL)}, []ProbeAction{ProbeExpired, ProbeOK}, 1, false},
		{"recovery needs a clean page", nil, []probe{ok("https://login.live.com/"), signin(teamsURL, "signed-out-text")}, []ProbeAction{ProbeExpired, ProbeExpired}, 2, true},
		{"only TeamsURL origin", func(c *DetectorConfig) { c.TeamsOrigins = []string{} },
			[]probe{ok("https://teams.cloud.microsoft/v2/")}, []ProbeAction{ProbeExpired}, 1, true},
		{"fake Teams (E2E)", func(c *DetectorConfig) { c.TeamsURL = "http://127.0.0.1:8080/" },
			[]probe{ok("http://127.0.0.1:8080/app"), ok("http://127.0.0.1:9090/")}, []ProbeAction{ProbeOK, ProbeExpired}, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _, rec := newDetector(t, tc.mod)
			for i, p := range tc.probes {
				if got := d.ProbeResult(p.r, p.err); got != tc.want[i] {
					t.Fatalf("probe %d: action %v, want %v", i, got, tc.want[i])
				}
			}
			checkAlerts(t, rec, tc.alerts)
			if d.Expired() != tc.expired {
				t.Errorf("Expired() = %v, want %v", d.Expired(), tc.expired)
			}
		})
	}
}

func TestProbeDefersToLoginGrace(t *testing.T) {
	d, clk, rec := newDetector(t, nil)
	d.MainFrameNavigated("https://login.microsoftonline.com/")
	if got := d.ProbeResult(ProbeResult{Href: "https://login.microsoftonline.com/"}, nil); got != ProbeRetry {
		t.Fatalf("probe during grace: %v, want retry", got)
	}
	checkAlerts(t, rec, 0)
	// A sign-in prompt on Teams itself does not wait.
	if got := d.ProbeResult(ProbeResult{Href: teamsURL, Signin: true, Why: "signin-button"}, nil); got != ProbeExpired {
		t.Fatalf("sign-in prompt on Teams during grace: %v", got)
	}
	checkAlerts(t, rec, 1)
	clk.Advance(time.Minute)
	checkAlerts(t, rec, 1) // the probe on Teams cancelled the grace timer
}

func TestHealthyProbeCancelsLoginGrace(t *testing.T) {
	d, clk, rec := newDetector(t, nil)
	d.MainFrameNavigated("https://login.microsoftonline.com/")
	// The navigation back to Teams was missed (e.g. events dropped); the probe sees Teams.
	if got := d.ProbeResult(ProbeResult{Href: teamsURL}, nil); got != ProbeOK {
		t.Fatalf("probe: %v", got)
	}
	clk.Advance(time.Hour)
	checkAlerts(t, rec, 0)
}

func TestProbeReasonIsSanitised(t *testing.T) {
	d, _, rec := newDetector(t, nil)
	d.ProbeResult(ProbeResult{Href: teamsURL, Signin: true, Why: "<img src=x> Alice: hi\n"}, nil)
	d.ProbeResult(ProbeResult{Href: "https://xn--80ak6aa92e.example/" + strings.Repeat("a", 5000)}, nil)
	d.ProbeResult(ProbeResult{Href: "https://exa%20mple.com/"}, nil)
	for _, a := range rec.all() {
		if strings.ContainsAny(a.Reason, "<>\n/") || strings.Contains(a.Reason, "Alice") || len(a.Reason) > 200 {
			t.Errorf("unsafe reason %q", a.Reason)
		}
	}
}

func TestProbeActionSchedule(t *testing.T) {
	cases := []struct {
		a     ProbeAction
		delay time.Duration
		renav bool
		str   string
	}{
		{ProbeOK, ProbeInterval, false, "ok"},
		{ProbeRetry, ProbeRetryInterval, false, "retry"},
		{ProbeOffPage, ProbeRetryInterval, true, "off-page"},
		{ProbeExpired, ProbeInterval, true, "expired"},
		{ProbeRestart, 0, false, "restart"},
		{ProbeAction(99), 0, false, "unknown"},
	}
	for _, tc := range cases {
		if tc.a.Delay() != tc.delay || tc.a.Renavigate() != tc.renav || tc.a.String() != tc.str {
			t.Errorf("%v: Delay %v Renavigate %v String %q", tc.a, tc.a.Delay(), tc.a.Renavigate(), tc.a.String())
		}
	}
	if ProbeInterval != 15*time.Minute {
		t.Errorf("ProbeInterval = %v, BRIEF §5 says 15 min", ProbeInterval)
	}
}

func TestBrowserLost(t *testing.T) {
	t.Run("cancels the login grace timer", func(t *testing.T) {
		d, clk, rec := newDetector(t, nil)
		d.MainFrameNavigated("https://login.microsoftonline.com/")
		d.BrowserLost(errors.New("websocket: close 1006"))
		clk.Advance(time.Hour)
		checkAlerts(t, rec, 0)
		if clk.pending() != 0 {
			t.Error("timer still pending")
		}
	})
	t.Run("clears the redirect-loop count", func(t *testing.T) {
		// A crash loop: every new browser bounces through the login host once on start.
		d, clk, rec := newDetector(t, nil)
		for range 2 * LoginLoopLimit {
			d.MainFrameNavigated("https://login.microsoftonline.com/")
			d.MainFrameNavigated(teamsURL)
			d.BrowserLost(errors.New("exit status 1"))
			clk.Advance(5 * time.Second)
		}
		checkAlerts(t, rec, 0)
	})
	t.Run("clears the 401/403 window", func(t *testing.T) {
		d, clk, rec := newDetector(t, nil)
		u := "https://teams.microsoft.com/api/csa/x"
		d.Response(u, 401, "XHR")
		d.Response(u, 401, "XHR")
		d.BrowserLost(nil)
		clk.Advance(time.Second)
		d.Response(u, 401, "XHR")
		checkAlerts(t, rec, 0)
	})
	t.Run("clears the probe error count", func(t *testing.T) {
		d, _, _ := newDetector(t, nil)
		e := errors.New("target closed")
		d.ProbeResult(ProbeResult{}, e)
		d.ProbeResult(ProbeResult{}, e)
		d.BrowserLost(e)
		if got := d.ProbeResult(ProbeResult{}, e); got != ProbeRetry {
			t.Errorf("after BrowserLost: %v, want retry", got)
		}
	})
	t.Run("never alerts and keeps Expired", func(t *testing.T) {
		d, _, rec := newDetector(t, func(c *DetectorConfig) { c.LoginGrace = -1 })
		for range 10 {
			d.BrowserLost(errors.New("exit status 1"))
		}
		checkAlerts(t, rec, 0)
		d.MainFrameNavigated("https://login.live.com/")
		d.BrowserLost(nil)
		if !d.Expired() {
			t.Error("BrowserLost cleared Expired")
		}
		checkAlerts(t, rec, 1)
	})
}

func TestDetectorClose(t *testing.T) {
	d, clk, rec := newDetector(t, nil)
	d.MainFrameNavigated("https://login.microsoftonline.com/")
	d.Close()
	if clk.pending() != 0 {
		t.Error("Close left the grace timer pending")
	}
	clk.Advance(time.Hour)
	d.MainFrameNavigated("https://login.microsoftonline.com/")
	if clk.pending() != 0 {
		t.Error("a login navigation after Close armed a timer")
	}
	clk.Advance(time.Hour)
	for range 5 {
		d.Response("https://teams.microsoft.com/api/csa/x", 401, "XHR")
	}
	if act := d.ProbeResult(ProbeResult{Href: "https://login.live.com/"}, nil); act != ProbeExpired {
		t.Errorf("probe after Close: %v, want expired (the methods keep working)", act)
	}
	checkAlerts(t, rec, 0)

	// A timer callback that was already running when Close came in finds the Detector closed.
	d2, _, rec2 := newDetector(t, nil)
	d2.MainFrameNavigated("https://login.microsoftonline.com/")
	d2.mu.Lock()
	gen := d2.loginGen
	d2.closed = true // Close without cancelling, as if Stop had lost the race with the timer
	d2.mu.Unlock()
	d2.loginTimeout(gen)
	checkAlerts(t, rec2, 0)
}

// TestDetectorDebounced: with the default debouncer, session_expired reaches emit at most once
// per 6 h whichever detector raises it.
func TestDetectorDebounced(t *testing.T) {
	d, clk, rec := newDetector(t, func(c *DetectorConfig) { c.Alerts = nil; c.LoginGrace = -1 })
	login := "https://login.microsoftonline.com/"
	api := "https://teams.microsoft.com/api/csa/x"
	d.MainFrameNavigated(login) // t=0: sent
	clk.Advance(time.Hour)
	for range 3 {
		d.Response(api, 401, "XHR") // t=1h: suppressed
	}
	d.ProbeResult(ProbeResult{Href: login}, nil) // suppressed
	clk.Advance(5*time.Hour - time.Second)
	d.MainFrameNavigated(teamsURL)
	d.MainFrameNavigated(login) // t=6h-1s: suppressed
	if n := rec.count(proto.KindSessionExpired); n != 1 {
		t.Fatalf("%d alerts within 6 h, want 1", n)
	}
	clk.Advance(time.Second)
	d.ProbeResult(ProbeResult{Href: login}, nil) // t=6h: sent
	if n := rec.count(proto.KindSessionExpired); n != 2 {
		t.Fatalf("%d alerts after 6 h, want 2", n)
	}
}

// TestSharedDebouncer: one Debouncer shared by Detector, FailureTracker and the shutdown path
// keeps the kinds independent.
func TestSharedDebouncer(t *testing.T) {
	clk := newFakeClock()
	rec := &recorder{}
	deb := NewDebouncer(0, clk.Now)
	d := NewDetector(DetectorConfig{TeamsURL: teamsURL, LoginGrace: -1, Alerts: deb, Now: clk.Now, AfterFunc: clk.AfterFunc, Logger: quiet}, rec.emit)
	ft := NewFailureTracker(TrackerConfig{Alerts: deb, Now: clk.Now, Backoff: Backoff{Jitter: NoJitter}, Logger: quiet}, rec.emit)
	d.MainFrameNavigated("https://login.live.com/")
	for range 5 {
		ft.Stopped(errors.New("exec: msedge: not found"))
	}
	stop := deb.Wrap(rec.emit)
	stop(Alert{Kind: proto.KindDaemonStopping, Reason: "SIGTERM", At: clk.Now()})
	stop(Alert{Kind: proto.KindDaemonStopping, Reason: "SIGTERM", At: clk.Now()})
	for kind, want := range map[string]int{proto.KindSessionExpired: 1, proto.KindBrowserDead: 1, proto.KindDaemonStopping: 1} {
		if got := rec.count(kind); got != want {
			t.Errorf("%s: %d alerts, want %d", kind, got, want)
		}
	}
}
