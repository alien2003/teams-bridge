package fcm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/fcm/fcmtest"
	"github.com/alien2003/teams-bridge/internal/proto"
)

var nameRE = regexp.MustCompile(`^projects/` + fcmtest.DefaultProjectID + `/messages/[0-9]+$`)

func TestSendEveryTypeBodyMatchesProto(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	long := strings.Repeat("ü", 1500) // forces the 2048 padding
	cases := []struct {
		typ, body, ttl string
		sealedLen      int
	}{
		{proto.TypeMsg, "hi", "3600s", 1052},
		{proto.TypeAlert, "session expired", "3600s", 1052},
		{proto.TypeHB, "", "21600s", 1052},
		{proto.TypePair, "", "3600s", 1052},
		{proto.TypeMsg, long, "3600s", 2076},
	}
	for i, tc := range cases {
		sealed := e.seal(tc.typ, tc.body)
		name, err := e.c.Send(context.Background(), e.secret.Topic(), sealed.C, tc.typ)
		if err != nil {
			t.Fatalf("%s: %v (%q)", tc.typ, err, e.srv.Rejections())
		}
		msgs := e.srv.Messages()
		if len(msgs) != i+1 {
			t.Fatalf("mock recorded %d messages, want %d", len(msgs), i+1)
		}
		got := msgs[i]
		if !nameRE.MatchString(name) || got.Name != name {
			t.Errorf("%s: name %q (mock %q)", tc.typ, name, got.Name)
		}
		if want := proto.FCMRequest(e.secret.Topic(), sealed.C, tc.typ); !bytes.Equal(got.Body, want) {
			t.Errorf("%s: body\n%s\nwant proto.FCMRequest\n%s", tc.typ, got.Body, want)
		}
		if got.Topic != e.secret.Topic() || got.TTL != tc.ttl || got.Priority != "high" || got.SealedLen != tc.sealedLen {
			t.Errorf("%s: topic/ttl/priority/len = %s/%s/%s/%d", tc.typ, got.Topic, got.TTL, got.Priority, got.SealedLen)
		}
		if got.DecryptErr != nil || got.Decrypted == nil || *got.Decrypted != sealed.Sent || got.Type != tc.typ {
			t.Errorf("%s: mock-side decryption: %+v, %v", tc.typ, got.Decrypted, got.DecryptErr)
		}
	}
	if r := e.srv.Rejections(); len(r) != 0 {
		t.Errorf("mock rejected requests: %q", r)
	}
}

// The shared vectors' payloads go out exactly as the vectors' fcm_request.
func TestSendVectors(t *testing.T) {
	t.Parallel()
	v := proto.GenerateVectors()
	e := newEnv(t, nil)
	e.srv.SetSecret(proto.VectorSecret)
	for _, mv := range v.Messages {
		if _, err := e.c.Send(context.Background(), v.Topic, mv.SealedB64, mv.Type); err != nil {
			t.Fatalf("%s: %v", mv.Name, err)
		}
	}
	for i, m := range e.srv.Messages() {
		mv := v.Messages[i]
		if !bytes.Equal(m.Body, mv.FCMRequest) {
			t.Errorf("%s: body %s, want vector %s", mv.Name, m.Body, mv.FCMRequest)
		}
		if m.DecryptErr != nil || m.Decrypted == nil || m.Decrypted.T != mv.Type {
			t.Errorf("%s: mock decryption: %v", mv.Name, m.DecryptErr)
		}
	}
}

func TestTokenExchangeAndCaching(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	if err := e.c.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		e.mustSend(proto.TypeMsg)
	}
	if st := e.srv.Stats(); st.TokenRequests != 1 || st.TokensIssued != 1 || st.SendRequests != 3 || st.Accepted != 3 {
		t.Errorf("stats %+v, want 1 token for 3 sends", st)
	}
	// Google's tokens live 3599 s; the client renews 5 min early.
	e.clk.Advance(50 * time.Minute)
	e.mustSend(proto.TypeMsg)
	if st := e.srv.Stats(); st.TokenRequests != 1 {
		t.Errorf("token renewed too early: %+v", st)
	}
	e.clk.Advance(5 * time.Minute)
	e.mustSend(proto.TypeMsg)
	if st := e.srv.Stats(); st.TokenRequests != 2 || st.SendRequests != 5 {
		t.Errorf("token not renewed before expiry: %+v", st)
	}
}

func TestShortLivedToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	e.srv.SetTokenTTL(60 * time.Second) // renewed at half its lifetime
	e.mustSend(proto.TypeMsg)
	e.clk.Advance(29 * time.Second)
	e.mustSend(proto.TypeMsg)
	e.clk.Advance(2 * time.Second)
	e.mustSend(proto.TypeMsg)
	if st := e.srv.Stats(); st.TokenRequests != 2 {
		t.Errorf("stats %+v, want 2 token requests", st)
	}
}

func TestRefreshOn401(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	e.mustSend(proto.TypeMsg)
	e.srv.ExpireTokens() // the cached token is now unknown to the server
	e.mustSend(proto.TypeMsg)
	if st := e.srv.Stats(); st.TokenRequests != 2 || st.SendRequests != 3 || st.Accepted != 2 {
		t.Errorf("stats %+v, want a token refresh and one resend", st)
	}
	if d := e.sl.delays(); len(d) != 0 {
		t.Errorf("a 401 refresh must not back off, slept %v", d)
	}

	// A scripted 401 is handled the same way, and the retry does not consume MaxAttempts.
	e2 := newEnv(t, func(o *Options) { o.MaxAttempts = 1 })
	e2.mustSend(proto.TypeMsg)
	e2.srv.FailNext(http.StatusUnauthorized, 1)
	e2.mustSend(proto.TypeMsg)
	if st := e2.srv.Stats(); st.TokenRequests != 2 || st.SendRequests != 3 {
		t.Errorf("stats %+v", st)
	}
}

func TestPersistent401(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	e.srv.FailNext(http.StatusUnauthorized, 10)
	_, err := e.send(proto.TypeMsg)
	var fe *Error
	if !errors.As(err, &fe) || !errors.Is(err, ErrUnauthenticated) || fe.HTTPStatus != 401 || fe.Status != StatusUnauthenticated {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(fe.Hint, "role") {
		t.Errorf("a 401 with a fresh token should hint at the IAM role: %q", fe.Hint)
	}
	if st := e.srv.Stats(); st.TokenRequests != 2 || st.SendRequests != 2 {
		t.Errorf("stats %+v, want exactly one refresh and one resend", st)
	}
	e.checkNoSecrets("error", err.Error())

	// THIRD_PARTY_AUTH_ERROR (APNs/web push credentials) is not fixed by a new token.
	e2 := newEnv(t, nil)
	e2.srv.FailNextWith(1, fcmtest.Failure{HTTPStatus: 401, ErrorCode: CodeThirdPartyAuthError, Message: "Auth error from APNS or Web Push Service"})
	_, err = e2.send(proto.TypeMsg)
	if !errors.As(err, &fe) || fe.FCMCode != CodeThirdPartyAuthError || !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("got %v", err)
	}
	if st := e2.srv.Stats(); st.TokenRequests != 1 || st.SendRequests != 1 {
		t.Errorf("stats %+v", st)
	}
}

