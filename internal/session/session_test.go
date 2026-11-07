package session

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/proto"
)

func TestDebouncer(t *testing.T) {
	type step struct {
		at    time.Duration // since epoch
		kind  string
		allow bool
	}
	const (
		se = proto.KindSessionExpired
		bd = proto.KindBrowserDead
		ds = proto.KindDaemonStopping
	)
	cases := []struct {
		name   string
		period time.Duration
		steps  []step
	}{
		{"same kind within 6 h is suppressed", 0, []step{{0, se, true}, {time.Minute, se, false}, {6*time.Hour - time.Second, se, false}}},
		{"after 6 h it passes again", 0, []step{{0, se, true}, {6 * time.Hour, se, true}, {7 * time.Hour, se, false}, {12 * time.Hour, se, true}}},
		{"suppressed attempts do not extend the period", 0, []step{{0, se, true}, {5 * time.Hour, se, false}, {6 * time.Hour, se, true}}},
		{"kinds are independent", 0, []step{{0, se, true}, {time.Second, bd, true}, {2 * time.Second, se, false}, {3 * time.Second, bd, false}, {4 * time.Second, ds, true}}},
		{"daemon_stopping after other alerts", 0, []step{{0, bd, true}, {0, se, true}, {0, ds, true}, {0, ds, false}}},
		{"custom period", time.Hour, []step{{0, se, true}, {59 * time.Minute, se, false}, {time.Hour, se, true}}},
		{"negative period disables", -1, []step{{0, se, true}, {0, se, true}, {time.Second, se, true}}},
		{"clock moved back", 0, []step{{10 * time.Hour, se, true}, {9 * time.Hour, se, true}, {10 * time.Hour, se, false}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := epoch
			d := NewDebouncer(tc.period, func() time.Time { return now })
			for i, s := range tc.steps {
				now = epoch.Add(s.at)
				if got := d.Allow(s.kind); got != s.allow {
					t.Fatalf("step %d (%s at %v): Allow = %v, want %v", i, s.kind, s.at, got, s.allow)
				}
			}
		})
	}
}

func TestDebouncerForgetAndWrap(t *testing.T) {
	clk := newFakeClock()
	d := NewDebouncer(0, clk.Now)
	rec := &recorder{}
	emit := d.Wrap(rec.emit)
	a := Alert{Kind: proto.KindSessionExpired, Reason: "x", At: clk.Now()}
	emit(a)
	emit(a)
	if rec.count(a.Kind) != 1 {
		t.Fatalf("Wrap let %d through", rec.count(a.Kind))
	}
	d.Forget(a.Kind)
	emit(a)
	if rec.count(a.Kind) != 2 {
		t.Fatal("Forget did not reset the kind")
	}
}

func TestDebouncerSeed(t *testing.T) {
	const se, bd = proto.KindSessionExpired, proto.KindBrowserDead
	now := epoch
	d := NewDebouncer(0, func() time.Time { return now })
	d.Seed(se, epoch.Add(-5*time.Hour)) // sent by the previous daemon process 5 h ago
	d.Seed(se, epoch.Add(-7*time.Hour)) // older: ignored
	d.Seed(bd, time.Time{})             // zero: ignored
	d.Seed(proto.KindDaemonStopping, epoch.Add(time.Hour))
	if d.Allow(se) {
		t.Error("seeded kind passed within the period")
	}
	if !d.Allow(bd) {
		t.Error("zero seed blocked the kind")
	}
	if !d.Allow(proto.KindDaemonStopping) {
		t.Error("a seed in the future (clock went back) blocked the kind")
	}
	now = epoch.Add(time.Hour)
	if !d.Allow(se) {
		t.Error("seeded kind still blocked 6 h after the seed")
	}
}

func TestDebouncerConcurrent(t *testing.T) {
	d := NewDebouncer(0, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 50 {
		wg.Go(func() {
			if d.Allow(proto.KindBrowserDead) {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("%d concurrent alerts allowed, want 1", allowed)
	}
}

func TestAlertMessage(t *testing.T) {
	at := time.Unix(1767225600, 0)
	for _, kind := range []string{proto.KindSessionExpired, proto.KindBrowserDead, proto.KindDaemonStopping} {
		m, err := Alert{Kind: kind, Reason: "main frame navigated to login.live.com", At: at}.Message()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Validate(); err != nil {
			t.Fatal(err)
		}
		if m.T != proto.TypeAlert || m.Kind != kind || m.TS != at.Unix() || m.Title == "" || m.Body == "" || m.URL != "" {
			t.Errorf("%s: %+v", kind, m)
		}
		var key [proto.KeySize]byte
		if _, err := proto.EncryptMessage(key, m, nil); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	m, _ := Alert{Kind: "other"}.Message()
	if m.Title == "" || m.TS == 0 {
		t.Errorf("fallbacks: %+v", m)
	}
}

func TestShortErr(t *testing.T) {
	if got := shortErr(nil); got != "exited" {
		t.Errorf("nil: %q", got)
	}
	long := errors.New("chrome failed to start:\n" + strings.Repeat("σφάλμα エラー 😀 ", 100))
	got := shortErr(long)
	if strings.Contains(got, "\n") || len([]rune(got)) > 160 || !strings.HasSuffix(got, "…") {
		t.Errorf("long: %q", got)
	}
}

func TestHostHelpers(t *testing.T) {
	origins := map[string]string{
		"https://teams.microsoft.com/v2/":       "https://teams.microsoft.com",
		"HTTPS://Teams.Microsoft.COM:443/x":     "https://teams.microsoft.com",
		"http://127.0.0.1:8080/":                "http://127.0.0.1:8080",
		"http://localhost:80/":                  "http://localhost",
		"http://[::1]:9222/json":                "http://[::1]:9222",
		"https://teams.cloud.microsoft./v2/":    "https://teams.cloud.microsoft",
		"about:blank":                           "",
		"chrome-error://chromewebdata/":         "",
		"teams.microsoft.com":                   "",
		"":                                      "",
		"https:///nohost":                       "",
		"wss://go.trouter.teams.microsoft.com/": "",
	}
	for in, want := range origins {
		if got := originOf(in); got != want {
			t.Errorf("originOf(%q) = %q, want %q", in, got, want)
		}
	}
	login := compilePatterns([]string{"login.live.com", " *.Example.ORG. ", ""}, true)
	api := compilePatterns([]string{"teams.microsoft.com", "*.teams.cloud.microsoft"}, false)
	matches := []struct {
		host string
		pats []hostPattern
		want bool
	}{
		{"login.live.com", login, true},
		{"a.login.live.com", login, true},
		{"xlogin.live.com", login, false},
		{"example.org", login, true},
		{"a.b.example.org", login, true},
		{"example.org.evil", login, false},
		{"", login, false},
		{"teams.microsoft.com", api, true},
		{"x.teams.microsoft.com", api, false},
		{"teams.cloud.microsoft", api, true},
		{"zz.teams.cloud.microsoft", api, true},
		{"cloud.microsoft", api, false},
	}
	for _, m := range matches {
		if got := matchHost(m.host, m.pats); got != m.want {
			t.Errorf("matchHost(%q) = %v, want %v", m.host, got, m.want)
		}
	}
	if got := safeHost("bad host<script>"); got != "an unexpected host" {
		t.Errorf("safeHost: %q", got)
	}
	if got := safeHost("zz.ng.msg.teams.microsoft.com"); got != "zz.ng.msg.teams.microsoft.com" {
		t.Errorf("safeHost: %q", got)
	}
}
