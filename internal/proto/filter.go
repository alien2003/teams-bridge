package proto

import "time"

// Receiver rules (PROTOCOL.md §7).
const (
	MaxAge     = 24 * time.Hour
	ReplaySize = 256
)

// Filter applies the receiver rules: drop messages older than MaxAge and the last ReplaySize ids.
// The Go daemon never receives; this mirrors the Android implementation for tests and docs.
type Filter struct {
	ids  []string
	seen map[string]struct{}
}

// Accept reports whether m should be shown, and remembers its id if so.
func (f *Filter) Accept(m Message, now time.Time) bool {
	// Compare in Unix seconds: time.Unix on an arbitrary ts can overflow and wrap into the past.
	if cut := now.Add(-MaxAge); m.TS < cut.Unix() || (m.TS == cut.Unix() && cut.Nanosecond() > 0) {
		return false
	}
	if f.seen == nil {
		f.seen = make(map[string]struct{}, ReplaySize)
	}
	if _, dup := f.seen[m.ID]; dup {
		return false
	}
	if len(f.ids) == ReplaySize {
		delete(f.seen, f.ids[0])
		f.ids = f.ids[1:]
	}
	f.ids = append(f.ids, m.ID)
	f.seen[m.ID] = struct{}{}
	return true
}
