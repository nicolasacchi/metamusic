package servertest

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
)

// Clock is the one advanceable clock every store and service of a Rig reads.
// It is the manual clock of clock/clocktest (M3 C68): Advance also fires the
// timers of the runner, the reconciler and Wait.
type Clock = clocktest.Manual

// NewClock returns a clock fixed at t.
func NewClock(t time.Time) *Clock { return clocktest.NewManual(t) }
