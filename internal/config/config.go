// Package config loads config.toml from the data directory (TBD_HOME), resolves that directory and
// detects the browser.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/alien2003/teams-bridge/internal/proto"
)

// FileName is the config file inside the data directory.
const FileName = "config.toml"

// Defaults (BRIEF §5).
const (
	DefaultTeamsURL       = "https://teams.microsoft.com/v2/"
	DefaultHeartbeatHours = 6
	DefaultLogLevel       = "info"
	DefaultFCMEndpoint    = "https://fcm.googleapis.com"
)

// DefaultLoginHosts are the Microsoft sign-in hosts; landing on one means the session expired.
var DefaultLoginHosts = []string{"login.microsoftonline.com", "login.live.com", "login.microsoft.com"}

// DefaultAPIHosts are the host patterns whose 401/403 bursts mean the session expired.
// "*.example.com" matches example.com and every subdomain.
var DefaultAPIHosts = []string{"*.teams.microsoft.com", "*.teams.cloud.microsoft"}

// DefaultPermissionOrigins get the notifications permission (BRIEF §5).
var DefaultPermissionOrigins = []string{"https://teams.microsoft.com", "https://teams.cloud.microsoft", "https://teams.live.com"}

// Config is config.toml.
type Config struct {
	// Secret is the pairing secret as its 26-character base32 code. Written by `tbd pair`.
	Secret string `toml:"secret"`
	// FCMServiceAccount is a path to the Firebase service-account JSON (relative paths are relative
	// to the data directory) or the JSON itself.
	FCMServiceAccount string `toml:"fcm_service_account"`
	// Browser is the browser executable; empty means auto-detect.
	Browser string `toml:"browser,omitempty"`
	// BrowserArgs are extra command-line switches for the browser.
	BrowserArgs []string `toml:"browser_args,omitempty"`
	// TeamsURL is the page the daemon keeps open.
	TeamsURL string `toml:"teams_url"`
	// LoginHosts: a main-frame navigation to one of these means the session expired.
	LoginHosts []string `toml:"login_hosts"`
	// APIHosts: host patterns whose XHR/fetch 401/403 bursts mean the session expired.
	APIHosts []string `toml:"api_hosts,omitempty"`
	// UserAgent fully overrides the templated Windows Edge user agent.
	UserAgent string `toml:"user_agent,omitempty"`
	// HeartbeatHours is the heartbeat period.
	HeartbeatHours int `toml:"heartbeat_hours"`
	// LogLevel is debug, info, warn or error.
	LogLevel string `toml:"log_level"`
	// ReportHidden makes the page believe it is in the background (document.hidden, hasFocus),
	// which is what makes Teams raise notifications. Default true.
	ReportHidden *bool `toml:"report_hidden,omitempty"`
	// ImplicitSignin lets Edge sign the daemon's new profile in to the Windows account (off by
	// default). Turn it on only where Conditional Access needs Edge's OS-account SSO (an
	// Entra-joined Windows PC).
	ImplicitSignin bool `toml:"implicit_signin,omitempty"`
	// FCMEndpoint overrides https://fcm.googleapis.com (tests).
	FCMEndpoint string `toml:"fcm_endpoint,omitempty"`
}

// Default returns a config with every default filled in and no secret.
func Default() *Config {
	c := &Config{}
	c.applyDefaults()
	return c
}

func (c *Config) applyDefaults() {
	if c.TeamsURL == "" {
		c.TeamsURL = DefaultTeamsURL
	}
	if c.LoginHosts == nil {
		c.LoginHosts = append([]string(nil), DefaultLoginHosts...)
	}
	if c.APIHosts == nil {
		c.APIHosts = append([]string(nil), DefaultAPIHosts...)
	}
	if c.HeartbeatHours <= 0 {
		c.HeartbeatHours = DefaultHeartbeatHours
	}
	if c.LogLevel == "" {
		c.LogLevel = DefaultLogLevel
	}
	if c.FCMEndpoint == "" {
		c.FCMEndpoint = DefaultFCMEndpoint
	}
}

// Hidden reports the effective report_hidden value.
func (c *Config) Hidden() bool { return c.ReportHidden == nil || *c.ReportHidden }

