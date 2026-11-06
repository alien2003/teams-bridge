package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/fcm/fcmtest"
	"github.com/alien2003/teams-bridge/internal/proto"
	"github.com/alien2003/teams-bridge/test/faketeams"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	t      *testing.T
	teams  *faketeams.Server
	fcm    *fcmtest.Server
	home   string
	cfg    *config.Config
	logs   *syncBuffer
	cancel context.CancelFunc
	done   chan error
}

func requireBrowser(t *testing.T) {
	t.Helper()
	if _, err := config.Default().DetectBrowser(); err != nil {
		if os.Getenv("TBD_REQUIRE_BROWSER") == "1" {
			t.Fatalf("no browser: %v", err)
		}
		t.Skipf("no browser: %v", err)
	}
}

// start runs the daemon in-process against faketeams and the FCM mock, with the vectors' secret.
func start(t *testing.T, opts Options) *harness {
	t.Helper()
	requireBrowser(t)
	h := &harness{t: t, teams: faketeams.New(), fcm: fcmtest.NewServer(t, fcmtest.WithSecret(proto.VectorSecret)),
		home: t.TempDir(), logs: &syncBuffer{}, done: make(chan error, 1)}
	t.Cleanup(h.teams.Close)
	cfg := config.Default()
	cfg.Secret = proto.VectorSecret.Code()
	cfg.TeamsURL = h.teams.TeamsURL()
	cfg.LoginHosts = []string{h.teams.LoginHost()}
	cfg.APIHosts = []string{h.teams.APIHostPattern()}
	cfg.LogLevel = "debug"
	h.fcm.SetupHome(h.home, cfg)
	var err error
	h.cfg, _, err = config.Load(h.home)
	if err != nil {
		t.Fatal(err)
	}
	opts.Home, opts.Config = h.home, h.cfg
	opts.Logger = slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- Run(ctx, opts) }()
	t.Cleanup(func() {
		h.stop()
		if t.Failed() {
			t.Logf("daemon log:\n%s", h.logs.String())
		}
	})
	return h
}

func (h *harness) stop() error {
	h.cancel()
	select {
	case err := <-h.done:
		h.done <- err
		return err
	case <-time.After(60 * time.Second):
		h.t.Error("daemon did not stop within 60 s")
		return errors.New("timeout")
	}
}

