package schedule

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	_ "time/tzdata"

	"github.com/nicolasacchi/metamusic/internal/clock"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// recorder collects the keys Run saw, with the wall instant of each run.
type recorder struct {
	mu   sync.Mutex
	keys []Key
	at   []time.Time
}

func (r *recorder) run(_ context.Context, k Key) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, k)
	r.at = append(r.at, time.Now())
}

func (r *recorder) n() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.keys)
}

func fixedAt(at string) func(context.Context) (string, bool, error) {
	return func(context.Context) (string, bool, error) { return at, true, nil }
}

// startAt sleeps the bubble's clock (it starts at 2000-01-01 00:00 UTC) until t.
func startAt(t time.Time) { time.Sleep(time.Until(t)) }

func TestDaily_RunsAtLocalTime(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		loc := zone(t, "Europe/Berlin")
		startAt(time.Date(2000, 6, 10, 6, 0, 0, 0, loc))
		rec := &recorder{}
		ctx, cancel := context.WithCancel(context.Background())
		d := &Daily{Name: "t", At: fixedAt("07:00"), Loc: loc, Clock: clock.Real, Run: rec.run}
		done := make(chan struct{})
		go func() { d.Loop(ctx); close(done) }()
		time.Sleep(59 * time.Minute)
		synctest.Wait()
		if rec.n() != 0 {
			t.Fatalf("ran early: %d", rec.n())
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if rec.n() != 1 {
			t.Fatalf("runs = %d, want 1", rec.n())
		}
		want := time.Date(2000, 6, 10, 7, 0, 0, 0, loc)
		if !rec.at[0].Equal(want) {
			t.Errorf("ran at %s, want %s", rec.at[0], want)
		}
		if rec.keys[0].Day != "2000-06-10" || rec.keys[0].At != "07:00" || !rec.keys[0].Planned.Equal(want) {
			t.Errorf("key = %+v", rec.keys[0])
		}
		cancel()
		<-done
	})
}

func TestDaily_DSTSpringAndAutumnOnce(t *testing.T) {
	t.Parallel()
	loc := zone(t, "Europe/Berlin")
	cases := []struct {
		name    string
		start   time.Time
		at      string
		wantDay string
		want    time.Time
	}{
		// 2000-03-26: 02:00 CET jumps to 03:00 CEST; 02:30 does not exist and runs at the first valid instant (03:30 local).
		{"spring gap", time.Date(2000, 3, 25, 12, 0, 0, 0, loc), "02:30", "2000-03-26", time.Date(2000, 3, 26, 3, 30, 0, 0, loc)},
		// 2000-10-29: 03:00 CEST falls back to 02:00 CET; 02:30 happens twice and runs on the first occurrence only.
		{"autumn overlap", time.Date(2000, 10, 28, 12, 0, 0, 0, loc), "02:30", "2000-10-29", time.Date(2000, 10, 29, 0, 30, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				startAt(tc.start)
				rec := &recorder{}
				ctx, cancel := context.WithCancel(context.Background())
				d := &Daily{Name: "t", At: fixedAt(tc.at), Loc: loc, Clock: clock.Real, Run: rec.run}
				done := make(chan struct{})
				go func() { d.Loop(ctx); close(done) }()
				time.Sleep(20 * time.Hour) // past the change instant and both 02:30s
				synctest.Wait()
				if rec.n() != 1 {
					t.Fatalf("runs = %d, want exactly 1", rec.n())
				}
				if !rec.at[0].Equal(tc.want) || rec.keys[0].Day != tc.wantDay {
					t.Errorf("ran at %s key %+v, want %s day %s", rec.at[0], rec.keys[0], tc.want, tc.wantDay)
				}
				cancel()
				<-done
			})
		})
	}
}

func TestDaily_AtChangeMovesNextRun(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		loc := time.UTC
		startAt(time.Date(2000, 6, 10, 3, 0, 0, 0, loc))
		var mu sync.Mutex
		at := "06:00"
		rec := &recorder{}
		ctx, cancel := context.WithCancel(context.Background())
		d := &Daily{Name: "t", Loc: loc, Clock: clock.Real, Run: rec.run, At: func(context.Context) (string, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return at, true, nil
		}}
		done := make(chan struct{})
		go func() { d.Loop(ctx); close(done) }()
		time.Sleep(time.Hour) // 04:00
		mu.Lock()
		at = "05:00"
		mu.Unlock()
		time.Sleep(90 * time.Minute) // 05:30
		synctest.Wait()
		if rec.n() != 1 || !rec.at[0].Equal(time.Date(2000, 6, 10, 5, 0, 0, 0, loc)) {
			t.Fatalf("runs = %d at %v, want one at 05:00", rec.n(), rec.at)
		}
		time.Sleep(time.Hour) // 06:30: the old time must not fire a second run
		synctest.Wait()
		if rec.n() != 1 {
			t.Fatalf("runs = %d after the old time, want 1", rec.n())
		}
		cancel()
		<-done
	})
}

func TestDaily_AtErrorKeepsPrevious(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		loc := time.UTC
		startAt(time.Date(2000, 6, 10, 3, 0, 0, 0, loc))
		var mu sync.Mutex
		fail := false
		rec := &recorder{}
		ctx, cancel := context.WithCancel(context.Background())
		d := &Daily{Name: "t", Loc: loc, Clock: clock.Real, Run: rec.run, At: func(context.Context) (string, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				return "", false, errors.New("store busy")
			}
			return "05:00", true, nil
		}}
		done := make(chan struct{})
		go func() { d.Loop(ctx); close(done) }()
		time.Sleep(30 * time.Minute)
		mu.Lock()
		fail = true
		mu.Unlock()
		time.Sleep(3 * time.Hour) // 06:30, every re-read failed since 03:30
		synctest.Wait()
		if rec.n() != 1 || !rec.at[0].Equal(time.Date(2000, 6, 10, 5, 0, 0, 0, loc)) {
			t.Fatalf("runs = %d at %v, want one at 05:00 with the previous value kept", rec.n(), rec.at)
		}
		cancel()
		<-done
	})
}

func TestDaily_DisabledWaits(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		loc := time.UTC
		startAt(time.Date(2000, 6, 10, 3, 0, 0, 0, loc))
		var mu sync.Mutex
		enabled := false
		rec := &recorder{}
		ctx, cancel := context.WithCancel(context.Background())
		d := &Daily{Name: "t", Loc: loc, Clock: clock.Real, Run: rec.run, At: func(context.Context) (string, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return "04:00", enabled, nil
		}}
		done := make(chan struct{})
		go func() { d.Loop(ctx); close(done) }()
		time.Sleep(2 * time.Hour) // 05:00, disabled the whole time
		synctest.Wait()
		if rec.n() != 0 {
			t.Fatalf("a disabled schedule ran %d times", rec.n())
		}
		mu.Lock()
		enabled = true
		mu.Unlock()
		time.Sleep(23 * time.Hour) // 04:00 next day passes
		synctest.Wait()
		if rec.n() != 1 || rec.keys[0].Day != "2000-06-11" {
			t.Fatalf("runs = %d keys %+v, want one on 2000-06-11", rec.n(), rec.keys)
		}
		cancel()
		<-done
	})
}

func TestDaily_CatchUpWithinGrace(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		loc := time.UTC
		startAt(time.Date(2000, 6, 10, 7, 30, 0, 0, loc)) // restarted 30 min after 07:00
		rec := &recorder{}
		var asked []Key
		ctx, cancel := context.WithCancel(context.Background())
		d := &Daily{Name: "t", At: fixedAt("07:00"), Loc: loc, Clock: clock.Real, CatchUp: 2 * time.Hour, Run: rec.run,
			Done: func(_ context.Context, k Key) (bool, error) { asked = append(asked, k); return false, nil }}
		done := make(chan struct{})
		go func() { d.Loop(ctx); close(done) }()
		synctest.Wait()
		if rec.n() != 1 || rec.keys[0].Day != "2000-06-10" || len(asked) != 1 {
			t.Fatalf("runs = %d keys %+v asked %d, want one catch-up run for 2000-06-10", rec.n(), rec.keys, len(asked))
		}
		time.Sleep(23 * time.Hour) // 06:30 next day: no second run today, the next slot has not come
		synctest.Wait()
		if rec.n() != 1 {
			t.Fatalf("runs = %d, want 1", rec.n())
		}
		cancel()
		<-done
	})
}

func TestDaily_NoCatchUpAfterGraceOrWhenDone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		start time.Time
		grace time.Duration
		done  func(context.Context, Key) (bool, error)
	}{
		{"after grace", time.Date(2000, 6, 10, 9, 30, 0, 0, time.UTC), 2 * time.Hour, func(context.Context, Key) (bool, error) { return false, nil }},
		{"already done", time.Date(2000, 6, 10, 7, 30, 0, 0, time.UTC), 2 * time.Hour, func(context.Context, Key) (bool, error) { return true, nil }},
		{"done errors", time.Date(2000, 6, 10, 7, 30, 0, 0, time.UTC), 2 * time.Hour, func(context.Context, Key) (bool, error) { return false, errors.New("x") }},
		{"nil done never catches up", time.Date(2000, 6, 10, 7, 30, 0, 0, time.UTC), 2 * time.Hour, nil},
		{"zero grace", time.Date(2000, 6, 10, 7, 30, 0, 0, time.UTC), 0, func(context.Context, Key) (bool, error) { return false, nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				startAt(tc.start)
				rec := &recorder{}
				ctx, cancel := context.WithCancel(context.Background())
				d := &Daily{Name: "t", At: fixedAt("07:00"), Loc: time.UTC, Clock: clock.Real, CatchUp: tc.grace, Done: tc.done, Run: rec.run}
				done := make(chan struct{})
				go func() { d.Loop(ctx); close(done) }()
				time.Sleep(time.Hour)
				synctest.Wait()
				if rec.n() != 0 {
					t.Fatalf("ran %d times, want none", rec.n())
				}
				cancel()
				<-done
			})
		})
	}
}

func TestDaily_KeyIsLocalDay(t *testing.T) {
	t.Parallel()
	loc := zone(t, "Pacific/Auckland") // UTC+12/+13: the local day differs from the UTC day
	if got := DayKey(time.Date(2000, 6, 10, 23, 0, 0, 0, time.UTC), loc); got != "2000-06-11" {
		t.Errorf("DayKey = %s, want 2000-06-11", got)
	}
	if got := DayKey(time.Date(2000, 6, 10, 23, 0, 0, 0, time.UTC), nil); got == "" {
		t.Error("nil loc must fall back to the process zone")
	}
	synctest.Test(t, func(t *testing.T) {
		startAt(time.Date(2000, 6, 10, 10, 0, 0, 0, time.UTC)) // 22:00 local on the 10th
		rec := &recorder{}
		ctx, cancel := context.WithCancel(context.Background())
		d := &Daily{Name: "t", At: fixedAt("01:00"), Loc: loc, Clock: clock.Real, Run: rec.run}
		done := make(chan struct{})
		go func() { d.Loop(ctx); close(done) }()
		time.Sleep(4 * time.Hour) // 02:00 local on the 11th, still the 10th in UTC? (14:00 UTC the 10th)
		synctest.Wait()
		if rec.n() != 1 || rec.keys[0].Day != "2000-06-11" {
			t.Fatalf("runs = %d keys %+v, want one with local day 2000-06-11", rec.n(), rec.keys)
		}
		cancel()
		<-done
	})
}
