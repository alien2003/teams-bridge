// Package faketeams serves a deterministic local stand-in for the Microsoft Teams web app, used by
// the browser and daemon E2E tests. On load the page raises three notifications, each exactly once
// (InitialNotifications: a window Notification, a page-side
// ServiceWorkerRegistration.showNotification, and a worker-side self.registration.showNotification
// that the worker raises once it is hooked), sets an unread-count title "(3) …", reports its client
// hints and visibility/focus/permission state (Report type "env") and every notification it raises
// (type "raised", see Raised/WaitRaised), starts a dedicated worker and a service worker that fetch
// on their own (user agent of worker requests), and polls two endpoints: GET /api/v1/poll (Fetch)
// and a command poll that delivers Notify and RedirectToLogin. The server can simulate session
// expiry two ways: a burst of 401s from the API path (Expire401), or a redirect of the main frame to
// a fake Microsoft sign-in page on a different host (RedirectToLogin).
//
// The HTTP server listens on 127.0.0.1. "localhost" resolves to the same address, so the same
// server also answers as the fake login host; tests set login_hosts to [LoginHost()] and api_hosts
// to [APIHostPattern()]. The server records every request's User-Agent and Sec-CH-UA* headers and
// asks for the high-entropy hints with Accept-CH/Critical-CH on the page.
package faketeams

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"
)

//go:embed site
var siteFS embed.FS

// Request is one recorded HTTP request: its path and the headers that matter for the UA assertions.
type Request struct {
	Path              string
	Host              string
	UserAgent         string
	SecCHUA           string
	SecCHUAMobile     string
	SecCHUAPlatform   string
	SecCHUAPlatformV  string
	SecCHUAArch       string
	SecCHUABitness    string
	SecCHUAFullVerLst string
	SecCHUAModel      string
	SecCHUAWoW64      string
}

// Report is a /report payload posted by the page or its service worker. Type "env" carries the
// environment the page saw; type "raised" is sent once for every notification the site raises.
type Report struct {
	Type            string          `json:"type"`
	Source          string          `json:"source"` // "raised": window | sw-page | sw-worker
	Title           string          `json:"title"`  // "raised": the notification title
	UserAgent       string          `json:"ua"`
	Hidden          bool            `json:"hidden"`
	VisibilityState string          `json:"visibilityState"`
	HasFocus        bool            `json:"hasFocus"`
	Permission      string          `json:"permission"`
	Mobile          *bool           `json:"mobile"`
	Platform        string          `json:"platform"`
	HighEntropy     map[string]any  `json:"highEntropy"`
	Raw             json.RawMessage `json:"-"`
}

// Raised is one notification the fake site raised (as the site itself reports it).
type Raised struct {
	Source string // window | sw-page | sw-worker (the hook.Source* values)
	Title  string
}

// InitialNotifications are the notifications the page raises on every load, each exactly once:
// a window Notification, a page-side ServiceWorkerRegistration.showNotification, and one raised
// by the service worker itself.
var InitialNotifications = []Raised{
	{Source: "window", Title: "Alice"},
	{Source: "sw-page", Title: "Bob"},
	{Source: "sw-worker", Title: "Carol"},
}

// Server is a running fake Teams site.
type Server struct {
	ts   *httptest.Server
	port string

	mu        sync.Mutex
	changed   chan struct{} // closed and replaced whenever a request or report is recorded
	requests  []Request
	reports   []Report
	expire401 int
	redirect  bool
	pending   []notifySpec
}

type notifySpec struct {
	Title   string         `json:"title"`
	Options map[string]any `json:"options"`
}

const acceptCH = "Sec-CH-UA-Platform-Version, Sec-CH-UA-Arch, Sec-CH-UA-Bitness, " +
	"Sec-CH-UA-Full-Version-List, Sec-CH-UA-Model, Sec-CH-UA-Full-Version, " +
	"Sec-CH-UA-Mobile, Sec-CH-UA-Platform, Sec-CH-UA-WoW64"

// New starts a fake Teams server on 127.0.0.1. Call Close to stop it.
func New() *Server {
	s := &Server{changed: make(chan struct{})}
	s.ts = httptest.NewServer(http.HandlerFunc(s.handle))
	if u, err := url.Parse(s.ts.URL); err == nil {
		s.port = u.Port()
	}
	return s
}

