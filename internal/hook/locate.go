package hook

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
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

// idKeyRE selects the keys whose values are worth a shape in DescribeOccurrences.
var idKeyRE = regexp.MustCompile(`(?i)(conversation|thread|link|^id$|messageid|from|resourcetype|^type$|messagetype|^to$)`)

// DescribeOccurrences describes, by structure only, where needle (or its URL-encoded form) occurs in
// doc, a JSON document possibly behind a short non-JSON prefix (Trouter's "3:::"). For each hit (at
// most max) it gives the key path ([] for array elements), the key names of the object that holds
// it and of its parent, and Shape()s of id-like values next to it. Values themselves are never
// returned. doc that is not JSON yields one "non-json" entry with the hit count.
func DescribeOccurrences(doc, needle string, max int) []string {
	if needle == "" {
		return nil
	}
	enc := url.QueryEscape(needle)
	if !strings.Contains(doc, needle) && !strings.Contains(doc, enc) && !strings.Contains(doc, url.PathEscape(needle)) {
		return nil
	}
	start := strings.IndexAny(doc, "{[")
	var v any
	if start < 0 || start > 32 || json.Unmarshal([]byte(doc[start:]), &v) != nil {
		return []string{fmt.Sprintf("non-json len=%d hits=%d", len(doc), strings.Count(doc, needle)+strings.Count(doc, enc))}
	}
	var out []string
	var walk func(x any, path []string, parent, grand map[string]any)
	walk = func(x any, path []string, parent, grand map[string]any) {
		if len(out) >= max {
			return
		}
		switch t := x.(type) {
		case map[string]any:
			for k, val := range t {
				walk(val, append(path, maskKey(k)), t, parent)
			}
		case []any:
			for _, val := range t {
				walk(val, append(path, "[]"), parent, grand)
			}
		case string:
			if strings.Contains(t, needle) || strings.Contains(t, enc) {
				out = append(out, describeHit(path, parent, grand))
			}
		}
	}
	walk(v, nil, nil, nil)
	return out
}

func describeHit(path []string, obj, parent map[string]any) string {
	var b strings.Builder
	b.WriteString("path=" + strings.Join(path, "."))
	if obj != nil {
		b.WriteString(" keys=" + keyList(obj))
		b.WriteString(" ids={" + idShapes(obj) + "}")
	}
	if parent != nil {
		b.WriteString(" parent_keys=" + keyList(parent))
		b.WriteString(" parent_ids={" + idShapes(parent) + "}")
	}
	return b.String()
}

func keyList(m map[string]any) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, maskKey(k))
	}
	sort.Strings(ks)
	if len(ks) > 30 {
		ks = append(ks[:30], fmt.Sprintf("…(+%d)", len(ks)-30))
	}
	return strings.Join(ks, ",")
}

func idShapes(m map[string]any) string {
	var parts []string
	for k, v := range m {
		if !idKeyRE.MatchString(k) {
			continue
		}
		switch t := v.(type) {
		case string:
			parts = append(parts, k+"="+Shape(t, 160))
		case float64, bool:
			parts = append(parts, fmt.Sprintf("%s=<%T>", k, t))
		case map[string]any:
			parts = append(parts, k+"=<object>")
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// maskKey hides keys that are themselves ids (maps keyed by conversation or user id).
func maskKey(k string) string {
	if len(k) > 40 || strings.ContainsAny(k, ":@/ ") {
		return "<" + Shape(k, 24) + ">"
	}
	return k
}

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
