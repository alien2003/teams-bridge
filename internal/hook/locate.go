package hook

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// SenderMRI extracts the sender's id from a Teams avatar URL such as
// https://teams.live.com/api/mt/beta/users/8:live:.cid.0123/profilepicturev2?… or
// https://teams.microsoft.com/api/mt/zz/beta/users/8:orgid:<guid>/profilepicturev2. It returns ""
// if the URL does not look like that.
func SenderMRI(icon string) string {
	u, err := url.Parse(icon)
	if err != nil {
		return ""
	}
	segs := strings.Split(u.EscapedPath(), "/")
	for i := 0; i+1 < len(segs); i++ {
		if segs[i] != "users" {
			continue
		}
		mri, err := url.PathUnescape(segs[i+1])
		if err == nil && mriRE.MatchString(mri) {
			return mri
		}
	}
	return ""
}

var mriRE = regexp.MustCompile(`^\d{1,2}:[A-Za-z0-9._:@-]{3,200}$`)

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
