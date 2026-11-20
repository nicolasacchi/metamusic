package tierb_test

import (
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
)

func TestLevenshteinCharSim(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		a, b string
		d    int
		sim  float64
	}{
		{"", "", 0, 1}, {"a", "", 1, 0}, {"chiave", "kiave", 2, 1 - 2.0/6}, {"mare", "male", 1, 0.75}, {"città", "citta", 1, 0.8},
	} {
		if d := tierb.Levenshtein([]rune(c.a), []rune(c.b)); d != c.d {
			t.Errorf("Levenshtein(%q,%q) = %d, want %d", c.a, c.b, d, c.d)
		}
		if s := tierb.CharSim(c.a, c.b); math.Abs(s-c.sim) > 1e-12 {
			t.Errorf("CharSim(%q,%q) = %v, want %v", c.a, c.b, s, c.sim)
		}
	}
}

func ops(ps []tierb.Pair) string {
	var b strings.Builder
	for _, p := range ps {
		b.WriteString(p.Op)
	}
	return b.String()
}

func TestAlign_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		ref, hyp string
		ops      string
	}{
		{"identical", "a b c", "a b c", "MMM"},
		{"both empty", "", "", ""},
		{"all deleted", "a b", "", "DD"},
		{"all inserted", "", "a b", "II"},
		{"substitution", "la chiave del mare", "la kiave del male", "MSMS"},
		{"deletion", "sotto il cielo", "sotto cielo", "MDM"},
		{"insertion", "sotto cielo", "sotto il cielo", "MIM"},
		{"dissimilar words still pair", "aaaa", "zzzz", "S"},
		{"tie prefers substitution over delete+insert", "ab", "ba", "S"},
		{"repeat", "a a a", "a a", "MMD"},
	} {
		ref, hyp := strings.Fields(c.ref), strings.Fields(c.hyp)
		got := tierb.Align(ref, hyp, tierb.CharSim)
		if ops(got) != c.ops {
			t.Errorf("%s: ops %q, want %q", c.name, ops(got), c.ops)
		}
		if again := tierb.Align(ref, hyp, tierb.CharSim); cmp.Diff(got, again) != "" {
			t.Errorf("%s: not deterministic", c.name)
		}
	}
	// pairs carry indexes and similarity
	got := tierb.Align([]string{"chiave"}, []string{"kiave"}, tierb.CharSim)
	want := []tierb.Pair{{Ref: 0, Hyp: 0, Op: "S", Sim: 1 - 2.0/6}}
	if diff := cmp.Diff(want, got, cmpopts.EquateApprox(0, 1e-12)); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func FuzzAlign(f *testing.F) {
	f.Add("la chiave del mare", "la kiave mare")
	f.Add("", "x y")
	f.Add("a a a b", "b a")
	f.Fuzz(func(t *testing.T, r, h string) {
		ref, hyp := strings.Fields(r), strings.Fields(h)
		if len(ref) > 40 || len(hyp) > 40 {
			t.Skip()
		}
		ps := tierb.Align(ref, hyp, tierb.CharSim)
		nr, nh := 0, 0
		for _, p := range ps {
			if p.Ref >= 0 {
				if p.Ref != nr {
					t.Fatalf("ref index %d, want %d", p.Ref, nr)
				}
				nr++
			}
			if p.Hyp >= 0 {
				if p.Hyp != nh {
					t.Fatalf("hyp index %d, want %d", p.Hyp, nh)
				}
				nh++
			}
		}
		if nr != len(ref) || nh != len(hyp) {
			t.Fatalf("covered %d/%d ref, %d/%d hyp", nr, len(ref), nh, len(hyp))
		}
	})
}
