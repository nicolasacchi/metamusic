// Package clocktest is the manual clock for tests. Import it only from
// _test.go files and servertest (see TestNoTestHelpersInBinary).
package clocktest

import (
	"sort"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
)

// Manual is a clock that moves only when told to. It implements clock.Clock:
// Advance and Set fire every due timer in deadline order.
type Manual struct {
	mu     sync.Mutex
	now    time.Time
	timers []*timer
}

var _ clock.Clock = (*Manual)(nil)

// NewManual returns a clock fixed at t.
func NewManual(t time.Time) *Manual { return &Manual{now: t} }

// Now is the current fake time.
func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// Advance moves the clock forward by d, firing due timers in order.
func (m *Manual) Advance(d time.Duration) {
	m.mu.Lock()
	target := m.now.Add(d)
	m.moveLocked(target)
	m.mu.Unlock()
}

// Set jumps the clock to t, firing timers that became due when t is later.
func (m *Manual) Set(t time.Time) {
	m.mu.Lock()
	if t.Before(m.now) {
		m.now = t
	} else {
		m.moveLocked(t)
	}
	m.mu.Unlock()
}

// moveLocked fires timers due at or before target in deadline order (ties in
// creation order), setting the clock to each deadline on the way.
func (m *Manual) moveLocked(target time.Time) {
	for {
		var next *timer
		for _, t := range m.timers {
			if !t.when.After(target) && (next == nil || t.when.Before(next.when)) {
				next = t
			}
		}
		if next == nil {
			break
		}
		if next.when.After(m.now) {
			m.now = next.when
		}
		m.removeLocked(next)
		next.fire(next.when)
	}
	if target.After(m.now) {
		m.now = target
	}
}

func (m *Manual) removeLocked(t *timer) bool {
	for i, x := range m.timers {
		if x == t {
			m.timers = append(m.timers[:i], m.timers[i+1:]...)
			return true
		}
	}
	return false
}

// NewTimer returns a timer that fires when the clock reaches Now()+d.
func (m *Manual) NewTimer(d time.Duration) clock.Timer {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := &timer{m: m, ch: make(chan time.Time, 1), when: m.now.Add(d)}
	if d <= 0 {
		t.fire(m.now)
		return t
	}
	m.timers = append(m.timers, t)
	sort.SliceStable(m.timers, func(i, j int) bool { return m.timers[i].when.Before(m.timers[j].when) })
	return t
}

// Pending reports how many timers are waiting.
func (m *Manual) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.timers)
}

type timer struct {
	m    *Manual
	ch   chan time.Time
	when time.Time
}

func (t *timer) fire(at time.Time) {
	select {
	case t.ch <- at:
	default:
	}
}

func (t *timer) C() <-chan time.Time { return t.ch }

func (t *timer) Stop() bool {
	t.m.mu.Lock()
	defer t.m.mu.Unlock()
	return t.m.removeLocked(t)
}
