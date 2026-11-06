// Package hook holds the browser-side notification hook: the JavaScript injected into every Teams
// document (and service worker), and the Go side that parses and validates the events the hook
// sends back over the CDP binding.
//
// The binding is callable by any script on the Teams page, so a binding payload is untrusted
// input. [Parse] enforces a strict schema and hard size caps before the daemon ever acts on it.
package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// BindingName is the name of the CDP binding (Runtime.addBinding) that the injected JavaScript
// calls. The global function window.__tbd / self.__tbd carries one JSON string argument.
const BindingName = "__tbd"

// Size and value caps for an untrusted payload (BRIEF §5, §2 "binding payloads are untrusted").
const (
	// MaxPayloadBytes is the largest binding payload Parse accepts; larger payloads are rejected.
	MaxPayloadBytes = 64 * 1024
	maxTitleRunes   = 512
	maxBodyRunes    = 8192
	maxTagRunes     = 512
	maxURLBytes     = 4096
	maxDataBytes    = 16 * 1024
	maxCount        = 1_000_000
)

// Kinds reported by the hook.
const (
	KindNotification = "notification"
	KindTitle        = "title"
	KindHooked       = "hooked"
)

// Sources a notification can come from.
const (
	SourceWindow   = "window"    // window.Notification in a document
	SourceSWPage   = "sw-page"   // ServiceWorkerRegistration.showNotification called from a page
	SourceSWWorker = "sw-worker" // self.registration.showNotification inside a service worker
)

// Event is one validated message from the hook. Every string field is valid UTF-8 and within its
// cap. Data is the raw (already length-checked) JSON value of the notification's options.data, or
// nil when absent or dropped.
type Event struct {
	Kind        string          // notification | title | hooked
	Source      string          // window | sw-page | sw-worker
	Title       string          // notification title (capped)
	Body        string          // notification body (capped)
	Tag         string          // notification tag (capped)
	Icon        string          // notification icon URL (dropped if over the cap)
	Href        string          // the document/scope URL the hook reported from
	Data        json.RawMessage // notification options.data, verbatim JSON (dropped if too large)
	DataDropped bool            // true when options.data was present but too large or not serialisable
	Count       int             // unread count for a "title" event
	OptionKeys  []string        // names (not values) of the notification options the page passed
}

// ErrTooLarge is returned by Parse for a payload larger than MaxPayloadBytes.
var ErrTooLarge = errors.New("hook: payload too large")

// wire is the on-the-wire shape. Pointers distinguish absent from empty, and the typed fields make
// json.Unmarshal reject a wrong type (a number where a string is required, etc.).
type wire struct {
	Kind   *string         `json:"kind"`
	Source *string         `json:"source"`
	Title  *string         `json:"title"`
	Body   *string         `json:"body"`
	Tag    *string         `json:"tag"`
	Icon   *string         `json:"icon"`
	Href   *string         `json:"href"`
	Data   json.RawMessage `json:"data"`
	Count  json.RawMessage `json:"count"`
	// DataError is set by the hook when options.data could not be serialised or was too large.
	DataError json.RawMessage `json:"dataError"`
	// OptionKeys: names of the options object (lenient: anything but short strings is ignored).
	OptionKeys json.RawMessage `json:"optionKeys"`
}

