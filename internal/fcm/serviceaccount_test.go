package fcm

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func pemKey(t *testing.T, typ string, der []byte) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

func pkcs8(t *testing.T, k any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pemKey(t, "PRIVATE KEY", der)
}

func saJSON(t *testing.T, mod func(m map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"type":           "service_account",
		"project_id":     "my-proj-123",
		"private_key_id": "0123456789abcdef0123456789abcdef01234567",
		"private_key":    pkcs8(t, testKey(t)),
		"client_email":   "tbd@my-proj-123.iam.gserviceaccount.com",
		"client_id":      "123456789012345678901",
		"token_uri":      "https://oauth2.googleapis.com/token",
	}
	if mod != nil {
		mod(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// checkNoKey fails if an error or a printed value could contain key material.
func checkNoKey(t *testing.T, what, s string) {
	t.Helper()
	for _, bad := range []string{"-----", "MII", "\\n"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s may leak key material (%q): %s", what, bad, s)
		}
	}
}

func TestParseServiceAccount(t *testing.T) {
	t.Parallel()
	sa, err := ParseServiceAccount(saJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if sa.ProjectID != "my-proj-123" || sa.ClientEmail != "tbd@my-proj-123.iam.gserviceaccount.com" ||
		sa.TokenURI != DefaultTokenURI || sa.key == nil || !sa.key.PublicKey.Equal(&testKey(t).PublicKey) {
		t.Fatalf("unexpected result: %v", sa)
	}

	sa, err = ParseServiceAccount(saJSON(t, func(m map[string]any) { delete(m, "token_uri") }))
	if err != nil || sa.TokenURI != DefaultTokenURI {
		t.Fatalf("token_uri default: %v, %v", sa, err)
	}

	pkcs1 := pemKey(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(testKey(t)))
	if _, err := ParseServiceAccount(saJSON(t, func(m map[string]any) { m["private_key"] = pkcs1 })); err != nil {
		t.Fatalf("PKCS#1: %v", err)
	}

	escaped := strings.ReplaceAll(pkcs8(t, testKey(t)), "\n", `\n`)
	if _, err := ParseServiceAccount(saJSON(t, func(m map[string]any) { m["private_key"] = escaped })); err != nil {
		t.Fatalf("doubly escaped newlines: %v", err)
	}

	local := "http://127.0.0.1:8080/token"
	if sa, err := ParseServiceAccount(saJSON(t, func(m map[string]any) { m["token_uri"] = local })); err != nil || sa.TokenURI != local {
		t.Fatalf("loopback http token_uri: %v", err)
	}
}

func TestParseServiceAccountErrors(t *testing.T) {
	t.Parallel()
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	goodKey := pkcs8(t, testKey(t))
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"empty", nil, "empty input"},
		{"not json", []byte("private_key: " + goodKey), "invalid JSON"},
		{"truncated", saJSON(t, nil)[:200], "truncated JSON"},
		{"trailing", append(saJSON(t, nil), []byte(`{"x":1}`)...), "trailing data"},
		{"trailing brace", append(saJSON(t, nil), []byte(" }")...), "trailing data"},
		{"key not a string", saJSON(t, func(m map[string]any) { m["private_key"] = 42 }), `"private_key" must be a string`},
		{"wrong type", saJSON(t, func(m map[string]any) { m["type"] = "authorized_user" }), `type is "authorized_user"`},
		{"google-services.json", []byte(`{"project_info":{"project_number":"1","project_id":"x"},"client":[]}`), "google-services.json"},
		{"no project", saJSON(t, func(m map[string]any) { delete(m, "project_id") }), "project_id is missing"},
		{"no key id", saJSON(t, func(m map[string]any) { m["private_key_id"] = "" }), "private_key_id is missing"},
		{"no key", saJSON(t, func(m map[string]any) { delete(m, "private_key") }), "private_key is missing"},
		{"no email", saJSON(t, func(m map[string]any) { delete(m, "client_email") }), "client_email is missing"},
		{"bad email", saJSON(t, func(m map[string]any) { m["client_email"] = "nobody" }), "not an e-mail"},
		{"bad project", saJSON(t, func(m map[string]any) { m["project_id"] = "a/b?c" }), "unexpected characters"},
		{"dot-dot project", saJSON(t, func(m map[string]any) { m["project_id"] = ".." }), "unexpected characters"},
		{"not pem", saJSON(t, func(m map[string]any) { m["private_key"] = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC" }), "not a PEM"},
		{"broken der", saJSON(t, func(m map[string]any) { m["private_key"] = pemKey(t, "PRIVATE KEY", []byte("MIIgarbage")) }), "not a valid PKCS#8"},
		{"broken pkcs1", saJSON(t, func(m map[string]any) { m["private_key"] = pemKey(t, "RSA PRIVATE KEY", []byte{1, 2, 3}) }), "not a valid PKCS#1"},
		{"ec key", saJSON(t, func(m map[string]any) { m["private_key"] = pkcs8(t, ec) }), "ECDSA key, want RSA"},
		{"small rsa", saJSON(t, func(m map[string]any) { m["private_key"] = pkcs8(t, small) }), "1024-bit"},
		{"cert", saJSON(t, func(m map[string]any) { m["private_key"] = pemKey(t, "CERTIFICATE", []byte{1}) }), `PEM type "CERTIFICATE"`},
		{"encrypted", saJSON(t, func(m map[string]any) {
			m["private_key"] = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY",
				Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: []byte{1}}))
		}), "encrypted"},
		{"http token_uri", saJSON(t, func(m map[string]any) { m["token_uri"] = "http://oauth2.googleapis.com/token" }), "plain http"},
		{"ftp token_uri", saJSON(t, func(m map[string]any) { m["token_uri"] = "ftp://x/token" }), "not https"},
		{"relative token_uri", saJSON(t, func(m map[string]any) { m["token_uri"] = "/token" }), "not an absolute URL"},
		{"creds in token_uri", saJSON(t, func(m map[string]any) { m["token_uri"] = "https://u:p@x/token" }), "credentials"},
	}
	for _, tc := range cases {
		_, err := ParseServiceAccount(tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error containing %q", tc.name, err, tc.want)
			continue
		}
		checkNoKey(t, tc.name, err.Error())
		if strings.Contains(err.Error(), "p@x") {
			t.Errorf("%s: error echoes URL credentials: %v", tc.name, err)
		}
	}
}

func TestServiceAccountNeverPrintsKey(t *testing.T) {
	t.Parallel()
	sa, err := ParseServiceAccount(saJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "sa", sa, "sav", *sa)
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "sa", sa)
	out := fmt.Sprintf("%v %+v %#v %s %v %+v %#v", sa, sa, sa, sa, *sa, *sa, *sa) + logs.String()
	checkNoKey(t, "printed service account", out)
	if !strings.Contains(out, sa.ClientEmail) {
		t.Errorf("printed form should identify the account: %s", out)
	}
}

