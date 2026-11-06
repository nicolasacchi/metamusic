package generation

import (
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func TestSeconds_Rounding(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   float64
		want int
	}{{12.34, 123}, {12.35, 124}, {0, 0}, {0.05, 1}, {0.04, 0}, {600, 6000}, {89.95, 900}} {
		if got := ds(c.in); got != c.want {
			t.Errorf("ds(%v) = %d, want %d", c.in, got, c.want)
		}
	}
	if validSeconds(-0.04) {
		t.Error("-0.04 s is a validation error")
	}
	if !validSeconds(0) || validSeconds(1/zero()) {
		t.Error("0 is valid, +Inf is not")
	}
	if secs(123) != 12.3 {
		t.Errorf("secs(123) = %v", secs(123))
	}
}

func zero() float64 { return 0 }

func TestRefWindow_Defaults(t *testing.T) {
	t.Parallel()
	f := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		name         string
		dur          int
		from, to     *float64
		wantF, wantT int
	}{
		{"tail of a long track", 1000, nil, nil, 800, 1000},
		{"whole short track", 120, nil, nil, 0, 120},
		{"from only", 1000, f(2), nil, 20, 220},
		{"from only near the end", 1000, f(95), nil, 950, 1000},
		{"to only", 1000, nil, f(25), 50, 250},
		{"to only near the start", 1000, nil, f(5), 0, 50},
		{"both", 1000, f(10), f(30), 100, 300},
	} {
		if gf, gt := refWindow(c.dur, c.from, c.to); gf != c.wantF || gt != c.wantT {
			t.Errorf("%s: %d..%d, want %d..%d", c.name, gf, gt, c.wantF, c.wantT)
		}
	}
}

func TestRefWarnings_ShortWindow(t *testing.T) {
	t.Parallel()
	// the engine currently refuses windows under 20 s at parse time, so the under-5 s warning is reached through the planner
	// only when that bound is fixed; the rule itself is pinned here
	p := &planner{eng: acestep.Engine{Verified: acestep.Features{ReferenceReproducible: true}}}
	ac := &aceCtx{ref: &refPin{Short: true}}
	got := p.refWarnings(ac)
	if len(got) != 1 || got[0].Code != WarnReferenceWindowShort {
		t.Errorf("warnings = %+v", got)
	}
	p.eng = acestep.Engine{}
	ac.ref.Short = false
	if got := p.refWarnings(ac); len(got) != 1 || got[0].Code != WarnReferenceNotReproducible {
		t.Errorf("warnings = %+v", got)
	}
	if got := p.refWarnings(&aceCtx{}); got != nil {
		t.Errorf("no reference, no warnings: %+v", got)
	}
}

func TestRefClipErr_Mapping(t *testing.T) {
	t.Parallel()
	for code, want := range map[string]string{
		"CLIP_REFERENCE_WINDOW":     "INVALID_ITEM/reference_window",
		"CLIP_REFERENCE_SILENT":     "INVALID_ITEM/reference_silent",
		"CLIP_UNSUPPORTED_RATE":     "ENGINE_CAPABILITY/src_format",
		"CLIP_UNSUPPORTED_CHANNELS": "ENGINE_CAPABILITY/src_format",
		"OTHER":                     "OTHER/",
	} {
		err := refClipErr(3, problem.E(422, code, "m", nil))
		var pe *problem.Error
		if !errors.As(err, &pe) {
			t.Fatalf("%s: %v", code, err)
		}
		got := pe.Code + "/"
		if r, ok := pe.Details["rule"].(string); ok {
			got += r
		}
		if c, ok := pe.Details["capability"].(string); ok {
			got += c
		}
		if got != want {
			t.Errorf("%s: %s, want %s", code, got, want)
		}
	}
	if err := refClipErr(0, errors.New("x")); err == nil || err.Error() != "x" {
		t.Errorf("plain error = %v", err)
	}
}

func TestDeriveIssues(t *testing.T) {
	t.Parallel()
	got := deriveIssues(errors.New("acestep: derived recipe invalid: range_too_short (repaint_to_ds)"))
	if len(got) != 1 || got[0].Code != "range_too_short" || got[0].Field != "repaint_to_ds" {
		t.Errorf("issue = %+v", got)
	}
	if got := deriveIssues(errors.New("boom")); got[0].Code != "invalid_request" {
		t.Errorf("issue = %+v", got)
	}
}

func TestRatio(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		n, d int
		want string
	}{{512, 100, "5.1"}, {100, 100, "1.0"}, {3, 2, "1.5"}, {1, 0, "several"}} {
		if got := ratio(c.n, c.d); got != c.want {
			t.Errorf("ratio(%d,%d) = %s, want %s", c.n, c.d, got, c.want)
		}
	}
}

func TestIsVariant_E2Modes(t *testing.T) {
	t.Parallel()
	for _, m := range []string{lineage.ModeNewSeed, lineage.ModeSameSeedEdit, lineage.ModeLyricEdit, lineage.ModeStyleEdit,
		lineage.ModePort, lineage.ModeExtend, lineage.ModeRepaint, lineage.ModeReference, lineage.ModeComplete, lineage.ModeStems} {
		if !isVariant(m) {
			t.Errorf("%s derives from a parent take", m)
		}
	}
	for _, m := range []string{lineage.ModeComposeNew, lineage.ModeFromDraft, lineage.ModeFromTemplate, "nope"} {
		if isVariant(m) {
			t.Errorf("%s is no variant", m)
		}
	}
}

func TestItemTakeCount_E2(t *testing.T) {
	t.Parallel()
	pid := "p"
	for _, c := range []struct {
		it   Item
		want int
	}{
		{Item{Mode: lineage.ModeStems, ParentTrackID: &pid, Edit: &Edit{Tracks: []string{"vocals", "drums"}}, NSeeds: 5}, 2},
		{Item{Mode: lineage.ModeComplete, ParentTrackID: &pid, Edit: &Edit{Tracks: []string{"vocals", "drums"}}}, 1},
		{Item{Mode: lineage.ModeRepaint, ParentTrackID: &pid, NSeeds: 3}, 3},
		{Item{Mode: lineage.ModePort, ParentTrackID: &pid}, 1},
		{Item{Mode: lineage.ModeStems, ParentTrackID: &pid}, 1},
	} {
		if got := itemTakeCount(c.it); got != c.want {
			t.Errorf("%s: %d, want %d", c.it.Mode, got, c.want)
		}
	}
}

func TestClipResolveWindow_AcceptsABWindow(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ from, to, dur int }{{80, 140, 1000}, {500, 560, 1000}, {950, 1000, 1000}, {200, 600, 1000}, {100, 900, 1000}} {
		w := RepaintABWindow(c.from, c.to, c.dur)
		from, to := secs(w.FromDS), secs(w.ToDS)
		if _, err := clip.ResolveWindow(secs(c.dur), &from, &to); err != nil {
			t.Errorf("%+v -> %+v: the existing duel window rejects it: %v", c, w, err)
		}
	}
}