func TestErrorMapping(t *testing.T) {
	t.Parallel()
	sentinels := []error{ErrUnauthenticated, ErrNotFound, ErrQuota, ErrInvalid, ErrUnavailable, ErrPermission}
	cases := []struct {
		f         fcmtest.Failure
		want      error
		status    string
		code      string
		retryable bool
		sends     int
	}{
		{fcmtest.Failure{HTTPStatus: 400}, ErrInvalid, StatusInvalidArgument, CodeInvalidArgument, false, 1},
		{fcmtest.Failure{HTTPStatus: 403}, ErrPermission, StatusPermissionDenied, CodeSenderIDMismatch, false, 1},
		{fcmtest.Failure{HTTPStatus: 403, ErrorCode: "-", Message: "Firebase Cloud Messaging API has not been used in project 1 before or it is disabled."},
			ErrPermission, StatusPermissionDenied, "", false, 1},
		{fcmtest.Failure{HTTPStatus: 404}, ErrNotFound, StatusNotFound, CodeUnregistered, false, 1},
		{fcmtest.Failure{HTTPStatus: 429}, ErrQuota, StatusResourceExhausted, CodeQuotaExceeded, true, 3},
		{fcmtest.Failure{HTTPStatus: 500}, ErrUnavailable, StatusInternal, CodeInternal, true, 3},
		{fcmtest.Failure{HTTPStatus: 503}, ErrUnavailable, StatusUnavailable, CodeUnavailable, true, 3},
		{fcmtest.Failure{HTTPStatus: 502}, ErrUnavailable, StatusUnavailable, "", true, 3}, // HTML from the front end
		{fcmtest.Failure{HTTPStatus: 504}, ErrUnavailable, StatusDeadlineExceeded, "", true, 3},
	}
	for _, tc := range cases {
		e := newEnv(t, nil)
		e.srv.FailNextWith(10, tc.f)
		_, err := e.send(proto.TypeMsg)
		var fe *Error
		if !errors.As(err, &fe) {
			t.Fatalf("%d: not an *Error: %v", tc.f.HTTPStatus, err)
		}
		if fe.Op != opSend || fe.HTTPStatus != tc.f.HTTPStatus || fe.Status != tc.status || fe.FCMCode != tc.code || fe.Retryable != tc.retryable {
			t.Errorf("%d: got op=%s http=%d status=%s code=%s retryable=%v", tc.f.HTTPStatus, fe.Op, fe.HTTPStatus, fe.Status, fe.FCMCode, fe.Retryable)
		}
		for _, s := range sentinels {
			if errors.Is(err, s) != (s == tc.want) {
				t.Errorf("%d: errors.Is(%v) = %v", tc.f.HTTPStatus, s, !(s == tc.want))
			}
		}
		if st := e.srv.Stats(); st.SendRequests != tc.sends || st.TokenRequests != 1 {
			t.Errorf("%d: stats %+v, want %d send requests", tc.f.HTTPStatus, st, tc.sends)
		}
		if got := len(e.sl.delays()); got != tc.sends-1 {
			t.Errorf("%d: slept %d times", tc.f.HTTPStatus, got)
		}
		e.checkNoSecrets("error", err.Error())
	}
}

func TestFailTopic(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	e.srv.FailTopic(e.secret.Topic(), http.StatusNotFound)
	if _, err := e.send(proto.TypeMsg); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	e.srv.UnfailTopic(e.secret.Topic())
	e.mustSend(proto.TypeMsg)
}

func TestRetryAfterHonoured(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	e.srv.FailNextRetryAfter(http.StatusTooManyRequests, 2, 7*time.Second)
	e.mustSend(proto.TypeMsg)
	if d := e.sl.delays(); len(d) != 2 || d[0] != 7*time.Second || d[1] != 7*time.Second {
		t.Errorf("delays %v, want [7s 7s]", d)
	}

	// HTTP-date form, relative to the client's clock.
	e = newEnv(t, nil)
	at := e.clk.Now().Add(90 * time.Second).Format(http.TimeFormat)
	e.srv.FailNextWith(1, fcmtest.Failure{HTTPStatus: 503, RetryAfter: at})
	e.mustSend(proto.TypeMsg)
	if d := e.sl.delays(); len(d) != 1 || d[0] != 90*time.Second {
		t.Errorf("delays %v, want [1m30s]", d)
	}

	// Longer than MaxRetryAfter: no retry, the caller gets RetryAfter.
	e = newEnv(t, nil)
	e.srv.FailNextRetryAfter(http.StatusTooManyRequests, 1, time.Hour)
	_, err := e.send(proto.TypeMsg)
	var fe *Error
	if !errors.As(err, &fe) || !errors.Is(err, ErrQuota) || fe.RetryAfter != time.Hour || !fe.Retryable {
		t.Fatalf("got %v", err)
	}
	if st := e.srv.Stats(); st.SendRequests != 1 || len(e.sl.delays()) != 0 {
		t.Errorf("must not wait an hour: %+v %v", st, e.sl.delays())
	}
}

