package session

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/proto"
)

func TestBackoff(t *testing.T) {
	s := time.Second
	cases := []struct {
		name string
		b    Backoff
		want []time.Duration
	}{
		{"defaults: 2 s doubling, 5 min cap", Backoff{Jitter: NoJitter},
			[]time.Duration{2 * s, 4 * s, 8 * s, 16 * s, 32 * s, 64 * s, 128 * s, 256 * s, 300 * s, 300 * s, 300 * s}},
		{"custom", Backoff{Base: 100 * time.Millisecond, Max: time.Second, Jitter: NoJitter},
			[]time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, s, s}},
		{"base above max", Backoff{Base: time.Hour, Max: time.Minute, Jitter: NoJitter},
			[]time.Duration{time.Minute, time.Minute}},
		{"jitter is clamped to max", Backoff{Jitter: func(d time.Duration) time.Duration { return 10 * d }},
			[]time.Duration{20 * s, 40 * s, 80 * s, 160 * s, 300 * s, 300 * s}},
		{"negative jitter is clamped to 0", Backoff{Jitter: func(time.Duration) time.Duration { return -s }},
			[]time.Duration{0, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.b
			for i, w := range tc.want {
				if got := b.Next(); got != w {
					t.Fatalf("attempt %d: %v, want %v", i, got, w)
				}
			}
			b.Reset()
			if got, w := b.Next(), tc.want[0]; got != w {
				t.Fatalf("after Reset: %v, want %v", got, w)
			}
		})
	}
	t.Run("many attempts do not overflow", func(t *testing.T) {
		b := Backoff{Base: time.Nanosecond, Max: 1 << 62, Jitter: NoJitter}
		for range 200 {
			if d := b.Next(); d <= 0 || d > 1<<62 {
				t.Fatalf("delay %v", d)
			}
		}
	})
	t.Run("default jitter stays within ±20 %", func(t *testing.T) {
		var b Backoff
		for i := range 9 {
			nominal := min(DefaultBackoffBase<<i, DefaultBackoffMax)
			d := b.Next()
			if d < nominal*8/10 || d > min(nominal*12/10, DefaultBackoffMax) {
				t.Fatalf("attempt %d: %v outside ±20 %% of %v", i, d, nominal)
			}
		}
	})
}

func newTracker(mod func(*TrackerConfig)) (*FailureTracker, *fakeClock, *recorder) {
	clk := newFakeClock()
	rec := &recorder{}
	cfg := TrackerConfig{Now: clk.Now, Backoff: Backoff{Jitter: NoJitter}, Logger: quiet}
	if mod != nil {
		mod(&cfg)
	}
	return NewFailureTracker(cfg, rec.emit), clk, rec
}

type run struct {
	launchFails bool          // Stopped without Started
	lasts       time.Duration // run duration
	cause       error
	pause       time.Duration // idle time after Stopped (the backoff sleep)
}

