package servertest

import (
	"sync"
	"time"
)

// Clock is the one advanceable clock every store and service of a Rig reads.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock returns a clock fixed at t.
func NewClock(t time.Time) *Clock { return &Clock{t: t} }

// Now is the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// Set jumps the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}