func TestBackoff(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	e.srv.FailNext(http.StatusServiceUnavailable, 2)
	e.mustSend(proto.TypeMsg)
	d := e.sl.delays()
	if len(d) != 2 || d[0] < 10*time.Second || d[0] > 12500*time.Millisecond || d[1] < 20*time.Second || d[1] > 25*time.Second {
		t.Errorf("delays %v, want ~10s then ~20s (+≤25%% jitter)", d)
	}

	// 429 without Retry-After waits Google's default of 60 s.
	e = newEnv(t, nil)
	e.srv.FailNext(http.StatusTooManyRequests, 2)
	e.mustSend(proto.TypeMsg)
	if d := e.sl.delays(); len(d) != 2 || d[0] != time.Minute || d[1] != time.Minute {
		t.Errorf("delays %v, want [1m 1m]", d)
	}

	// The cap.
	e = newEnv(t, func(o *Options) { o.MaxAttempts = 6; o.MinBackoff = time.Second; o.MaxBackoff = 5 * time.Second })
	e.srv.FailNext(http.StatusInternalServerError, 5)
	e.mustSend(proto.TypeMsg)
	for i, d := range e.sl.delays() {
		if d > 5*time.Second || d < time.Second {
			t.Errorf("delay %d = %v outside [1s, 5s]", i, d)
		}
	}

	// MaxAttempts = 1 never retries.
	e = newEnv(t, func(o *Options) { o.MaxAttempts = 1 })
	e.srv.FailNext(http.StatusServiceUnavailable, 1)
	if _, err := e.send(proto.TypeMsg); !errors.Is(err, ErrUnavailable) || e.srv.Stats().SendRequests != 1 {
		t.Errorf("got %v, %+v", err, e.srv.Stats())
	}
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()
	// Already cancelled: nothing is sent.
	e := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.c.Send(ctx, e.secret.Topic(), e.seal(proto.TypeMsg, "x").C, proto.TypeMsg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if st := e.srv.Stats(); st.TokenRequests+st.SendRequests != 0 {
		t.Errorf("requests were made: %+v", st)
	}

	// Cancelled while backing off: the last error is kept.
	e = newEnv(t, nil)
	e.srv.FailNext(http.StatusServiceUnavailable, 5)
	ctx, cancel = context.WithCancel(context.Background())
	e.c.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	_, err = e.c.Send(ctx, e.secret.Topic(), e.seal(proto.TypeMsg, "x").C, proto.TypeMsg)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if st := e.srv.Stats(); st.SendRequests != 1 {
		t.Errorf("stats %+v", st)
	}

	// The deadline ends before the backoff would: return at once instead of sleeping.
	e = newEnv(t, nil)
	e.srv.FailNext(http.StatusServiceUnavailable, 1)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = e.c.Send(ctx, e.secret.Topic(), e.seal(proto.TypeMsg, "x").C, proto.TypeMsg)
	if !errors.Is(err, ErrUnavailable) || len(e.sl.delays()) != 0 {
		t.Fatalf("got %v, slept %v", err, e.sl.delays())
	}

	// Cancelled while the request is in flight.
	e = newEnv(t, nil)
	if err := e.c.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	e.c.hc = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel = context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err = e.c.Send(ctx, e.secret.Topic(), e.seal(proto.TypeMsg, "x").C, proto.TypeMsg)
	var fe *Error
	if !errors.Is(err, context.Canceled) || errors.As(err, &fe) {
		t.Fatalf("got %v, want a plain cancellation", err)
	}

	// The default sleep honours the context.
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx: %v", err)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleepCtx: %v", err)
	}
}

