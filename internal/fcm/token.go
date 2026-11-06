package fcm

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Scope is the OAuth scope for sending FCM messages.
const Scope = "https://www.googleapis.com/auth/firebase.messaging"

// GrantType is the OAuth 2.0 JWT bearer grant (RFC 7523) used by service accounts.
const GrantType = "urn:ietf:params:oauth:grant-type:jwt-bearer"

// AssertionLifetime is exp-iat of the JWT; Google's maximum is one hour.
const AssertionLifetime = time.Hour

const (
	// refreshMargin: a cached access token is replaced this long before it expires.
	refreshMargin = 5 * time.Minute
	// defaultTokenLifetime is assumed when the token response has no expires_in.
	defaultTokenLifetime = 10 * time.Minute
	// maxTokenLifetime caps expires_in (seconds); Google's tokens live 3599 s.
	maxTokenLifetime = 24 * 3600
	maxTokenResponse = 64 << 10
)

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Iss   string `json:"iss"`
	Scope string `json:"scope"`
	Aud   string `json:"aud"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
}

var b64url = base64.RawURLEncoding

// assertion builds the RS256-signed JWT that is exchanged for an access token.
func (sa *ServiceAccount) assertion(now time.Time) (string, error) {
	h, err := json.Marshal(jwtHeader{Alg: "RS256", Typ: "JWT", Kid: sa.PrivateKeyID})
	if err != nil {
		return "", err
	}
	iat := now.Unix()
	c, err := json.Marshal(jwtClaims{Iss: sa.ClientEmail, Scope: Scope, Aud: sa.TokenURI,
		Iat: iat, Exp: iat + int64(AssertionLifetime/time.Second)})
	if err != nil {
		return "", err
	}
	signing := b64url.EncodeToString(h) + "." + b64url.EncodeToString(c)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, sa.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("fcm: signing the JWT assertion failed")
	}
	return signing + "." + b64url.EncodeToString(sig), nil
}

// tokenSource exchanges assertions for access tokens and caches them. Safe for concurrent use:
// concurrent callers wait for one exchange (context-aware) instead of each starting their own.
type tokenSource struct {
	sa  *ServiceAccount
	hc  *http.Client
	now func() time.Time
	log *slog.Logger

	sem chan struct{} // one exchange at a time

	mu        sync.Mutex
	tok       string
	refreshAt time.Time
}

func newTokenSource(sa *ServiceAccount, hc *http.Client, now func() time.Time, log *slog.Logger) *tokenSource {
	return &tokenSource{sa: sa, hc: hc, now: now, log: log, sem: make(chan struct{}, 1)}
}

func (ts *tokenSource) cached() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.tok != "" && ts.now().Before(ts.refreshAt) {
		return ts.tok
	}
	return ""
}

// invalidate drops tok if it is still the cached token (FCM answered 401 for it).
func (ts *tokenSource) invalidate(tok string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.tok == tok {
		ts.tok = ""
	}
}

// token returns a valid access token, exchanging a new assertion when needed.
func (ts *tokenSource) token(ctx context.Context) (string, error) {
	if t := ts.cached(); t != "" {
		return t, nil
	}
	select {
	case ts.sem <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("fcm: token: %w", ctx.Err())
	}
	defer func() { <-ts.sem }()
	if t := ts.cached(); t != "" { // another caller refreshed it while we waited
		return t, nil
	}
	start := ts.now()
	tok, lifetime, err := ts.exchange(ctx, start)
	if err != nil {
		return "", err
	}
	margin := refreshMargin
	if lifetime <= 2*margin {
		margin = lifetime / 2
	}
	ts.mu.Lock()
	// Round(0) drops the monotonic reading, so the comparison in cached() uses the wall clock, as
	// Google's expiry does. Linux's monotonic clock stops while the machine (or the WSL VM) is
	// suspended; with it, a token would look fresh after a resume and cost a 401 round trip.
	ts.tok, ts.refreshAt = tok, start.Add(lifetime-margin).Round(0)
	ts.mu.Unlock()
	ts.log.Debug("fcm: access token obtained", "expires_in", lifetime, "refresh_in", lifetime-margin)
	return tok, nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

func (ts *tokenSource) exchange(ctx context.Context, now time.Time) (string, time.Duration, error) {
	a, err := ts.sa.assertion(now)
	if err != nil {
		return "", 0, err
	}
	form := url.Values{"grant_type": {GrantType}, "assertion": {a}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.sa.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("fcm: token: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := ts.hc.Do(req)
	if err != nil {
		return "", 0, transportError(ctx, opToken, err)
	}
	body, rerr := readBody(resp, maxTokenResponse)
	if resp.StatusCode != http.StatusOK {
		return "", 0, tokenError(resp, body, now)
	}
	if rerr != nil {
		return "", 0, transportError(ctx, opToken, rerr)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return "", 0, &Error{Op: opToken, HTTPStatus: resp.StatusCode, Status: StatusUnknown,
			Message: "malformed token response (no access_token)"}
	}
	if tr.TokenType != "" && !strings.EqualFold(tr.TokenType, "Bearer") {
		return "", 0, &Error{Op: opToken, HTTPStatus: resp.StatusCode, Status: StatusUnknown,
			Message: fmt.Sprintf("token_type %.20q, want Bearer", tr.TokenType)}
	}
	lifetime := defaultTokenLifetime
	if tr.ExpiresIn > 0 {
		lifetime = time.Duration(min(tr.ExpiresIn, maxTokenLifetime)) * time.Second
	}
	return tr.AccessToken, lifetime, nil
}