// waitFor decrypts everything the FCM mock accepted until pred matches.
func (h *harness) waitFor(what string, timeout time.Duration, pred func(proto.Message) bool) proto.Message {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	key := proto.VectorSecret.Key()
	for time.Now().Before(deadline) {
		for _, sent := range h.fcm.Messages() {
			if sent.Topic != proto.VectorSecret.Topic() {
				h.t.Fatalf("message to unexpected topic %q", sent.Topic)
			}
			m, err := proto.DecryptMessage(key, sent.C)
			if err != nil || sent.DecryptErr != nil {
				h.t.Fatalf("payload does not decrypt with the vectors' key: %v / %v", err, sent.DecryptErr)
			}
			if pred(m) {
				return m
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("timed out after %s waiting for %s; got %d message(s)", timeout, what, len(h.fcm.Messages()))
	return proto.Message{}
}

func isMsg(title string) func(proto.Message) bool {
	return func(m proto.Message) bool { return m.T == proto.TypeMsg && m.Title == title }
}

func isAlert(kind string) func(proto.Message) bool {
	return func(m proto.Message) bool { return m.T == proto.TypeAlert && m.Kind == kind }
}

func TestE2ENotificationsReachFCMEncrypted(t *testing.T) {
	h := start(t, Options{ProbeInterval: time.Hour})
	hb := h.waitFor("startup heartbeat", 30*time.Second, func(m proto.Message) bool { return m.T == proto.TypeHB })
	if time.Since(time.Unix(hb.TS, 0)) > time.Minute {
		t.Errorf("heartbeat ts %d is not now", hb.TS)
	}
	alice := h.waitFor("Alice (window.Notification)", 60*time.Second, isMsg("Alice"))
	if alice.Body != "hi" || alice.Tag != "19:abc@thread.v2" {
		t.Errorf("Alice: %+v", alice)
	}
	if !strings.HasPrefix(alice.URL, h.teams.Origin()+"/") {
		t.Errorf("deep link %q is not on the Teams origin %s", alice.URL, h.teams.Origin())
	}
	h.waitFor("Bob (ServiceWorkerRegistration.showNotification from the page)", 60*time.Second, isMsg("Bob"))
	for _, s := range h.fcm.Messages() {
		if strings.Contains(s.C, "Alice") || strings.Contains(s.C, "hi") && len(s.C) < 100 {
			t.Fatal("clear text in the FCM payload")
		}
	}
	// The info line carries the tag's shape and the option names, never the tag itself.
	if logs := h.logs.String(); !strings.Contains(logs, "tag_shape=99:aaa@aaaaaa.a9") || !strings.Contains(logs, "options=body,") {
		t.Errorf("structure-only fields missing from the capture log line")
	}
	// BRIEF §5: titles and bodies only at debug level.
	for _, ln := range strings.Split(h.logs.String(), "\n") {
		if !strings.Contains(ln, "level=DEBUG") && (strings.Contains(ln, "Alice") || strings.Contains(ln, "body=hi") || strings.Contains(ln, "from sw-") || strings.Contains(ln, "19:abc@thread.v2")) {
			t.Errorf("notification text logged above debug level: %s", ln)
		}
	}
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.waitFor("daemon_stopping on shutdown", 5*time.Second, isAlert(proto.KindDaemonStopping))
	st := LoadState(h.home)
	if st.Messages < 2 || st.LastHeartbeat.IsZero() {
		t.Errorf("state not updated: %+v", st)
	}
}

func TestE2EServiceWorkerSideHook(t *testing.T) {
	h := start(t, Options{ProbeInterval: time.Hour})
	// Best effort per the brief: the worker-side wrapper must work with the bundled Edge.
	h.waitFor("Carol (self.registration.showNotification inside the service worker)", 60*time.Second, isMsg("Carol"))
}

func TestE2ESessionExpiredBy401Burst(t *testing.T) {
	h := start(t, Options{ProbeInterval: time.Hour})
	h.waitFor("Alice", 60*time.Second, isMsg("Alice"))
	h.teams.Expire401(10)
	a := h.waitFor("session_expired after 401 burst", 30*time.Second, isAlert(proto.KindSessionExpired))
	if a.Title == "" || a.Body == "" {
		t.Errorf("alert without text: %+v", a)
	}
	// Debounced: a second burst within 6 h does not alert again.
	h.teams.Expire401(10)
	time.Sleep(5 * time.Second)
	n := 0
	for _, s := range h.fcm.Messages() {
		if m, err := proto.DecryptMessage(proto.VectorSecret.Key(), s.C); err == nil && isAlert(proto.KindSessionExpired)(m) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d session_expired alerts, want exactly 1 (debounced)", n)
	}
}

func TestE2ESessionExpiredByLoginRedirect(t *testing.T) {
	h := start(t, Options{LoginGrace: time.Second, ProbeInterval: 2 * time.Second, FirstProbe: 2 * time.Second})
	h.waitFor("Alice", 60*time.Second, isMsg("Alice"))
	h.teams.RedirectToLogin()
	h.waitFor("session_expired after redirect to the login host", 30*time.Second, isAlert(proto.KindSessionExpired))
}

func TestE2EOnceMode(t *testing.T) {
	h := start(t, Options{Once: true, Timeout: 90 * time.Second, ProbeInterval: time.Hour})
	select {
	case err := <-h.done:
		h.done <- err
		if err != nil {
			t.Fatalf("Run --once: %v", err)
		}
	case <-time.After(100 * time.Second):
		t.Fatal("--once did not exit")
	}
	h.waitFor("one notification", time.Second, func(m proto.Message) bool { return m.T == proto.TypeMsg })
	for _, s := range h.fcm.Messages() {
		if m, _ := proto.DecryptMessage(proto.VectorSecret.Key(), s.C); m.T == proto.TypeHB || m.Kind == proto.KindDaemonStopping {
			t.Errorf("--once sent %s/%s", m.T, m.Kind)
		}
	}
}

func TestSingleInstanceLock(t *testing.T) {
	home := t.TempDir()
	l, err := AcquireLock(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	if _, err := AcquireLock(home); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	if held, pid := Held(home); !held || pid != strconv.Itoa(os.Getpid()) {
		t.Fatalf("Held = %v %q", held, pid)
	}
	l.Release()
	l2, err := AcquireLock(home)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l2.Release()
}
