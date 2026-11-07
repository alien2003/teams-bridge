package browser

import (
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/emulation"
)

// Client-hint platform values as current Edge on Windows 11 sends them. Windows 11 24H2 (build
// 26100) reports platformVersion "19.0.0" via User-Agent Client Hints, because the UA-CH platform
// version maps the real Windows build to the 13.0.0+ range. Architecture "x86"
// with bitness "64" is how 64-bit Windows is described (x86 is the CPU family, not the word size).
const (
	platform        = "Windows"
	platformVersion = "19.0.0"
	architecture    = "x86"
	bitness         = "64"
)

// greaseBrand is a stable GREASE entry (the "Not ... Brand" the UA-CH spec reserves to keep parsers
// honest). A fixed value keeps the override deterministic.
const greaseBrand = "Not=A?Brand"

// UserAgent returns the reduced Windows-11 Edge user-agent string for the given Chromium major
// version, in the exact form current Edge on Windows sends it.
func UserAgent(major int) string {
	return fmt.Sprintf(
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36 Edg/%d.0.0.0",
		major, major)
}

// EffectiveUserAgent is the user agent the daemon presents: the config override if set, else the
// templated Windows Edge UA for the browser's major version, else "" (version unknown: the session
// then templates it from CDP Browser.getVersion, but no browser-wide --user-agent can be set before
// launch). Pass it as LaunchOptions.UserAgent / LoginOptions.UserAgent.
func EffectiveUserAgent(override string, major int) string {
	if override != "" {
		return override
	}
	if major > 0 {
		return UserAgent(major)
	}
	return ""
}

// UserAgentMetadata returns the client-hint metadata (userAgentData) to send alongside the UA
// string, built from the browser's full version (e.g. "141.0.3537.57"). The brand lists include
// "Chromium", "Microsoft Edge" and a GREASE brand, in both Brands (major-only, for Sec-CH-UA) and
// FullVersionList (full version, for Sec-CH-UA-Full-Version-List). Platform is Windows.
func UserAgentMetadata(fullVersion string) *emulation.UserAgentMetadata {
	major := fullVersion
	if i := strings.IndexByte(fullVersion, '.'); i > 0 {
		major = fullVersion[:i]
	}
	if major == "" {
		major = "0"
	}
	if fullVersion == "" {
		fullVersion = major + ".0.0.0"
	}
	return &emulation.UserAgentMetadata{
		Brands: []*emulation.UserAgentBrandVersion{
			{Brand: "Chromium", Version: major},
			{Brand: "Microsoft Edge", Version: major},
			{Brand: greaseBrand, Version: "24"},
		},
		FullVersionList: []*emulation.UserAgentBrandVersion{
			{Brand: "Chromium", Version: fullVersion},
			{Brand: "Microsoft Edge", Version: fullVersion},
			{Brand: greaseBrand, Version: "24.0.0.0"},
		},
		Platform:        platform,
		PlatformVersion: platformVersion,
		Architecture:    architecture,
		Bitness:         bitness,
		Model:           "",
		Mobile:          false,
		Wow64:           false,
	}
}

// looksLikeChromiumUA reports whether a full UA-string override is still a Chrome/Edge UA, so that
// the Windows client-hint metadata stays consistent with it.
func looksLikeChromiumUA(ua string) bool {
	return strings.Contains(ua, "Chrome/") || strings.Contains(ua, "Edg/")
}

// uaPlatformFor is the navigator.platform to present on goos together with the Windows client
// hints: "Win32" on macOS, where the browser would otherwise report "MacIntel" next to a Windows
// UA, a combination that was never tested against Conditional Access; empty (the browser's own)
// elsewhere. Linux keeps its own "Linux x86_64", the combination proven in production, and Windows
// already says "Win32" (docs/DECISIONS.md D8, D26).
func uaPlatformFor(goos string) string {
	if goos == "darwin" {
		return "Win32"
	}
	return ""
}

// uaOverrideFor returns the Emulation/Network.setUserAgentOverride parameters for ua and meta on
// goos. The platform is overridden only together with the Windows metadata: a user's own non-Chromium
// UA gets neither.
func uaOverrideFor(goos, ua string, meta *emulation.UserAgentMetadata) emulation.SetUserAgentOverrideParams {
	p := emulation.SetUserAgentOverrideParams{UserAgent: ua, UserAgentMetadata: meta}
	if meta != nil {
		p.Platform = uaPlatformFor(goos)
	}
	return p
}