// Parse validates and decodes an untrusted binding payload. Unknown fields are ignored; invalid
// UTF-8 in strings is replaced; over-cap strings are truncated (icon and oversized data are
// dropped rather than truncated). It returns an error only for a payload that is too large, is not
// a JSON object, or has an unknown/missing kind or a malformed count.
func Parse(payload string) (Event, error) {
	if len(payload) > MaxPayloadBytes {
		return Event{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(payload))
	}
	if i := firstNonSpace(payload); i < 0 || payload[i] != '{' {
		return Event{}, errors.New("hook: payload is not a JSON object")
	}
	var w wire
	dec := json.NewDecoder(strings.NewReader(payload))
	if err := dec.Decode(&w); err != nil {
		return Event{}, fmt.Errorf("hook: decode: %w", err)
	}
	if dec.More() {
		return Event{}, errors.New("hook: trailing data after the JSON object")
	}

	if w.Kind == nil {
		return Event{}, errors.New("hook: missing kind")
	}
	ev := Event{Kind: *w.Kind}
	switch ev.Kind {
	case KindNotification, KindTitle, KindHooked:
	default:
		return Event{}, fmt.Errorf("hook: unknown kind %q", ev.Kind)
	}

	ev.Source = SourceWindow
	if w.Source != nil {
		switch *w.Source {
		case SourceWindow, SourceSWPage, SourceSWWorker:
			ev.Source = *w.Source
		default:
			// An unrecognised source is not fatal; fall back to the default and ignore it.
		}
	}

	ev.Title = capRunes(clean(w.Title), maxTitleRunes)
	ev.Body = capRunes(clean(w.Body), maxBodyRunes)
	ev.Tag = capRunes(clean(w.Tag), maxTagRunes)
	ev.Href = capBytes(clean(w.Href), maxURLBytes)
	if icon := clean(w.Icon); len(icon) <= maxURLBytes {
		ev.Icon = icon
	}

	ev.OptionKeys = optionKeys(w.OptionKeys)

	if strings.TrimSpace(string(w.DataError)) == "true" {
		ev.DataDropped = true
	}
	if len(w.Data) > 0 && !isJSONNull(w.Data) {
		if len(w.Data) > maxDataBytes {
			ev.DataDropped = true
		} else {
			// Copy so the Event does not alias the decoder's buffer.
			ev.Data = append(json.RawMessage(nil), w.Data...)
		}
	}

	if len(w.Count) > 0 && !isJSONNull(w.Count) {
		n, err := parseCount(w.Count)
		if err != nil {
			return Event{}, err
		}
		ev.Count = n
	}
	return ev, nil
}

// parseCount requires a non-negative JSON integer no greater than maxCount.
func parseCount(raw json.RawMessage) (int, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0, errors.New("hook: empty count")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("hook: count %q is not a non-negative integer", s)
		}
	}
	// encoding/json already rejected malformed numbers (leading zeros, "+1"); cap explicitly.
	var n int
	for _, r := range s {
		n = n*10 + int(r-'0')
		if n > maxCount {
			return 0, fmt.Errorf("hook: count exceeds %d", maxCount)
		}
	}
	return n, nil
}

func clean(p *string) string {
	if p == nil {
		return ""
	}
	if utf8.ValidString(*p) {
		return *p
	}
	return strings.ToValidUTF8(*p, "�")
}

func capRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	i, n := 0, 0
	for i = range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

func capBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Trim to a rune boundary at or below max.
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

func firstNonSpace(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
		default:
			return i
		}
	}
	return -1
}

func isJSONNull(b json.RawMessage) bool {
	return strings.TrimSpace(string(b)) == "null"
}

// optionKeys keeps at most 32 option names of at most 40 runes; anything malformed is ignored (it
// is diagnostics, never a reason to drop a notification).
func optionKeys(raw json.RawMessage) []string {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		var k string
		if json.Unmarshal(it, &k) != nil {
			continue
		}
		if k = capRunes(clean(&k), 40); k != "" {
			out = append(out, k)
		}
		if len(out) == 32 {
			break
		}
	}
	return out
}

// Shape masks s for structure-only logging: letters become 'a', digits '9', whitespace ' ', and
// ASCII punctuation is kept, so "19:abc@thread.v2" shows as "99:aaa@aaaaaa.a9" without revealing
// ids, names or text. Longer than max runes, the rest is summarised as "…(+N)".
func Shape(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == max {
			return b.String() + fmt.Sprintf("…(+%d)", utf8.RuneCountInString(s)-max)
		}
		switch {
		case unicode.IsLetter(r):
			b.WriteByte('a')
		case unicode.IsDigit(r):
			b.WriteByte('9')
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		case r < 0x80 && unicode.IsPunct(r) || r < 0x80 && unicode.IsSymbol(r):
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
		n++
	}
	return b.String()
}