func TestFailureTracker(t *testing.T) {
	errLaunch := errors.New("chrome failed to start: exec: msedge: not found")
	errDied := errors.New("browser process exited: signal: killed")
	launch := run{launchFails: true, cause: errLaunch}
	quick := run{lasts: 10 * time.Second, cause: errDied}
	healthy := run{lasts: time.Hour, cause: errDied}
	s := time.Second
	cases := []struct {
		name     string
		mod      func(*TrackerConfig)
		runs     []run
		waits    []time.Duration
		failures int
		alerts   int
	}{
		{"launch failures", nil, []run{launch, launch, launch}, []time.Duration{2 * s, 4 * s, 8 * s}, 3, 1},
		{"quick deaths", nil, []run{quick, quick, quick}, []time.Duration{2 * s, 4 * s, 8 * s}, 3, 1},
		{"mixed failures", nil, []run{launch, quick, launch}, []time.Duration{2 * s, 4 * s, 8 * s}, 3, 1},
		{"two failures do not alert", nil, []run{launch, quick}, []time.Duration{2 * s, 4 * s}, 2, 0},
		{"a healthy run resets", nil, []run{launch, quick, healthy, launch, quick}, []time.Duration{2 * s, 4 * s, 2 * s, 4 * s, 8 * s}, 2, 0},
		{"just healthy", nil, []run{quick, {lasts: DefaultHealthy}}, []time.Duration{2 * s, 2 * s}, 0, 0},
		{"just short of healthy", nil, []run{quick, {lasts: DefaultHealthy - s}}, []time.Duration{2 * s, 4 * s}, 2, 0},
		{"clean exit counts by duration", nil, []run{{lasts: s}, {lasts: s}, {lasts: s}}, []time.Duration{2 * s, 4 * s, 8 * s}, 3, 1},
		{"unresponsive counts however long it ran", nil,
			[]run{{lasts: time.Hour, cause: ErrUnresponsive}, {lasts: time.Hour, cause: fmt.Errorf("restart: %w", ErrUnresponsive)}, {lasts: time.Hour, cause: ErrUnresponsive}},
			[]time.Duration{2 * s, 4 * s, 8 * s}, 3, 1},
		{"further failures are debounced", nil, []run{launch, launch, launch, launch, launch}, []time.Duration{2 * s, 4 * s, 8 * s, 16 * s, 32 * s}, 5, 1},
		{"still dead after 6 h alerts again", nil,
			[]run{launch, launch, launch, {launchFails: true, pause: 6 * time.Hour}, launch},
			[]time.Duration{2 * s, 4 * s, 8 * s, 16 * s, 32 * s}, 5, 2},
		{"custom limit", func(c *TrackerConfig) { c.Limit = 5 }, []run{launch, launch, launch, launch}, nil, 4, 0},
		{"custom limit reached", func(c *TrackerConfig) { c.Limit = 1 }, []run{launch}, nil, 1, 1},
		{"custom healthy period", func(c *TrackerConfig) { c.Healthy = 5 * time.Second }, []run{quick, quick}, nil, 0, 0},
		{"custom backoff", func(c *TrackerConfig) { c.Backoff = Backoff{Base: s, Max: 3 * s, Jitter: NoJitter} },
			[]run{launch, launch, launch, launch}, []time.Duration{s, 2 * s, 3 * s, 3 * s}, 4, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ft, clk, rec := newTracker(tc.mod)
			for i, r := range tc.runs {
				clk.Advance(r.pause)
				if !r.launchFails {
					ft.Started()
					clk.Advance(r.lasts)
				}
				w := ft.Stopped(r.cause)
				if tc.waits != nil && w != tc.waits[i] {
					t.Fatalf("run %d: wait %v, want %v", i, w, tc.waits[i])
				}
				clk.Advance(w)
			}
			if got := ft.Failures(); got != tc.failures {
				t.Errorf("Failures() = %d, want %d", got, tc.failures)
			}
			if got := len(rec.all()); got != tc.alerts {
				t.Fatalf("%d alerts %+v, want %d", got, rec.all(), tc.alerts)
			}
			for _, a := range rec.all() {
				if a.Kind != proto.KindBrowserDead || !strings.Contains(a.Reason, "in a row") || a.At.IsZero() {
					t.Errorf("alert %+v", a)
				}
			}
		})
	}
}

func TestFailureTrackerRunningHealthy(t *testing.T) {
	ft, clk, _ := newTracker(nil)
	ft.Stopped(errors.New("x"))
	ft.Stopped(errors.New("x"))
	ft.Started()
	if ft.Failures() != 2 {
		t.Fatalf("Failures() right after start = %d, want 2", ft.Failures())
	}
	clk.Advance(DefaultHealthy)
	if ft.Failures() != 0 {
		t.Fatalf("Failures() after a healthy period = %d, want 0", ft.Failures())
	}
}

func TestFailureTrackerReset(t *testing.T) {
	ft, _, rec := newTracker(nil)
	ft.Stopped(nil)
	ft.Stopped(nil)
	ft.Reset()
	if ft.Failures() != 0 {
		t.Fatal("Reset kept failures")
	}
	if w := ft.Stopped(nil); w != DefaultBackoffBase {
		t.Fatalf("Reset kept the backoff: %v", w)
	}
	if len(rec.all()) != 0 {
		t.Fatal("alerted after Reset")
	}
}

func TestFailureTrackerReasonNilCause(t *testing.T) {
	ft, _, rec := newTracker(func(c *TrackerConfig) { c.Limit = 1 })
	ft.Stopped(nil)
	if a := rec.all(); len(a) != 1 || !strings.Contains(a[0].Reason, "exited") {
		t.Fatalf("alerts %+v", a)
	}
}
