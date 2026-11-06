// Package fcmtest is an in-process fake of Google's OAuth 2.0 token endpoint and of the FCM HTTP v1
// send API, for tests of internal/fcm and of everything that sends through it.
//
// One httptest server serves both POST /token and POST /v1/projects/{id}/messages:send. It
// validates requests the way Google does and, where tbd's protocol (BRIEF §3) allows only one
// shape, more strictly: the JWT assertion (structure, header, claims, lifetime, RS256 signature
// against the registered key), Bearer tokens, and the exact send body. Failures are answered with
// Google's error JSON and can be scripted (FailNext, FailTopic, FailNextToken, ExpireTokens).
// Accepted messages are recorded and can be awaited (WaitFor).
//
// The package does not depend on internal/fcm: it is an independent implementation of the
// server side, so a bug in the client cannot hide behind the same bug in the mock. Private keys
// are generated at run time (NewServiceAccount); none is ever stored in the repository.
package fcmtest

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/proto"
)

// Constants of the emulated services.
const (
	DefaultProjectID = "tbd-test-project"
	// DefaultTokenTTL is expires_in of issued access tokens (Google issues 3599 s).
	DefaultTokenTTL = 3599 * time.Second
	ScopeFCM        = "https://www.googleapis.com/auth/firebase.messaging"
	ScopeCloud      = "https://www.googleapis.com/auth/cloud-platform"
	GrantType       = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	// GoogleTokenURL and GoogleFCMHost are the production endpoints; Transport serves them in memory.
	GoogleTokenURL = "https://oauth2.googleapis.com/token"
	GoogleFCMHost  = "fcm.googleapis.com"
	FCMErrorType   = "type.googleapis.com/google.firebase.fcm.v1.FcmError"
	// MaxJWTLifetime is the longest exp-iat Google accepts.
	MaxJWTLifetime = time.Hour
	// ClockSkew is how far in the future iat may be.
	ClockSkew = time.Minute
)

// Message is an accepted send request.
type Message struct {
	Name      string // projects/<id>/messages/<n>, as returned to the client
	Topic     string
	C         string // data["c"]
	Priority  string // android.priority
	TTL       string // android.ttl
	Heartbeat bool   // TTL is 21600s
	// Type is the decrypted "t" when the server knows the key (SetSecret/SetKey); otherwise it is
	// inferred from the TTL: "hb" for 21600s, "msg" for any other type.
	Type string
	// Decrypted is the decrypted message when the key is known and decryption succeeded.
	Decrypted *proto.Message
	// DecryptErr is set when the key is known but "c" does not decrypt, or the decrypted type does
	// not match the TTL that BRIEF §3 prescribes for it. Real FCM cannot see either, so the
	// message is still accepted; tests should assert DecryptErr == nil.
	DecryptErr error
	SealedLen  int    // len(base64-decoded c): 1052 or 2076
	Body       []byte // the raw request body
	Received   time.Time
}

// Stats counts requests.
type Stats struct {
	TokenRequests int // POST /token, including rejected ones
	TokensIssued  int
	SendRequests  int // POST …/messages:send, including rejected ones
	Accepted      int
}

// Failure is a scripted error response of the send endpoint. Zero fields get realistic defaults
// for HTTPStatus (see DefaultFailure).
type Failure struct {
	HTTPStatus int
	Status     string // google.rpc status, e.g. RESOURCE_EXHAUSTED
	ErrorCode  string // FcmError errorCode, e.g. QUOTA_EXCEEDED; "-" for no FcmError detail
	Message    string
	RetryAfter string // Retry-After header value (seconds or an HTTP-date)
	// RawBody, when set, is sent verbatim as text/html instead of Google JSON (a proxy or
	// front-end error page).
	RawBody string
}

// TokenFailure is a scripted error response of the token endpoint.
type TokenFailure struct {
	HTTPStatus  int    // default 400
	Error       string // e.g. invalid_grant
	Description string // e.g. Invalid JWT Signature.
	RetryAfter  string
}

type account struct {
	email, kid, tokenURI string
	pub                  *rsa.PublicKey
}

type grant struct {
	email string
	exp   time.Time
}

// Server is the fake. Create it with NewServer; it is closed when the test ends.
type Server struct {
	// URL is the base URL ("http://127.0.0.1:port"): config fcm_endpoint / fcm.Options.Endpoint.
	URL string
	// ProjectID is the only project the send endpoint knows.
	ProjectID string

	t       testing.TB
	hs      *httptest.Server
	handler http.Handler

	mu         sync.Mutex
	now        func() time.Time
	tokenTTL   time.Duration
	key        *[proto.KeySize]byte
	dataLimit  int
	accounts   map[string]*account
	tokens     map[string]grant
	issued     []string // every access token ever issued, valid or not
	sendFails  []Failure
	tokenFails []TokenFailure
	topicFails map[string]Failure
	msgs       []Message
	changed    chan struct{}
	stats      Stats
	rejections []string
	nAccounts  int
}

// Option configures NewServer.
type Option func(*Server)

// WithProjectID sets the project id (default DefaultProjectID).
func WithProjectID(id string) Option { return func(s *Server) { s.ProjectID = id } }

// WithSecret lets the server decrypt accepted messages with the pairing secret's key.
func WithSecret(sec proto.Secret) Option {
	return func(s *Server) { k := sec.Key(); s.key = &k }
}

