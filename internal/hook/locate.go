package hook

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// SenderMRI extracts the sender's id from a Teams avatar URL. Both layouts occur (measured):
//
//	personal: https://teams.live.com/api/mt/beta/users/8:live:.cid.0123/profilepicturev2?…
//	work:     https://teams.cloud.microsoft/api/mt/part/zz-01/beta/users/<tenant GUID>/profilepicturev2/8:orgid:<GUID>?…
//
// so the id is whichever path segment looks like one. It returns "" if none does.
func SenderMRI(icon string) string {
	u, err := url.Parse(icon)
	if err != nil {
		return ""
	}
	for _, seg := range strings.Split(u.EscapedPath(), "/") {
		mri, err := url.PathUnescape(seg)
		if err == nil && mriRE.MatchString(mri) {
			return mri
		}
	}
	return ""
}

// mriRE is a user id: "8:orgid:<GUID>", "8:live:.cid.<hex>", "28:<bot id>"…
var mriRE = regexp.MustCompile(`^\d{1,2}:[A-Za-z]+:[A-Za-z0-9._:@-]{2,200}$`)

// probeStrings keeps at most 20 strings of at most 240 runes from a lenient JSON array.
func probeStrings(raw json.RawMessage) []string {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		var s string
		if json.Unmarshal(it, &s) != nil {
			continue
		}
		if s = capRunes(clean(&s), 240); s != "" {
			out = append(out, s)
		}
		if len(out) == 20 {
			break
		}
	}
	return out
}
