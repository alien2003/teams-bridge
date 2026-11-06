package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State is <home>/state.json: what the daemon last did, for `tbd status` and for alert debouncing
// across restarts. It never contains message content.
type State struct {
	PID           int                  `json:"pid,omitempty"`
	StartedAt     time.Time            `json:"started_at,omitempty"`
	LastHeartbeat time.Time            `json:"last_heartbeat,omitempty"`
	LastMessage   time.Time            `json:"last_message,omitempty"`
	LastEvent     time.Time            `json:"last_event,omitempty"`
	Messages      int                  `json:"messages_sent"`
	LastAlerts    map[string]time.Time `json:"last_alerts,omitempty"`
	LastError     string               `json:"last_error,omitempty"`
	BrowserPath   string               `json:"browser,omitempty"`
	BrowserVer    string               `json:"browser_version,omitempty"`
	SessionOK     bool                 `json:"session_ok"`
	UnreadCount   int                  `json:"unread_count"`
}

// StatePath is state.json inside home.
func StatePath(home string) string { return filepath.Join(home, "state.json") }

// LoadState reads state.json (zero value if missing or unreadable).
func LoadState(home string) State {
	var s State
	if b, err := os.ReadFile(StatePath(home)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

type stateStore struct {
	mu   sync.Mutex
	home string
	s    State
}

func (st *stateStore) update(fn func(*State)) {
	st.mu.Lock()
	defer st.mu.Unlock()
	fn(&st.s)
	b, err := json.MarshalIndent(st.s, "", "  ")
	if err != nil {
		return
	}
	tmp := StatePath(st.home) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, StatePath(st.home))
	}
}

func (st *stateStore) get() State {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.s
	s.LastAlerts = make(map[string]time.Time, len(st.s.LastAlerts))
	for k, v := range st.s.LastAlerts {
		s.LastAlerts[k] = v
	}
	return s
}
