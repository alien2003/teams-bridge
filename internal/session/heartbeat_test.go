package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNextHeartbeat(t *testing.T) {
	h := time.Hour
	cases := []struct {
		name  string
		last  time.Time
		now   time.Time
		every time.Duration
		want  time.Duration
	}{
		{"nothing sent yet", time.Time{}, epoch, 6 * h, 0},
		{"just sent", epoch, epoch, 6 * h, 6 * h},
		{"an hour ago", epoch, epoch.Add(h), 6 * h, 5 * h},
		{"exactly due", epoch, epoch.Add(6 * h), 6 * h, 0},
		{"overdue (woke from sleep)", epoch, epoch.Add(30 * h), 6 * h, 0},
		{"clock went back", epoch, epoch.Add(-3 * h), 6 * h, 6 * h},
		{"default period", epoch, epoch.Add(h), 0, 5 * h},
		{"custom period", epoch, epoch.Add(10 * time.Minute), h, 50 * time.Minute},
	}
	for _, tc := range cases {
		if got := NextHeartbeat(tc.last, tc.now, tc.every); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// hbHarness drives runHeartbeat on a manual clock: every timer request shows up on waits.
type hbHarness struct {
	mu    sync.Mutex
	t     time.Time
	waits chan hbWait
	sends chan time.Time
}

type hbWait struct {
	d  time.Duration
	ch chan time.Time
}

func newHB() *hbHarness {
	return &hbHarness{t: epoch, waits: make(chan hbWait, 100), sends: make(chan time.Time, 100)}
}

func (h *hbHarness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.t
}

func (h *hbHarness) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	h.waits <- hbWait{d, ch}
	return ch
}

// step waits for the next timer request, checks its duration, moves the clock by advance and
// fires the timer.
func (h *hbHarness) step(t *testing.T, wantWait, advance time.Duration) {
	t.Helper()
	select {
	case w := <-h.waits:
		if wantWait >= 0 && w.d != wantWait {
			t.Fatalf("timer %v, want %v", w.d, wantWait)
		}
		h.mu.Lock()
		h.t = h.t.Add(advance)
		h.mu.Unlock()
		w.ch <- h.now()
	case <-time.After(5 * time.Second):
		t.Fatal("no timer request")
	}
}

func (h *hbHarness) expectSend(t *testing.T, at time.Duration) {
	t.Helper()
	select {
	case s := <-h.sends:
		if got := s.Sub(epoch); got != at {
			t.Fatalf("heartbeat at %v, want %v", got, at)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no heartbeat (wanted one at %v)", at)
	}
}

func (h *hbHarness) expectNoSend(t *testing.T) {
	t.Helper()
	select {
	case s := <-h.sends:
		t.Fatalf("unexpected heartbeat at %v", s.Sub(epoch))
	default:
	}
}

func TestRunHeartbeat(t *testing.T) {
	const h = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	hb := newHB()
	fail := map[int]bool{4: true} // the 5th send attempt fails
	n := 0
	send := func(context.Context) error {
		n++
		hb.sends <- hb.now()
		if fail[n-1] {
			return errors.New("fcm: 503")
		}
		return nil
	}
	done := make(chan struct{})
	go func() {
		runHeartbeat(ctx, 6*h, h, send, hb.now, hb.after, quiet)
		close(done)
	}()

	hb.expectSend(t, 0) // once at start
	for range 5 {
		hb.step(t, h, h) // the check interval caps every wait
		hb.expectNoSend(t)
	}
	hb.step(t, h, h)
	hb.expectSend(t, 6*h) // every 6 h

	// Suspend: the timer fires long after it was due (Go timers do not count suspended time).
	hb.step(t, h, 20*h)
	hb.expectSend(t, 26*h)

	// A partial wait: 30 min into the period the next timer asks for the remaining 5.5 h capped.
	hb.step(t, h, 30*time.Minute)
	hb.expectNoSend(t)
	hb.step(t, h, 5*h+30*time.Minute)
	hb.expectSend(t, 32*h)

	// The 5th attempt fails; it is retried after HeartbeatRetry, not after 6 h.
	for range 6 {
		hb.step(t, -1, h)
	}
	hb.expectSend(t, 38*h) // fails
	hb.step(t, HeartbeatRetry, HeartbeatRetry)
	hb.expectSend(t, 38*h+HeartbeatRetry)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runHeartbeat did not return after cancel")
	}
}

func TestRunHeartbeatStopsOnCancelDuringSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	hb := newHB()
	done := make(chan struct{})
	go func() {
		runHeartbeat(ctx, time.Hour, time.Minute, func(ctx context.Context) error {
			cancel()
			return ctx.Err()
		}, hb.now, hb.after, quiet)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runHeartbeat did not return")
	}
	if len(hb.waits) != 0 {
		t.Fatal("scheduled a retry after cancel")
	}
}

func TestRunHeartbeatReal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	count := 0
	got := make(chan struct{})
	go RunHeartbeat(ctx, 10*time.Millisecond, func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		count++
		if count == 3 {
			close(got)
		}
		return nil
	})
	select {
	case <-got:
	case <-ctx.Done():
		t.Fatal("fewer than 3 heartbeats in 5 s with a 10 ms period")
	}
}
