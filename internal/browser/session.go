package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/inspector"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/hook"
)

func ptr[T any](v T) *T { return &v }

// Binding transports, the first argument of Handlers.OnBinding.
const (
	TransportPage   = "page"      // a Runtime.bindingCalled of __tbd in a document of the Teams page
	TransportWorker = "sw-worker" // a report from a hooked service worker (its binding or its queue)
)

// ErrConnectionLost is reported through OnDisconnect when the CDP connection to the browser ends.
var ErrConnectionLost = errors.New("browser: CDP connection lost")

// SessionOptions configure Attach.
type SessionOptions struct {
	TeamsURL          string         // page to open
	UserAgent         string         // full UA override; empty means the templated Windows Edge UA
	Version           config.Version // browser version; zero means ask CDP after attach
	PermissionOrigins []string       // origins granted the notifications permission
	ReportHidden      bool           // make the page believe it is hidden/unfocused
	Logger            *slog.Logger
}

// Handlers receive the signals the daemon needs. All are optional. They are called from internal
// goroutines and must not block for long: each stream (bindings, navigations, responses, each
// worker's drain) has its own goroutine, so a slow handler delays only its own stream, while the
// events behind it queue in memory. Handlers may still be called briefly after Close returns.
type Handlers struct {
	// OnBinding receives one raw, UNTRUSTED hook report (parse it with hook.Parse). transport is
	// TransportPage or TransportWorker.
	OnBinding            func(transport string, payload string)
	OnMainFrameNavigated func(url string) // main-frame Page.frameNavigated
	OnResponse           func(url string, status int, resourceType string)
	// OnDisconnect is called at most once when the session dies on its own: the CDP connection
	// was lost (ErrConnectionLost, e.g. the browser exited), or the Teams page crashed or was
	// detached. It is not called for Close or for the end of the context passed to Attach.
	OnDisconnect func(err error)
}

// Session is an attached Teams page.
type Session struct {
	log     *slog.Logger
	ver     config.Version
	actual  config.Version
	ua      string
	meta    *emulation.UserAgentMetadata
	handler Handlers

	pageCtx  context.Context
	evCtx    context.Context
	cancelEv context.CancelFunc
	cancel   func() // closes the page target and the connection

	done       chan struct{}
	ended      chan error // the page crashed or was detached
	closing    atomic.Bool
	disconnect sync.Once

	workersMu sync.Mutex
	workers   map[target.ID]struct{}
}

// openPage connects to the browser at wsURL and attaches to its existing page target (the
// about:blank tab that Launch opens). For a remote browser chromedp would otherwise create a second
// tab, which is a wasted renderer headless and a second window for `tbd login` (the browser would
// then not exit when the user closes the Teams window). Without an existing page, chromedp creates
// one. The browser connection lives as long as ctx and until cancel is called.
func openPage(ctx context.Context, wsURL string) (context.Context, func(), error) {
	allocCtx, cancelAlloc := remote.NewAllocator(ctx, wsURL, remote.NoModifyURL)
	bctx, cancelB := chromedp.NewContext(allocCtx)

	var id target.ID
	deadline := time.Now().Add(10 * time.Second)
	for id == "" {
		infos, err := chromedp.Targets(bctx)
		if err != nil {
			cancelB()
			cancelAlloc()
			return nil, nil, fmt.Errorf("browser: list targets: %w", err)
		}
		for _, ti := range infos {
			if ti.Type == "page" && (id == "" || ti.URL == "about:blank") {
				id = ti.TargetID
			}
		}
		if id != "" || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			cancelB()
			cancelAlloc()
			return nil, nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	var opts []chromedp.ContextOption
	if id != "" {
		opts = append(opts, chromedp.WithTargetID(id))
	}
	pageCtx, cancelPage := chromedp.NewContext(bctx, opts...)
	cancel := func() {
		cancelPage()
		cancelB()
		cancelAlloc()
	}
	if err := chromedp.Do(pageCtx); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("browser: attach: %w", err)
	}
	return pageCtx, cancel, nil
}

