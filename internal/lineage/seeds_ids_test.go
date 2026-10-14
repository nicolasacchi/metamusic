package lineage_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/lineage"
)

func TestNextSeeds(t *testing.T) {
	t.Parallel()
	top := int64(lineage.MaxSeed)
	cases := []struct {
		name string
		base int64
		used []int64
		n    int
		want []int64
	}{
		{"above the used run", 1100, []int64{1100, 1101}, 3, []int64{1102, 1103, 1104}},
		{"nothing used", 1100, nil, 3, []int64{1101, 1102, 1103}},
		{"wraps to 1000", top - 1, nil, 3, []int64{top, 1000, 1001}},
		{"wraps to 0 after 1000", 1002, []int64{1003, 1004}, 1, []int64{1005}},
		{"used values are skipped while wrapping", top, []int64{1000, 1002}, 3, []int64{1001, 1003, 1004}},
		{"all of 1000.. used below start reaches 0", top, seq(1000, 1004), 2, []int64{1005, 1006}},
		{"zero wanted", 5, nil, 0, nil},
		{"base below 1000 with high used", 5, []int64{2000}, 2, []int64{2001, 2002}},
		{"used below base is ignored for the start", 5000, []int64{1, 2, 3}, 1, []int64{5001}},
	}
	for _, c := range cases {
		if diff := cmp.Diff(c.want, lineage.NextSeeds(c.base, c.used, c.n)); diff != "" {
			t.Errorf("%s: %s", c.name, diff)
		}
	}
	// the wrap phase reaches 0..999 once 1000..start-1 is exhausted
	start := int64(1003)
	used := seq(1000, 1002)
	got := lineage.NextSeeds(start-1, append(used, start-1), 3) // start = 1003, MaxSeed.. first
	if len(got) != 3 || got[0] != 1003 {
		t.Errorf("got %v", got)
	}
	// exhaust everything above 1000 up to a start of MaxSeed so only 0..999 remains
	got = lineage.NextSeeds(top, nil, 4)
	if diff := cmp.Diff([]int64{1000, 1001, 1002, 1003}, got); diff != "" {
		t.Error(diff)
	}
}

func seq(from, to int64) []int64 {
	var out []int64
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func TestAllocateID(t *testing.T) {
	t.Parallel()
	none := func(string) bool { return false }
	set := func(ids ...string) func(string) bool {
		return func(id string) bool {
			for _, x := range ids {
				if x == id {
					return true
				}
			}
			return false
		}
	}
	cases := []struct {
		name  string
		base  string
		seed  int64
		keep  bool
		taken func(string) bool
		want  string
	}{
		{"seed suffix", "syn-a", 1101, false, none, "syn-a-s1101"},
		{"taken gets -2", "syn-a", 1101, false, set("syn-a-s1101"), "syn-a-s1101-2"},
		{"taken twice gets -3", "syn-a", 1101, false, set("syn-a-s1101", "syn-a-s1101-2"), "syn-a-s1101-3"},
		{"a seed suffix is replaced", "song-s1101", 1102, false, none, "song-s1102"},
		{"keep and free", "syn-a", 1101, true, none, "syn-a"},
		{"keep but taken", "syn-a", 1101, true, set("syn-a"), "syn-a-s1101"},
		{"underscore suffix", "fx_001_seed7", 9, false, none, "fx_001-s9"},
	}
	for _, c := range cases {
		got, err := lineage.AllocateID(c.base, c.seed, c.keep, c.taken)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v, want %q", c.name, got, err, c.want)
		}
	}

	long := "a" + strings.Repeat("b", 179) // a valid 180-character id
	got, err := lineage.AllocateID(long, 1102, false, none)
	if err != nil || len(got) > 180 || !strings.HasSuffix(got, "-s1102") {
		t.Fatalf("long: len %d %q %v", len(got), got, err)
	}
	// even with the longest suffix the id fits
	every := func(id string) bool { return !strings.HasSuffix(id, "-99") }
	got, err = lineage.AllocateID(long, 1102, false, every)
	if err != nil || len(got) > 180 || !strings.HasSuffix(got, "-s1102-99") {
		t.Fatalf("long -99: len %d %q %v", len(got), got, err)
	}
	// trailing separators of the cut stem are trimmed
	got, err = lineage.AllocateID("x"+strings.Repeat("-", 200), 5, false, none)
	if err != nil || strings.Contains(got, "--") || !strings.HasPrefix(got, "x-s5") {
		t.Errorf("separators: %q %v", got, err)
	}
	// all of -2 … -99 taken
	all := func(id string) bool { return true }
	if _, err := lineage.AllocateID("syn-a", 1, false, all); !errors.Is(err, lineage.ErrNoFreeID) {
		t.Errorf("exhausted: %v", err)
	}
}

func TestDefaultTitles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   lineage.TitleIn
		want string
	}{
		{"explicit wins", lineage.TitleIn{Explicit: "My Song", Variant: true, ParentTitle: "Luna Park", Seed: 1102}, "My Song"},
		{"variant", lineage.TitleIn{Variant: true, ParentTitle: "Luna Park", Seed: 1102}, "Luna Park · s1102"},
		{"draft with mother and label", lineage.TitleIn{FromDraft: true, HasMother: true, ParentTitle: "Luna Park", Label: "v2", RequestID: "fxb_001_v2"}, "Luna Park · v2"},
		{"draft with mother, no label", lineage.TitleIn{FromDraft: true, HasMother: true, ParentTitle: "Luna Park", RequestID: "fxb_001_v2"}, "Luna Park · Fxb 001 V2"},
		{"draft without mother", lineage.TitleIn{FromDraft: true, RequestID: "fxb_001_v2", Label: "v2"}, "Fxb 001 V2"},
		{"compose", lineage.TitleIn{RequestID: "syn-a-s1101"}, "Syn A S1101"},
		{"blank explicit falls through", lineage.TitleIn{Explicit: "  ", RequestID: "syn-a"}, "Syn A"},
	}
	for _, c := range cases {
		if got := lineage.DefaultTitle(c.in); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	long := strings.Repeat("é", 300)
	got := lineage.DefaultTitle(lineage.TitleIn{Variant: true, ParentTitle: long, Seed: 1})
	if n := len([]rune(got)); n != 200 {
		t.Errorf("long title has %d runes", n)
	}
	if lineage.CutRunes("abc", 0) != "" || lineage.CutRunes("abc", 5) != "abc" {
		t.Error("CutRunes")
	}
	if lineage.Humanise("---") != "---" {
		t.Error("Humanise of separators only")
	}
}
