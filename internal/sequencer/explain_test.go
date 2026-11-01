package sequencer

import (
	"slices"
	"strings"
	"testing"
)

func tempoR(code string, from, to int) Reason {
	return Reason{Code: code, Params: map[string]int{"from": from, "to": to, "pct": pctX100(from, to)}}
}

func keyR(code string, fn, fb, tn, tb int) Reason {
	return Reason{Code: code, Params: map[string]int{"from_n": fn, "from_b": fb, "to_n": tn, "to_b": tb}}
}

func energyR(code string, from, to int) Reason {
	return Reason{Code: code, Params: map[string]int{"delta": to - from, "from": from, "to": to}}
}

func state(code string, bpm, kn, kb, e int) Reason {
	return Reason{Code: code, Params: map[string]int{"bpm": bpm, "key_n": kn, "key_b": kb, "energy": e}}
}

type explainRow struct {
	Name     string   `json:"name"`
	Strategy Strategy `json:"strategy"`
	Step     Step     `json:"step"`
	Text     string   `json:"text"`
}

func explainRows() []explainRow {
	row := func(name string, s Strategy, pos int, rs ...Reason) explainRow {
		return explainRow{Name: name, Strategy: s, Step: Step{Position: pos, SongID: "s", Reasons: rs}}
	}
	unk := func(c string) Reason { return Reason{Code: c} }
	closer := func(from, to int) Reason {
		return Reason{Code: ReasonCloser, Params: map[string]int{"from": from, "to": to}}
	}
	return []explainRow{
		row("opener_arc", StrategyArc, 1, state(ReasonOpener, 9600, 8, 0, 0)),
		row("opener_steady_unknown", StrategySteady, 1, state(ReasonOpener, 0, 0, 0, NoEnergy)),
		row("transition_up", StrategyArc, 2, tempoR(ReasonTempoClose, 9600, 9800), keyR(ReasonKeySame, 8, 0, 8, 0), energyR(ReasonEnergyRise, 0, 2500)),
		row("peak", StrategyArc, 5, state(ReasonArcPeak, 10800, 9, 1, 10000), tempoR(ReasonTempoClose, 10400, 10800)),
		row("peak_closes", StrategyArc, 4, state(ReasonArcPeak, 10800, 9, 1, 10000), closer(5000, 10000)),
		row("lands_softly", StrategyArc, 6, closer(5000, 1667)),
		row("unknowns", StrategyArc, 3, unk(ReasonKeyUnknown), unk(ReasonEnergyUnknown)),
		row("key_adjacent", StrategyArc, 4, keyR(ReasonKeyAdjacent, 8, 0, 9, 0)),
		row("key_boost", StrategyArc, 4, keyR(ReasonKeyBoost, 8, 0, 10, 0)),
		row("tempo_octave_fall", StrategySteady, 2, tempoR(ReasonTempoOctave, 7600, 15200), energyR(ReasonEnergyFall, 6000, 4000)),
		row("tempo_jump_clash", StrategyRampUp, 3, tempoR(ReasonTempoJump, 9600, 12000), keyR(ReasonKeyClash, 8, 0, 11, 1)),
		row("shift_diagonal_hold", StrategyArc, 3, tempoR(ReasonTempoShift, 9600, 10800), keyR(ReasonKeyDiagonal, 8, 0, 9, 1), energyR(ReasonEnergyHold, 5000, 5400)),
		row("no_features", StrategyArc, 2, unk(ReasonNoFeatures), unk(ReasonTempoUnknown), unk(ReasonKeyUnknown), unk(ReasonEnergyUnknown)),
		row("closer_steady_relative", StrategySteady, 6, closer(5000, 5000), keyR(ReasonKeyRelative, 8, 0, 8, 1), energyR(ReasonEnergyHold, 5000, 5000)),
	}
}

func TestExplainText_Golden(t *testing.T) {
	t.Parallel()
	rows := explainRows()
	if len(rows) != 14 {
		t.Fatalf("%d rows, want 14", len(rows))
	}
	for i := range rows {
		rows[i].Text = ExplainText(rows[i].Step, rows[i].Strategy)
		for _, bad := range []string{"title", "t1"} {
			if strings.Contains(rows[i].Text, bad) {
				t.Errorf("%s leaks %q", rows[i].Name, bad)
			}
		}
	}
	golden(t, "explain_text.golden.json", rows)
	// The worded examples of the contract.
	for name, want := range map[string]string{
		"opener_arc":    "1. opens low: 96 BPM, key 8A, energy 0.00 (relative)",
		"transition_up": "2. tempo 96 → 98 BPM (+2.1 %), key 8A → 8A (same), energy +0.25 — on the way up",
		"peak":          "5. peak: 108 BPM, key 9B, energy 1.00",
		"lands_softly":  "6. lands softly: energy 0.50 → 0.17",
		"unknowns":      "3. key unknown (neutral); no energy measurement (neutral)",
		"key_adjacent":  "4. key 8A → 9A (neighbour on the wheel)",
		"key_boost":     "4. key 8A → 10A (two steps apart: a deliberate lift, costlier than a neighbour)",
	} {
		for _, r := range rows {
			if r.Name == name && r.Text != want {
				t.Errorf("%s = %q, want %q", name, r.Text, want)
			}
		}
	}
}

