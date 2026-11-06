package fcmtest

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/proto"
)

// testAccount is a registered account with its private key, parsed independently of internal/fcm.
type testAccount struct {
	email, kid, tokenURI, projectID string
	key                             *rsa.PrivateKey
	raw                             map[string]string
}

func parseAccount(t *testing.T, b []byte) testAccount {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode([]byte(m["private_key"]))
	if blk == nil || blk.Type != "PRIVATE KEY" {
		t.Fatalf("private_key is not a PKCS#8 PEM block")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return testAccount{email: m["client_email"], kid: m["private_key_id"], tokenURI: m["token_uri"],
		projectID: m["project_id"], key: k.(*rsa.PrivateKey), raw: m}
}

func newAccount(t *testing.T, s *Server) testAccount { return parseAccount(t, s.NewServiceAccount()) }

func (a testAccount) header() map[string]any {
	return map[string]any{"alg": "RS256", "typ": "JWT", "kid": a.kid}
}

func (a testAccount) claims(now time.Time) map[string]any {
	return map[string]any{"iss": a.email, "scope": ScopeFCM, "aud": a.tokenURI, "iat": now.Unix(), "exp": now.Unix() + 3600}
}

func enc(t *testing.T, v any) string {
	t.Helper()
	if s, ok := v.(string); ok {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// mint signs header.claims (each a map, or a string used verbatim as JSON) with key.
func mint(t *testing.T, key *rsa.PrivateKey, header, claims any) string {
	t.Helper()
	signing := enc(t, header) + "." + enc(t, claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func postForm(t *testing.T, s *Server, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := s.Client().PostForm(s.TokenURL(), form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func exchange(t *testing.T, s *Server, assertion string) (int, map[string]any) {
	t.Helper()
	return postForm(t, s, url.Values{"grant_type": {GrantType}, "assertion": {assertion}})
}

func token(t *testing.T, s *Server, a testAccount) string {
	t.Helper()
	code, m := exchange(t, s, mint(t, a.key, a.header(), a.claims(s.now())))
	if code != 200 {
		t.Fatalf("token exchange: %d %v", code, m)
	}
	if m["token_type"] != "Bearer" || m["expires_in"] != float64(3599) || m["access_token"] == "" {
		t.Fatalf("token response %v", m)
	}
	return m["access_token"].(string)
}

func send(t *testing.T, s *Server, tok, contentType string, body []byte) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.URL+"/v1/projects/"+s.ProjectID+"/messages:send", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &m)
	return resp, m
}

func sealed(t *testing.T, sec proto.Secret, typ, body string) string {
	t.Helper()
	s, err := proto.EncryptMessage(sec.Key(), proto.Message{V: 1, T: typ, ID: "id-1", TS: time.Now().Unix(), Body: body}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s.C
}

func TestTokenExchangeValidatesJWT(t *testing.T) {
	t.Parallel()
	s := NewServer(t)
	a := newAccount(t, s)
	other := newAccount(t, s)
	stranger, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	good := func() (map[string]any, map[string]any) { return a.header(), a.claims(now) }
	type mut func(h, c map[string]any) string // returns the assertion; "" means mint(h, c)
	cases := []struct {
		name string
		mut  mut
		want string // OAuth error
		desc string // substring of error_description
	}{
		{"two parts", func(h, c map[string]any) string { return enc(t, h) + "." + enc(t, c) }, "invalid_grant", "want 3 dot-separated parts"},
		{"padded", func(h, c map[string]any) string { return mint(t, a.key, h, c) + "==" }, "invalid_grant", "part 3 is not unpadded"},
		{"std base64", func(h, c map[string]any) string {
			p := strings.Split(mint(t, a.key, h, c), ".")
			sig, _ := base64.RawURLEncoding.DecodeString(p[2])
			return p[0] + "." + p[1] + "." + base64.StdEncoding.EncodeToString(sig)
		}, "invalid_grant", "part 3 is not unpadded"},
		{"alg none", func(h, c map[string]any) string {
			h["alg"] = "none"
			return enc(t, h) + "." + enc(t, c) + "." + enc(t, "sig")
		}, "invalid_grant", "alg=none"},
		{"alg HS256", func(h, c map[string]any) string { h["alg"] = "HS256"; return "" }, "invalid_grant", "alg=HS256"},
		{"typ missing", func(h, c map[string]any) string { delete(h, "typ"); return "" }, "invalid_grant", "missing field \"typ\""},
		{"kid missing", func(h, c map[string]any) string { delete(h, "kid"); return "" }, "invalid_grant", "missing field \"kid\""},
		{"kid of another account", func(h, c map[string]any) string { h["kid"] = other.kid; return "" }, "invalid_grant", "unknown kid"},
		{"extra header", func(h, c map[string]any) string { h["x5u"] = "https://evil"; return "" }, "invalid_grant", "unexpected field \"x5u\""},
		{"duplicate claim", func(h, c map[string]any) string {
			js := `{"iss":"` + a.email + `","iss":"x","scope":"` + ScopeFCM + `","aud":"` + a.tokenURI + `","iat":1,"exp":2}`
			return mint(t, a.key, h, js)
		}, "invalid_grant", "duplicate key"},
		{"unknown iss", func(h, c map[string]any) string { c["iss"] = "nobody@x.iam.gserviceaccount.com"; return "" }, "invalid_grant", "account not found"},
		{"wrong aud", func(h, c map[string]any) string { c["aud"] = GoogleTokenURL; return "" }, "invalid_grant", "Failed audience check"},
		{"wrong scope", func(h, c map[string]any) string {
			c["scope"] = "https://www.googleapis.com/auth/userinfo.email"
			return ""
		}, "invalid_scope", "Invalid OAuth scope"},
		{"sub (delegation)", func(h, c map[string]any) string { c["sub"] = "user@example.com"; return "" }, "invalid_grant", "unexpected field \"sub\""},
		{"float iat", func(h, c map[string]any) string { c["iat"] = float64(now.Unix()) + 0.5; return "" }, "invalid_grant", "iat/exp integers"},
		{"string exp", func(h, c map[string]any) string { c["exp"] = "9999999999"; return "" }, "invalid_grant", "iat/exp integers"},
		{"lifetime 3601 s", func(h, c map[string]any) string { c["exp"] = now.Unix() + 3601; return "" }, "invalid_grant", "short-lived"},
		{"exp before iat", func(h, c map[string]any) string { c["exp"] = now.Unix() - 1; return "" }, "invalid_grant", "short-lived"},
		{"expired", func(h, c map[string]any) string {
			c["iat"], c["exp"] = now.Unix()-3600, now.Unix()
			return ""
		}, "invalid_grant", "short-lived"},
		{"iat in the future", func(h, c map[string]any) string {
			c["iat"], c["exp"] = now.Unix()+120, now.Unix()+3600
			return ""
		}, "invalid_grant", "short-lived"},
		{"tampered claims", func(h, c map[string]any) string {
			p := strings.Split(mint(t, a.key, h, c), ".")
			c["scope"] = ScopeCloud
			return p[0] + "." + enc(t, c) + "." + p[2]
		}, "invalid_grant", "Invalid JWT Signature."},
		{"signed by a stranger", func(h, c map[string]any) string { return mint(t, stranger, h, c) }, "invalid_grant", "Invalid JWT Signature."},
		{"signed by the other account's key", func(h, c map[string]any) string { return mint(t, other.key, h, c) }, "invalid_grant", "Invalid JWT Signature."},
	}
	for _, tc := range cases {
		h, c := good()
		as := tc.mut(h, c)
		if as == "" {
			as = mint(t, a.key, h, c)
		}
		code, m := exchange(t, s, as)
		if d, _ := m["error_description"].(string); code != http.StatusBadRequest || m["error"] != tc.want || !strings.Contains(d, tc.desc) {
			t.Errorf("%s: %d %v, want 400 %s %q", tc.name, code, m, tc.want, tc.desc)
		}
		if _, ok := m["access_token"]; ok {
			t.Errorf("%s: token issued", tc.name)
		}
	}
	if st := s.Stats(); st.TokensIssued != 0 || st.TokenRequests != len(cases) {
		t.Errorf("stats %+v", st)
	}
	if len(s.Rejections()) != len(cases) {
		t.Errorf("%d rejections recorded for %d cases", len(s.Rejections()), len(cases))
	}

	// Both FCM scopes, and exactly one hour of lifetime, are fine.
	for _, scope := range []string{ScopeFCM, ScopeCloud, "openid " + ScopeFCM} {
		h, c := good()
		c["scope"] = scope
		if code, m := exchange(t, s, mint(t, a.key, h, c)); code != 200 {
			t.Errorf("scope %q: %d %v", scope, code, m)
		}
	}

	// Form-level errors.
	if code, m := postForm(t, s, url.Values{"grant_type": {"client_credentials"}, "assertion": {"x"}}); code != 400 || m["error"] != "unsupported_grant_type" {
		t.Errorf("grant_type: %d %v", code, m)
	}
	if code, m := postForm(t, s, url.Values{"grant_type": {GrantType}}); code != 400 || m["error"] != "invalid_request" {
		t.Errorf("no assertion: %d %v", code, m)
	}
	resp, err := s.Client().Post(s.TokenURL(), "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("JSON body accepted: %d", resp.StatusCode)
	}
}

func TestSendValidatesBody(t *testing.T) {
	t.Parallel()
	sec, _ := proto.NewSecret(nil)
	s := NewServer(t, WithSecret(sec))
	a := newAccount(t, s)
	tok := token(t, s, a)
	topic := sec.Topic()
	c := sealed(t, sec, proto.TypeMsg, "hi")
	valid := proto.FCMRequest(topic, c, proto.TypeMsg)
	mutate := func(f func(msg map[string]any)) []byte {
		var top map[string]any
		_ = json.Unmarshal(valid, &top)
		f(top["message"].(map[string]any))
		b, _ := json.Marshal(top)
		return b
	}
	android := func(m map[string]any) map[string]any { return m["android"].(map[string]any) }
	data := func(m map[string]any) map[string]any { return m["data"].(map[string]any) }
	cases := []struct {
		name string
		body []byte
	}{
		{"token target", mutate(func(m map[string]any) { m["token"] = "fcm-registration-token" })},
		{"token instead of topic", mutate(func(m map[string]any) { delete(m, "topic"); m["token"] = "x" })},
		{"condition", mutate(func(m map[string]any) { m["condition"] = "'a' in topics" })},
		{"notification block", mutate(func(m map[string]any) { m["notification"] = map[string]any{"title": "x"} })},
		{"apns block", mutate(func(m map[string]any) { m["apns"] = map[string]any{} })},
		{"no topic", mutate(func(m map[string]any) { delete(m, "topic") })},
		{"/topics/ prefix", mutate(func(m map[string]any) { m["topic"] = "/topics/" + topic })},
		{"bad topic", mutate(func(m map[string]any) { m["topic"] = "a b" })},
		{"topic not a string", mutate(func(m map[string]any) { m["topic"] = 5 })},
		{"no android", mutate(func(m map[string]any) { delete(m, "android") })},
		{"normal priority", mutate(func(m map[string]any) { android(m)["priority"] = "normal" })},
		{"priority missing", mutate(func(m map[string]any) { delete(android(m), "priority") })},
		{"ttl 60s", mutate(func(m map[string]any) { android(m)["ttl"] = "60s" })},
		{"ttl as number", mutate(func(m map[string]any) { android(m)["ttl"] = 3600 })},
		{"collapse_key", mutate(func(m map[string]any) { android(m)["collapse_key"] = "x" })},
		{"no data", mutate(func(m map[string]any) { delete(m, "data") })},
		{"extra data key", mutate(func(m map[string]any) { data(m)["title"] = "Alice" })},
		{"c missing", mutate(func(m map[string]any) { delete(data(m), "c"); data(m)["d"] = c })},
		{"c not base64", mutate(func(m map[string]any) { data(m)["c"] = "%%%%" })},
		{"c unpadded", mutate(func(m map[string]any) { data(m)["c"] = strings.TrimRight(c[:len(c)-1], "=") })},
		{"c wrong size", mutate(func(m map[string]any) { data(m)["c"] = base64.StdEncoding.EncodeToString(make([]byte, 1000)) })},
		{"c too big", mutate(func(m map[string]any) { data(m)["c"] = strings.Repeat("A", 4100) })},
		{"validate_only", func() []byte {
			var top map[string]any
			_ = json.Unmarshal(valid, &top)
			top["validate_only"] = true
			b, _ := json.Marshal(top)
			return b
		}()},
		{"duplicate key", []byte(strings.Replace(string(valid), `"topic":`, `"topic":"x","topic":`, 1))},
		{"trailing data", append(append([]byte{}, valid...), []byte(`{}`)...)},
		{"not an object", []byte(`[]`)},
		{"empty", nil},
	}
	for _, tc := range cases {
		resp, m := send(t, s, tok, "application/json", tc.body)
		e, _ := m["error"].(map[string]any)
		if resp.StatusCode != 400 || e == nil || e["status"] != "INVALID_ARGUMENT" || e["code"] != float64(400) || e["message"] == "" {
			t.Errorf("%s: %d %v", tc.name, resp.StatusCode, m)
		}
	}
	if st := s.Stats(); st.Accepted != 0 {
		t.Fatalf("invalid bodies accepted: %+v", st)
	}

	if resp, m := send(t, s, tok, "text/plain", valid); resp.StatusCode != 400 {
		t.Errorf("text/plain accepted: %v", m)
	}
	resp, m := send(t, s, tok, "application/json; charset=UTF-8", valid)
	if resp.StatusCode != 200 || !strings.HasPrefix(m["name"].(string), "projects/"+DefaultProjectID+"/messages/") {
		t.Fatalf("valid body: %d %v %q", resp.StatusCode, m, s.Rejections())
	}
	got := s.Messages()
	if len(got) != 1 || got[0].Name != m["name"] || got[0].Topic != topic || got[0].C != c || got[0].TTL != "3600s" ||
		got[0].Type != proto.TypeMsg || got[0].Decrypted == nil || got[0].Decrypted.Body != "hi" || got[0].DecryptErr != nil ||
		!bytes.Equal(got[0].Body, valid) {
		t.Errorf("recorded %+v", got)
	}
}

func TestSendAuthAndProject(t *testing.T) {
	t.Parallel()
	clk := struct {
		sync.Mutex
		t time.Time
	}{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	now := func() time.Time { clk.Lock(); defer clk.Unlock(); return clk.t }
	s := NewServer(t, WithClock(now))
	a := newAccount(t, s)
	sec, _ := proto.NewSecret(nil)
	body := proto.FCMRequest(sec.Topic(), sealed(t, sec, proto.TypeMsg, "x"), proto.TypeMsg)

	resp, m := send(t, s, "", "application/json", body)
	if resp.StatusCode != 401 || !strings.Contains(m["error"].(map[string]any)["message"].(string), "missing required authentication") ||
		!strings.Contains(resp.Header.Get("WWW-Authenticate"), "invalid_token") {
		t.Errorf("no auth: %d %v", resp.StatusCode, m)
	}
	if resp, _ := send(t, s, "made-up", "application/json", body); resp.StatusCode != 401 {
		t.Errorf("unknown token: %d", resp.StatusCode)
	}
	tok := token(t, s, a)
	if resp, _ := send(t, s, tok, "application/json", body); resp.StatusCode != 200 {
		t.Errorf("fresh token: %d", resp.StatusCode)
	}
	clk.Lock()
	clk.t = clk.t.Add(DefaultTokenTTL)
	clk.Unlock()
	if resp, m := send(t, s, tok, "application/json", body); resp.StatusCode != 401 || m["error"].(map[string]any)["status"] != "UNAUTHENTICATED" {
		t.Errorf("expired token: %d %v", resp.StatusCode, m)
	}
	tok = token(t, s, a)
	s.ExpireTokens()
	if resp, _ := send(t, s, tok, "application/json", body); resp.StatusCode != 401 {
		t.Errorf("ExpireTokens: %d", resp.StatusCode)
	}
	if got := s.IssuedTokens(); len(got) != 2 || got[1] != tok {
		t.Errorf("IssuedTokens must keep expired tokens: %d", len(got))
	}

	tok = token(t, s, a)
	req, _ := http.NewRequest(http.MethodPost, s.URL+"/v1/projects/someone-else/messages:send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	r2, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != 403 {
		t.Errorf("other project: %d", r2.StatusCode)
	}
}

func TestScriptedFailures(t *testing.T) {
	t.Parallel()
	sec, _ := proto.NewSecret(nil)
	s := NewServer(t)
	tok := token(t, s, newAccount(t, s))
	body := proto.FCMRequest(sec.Topic(), sealed(t, sec, proto.TypeHB, ""), proto.TypeHB)

	s.FailNextRetryAfter(429, 1, 30*time.Second)
	resp, m := send(t, s, tok, "application/json", body)
	e := m["error"].(map[string]any)
	det := e["details"].([]any)[0].(map[string]any)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "30" || e["status"] != "RESOURCE_EXHAUSTED" ||
		e["code"] != float64(429) || det["@type"] != FCMErrorType || det["errorCode"] != "QUOTA_EXCEEDED" {
		t.Errorf("429: %d %v", resp.StatusCode, m)
	}
	for status, code := range map[int]string{400: "INVALID_ARGUMENT", 403: "SENDER_ID_MISMATCH", 404: "UNREGISTERED", 500: "INTERNAL", 503: "UNAVAILABLE"} {
		s.FailNext(status, 1)
		resp, m := send(t, s, tok, "application/json", body)
		e := m["error"].(map[string]any)
		if resp.StatusCode != status || e["details"].([]any)[0].(map[string]any)["errorCode"] != code {
			t.Errorf("%d: %v", status, m)
		}
	}
	s.FailNext(502, 1)
	if resp, m := send(t, s, tok, "application/json", body); resp.StatusCode != 502 || m != nil ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("502 should be an HTML page: %v", m)
	}
	s.FailTopic(sec.Topic(), 404)
	for range 2 {
		if resp, _ := send(t, s, tok, "application/json", body); resp.StatusCode != 404 {
			t.Errorf("FailTopic: %d", resp.StatusCode)
		}
	}
	s.UnfailTopic(sec.Topic())
	if resp, _ := send(t, s, tok, "application/json", body); resp.StatusCode != 200 {
		t.Errorf("after UnfailTopic: %d", resp.StatusCode)
	}
	if got := s.Messages(); len(got) != 1 || got[0].Type != proto.TypeHB || !got[0].Heartbeat || got[0].Decrypted != nil {
		t.Errorf("without a key the type comes from the ttl: %+v", got)
	}

	s.SetTopicDataLimit(2048)
	big := proto.FCMRequest(sec.Topic(), sealed(t, sec, proto.TypeMsg, strings.Repeat("x", 1500)), proto.TypeMsg)
	if resp, _ := send(t, s, tok, "application/json", big); resp.StatusCode != 400 {
		t.Errorf("topic data limit: %d", resp.StatusCode)
	}

	s.FailNextToken(1, TokenFailure{})
	if code, m := exchange(t, s, "whatever"); code != 400 || m["error"] != "invalid_grant" || m["error_description"] != msgJWTSig {
		t.Errorf("FailNextToken: %d %v", code, m)
	}
	s.FailNextToken(1, TokenFailure{Description: "Invalid grant: account not found"})
	if code, m := exchange(t, s, "whatever"); code != 400 || m["error"] != "invalid_grant" || m["error_description"] != "Invalid grant: account not found" {
		t.Errorf("FailNextToken keeps a given description: %d %v", code, m)
	}
}

func TestDecryptChecks(t *testing.T) {
	t.Parallel()
	sec, _ := proto.NewSecret(nil)
	s := NewServer(t, WithSecret(sec))
	tok := token(t, s, newAccount(t, s))
	// A heartbeat sent with the 3600s TTL violates §3.
	if resp, _ := send(t, s, tok, "application/json", proto.FCMRequest(sec.Topic(), sealed(t, sec, proto.TypeHB, ""), proto.TypeMsg)); resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	other, _ := proto.NewSecret(nil)
	if resp, _ := send(t, s, tok, "application/json", proto.FCMRequest(sec.Topic(), sealed(t, other, proto.TypeMsg, "x"), proto.TypeMsg)); resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	got := s.Messages()
	if len(got) != 2 || got[0].DecryptErr == nil || !strings.Contains(got[0].DecryptErr.Error(), "ttl") ||
		got[1].DecryptErr == nil || got[1].Decrypted != nil {
		t.Errorf("%+v", got)
	}
}

func TestWaitFor(t *testing.T) {
	t.Parallel()
	sec, _ := proto.NewSecret(nil)
	s := NewServer(t)
	tok := token(t, s, newAccount(t, s))
	body := proto.FCMRequest(sec.Topic(), sealed(t, sec, proto.TypeMsg, "x"), proto.TypeMsg)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if got, err := s.WaitFor(ctx, 1); err == nil || len(got) != 0 {
		t.Errorf("WaitFor without messages: %v %v", got, err)
	}
	done := make(chan error, 1)
	go func() {
		for range 3 {
			req, _ := http.NewRequest(http.MethodPost, s.URL+"/v1/projects/"+s.ProjectID+"/messages:send", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Content-Type", "application/json")
			resp, err := s.Client().Do(req)
			if err != nil {
				done <- err
				return
			}
			resp.Body.Close()
		}
		done <- nil
	}()
	if got := s.MustWaitFor(3, 10*time.Second); len(got) != 3 {
		t.Errorf("got %d", len(got))
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTransport(t *testing.T) {
	t.Parallel()
	s := NewServer(t)
	a := parseAccount(t, s.NewServiceAccountFor(GoogleTokenURL))
	hc := &http.Client{Transport: s.Transport()}
	as := mint(t, a.key, a.header(), a.claims(time.Now()))
	resp, err := hc.PostForm(GoogleTokenURL, url.Values{"grant_type": {GrantType}, "assertion": {as}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if resp.StatusCode != 200 || m["access_token"] == nil {
		t.Fatalf("token via Transport: %d %v", resp.StatusCode, m)
	}
	for _, u := range []string{"https://example.com/token", "http://oauth2.googleapis.com/token",
		"https://fcm.googleapis.com/token", "https://oauth2.googleapis.com/v1/projects/x/messages:send"} {
		if resp, err := hc.Post(u, "application/json", nil); err == nil {
			resp.Body.Close()
			t.Errorf("%s: Transport must refuse", u)
		}
	}
}

func TestServiceAccountFiles(t *testing.T) {
	t.Parallel()
	s := NewServer(t)
	a := newAccount(t, s)
	for _, k := range []string{"type", "project_id", "private_key_id", "private_key", "client_email", "client_id",
		"auth_uri", "token_uri", "auth_provider_x509_cert_url", "client_x509_cert_url", "universe_domain"} {
		if a.raw[k] == "" {
			t.Errorf("service account lacks %s", k)
		}
	}
	if a.raw["type"] != "service_account" || a.tokenURI != s.TokenURL() || a.projectID != DefaultProjectID ||
		a.key.N.BitLen() != 2048 || len(a.kid) != 40 {
		t.Errorf("unexpected account %v", a.raw["client_email"])
	}
	if b := newAccount(t, s); b.email == a.email || b.kid == a.kid || b.key.Equal(a.key) {
		t.Error("accounts must be distinct with fresh keys")
	}

	home := filepath.Join(t.TempDir(), "tbd")
	sec, _ := proto.NewSecret(nil)
	cfg := config.Default()
	cfg.Secret = sec.Code()
	s.SetupHome(home, cfg)
	loaded, exists, err := config.Load(home)
	if err != nil || !exists || loaded.FCMEndpoint != s.URL || loaded.Secret != sec.Code() || loaded.FCMServiceAccount != ServiceAccountFile {
		t.Fatalf("config after SetupHome: %v %v %+v", exists, err, loaded)
	}
	b, err := loaded.ServiceAccountJSON(home)
	if err != nil {
		t.Fatal(err)
	}
	parseAccount(t, b)
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(home, ServiceAccountFile)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("service-account file mode: %v %v", fi.Mode(), err)
		}
	}
	// nil cfg keeps what config.toml already has.
	s.SetupHome(home, nil)
	if again, _, _ := config.Load(home); again.Secret != sec.Code() {
		t.Error("SetupHome(nil) lost the secret")
	}

	inline := config.Default()
	s.Configure(inline)
	if !strings.HasPrefix(inline.FCMServiceAccount, "{") || strings.Contains(inline.FCMServiceAccount, "\n") || inline.FCMEndpoint != s.URL {
		t.Errorf("Configure: %q", inline.FCMEndpoint)
	}
}

func TestNoDuplicateKeys(t *testing.T) {
	t.Parallel()
	ok := []string{`{}`, `[]`, `{"a":1,"b":{"a":2},"c":[{"a":1},{"a":2}]}`, `{"a":"b","b":"a"}`, `[{"x":[1,{"y":2}]},"z"]`}
	bad := []string{`{"a":1,"a":2}`, `{"o":{"k":1,"k":2}}`, `[{"x":1,"x":1}]`, `{"a":{"b":1},"a":2}`, `{"a":`}
	for _, s := range ok {
		if err := noDuplicateKeys([]byte(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := noDuplicateKeys([]byte(s)); err == nil {
			t.Errorf("%s: accepted", s)
		}
	}
}