// Close stops the server.
func (s *Server) Close() { s.ts.Close() }

// Origin is scheme://127.0.0.1:port.
func (s *Server) Origin() string { return s.ts.URL }

// TeamsURL is the page the daemon keeps open.
func (s *Server) TeamsURL() string { return s.ts.URL + "/v2/" }

// LoginHost is a different hostname for the same server, used as the fake Microsoft login host.
func (s *Server) LoginHost() string { return "localhost" }

// LoginURL is the fake sign-in page on the login host.
func (s *Server) LoginURL() string { return "http://localhost:" + s.port + "/login" }

// APIHostPattern is the host pattern for the 401/403 session detector (api_hosts).
func (s *Server) APIHostPattern() string { return "127.0.0.1" }

// Expire401 makes the next n GET /api/v1/poll responses return 401 (session-expiry simulation a).
func (s *Server) Expire401(n int) {
	s.mu.Lock()
	s.expire401 = n
	s.mu.Unlock()
}

// RedirectToLogin makes the page's next command poll (every 500 ms) send the main frame to the
// fake login host (session-expiry simulation b).
func (s *Server) RedirectToLogin() {
	s.mu.Lock()
	s.redirect = true
	s.mu.Unlock()
}

// Notify queues a window Notification that the page raises on its next command poll (every
// 500 ms), with options {body, tag, data}. data may be nil. Each queued notification is raised
// once, by the first page that polls.
func (s *Server) Notify(title, body, tag string, data any) {
	opts := map[string]any{"body": body, "tag": tag}
	if data != nil {
		opts["data"] = data
	}
	s.mu.Lock()
	s.pending = append(s.pending, notifySpec{Title: title, Options: opts})
	s.mu.Unlock()
}

// NotifyWith queues an on-demand window.Notification with exactly these options (e.g. shaped like
// Teams v2's: body, icon, timestamp, tag, requireInteraction, dir).
func (s *Server) NotifyWith(title string, options map[string]any) {
	s.mu.Lock()
	s.pending = append(s.pending, notifySpec{Title: title, Options: options})
	s.mu.Unlock()
}

// AliceIcon is the avatar URL (on origin) that carries Alice's id, as Teams v2 uses it as the icon.
func AliceIcon(origin string) string {
	return origin + "/api/mt/part/zz-01/beta/users/00000000-1111-4222-8333-000000000000/profilepicturev2/8:orgid:00000000-0000-4000-8000-00000000a11c?displayname=Alice"
}

// Reset clears recorded requests and reports, queued notifications and the expiry simulations, so
// the server can be reused across sub-tests.
func (s *Server) Reset() {
	s.mu.Lock()
	s.requests = nil
	s.reports = nil
	s.expire401 = 0
	s.redirect = false
	s.pending = nil
	s.mu.Unlock()
}

// Raised returns, in order, every notification the site reported raising.
func (s *Server) Raised() []Raised {
	var out []Raised
	for _, r := range s.Reports() {
		if r.Type == "raised" {
			out = append(out, Raised{Source: r.Source, Title: r.Title})
		}
	}
	return out
}

// WaitRaised waits up to timeout until the site has raised at least n notifications and returns
// them; the second result is false on timeout.
func (s *Server) WaitRaised(timeout time.Duration, n int) ([]Raised, bool) {
	var got []Raised
	ok := s.wait(timeout, func() bool {
		got = s.Raised()
		return len(got) >= n
	})
	return got, ok
}

// Requests returns a copy of every recorded request.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Reports returns a copy of every /report payload.
func (s *Server) Reports() []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Report(nil), s.reports...)
}

// wait calls cond whenever a request or report is recorded, until it returns true (true) or the
// timeout passes (false).
func (s *Server) wait(timeout time.Duration, cond func() bool) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		ch := s.changed
		s.mu.Unlock()
		if cond() {
			return true
		}
		select {
		case <-ch:
		case <-timer.C:
			return cond()
		}
	}
}

