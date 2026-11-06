package faketeams

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/browser"
	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/hook"

	"github.com/chromedp/cdproto"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp/remote"
)

// capture records everything a session hands to its Handlers.
type capture struct {
	mu          sync.Mutex
	changed     chan struct{}
	events      []captured
	parseErrors int
	navs        []string
	responses   []response
	disconnects []error
}

type captured struct {
	transport string
	ev        hook.Event
}

type response struct {
	url    string
	status int
	rtype  string
}

func newCapture() *capture { return &capture{changed: make(chan struct{})} }

func (c *capture) bump() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *capture) onBinding(transport, payload string) {
	ev, err := hook.Parse(payload)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.parseErrors++
	} else {
		c.events = append(c.events, captured{transport, ev})
	}
	c.bump()
}

func (c *capture) onNav(u string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.navs = append(c.navs, u)
	c.bump()
}

func (c *capture) onResponse(u string, status int, rtype string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responses = append(c.responses, response{u, status, rtype})
	c.bump()
}

func (c *capture) onDisconnect(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnects = append(c.disconnects, err)
	c.bump()
}

func (c *capture) snapshot() (events []captured, navs []string, responses []response, disconnects []error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]captured(nil), c.events...), append([]string(nil), c.navs...),
		append([]response(nil), c.responses...), append([]error(nil), c.disconnects...)
}

// waitFor calls cond after every captured signal until it returns true; it fails the test after
// timeout, describing what.
func (c *capture) waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		c.mu.Lock()
		ch := c.changed
		c.mu.Unlock()
		if cond() {
			return
		}
		select {
		case <-ch:
		case <-timer.C:
			if cond() {
				return
			}
			evs, navs, _, _ := c.snapshot()
			t.Fatalf("timed out after %s waiting for %s; captured %d events, navs=%v", timeout, what, len(evs), navs)
		}
	}
}

// waitEvent waits for a parsed event matching pred and returns it.
func (c *capture) waitEvent(t *testing.T, timeout time.Duration, what string, pred func(captured) bool) captured {
	t.Helper()
	var found captured
	c.waitFor(t, timeout, what, func() bool {
		evs, _, _, _ := c.snapshot()
		for _, e := range evs {
			if pred(e) {
				found = e
				return true
			}
		}
		return false
	})
	return found
}

func isNotification(source, title string) func(captured) bool {
	return func(c captured) bool {
		return c.ev.Kind == hook.KindNotification && c.ev.Source == source && c.ev.Title == title
	}
}

