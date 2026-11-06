// Package fcm sends tbd's sealed payloads as FCM HTTP v1 data messages to a topic (BRIEF §2, §3,
// §5 "FCM"). It implements Google's service-account flow itself, without a Firebase SDK or an
// OAuth library: an RS256-signed JWT assertion is exchanged at the token endpoint for an access
// token, which is cached and sent as a Bearer token to projects.messages.send.
//
// The request body comes from proto.FCMRequest, the single definition of the wire format. The
// HTTP client's Transport is the injection point for tests; package fcmtest provides a mock of
// both Google endpoints.
package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/proto"
)

// DefaultEndpoint is the FCM API base URL (config fcm_endpoint).
const DefaultEndpoint = config.DefaultFCMEndpoint

// Retry defaults, following Google's guidance (see docs/DECISIONS.md).
const (
	DefaultMaxAttempts   = 3
	DefaultMinBackoff    = 10 * time.Second // "wait at least 10 seconds before retrying"
	DefaultMaxBackoff    = 60 * time.Second
	DefaultQuotaBackoff  = 60 * time.Second // 429 without Retry-After: "default to 60 seconds"
	DefaultMaxRetryAfter = 2 * time.Minute  // a longer Retry-After ends the retries
	DefaultTimeout       = 30 * time.Second // per HTTP request; Google asks for at least 10 s
	// MaxTopicLen is the longest topic name FCM accepts.
	MaxTopicLen = 900
)

// Options configures a Client. The zero value is production-ready.
type Options struct {
	// HTTPClient is used for both the token endpoint and FCM. Nil means a client with sane
	// timeouts that honours HTTPS_PROXY and does not follow redirects. Its Transport is the
	// injection point for tests (fcmtest.Server.Transport, or an httptest server's client).
	HTTPClient *http.Client
	// Endpoint is the FCM base URL (config fcm_endpoint); default https://fcm.googleapis.com.
	// Plain http is accepted for loopback hosts only.
	Endpoint string
	// Now is the clock for JWT iat/exp, token caching and Retry-After dates; default time.Now.
	Now func() time.Time
	// Logger receives debug/warn lines; default slog.Default(). Nothing secret is ever logged:
	// no tokens, assertions, topics or payloads.
	Logger *slog.Logger
	// MaxAttempts bounds the sends of one message for 429/5xx/network failures (default 3).
	// The single retry after a 401 with a fresh access token does not count.
	MaxAttempts int
	// Sleep waits between attempts and must return ctx.Err() when ctx ends first. Test hook;
	// default is a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// MinBackoff is the first retry delay after a 5xx or network failure, doubled per retry
	// with up to +25 % jitter (default 10 s).
	MinBackoff time.Duration
	// MaxBackoff caps the computed backoff (default 60 s).
	MaxBackoff time.Duration
	// MaxRetryAfter: a Retry-After longer than this is not waited for; Send returns the error,
	// whose RetryAfter tells the caller when to try again (default 2 min).
	MaxRetryAfter time.Duration
}

// Client sends FCM messages for one service account. It is safe for concurrent use.
type Client struct {
	sa      *ServiceAccount
	hc      *http.Client
	sendURL string
	now     func() time.Time
	log     *slog.Logger
	tokens  *tokenSource

	maxAttempts                           int
	sleep                                 func(context.Context, time.Duration) error
	minBackoff, maxBackoff, maxRetryAfter time.Duration
}

