// Package schedule is the program's one daily scheduler (G1 C324): a leaf that runs a function once per local day at an editable
// wall-clock time in a configured zone. It imports only the standard library and internal/clock. At-most-once per local-day Key is
// the consumer's unique constraint; the scheduler never persists anything and its in-process guard only stops a repeated wake.
package schedule

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
)

// Key identifies one scheduled run: the local calendar day, the HH:MM it was scheduled for and the planned instant.
type Key struct {
	Day     string // local YYYY-MM-DD in the schedule's zone
	At      string // HH:MM
	Planned time.Time
}

// Daily runs Run once per local day at the time At reports.
type Daily struct {
	Name string
	// At is re-read on every wake. An error keeps the previous value; enabled=false waits without running.
	At    func(ctx context.Context) (at string, enabled bool, err error)
	Loc   *time.Location // nil = the process zone
	Clock clock.Clock    // nil = the wall clock
	// CatchUp runs a missed slot of today at loop start when it is at most this late (0 = never); Done is consulted first
	// (nil = never catch up).
	CatchUp time.Duration
	Done    func(ctx context.Context, k Key) (bool, error)
	Run     func(ctx context.Context, k Key)
	// MaxSleep bounds one sleep, so an edited time takes effect within it. Default one minute.
	MaxSleep time.Duration
}

var atRE = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

func parseAt(at string) (h, m int, ok bool) {
	g := atRE.FindStringSubmatch(at)
	if g == nil {
		return 0, 0, false
	}
	h, _ = strconv.Atoi(g[1])
	m, _ = strconv.Atoi(g[2])
	return h, m, true
}

// slotOn is the instant of h:m on the local day that is i days after l's day. A nonexistent local time is normalised by time.Date
// to the first valid instant after the gap; in the autumn overlap the first occurrence counts.
func slotOn(l time.Time, i, h, m int, loc *time.Location) time.Time {
	c := time.Date(l.Year(), l.Month(), l.Day()+i, h, m, 0, 0, loc)
	if e := c.Add(-time.Hour).In(loc); e.Hour() == h && e.Minute() == m && e.Day() == c.In(loc).Day() {
		c = e
	}
	return c
}

// NextRun returns the first instant after now whose local wall clock is at ("HH:MM"). On the spring change day a time inside the
// gap (02:30) is normalised by time.Date to 03:30 local; in the autumn overlap the first occurrence counts. A bad or "off" at gives
// the zero time.
func NextRun(now time.Time, at string, loc *time.Location) time.Time {
	h, m, ok := parseAt(at)
	if !ok {
		return time.Time{}
	}
	if loc == nil {
		loc = time.Local
	}
	l := now.In(loc)
	for i := 0; ; i++ {
		if c := slotOn(l, i, h, m, loc); c.After(now) {
			return c
		}
	}
}

// DayKey is the local YYYY-MM-DD of t in loc (nil = the process zone).
func DayKey(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	return t.In(loc).Format("2006-01-02")
}

func keyFor(planned time.Time, at string, loc *time.Location) Key {
	return Key{Day: DayKey(planned, loc), At: at, Planned: planned}
}

// Loop runs until ctx ends. It never returns early and a failing At keeps the last good value.
func (d *Daily) Loop(ctx context.Context) {
	loc := d.Loc
	if loc == nil {
		loc = time.Local
	}
	clk := d.Clock
	if clk == nil {
		clk = clock.Real
	}
	maxSleep := d.MaxSleep
	if maxSleep <= 0 {
		maxSleep = time.Minute
	}
	var (
		at       string
		enabled  bool
		have     bool
		lastRan  Key
		ran      bool
		lastWake time.Time
	)
	read := func() {
		a, en, err := d.At(ctx)
		if err != nil {
			if have {
				slog.Warn("schedule time not re-read, keeping the previous value", "schedule", d.Name)
			}
			return
		}
		at, enabled, have = a, en, true
	}
	fire := func(k Key) {
		if ran && lastRan.Day == k.Day && lastRan.At == k.At {
			return
		}
		lastRan, ran = k, true
		d.Run(ctx, k)
	}
	read()
	if d.CatchUp > 0 && d.Done != nil && enabled {
		if h, m, ok := parseAt(at); ok {
			now := clk.Now()
			slot := slotOn(now.In(loc), 0, h, m, loc)
			if !slot.After(now) && now.Sub(slot) <= d.CatchUp {
				k := keyFor(slot, at, loc)
				if done, err := d.Done(ctx, k); err == nil && !done {
					fire(k)
				}
			}
		}
	}
	lastWake = clk.Now()
	for ctx.Err() == nil {
		now := clk.Now()
		sleep := maxSleep
		if enabled {
			if h, m, ok := parseAt(at); ok {
				// A slot crossed since the last wake runs now. Only today's slot is considered; after a stall across midnight the previous day's slot is not replayed (catch-up covers boot only).
				slot := slotOn(now.In(loc), 0, h, m, loc)
				if slot.After(lastWake) && !slot.After(now) {
					fire(keyFor(slot, at, loc))
				}
				if next := NextRun(clk.Now(), at, loc); !next.IsZero() {
					if until := next.Sub(clk.Now()); until < sleep {
						sleep = until
					}
				}
			}
		}
		lastWake = now
		if sleep < 0 {
			sleep = 0
		}
		t := clk.NewTimer(sleep)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		read()
	}
}