// Validate checks the values that do not depend on the environment.
func (c *Config) Validate() error {
	u, err := url.Parse(c.TeamsURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("config: teams_url %q is not an http(s) URL", c.TeamsURL)
	}
	if c.Secret != "" {
		if _, err := proto.ParseCode(c.Secret); err != nil {
			return fmt.Errorf("config: secret: %w", err)
		}
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("config: log_level %q (want debug, info, warn or error)", c.LogLevel)
	}
	return nil
}

// TeamsOrigin is scheme://host[:port] of TeamsURL.
func (c *Config) TeamsOrigin() string {
	u, err := url.Parse(c.TeamsURL)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// PermissionOrigins is DefaultPermissionOrigins plus the origin of TeamsURL.
func (c *Config) PermissionOrigins() []string {
	out := append([]string(nil), DefaultPermissionOrigins...)
	if o := c.TeamsOrigin(); o != "" {
		found := false
		for _, x := range out {
			found = found || x == o
		}
		if !found {
			out = append(out, o)
		}
	}
	return out
}

// PairingSecret parses Secret.
func (c *Config) PairingSecret() (proto.Secret, error) {
	if c.Secret == "" {
		return proto.Secret{}, ErrNotPaired
	}
	return proto.ParseCode(c.Secret)
}

// ErrNotPaired means config.toml has no secret yet.
var ErrNotPaired = errors.New("not paired: run `tbd pair`")

// ErrNoServiceAccount means fcm_service_account is empty.
var ErrNoServiceAccount = errors.New("fcm_service_account is not set in config.toml")

// ServiceAccountJSON returns the service-account JSON, reading the file if the value is a path.
func (c *Config) ServiceAccountJSON(home string) ([]byte, error) {
	v := strings.TrimSpace(c.FCMServiceAccount)
	if v == "" {
		return nil, ErrNoServiceAccount
	}
	if strings.HasPrefix(v, "{") {
		return []byte(v), nil
	}
	p := v
	if !filepath.IsAbs(p) {
		p = filepath.Join(home, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("config: fcm_service_account: %w", err)
	}
	return b, nil
}

// Path is the config file inside home.
func Path(home string) string { return filepath.Join(home, FileName) }

// Load reads home/config.toml. A missing file yields the defaults and exists=false.
func Load(home string) (cfg *Config, exists bool, err error) {
	c := &Config{}
	b, err := os.ReadFile(Path(home))
	switch {
	case errors.Is(err, os.ErrNotExist):
		c.applyDefaults()
		return c, false, nil
	case err != nil:
		return nil, false, err
	}
	md, err := toml.Decode(string(b), c)
	if err != nil {
		return nil, true, fmt.Errorf("config: %s: %w", Path(home), err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, true, fmt.Errorf("config: %s: unknown key %q", Path(home), und[0].String())
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, true, err
	}
	return c, true, nil
}

// Save writes home/config.toml atomically with mode 0600 (it holds the pairing secret).
func Save(home string, c *Config) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# tbd configuration. The secret pairs this daemon with the phone; keep it private.\n")
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), Path(home))
}

// ResolveHome picks the data directory: --home, then $TBD_HOME, then the OS default
// (%LOCALAPPDATA%\tbd on Windows, $XDG_DATA_HOME/tbd or ~/.local/share/tbd elsewhere).
func ResolveHome(flagValue string) (string, error) {
	return resolveHome(flagValue, os.Getenv, runtime.GOOS, os.UserHomeDir)
}

func resolveHome(flagValue string, getenv func(string) string, goos string, userHome func() (string, error)) (string, error) {
	pick := func(p string) (string, error) { return filepath.Abs(p) }
	if flagValue != "" {
		return pick(flagValue)
	}
	if v := getenv("TBD_HOME"); v != "" {
		return pick(v)
	}
	if goos == "windows" {
		if v := getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "tbd"), nil
		}
		return "", errors.New("config: LOCALAPPDATA is not set; use --home")
	}
	if v := getenv("XDG_DATA_HOME"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "tbd"), nil
	}
	h, err := userHome()
	if err != nil {
		return "", fmt.Errorf("config: %w; use --home", err)
	}
	return filepath.Join(h, ".local", "share", "tbd"), nil
}
