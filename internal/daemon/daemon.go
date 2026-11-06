// Package daemon wires the parts together: browser + hook capture → proto → FCM, plus the session
// detectors, heartbeat and the browser restart loop. cmd/tbd is a thin CLI on top.
package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alien2003/teams-bridge/internal/browser"
	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/hook"
	"github.com/alien2003/teams-bridge/internal/proto"
	"github.com/alien2003/teams-bridge/internal/session"
)

// Options configure Run.
type Options struct {
	Home    string
	Config  *config.Config
	Logger  *slog.Logger
	Once    bool          // exit 0 after the first notification has been delivered
	Timeout time.Duration // 0 = run until cancelled
	Version string

	// Test hooks; zero values mean production behaviour.
	Sender        Sender        // nil → FCM client from config
	ProbeInterval time.Duration // overrides the probe schedule (session.ProbeAction.Delay)
	FirstProbe    time.Duration // first probe after attach (default session.ProbeRetryInterval)
	LoginGrace    time.Duration // detector (1) grace (default session.DefaultLoginGrace)
	Debounce      time.Duration // alert debounce period (default session.DefaultDebounce)
	Healthy       time.Duration // a shorter browser run counts as failed (default session.DefaultHealthy)
}

// ErrNoNotification is returned by Run in --once mode when the timeout passes without a delivery.
var ErrNoNotification = errors.New("no notification was delivered before the timeout")

type exitCodeError struct {
	error
	code int
}

func (e exitCodeError) ExitCode() int { return e.code }
func (e exitCodeError) Unwrap() error { return e.error }

const (
	outboxSize  = 256
	dedupWindow = 10 * time.Second
	stopTimeout = 20 * time.Second
)

// errRelaunch asks the supervisor for an immediate restart that is not a failure.
var errRelaunch = errors.New("relaunch with the browser-wide user agent")

type daemon struct {
	o      Options
	cfg    *config.Config
	log    *slog.Logger
	secret proto.Secret
	sender Sender
	state  *stateStore
	alerts *session.Debouncer
	det    *session.Detector

	outbox    chan proto.Message
	closing   chan struct{} // closed when Run starts shutting down; enqueue stops accepting
	delivered chan struct{} // closed after the first msg delivery (--once)
	once      sync.Once
	outMu     sync.RWMutex // guards closing the outbox against concurrent enqueue

	mu      sync.Mutex
	recent  map[[32]byte]time.Time // dedup of identical notifications (page + SW for one message)
	counts  map[string]int         // occurrences per throttled log line
	bucket  tokenBucket            // page-originated notifications per second
	ver     config.Version         // last version the running browser reported (CDP)
	verDisk string                 // the on-disk version that ver was learned for
}

