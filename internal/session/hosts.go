package session

import (
	"net/url"
	"strings"
)

// normHost lowercases a host name and drops a trailing dot ("Login.Live.COM." → "login.live.com").
func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".") }

// hostPattern is "x" (exactly x) or "*.x" (x and every subdomain of x); with subdomains set, a
// plain "x" also matches its subdomains.
type hostPattern struct {
	base string
	sub  bool
}

func compilePatterns(in []string, plainMatchesSubdomains bool) []hostPattern {
	var out []hostPattern
	for _, p := range in {
		p = normHost(p)
		sub := plainMatchesSubdomains
		if rest, ok := strings.CutPrefix(p, "*."); ok {
			p, sub = rest, true
		}
		if p != "" {
			out = append(out, hostPattern{base: p, sub: sub})
		}
	}
	return out
}

func matchHost(host string, pats []hostPattern) bool {
	if host == "" {
		return false
	}
	for _, p := range pats {
		if host == p.base || (p.sub && strings.HasSuffix(host, "."+p.base)) {
			return true
		}
	}
	return false
}

// origin is scheme://host[:port] with the default port dropped, lowercased; "" if u is not
// an absolute http(s) URL.
func origin(u *url.URL) string {
	if u == nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := normHost(u.Hostname())
	if host == "" {
		return ""
	}
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + host + "]"
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + host + ":" + port
	}
	return scheme + "://" + host
}

func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return origin(u)
}

// hostOf is the normalised host of an absolute URL, "" if raw has none.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return normHost(u.Hostname())
}

// safeHost keeps a host printable in an alert reason (it came from page-controlled data).
func safeHost(h string) string {
	if len(h) > 100 {
		h = h[:100]
	}
	ok := true
	for _, r := range h {
		if !(r == '.' || r == '-' || r == ':' || r == '[' || r == ']' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			ok = false
			break
		}
	}
	if !ok || h == "" {
		return "an unexpected host"
	}
	return h
}
