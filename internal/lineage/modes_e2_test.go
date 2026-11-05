package lineage

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// TestModes_Table_E2 pins the E2 rows of the C149.1 mode table and the engine axis: every mode against
// both engines, with the engine-specific patch rules.
func TestModes_Table_E2(t *testing.T) {
	t.Parallel()
	type row struct {
		mode, engine string
		ok           bool
		field, rule  string
		patch        *Patch
		ep           EnginePresence
	}
	bpm := EnginePresence{BPM: true}
	cases := []row{
		// new modes exist for acestep15 only
		{ModePort, EngineACEStep, true, "", "", nil, EnginePresence{}},
		{ModePort, EngineYuE2, false, "", "", nil, EnginePresence{}},
		{ModeRepaint, EngineYuE2, false, "", "", nil, EnginePresence{}},
		{ModeExtend, EngineYuE2, false, "", "", nil, EnginePresence{}},
		{ModeReference, EngineYuE2, false, "", "", nil, EnginePresence{}},
		{ModeComplete, EngineYuE2, false, "", "", nil, EnginePresence{}},
		{ModeStems, EngineYuE2, false, "", "", nil, EnginePresence{}},
		// patch rules of the new rows
		{ModePort, EngineACEStep, true, "", "", &Patch{Style: sp("x"), Lyrics: sp("y")}, EnginePresence{BPM: true, KeyScale: true}},
		{ModePort, EngineACEStep, true, "patch.duration_s", "duration_s_forbidden", nil, EnginePresence{DurationS: true}},
		{ModePort, EngineACEStep, true, "patch.timesignature", "timesignature_forbidden", nil, EnginePresence{TimeSignature: true}},
		{ModePort, EngineACEStep, true, "patch.cot", "cot_forbidden", &Patch{Cot: sp("off")}, EnginePresence{}},
		{ModePort, EngineACEStep, true, "patch.abc", "abc_forbidden", &Patch{ABC: yue2.OptString{Set: true, Null: true}}, EnginePresence{}},
		{ModePort, EngineACEStep, true, "patch.cfg_scale", "cfg_scale_forbidden", &Patch{CFGScale: OptNumber{Set: true, Value: 2}}, EnginePresence{}},
		{ModeExtend, EngineACEStep, true, "", "", &Patch{Style: sp("x")}, EnginePresence{}},
		{ModeExtend, EngineACEStep, true, "patch.lyrics", "lyrics_forbidden", &Patch{Lyrics: sp("x")}, EnginePresence{}},
		{ModeExtend, EngineACEStep, true, "patch.bpm", "bpm_forbidden", nil, bpm},
		{ModeRepaint, EngineACEStep, true, "", "", &Patch{Style: sp("x"), Lyrics: sp("y")}, EnginePresence{}},
		{ModeRepaint, EngineACEStep, true, "patch.bpm", "bpm_forbidden", nil, bpm},
		{ModeReference, EngineACEStep, true, "", "", &Patch{Style: sp("x")}, EnginePresence{BPM: true, KeyScale: true, DurationS: true}},
		{ModeReference, EngineACEStep, true, "patch.timesignature", "timesignature_forbidden", nil, EnginePresence{TimeSignature: true}},
		{ModeComplete, EngineACEStep, true, "", "", &Patch{Style: sp("x")}, EnginePresence{}},
		{ModeComplete, EngineACEStep, true, "patch.lyrics", "lyrics_forbidden", &Patch{Lyrics: sp("x")}, EnginePresence{}},
		{ModeStems, EngineACEStep, true, "", "", nil, EnginePresence{}},
		{ModeStems, EngineACEStep, true, "patch", "patch_forbidden", &Patch{Style: sp("x")}, EnginePresence{}},
		{ModeStems, EngineACEStep, true, "patch", "patch_forbidden", nil, bpm},
		// variants on both engines
		{ModeNewSeed, EngineYuE2, true, "", "", nil, EnginePresence{}},
		{ModeNewSeed, EngineACEStep, true, "", "", nil, EnginePresence{}},
		{ModeNewSeed, EngineACEStep, true, "patch", "patch_forbidden", nil, bpm},
		{ModeSameSeedEdit, EngineYuE2, true, "patch.bpm", "bpm_forbidden", nil, bpm},
		{ModeSameSeedEdit, EngineYuE2, true, "patch.duration_s", "duration_s_forbidden", &Patch{}, EnginePresence{DurationS: true}},
		{ModeSameSeedEdit, EngineACEStep, true, "", "", nil, EnginePresence{BPM: true, KeyScale: true, TimeSignature: true, DurationS: true}},
		{ModeSameSeedEdit, EngineACEStep, true, "patch.abc", "abc_forbidden", &Patch{ABC: yue2.OptString{Set: true}}, EnginePresence{}},
		{ModeSameSeedEdit, EngineACEStep, true, "patch.cfg_scale", "cfg_scale_forbidden", &Patch{CFGScale: OptNumber{Set: true}}, EnginePresence{}},
		{ModeStyleEdit, EngineACEStep, true, "", "", &Patch{Style: sp("x"), Cot: sp("off")}, EnginePresence{BPM: true, KeyScale: true}},
		{ModeStyleEdit, EngineACEStep, true, "patch.duration_s", "duration_s_forbidden", &Patch{Style: sp("x")}, EnginePresence{DurationS: true}},
		{ModeStyleEdit, EngineACEStep, true, "patch.style", "style_required", &Patch{}, bpm},
		{ModeStyleEdit, EngineYuE2, true, "patch.bpm", "bpm_forbidden", &Patch{Style: sp("x")}, bpm},
		{ModeLyricEdit, EngineACEStep, true, "", "", &Patch{Lyrics: sp("x")}, EnginePresence{DurationS: true}},
		{ModeLyricEdit, EngineACEStep, true, "patch.bpm", "bpm_forbidden", &Patch{Lyrics: sp("x")}, bpm},
		{ModeLyricEdit, EngineACEStep, true, "patch", "patch_required", nil, EnginePresence{}},
		{ModeComposeNew, EngineACEStep, true, "", "", nil, EnginePresence{}},
		{ModeComposeNew, EngineYuE2, true, "", "", nil, EnginePresence{}},
		{ModeFromTemplate, EngineACEStep, true, "", "", nil, EnginePresence{}},
		{ModeFromTemplate, EngineYuE2, true, "", "", nil, EnginePresence{}},
		{"teleport", EngineACEStep, false, "", "", nil, EnginePresence{}},
		{ModeNewSeed, "other", false, "", "", nil, EnginePresence{}},
	}
	if len(cases) < 40 {
		t.Fatalf("table has %d rows, want >= 40", len(cases))
	}
	for _, c := range cases {
		spec, ok := SpecFor(c.mode, c.engine)
		if ok != c.ok {
			t.Errorf("%s/%s: ok = %v, want %v", c.mode, c.engine, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		field, rule := spec.CheckPatchFor(c.engine, c.patch, c.ep)
		if field != c.field || rule != c.rule {
			t.Errorf("%s/%s: CheckPatchFor = %q %q, want %q %q", c.mode, c.engine, field, rule, c.field, c.rule)
		}
	}
}

func TestModes_E2Rows(t *testing.T) {
	t.Parallel()
	want := map[string]struct {
		op, task, capability, parent string
		seeds                        SeedRule
		part                         string
	}{
		ModePort:      {OpEnginePort, "text2music", "", "required", SeedsParentNew, "E2a"},
		ModeExtend:    {OpExtend, "extend", "extend", "required", SeedsParentNew, "E2b"},
		ModeRepaint:   {OpRepaint, "repaint", "repaint", "required", SeedsParentNew, "E2a"},
		ModeReference: {OpReferenceRender, "text2music", "reference", "required", SeedsParentNew, "E2a"},
		ModeComplete:  {OpCompleteTracks, "complete", "complete", "required", SeedsParentOnly, "E2b"},
		ModeStems:     {OpExtractStems, "extract", "stems", "required", SeedsParentOnly, "E2b"},
	}
	for mode, w := range want {
		m, ok := Spec(mode)
		if !ok {
			t.Errorf("%s: no row", mode)
			continue
		}
		if m.Operator != w.op || m.Task != w.task || m.Capability != w.capability || m.Parent != w.parent || m.Seeds != w.seeds || m.Part != w.part {
			t.Errorf("%s = %+v", mode, m)
		}
		if !slices.Equal(m.Engines, []string{EngineACEStep}) {
			t.Errorf("%s engines = %v", mode, m.Engines)
		}
		if !ValidOperator(m.Operator) {
			t.Errorf("%s: operator %q not valid", mode, m.Operator)
		}
		if err := CheckOperator(m.Operator); err != nil {
			t.Errorf("%s: CheckOperator: %v", mode, err)
		}
	}
	// the M3 table is untouched
	if len(Modes) != 7 {
		t.Errorf("Modes has %d rows, want the M3 7", len(Modes))
	}
	for _, m := range Modes {
		if len(m.Engines) != 0 {
			t.Errorf("M3 row %s has engines %v", m.Mode, m.Engines)
		}
	}
	for _, op := range []string{"engine_port", "extend", "repaint", "reference_render", "complete_tracks", "extract_stems"} {
		if !ValidOperator(op) {
			t.Errorf("operator %q rejected", op)
		}
	}
	if ValidOperator("nope") {
		t.Error("unknown operator accepted")
	}
}

func TestModeAllowed_Engine(t *testing.T) {
	t.Parallel()
	if !ModeAllowed(ModeNewSeed, EngineYuE2) || !ModeAllowed(ModeNewSeed, EngineACEStep) {
		t.Error("variant modes are open to every engine")
	}
	if ModeAllowed(ModeRepaint, EngineYuE2) || !ModeAllowed(ModeRepaint, EngineACEStep) {
		t.Error("repaint is acestep15 only")
	}
	if ModeAllowed("nope", EngineACEStep) {
		t.Error("unknown mode allowed")
	}
}

// TestKeys_ACEStepRequest pins that the engine-neutral key functions work on an ACE-Step canonical request:
// the id is out of both keys, the seed out of the content key only.
func TestKeys_ACEStepRequest(t *testing.T) {
	t.Parallel()
	a := []byte(`{"caption":"c","engine_params_version":"v","id":"a-1","seed":1,"task":"text2music"}`)
	b := []byte(`{"caption":"c","engine_params_version":"v","id":"b-2","seed":2,"task":"text2music"}`)
	c := []byte(`{"caption":"c","engine_params_version":"v","id":"c-3","seed":1,"task":"repaint"}`)
	ca, ra := Keys(a)
	cb, rb := Keys(b)
	cc, rc := Keys(c)
	if ca != cb || ra == rb {
		t.Errorf("seed must change only the render key: %v", cmp.Diff([]string{ca, ra}, []string{cb, rb}))
	}
	if ca == cc || ra == rc {
		t.Error("task must change both keys")
	}
}

// TestMutation_ReplayReproducesRender_E2 replays every E2 operator: applying the stored mode over the stored
// parent request twice reproduces the child's render hash, the child's task is the row's task, and the src sha
// pins the audio (a different master digest is a different render).
func TestMutation_ReplayReproducesRender_E2(t *testing.T) {
	t.Parallel()
	const sha1 = "1111111111111111111111111111111111111111111111111111111111111111"
	const sha2 = "2222222222222222222222222222222222222222222222222222222222222222"
	eng := acestep.Engine{}
	parent := []byte(`{"id":"syn-a","task":"text2music","caption":"warm pop","lyrics":"[Verse]\nla la","instrumental":false,"vocal_language":"en","duration_ds":1800,"seed":7,"thinking":true}`)
	base, is := eng.ParseRecipe(parent)
	if len(is) > 0 {
		t.Fatalf("parent: %v", is)
	}
	ip := func(i int) *int { return &i }
	patch := func(mode, sha string) *acestep.Patch {
		src := &acestep.SrcRef{TrackID: "trk-1", MasterSHA256: sha, DurationDS: 2000}
		switch mode {
		case ModeRepaint:
			return &acestep.Patch{Src: src, RepaintFromDS: ip(100), RepaintToDS: ip(400)}
		case ModeExtend:
			return &acestep.Patch{Src: src, ExtendDS: ip(300)}
		case ModeComplete:
			return &acestep.Patch{Src: src, Tracks: []string{"vocals"}}
		case ModeStems:
			return &acestep.Patch{Src: src, Tracks: []string{"drums"}}
		case ModeReference:
			return &acestep.Patch{Ref: &acestep.RefRef{TrackID: "trk-1", ClipSHA256: sha, FromDS: 0, ToDS: 200}}
		}
		return nil // port
	}
	for _, row := range E2Modes {
		derive := func(sha string) (string, string) {
			p := engine.ModeParams{Mode: row.Mode, Operator: row.Operator, Seed: 8, ID: "syn-a-s8"}
			var pt any
			if pp := patch(row.Mode, sha); pp != nil {
				pt = pp
			}
			child, err := eng.ApplyMode(base, p, pt)
			if err != nil {
				t.Fatalf("%s: %v", row.Mode, err)
			}
			if r, _, is := acestep.Parse(child.Canonical); len(is) > 0 && acestep.HasErrors(is) || r.Task != row.Task {
				t.Fatalf("%s: task %q, want %q (%v)", row.Mode, r.Task, row.Task, is)
			}
			_, render := Keys(child.Canonical)
			return render, string(child.Canonical)
		}
		r1, _ := derive(sha1)
		r2, _ := derive(sha1)
		r3, _ := derive(sha2)
		if r1 == "" || r1 != r2 {
			t.Errorf("%s: replay render hash %q != %q", row.Mode, r1, r2)
		}
		if row.Mode != ModePort && r1 == r3 {
			t.Errorf("%s: the audio sha does not pin the render", row.Mode)
		}
	}
}
