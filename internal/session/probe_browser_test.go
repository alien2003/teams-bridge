package session

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/alien2003/teams-bridge/internal/config"
)

// shell imitates the Teams app shell: many visible buttons and links (not a "sparse" page).
func shell(extra string) string {
	var b strings.Builder
	b.WriteString(`<nav>`)
	for i := range 30 {
		fmt.Fprintf(&b, `<button>Rail %d</button>`, i)
	}
	b.WriteString(`</nav><main><div class="chat">`)
	b.WriteString(`<div class="card"><p>Connect your Jira account</p><button>Sign in</button></div>`)
	b.WriteString(`</div></main>`)
	b.WriteString(extra)
	return b.String()
}

var probePages = map[string]string{
	// A healthy Teams page full of decoys that must not count.
	"/app": shell(`
		<div role="dialog" style="display:none"><h2>You're signed out</h2><button>Sign in</button></div>
		<div aria-hidden="true"><div role="dialog"><button>Sign in again</button></div></div>
		<div role="dialog" style="opacity:0"><button>Sign in</button></div>
		<div role="status">Alice: you're signed out of the VPN, sign in again</div>
		<input type="password" style="position:absolute;left:-10000px;top:0">
		<input type="email" placeholder="Invite someone">
		<iframe src="https://login.microsoftonline.com/common/oauth2/v2.0/authorize?prompt=none" style="width:0;height:0;border:0"></iframe>`),
	// An app shell made of tree items and an editor rather than buttons, with a chat that says
	// "sign in again" and a bot card with a "Sign in" button: not a sparse page.
	"/tree-shell": `<div role="tree">` + strings.Repeat(`<div role="treeitem" tabindex="0">Chat</div>`, 22) + `</div>
		<main><p>Bob: please sign in again to the VPN, your session has expired</p>
		<div class="card"><button>Sign in</button></div><div contenteditable="true">Type a message</div></main>`,
	// A modal dialog over the app shell: Fluent UI (tabster's modalizer) marks everything behind
	// it aria-hidden, which leaves few *visible* interactive elements, but the chat list behind
	// it still quotes message text. Not a sparse page, and not a sign-in prompt.
	"/modal-over-shell": `<div aria-hidden="true">` + shell(`<ul><li>Bob: please sign in again to the VPN</li><li>Eve: pick an account for the demo</li></ul>`) + `</div>
		<div role="dialog" aria-modal="true"><h2>What's new in Teams</h2><button>Close</button></div>`,
	// The same with Teams' own signed-out dialog: it is the modal itself, so it still counts.
	"/signed-out-over-shell": `<div aria-hidden="true">` + shell(``) + `</div>
		<div role="dialog" aria-modal="true"><h2>You've been signed out</h2><button>Continue</button></div>`,
	"/dialog":       shell(`<div role="dialog" aria-modal="true"><h2>You’re signed out</h2><p>Sign in again to keep using Teams.</p><button>Sign in</button></div>`),
	"/dialog-text":  shell(`<div role="dialog" aria-modal="true"><h2>Your session has expired</h2><button>OK</button></div>`),
	"/native":       shell(`<dialog open><p>Please sign in</p><button>Continue</button></dialog>`),
	"/banner":       shell(`<div role="alert">You need to sign in. <button>Sign in →</button></div>`),
	"/aria-label":   shell(`<div role="alertdialog"><button aria-label="Sign in"><svg width="16" height="16"></svg></button></div>`),
	"/ru-dialog":    shell(`<div role="alertdialog"><p>Сеанс истёк</p><button>Войти снова</button></div>`),
	"/ru-text":      shell(`<div role="dialog"><p>Срок действия сеанса истёк. Войдите снова.</p><button>ОК</button></div>`),
	"/credential":   shell(`<div role="dialog"><label>Password <input type="password"></label><button>Next</button></div>`),
	"/login-iframe": shell(`<iframe src="https://login.microsoftonline.com/common/oauth2/authorize" width="400" height="400"></iframe>`),
	"/aad": `<form><div>Sign in</div><input type="email" name="loginfmt" id="i0116" placeholder="Email, phone, or Skype">
		<a href="#">Can't access your account?</a><input type="submit" value="Next"></form>`,
	"/picker":     `<div>Pick an account</div><div id="tilesHolder"><div class="table" role="button">user@contoso.example</div></div>`,
	"/landing":    `<h1>Microsoft Teams</h1><p>Meet, chat, call and collaborate.</p><a href="/signin">Sign in</a>`,
	"/landing-ru": `<h1>Microsoft Teams</h1><button>Войти</button>`,
	"/loading":    `<div class="spinner"></div><p>Loading Teams…</p>`,
	"/error":      `<p>We're sorry—we've run into an issue.</p><button>Retry</button><button>Sign out</button>`,
	"/below-fold": `<p>Hello</p><button style="position:absolute;top:5000px">Sign in</button>`,
}