// Attach connects chromedp to the running browser at wsURL, attaches to its page and, before
// navigating, installs the Windows UA override and client hints, grants the notifications
// permission, installs the __tbd binding and the preload hook, and arranges service-worker hooking
// (Network is enabled and related targets are auto-attached by chromedp's attach). It then starts
// the navigation to the Teams URL and returns without waiting for the page to load.
//
// ctx bounds the whole session, not just the call: pass a long-lived context.
func Attach(ctx context.Context, wsURL string, o SessionOptions, h Handlers) (*Session, error) {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}

	pageCtx, cancel, err := openPage(ctx, wsURL)
	if err != nil {
		return nil, err
	}
	br := chromedp.FromContext(pageCtx).Browser

	// Always ask the running browser: the caller's version comes from the binary on disk, which an
	// auto-update can change under a running daemon (or stage next to a still-old msedge.exe).
	var actual config.Version
	if res, err := chromedp.CallBrowser(pageCtx, browser.GetVersion, cdp.Empty{}); err == nil {
		if v, ok := config.ParseVersion(res.Product); ok {
			actual = v
		} else if v, ok := config.ParseVersion(res.UserAgent); ok {
			actual = v
		}
		log.Debug("browser version from CDP", "product", res.Product, "parsed", actual.Full)
	} else {
		log.Warn("Browser.getVersion failed; using the configured version", "err", err)
	}
	ver := o.Version
	if ver.Major == 0 || ver.Full == "" {
		ver = actual
	}

	uaString := o.UserAgent
	sendMeta := true
	if uaString == "" {
		uaString = UserAgent(ver.Major)
	} else if !looksLikeChromiumUA(uaString) {
		// A non-Chromium override would contradict the client hints, so send none with it.
		sendMeta = false
		log.Warn("user_agent override is not a Chrome/Edge UA; sending no client-hint metadata")
	}
	var meta *emulation.UserAgentMetadata
	if sendMeta {
		meta = UserAgentMetadata(ver.Full)
	}

	preamble := fmt.Sprintf("window.__tbdConfig=%s;\n", configJSON(o.ReportHidden))
	script := preamble + hook.PreloadJS

	evCtx, cancelEv := context.WithCancel(pageCtx)
	s := &Session{
		log: log, ver: ver, actual: actual, ua: uaString, meta: meta, handler: h,
		pageCtx: pageCtx, evCtx: evCtx,
		cancelEv: cancelEv, cancel: cancel,
		done:    make(chan struct{}),
		ended:   make(chan error, 1),
		workers: map[target.ID]struct{}{},
	}

	// Subscribe before navigating, so no early event is lost (Events subscribes on return).
	bindings := chromedp.Events(evCtx, runtime.BindingCalled)
	navs := chromedp.Events(evCtx, page.FrameNavigated)
	responses := chromedp.Events(evCtx, network.ResponseReceived)
	attached := chromedp.Events(evCtx, target.AttachedToTarget)
	crashed := chromedp.Events(evCtx, inspector.TargetCrashed)
	detached := chromedp.Events(evCtx, inspector.Detached)

	setup := chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		if _, err := cdp.Call(ctx, t, emulation.SetUserAgentOverride, uaOverrideFor(goruntime.GOOS, uaString, meta)); err != nil {
			return fmt.Errorf("setUserAgentOverride: %w", err)
		}
		if _, err := cdp.Call(ctx, t, runtime.AddBinding, runtime.AddBindingParams{Name: hook.BindingName}); err != nil {
			return fmt.Errorf("addBinding: %w", err)
		}
		if _, err := cdp.Call(ctx, t, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
			Source: script, RunImmediately: ptr(true),
		}); err != nil {
			return fmt.Errorf("addScriptToEvaluateOnNewDocument: %w", err)
		}
		// chromedp's attach already auto-attaches related targets like this; it is repeated here
		// because the service-worker hook depends on exactly these flags. Workers are not paused
		// on start (WaitForDebuggerOnStart): chromedp has no handle on the auto-attached session
		// to resume them from, and a paused worker stalls navigator.serviceWorker.ready. So a
		// worker is hooked moments after it starts; a notification it raises in that window is
		// missed (see docs/DECISIONS.md).
		if _, err := cdp.Call(ctx, t, target.SetAutoAttach, target.SetAutoAttachParams{
			AutoAttach: true, WaitForDebuggerOnStart: false, Flatten: ptr(true),
		}); err != nil {
			return fmt.Errorf("setAutoAttach: %w", err)
		}
		return nil
	})
	if err := chromedp.Do(pageCtx, setup); err != nil {
		s.Close()
		return nil, fmt.Errorf("browser: session setup: %w", err)
	}

	for _, origin := range o.PermissionOrigins {
		if _, err := chromedp.CallBrowser(pageCtx, browser.SetPermission, browser.SetPermissionParams{
			Permission: &browser.PermissionDescriptor{Name: "notifications"},
			Setting:    browser.PermissionSettingGranted,
			Origin:     origin,
		}); err != nil {
			log.Warn("granting the notifications permission failed", "origin", origin, "err", err)
		}
	}

	// Event pumps. Each handler runs on its own goroutine.
	go pump(s, "bindings", bindings, func(ev runtime.EventBindingCalled) {
		if ev.Name == hook.BindingName && s.handler.OnBinding != nil {
			s.handler.OnBinding(TransportPage, ev.Payload)
		}
	})
	go pump(s, "navigations", navs, func(ev page.EventFrameNavigated) {
		// Main frame only (ignore the token-renewal iframes).
		if ev.Frame != nil && ev.Frame.ParentID == "" && s.handler.OnMainFrameNavigated != nil {
			s.handler.OnMainFrameNavigated(ev.Frame.URL)
		}
	})
	go pump(s, "responses", responses, func(ev network.EventResponseReceived) {
		if ev.Response != nil && s.handler.OnResponse != nil {
			s.handler.OnResponse(ev.Response.URL, int(ev.Response.Status), string(ev.Type))
		}
	})
	go pump(s, "attached targets", attached, s.onAttached)
	go pump(s, "crashes", crashed, func(inspector.EventTargetCrashed) {
		s.end(errors.New("browser: the Teams page crashed"))
	})
	go pump(s, "detaches", detached, func(ev inspector.EventDetached) {
		s.end(fmt.Errorf("browser: the Teams page was detached: %s", ev.Reason))
	})

	// Watch for the session dying on its own.
	go func() {
		defer close(s.done)
		var err error
		select {
		case <-br.LostConnection:
			err = ErrConnectionLost
		case err = <-s.ended:
		case <-evCtx.Done():
			// chromedp also cancels the context when the connection is lost, so the two race:
			// LostConnection is closed before that cancel, so check it.
			select {
			case <-br.LostConnection:
				err = ErrConnectionLost
			default:
				return // Close, or the caller's context ended
			}
		}
		s.fireDisconnect(err)
	}()

	// Navigate without waiting for the load event, so a slow page does not stall Attach.
	if err := chromedp.Do(pageCtx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		res, err := cdp.Call(ctx, t, page.Navigate, page.NavigateParams{URL: o.TeamsURL})
		if err != nil {
			return err
		}
		if res.ErrorText != "" {
			return errors.New(res.ErrorText)
		}
		return nil
	})); err != nil {
		s.Close()
		return nil, fmt.Errorf("browser: navigate to %s: %w", o.TeamsURL, err)
	}
	log.Debug("session navigated", "url", o.TeamsURL, "ua", uaString, "version", ver.Full)
	return s, nil
}