// Page-originated notifications are rate limited: any script on the Teams page can call the
// binding, and every accepted call is a push to the phone.
const (
	notifyBurst = 30
	notifyRate  = 1.0 // per second, sustained
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func (b *tokenBucket) allow(now time.Time) bool {
	if b.last.IsZero() {
		b.tokens, b.last = notifyBurst, now
	}
	b.tokens = min(notifyBurst, b.tokens+now.Sub(b.last).Seconds()*notifyRate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// throttled reports whether the n-th occurrence of a per-event log line should be written (the
// first 10, then every 100th), so a page cannot flood the log.
func (d *daemon) throttled(key string) (int, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.counts[key]++
	n := d.counts[key]
	return n, n <= 10 || n%100 == 0
}

// Run runs the daemon until ctx is cancelled (or, with Once, until one notification was delivered).
func Run(ctx context.Context, o Options) error {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	cfg, log := o.Config, o.Logger
	secret, err := cfg.PairingSecret()
	if err != nil {
		return err
	}
	sender := o.Sender
	if sender == nil {
		if sender, err = NewSender(cfg, o.Home, log); err != nil {
			return err
		}
	}
	lock, err := AcquireLock(o.Home)
	if err != nil {
		return err
	}
	defer lock.Release()

	d := &daemon{
		o: o, cfg: cfg, log: log, secret: secret, sender: sender,
		state:     &stateStore{home: o.Home, s: LoadState(o.Home)},
		outbox:    make(chan proto.Message, outboxSize),
		closing:   make(chan struct{}),
		delivered: make(chan struct{}),
		recent:    map[[32]byte]time.Time{},
		counts:    map[string]int{},
	}
	d.alerts = session.NewDebouncer(o.Debounce, nil)
	for kind, at := range d.state.get().LastAlerts { // survive restarts (systemd Restart=always)
		d.alerts.Seed(kind, at)
	}
	d.det = session.NewDetector(session.DetectorConfig{
		TeamsURL: cfg.TeamsURL, LoginHosts: cfg.LoginHosts, APIHosts: cfg.APIHosts,
		LoginGrace: o.LoginGrace, Alerts: d.alerts, Logger: log,
	}, d.onAlert)
	defer d.det.Close()
	d.state.update(func(s *State) {
		s.PID, s.StartedAt, s.LastError, s.SessionOK = os.Getpid(), time.Now(), "", false
	})
	log.Info("tbd starting", "version", o.Version, "home", o.Home, "teams_url", cfg.TeamsURL, "once", o.Once)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if o.Timeout > 0 {
		var c2 context.CancelFunc
		runCtx, c2 = context.WithTimeout(runCtx, o.Timeout)
		defer c2()
	}

	senderDone := make(chan struct{})
	go func() { defer close(senderDone); d.sendLoop() }()

	var hbDone chan struct{}
	if !o.Once {
		hbDone = make(chan struct{})
		go func() {
			defer close(hbDone)
			session.RunHeartbeat(runCtx, time.Duration(cfg.HeartbeatHours)*time.Hour, d.sendHeartbeat, log)
		}()
	} else {
		go func() {
			select {
			case <-d.delivered:
				cancel()
			case <-runCtx.Done():
			}
		}()
	}

	d.supervise(runCtx)
	cancel()
	if hbDone != nil {
		<-hbDone
	}
	d.det.Close() // no session_expired from a pending grace timer after this point

	// Shutdown: tell the phone (not in --once mode). A timeout is a stop, too.
	if !o.Once {
		d.enqueue(proto.Message{T: proto.TypeAlert, Kind: proto.KindDaemonStopping,
			Title: session.Title(proto.KindDaemonStopping),
			Body:  "The tbd daemon on the computer stopped; no Teams notifications until it runs again."})
	}
	d.outMu.Lock()
	close(d.closing)
	close(d.outbox)
	d.outMu.Unlock()
	select {
	case <-senderDone:
	case <-time.After(stopTimeout):
		log.Warn("gave up flushing the outbox")
	}
	log.Info("tbd stopped")
	if o.Once {
		select {
		case <-d.delivered:
			return nil
		default:
			return exitCodeError{ErrNoNotification, 3}
		}
	}
	return nil
}

// enqueue queues a message for delivery without blocking the CDP event goroutines.
func (d *daemon) enqueue(m proto.Message) {
	d.outMu.RLock()
	defer d.outMu.RUnlock()
	select {
	case <-d.closing:
		d.log.Warn("shutting down; message not sent", "type", m.T)
		return
	default:
	}
	select {
	case d.outbox <- m:
	default:
		if n, ok := d.throttled("outbox full"); ok {
			d.log.Warn("outbox full; dropping a message", "type", m.T, "dropped_total", n)
		}
	}
}

// deliver sends one message synchronously and records the outcome.
func (d *daemon) deliver(ctx context.Context, m proto.Message) error {
	name, err := sendAt(ctx, d.sender, d.secret, m, time.Now())
	if err != nil {
		d.log.Error("push failed", "type", m.T, "kind", m.Kind, "err", err)
		d.state.update(func(s *State) { s.LastError = "push: " + err.Error() })
		if m.T == proto.TypeAlert {
			d.alerts.Forget(m.Kind) // let the next occurrence try again
		}
		return err
	}
	now := time.Now()
	d.log.Info("pushed", "type", m.T, "kind", m.Kind, "fcm", name)
	d.state.update(func(s *State) {
		switch m.T {
		case proto.TypeHB:
			s.LastHeartbeat = now
		case proto.TypeMsg:
			s.LastMessage = now
			s.Messages++
		case proto.TypeAlert:
			// Only a delivered alert counts for the debounce carried across restarts: one that
			// failed must be able to go out after the next start.
			if m.Kind != proto.KindDaemonStopping {
				if s.LastAlerts == nil {
					s.LastAlerts = map[string]time.Time{}
				}
				s.LastAlerts[m.Kind] = now
			}
		}
	})
	if m.T == proto.TypeMsg {
		d.once.Do(func() { close(d.delivered) })
	}
	return nil
}

func (d *daemon) sendLoop() {
	for m := range d.outbox {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_ = d.deliver(ctx, m)
		cancel()
	}
}

func (d *daemon) sendHeartbeat(ctx context.Context) error {
	return d.deliver(ctx, proto.Message{T: proto.TypeHB})
}

// onAlert receives the alerts that passed the shared debouncer (one per kind per 6 h).
func (d *daemon) onAlert(a session.Alert) {
	d.log.Warn("alert", "kind", a.Kind, "reason", a.Reason)
	if a.Kind == proto.KindSessionExpired {
		d.state.update(func(s *State) { s.SessionOK = false })
	}
	m := proto.Message{T: proto.TypeAlert, Kind: a.Kind, Title: session.Title(a.Kind), URL: d.cfg.TeamsURL}
	switch a.Kind {
	case proto.KindSessionExpired:
		m.Body = "Sign in again on the computer: stop tbd, run `tbd login`, start tbd."
	case proto.KindBrowserDead:
		m.Body = "tbd cannot keep the browser running on the computer. Check `tbd status` and the logs."
	}
	if !a.At.IsZero() {
		m.TS = a.At.Unix()
	}
	d.enqueue(m)
}

// onBinding handles one untrusted __tbd payload from the page or a service worker.
func (d *daemon) onBinding(transport, payload string) {
	ev, err := hook.Parse(payload)
	if err != nil {
		// The page can call the binding at will: don't let it flood the log.
		if n, ok := d.throttled("rejected"); ok {
			d.log.Warn("rejected binding payload", "transport", transport, "bytes", len(payload), "err", err, "rejected_total", n)
		}
		return
	}
	switch ev.Kind {
	case hook.KindNotification:
		d.onNotification(ev)
	case hook.KindTitle:
		if n, ok := d.throttled("unread"); ok {
			d.log.Info("unread count", "count", ev.Count, "reports", n)
		}
		d.state.update(func(s *State) { s.UnreadCount = ev.Count })
	case hook.KindHooked:
		if n, ok := d.throttled("hooked"); ok {
			d.log.Info("page hook active", "source", ev.Source, "reports", n)
		}
	}
}

func (d *daemon) onNotification(ev hook.Event) {
	now := time.Now()
	// Teams' tag is always empty: the conversation and Teams' own timestamp separate two messages
	// with the same text, while the same message reported through two paths still matches.
	key := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", ev.Title, ev.Body, ev.Tag, ev.Conversation, ev.Timestamp)))
	d.mu.Lock()
	for k, t := range d.recent {
		if now.Sub(t) > dedupWindow {
			delete(d.recent, k)
		}
	}
	_, dup := d.recent[key]
	d.recent[key] = now
	allowed := dup || d.bucket.allow(now)
	d.mu.Unlock()
	d.log.Debug("notification raw", "source", ev.Source, "title", ev.Title, "body", ev.Body, "tag", ev.Tag,
		"data", string(ev.Data), "data_dropped", ev.DataDropped, "href", ev.Href)
	if dup {
		if n, ok := d.throttled("duplicate"); ok {
			d.log.Info("duplicate notification dropped", "source", ev.Source, "dropped_total", n)
		}
		return
	}
	if !allowed {
		if n, ok := d.throttled("rate"); ok {
			d.log.Warn("notification rate limit exceeded; dropping", "source", ev.Source, "dropped_total", n,
				"limit", fmt.Sprintf("%d burst, %.0f/s", notifyBurst, notifyRate))
		}
		return
	}
	url := hook.DeepLink(d.cfg.TeamsURL, ev)
	d.log.Info("notification captured", "source", ev.Source, "title_len", len(ev.Title), "body_len", len(ev.Body),
		"data", hook.DescribeData(ev.Data), "deep_link", url != d.cfg.TeamsURL,
		// Structure only (letters → a, digits → 9): shows whether the tag or icon carries an id
		// in some format DeepLink does not know yet, without logging the id itself.
		"tag_shape", hook.Shape(ev.Tag, 96), "icon_shape", hook.Shape(ev.Icon, 160),
		"options", strings.Join(ev.OptionKeys, ","), "page_origin", originOf(ev.Href),
		"conversation_shape", hook.Shape(ev.Conversation, 120), "dom", strings.Join(ev.DOMProbe, " | "))
	d.state.update(func(s *State) { s.LastEvent = now; s.SessionOK = true })
	d.enqueue(proto.Message{T: proto.TypeMsg, Title: ev.Title, Body: ev.Body, Tag: ev.Tag, URL: url})
}

// supervise keeps a browser + session alive until ctx ends, with exponential backoff between
// failed runs and a browser_dead alert after repeated failures (session.FailureTracker).
func (d *daemon) supervise(ctx context.Context) {
	ft := session.NewFailureTracker(session.TrackerConfig{Healthy: d.o.Healthy, Alerts: d.alerts, Logger: d.log}, d.onAlert)
	for ctx.Err() == nil {
		err := d.runBrowser(ctx, ft.Started)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRelaunch) {
			continue
		}
		wait := ft.Stopped(err)
		d.state.update(func(s *State) { s.LastError = "browser: " + err.Error() })
		d.log.Warn("browser session ended; restarting", "err", err, "in", wait.Round(time.Millisecond), "consecutive_failures", ft.Failures())
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// runBrowser launches the browser, attaches the session and blocks until it is lost or ctx ends.
func (d *daemon) runBrowser(ctx context.Context, started func()) error {
	b, err := d.cfg.DetectBrowser()
	if err != nil {
		return err
	}
	// The version on disk is read before every launch (an auto-update changes it under a running
	// daemon); what the running browser reports over CDP is the authority (D7/D13).
	disk, derr := config.BrowserVersion(ctx, b.Path)
	d.mu.Lock()
	ver := disk
	switch {
	case derr != nil:
		ver = d.ver // unknown on disk: what the browser told us last time (maybe nothing yet)
	case d.ver.Major != 0 && d.verDisk == disk.Full:
		ver = d.ver // disk said this before and the running binary differed (a staged update)
	}
	d.mu.Unlock()
	if derr != nil && ver.Major == 0 {
		d.log.Warn("browser version unknown before launch; asking the browser", "err", derr)
	}
	ua := browser.EffectiveUserAgent(d.cfg.UserAgent, ver.Major)
	proc, err := browser.Launch(ctx, browser.LaunchOptions{
		Path: b.Path, Home: d.o.Home, Headless: true, UserAgent: ua, ExtraArgs: d.cfg.BrowserArgs,
		ImplicitSignin: d.cfg.ImplicitSignin, Logger: d.log,
	})
	if err != nil {
		return fmt.Errorf("launch: %w", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = proc.Close(cctx)
	}()
	d.log.Info("browser started", "pid", proc.PID, "path", b.Path, "source", b.Source, "version", ver.Full,
		"profile", browser.ProfileDir(d.o.Home))
	d.state.update(func(s *State) { s.BrowserPath, s.BrowserVer = b.Path, ver.Full })

	lost := make(chan error, 1)
	sess, err := browser.Attach(ctx, proc.WSURL, browser.SessionOptions{
		TeamsURL: d.cfg.TeamsURL, UserAgent: d.cfg.UserAgent, Version: ver,
		PermissionOrigins: d.cfg.PermissionOrigins(), ReportHidden: d.cfg.Hidden(), Logger: d.log,
	}, browser.Handlers{
		OnBinding:            d.onBinding,
		OnMainFrameNavigated: d.det.MainFrameNavigated,
		OnResponse:           d.det.Response,
		OnDisconnect: func(err error) {
			select {
			case lost <- err:
			default:
			}
		},
	})
	if err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	defer sess.Close()
	started()
	if actual := sess.ActualVersion(); actual.Major != 0 {
		d.mu.Lock()
		d.ver, d.verDisk = actual, disk.Full
		d.mu.Unlock()
		// Launched without the browser-wide UA, or with another major version's: service workers
		// and the client hints would disagree with the running browser. Restart once with it.
		if ua == "" || actual.Major != ver.Major {
			d.log.Warn("restarting the browser with the user agent of the running version",
				"running", actual.Full, "assumed", ver.Full)
			return errRelaunch
		}
	}
	d.log.Info("session attached", "teams_url", d.cfg.TeamsURL, "user_agent", sess.UserAgent())

	first := d.o.FirstProbe
	if first <= 0 {
		first = session.ProbeRetryInterval
	}
	probe := time.NewTimer(first)
	defer probe.Stop()
	lastSurvey := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-proc.Done():
			d.det.BrowserLost(proc.Err())
			return fmt.Errorf("browser exited: %v", proc.Err())
		case <-sess.Done():
			d.det.BrowserLost(browser.ErrConnectionLost)
			return browser.ErrConnectionLost
		case err := <-lost:
			d.det.BrowserLost(err)
			return fmt.Errorf("session: %w", err)
		case <-probe.C:
			var r session.ProbeResult
			pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			perr := sess.Evaluate(pctx, session.ProbeJS, &r)
			cancel()
			act := d.det.ProbeResult(r, perr)
			ok := act == session.ProbeOK
			d.state.update(func(s *State) { s.SessionOK = ok })
			d.log.Debug("probe", "action", act.String(), "why", r.Why, "err", perr)
			if act == session.ProbeOK { // structure only; logged when the chat list's shape changes
				lastSurvey = d.surveyChatList(ctx, sess, lastSurvey)
			}
			if act == session.ProbeRestart {
				d.det.BrowserLost(session.ErrUnresponsive)
				return session.ErrUnresponsive
			}
			if act.Renavigate() { // keep retrying (§5): SSO may bring the session back on its own
				d.log.Info("probe: not on a healthy Teams page; reloading Teams", "action", act.String())
				nctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := sess.Navigate(nctx, d.cfg.TeamsURL); err != nil {
					d.log.Warn("reload failed", "err", err)
				}
				cancel()
			}
			next := act.Delay()
			if d.o.ProbeInterval > 0 {
				next = d.o.ProbeInterval
			}
			probe.Reset(next)
		}
	}
}

// Login runs `tbd login`: a windowed browser on the daemon's profile.
func Login(ctx context.Context, o Options) error {
	lock, err := AcquireLock(o.Home)
	if err != nil {
		return fmt.Errorf("%w: stop the daemon first", err)
	}
	defer lock.Release()
	b, err := o.Config.DetectBrowser()
	if err != nil {
		return err
	}
	ver, err := config.BrowserVersion(ctx, b.Path)
	if err != nil && o.Logger != nil {
		o.Logger.Warn("browser version unknown", "err", err)
	}
	return browser.Login(ctx, browser.LoginOptions{
		Path: b.Path, Home: o.Home, TeamsURL: o.Config.TeamsURL, LoginHosts: o.Config.LoginHosts, Version: ver,
		ExtraArgs: o.Config.BrowserArgs, ImplicitSignin: o.Config.ImplicitSignin,
		UserAgent: browser.EffectiveUserAgent(o.Config.UserAgent, ver.Major),
		Logger:    o.Logger,
	})
}

// originOf is scheme://host of a URL (no path, no query), for logging which Teams domain a page is on.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// surveyChatList logs (when it changed since last), structure only, what the conversation resolver
// would find on this page:
// how many chat-list elements hold a conversation id, under which attributes, with which labels and
// avatars (hook.SurveyJS). It needs no notification, so it shows on a new tenant right away whether
// taps will open the right chat.
func (d *daemon) surveyChatList(ctx context.Context, sess *browser.Session, last string) string {
	var sv hook.Survey
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := sess.Evaluate(sctx, hook.SurveyJS, &sv); err != nil {
		d.log.Warn("chat list survey failed", "err", err)
		return last
	}
	key := fmt.Sprintf("%d|%s|%s|%d|%d|%d", sv.Items, hook.Counts(sv.Attrs), hook.Counts(sv.Kinds), sv.Labelled, sv.AvatarMRI, sv.AvatarImg)
	if key == last {
		return last
	}
	samples := sv.Samples
	if len(samples) > 3 {
		samples = samples[:3]
	}
	for i := range samples {
		samples[i] = hook.Shape(samples[i], 80) // already masked by the page; mask again, it is page data
	}
	d.log.Info("chat list survey", "conversation_items", sv.Items, "attrs", hook.Counts(sv.Attrs),
		"kinds", hook.Counts(sv.Kinds), "labelled", sv.Labelled, "avatar_mri", sv.AvatarMRI, "avatar_img", sv.AvatarImg,
		"samples", strings.Join(samples, " "), "elements", sv.Elements, "ms", sv.MS, "path", hook.Shape(sv.Path, 40), "error", sv.Error)
	return key
}