// WithClock sets the server's clock (JWT validation, token expiry, Received).
func WithClock(now func() time.Time) Option { return func(s *Server) { s.now = now } }

// NewServer starts the fake on 127.0.0.1 and closes it in t.Cleanup.
func NewServer(t testing.TB, opts ...Option) *Server {
	t.Helper()
	s := &Server{
		ProjectID:  DefaultProjectID,
		t:          t,
		now:        time.Now,
		tokenTTL:   DefaultTokenTTL,
		accounts:   map[string]*account{},
		tokens:     map[string]grant{},
		topicFails: map[string]Failure{},
		changed:    make(chan struct{}),
	}
	for _, o := range opts {
		o(s)
	}
	s.handler = http.HandlerFunc(s.serve)
	s.hs = httptest.NewServer(s.handler)
	s.URL = s.hs.URL
	t.Cleanup(s.hs.Close)
	return s
}

// TokenURL is the token endpoint ("<URL>/token"), the token_uri of NewServiceAccount accounts.
func (s *Server) TokenURL() string { return s.URL + "/token" }

// Client returns an http.Client for the server (plain HTTP on loopback).
func (s *Server) Client() *http.Client { return s.hs.Client() }

// SetNow replaces the server's clock.
func (s *Server) SetNow(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// SetTokenTTL sets expires_in (and the real lifetime) of tokens issued from now on.
func (s *Server) SetTokenTTL(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenTTL = d
}

// SetSecret makes the server decrypt accepted messages with sec's key.
func (s *Server) SetSecret(sec proto.Secret) { s.SetKey(sec.Key()) }

// SetKey makes the server decrypt accepted messages with k.
func (s *Server) SetKey(k [proto.KeySize]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.key = &k
}

// SetTopicDataLimit makes the send endpoint reject payloads whose data (keys plus values) exceeds
// n bytes with 400 INVALID_ARGUMENT. FCM's error reference documents 2048 for topic messages;
// 0 (default) applies only the general 4096-byte limit.
func (s *Server) SetTopicDataLimit(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dataLimit = n
}

// FailNext makes the next n send requests that pass authentication and validation fail with
// status and Google's default error body for it (DefaultFailure).
func (s *Server) FailNext(status, n int) { s.FailNextWith(n, Failure{HTTPStatus: status}) }

// FailNextRetryAfter is FailNext with a Retry-After header of d (whole seconds).
func (s *Server) FailNextRetryAfter(status, n int, d time.Duration) {
	s.FailNextWith(n, Failure{HTTPStatus: status, RetryAfter: strconv.FormatInt(int64(d/time.Second), 10)})
}

// FailNextWith queues n copies of f.
func (s *Server) FailNextWith(n int, f Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for range n {
		s.sendFails = append(s.sendFails, f)
	}
}

// FailTopic makes every send to topic fail with status (e.g. 404) until UnfailTopic.
func (s *Server) FailTopic(topic string, status int) {
	s.FailTopicWith(topic, Failure{HTTPStatus: status})
}

// FailTopicWith makes every send to topic fail with f until UnfailTopic.
func (s *Server) FailTopicWith(topic string, f Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.topicFails[topic] = f
}

// UnfailTopic removes FailTopic's failure.
func (s *Server) UnfailTopic(topic string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.topicFails, topic)
}

// FailNextToken makes the next n token requests fail with f (default 400 invalid_grant).
func (s *Server) FailNextToken(n int, f TokenFailure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for range n {
		s.tokenFails = append(s.tokenFails, f)
	}
}

// ExpireTokens invalidates every access token issued so far; sends with them get 401.
func (s *Server) ExpireTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.tokens)
}

// IssuedTokens returns every access token issued so far, including expired and invalidated ones
// (ExpireTokens), oldest first, so a test can assert that none of them leaks into logs or errors.
func (s *Server) IssuedTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.issued)
}

// Stats returns the request counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Rejections returns why requests were rejected (validation and authentication failures; not
// scripted failures), oldest first.
func (s *Server) Rejections() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.rejections)
}

// Messages returns the accepted messages, oldest first.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.msgs)
}

// WaitFor blocks until at least n messages were accepted and returns all of them, or returns what
// has arrived together with ctx's error.
func (s *Server) WaitFor(ctx context.Context, n int) ([]Message, error) {
	for {
		s.mu.Lock()
		if len(s.msgs) >= n {
			out := slices.Clone(s.msgs)
			s.mu.Unlock()
			return out, nil
		}
		ch, out := s.changed, slices.Clone(s.msgs)
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return out, fmt.Errorf("fcmtest: %d of %d messages arrived: %w", len(out), n, ctx.Err())
		}
	}
}

// MustWaitFor is WaitFor with a timeout that fails the test (call it from the test goroutine).
func (s *Server) MustWaitFor(n int, timeout time.Duration) []Message {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	msgs, err := s.WaitFor(ctx, n)
	if err != nil {
		s.t.Fatalf("%v; rejections: %q", err, s.Rejections())
	}
	return msgs
}

func (s *Server) reject(format string, args ...any) string {
	r := fmt.Sprintf(format, args...)
	s.rejections = append(s.rejections, r)
	return r
}

func (s *Server) record(m Message) {
	s.msgs = append(s.msgs, m)
	s.stats.Accepted++
	close(s.changed)
	s.changed = make(chan struct{})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "mock." + base64.RawURLEncoding.EncodeToString(b)
}

func randomMessageID() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	return n.String()
}