// pump feeds the events of one stream to fn until the session ends. An iterator only ends early on
// an event that does not decode (or a closed target); that would silently stop a detector, so it is
// logged loudly.
func pump[E any](s *Session, name string, it iter.Seq2[E, error], fn func(E)) {
	for ev, err := range it {
		if err != nil {
			if s.evCtx.Err() == nil {
				s.log.Warn("browser: CDP event stream ended; its signal is lost until the session restarts",
					"stream", name, "err", err)
			}
			return
		}
		fn(ev)
	}
}

// end reports that the page died (crash, detach) to the watcher, once.
func (s *Session) end(err error) {
	select {
	case s.ended <- err:
	default:
	}
}

func (s *Session) onAttached(ev target.EventAttachedToTarget) {
	ti := ev.TargetInfo
	if ti == nil || ti.Type != "service_worker" {
		// Dedicated and shared workers have no registration to show notifications from, and
		// they inherit the page's (and the --user-agent) user agent; nothing to hook.
		return
	}
	if !strings.HasPrefix(ti.URL, "https://") && !strings.HasPrefix(ti.URL, "http://") {
		return // e.g. a browser extension's worker
	}
	s.workersMu.Lock()
	_, seen := s.workers[ti.TargetID]
	if !seen {
		s.workers[ti.TargetID] = struct{}{}
	}
	s.workersMu.Unlock()
	if !seen {
		go s.hookWorker(ti.TargetID)
	}
}

// workerDrainInterval is how often the Go side drains a worker's fallback queue (and checks that
// the worker is still there).
const workerDrainInterval = time.Second

// hookWorker attaches to a service-worker target, applies the session's UA override where the
// target supports it, installs the __tbd binding and the worker-side hook, forwards the binding's
// calls, and polls the worker's fallback queue (reports made while no binding was installed). It
// logs loudly if the worker cannot be hooked (BRIEF §5).
func (s *Session) hookWorker(id target.ID) {
	defer func() {
		s.workersMu.Lock()
		delete(s.workers, id)
		s.workersMu.Unlock()
	}()
	// WithDetachOnCancel: ending our interest in the worker must only detach. By default chromedp
	// closes the target on cancel, which for a service worker means terminating Teams' worker.
	wctx, cancel := chromedp.NewContext(s.pageCtx, chromedp.WithTargetID(id), chromedp.WithDetachOnCancel())
	defer cancel()
	// Tear the worker context down when the session ends.
	stop := context.AfterFunc(s.evCtx, cancel)
	defer stop()

	// Attach the worker target. The first chromedp call on wctx binds the Target's event pump to
	// wctx, so it must be wctx itself (a short-lived timeout context here would stop the pump when
	// it ended, hanging every later command). Bound the attach with a timer instead.
	attachDone := make(chan error, 1)
	go func() { attachDone <- chromedp.Do(wctx) }()
	select {
	case err := <-attachDone:
		if err != nil {
			if s.evCtx.Err() == nil {
				s.log.Warn("could not attach to the service worker; worker-raised notifications will be missed",
					"target", id, "err", err)
			}
			return
		}
	case <-time.After(20 * time.Second):
		s.log.Warn("attaching to the service worker timed out; worker-raised notifications will be missed",
			"target", id)
		return
	case <-s.evCtx.Done():
		return
	}

	// The worker's binding calls arrive on the worker's own session. Subscribe before the binding
	// exists, so the hook's first report ("hooked") is not missed.
	go func() {
		for ev, err := range chromedp.Events(wctx, runtime.BindingCalled) {
			if err != nil {
				return // the worker or the session went away
			}
			if ev.Name == hook.BindingName && s.handler.OnBinding != nil {
				s.handler.OnBinding(TransportWorker, ev.Payload)
			}
		}
	}()

	// A per-call timeout is safe now that the pump is bound to wctx.
	ictx, icancel := context.WithTimeout(wctx, 15*time.Second)
	var hooked bool
	err := chromedp.Do(ictx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		// Best effort, first: the worker's own requests should carry the session's UA and client
		// hints (the browser-wide --user-agent covers the UA string up to this point). A worker
		// target may implement only one of the two domains.
		ov := uaOverrideFor(goruntime.GOOS, s.ua, s.meta)
		if _, err := cdp.Call(ctx, t, emulation.SetUserAgentOverride, ov); err != nil {
			// Network.setUserAgentOverride takes the same parameters; cdproto has no typed command.
			if nerr := t.Call(ctx, "Network.setUserAgentOverride", ov, nil); nerr != nil {
				s.log.Debug("worker UA override not applied", "target", id, "emulation", err, "network", nerr)
			}
		}
		if _, err := cdp.Call(ctx, t, runtime.AddBinding, runtime.AddBindingParams{Name: hook.BindingName}); err != nil {
			s.log.Debug("worker binding not installed; reports go through the drained queue", "target", id, "err", err)
		}
		res, err := cdp.Call(ctx, t, runtime.Evaluate, runtime.EvaluateParams{
			Expression: hook.WorkerJS, ReturnByValue: ptr(true), Silent: ptr(true),
		})
		if err != nil {
			return fmt.Errorf("evaluate worker hook: %w", err)
		}
		if res.Result != nil {
			_ = json.Unmarshal(res.Result.Value, &hooked)
		}
		// In case the worker was paused on start, let it run.
		if _, err := cdp.Call(ctx, t, runtime.RunIfWaitingForDebugger, cdp.Empty{}); err != nil {
			s.log.Debug("runIfWaitingForDebugger on the worker failed", "target", id, "err", err)
		}
		return nil
	}))
	icancel()
	switch {
	case err != nil:
		if s.evCtx.Err() == nil {
			s.log.Warn("could not hook the service worker; worker-raised notifications will be missed",
				"target", id, "err", err)
		}
		return
	case !hooked:
		s.log.Warn("the service worker has no showNotification to wrap; worker-raised notifications will be missed",
			"target", id)
		return
	}
	s.log.Debug("service worker hooked", "target", id)
	s.drainWorker(wctx, id)
}