func TestAssertion(t *testing.T) {
	t.Parallel()
	sa, err := ParseServiceAccount(saJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_790_000_000, 0)
	a, err := sa.assertion(now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(a, ".")
	if len(parts) != 3 || strings.ContainsAny(a, "=+/ \n") {
		t.Fatalf("not a compact unpadded base64url JWS: %q", a)
	}
	dec := func(i int) []byte {
		b, err := base64.RawURLEncoding.Strict().DecodeString(parts[i])
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		return b
	}
	if got, want := string(dec(0)), `{"alg":"RS256","typ":"JWT","kid":"`+sa.PrivateKeyID+`"}`; got != want {
		t.Errorf("header %s, want %s", got, want)
	}
	want := fmt.Sprintf(`{"iss":%q,"scope":%q,"aud":%q,"exp":%d,"iat":%d}`,
		sa.ClientEmail, Scope, sa.TokenURI, now.Unix()+3600, now.Unix())
	if got := string(dec(1)); got != want {
		t.Errorf("claims %s, want %s", got, want)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&testKey(t).PublicKey, crypto.SHA256, sum[:], dec(2)); err != nil {
		t.Errorf("signature does not verify: %v", err)
	}
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	if _, err := New(nil, Options{}); err == nil {
		t.Error("nil account accepted")
	}
	// A hand-built account is validated and its key parsed; the caller's struct is not modified.
	hand := &ServiceAccount{Type: "service_account", ProjectID: "p", PrivateKeyID: "k",
		PrivateKey: pkcs8(t, testKey(t)), ClientEmail: "a@p.iam.gserviceaccount.com"}
	c, err := New(hand, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if hand.TokenURI != "" || hand.key != nil {
		t.Error("New modified the caller's ServiceAccount")
	}
	if c.ProjectID() != "p" || c.sendURL != "https://fcm.googleapis.com/v1/projects/p/messages:send" {
		t.Errorf("defaults: project %q, url %q", c.ProjectID(), c.sendURL)
	}
	if got := c.ServiceAccount(); got.PrivateKey != "" || got.key != nil || got.ClientEmail != hand.ClientEmail {
		t.Errorf("ServiceAccount() must not expose the key: %#v", got)
	}
	if _, err := New(&ServiceAccount{ProjectID: "p"}, Options{}); err == nil {
		t.Error("incomplete hand-built account accepted")
	}
	for _, ep := range []string{"http://fcm.example.com", "fcm.googleapis.com", "https://x/?a=b", "https://x/#f"} {
		if _, err := New(hand, Options{Endpoint: ep}); err == nil {
			t.Errorf("endpoint %q accepted", ep)
		}
	}
	for _, ep := range []string{"http://127.0.0.1:1234/", "http://localhost:1", "http://[::1]:2", "https://fcm.example.com/base"} {
		if _, err := New(hand, Options{Endpoint: ep}); err != nil {
			t.Errorf("endpoint %q rejected: %v", ep, err)
		}
	}
	c, _ = New(hand, Options{Endpoint: "https://fcm.example.com/base/"})
	if c.sendURL != "https://fcm.example.com/base/v1/projects/p/messages:send" {
		t.Errorf("endpoint path not kept: %s", c.sendURL)
	}
}
