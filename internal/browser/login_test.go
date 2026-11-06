package browser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp/remote"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/test/faketeams"
)

func TestLoginStateSettle(t *testing.T) {
	signed := make(chan struct{}, 4)
	l := &loginState{
		settle: 50 * time.Millisecond, teamsOrigins: []string{"https://teams.microsoft.com", "https://teams.cloud.microsoft"},
		loginHosts: []string{"login.microsoftonline.com"}, log: testLogger(),
		onSignedIn: func() { signed <- struct{}{} },
	}
	defer l.stop()

	// Teams' shell loads, then redirects to the login host before the settle time: not signed in.
	l.navigated("https://teams.microsoft.com/v2/")
	l.navigated("https://login.microsoftonline.com/common/oauth2")
	time.Sleep(120 * time.Millisecond)
	if l.signedIn() {
		t.Fatal("signed in while on the login host")
	}
	// Back on Teams and staying there: signed in, callback once.
	l.navigated("https://teams.microsoft.com/v2/")
	select {
	case <-signed:
	case <-time.After(2 * time.Second):
		t.Fatal("never signed in")
	}
	if !l.signedIn() {
		t.Fatal("signedIn false after the callback")
	}
	// Leaving again resets; a second sign-in does not call back again.
	l.navigated("https://login.microsoftonline.com/x")
	if l.signedIn() {
		t.Fatal("still signed in after leaving Teams")
	}
	l.navigated("https://teams.microsoft.com/v2/")
	time.Sleep(150 * time.Millisecond)
	if !l.signedIn() {
		t.Fatal("not signed in again")
	}
	if len(signed) != 0 {
		t.Fatal("OnSignedIn called twice")
	}
}

func closeBrowser(t *testing.T, wsURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := remote.DialContext(ctx, wsURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.Write(ctx, &cdproto.Message{ID: 1, Method: cdproto.MethodType(cdpbrowser.CommandClose)}); err != nil {
		t.Fatalf("Browser.close: %v", err)
	}
	_ = conn.Read(ctx, &cdproto.Message{})
}

// pageTargets lists the page targets over the DevTools HTTP endpoint.
func pageTargets(t *testing.T, wsURL string) []string {
	t.Helper()
	host := strings.TrimPrefix(wsURL, "ws://")
	host = host[:strings.IndexByte(host, '/')]
	resp, err := http.Get("http://" + host + "/json/list")
	if err != nil {
		t.Fatalf("json/list: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var infos []struct{ Type, URL string }
	if err := json.Unmarshal(b, &infos); err != nil {
		t.Fatalf("json/list: %v", err)
	}
	var pages []string
	for _, i := range infos {
		if i.Type == "page" {
			pages = append(pages, i.URL)
		}
	}
	return pages
}

// TestLoginFlow runs Login headless (the windowed run is in test/faketeams and needs a display):
// one tab only, signed in after the settle time, nil after the window closes.
func TestLoginFlow(t *testing.T) {
	path := requireBrowser(t)
	srv := faketeams.New()
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ready := make(chan string, 1)
	signedIn := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Login(ctx, LoginOptions{
			Path: path, Home: t.TempDir(), TeamsURL: srv.TeamsURL(), LoginHosts: []string{srv.LoginHost()},
			UserAgent: UserAgent(150), Settle: time.Second, Logger: testLogger(), headless: true,
			OnReady: func(ws string) { ready <- ws }, OnSignedIn: func() { close(signedIn) },
		})
	}()
	ws := <-ready
	select {
	case <-signedIn:
	case err := <-done:
		t.Fatalf("Login returned early: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("never signed in")
	}
	if pages := pageTargets(t, ws); len(pages) != 1 || pages[0] != srv.TeamsURL() {
		t.Errorf("page targets %v, want only the Teams page (no second tab/window)", pages)
	}
	if r, ok := srv.WaitRequest(10*time.Second, func(r faketeams.Request) bool { return r.Path == "/v2/" }); !ok || r.UserAgent != UserAgent(150) {
		t.Errorf("login UA %q, want the --user-agent value", r.UserAgent)
	}
	closeBrowser(t, ws)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Login = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Login did not return after the browser closed")
	}
}

func TestLoginClosedBeforeSignIn(t *testing.T) {
	path := requireBrowser(t)
	srv := faketeams.New()
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Login(ctx, LoginOptions{
			Path: path, Home: t.TempDir(), TeamsURL: srv.TeamsURL(), LoginHosts: []string{srv.LoginHost()},
			Settle: time.Hour, Logger: testLogger(), headless: true,
			OnReady: func(ws string) { ready <- ws },
		})
	}()
	select {
	case ws := <-ready:
		closeBrowser(t, ws)
	case err := <-done:
		t.Fatalf("Login returned before the browser was ready: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the browser never became ready")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotSignedIn) {
			t.Fatalf("Login = %v, want ErrNotSignedIn", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Login did not return after the browser closed")
	}
}

func TestLoginContextCancel(t *testing.T) {
	path := requireBrowser(t)
	srv := faketeams.New()
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Login(ctx, LoginOptions{
			Path: path, Home: t.TempDir(), TeamsURL: srv.TeamsURL(), Logger: testLogger(), headless: true,
			OnReady: func(ws string) { ready <- ws },
		})
	}()
	ws := <-ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Login = %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Login did not return after cancel")
	}
	// Login closed the browser before returning.
	if _, err := http.Get("http://" + strings.Split(strings.TrimPrefix(ws, "ws://"), "/")[0] + "/json/version"); err == nil {
		t.Error("the browser still answers after Login returned")
	}
}

