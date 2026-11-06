package session

import (
	"context"
	"log/slog"
	"time"
)

// Heartbeat timing.
const (
	// DefaultHeartbeat is the heartbeat period when none is given (heartbeat_hours = 6).
	DefaultHeartbeat = 6 * time.Hour
	// HeartbeatCheck is how often RunHeartbeat looks at the wall clock, so that a heartbeat goes
	// out within a minute after the computer wakes from sleep (Go timers do not count suspend).
	HeartbeatCheck = time.Minute
	// HeartbeatRetry is the delay before a failed heartbeat is sent again (at most the period).
	HeartbeatRetry = 5 * time.Minute
)

// NextHeartbeat is how long after now the heartbeat following one sent at last is due; 0 means
// due now (also when last is zero: nothing sent yet). Wall-clock based; if the clock went back
// before last, the wait is at most every.
func NextHeartbeat(last, now time.Time, every time.Duration) time.Duration {
	if every <= 0 {
		every = DefaultHeartbeat
	}
	if last.IsZero() {
		return 0
	}
	d := wall(last).Add(every).Sub(wall(now))
	return min(max(d, 0), every)
}

// RunHeartbeat calls send once right away and then every `every` (DefaultHeartbeat if ≤ 0)
// until ctx is done. A send that returns an error is retried after HeartbeatRetry. send is
// called from this goroutine; RunHeartbeat returns when ctx is done.
func RunHeartbeat(ctx context.Context, every time.Duration, send func(context.Context) error) {
	runHeartbeat(ctx, every, HeartbeatCheck, send, time.Now, time.After, slog.Default())
}

func runHeartbeat(ctx context.Context, every, check time.Duration, send func(context.Context) error,
	now func() time.Time, after func(time.Duration) <-chan time.Time, log *slog.Logger) {
	if every <= 0 {
		every = DefaultHeartbeat
	}
	var last time.Time // last successful send; zero → due
	var retryAt time.Time
	for ctx.Err() == nil {
		t := wall(now())
		due := NextHeartbeat(last, t, every) == 0 && !t.Before(retryAt)
		if due {
			if err := send(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				retryAt = wall(now()).Add(min(HeartbeatRetry, every))
				log.Warn("session: heartbeat failed; retrying", "err", err, "in", min(HeartbeatRetry, every))
			} else {
				last, retryAt = t, time.Time{}
				log.Debug("session: heartbeat sent")
			}
		}
		t = wall(now())
		wait := NextHeartbeat(last, t, every)
		if d := retryAt.Sub(t); !retryAt.IsZero() && d > wait {
			wait = d
		}
		wait = min(max(wait, 0), check)
		select {
		case <-ctx.Done():
			return
		case <-after(wait):
		}
	}
}