// New returns a client for sa. sa is copied; a ServiceAccount built by hand is validated (and its
// key parsed) exactly like one from ParseServiceAccount.
func New(sa *ServiceAccount, opts Options) (*Client, error) {
	if sa == nil {
		return nil, errors.New("fcm: no service account")
	}
	acct := *sa
	if err := acct.prepare(); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(opts.Endpoint, "/")
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if err := checkURL("fcm_endpoint", endpoint); err != nil {
		return nil, err
	}
	if u, _ := url.Parse(endpoint); u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("fcm: fcm_endpoint must not have a query or fragment")
	}
	c := &Client{
		sa:            &acct,
		hc:            opts.HTTPClient,
		sendURL:       endpoint + "/v1/projects/" + url.PathEscape(acct.ProjectID) + "/messages:send",
		now:           opts.Now,
		log:           opts.Logger,
		maxAttempts:   opts.MaxAttempts,
		sleep:         opts.Sleep,
		minBackoff:    opts.MinBackoff,
		maxBackoff:    opts.MaxBackoff,
		maxRetryAfter: opts.MaxRetryAfter,
	}
	if c.hc == nil {
		c.hc = defaultHTTPClient()
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	if c.maxAttempts <= 0 {
		c.maxAttempts = DefaultMaxAttempts
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	if c.minBackoff <= 0 {
		c.minBackoff = DefaultMinBackoff
	}
	if c.maxBackoff <= 0 {
		c.maxBackoff = DefaultMaxBackoff
	}
	c.maxBackoff = max(c.maxBackoff, c.minBackoff)
	if c.maxRetryAfter <= 0 {
		c.maxRetryAfter = DefaultMaxRetryAfter
	}
	c.tokens = newTokenSource(c.sa, c.hc, c.now, c.log)
	return c, nil
}

// NewFromConfig reads fcm_service_account (a path relative to home, or inline JSON) and
// fcm_endpoint from cfg. opts.Endpoint, when set, wins over the config.
func NewFromConfig(cfg *config.Config, home string, opts Options) (*Client, error) {
	b, err := cfg.ServiceAccountJSON(home)
	if err != nil {
		return nil, err
	}
	sa, err := ParseServiceAccount(b)
	if err != nil {
		return nil, err
	}
	if opts.Endpoint == "" {
		opts.Endpoint = cfg.FCMEndpoint
	}
	return New(sa, opts)
}

func defaultHTTPClient() *http.Client {
	var rt http.RoundTripper = http.DefaultTransport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr := dt.Clone() // keeps ProxyFromEnvironment (HTTPS_PROXY behind corporate proxies)
		tr.TLSHandshakeTimeout = 10 * time.Second
		tr.ResponseHeaderTimeout = 20 * time.Second
		rt = tr
	}
	return &http.Client{
		Transport: rt,
		Timeout:   DefaultTimeout,
		// Google never redirects these calls; following one would resend the bearer token.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ProjectID is the Firebase project messages are sent through.
func (c *Client) ProjectID() string { return c.sa.ProjectID }

// ServiceAccount returns a copy of the account's identity (the key fields are cleared).
func (c *Client) ServiceAccount() ServiceAccount {
	return ServiceAccount{Type: c.sa.Type, ProjectID: c.sa.ProjectID, PrivateKeyID: c.sa.PrivateKeyID,
		ClientEmail: c.sa.ClientEmail, TokenURI: c.sa.TokenURI}
}

// Authenticate obtains (or reuses) an access token, which proves that the service-account key is
// valid, without sending a message. For `tbd status`.
func (c *Client) Authenticate(ctx context.Context) error {
	_, err := c.tokens.token(ctx)
	return err
}

// ValidateTopic checks FCM's topic syntax [a-zA-Z0-9-_.~%]{1,900} (without a "/topics/" prefix).
func ValidateTopic(topic string) error {
	if topic == "" || len(topic) > MaxTopicLen {
		return fmt.Errorf("fcm: %w: topic must be 1..%d characters, got %d", ErrInvalid, MaxTopicLen, len(topic))
	}
	for i := 0; i < len(topic); i++ {
		b := topic[i]
		ok := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' ||
			b == '-' || b == '_' || b == '.' || b == '~' || b == '%'
		if !ok {
			return fmt.Errorf("fcm: %w: topic contains %q at offset %d (allowed: a-z A-Z 0-9 - _ . ~ %%; no /topics/ prefix)", ErrInvalid, b, i)
		}
	}
	return nil
}

// DataSize is the size FCM counts against its data limit: keys plus values.
func DataSize(data string) int { return len("c") + len(data) }

// Send posts one data message {"c": data} to topic. msgType ("msg", "alert", "hb", "pair") selects
// the TTL (21600 s for "hb", 3600 s otherwise); data is the sealed payload from
// proto.EncryptMessage. It returns the message name ("projects/<id>/messages/<id>").
//
// 429, 5xx and network failures are retried up to MaxAttempts times with backoff, honouring
// Retry-After; a 401 makes the client fetch a fresh access token and retry once; other 4xx
// fail at once. Invalid arguments fail before any request with an error matching ErrInvalid.
// Server and network failures are *Error (match them with errors.Is against ErrQuota and
// friends); cancellation returns an error matching the context's error.
func (c *Client) Send(ctx context.Context, topic, data, msgType string) (string, error) {
	if err := ValidateTopic(topic); err != nil {
		return "", err
	}
	switch msgType {
	case proto.TypeMsg, proto.TypeAlert, proto.TypeHB, proto.TypePair:
	default:
		return "", fmt.Errorf("fcm: %w: unknown message type %.20q", ErrInvalid, msgType)
	}
	if data == "" {
		return "", fmt.Errorf("fcm: %w: empty payload", ErrInvalid)
	}
	if n := DataSize(data); n > proto.FCMDataLimit {
		return "", fmt.Errorf("fcm: %w: data payload is %d bytes, FCM allows %d", ErrInvalid, n, proto.FCMDataLimit)
	}
	body := proto.FCMRequest(topic, data, msgType)

	refreshed := false
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("fcm: send: %w", err)
		}
		name, err := c.post(ctx, body)
		if err == nil {
			c.log.Debug("fcm: message sent", "type", msgType, "name", name, "attempt", attempt)
			return name, nil
		}
		var fe *Error
		if !errors.As(err, &fe) {
			return "", err // cancellation or a local failure
		}
		if fe.Op == opSend && fe.HTTPStatus == http.StatusUnauthorized && fe.FCMCode != CodeThirdPartyAuthError && !refreshed {
			// post already dropped the cached token; this retry does not count as an attempt.
			refreshed = true
			attempt--
			c.log.Info("fcm: access token rejected, fetching a new one")
			continue
		}
		if fe.Op == opSend && fe.HTTPStatus == http.StatusUnauthorized && refreshed && fe.Hint == "" {
			fe.Hint = "a freshly issued access token was rejected too; the service account needs the " +
				"\"Firebase Cloud Messaging API Admin\" role (or Firebase Admin) in this project"
		}
		if !fe.Retryable || attempt >= c.maxAttempts {
			if attempt > 1 {
				return "", fmt.Errorf("%w (after %d attempts)", fe, attempt)
			}
			return "", fe
		}
		d := c.delay(attempt, fe)
		if fe.RetryAfter > c.maxRetryAfter {
			c.log.Warn("fcm: server asks to wait longer than max_retry_after, giving up",
				"op", fe.Op, "http", fe.HTTPStatus, "status", fe.Status, "retry_after", fe.RetryAfter)
			return "", fe
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
			return "", fe // the caller's deadline ends before the next attempt could start
		}
		c.log.Warn("fcm: request failed, retrying", "op", fe.Op, "http", fe.HTTPStatus, "status", fe.Status,
			"code", fe.FCMCode, "attempt", attempt, "max_attempts", c.maxAttempts, "delay", d)
		if err := c.sleep(ctx, d); err != nil {
			return "", fmt.Errorf("fcm: send: %w; last error: %w", err, fe)
		}
	}
}