// drainWorker polls the worker's fallback queue until the worker or the session goes away.
func (s *Session) drainWorker(wctx context.Context, id target.ID) {
	t := time.NewTicker(workerDrainInterval)
	defer t.Stop()
	for {
		select {
		case <-wctx.Done():
			return
		case <-t.C:
		}
		dctx, dcancel := context.WithTimeout(wctx, 10*time.Second)
		var raw string
		err := chromedp.Do(dctx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			r, e := cdp.Call(ctx, t, runtime.Evaluate, runtime.EvaluateParams{
				Expression: hook.WorkerDrainJS, ReturnByValue: ptr(true), Silent: ptr(true),
			})
			if e != nil {
				return e
			}
			if r.Result != nil && len(r.Result.Value) > 0 {
				_ = json.Unmarshal(r.Result.Value, &raw)
			}
			return nil
		}))
		dcancel()
		if err != nil {
			// The worker stopped (a new version attaches and is hooked afresh) or the session ended.
			s.log.Debug("worker drain ended", "target", id, "err", err)
			return
		}
		// 64 reports of at most hook.MaxPayloadBytes each, plus JSON quoting: anything bigger did not
		// come from drain.js as written.
		if len(raw) > 64*(2*hook.MaxPayloadBytes+8) {
			s.log.Warn("service-worker queue drain too large; dropped", "bytes", len(raw))
			raw = ""
		}
		if raw == "" || raw == "[]" {
			continue
		}
		var items []string
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			s.log.Debug("worker drain payload did not parse", "target", id, "err", err)
			continue
		}
		for _, it := range items {
			if s.handler.OnBinding != nil {
				s.handler.OnBinding(TransportWorker, it)
			}
		}
	}
}

func (s *Session) fireDisconnect(err error) {
	if s.closing.Load() {
		return
	}
	s.disconnect.Do(func() {
		s.log.Warn("browser session lost", "err", err)
		if s.handler.OnDisconnect != nil {
			s.handler.OnDisconnect(err)
		}
	})
}

// Evaluate runs expr in the main frame and decodes the by-value result into out (which may be nil).
// It awaits a promise the expression returns. A thrown exception or a rejected promise is returned
// as an error (*chromedp.ExceptionError); undefined and null leave out unchanged.
func (s *Session) Evaluate(ctx context.Context, expr string, out any) error {
	cctx, cancel := context.WithCancel(s.pageCtx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	raw, err := chromedp.Run(cctx, chromedp.Evaluate[json.RawMessage](expr, chromedp.EvalAwaitPromise, chromedp.EvalIgnoreExceptions))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("browser: evaluate: %w", err)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Navigate navigates the main frame to url and waits for the load event.
func (s *Session) Navigate(ctx context.Context, url string) error {
	cctx, cancel := context.WithCancel(s.pageCtx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	err := chromedp.Do(cctx, chromedp.Navigate(url))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("browser: navigate: %w", err)
	}
	return nil
}

// Version returns the browser version this session is using.
func (s *Session) Version() config.Version { return s.ver }

// ActualVersion is what the running browser reported over CDP (zero if it could not be asked).
func (s *Session) ActualVersion() config.Version { return s.actual }

// UserAgent returns the user-agent string this session applies to the page and its workers.
func (s *Session) UserAgent() string { return s.ua }

// Done is closed when the session ends: Close, the end of the Attach context, a lost CDP
// connection, or a crashed or detached page (the last two also call OnDisconnect).
func (s *Session) Done() <-chan struct{} { return s.done }

// Close detaches, closes the Teams tab and the CDP connection. It does not stop the browser
// process (see Process). It is safe to call more than once, also from a handler.
func (s *Session) Close() {
	s.closing.Store(true)
	s.cancelEv()
	s.cancel()
}

// configJSON renders the preload config preamble value.
func configJSON(hidden bool) string {
	b, _ := json.Marshal(struct {
		Hidden bool `json:"hidden"`
	}{Hidden: hidden})
	return string(b)
}
