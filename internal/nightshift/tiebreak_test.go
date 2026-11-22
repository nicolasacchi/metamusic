package nightshift

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type fakeTrust struct {
	got    []string
	out    []TrustedCritic
	err    error
	called int
}

func (f *fakeTrust) TrustedMetrics(_ context.Context, tracks []string) ([]TrustedCritic, error) {
	f.called++
	f.got = tracks
	return f.out, f.err
}

func TestTieBreak_Table(t *testing.T) {
	t.Parallel()
	ls := []Lineage{lin("a", 5, "p"), lin("b", 5, "p"), lin("c", 5, "p")}
	cases := []struct {
		name    string
		critics []TrustedCritic
		want    map[string]float64
	}{
		{"none", nil, nil},
		{"direction plus", []TrustedCritic{{ID: "x", Direction: 1, Values: map[string]float64{"a-best": 10, "b-best": 20}}},
			map[string]float64{"a": -1, "b": 1, "c": 0}},
		{"direction minus flips the sign", []TrustedCritic{{ID: "x", Direction: -1, Values: map[string]float64{"a-best": 10, "b-best": 20}}},
			map[string]float64{"a": 1, "b": -1, "c": 0}},
		{"two critics add", []TrustedCritic{
			{ID: "x", Direction: 1, Values: map[string]float64{"a-best": 10, "b-best": 20}},
			{ID: "y", Direction: 1, Values: map[string]float64{"a-best": 1, "c-best": 3}}},
			map[string]float64{"a": -2, "b": 1, "c": 1}},
		{"one value has no spread", []TrustedCritic{{ID: "x", Direction: 1, Values: map[string]float64{"a-best": 10}}}, nil},
		{"identical values have no spread", []TrustedCritic{{ID: "x", Direction: 1, Values: map[string]float64{"a-best": 4, "b-best": 4}}}, nil},
		{"values of other takes are ignored", []TrustedCritic{{ID: "x", Direction: 1, Values: map[string]float64{"zzz": 1, "a-best": 2, "b-best": 4}}},
			map[string]float64{"a": -1, "b": 1, "c": 0}},
		{"zero direction is ignored", []TrustedCritic{{ID: "x", Direction: 0, Values: map[string]float64{"a-best": 1, "b-best": 3}}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if d := cmp.Diff(c.want, TieBreak(ls, c.critics)); d != "" {
				t.Errorf("TieBreak (-want +got):\n%s", d)
			}
		})
	}
}

func TestTieBreak_Deterministic(t *testing.T) {
	t.Parallel()
	ls := []Lineage{lin("a", 5, "p"), lin("b", 5, "p"), lin("c", 5, "p")}
	cs := []TrustedCritic{{ID: "x", Direction: 1, Values: map[string]float64{"a-best": 1.5, "b-best": 7.25, "c-best": 3}},
		{ID: "y", Direction: -1, Values: map[string]float64{"a-best": 9, "b-best": 2, "c-best": 5}}}
	first := TieBreak(ls, cs)
	for i := 0; i < 20; i++ {
		if d := cmp.Diff(first, TieBreak(ls, cs)); d != "" {
			t.Fatalf("run %d differs:\n%s", i, d)
		}
	}
}

func TestTieBreakFor_AsksForBestTakes(t *testing.T) {
	t.Parallel()
	ls := []Lineage{lin("b", 5, "p"), lin("a", 5, "p")}
	f := &fakeTrust{out: []TrustedCritic{{ID: "x", Direction: 1, Values: map[string]float64{"a-best": 1, "b-best": 3}}}}
	got, err := TieBreakFor(context.Background(), f, ls)
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff([]string{"a-best", "b-best"}, f.got); d != "" {
		t.Errorf("tracks asked (-want +got):\n%s", d)
	}
	if d := cmp.Diff(map[string]float64{"a": -1, "b": 1}, got); d != "" {
		t.Errorf("tie-break (-want +got):\n%s", d)
	}
	if got, err := TieBreakFor(context.Background(), nil, ls); got != nil || err != nil {
		t.Errorf("no source: %v %v", got, err)
	}
	if got, err := TieBreakFor(context.Background(), f, nil); got != nil || err != nil {
		t.Errorf("no lineages: %v %v", got, err)
	}
	boom := &fakeTrust{err: errors.New("boom")}
	if _, err := TieBreakFor(context.Background(), boom, ls); err == nil {
		t.Error("a failing source must fail the call (the caller decides to plan without it)")
	}
}

// the trusted critics decide only between lineages the planner already ranks equal.
func TestPlan_TrustedCriticTieBreak(t *testing.T) {
	t.Parallel()
	a, b := lin("a", 5, "pop"), lin("b", 5, "pop")
	in := baseInput(a, b)
	in.MaxTakes, in.ExplorePPM = 1, 0
	in.TieBreak = TieBreak(in.Lineages, []TrustedCritic{{ID: "tier_a.true_peak", Direction: -1,
		Values: map[string]float64{"a-best": -1.0, "b-best": -3.0}}}) // lower peak is better: b
	if p, _ := Plan(in); len(p) != 1 || p[0].RootID != "b" {
		t.Errorf("trusted critic should pick b: %v", roots(p))
	}
	in.TieBreak = TieBreak(in.Lineages, []TrustedCritic{{ID: "tier_a.true_peak", Direction: -1,
		Values: map[string]float64{"a-best": -3.0, "b-best": -1.0}}})
	if p, _ := Plan(in); len(p) != 1 || p[0].RootID != "a" {
		t.Errorf("trusted critic should pick a: %v", roots(p))
	}
}