// browserContext starts the detected browser headless with a throwaway profile; it skips the
// test when there is none, unless TBD_REQUIRE_BROWSER=1.
func browserContext(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("browser test skipped in -short mode")
	}
	b, err := config.Default().DetectBrowser()
	if err != nil {
		if os.Getenv("TBD_REQUIRE_BROWSER") == "1" {
			t.Fatalf("TBD_REQUIRE_BROWSER=1 but no browser: %v", err)
		}
		t.Skipf("no browser: %v", err)
	}
	t.Logf("browser: %s (%s)", b.Path, b.Source)
	dir := t.TempDir()
	// Edge leaves its SingletonSocket directory in TMPDIR when it is killed, so give it a TMPDIR
	// that the test removes. Not t.TempDir(): a Unix socket path must stay under 108 bytes.
	tmp, err := os.MkdirTemp("", "e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(b.Path),
		chromedp.Env("TMPDIR="+tmp),
		chromedp.UserDataDir(filepath.Join(dir, "profile")),
		chromedp.WindowSize(1280, 800),
		chromedp.Flag("disable-crash-reporter", true),
		chromedp.Flag("crash-dumps-dir", filepath.Join(dir, "crashes")),
		// Nothing leaves the machine: the pages reference login.microsoftonline.com.
		chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1"),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	// Cleanups run last-in first-out: the browser is gone before TempDir is removed (Windows).
	t.Cleanup(cancelAlloc)
	t.Cleanup(cancelCtx)
	// The first Do binds the browser's life to its context, so it gets ctx itself, not a
	// context with a timeout (chromedp docs/API.md, example 21).
	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("start browser: %v", err)
	}
	return ctx
}

func TestProbeJSInBrowser(t *testing.T) {
	ctx := browserContext(t)
	mux := http.NewServeMux()
	for path, body := range probePages {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>t</title></head><body>%s</body></html>`, body)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	det := NewDetector(DetectorConfig{TeamsURL: srv.URL + "/", Alerts: NewDebouncer(-1, nil), Logger: quiet}, nil)
	cases := []struct {
		path string
		why  string // "" = no sign-in prompt
	}{
		{"/app", ""},
		{"/tree-shell", ""},
		{"/loading", ""},
		{"/error", ""},
		{"/below-fold", ""},
		{"/modal-over-shell", ""},
		{"/signed-out-over-shell", "signed-out-text"},
		{"/dialog", "signin-button"},
		{"/dialog-text", "signed-out-text"},
		{"/native", "signed-out-text"},
		{"/banner", "signin-button"},
		{"/aria-label", "signin-button"},
		{"/ru-dialog", "signin-button"},
		{"/ru-text", "signed-out-text"},
		{"/credential", "credential-input"},
		{"/login-iframe", "login-iframe"},
		{"/aad", "login-form"},
		{"/picker", "account-picker"},
		{"/landing", "signin-button"},
		{"/landing-ru", "signin-button"},
		{"about:blank", ""},
	}
	for _, tc := range cases {
		t.Run(strings.TrimPrefix(tc.path, "/"), func(t *testing.T) {
			u := tc.path
			if strings.HasPrefix(u, "/") {
				u = srv.URL + u
			}
			actx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			if err := chromedp.Do(actx, chromedp.Navigate(u)); err != nil {
				t.Fatalf("navigate: %v", err)
			}
			start := time.Now()
			r, err := chromedp.Run(actx, chromedp.Evaluate[ProbeResult](ProbeJS))
			if err != nil {
				t.Fatalf("evaluate ProbeJS: %v", err)
			}
			t.Logf("%+v in %v", r, time.Since(start).Round(time.Millisecond))
			if r.Href != u {
				t.Errorf("href %q, want %q", r.Href, u)
			}
			if r.Signin != (tc.why != "") || r.Why != tc.why {
				t.Errorf("signin=%v why=%q, want why=%q", r.Signin, r.Why, tc.why)
			}
			want := ProbeOK
			switch {
			case tc.path == "about:blank":
				want = ProbeOffPage
			case tc.why != "":
				want = ProbeExpired
			}
			if got := det.ProbeResult(r, nil); got != want {
				t.Errorf("Detector action %v, want %v", got, want)
			}
		})
	}
}