// TestAttachSinglePage: Attach uses the browser's existing tab, not a second one.
func TestAttachSinglePage(t *testing.T) {
	path := requireBrowser(t)
	srv := faketeams.New()
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	proc, err := Launch(ctx, LaunchOptions{Path: path, Home: t.TempDir(), Headless: true, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, c := context.WithTimeout(context.Background(), 20*time.Second)
		defer c()
		_ = proc.Close(cctx)
	})
	sess, err := Attach(ctx, proc.WSURL, SessionOptions{TeamsURL: srv.TeamsURL(), Logger: testLogger()}, Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.Version().Major == 0 {
		t.Errorf("version from CDP not parsed: %+v", sess.Version())
	}
	if v, err := config.BrowserVersion(ctx, path); err == nil && v != sess.Version() {
		// Edge's CDP product is "Edg/<Edge version>"; a mismatch means the client hints would
		// carry another version than the binary when no version is configured.
		t.Errorf("CDP version %+v != binary version %+v", sess.Version(), v)
	}
	if _, ok := srv.WaitRequest(30*time.Second, func(r faketeams.Request) bool { return r.Path == "/v2/app.js" }); !ok {
		t.Fatal("page never loaded")
	}
	var href string
	if err := sess.Evaluate(ctx, "location.href", &href); err != nil || href != srv.TeamsURL() {
		t.Errorf("Evaluate(location.href) = %q, %v", href, err)
	}
	if err := sess.Evaluate(ctx, "(() => { throw new Error('x') })()", nil); err == nil {
		t.Error("Evaluate of a throwing expression returned no error")
	}
	if pages := pageTargets(t, proc.WSURL); len(pages) != 1 {
		t.Errorf("page targets %v, want exactly one", pages)
	}
}

// TestLoginStateOtherTeamsDomain: a normal sign-in may end on teams.cloud.microsoft (Teams moves
// users between its domains); an unrelated origin never counts.
func TestLoginStateOtherTeamsDomain(t *testing.T) {
	signed := make(chan struct{}, 1)
	l := &loginState{
		settle: 30 * time.Millisecond, teamsOrigins: []string{"https://teams.microsoft.com", "https://teams.cloud.microsoft"},
		loginHosts: []string{"login.microsoftonline.com"}, log: testLogger(),
		onSignedIn: func() { signed <- struct{}{} },
	}
	defer l.stop()
	l.navigated("https://teams.microsoft.com/v2/")
	l.navigated("https://login.microsoftonline.com/common/oauth2")
	l.navigated("https://teams.cloud.microsoft/v2/")
	select {
	case <-signed:
	case <-time.After(2 * time.Second):
		t.Fatal("a sign-in that ends on teams.cloud.microsoft never counted")
	}
	l.navigated("https://evil.example/v2/")
	time.Sleep(100 * time.Millisecond)
	if l.signedIn() {
		t.Fatal("an unrelated origin counts as Teams")
	}
}