func TestNetworkErrorRetried(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	if err := e.c.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	real := e.c.hc.Transport
	var mu sync.Mutex
	fails := 1
	e.c.hc = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if fails > 0 {
			fails--
			return nil, errors.New("connection reset by peer")
		}
		return real.RoundTrip(r)
	})}
	e.mustSend(proto.TypeMsg)
	if d := e.sl.delays(); len(d) != 1 {
		t.Errorf("delays %v", d)
	}
	mu.Lock()
	fails = 10
	mu.Unlock()
	_, err := e.send(proto.TypeMsg)
	var fe *Error
	if !errors.As(err, &fe) || !errors.Is(err, ErrUnavailable) || fe.HTTPStatus != 0 || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("got %v", err)
	}
}

func TestTokenEndpointErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	e.srv.FailNextToken(1, fcmtest.TokenFailure{Error: "invalid_grant", Description: "Invalid JWT Signature."})
	_, err := e.send(proto.TypeMsg)
	var fe *Error
	if !errors.As(err, &fe) || !errors.Is(err, ErrUnauthenticated) || fe.Op != opToken || fe.OAuthError != "invalid_grant" ||
		fe.Retryable || !strings.Contains(fe.Message, "Invalid JWT Signature") || !strings.Contains(fe.Hint, "new key") {
		t.Fatalf("got %#v", err)
	}
	if st := e.srv.Stats(); st.TokenRequests != 1 || st.SendRequests != 0 {
		t.Errorf("stats %+v", st)
	}
	e.checkNoSecrets("error", err.Error())

	// A transient token endpoint failure is retried like a send failure.
	e = newEnv(t, nil)
	e.srv.FailNextToken(1, fcmtest.TokenFailure{HTTPStatus: 503, Error: "temporarily_unavailable", Description: "try again", RetryAfter: "3"})
	e.mustSend(proto.TypeMsg)
	if d := e.sl.delays(); len(d) != 1 || d[0] != 3*time.Second {
		t.Errorf("delays %v", d)
	}

	others := []struct {
		f    fcmtest.TokenFailure
		want error
	}{
		{fcmtest.TokenFailure{Error: "invalid_scope", Description: "Invalid OAuth scope or ID token audience provided."}, ErrInvalid},
		{fcmtest.TokenFailure{HTTPStatus: 401, Error: "invalid_client", Description: "The OAuth client was not found."}, ErrUnauthenticated},
		{fcmtest.TokenFailure{HTTPStatus: 403, Error: "access_denied", Description: "x"}, ErrPermission},
		{fcmtest.TokenFailure{HTTPStatus: 404, Error: "not_found", Description: "x"}, ErrNotFound},
	}
	for _, o := range others {
		e = newEnv(t, nil)
		e.srv.FailNextToken(1, o.f)
		if _, err := e.send(proto.TypeMsg); !errors.Is(err, o.want) {
			t.Errorf("%s: got %v, want %v", o.f.Error, err, o.want)
		}
	}
}

func TestClockSkewIsExplained(t *testing.T) {
	t.Parallel()
	for _, skew := range []time.Duration{2 * time.Hour, -2 * time.Hour} {
		e := newEnv(t, nil)
		e.c.now = func() time.Time { return e.clk.Now().Add(skew) }
		e.c.tokens.now = e.c.now
		err := e.c.Authenticate(context.Background())
		var fe *Error
		if !errors.As(err, &fe) || fe.OAuthError != "invalid_grant" || !strings.Contains(fe.Hint, "clock") {
			t.Errorf("skew %v: got %v", skew, err)
		}
	}
}

func TestUnknownOrRevokedKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	b, _, err := fcmtest.GenerateServiceAccount(fcmtest.DefaultProjectID, "ghost@x.iam.gserviceaccount.com", e.srv.TokenURL())
	if err != nil {
		t.Fatal(err)
	}
	sa, err := ParseServiceAccount(b)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(sa, Options{HTTPClient: e.srv.Client(), Endpoint: e.srv.URL, Now: e.clk.Now, Sleep: e.sl.sleep})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Authenticate(context.Background()); !errors.Is(err, ErrUnauthenticated) || !strings.Contains(err.Error(), "account not found") {
		t.Errorf("unregistered key: %v", err)
	}

	e.mustSend(proto.TypeMsg)
	e.srv.RevokeServiceAccounts()
	e.srv.ExpireTokens()
	if _, err := e.send(proto.TypeMsg); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("revoked key: %v", err)
	}
}

func TestMalformedTokenResponses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body string
		ok         bool
		lifetime   time.Duration
	}{
		{"not json", "<html>", false, 0},
		{"no token", `{"expires_in":3599,"token_type":"Bearer"}`, false, 0},
		{"mac token", `{"access_token":"a","expires_in":3599,"token_type":"mac"}`, false, 0},
		{"no expiry", `{"access_token":"a","token_type":"Bearer"}`, true, defaultTokenLifetime},
		{"huge expiry", `{"access_token":"a","expires_in":99999999,"token_type":"Bearer"}`, true, 24 * time.Hour},
	}
	sa, err := ParseServiceAccount(saJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		hc := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
		})}
		ts := newTokenSource(sa, hc, time.Now, discardLogger())
		_, lifetime, err := ts.exchange(context.Background(), time.Now())
		if (err == nil) != tc.ok || lifetime != tc.lifetime {
			t.Errorf("%s: lifetime %v, err %v", tc.name, lifetime, err)
		}
	}
}

func TestConcurrentSendsShareOneToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			_, err := e.send(proto.TypeMsg)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if st := e.srv.Stats(); st.TokenRequests != 1 || st.Accepted != n {
		t.Errorf("stats %+v, want one token for %d sends", st, n)
	}
}

func TestSendValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	c := e.seal(proto.TypeMsg, "x").C
	cases := []struct{ name, topic, data, typ string }{
		{"empty topic", "", c, proto.TypeMsg},
		{"prefixed topic", "/topics/" + e.secret.Topic(), c, proto.TypeMsg},
		{"space", "a b", c, proto.TypeMsg},
		{"non-ascii", "tôpic", c, proto.TypeMsg},
		{"too long", strings.Repeat("a", 901), c, proto.TypeMsg},
		{"unknown type", e.secret.Topic(), c, "note"},
		{"empty data", e.secret.Topic(), "", proto.TypeMsg},
		{"too big", e.secret.Topic(), strings.Repeat("A", proto.FCMDataLimit), proto.TypeMsg},
	}
	for _, tc := range cases {
		if _, err := e.c.Send(context.Background(), tc.topic, tc.data, tc.typ); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", tc.name, err)
		}
	}
	if st := e.srv.Stats(); st.TokenRequests+st.SendRequests != 0 {
		t.Errorf("invalid input reached the network: %+v", st)
	}
	if err := ValidateTopic(strings.Repeat("a-_.~%Z9", 112) + "abcd"); err != nil { // 900 chars
		t.Errorf("valid 900-char topic: %v", err)
	}
}