// notifyLocked wakes the waiters; s.mu must be held.
func (s *Server) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// WaitReport waits up to timeout for a /report payload matching pred and returns it; the second
// result is false on timeout.
func (s *Server) WaitReport(timeout time.Duration, pred func(Report) bool) (Report, bool) {
	var found Report
	ok := s.wait(timeout, func() bool {
		for _, r := range s.Reports() {
			if pred(r) {
				found = r
				return true
			}
		}
		return false
	})
	return found, ok
}

// WaitRequest waits up to timeout for a recorded request matching pred and returns it.
func (s *Server) WaitRequest(timeout time.Duration, pred func(Request) bool) (Request, bool) {
	var found Request
	ok := s.wait(timeout, func() bool {
		for _, r := range s.Requests() {
			if pred(r) {
				found = r
				return true
			}
		}
		return false
	})
	return found, ok
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.record(r)

	switch {
	case r.URL.Path == "/report":
		s.handleReport(w, r)
		return
	case r.URL.Path == "/api/v1/poll":
		s.handlePoll(w)
		return
	case r.URL.Path == "/v2/nav-check":
		s.handleNavCheck(w)
		return
	case r.URL.Path == "/login":
		s.serveFile(w, "login.html", "text/html; charset=utf-8", false)
		return
	case r.URL.Path == "/v2/" || r.URL.Path == "/v2":
		s.serveFile(w, "index.html", "text/html; charset=utf-8", true)
		return
	case r.URL.Path == "/v2/app.js":
		s.serveFile(w, "app.js", "text/javascript; charset=utf-8", false)
		return
	case r.URL.Path == "/v2/worker.js":
		s.serveFile(w, "worker.js", "text/javascript; charset=utf-8", false)
		return
	case r.URL.Path == "/api/v1/sw-ping" || r.URL.Path == "/api/v1/worker-ping":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	case r.URL.Path == "/v2/sw.js":
		w.Header().Set("Service-Worker-Allowed", "/v2/")
		s.serveFile(w, "sw.js", "text/javascript; charset=utf-8", false)
		return
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) record(r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Path:              r.URL.Path,
		Host:              r.Host,
		UserAgent:         r.Header.Get("User-Agent"),
		SecCHUA:           r.Header.Get("Sec-CH-UA"),
		SecCHUAMobile:     r.Header.Get("Sec-CH-UA-Mobile"),
		SecCHUAPlatform:   r.Header.Get("Sec-CH-UA-Platform"),
		SecCHUAPlatformV:  r.Header.Get("Sec-CH-UA-Platform-Version"),
		SecCHUAArch:       r.Header.Get("Sec-CH-UA-Arch"),
		SecCHUABitness:    r.Header.Get("Sec-CH-UA-Bitness"),
		SecCHUAFullVerLst: r.Header.Get("Sec-CH-UA-Full-Version-List"),
		SecCHUAModel:      r.Header.Get("Sec-CH-UA-Model"),
		SecCHUAWoW64:      r.Header.Get("Sec-CH-UA-WoW64"),
	})
	s.notifyLocked()
	s.mu.Unlock()
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || r.Method != http.MethodPost {
		http.Error(w, "bad report", http.StatusBadRequest)
		return
	}
	rep := Report{Raw: raw}
	_ = json.Unmarshal(raw, &rep)
	s.mu.Lock()
	s.reports = append(s.reports, rep)
	s.notifyLocked()
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePoll(w http.ResponseWriter) {
	s.mu.Lock()
	expired := s.expire401 > 0
	if expired {
		s.expire401--
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if expired {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"token expired"}`))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleNavCheck answers the page's command poll: queued notifications, then (once) the redirect.
func (s *Server) handleNavCheck(w http.ResponseWriter) {
	s.mu.Lock()
	redirect := s.redirect
	s.redirect = false
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	cmd := map[string]any{}
	if len(pending) > 0 {
		cmd["notify"] = pending
	}
	if redirect {
		cmd["redirect"] = s.LoginURL()
	}
	b, _ := json.Marshal(cmd)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func (s *Server) serveFile(w http.ResponseWriter, name, contentType string, hints bool) {
	b, err := fs.ReadFile(siteFS, "site/"+name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if hints {
		// Ask the browser to send the high-entropy client hints on later same-origin requests.
		w.Header().Set("Accept-CH", acceptCH)
		w.Header().Set("Critical-CH", acceptCH)
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(b)
}
