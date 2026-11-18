package generation_test

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

func withCover(f *acestep.Features) { f.Tasks = append(f.Tasks, acestep.TaskCover) }

func coverItem(parent string, strength float64) generation.Item {
	it := parentItem(lineage.ModeCover, parent)
	s := "slow jazz trio, brushed drums"
	it.Patch = &generation.Patch{Style: &s}
	it.Edit = &generation.Edit{Strength: &strength}
	return it
}

func TestEstimate_Cover(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withFeatures(withCover))
	ace, yue := x.info.Port, x.info.Takes["T1"]

	// ACE parent: one take, turbo, source length, strength in milli, no cross-engine warning
	e := x.mustEstimate(agent, "", coverItem(ace, 0.5))
	tk := e.Items[0].Takes[0]
	if e.Takes != 1 || tk.Task != "cover" || tk.TaskClass != "turbo" || hasWarning(e, generation.WarnCrossEngineEdit) {
		t.Errorf("ace: takes %d %+v warns %v", e.Takes, tk, warningCodes(e))
	}
	if tk.Edit == nil || tk.Edit.Strength == nil || *tk.Edit.Strength != 0.5 {
		t.Errorf("edit echo %+v", tk.Edit)
	}
	st := x.stored(e)[0]
	r := x.request(st)
	if r.Task != acestep.TaskCover || r.CoverStrengthMilli == nil || *r.CoverStrengthMilli != 500 || r.Src == nil || r.Caption != "slow jazz trio, brushed drums" || r.DurationDS != 0 {
		t.Errorf("request %+v", r)
	}
	if !strings.Contains(string(st.Patch), `"src"`) {
		t.Errorf("patch doc lacks src: %s", st.Patch)
	}

	// strength is rounded to milli; 0 and 1 are valid
	for in, want := range map[float64]int{0: 0, 1: 1000, 0.2004: 200, 0.2006: 201} {
		r := x.request(x.stored(x.mustEstimate(agent, "", coverItem(ace, in)))[0])
		if r.CoverStrengthMilli == nil || *r.CoverStrengthMilli != want {
			t.Errorf("strength %v -> %v want %d", in, r.CoverStrengthMilli, want)
		}
	}

	// YuE2 parent: cross-engine, labelled
	e = x.mustEstimate(agent, aceName, coverItem(yue, 0.3))
	if !hasWarning(e, generation.WarnCrossEngineEdit) || e.Items[0].Takes[0].Task != "cover" {
		t.Errorf("yue2 parent: %v", warningCodes(e))
	}

	// several seeds are several takes
	two := coverItem(ace, 0.5)
	two.NSeeds = 2
	if e := x.mustEstimate(agent, "", two); e.Takes != 2 {
		t.Errorf("n_seeds 2 -> %d takes", e.Takes)
	}

	hi := 1.2
	neg := -0.1
	noStyle := coverItem(ace, 0.5)
	noStyle.Patch = nil
	noStrength := coverItem(ace, 0.5)
	noStrength.Edit = nil
	extra := coverItem(ace, 0.5)
	ext := 5.0
	extra.Edit.ExtendS = &ext
	for name, c := range map[string]struct {
		it   generation.Item
		rule string
	}{
		"strength above 1":  {coverItem(ace, hi), "strength_range"},
		"strength negative": {coverItem(ace, neg), "strength_range"},
		"no strength":       {noStrength, "strength_required"},
		"no style":          {noStyle, "patch_required"},
		"extend_s on cover": {extra, "not_allowed_for_cover"},
	} {
		_, err := x.estimate(agent, "", c.it)
		pe := wantProblem(t, err, 422, "INVALID_ITEM")
		if pe.Details["rule"] != c.rule {
			t.Errorf("%s: %v", name, pe.Details)
		}
	}
	// strength on another mode
	rp := repaintItem(ace, 8, 14)
	s := 0.5
	rp.Edit.Strength = &s
	_, err := x.estimate(agent, "", rp)
	if pe := wantProblem(t, err, 422, "INVALID_ITEM"); pe.Details["rule"] != "not_allowed_for_repaint" || pe.Details["field"] != "edit.strength" {
		t.Errorf("repaint strength: %v", pe.Details)
	}
}

func TestEstimate_Cover_CapabilityOff(t *testing.T) {
	t.Parallel()
	x := newRigE2(t) // the file does not list cover
	_, err := x.estimate(agent, "", coverItem(x.info.Port, 0.5))
	pe := wantProblem(t, err, 422, "ENGINE_CAPABILITY")
	if c := pe.Details["capability"]; c != "mode" && c != "cover" {
		t.Errorf("details %v", pe.Details)
	}
}

func TestEstimate_Cover_QuotesTurboAtSourceLength(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withFeatures(withCover))
	e := x.mustEstimate(agent, "", coverItem(x.info.Port, 0.5), repaintItem(x.info.Port, 8, 14))
	c, rp := e.Items[0].Takes[0], e.Items[1].Takes[0]
	// both render the whole 30 s source on the turbo row, so they quote the same
	if c.TaskClass != "turbo" || c.ExpectedAudioS != 30 || c.Max != rp.Max || c.TimeoutS != rp.TimeoutS {
		t.Errorf("cover %+v must quote like repaint %+v", c, rp)
	}
	if hasWarning(e, generation.WarnBaseClassCost) {
		t.Errorf("cover is turbo: %v", warningCodes(e))
	}
}