func TestDefaultEndpointsThroughTransport(t *testing.T) {
	t.Parallel()
	srv := fcmtest.NewServer(t)
	sa, err := ParseServiceAccount(srv.NewServiceAccountFor(fcmtest.GoogleTokenURL))
	if err != nil {
		t.Fatal(err)
	}
	if sa.TokenURI != DefaultTokenURI {
		t.Fatalf("token_uri %s", sa.TokenURI)
	}
	c, err := New(sa, Options{HTTPClient: &http.Client{Transport: srv.Transport()}, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := proto.NewSecret(nil)
	name, _, err := c.SendMessage(context.Background(), sec, proto.Message{V: 1, T: proto.TypePair, ID: "1", TS: 1}, nil)
	if err != nil || !nameRE.MatchString(name) {
		t.Fatalf("%q, %v (%q)", name, err, srv.Rejections())
	}
	// With the real clock, the refresh time must be wall-clock only: a monotonic reading would
	// make a token look fresh after a suspend/resume (Linux's monotonic clock stops in suspend).
	c.tokens.mu.Lock()
	refreshAt := c.tokens.refreshAt
	c.tokens.mu.Unlock()
	if refreshAt.IsZero() || strings.Contains(refreshAt.String(), "m=") {
		t.Errorf("refreshAt %s carries a monotonic clock reading", refreshAt)
	}
}

func TestNewFromConfig(t *testing.T) {
	t.Parallel()
	srv := fcmtest.NewServer(t)
	home := t.TempDir()
	srv.SetupHome(home, nil)
	cfg, exists, err := config.Load(home)
	if err != nil || !exists || cfg.FCMEndpoint != srv.URL {
		t.Fatalf("config: %v %v %+v", exists, err, cfg)
	}
	c, err := NewFromConfig(cfg, home, Options{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := proto.NewSecret(nil)
	if _, _, err := c.SendMessage(context.Background(), sec, proto.Message{V: 1, T: proto.TypeHB, ID: "1", TS: 1}, nil); err != nil {
		t.Fatalf("%v (%q)", err, srv.Rejections())
	}

	inline := config.Default()
	srv.Configure(inline)
	c, err = NewFromConfig(inline, t.TempDir(), Options{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFromConfig(config.Default(), home, Options{}); !errors.Is(err, config.ErrNoServiceAccount) {
		t.Errorf("no service account: %v", err)
	}
}

func TestLogsHoldNoSecrets(t *testing.T) {
	t.Parallel()
	e := newEnv(t, nil)
	e.mustSend(proto.TypeMsg)
	e.srv.ExpireTokens()
	e.srv.FailNext(http.StatusServiceUnavailable, 1)
	e.mustSend(proto.TypeAlert)
	tokens := e.srv.IssuedTokens()
	logs := e.logs.String()
	if !strings.Contains(logs, "retrying") || !strings.Contains(logs, "access token") {
		t.Fatalf("expected debug/warn lines, got:\n%s", logs)
	}
	e.checkNoSecrets("logs", logs)
	for _, m := range e.srv.Messages() {
		if strings.Contains(logs, m.C[:40]) {
			t.Error("logs contain the payload")
		}
	}
	if len(tokens) != 2 { // the expired one and its replacement
		t.Fatalf("checked %d tokens, want 2", len(tokens))
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"": 0, "0": 0, "-5": 0, "abc": 0, "7": 7 * time.Second, " 12 ": 12 * time.Second,
		"999999999999": 24 * time.Hour,
		now.Add(42 * time.Second).Format(http.TimeFormat): 42 * time.Second,
		now.Add(-time.Hour).Format(http.TimeFormat):       0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}

func TestErrorString(t *testing.T) {
	t.Parallel()
	e := &Error{Op: opSend, HTTPStatus: 429, Status: StatusResourceExhausted, FCMCode: CodeQuotaExceeded,
		Message: "Topic quota exceeded.", RetryAfter: 30 * time.Second, Retryable: true}
	want := "fcm: send: HTTP 429 RESOURCE_EXHAUSTED/QUOTA_EXCEEDED: Topic quota exceeded. (retry after 30s)"
	if e.Error() != want {
		t.Errorf("got  %q\nwant %q", e.Error(), want)
	}
	if got := clean("a\nb\x1b[31m" + strings.Repeat("é", 400)); strings.ContainsAny(got, "\n\x1b") || len(got) > maxMessageLen+len("…") {
		t.Errorf("clean: %q", got)
	}
	if (&Error{Status: "WHATEVER"}).Is(ErrInvalid) {
		t.Error("unknown status matched a sentinel")
	}
}