func devBrowser(t *testing.T) string {
	t.Helper()
	b, err := config.Default().DetectBrowser()
	if err != nil {
		if os.Getenv("TBD_REQUIRE_BROWSER") == "1" {
			t.Fatalf("no browser: %v", err)
		}
		t.Skipf("no browser: %v", err)
	}
	t.Logf("browser %s (%s)", b.Path, b.Source)
	return b.Path
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type env struct {
	srv  *Server
	c    *capture
	proc *browser.Process
	sess *browser.Session
	ua   string // the user agent the browser and session use
}

// start launches the headless browser exactly as the daemon would (browser-wide --user-agent plus
// the per-target override) and attaches a session against the fake Teams site.
func start(t *testing.T) *env {
	t.Helper()
	path := devBrowser(t)
	srv := New()
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ver, err := config.BrowserVersion(ctx, path)
	if err != nil || ver.Major == 0 {
		t.Fatalf("browser version: %v (%+v)", err, ver)
	}
	ua := browser.UserAgent(ver.Major)
	proc, err := browser.Launch(ctx, browser.LaunchOptions{
		Path: path, Home: t.TempDir(), Headless: true, UserAgent: ua, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() {
		cctx, cc := context.WithTimeout(context.Background(), 20*time.Second)
		defer cc()
		_ = proc.Close(cctx)
	})

	c := newCapture()
	sess, err := browser.Attach(ctx, proc.WSURL, browser.SessionOptions{
		TeamsURL:          srv.TeamsURL(),
		Version:           ver,
		PermissionOrigins: []string{srv.Origin()},
		ReportHidden:      true,
		Logger:            discardLogger(),
	}, browser.Handlers{
		OnBinding:            c.onBinding,
		OnMainFrameNavigated: c.onNav,
		OnResponse:           c.onResponse,
		OnDisconnect:         c.onDisconnect,
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(sess.Close)
	return &env{srv: srv, c: c, proc: proc, sess: sess, ua: ua}
}

func TestE2ENotificationsAllSources(t *testing.T) {
	e := start(t)
	const timeout = 60 * time.Second

	alice := e.c.waitEvent(t, timeout, "window Notification (Alice)", isNotification(hook.SourceWindow, "Alice"))
	// Shaped like a real Teams v2 notification: no data, empty tag, the sender only in the avatar
	// URL; the hook resolved Alice's 1:1 chat from the chat list (not the group she is also in).
	if alice.transport != browser.TransportPage || alice.ev.Body != "hi" || alice.ev.Tag != "" || len(alice.ev.Data) != 0 ||
		alice.ev.Href != e.srv.TeamsURL() || hook.SenderMRI(alice.ev.Icon) != "8:orgid:00000000-0000-4000-8000-00000000a11c" {
		t.Errorf("Alice: %+v", alice)
	}
	if alice.ev.Conversation != "19:uni01_alice@thread.v2" {
		t.Errorf("resolved conversation %q, want Alice's 1:1 chat", alice.ev.Conversation)
	}
	if got := strings.Join(alice.ev.OptionKeys, ","); got != "body,icon,timestamp,tag,requireInteraction,dir" {
		t.Errorf("option keys %q", got)
	}
	if alice.ev.Timestamp == 0 {
		t.Error("options.timestamp not forwarded")
	}
	wantLink := e.srv.Origin() + "/l/chat/19:uni01_alice@thread.v2/conversations"
	if got := hook.DeepLink(e.srv.TeamsURL(), alice.ev); got != wantLink {
		t.Errorf("DeepLink(Alice) = %q, want %q", got, wantLink)
	}

	bob := e.c.waitEvent(t, timeout, "page ServiceWorkerRegistration.showNotification (Bob)", isNotification(hook.SourceSWPage, "Bob"))
	if bob.transport != browser.TransportPage || bob.ev.Body != "from sw-page" || bob.ev.Tag != "19:bob@unq.gbl.spaces" {
		t.Errorf("Bob: %+v", bob)
	}

	// The worker-side hook: it must work with the bundled Edge and with host Edge.
	carol := e.c.waitEvent(t, timeout, "worker self.registration.showNotification (Carol)", isNotification(hook.SourceSWWorker, "Carol"))
	if carol.transport != browser.TransportWorker || carol.ev.Body != "from sw-worker" {
		t.Errorf("Carol: %+v", carol)
	}
	if got, want := hook.DeepLink(e.srv.TeamsURL(), carol.ev), e.srv.Origin()+"/l/message/19:carol@thread.tacv2/1648741500888"; got != want {
		t.Errorf("DeepLink(Carol) = %q, want %q", got, want)
	}

	// The liveness signals: the unread count from the title, and one "hooked" per document/worker.
	e.c.waitEvent(t, timeout, "title count 3", func(c captured) bool {
		return c.ev.Kind == hook.KindTitle && c.ev.Count == 3 && c.ev.Href == e.srv.TeamsURL()
	})
	e.c.waitEvent(t, timeout, "hooked (page)", func(c captured) bool {
		return c.ev.Kind == hook.KindHooked && c.ev.Source == hook.SourceWindow && c.ev.Href == e.srv.TeamsURL()
	})
	e.c.waitEvent(t, timeout, "hooked (service worker)", func(c captured) bool {
		return c.ev.Kind == hook.KindHooked && c.ev.Source == hook.SourceSWWorker && c.transport == browser.TransportWorker
	})

	// Each initial notification is raised once by the site and must arrive exactly once. Leave
	// the worker queue two more drain cycles to show a duplicate, if there were one.
	if raised, ok := e.srv.WaitRaised(timeout, len(InitialNotifications)); !ok {
		t.Fatalf("site raised only %v", raised)
	}
	time.Sleep(1 * time.Second)
	evs, _, _, _ := e.c.snapshot()
	for _, want := range InitialNotifications {
		n := 0
		for _, c := range evs {
			if isNotification(want.Source, want.Title)(c) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s/%s arrived %d times, want exactly once", want.Source, want.Title, n)
		}
	}
	if got := e.srv.Raised(); len(got) != len(InitialNotifications) {
		t.Errorf("site raised %v, want %v", got, InitialNotifications)
	}
	e.c.mu.Lock()
	perr := e.c.parseErrors
	e.c.mu.Unlock()
	if perr != 0 {
		t.Errorf("%d hook payloads did not parse", perr)
	}

	// On-demand notifications, as the daemon-level tests use them.
	e.srv.Notify("Dave", "on demand", "19:dave@thread.v2", map[string]any{"messageId": "1648741501000"})
	dave := e.c.waitEvent(t, timeout, "on-demand Notification (Dave)", isNotification(hook.SourceWindow, "Dave"))
	if dave.ev.Body != "on demand" || !strings.Contains(string(dave.ev.Data), "1648741501000") {
		t.Errorf("Dave: %+v", dave)
	}
}

func TestE2EUserAgentAndClientHints(t *testing.T) {
	e := start(t)
	const timeout = 60 * time.Second
	if e.sess.UserAgent() != e.ua {
		t.Errorf("session UA %q, want %q", e.sess.UserAgent(), e.ua)
	}

	// Wait for the page's own environment report and for one request from each kind of client:
	// the page, the dedicated worker, and the service worker (script fetch and its own fetch).
	env, ok := e.srv.WaitReport(timeout, func(r Report) bool { return r.Type == "env" && r.HighEntropy != nil })
	if !ok {
		t.Fatal("no env report with high-entropy values")
	}
	for _, path := range []string{"/v2/", "/v2/app.js", "/v2/worker.js", "/api/v1/worker-ping", "/v2/sw.js", "/api/v1/sw-ping", "/api/v1/poll"} {
		if _, ok := e.srv.WaitRequest(timeout, func(r Request) bool { return r.Path == path }); !ok {
			t.Fatalf("no request for %s", path)
		}
	}

	// Every request, whoever made it, carries exactly the Windows Edge UA.
	reqs := e.srv.Requests()
	for _, r := range reqs {
		if r.UserAgent != e.ua {
			t.Errorf("%s: User-Agent =\n%q\nwant\n%q", r.Path, r.UserAgent, e.ua)
		}
	}

	// The navigation carries the low-entropy client hints; once Accept-CH is known, the page's
	// later requests carry the high-entropy ones too.
	nav := reqs[0]
	if nav.Path != "/v2/" {
		t.Fatalf("first request %q, want /v2/", nav.Path)
	}
	if !strings.Contains(nav.SecCHUA, `"Microsoft Edge";v="`) || !strings.Contains(nav.SecCHUA, `"Chromium";v="`) {
		t.Errorf("Sec-CH-UA = %q, want Microsoft Edge and Chromium brands", nav.SecCHUA)
	}
	if nav.SecCHUAPlatform != `"Windows"` {
		t.Errorf("Sec-CH-UA-Platform = %q, want \"Windows\"", nav.SecCHUAPlatform)
	}
	if nav.SecCHUAMobile != "?0" {
		t.Errorf("Sec-CH-UA-Mobile = %q, want ?0", nav.SecCHUAMobile)
	}
	app, _ := e.srv.WaitRequest(timeout, func(r Request) bool { return r.Path == "/v2/app.js" })
	if app.SecCHUAPlatformV != `"19.0.0"` || app.SecCHUAArch != `"x86"` || app.SecCHUABitness != `"64"` ||
		app.SecCHUAWoW64 != "?0" || app.SecCHUAModel != `""` || !strings.Contains(app.SecCHUAFullVerLst, `"Microsoft Edge";v="`) {
		t.Errorf("high-entropy hints on %s: platformVersion=%q arch=%q bitness=%q wow64=%q model=%q fullVersionList=%q",
			app.Path, app.SecCHUAPlatformV, app.SecCHUAArch, app.SecCHUABitness, app.SecCHUAWoW64, app.SecCHUAModel, app.SecCHUAFullVerLst)
	}

	// What the page's JavaScript sees.
	if env.UserAgent != e.ua {
		t.Errorf("navigator.userAgent = %q, want %q", env.UserAgent, e.ua)
	}
	if env.Platform != "Windows" || env.Mobile == nil || *env.Mobile {
		t.Errorf("userAgentData platform=%q mobile=%v", env.Platform, env.Mobile)
	}
	he := env.HighEntropy
	if pv, _ := he["platformVersion"].(string); pv != "19.0.0" {
		t.Errorf("platformVersion = %v, want 19.0.0", he["platformVersion"])
	}
	if a, _ := he["architecture"].(string); a != "x86" {
		t.Errorf("architecture = %v, want x86", he["architecture"])
	}
	if b, _ := he["bitness"].(string); b != "64" {
		t.Errorf("bitness = %v, want 64", he["bitness"])
	}
	if w, _ := he["wow64"].(bool); w {
		t.Error("wow64 should be false")
	}
	if !strings.Contains(fmt.Sprint(he["fullVersionList"]), "Microsoft Edge") {
		t.Errorf("fullVersionList = %v", he["fullVersionList"])
	}
	if !env.Hidden {
		t.Errorf("document.hidden = %v, want true", env.Hidden)
	}
	if env.VisibilityState != "hidden" {
		t.Errorf("visibilityState = %q, want hidden", env.VisibilityState)
	}
	if env.HasFocus {
		t.Error("document.hasFocus() should be false")
	}
	if env.Permission != "granted" {
		t.Errorf("Notification.permission = %q, want granted", env.Permission)
	}
}

func TestE2ELoginRedirectNavigation(t *testing.T) {
	e := start(t)

	// Wait until the page is up (first API poll seen), then trigger the redirect.
	if _, ok := e.srv.WaitRequest(60*time.Second, func(r Request) bool { return r.Path == "/api/v1/poll" }); !ok {
		t.Fatal("page never polled")
	}
	e.srv.RedirectToLogin()

	prefix := "http://" + e.srv.LoginHost() + ":"
	e.c.waitFor(t, 30*time.Second, "a main-frame navigation to the login host", func() bool {
		_, navs, _, _ := e.c.snapshot()
		for _, u := range navs {
			if strings.HasPrefix(u, prefix) && strings.HasSuffix(u, "/login") {
				return true
			}
		}
		return false
	})
	// The fake sign-in page is served and shows a sign-in affordance.
	if _, ok := e.srv.WaitRequest(30*time.Second, func(r Request) bool {
		return r.Path == "/login" && strings.HasPrefix(r.Host, e.srv.LoginHost()+":")
	}); !ok {
		t.Error("the login page was not requested from the login host")
	}
	var hasInput bool
	if err := e.sess.Evaluate(context.Background(), `!!document.querySelector('input[type=email]') && /Sign in/.test(document.body.innerText)`, &hasInput); err != nil || !hasInput {
		t.Errorf("sign-in affordance on the login page: %v %v", hasInput, err)
	}

	// A cross-site navigation is not a disconnect, and neither is Close.
	e.sess.Close()
	select {
	case <-e.sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Done not closed after Close")
	}
	if _, _, _, d := e.c.snapshot(); len(d) != 0 {
		t.Errorf("unexpected OnDisconnect: %v", d)
	}
}

func TestE2E401Burst(t *testing.T) {
	e := start(t)

	if _, ok := e.srv.WaitRequest(60*time.Second, func(r Request) bool { return r.Path == "/api/v1/poll" }); !ok {
		t.Fatal("page never polled")
	}
	e.srv.Expire401(5)

	e.c.waitFor(t, 30*time.Second, "a burst of 401 responses on /api/v1/poll", func() bool {
		_, _, resps, _ := e.c.snapshot()
		n := 0
		for _, r := range resps {
			if strings.HasPrefix(r.url, e.srv.Origin()+"/api/v1/poll") && r.status == 401 {
				if r.rtype != "Fetch" {
					t.Errorf("401 response type %q, want Fetch", r.rtype)
				}
				n++
			}
		}
		return n >= 3
	})
}

func TestE2EDisconnectWhenBrowserDies(t *testing.T) {
	e := start(t)
	if _, ok := e.srv.WaitRequest(60*time.Second, func(r Request) bool { return r.Path == "/v2/app.js" }); !ok {
		t.Fatal("page never loaded")
	}
	p, err := os.FindProcess(e.proc.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("kill browser: %v", err)
	}
	e.c.waitFor(t, 30*time.Second, "OnDisconnect", func() bool {
		_, _, _, d := e.c.snapshot()
		return len(d) > 0
	})
	select {
	case <-e.sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Done not closed after the browser died")
	}
	select {
	case <-e.proc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Process.Done not closed after the browser died")
	}
	_, _, _, d := e.c.snapshot()
	if len(d) != 1 || !errors.Is(d[0], browser.ErrConnectionLost) {
		t.Errorf("OnDisconnect calls: %v, want one ErrConnectionLost", d)
	}
}

// TestE2ELoginWindowed is the `tbd login` flow with a real window: it runs on Windows (a window
// flashes for a few seconds) and on Linux only with a display.
func TestE2ELoginWindowed(t *testing.T) {
	if runtime.GOOS != "windows" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no display: a windowed login cannot run here (it runs on the Windows host)")
	}
	path := devBrowser(t)
	srv := New()
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ready := make(chan string, 1)
	signedIn := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- browser.Login(ctx, browser.LoginOptions{
			Path:       path,
			Home:       t.TempDir(),
			TeamsURL:   srv.TeamsURL(),
			LoginHosts: []string{srv.LoginHost()},
			Settle:     2 * time.Second,
			Logger:     discardLogger(),
			OnReady:    func(ws string) { ready <- ws },
			OnSignedIn: func() { close(signedIn) },
		})
	}()

	var ws string
	select {
	case ws = <-ready:
	case err := <-done:
		t.Fatalf("Login returned early: %v", err)
	case <-time.After(40 * time.Second):
		t.Fatal("login never became ready")
	}
	select {
	case <-signedIn:
	case <-time.After(40 * time.Second):
		t.Fatal("Login never considered the page signed in")
	}
	if _, ok := srv.WaitRequest(10*time.Second, func(r Request) bool { return r.Path == "/v2/app.js" }); !ok {
		t.Error("the Teams page was not loaded in the login window")
	}

	// The user closes the window.
	closeViaCDP(t, ws)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Login returned %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Login did not return after the window closed")
	}
}

func closeViaCDP(t *testing.T, wsURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := remote.DialContext(ctx, wsURL)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer conn.Close()
	if err := conn.Write(ctx, &cdproto.Message{ID: 1, Method: cdproto.MethodType(cdpbrowser.CommandClose)}); err != nil {
		t.Fatalf("Browser.close: %v", err)
	}
	reply := &cdproto.Message{}
	_ = conn.Read(ctx, reply)
}

// TestE2ENotificationShim drives the window.Notification shim from the page, as Teams would use it.
func TestE2ENotificationShim(t *testing.T) {
	e := start(t)
	const timeout = 60 * time.Second
	e.c.waitEvent(t, timeout, "the page's first notification", isNotification(hook.SourceWindow, "Alice"))

	var r struct {
		Perm, Legacy, Static string
		Props                []any
		Events               []string
		Config               string
		Proto                bool
	}
	err := e.sess.Evaluate(context.Background(), `(async () => {
		const r = {};
		let legacy = null;
		r.Perm = await Notification.requestPermission(v => { legacy = v; });
		r.Legacy = legacy;
		r.Static = Notification.permission;
		const n = new Notification("Shim", {body: "b", tag: "t", data: {a: 1}, icon: "/i.png", requireInteraction: true});
		r.Props = [n.title, n.body, n.tag, n.data && n.data.a, n.icon, n.requireInteraction, typeof n.timestamp];
		const ev = [];
		n.onshow = () => ev.push("onshow");
		n.addEventListener("show", () => ev.push("show"));
		const gone = () => ev.push("removed-listener");
		n.addEventListener("close", gone);
		n.onclose = () => ev.push("onclose");
		await new Promise(res => setTimeout(res, 50));
		n.removeEventListener("close", gone);
		n.close();
		r.Events = ev;
		const cyc = {}; cyc.self = cyc;
		new Notification("Cyclic", {body: "kept", data: cyc});
		new Notification("BigInt", {data: {x: 10n}});
		document.title = "(99+) Chat | Microsoft Teams";
		r.Config = typeof window.__tbdConfig;
		r.Proto = typeof Notification.prototype.close === "function";
		return r;
	})()`, &r)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if r.Perm != "granted" || r.Legacy != "granted" || r.Static != "granted" {
		t.Errorf("permission: %+v", r)
	}
	if fmt.Sprint(r.Props) != "[Shim b t 1 /i.png true number]" {
		t.Errorf("instance properties: %v", r.Props)
	}
	if strings.Join(r.Events, ",") != "onshow,show,onclose" {
		t.Errorf("events %v, want onshow,show,onclose (show async, removed listener not called)", r.Events)
	}
	if r.Config != "undefined" || !r.Proto {
		t.Errorf("config global %q (want removed), prototype close %v", r.Config, r.Proto)
	}

	e.c.waitEvent(t, timeout, "title count 99 from \"(99+)\"", func(c captured) bool {
		return c.ev.Kind == hook.KindTitle && c.ev.Count == 99
	})
	shim := e.c.waitEvent(t, timeout, "Shim", isNotification(hook.SourceWindow, "Shim"))
	if shim.ev.Body != "b" || shim.ev.Tag != "t" || string(shim.ev.Data) != `{"a":1}` || !strings.HasSuffix(shim.ev.Href, "/v2/") {
		t.Errorf("Shim event: %+v", shim.ev)
	}
	for _, title := range []string{"Cyclic", "BigInt"} {
		c := e.c.waitEvent(t, timeout, title, isNotification(hook.SourceWindow, title))
		if !c.ev.DataDropped || c.ev.Data != nil {
			t.Errorf("%s: unserialisable data should be dropped and flagged: %+v", title, c.ev)
		}
	}

	// Idempotent: injecting the preload again does not double-wrap.
	if err := e.sess.Evaluate(context.Background(), hook.PreloadJS+`; new Notification("Once"); true`, nil); err != nil {
		t.Fatalf("re-inject: %v", err)
	}
	e.c.waitEvent(t, timeout, "Once", isNotification(hook.SourceWindow, "Once"))
	e.srv.Notify("After", "", "", nil) // a later notification marks the end of the Once stream
	e.c.waitEvent(t, timeout, "After", isNotification(hook.SourceWindow, "After"))
	evs, _, _, _ := e.c.snapshot()
	n := 0
	for _, c := range evs {
		if isNotification(hook.SourceWindow, "Once")(c) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Once arrived %d times after a second injection, want 1", n)
	}
}

// TestE2EGroupMessageFromSenderWithA1on1 is the case the title alone cannot decide: a group message
// whose title is the sender's name (as Teams titles chat toasts), from someone who also has a 1:1
// chat. The chat list's preview of the group shows this message, so the group wins.
func TestE2EGroupMessageFromSenderWithA1on1(t *testing.T) {
	e := start(t)
	const timeout = 60 * time.Second
	e.c.waitEvent(t, timeout, "the page's first notification", isNotification(hook.SourceWindow, "Alice"))
	e.srv.NotifyWith("Alice", map[string]any{
		"body": "see the doc", "icon": AliceIcon(e.srv.Origin()), "timestamp": time.Now().UnixMilli(),
		"tag": "", "requireInteraction": true, "dir": "auto",
	})
	got := e.c.waitEvent(t, timeout, "the group message from Alice", func(r captured) bool {
		return r.ev.Kind == hook.KindNotification && r.ev.Title == "Alice" && r.ev.Body == "see the doc"
	})
	if got.ev.Conversation != "19:uni01_projectx@thread.v2" {
		t.Fatalf("resolved %q, want the group chat Project X", got.ev.Conversation)
	}
}
