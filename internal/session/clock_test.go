package session

import (
	"sort"
	"sync"
	"time"
)

// fakeClock is a manual clock with AfterFunc timers that fire during Advance.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

var epoch = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

func newFakeClock() *fakeClock { return &fakeClock{t: epoch} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &fakeTimer{at: c.t.Add(d), f: f}
	c.timers = append(c.timers, tm)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !tm.stopped && !tm.fired
		tm.stopped = true
		return was
	}
}

// pending is the number of timers that have neither fired nor been stopped.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, tm := range c.timers {
		if !tm.stopped && !tm.fired {
			n++
		}
	}
	return n
}

// Advance moves the clock forward by d, firing due timers in time order (outside the lock).
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.t.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
		var next *fakeTimer
		for _, tm := range c.timers {
			if !tm.stopped && !tm.fired && !tm.at.After(end) {
				next = tm
				break
			}
		}
		if next == nil {
			c.t = end
			c.mu.Unlock()
			return
		}
		next.fired = true
		if next.at.After(c.t) {
			c.t = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}
