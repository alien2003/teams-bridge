package browser

import (
	"strings"
	"testing"
)

func TestUserAgent(t *testing.T) {
	got := UserAgent(141)
	want := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0"
	if got != want {
		t.Fatalf("UserAgent(141) =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(got, "Windows NT 10.0") || !strings.Contains(got, "Edg/141") {
		t.Error("UA missing expected markers")
	}
}

func TestUserAgentMetadata(t *testing.T) {
	m := UserAgentMetadata("141.0.3537.57")
	if m.Platform != "Windows" || m.PlatformVersion != "19.0.0" {
		t.Errorf("platform %q / version %q", m.Platform, m.PlatformVersion)
	}
	if m.Architecture != "x86" || m.Bitness != "64" || m.Model != "" {
		t.Errorf("arch/bitness/model: %q %q %q", m.Architecture, m.Bitness, m.Model)
	}
	if m.Mobile || m.Wow64 {
		t.Errorf("mobile %v wow64 %v", m.Mobile, m.Wow64)
	}
	findBrand := func(name string, full bool) (string, bool) {
		src := m.Brands
		if full {
			src = m.FullVersionList
		}
		for _, b := range src {
			if b.Brand == name {
				return b.Version, true
			}
		}
		return "", false
	}
	for _, name := range []string{"Microsoft Edge", "Chromium", greaseBrand} {
		if _, ok := findBrand(name, false); !ok {
			t.Errorf("Brands missing %q", name)
		}
		if _, ok := findBrand(name, true); !ok {
			t.Errorf("FullVersionList missing %q", name)
		}
	}
	if v, _ := findBrand("Microsoft Edge", false); v != "141" {
		t.Errorf("Brands Edge version = %q, want major 141", v)
	}
	if v, _ := findBrand("Microsoft Edge", true); v != "141.0.3537.57" {
		t.Errorf("FullVersionList Edge version = %q, want full", v)
	}
}

func TestUserAgentMetadataEmptyVersion(t *testing.T) {
	m := UserAgentMetadata("")
	if v, _ := func() (string, bool) {
		for _, b := range m.Brands {
			if b.Brand == "Chromium" {
				return b.Version, true
			}
		}
		return "", false
	}(); v != "0" {
		t.Errorf("empty version should yield major 0, got %q", v)
	}
}

func TestLooksLikeChromiumUA(t *testing.T) {
	if !looksLikeChromiumUA(UserAgent(141)) {
		t.Error("Edge UA should look like Chromium")
	}
	if looksLikeChromiumUA("Mozilla/5.0 (X11; Linux) Gecko/20100101 Firefox/120.0") {
		t.Error("Firefox UA should not look like Chromium")
	}
}

func TestEffectiveUserAgent(t *testing.T) {
	if got := EffectiveUserAgent("Custom/1.0", 150); got != "Custom/1.0" {
		t.Errorf("override: %q", got)
	}
	if got := EffectiveUserAgent("", 150); got != UserAgent(150) {
		t.Errorf("templated: %q", got)
	}
	if got := EffectiveUserAgent("", 0); got != "" {
		t.Errorf("unknown version: %q", got)
	}
}
