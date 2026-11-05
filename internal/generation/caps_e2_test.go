package generation_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// TestEstimate_CapabilityOff_Table: a mode whose capability the verified-features file has not turned on is 422
// ENGINE_CAPABILITY with the capability name, before anything is planned (C149.1).
func TestEstimate_CapabilityOff_Table(t *testing.T) {
	t.Parallel()
	sec := 10.0
	tracks := []string{"vocals"}
	type row struct {
		name       string
		edit       func(*acestep.Features)
		item       func(x *rigE2) generation.Item
		capability string
	}
	port := func(x *rigE2) string { return x.info.Port }
	rows := []row{
		{"repaint off", func(f *acestep.Features) { f.Tasks = []string{"text2music"} }, func(x *rigE2) generation.Item { return repaintItem(port(x), 8, 14) }, "repaint"},
		{"extend off", func(f *acestep.Features) { f.Tasks = []string{"text2music", "repaint"} }, func(x *rigE2) generation.Item {
			it := parentItem(lineage.ModeExtend, port(x))
			it.Edit = &generation.Edit{ExtendS: &sec}
			return it
		}, "extend"},
		{"complete off", func(f *acestep.Features) { f.Tasks = []string{"text2music"} }, func(x *rigE2) generation.Item {
			it := parentItem(lineage.ModeComplete, port(x))
			it.Edit = &generation.Edit{Tracks: tracks}
			return it
		}, "complete"},
		{"complete off with bundle only models", func(f *acestep.Features) { f.ModelsMode = acestep.ModelsBundleOnly }, func(x *rigE2) generation.Item {
			it := parentItem(lineage.ModeComplete, port(x))
			it.Edit = &generation.Edit{Tracks: tracks}
			return it
		}, "complete"},
		{"stems off", func(f *acestep.Features) { f.Tasks = []string{"text2music"} }, func(x *rigE2) generation.Item {
			it := parentItem(lineage.ModeStems, port(x))
			it.Edit = &generation.Edit{Tracks: tracks}
			return it
		}, "stems"},
		{"reference off", func(f *acestep.Features) { f.Reference = false }, func(x *rigE2) generation.Item {
			return refItem(lineage.ModeReference, x.info.Takes["T1"], generation.Reference{TrackID: x.info.Takes["T1"]})
		}, "reference"},
		{"same_seed_edit needs seed_reproducible", func(f *acestep.Features) { f.SeedReproducible = false }, func(x *rigE2) generation.Item {
			it := parentItem(lineage.ModeSameSeedEdit, port(x))
			b := 100
			it.EnginePatch = &generation.EnginePatch{BPM: &b}
			return it
		}, "same_seed_edit"},
	}
	for _, c := range rows {
		x := newRigE2(t, withFeatures(c.edit))
		_, err := x.estimate(agent, "", c.item(x))
		pe := wantProblem(t, err, 422, "ENGINE_CAPABILITY")
		if pe.Details["capability"] != c.capability || pe.Details["engine"] != aceName {
			t.Errorf("%s: details = %v", c.name, pe.Details)
		}
	}
	// port, the always-on modes and the compose path stay available with nothing verified but text2music
	x := newRigE2(t, withFeatures(func(f *acestep.Features) {
		f.Tasks = []string{"text2music"}
		f.Reference = false
		f.SeedReproducible = false
	}))
	for _, it := range []generation.Item{parentItem(lineage.ModePort, x.info.Takes["T1"]), newSeedItem(x.info.Port, 1), composeACE(t, "ace-cap-1", nil)} {
		if _, err := x.estimate(agent, "", it); err != nil {
			t.Errorf("%s: %v", it.Mode, err)
		}
	}
}

func TestEstimate_Port_Warnings_FromYue2(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	// Luna has a chain of thought and no explicit length: neither has an ACE-Step equivalent (abc_dropped, the third translation
	// warning, needs a recipe that carries an ABC request field; the translation itself is pinned in acestep's fromyue2 tests)
	e := x.mustEstimate(agent, "", parentItem(lineage.ModePort, lt.TrackLuna))
	for _, w := range []string{generation.WarnCotIgnored, generation.WarnDurationEstimated} {
		if !hasWarning(e, w) {
			t.Errorf("missing %s, got %v", w, warningCodes(e))
		}
	}
	for _, w := range e.Warnings {
		if w.Severity != "warning" || w.TakeIndex != 0 {
			t.Errorf("warning = %+v", w)
		}
	}
}

func TestEstimate_ParamsVersionChanged(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	// the fixture's ACE-Step takes are pinned to an old parameter version: a derivation re-pins it and says so
	e := x.mustEstimate(agent, "", newSeedItem(x.info.Port, 1))
	if acestep.CurrentParamsVersion() != "ace1" && !hasWarning(e, generation.WarnParamsVersionChanged) {
		t.Errorf("warnings = %v", warningCodes(e))
	}
	if got := x.request(x.stored(e)[0]).ParamsVersion; got != acestep.CurrentParamsVersion() {
		t.Errorf("version = %s", got)
	}
}

func TestEstimate_ComposeACE_Reference(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	// the item's reference pins the clip into the recipe
	it := composeACE(t, "ace-cref-1", nil)
	it.Reference = &generation.Reference{TrackID: t1, FromS: fp(30), ToS: fp(55)}
	e := x.mustEstimate(agent, "", it)
	r := x.request(x.stored(e)[0])
	if r.Ref == nil || r.Ref.TrackID != t1 || r.Ref.FromDS != 300 || r.Ref.ToDS != 550 || len(r.Ref.ClipSHA256) != 64 {
		t.Errorf("ref = %+v", r.Ref)
	}
	if !hasWarning(e, generation.WarnReferenceNotReproducible) {
		t.Errorf("warnings = %v", warningCodes(e))
	}
	// a ref inside the recipe is re-resolved: the digest the client wrote is not trusted
	forged := composeACE(t, "ace-cref-2", func(m map[string]any) {
		m["ref"] = map[string]any{"track_id": t1, "clip_sha256": "aa" + "00000000000000000000000000000000000000000000000000000000000000"[:62], "from_ds": 300, "to_ds": 550}
	})
	e = x.mustEstimate(agent, "", forged)
	got := x.request(x.stored(e)[0]).Ref
	if got == nil || got.ClipSHA256[:2] == "aa" {
		t.Errorf("forged digest survived: %+v", got)
	}
}

func TestEstimate_ComposeACE_KeepsFreeIDAndSeed(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	e := x.mustEstimate(agent, "", composeACE(t, "ace-keep-1", nil))
	tk := e.Items[0].Takes[0]
	if tk.RequestID != "ace-keep-1" || tk.Seed != 42 || tk.Operator != "compose_new" {
		t.Errorf("take = %+v", tk)
	}
	two := composeACE(t, "ace-keep-2", nil)
	two.NSeeds = 2
	e = x.mustEstimate(agent, "", two)
	if e.Takes != 2 || e.Items[0].Takes[0].Seed != 42 || e.Items[0].Takes[1].Seed == 42 {
		t.Errorf("takes = %+v", e.Items[0].Takes)
	}
}