// post makes one token lookup and one send request.
func (c *Client) post(ctx context.Context, body []byte) (string, error) {
	tok, err := c.tokens.token(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.sendURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("fcm: send: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", transportError(ctx, opSend, err)
	}
	b, rerr := readBody(resp, maxResponse)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode == http.StatusUnauthorized {
			c.tokens.invalidate(tok)
		}
		return "", sendError(resp, b, c.now())
	}
	if rerr != nil {
		return "", transportError(ctx, opSend, rerr)
	}
	var r struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Name == "" {
		// Not retried: FCM may have accepted the message.
		return "", &Error{Op: opSend, HTTPStatus: resp.StatusCode, Status: StatusUnknown,
			Message: "response has no message name"}
	}
	return r.Name, nil
}

// delay is the wait before attempt+1: Retry-After when the server sent one, 60 s for a 429
// without it, else exponential backoff with jitter.
func (c *Client) delay(attempt int, fe *Error) time.Duration {
	if fe.RetryAfter > 0 {
		return fe.RetryAfter
	}
	d := c.minBackoff
	for i := 1; i < attempt && d < c.maxBackoff; i++ {
		d *= 2
	}
	d += time.Duration(rand.Int64N(int64(d)/4 + 1))
	d = min(d, c.maxBackoff)
	if fe.Status == StatusResourceExhausted {
		d = max(d, DefaultQuotaBackoff)
	}
	return d
}

// parseRetryAfter reads delay-seconds or an HTTP-date; 0 when absent or unusable. Capped at a day.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	const maxDelay = 24 * time.Hour
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n <= 0 {
			return 0
		}
		return min(time.Duration(min(n, int64(maxDelay/time.Second)))*time.Second, maxDelay)
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return min(d.Round(time.Second), maxDelay)
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
