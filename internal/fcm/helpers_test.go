package fcm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/fcm/fcmtest"
	"github.com/alien2003/teams-bridge/internal/proto"
)

// clock is a manually advanced clock shared by the client and the mock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// sleeper records the requested backoff delays instead of sleeping; it also advances the clock.
type sleeper struct {
	mu  sync.Mutex
	d   []time.Duration
	clk *clock
}

func (s *sleeper) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	if s.clk != nil {
		s.clk.Advance(d)
	}
	return nil
}

func (s *sleeper) delays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

// syncBuffer collects log output from concurrent goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type env struct {
	t      *testing.T
	srv    *fcmtest.Server
	clk    *clock
	sl     *sleeper
	logs   *syncBuffer
	secret proto.Secret
	c      *Client
	saJSON []byte
}

// newEnv starts a mock with a fresh account and a client whose clock, sleep and logger are fakes.
func newEnv(t *testing.T, mod func(*Options)) *env {
	t.Helper()
	e := &env{t: t, clk: newClock(), logs: &syncBuffer{}}
	e.sl = &sleeper{clk: e.clk}
	sec, err := proto.NewSecret(nil)
	if err != nil {
		t.Fatal(err)
	}
	e.secret = sec
	e.srv = fcmtest.NewServer(t, fcmtest.WithClock(e.clk.Now), fcmtest.WithSecret(sec))
	e.saJSON = e.srv.NewServiceAccount()
	sa, err := ParseServiceAccount(e.saJSON)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		HTTPClient: e.srv.Client(),
		Endpoint:   e.srv.URL,
		Now:        e.clk.Now,
		Logger:     slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Sleep:      e.sl.sleep,
	}
	if mod != nil {
		mod(&opts)
	}
	e.c, err = New(sa, opts)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// seal encrypts a message of type typ for the env's secret.
func (e *env) seal(typ, body string) proto.Sealed {
	e.t.Helper()
	id, err := proto.NewID(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	m := proto.Message{V: proto.Version, T: typ, ID: id, TS: e.clk.Now().Unix(), Title: "Alice", Body: body,
		URL: "https://teams.microsoft.com/v2/", Tag: "19:abc@thread"}
	if typ == proto.TypeAlert {
		m.Kind = proto.KindSessionExpired
	}
	s, err := proto.EncryptMessage(e.secret.Key(), m, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) send(typ string) (string, error) {
	return e.c.Send(context.Background(), e.secret.Topic(), e.seal(typ, "hi").C, typ)
}

func (e *env) mustSend(typ string) string {
	e.t.Helper()
	name, err := e.send(typ)
	if err != nil {
		e.t.Fatalf("send %s: %v (mock rejections: %q)", typ, err, e.srv.Rejections())
	}
	return name
}

// checkNoSecrets fails if s contains anything secret: access tokens, assertions, PEM, topic.
func (e *env) checkNoSecrets(what, s string) {
	e.t.Helper()
	bad := []string{"-----", "eyJhbGci", e.secret.Topic()}
	bad = append(bad, e.srv.IssuedTokens()...)
	for _, b := range bad {
		if b != "" && strings.Contains(s, b) {
			e.t.Errorf("%s contains a secret (%.12s…): %s", what, b, s)
		}
	}
}

// rtFunc adapts a function to http.RoundTripper.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var (
	keyOnce sync.Once
	key2048 *rsa.PrivateKey
)

// testKey is one RSA-2048 key per test process, generated at run time (never stored).
func testKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		key2048 = k
	})
	return key2048
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
