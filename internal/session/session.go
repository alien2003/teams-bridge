// Package session watches the health of the Teams session and of the browser that hosts it
// (BRIEF §5 "Session detection", "Browser" restart loop) and schedules heartbeats.
//
// It is pure logic with an injected clock: no browser, no network. The daemon feeds it CDP
// events and probe results and sends the alerts it emits.
//
//   - [Detector] implements the four expiry detectors: (1) main-frame navigation to a login host,
//     (2) bursts of 401/403 from Teams API calls, (3) the periodic page probe ([ProbeJS]),
//     (4) browser/CDP loss. It emits [proto.KindSessionExpired] alerts. Keep one Detector for
//     the daemon's lifetime (not one per browser run, or Expired() resets with every restart;
//     BrowserLost already resets the per-run state) and Close it on shutdown.
//   - [FailureTracker] and [Backoff] drive the browser restart loop and emit
//     [proto.KindBrowserDead] after N consecutive failed runs.
//   - [Debouncer] limits alerts to one per kind per 6 h. Detector and FailureTracker emit through
//     it, so the limit holds end to end; share one Debouncer between them. [Debouncer.Seed]
//     carries the record over from a previous daemon process.
//   - [RunHeartbeat] and [NextHeartbeat] schedule the "hb" message.
//
// Times are compared on the wall clock (monotonic readings are stripped), so a computer that
// slept for hours does not stretch the 6 h debounce, the heartbeat period or the "healthy run"
// period.
package session

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alien2003/teams-bridge/internal/proto"
)

// DefaultDebounce is the minimum time between two alerts of the same kind (BRIEF §5).
const DefaultDebounce = 6 * time.Hour

// Alert is a condition the phone must hear about. Reason is short and never contains user
// content (no titles, bodies, chat ids or URL paths); hosts and counts are fine.
type Alert struct {
	Kind   string // proto.KindSessionExpired, proto.KindBrowserDead or proto.KindDaemonStopping
	Reason string
	At     time.Time
}

// Title is the notification title the phone shows for an alert kind.
func Title(kind string) string {
	switch kind {
	case proto.KindSessionExpired:
		return "Teams session expired: run tbd login"
	case proto.KindBrowserDead:
		return "Teams Bridge: the browser keeps failing"
	case proto.KindDaemonStopping:
		return "Teams Bridge daemon stopped"
	}
	return "Teams Bridge alert"
}

// Message builds the "alert" protocol message for a, with a fresh random id.
func (a Alert) Message() (proto.Message, error) {
	id, err := proto.NewID(nil)
	if err != nil {
		return proto.Message{}, err
	}
	at := a.At
	if at.IsZero() {
		at = time.Now()
	}
	return proto.Message{
		V:     proto.Version,
		T:     proto.TypeAlert,
		ID:    id,
		TS:    at.Unix(),
		Title: Title(a.Kind),
		Body:  a.Reason,
		Kind:  a.Kind,
	}, nil
}

// Debouncer lets at most one alert per kind through per period. It is safe for concurrent use.
// Kinds are independent: daemon_stopping is never held back by an earlier session_expired.
type Debouncer struct {
	period time.Duration
	now    func() time.Time
	mu     sync.Mutex
	last   map[string]time.Time
}

// NewDebouncer returns a Debouncer. period 0 means DefaultDebounce; a negative period disables
// debouncing (every alert passes; for tests). now nil means time.Now.
func NewDebouncer(period time.Duration, now func() time.Time) *Debouncer {
	if period == 0 {
		period = DefaultDebounce
	}
	if now == nil {
		now = time.Now
	}
	return &Debouncer{period: period, now: now, last: map[string]time.Time{}}
}

// Allow reports whether an alert of kind may be sent now and, if so, records it as sent.
// The next one of the same kind passes once period has elapsed (exactly period is enough).
// If the wall clock went backwards past the last alert, the alert passes.
func (d *Debouncer) Allow(kind string) bool {
	if d.period < 0 {
		return true
	}
	now := wall(d.now())
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.last[kind]; ok && !now.Before(t) && now.Sub(t) < d.period {
		return false
	}
	d.last[kind] = now
	return true
}

// Seed records that an alert of kind was sent at at, typically by an earlier daemon process
// (from saved state), so that the period also spans daemon restarts: a crash loop under
// systemd's Restart=always or the scheduled task's restart-on-failure then does not re-send
// the same alert on every start. A zero at, or one older than what is already recorded, is
// ignored. An at in the future (the clock went back) does not block: Allow lets it pass.
func (d *Debouncer) Seed(kind string, at time.Time) {
	if at.IsZero() {
		return
	}
	at = wall(at)
	d.mu.Lock()
	if t, ok := d.last[kind]; !ok || at.After(t) {
		d.last[kind] = at
	}
	d.mu.Unlock()
}

// Forget drops the record of kind, so the next alert of that kind passes. A caller whose send
// finally failed (FCM unreachable) can use it to have the condition reported again.
func (d *Debouncer) Forget(kind string) {
	d.mu.Lock()
	delete(d.last, kind)
	d.mu.Unlock()
}

// Wrap returns an emit func that forwards only the alerts Allow lets through. It is for
// alerts raised outside Detector and FailureTracker (daemon_stopping); never pass a wrapped
// emit to an emitter that already uses the same Debouncer, since Allow records the alert and
// the second check would then suppress it.
func (d *Debouncer) Wrap(emit func(Alert)) func(Alert) {
	return func(a Alert) {
		if d.Allow(a.Kind) {
			emit(a)
		}
	}
}

// wall strips the monotonic reading, so that Sub and Before compare wall-clock time.
func wall(t time.Time) time.Time { return t.Round(0) }

func orNow(now func() time.Time) func() time.Time {
	if now == nil {
		return time.Now
	}
	return now
}

func orDefault(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}

// shortErr renders err for an alert reason: one line, at most 160 runes.
func shortErr(err error) string {
	if err == nil {
		return "exited"
	}
	s := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:159]) + "…"
	}
	return s
}
