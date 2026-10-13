package store

import (
	"math/rand/v2"
	"sort"
	"testing"
	"time"
)

func TestFormatTime_LexicographicOrder(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts := make([]time.Time, 1000)
	for i := range ts {
		// spread across years, milliseconds and zones, including whole-second instants
		ts[i] = base.Add(time.Duration(rng.Int64N(int64(5 * 365 * 24 * time.Hour))))
		if i%7 == 0 {
			ts[i] = ts[i].Truncate(time.Second)
		}
		if i%5 == 0 {
			ts[i] = ts[i].In(time.FixedZone("x", 5*3600+1800))
		}
	}
	strs := make([]string, len(ts))
	for i, x := range ts {
		strs[i] = FormatTime(x)
		if len(strs[i]) != 24 {
			t.Fatalf("width %d for %q", len(strs[i]), strs[i])
		}
	}
	idx := make([]int, len(ts))
	for i := range idx {
		idx[i] = i
	}
	byTime := append([]int(nil), idx...)
	sort.SliceStable(byTime, func(a, b int) bool { return ts[byTime[a]].Before(ts[byTime[b]]) })
	byStr := append([]int(nil), idx...)
	sort.SliceStable(byStr, func(a, b int) bool { return strs[byStr[a]] < strs[byStr[b]] })
	for i := range byTime {
		if !ts[byTime[i]].Equal(ts[byStr[i]]) {
			t.Fatalf("order differs at %d: %v vs %v", i, ts[byTime[i]], ts[byStr[i]])
		}
	}
}

func TestParseTime_RoundTrip(t *testing.T) {
	t.Parallel()
	in := time.Date(2026, 10, 14, 1, 2, 3, 456_000_000, time.UTC)
	s := FormatTime(in)
	if s != "2026-10-14T01:02:03.456Z" {
		t.Fatalf("got %q", s)
	}
	out, err := ParseTime(s)
	if err != nil || !out.Equal(in) {
		t.Fatalf("ParseTime = %v, %v", out, err)
	}
	if _, err := ParseTime("2026-10-14T01:02:03Z"); err == nil {
		t.Error("variable-width RFC 3339 must be refused")
	}
}