func TestExplainSteps_TSet(t *testing.T) {
	t.Parallel()
	spec, _ := SpecOf(StrategyArc)
	order := pick(tSet(), "t1", "t2", "t3", "t4", "t5", "t6")
	steps := explainSteps(order, spec)
	var texts []string
	var sum int64
	w := effWeights(spec.Weights, coverageOf(order))
	rel := withRel(order)
	for i, st := range steps {
		texts = append(texts, ExplainText(st, StrategyArc))
		if st.Position != i+1 || st.SongID != order[i].ID || st.Target != targetFor(StrategyArc, i, 6) {
			t.Errorf("step %d = %+v", i, st)
		}
		if st.From != nil {
			sum += st.From.Cost
		}
		sum += shapeJ(w, spec, rel[i].Energyx10000, i, 6)
	}
	if want := objective(order, spec); sum != want {
		t.Errorf("transition + shape = %d, objective %d", sum, want)
	}
	if steps[0].From != nil || steps[1].From == nil || steps[0].EnergyRel != 0 || steps[4].EnergyRel != 10000 {
		t.Errorf("steps = %+v", steps)
	}
	if !strings.HasPrefix(texts[0], "1. opens low: 96 BPM, key 8A") || !strings.HasPrefix(texts[4], "5. peak: 108 BPM, key 9B, energy 1.00") ||
		!strings.Contains(texts[5], "lands softly") {
		t.Errorf("texts = %q", texts)
	}
	if !slices.Equal(steps[0].Missing, nil) {
		t.Errorf("missing = %v", steps[0].Missing)
	}
	golden(t, "explain_steps_tset_arc.golden.json", steps)
}

func TestExplainSteps_Unknowns(t *testing.T) {
	t.Parallel()
	spec, _ := SpecOf(StrategySteady)
	order := []Song{{ID: "a", BPMx100: 9000, Energyx10000: NoEnergy}, {ID: "b", Energyx10000: NoEnergy}}
	st := explainSteps(order, spec)
	if !slices.Equal(st[0].Missing, []string{"key", "energy"}) || !slices.Equal(st[1].Missing, []string{"bpm", "key", "energy"}) {
		t.Errorf("missing = %v / %v", st[0].Missing, st[1].Missing)
	}
	if st[1].Reasons[0].Code != ReasonNoFeatures || st[1].Target != -1 || st[1].EnergyRel != NoEnergy {
		t.Errorf("step = %+v", st[1])
	}
	if got := ExplainText(st[1], StrategySteady); got != "2. no tempo, key or energy known (neutral)" {
		t.Errorf("text = %q", got)
	}
	// Coverage is tempo 50, key 0, energy 0: only the tempo term weighs, at the neutral cost.
	if want := int64(100*50) * 500_000; st[1].From.Cost != want {
		t.Errorf("cost = %d, want %d", st[1].From.Cost, want)
	}
	if st1 := ExplainText(explainSteps([]Song{{ID: "x", Energyx10000: NoEnergy}}, spec)[0], StrategySteady); st1 != "1. opens: tempo unknown, key unknown, no energy measurement" {
		t.Errorf("opener = %q", st1)
	}
}

func TestCoverageWarnings(t *testing.T) {
	t.Parallel()
	if w := coverageWarnings(tSet()); len(w) != 0 {
		t.Errorf("warnings = %+v", w)
	}
	if w := coverageWarnings(tSet()[:1]); len(w) != 1 || w[0].Code != WarnSingleSong {
		t.Errorf("single = %+v", w)
	}
	ss := tSet()
	ss[0].Key, ss[1].Key, ss[2].Key, ss[3].Key = nil, nil, nil, nil
	ss[4].Bases.Energy = "predicted"
	ss[5] = Song{ID: "none", Energyx10000: NoEnergy}
	want := []Warning{
		{Code: WarnNoFeatures, Song: "none"},
		{Code: WarnLowCoverage, Feature: "key", Known: 1, Total: 6},
		{Code: WarnPredictedEnergy, N: 1},
	}
	if got := coverageWarnings(ss); !slices.Equal(got, want) {
		t.Errorf("warnings = %+v, want %+v", got, want)
	}
}

func TestPctAndFormat(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ a, b, want int }{{9600, 9800, 208}, {9800, 9600, -204}, {10000, 10000, 0}, {7600, 15200, 10000}} {
		if got := pctX100(c.a, c.b); got != c.want {
			t.Errorf("pct(%d,%d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if fmtBPM(9650) != "96.5" || fmtBPM(9600) != "96" || fmtDelta(-2500) != "-0.25" || fmtDelta(0) != "+0.00" || fmtDelta(-10) != "+0.00" {
		t.Errorf("format: %s %s %s %s %s", fmtBPM(9650), fmtBPM(9600), fmtDelta(-2500), fmtDelta(0), fmtDelta(-10))
	}
}
