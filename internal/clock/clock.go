// Package clock is an injectable clock (M3 C68). It imports only the standard
// library; the manual implementation for tests lives in clock/clocktest.
package clock

import "time"

// Clock supplies the time and timers to the runner, the reconciler and Wait.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is the subset of time.Timer the services use.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type realClock struct{}

// Real is the wall clock.
var Real Clock = realClock{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }
