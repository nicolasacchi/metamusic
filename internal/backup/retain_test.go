package backup

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	return loc
}

func snapName(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

func TestRetain_Table(t *testing.T) {
	loc := berlin(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, loc) // a Tuesday

	t.Run("30 daily snapshots keep 14 days and the newest of older weeks", func(t *testing.T) {
		var names []string
		for d := 0; d < 30; d++ {
			names = append(names, snapName(time.Date(2026, 9, 14+d, 3, 30, 0, 0, loc)))
		}
		keep, drop := Retain(names, now, loc)
		if len(keep)+len(drop) != 30 {
			t.Fatalf("keep %d + drop %d != 30", len(keep), len(drop))
		}
		want := map[string]bool{}
		for d := 16; d < 30; d++ { // 2026-09-30 .. 2026-10-13
			want[names[d]] = true
		}
		want[names[6]] = true  // 2026-09-20: newest of ISO week 38
		want[names[13]] = true // 2026-09-27: newest of ISO week 39
		if len(keep) != len(want) {
			t.Fatalf("kept %d, want %d: %v", len(keep), len(want), keep)
		}
		for _, k := range keep {
			if !want[k] {
				t.Errorf("unexpected keep %s", k)
			}
		}
	})

	t.Run("two snapshots on one day keep the newer", func(t *testing.T) {
		a := snapName(time.Date(2026, 10, 1, 3, 30, 0, 0, loc))
		b := snapName(time.Date(2026, 10, 1, 15, 0, 0, 0, loc))
		newest := snapName(time.Date(2026, 10, 13, 3, 30, 0, 0, loc))
		keep, drop := Retain([]string{a, b, newest}, now, loc)
		if !slices.Contains(keep, b) || slices.Contains(keep, a) || !slices.Contains(drop, a) {
			t.Fatalf("keep %v drop %v", keep, drop)
		}
	})

	t.Run("suffixed names parse and order after their base", func(t *testing.T) {
		base := snapName(time.Date(2026, 10, 10, 3, 30, 0, 0, loc))
		keep, drop := Retain([]string{base, base + "-2", base + "-10", base + "-3"}, now, loc)
		if diff := cmp.Diff([]string{base + "-10"}, keep); diff != "" {
			t.Fatalf("keep (-want +got):\n%s", diff)
		}
		if len(drop) != 3 {
			t.Fatalf("drop %v", drop)
		}
	})

	t.Run("the newest is always kept, however old", func(t *testing.T) {
		old := snapName(time.Date(2025, 1, 5, 3, 30, 0, 0, loc))
		keep, drop := Retain([]string{old}, now, loc)
		if diff := cmp.Diff([]string{old}, keep); diff != "" || len(drop) != 0 {
			t.Fatalf("keep %v drop %v", keep, drop)
		}
	})

	t.Run("other directories are in neither list; partials age out after 24h", func(t *testing.T) {
		newest := snapName(time.Date(2026, 10, 13, 3, 30, 0, 0, loc))
		oldPartial := "." + snapName(now.Add(-25*time.Hour)) + ".partial"
		freshPartial := "." + snapName(now.Add(-time.Hour)) + ".partial"
		keep, drop := Retain([]string{newest, "notes", "20261013", ".hidden", "x" + newest, newest + "-1", oldPartial, freshPartial}, now, loc)
		if diff := cmp.Diff([]string{oldPartial}, drop); diff != "" {
			t.Fatalf("drop (-want +got):\n%s", diff)
		}
		for _, n := range []string{"notes", "20261013", ".hidden", "x" + newest, newest + "-1"} {
			if slices.Contains(keep, n) || slices.Contains(drop, n) {
				t.Errorf("%q must be untouched", n)
			}
		}
		if !slices.Contains(keep, freshPartial) || !slices.Contains(keep, newest) {
			t.Errorf("keep %v", keep)
		}
	})

	t.Run("empty", func(t *testing.T) {
		keep, drop := Retain(nil, now, loc)
		if len(keep)+len(drop) != 0 {
			t.Fatal("not empty")
		}
	})
}

func TestSnapshotName(t *testing.T) {
	at := time.Date(2026, 10, 13, 1, 30, 0, 0, time.UTC)
	taken := map[string]bool{}
	for i, want := range []string{"20261013T013000Z", "20261013T013000Z-2", "20261013T013000Z-3"} {
		got := SnapshotName(at, func(n string) bool { return taken[n] })
		if got != want {
			t.Fatalf("call %d: %q, want %q", i, got, want)
		}
		taken[got] = true
	}
}

func TestNextRun_Table(t *testing.T) {
	loc := berlin(t)
	at := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, loc) }
	tests := []struct {
		name string
		now  time.Time
		at   string
		want time.Time
	}{
		{"from 02:00 to 03:30 the same day", at(2026, 6, 10, 2, 0), "03:30", at(2026, 6, 10, 3, 30)},
		{"exactly at 03:30 means the next day", at(2026, 6, 10, 3, 30), "03:30", at(2026, 6, 11, 3, 30)},
		{"after 03:30", at(2026, 6, 10, 20, 0), "03:30", at(2026, 6, 11, 3, 30)},
		{"march change day, 03:30 is 03:30 local", at(2026, 3, 29, 1, 0), "03:30", at(2026, 3, 29, 3, 30)},
		{"october change day, 03:30 is 03:30 local", at(2026, 10, 25, 1, 0), "03:30", at(2026, 10, 25, 3, 30)},
		{"at inside the spring gap runs at 03:30 CEST", at(2026, 3, 29, 0, 10), "02:30", time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)},
		{"at in the autumn overlap runs at the first 02:30", at(2026, 10, 25, 0, 10), "02:30", time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)},
		{"after the first autumn 02:30 the next day", time.Date(2026, 10, 25, 0, 45, 0, 0, time.UTC), "02:30", at(2026, 10, 26, 2, 30)},
		{"midnight", at(2026, 12, 31, 23, 59), "00:00", at(2027, 1, 1, 0, 0)},
	}
	for _, tc := range tests {
		got := NextRun(tc.now, tc.at, loc)
		if !got.Equal(tc.want) {
			t.Errorf("%s: NextRun(%s, %s) = %s, want %s", tc.name, tc.now, tc.at, got, tc.want)
		}
		if !got.After(tc.now) {
			t.Errorf("%s: %s is not after now", tc.name, got)
		}
	}
	if got := NextRun(at(2026, 6, 10, 2, 0), "off", loc); !got.IsZero() {
		t.Errorf("off: %s", got)
	}
	if got := NextRun(at(2026, 6, 10, 2, 0), "25:99", loc); !got.IsZero() {
		t.Errorf("bad at: %s", got)
	}
}

func TestScheduler_LoopSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		start := time.Now()
		at := start.UTC().Add(time.Minute).Format("15:04")
		ctx, cancel := context.WithCancel(context.Background())
		var stamps []time.Duration
		done := make(chan struct{})
		s := &Scheduler{At: at, Loc: time.UTC, Run: func(context.Context) {
			calls.Add(1)
			stamps = append(stamps, time.Since(start))
		}}
		go func() { s.Loop(ctx); close(done) }()
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatalf("ran early: %d", calls.Load())
		}
		time.Sleep(31 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("calls = %d after 1 min, want 1", calls.Load())
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("calls = %d after 24 h, want 2", calls.Load())
		}
		cancel()
		<-done
		if want := []time.Duration{time.Minute, time.Minute + 24*time.Hour}; fmt.Sprint(stamps) != fmt.Sprint(want) {
			t.Fatalf("run times %v, want %v", stamps, want)
		}
	})
}
