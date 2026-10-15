package picks_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/picks"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestPicks_PriorSDMatchesLadder(t *testing.T) {
	t.Parallel()
	f, err := ladder.FitBTD(1, nil, ladder.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f.SD[0] != picks.LadderPriorSD {
		t.Fatalf("ladder default PriorSD %v != picks.LadderPriorSD %v", f.SD[0], picks.LadderPriorSD)
	}
}

func TestPick_EvidenceGolden(t *testing.T) {
	t.Parallel()
	cases := map[string]picks.Input{
		"pinned": {Takes: []picks.TakeInput{tk("a", 0), tk("b", 1)}, PinnedTakeID: sp("a")},
		"ladder": {IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(1.2, 0.5)), tk("b", 1, us(0.3, 0.6))}},
		"ladder_incumbent_held": {IntraDuels: 3, IncumbentTakeID: sp("b"),
			Takes: []picks.TakeInput{tk("a", 0, us(0.90, 0.5)), tk("b", 1, us(0.60, 0.6))}},
		"stars":  {Takes: []picks.TakeInput{tk("a", 0, stars(4)), tk("b", 1, stars(4))}},
		"newest": {Takes: []picks.TakeInput{tk("a", 0), tk("b", 1)}},
		"newest_after_margin": {IntraDuels: 3,
			Takes: []picks.TakeInput{tk("a", 0, us(0.6, 0.5)), tk("b", 1, us(0.3, 0.6))}},
		"newest_few_duels": {IntraDuels: 2, Takes: []picks.TakeInput{tk("a", 0), tk("b", 1)}},
		"pin_ineligible": {PinnedTakeID: sp("a"),
			Takes: []picks.TakeInput{tk("a", 0, status("discarded")), tk("b", 1)}},
		"none": {Takes: []picks.TakeInput{tk("a", 0, status("discarded"))}},
	}
	out := map[string]json.RawMessage{}
	for name, in := range cases {
		b, err := json.Marshal(picks.Derive(in).Evidence)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = b
	}
	got, err := json.MarshalIndent(out, "", "  ") // map keys are sorted: deterministic
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	p := filepath.Join("testdata", "evidence_rules.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got) {
		t.Errorf("evidence golden differs (run with -update after a deliberate change):\n%s", got)
	}
}
