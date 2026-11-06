package fcm

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
)

// DefaultTokenURI is Google's OAuth 2.0 token endpoint, used when the service account has none.
const DefaultTokenURI = "https://oauth2.googleapis.com/token"

// minKeyBits is the smallest RSA key accepted; Google issues 2048-bit service-account keys.
const minKeyBits = 2048

// ServiceAccount is the subset of a Google service-account key file (the JSON downloaded from the
// Firebase console, "Project settings → Service accounts → Generate new private key") that the
// JWT bearer flow needs. Its String, GoString and LogValue methods never reveal PrivateKey.
type ServiceAccount struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`

	key *rsa.PrivateKey
}

// String identifies the account without its key.
func (sa ServiceAccount) String() string {
	return fmt.Sprintf("service account %s (project %s, key id %s)", sa.ClientEmail, sa.ProjectID, sa.PrivateKeyID)
}

// GoString keeps %#v from printing the private key.
func (sa ServiceAccount) GoString() string { return "fcm.ServiceAccount{" + sa.String() + "}" }

// LogValue keeps slog from printing the private key.
func (sa ServiceAccount) LogValue() slog.Value {
	return slog.GroupValue(slog.String("client_email", sa.ClientEmail), slog.String("project_id", sa.ProjectID),
		slog.String("private_key_id", sa.PrivateKeyID))
}

// ParseServiceAccount parses and validates a service-account key file. Errors never contain the
// private key (nor any other part of the input beyond field names and short values).
func ParseServiceAccount(b []byte) (*ServiceAccount, error) {
	var sa ServiceAccount
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&sa); err != nil {
		return nil, jsonError(err)
	}
	// dec.More() would miss a trailing "}" or "]"; check the rest of the input directly.
	if len(bytes.TrimSpace(b[dec.InputOffset():])) != 0 {
		return nil, errors.New("fcm: service account: trailing data after the JSON object")
	}
	if sa.Type == "" && looksLikeGoogleServices(b) {
		return nil, errors.New("fcm: service account: this is google-services.json (the Android app config); " +
			"fcm_service_account needs the service-account key from Firebase console → Project settings → Service accounts")
	}
	if err := sa.prepare(); err != nil {
		return nil, err
	}
	return &sa, nil
}

// prepare validates every field, defaults TokenURI and parses the key.
func (sa *ServiceAccount) prepare() error {
	if sa.Type != "service_account" {
		return fmt.Errorf("fcm: service account: type is %.40q, want \"service_account\"", sa.Type)
	}
	for _, f := range []struct{ name, v string }{
		{"project_id", sa.ProjectID}, {"private_key_id", sa.PrivateKeyID},
		{"private_key", sa.PrivateKey}, {"client_email", sa.ClientEmail},
	} {
		if strings.TrimSpace(f.v) == "" {
			return fmt.Errorf("fcm: service account: %s is missing", f.name)
		}
	}
	if !validProjectID(sa.ProjectID) {
		return fmt.Errorf("fcm: service account: project_id %.80q has unexpected characters", sa.ProjectID)
	}
	if !strings.Contains(sa.ClientEmail, "@") {
		return fmt.Errorf("fcm: service account: client_email %.80q is not an e-mail address", sa.ClientEmail)
	}
	if sa.TokenURI == "" {
		sa.TokenURI = DefaultTokenURI
	}
	if err := checkURL("service account token_uri", sa.TokenURI); err != nil {
		return err
	}
	if sa.key == nil {
		k, err := parsePrivateKey(sa.PrivateKey)
		if err != nil {
			return err
		}
		sa.key = k
	}
	return nil
}

// validProjectID accepts Google project ids, including legacy domain-scoped ones ("example.com:x").
// The first character must be a letter or digit, so an id like ".." cannot turn the send URL into
// a dot-segment path.
func validProjectID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}
	if c := id[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
		return false
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == ':' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

func looksLikeGoogleServices(b []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	_, ok := m["project_info"]
	return ok
}

// jsonError reports where the JSON is broken without quoting it.
func jsonError(err error) error {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &se):
		return fmt.Errorf("fcm: service account: invalid JSON near byte %d", se.Offset)
	case errors.As(err, &te):
		return fmt.Errorf("fcm: service account: field %q must be a string", te.Field)
	case errors.Is(err, io.EOF):
		return errors.New("fcm: service account: empty input")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("fcm: service account: truncated JSON")
	}
	return errors.New("fcm: service account: invalid JSON")
}

// parsePrivateKey decodes a PEM PKCS#8 ("PRIVATE KEY") or PKCS#1 ("RSA PRIVATE KEY") RSA key.
// Error messages never include key material or the underlying parser's text.
func parsePrivateKey(s string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil && strings.Contains(s, `\n`) {
		// The key was escaped twice (e.g. pasted from an environment variable): "\\n" in the JSON.
		block, _ = pem.Decode([]byte(strings.ReplaceAll(s, `\n`, "\n")))
	}
	if block == nil {
		return nil, errors.New("fcm: service account: private_key is not a PEM-encoded key")
	}
	if _, enc := block.Headers["Proc-Type"]; enc {
		return nil, errors.New("fcm: service account: private_key is encrypted; use the unencrypted key file Google issued")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("fcm: service account: private_key is not a valid PKCS#8 key")
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("fcm: service account: private_key is a %s key, want RSA", keyKind(k))
		}
		key = rk
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("fcm: service account: private_key is not a valid PKCS#1 RSA key")
		}
		key = k
	default:
		return nil, fmt.Errorf("fcm: service account: private_key has PEM type %.40q, want PRIVATE KEY or RSA PRIVATE KEY", block.Type)
	}
	if bits := key.N.BitLen(); bits < minKeyBits {
		return nil, fmt.Errorf("fcm: service account: private_key is a %d-bit RSA key, want at least %d", bits, minKeyBits)
	}
	return key, nil
}

func keyKind(k any) string {
	switch k.(type) {
	case *ecdsa.PrivateKey:
		return "ECDSA"
	case ed25519.PrivateKey:
		return "Ed25519"
	case *ecdh.PrivateKey:
		return "ECDH"
	}
	return "non-RSA"
}

// checkURL accepts https URLs, and plain http only for loopback hosts (the test mock): a bearer
// token or a signed assertion must never cross a network in clear text.
func checkURL(what, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("fcm: %s is not an absolute URL", what)
	}
	if u.User != nil {
		return fmt.Errorf("fcm: %s must not contain credentials", what)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("fcm: %s: plain http is allowed only for localhost, got host %q", what, u.Hostname())
	}
	return fmt.Errorf("fcm: %s: scheme %q is not https", what, u.Scheme)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
