package session

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/alien2003/teams-bridge/internal/proto"
)

// Restart-loop defaults (BRIEF §5 "Restart loop with exponential backoff; after N consecutive
// failures → alert browser_dead").
const (
	DefaultBackoffBase  = 2 * time.Second
	DefaultBackoffMax   = 5 * time.Minute
	DefaultFailureLimit = 3
	// DefaultHealthy is how long a browser run must last to count as healthy.
	DefaultHealthy = 3 * time.Minute
)

// ErrUnresponsive is the cause to pass to FailureTracker.Stopped when the browser was restarted
// because the Detector answered ProbeRestart. Such a run counts as failed however long it lasted.
var ErrUnresponsive = errors.New("session: page unresponsive (repeated probe failures)")

// Backoff is an exponential backoff: Base, 2·Base, 4·Base … capped at Max, each value passed
// through Jitter. The zero value is ready to use with the defaults. Not safe for concurrent use.
type Backoff struct {
	Base time.Duration // 0 → DefaultBackoffBase
	Max  time.Duration // 0 → DefaultBackoffMax
	// Jitter maps the nominal delay to the actual one; nil → DefaultJitter. Use NoJitter in tests.
	// The result is clamped to [0, Max].
	Jitter  func(time.Duration) time.Duration
	attempt int
}

// DefaultJitter spreads d uniformly over [0.8·d, 1.2·d].
func DefaultJitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// NoJitter returns d unchanged.
func NoJitter(d time.Duration) time.Duration { return d }

// Next returns the delay before the next attempt and advances the backoff.
func (b *Backoff) Next() time.Duration {
	base, maxD := b.Base, b.Max
	if base <= 0 {
		base = DefaultBackoffBase
	}
	if maxD <= 0 {
		maxD = DefaultBackoffMax
	}
	d := base
	for i := 0; i < b.attempt && d < maxD; i++ {
		d *= 2
	}
	if d >= maxD {
		d = maxD
	} else {
		b.attempt++
	}
	j := b.Jitter
	if j == nil {
		j = DefaultJitter
	}
	return min(max(j(d), 0), maxD)
}

// Reset starts again from Base.
func (b *Backoff) Reset() { b.attempt = 0 }

// TrackerConfig configures a FailureTracker. Zero values mean the defaults.
type TrackerConfig struct {
	// Limit consecutive failed runs emit browser_dead (default 3). Every further failed run
	// emits it again, which the debouncer turns into at most one per 6 h.
	Limit int
	// Healthy: a run that lasted at least this long was healthy (default 3 min).
	Healthy time.Duration
	// Backoff for the delay between attempts (copied; zero value → 2 s doubling to 5 min, ±20 %).
	Backoff Backoff
	// Alerts debounces browser_dead; nil → a private NewDebouncer(0, Now). Share one with the
	// Detector, and do not wrap emit with the same Debouncer as well (see DetectorConfig.Alerts).
	Alerts *Debouncer
	Now    func() time.Time
	Logger *slog.Logger
}

// FailureTracker classifies browser runs for the daemon's restart loop and emits
// proto.KindBrowserDead after Limit consecutive failed runs. A run fails when launch/attach
// fails (Stopped without Started), when the browser dies less than Healthy after Started, or
// when it was stopped with ErrUnresponsive. A healthy run resets the count and the backoff.
// Safe for concurrent use.
//
//	ft := session.NewFailureTracker(session.TrackerConfig{Alerts: deb}, send)
//	for ctx.Err() == nil {
//		b, err := launchAndAttach(ctx)
//		if err == nil {
//			ft.Started()
//			err = b.Wait()        // until the process exits or CDP breaks
//			det.BrowserLost(err)
//		}
//		if ctx.Err() != nil {
//			break // shutting down: not a failed run (no browser_dead on SIGTERM)
//		}
//		sleep(ctx, ft.Stopped(err))
//	}
type FailureTracker struct {
	emit    func(Alert)
	alerts  *Debouncer
	now     func() time.Time
	log     *slog.Logger
	limit   int
	healthy time.Duration

	mu       sync.Mutex
	backoff  Backoff
	failures int
	running  bool
	started  time.Time
}

// NewFailureTracker returns a FailureTracker emitting through cfg.Alerts to emit.
func NewFailureTracker(cfg TrackerConfig, emit func(Alert)) *FailureTracker {
	t := &FailureTracker{
		emit:    emit,
		alerts:  cfg.Alerts,
		now:     orNow(cfg.Now),
		log:     orDefault(cfg.Logger),
		limit:   cfg.Limit,
		healthy: cfg.Healthy,
		backoff: cfg.Backoff,
	}
	t.backoff.attempt = 0
	if t.emit == nil {
		t.emit = func(Alert) {}
	}
	if t.alerts == nil {
		t.alerts = NewDebouncer(0, t.now)
	}
	if t.limit <= 0 {
		t.limit = DefaultFailureLimit
	}
	if t.healthy <= 0 {
		t.healthy = DefaultHealthy
	}
	return t
}

// Started records that launch and attach succeeded: a run begins now.
func (t *FailureTracker) Started() {
	t.mu.Lock()
	t.running, t.started = true, wall(t.now())
	t.mu.Unlock()
}

// Stopped records the end of a run, or a failed launch/attach when no run was Started, and
// returns how long to wait before the next attempt. cause is the reason (nil: exited cleanly).
func (t *FailureTracker) Stopped(cause error) time.Duration {
	now := wall(t.now())
	var a *Alert
	t.mu.Lock()
	ran := t.running
	lasted := now.Sub(t.started)
	t.running = false
	failed := !ran || lasted < t.healthy || errors.Is(cause, ErrUnresponsive)
	if !failed {
		t.failures = 0
		t.backoff.Reset()
	} else {
		t.failures++
	}
	wait := t.backoff.Next()
	n := t.failures
	if failed && n >= t.limit {
		a = &Alert{
			Kind:   proto.KindBrowserDead,
			Reason: fmt.Sprintf("browser failed %d times in a row (last: %s)", n, shortErr(cause)),
			At:     now,
		}
	}
	t.mu.Unlock()

	switch {
	case !failed:
		t.log.Info("session: browser stopped after a healthy run; restarting", "lasted", lasted.Round(time.Second), "err", cause, "wait", wait)
	case !ran:
		t.log.Warn("session: browser launch failed", "failures", n, "err", cause, "wait", wait)
	default:
		t.log.Warn("session: browser run failed", "failures", n, "lasted", lasted.Round(time.Second), "err", cause, "wait", wait)
	}
	if a != nil {
		if t.alerts.Allow(a.Kind) {
			t.log.Info("session: alert", "kind", a.Kind, "reason", a.Reason)
			t.emit(*a)
		} else {
			t.log.Debug("session: alert suppressed (debounced)", "kind", a.Kind)
		}
	}
	return wait
}

// Failures is the number of consecutive failed runs; 0 while the current run has been healthy
// for Healthy already.
func (t *FailureTracker) Failures() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running && wall(t.now()).Sub(t.started) >= t.healthy {
		return 0
	}
	return t.failures
}

// Reset forgets the failures and restarts the backoff from Base.
func (t *FailureTracker) Reset() {
	t.mu.Lock()
	t.failures = 0
	t.backoff.Reset()
	t.mu.Unlock()
}
