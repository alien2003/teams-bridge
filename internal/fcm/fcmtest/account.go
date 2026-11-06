package fcmtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/alien2003/teams-bridge/internal/config"
)

// ServiceAccountFile is the file name SetupHome writes into the data directory. The .sa.json
// suffix is gitignored.
const ServiceAccountFile = "fcm-test.sa.json"

// serviceAccountJSON has the field order and set of a key file downloaded from Google.
type serviceAccountJSON struct {
	Type                    string `json:"type"`
	ProjectID               string `json:"project_id"`
	PrivateKeyID            string `json:"private_key_id"`
	PrivateKey              string `json:"private_key"`
	ClientEmail             string `json:"client_email"`
	ClientID                string `json:"client_id"`
	AuthURI                 string `json:"auth_uri"`
	TokenURI                string `json:"token_uri"`
	AuthProviderX509CertURL string `json:"auth_provider_x509_cert_url"`
	ClientX509CertURL       string `json:"client_x509_cert_url"`
	UniverseDomain          string `json:"universe_domain"`
}

// GenerateServiceAccount returns a service-account key file with a fresh RSA-2048 key, and that
// key, without registering it anywhere (use it to test a key Google does not know).
func GenerateServiceAccount(projectID, email, tokenURI string) ([]byte, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	sa := serviceAccountJSON{
		Type:                    "service_account",
		ProjectID:               projectID,
		PrivateKeyID:            randomHex(20),
		PrivateKey:              string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		ClientEmail:             email,
		ClientID:                randomDigits(21),
		AuthURI:                 "https://accounts.google.com/o/oauth2/auth",
		TokenURI:                tokenURI,
		AuthProviderX509CertURL: "https://www.googleapis.com/oauth2/v1/certs",
		ClientX509CertURL:       "https://www.googleapis.com/robot/v1/metadata/x509/" + url.PathEscape(email),
		UniverseDomain:          "googleapis.com",
	}
	b, err := json.MarshalIndent(sa, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return append(b, '\n'), key, nil
}

func randomDigits(n int) string {
	b := []byte(randomHex(n))
	for i := range b {
		b[i] = '0' + b[i]%10
	}
	if b[0] == '0' {
		b[0] = '1'
	}
	return string(b)
}

// NewServiceAccount generates a service account with a fresh RSA-2048 key, registers its public
// key with the server and returns the key file JSON (token_uri = TokenURL()).
func (s *Server) NewServiceAccount() []byte { return s.NewServiceAccountFor(s.TokenURL()) }

// NewServiceAccountFor is NewServiceAccount with another token_uri, e.g. GoogleTokenURL for a
// client whose HTTP transport is Transport().
func (s *Server) NewServiceAccountFor(tokenURI string) []byte {
	s.t.Helper()
	s.mu.Lock()
	s.nAccounts++
	email := fmt.Sprintf("tbd-test-%d@%s.iam.gserviceaccount.com", s.nAccounts, s.ProjectID)
	s.mu.Unlock()
	b, key, err := GenerateServiceAccount(s.ProjectID, email, tokenURI)
	if err != nil {
		s.t.Fatalf("fcmtest: generating a service account: %v", err)
	}
	var sa serviceAccountJSON
	if err := json.Unmarshal(b, &sa); err != nil {
		s.t.Fatalf("fcmtest: %v", err)
	}
	s.mu.Lock()
	s.accounts[email] = &account{email: email, kid: sa.PrivateKeyID, tokenURI: tokenURI, pub: &key.PublicKey}
	s.mu.Unlock()
	return b
}

// RevokeServiceAccounts forgets every registered account (as if their keys were deleted):
// further token requests get invalid_grant. Already issued access tokens stay valid.
func (s *Server) RevokeServiceAccounts() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.accounts)
}

// Configure points cfg at the server: a fresh service account inline in fcm_service_account and
// fcm_endpoint = URL. Nothing is written to disk.
func (s *Server) Configure(cfg *config.Config) {
	cfg.FCMServiceAccount = string(compactJSON(s.t, s.NewServiceAccount()))
	cfg.FCMEndpoint = s.URL
}

// SetupHome prepares a data directory (TBD_HOME) for a daemon that should send to this server:
// it writes a fresh service-account key file to home/ServiceAccountFile (mode 0600), sets
// fcm_service_account to that file and fcm_endpoint to URL, and saves home/config.toml. cfg
// nil means the existing config.toml in home, or the defaults. The other settings (secret,
// browser, …) are left as they are. It returns the saved config.
func (s *Server) SetupHome(home string, cfg *config.Config) *config.Config {
	s.t.Helper()
	if cfg == nil {
		c, _, err := config.Load(home)
		if err != nil {
			s.t.Fatalf("fcmtest: SetupHome: %v", err)
		}
		cfg = c
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		s.t.Fatalf("fcmtest: SetupHome: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ServiceAccountFile), s.NewServiceAccount(), 0o600); err != nil {
		s.t.Fatalf("fcmtest: SetupHome: %v", err)
	}
	cfg.FCMServiceAccount = ServiceAccountFile
	cfg.FCMEndpoint = s.URL
	if err := config.Save(home, cfg); err != nil {
		s.t.Fatalf("fcmtest: SetupHome: %v", err)
	}
	return cfg
}

func compactJSON(t interface{ Fatalf(string, ...any) }, b []byte) []byte {
	var v json.RawMessage
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("fcmtest: %v", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("fcmtest: %v", err)
	}
	return out
}
