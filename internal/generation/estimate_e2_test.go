package generation_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

const aceName = "acestep15"

func parentItem(mode, parent string) generation.Item {
	return generation.Item{Mode: mode, ParentTrackID: &parent}
}

func repaintItem(parent string, from, to float64) generation.Item {
	it := parentItem(lineage.ModeRepaint, parent)
	it.Edit = &generation.Edit{FromS: &from, ToS: &to}
	return it
}

func hasWarning(e generation.Estimate, code string) bool {
	return slices.ContainsFunc(e.Warnings, func(w generation.Warning) bool { return w.Code == code })
}

func warningCodes(e generation.Estimate) []string {
	var out []string
	for _, w := range e.Warnings {
		out = append(out, w.Code)
	}
	slices.Sort(out)
	return out
}

func TestEstimate_Port_Yue2Parent(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	e := x.mustEstimate(agent, aceName, parentItem(lineage.ModePort, t1))
	if e.Engine != aceName || e.Backend != "fake-acestep" || e.Takes != 1 {
		t.Fatalf("estimate = engine %q backend %q takes %d", e.Engine, e.Backend, e.Takes)
	}
	tp := e.Items[0].Takes[0]
	if tp.Engine != aceName || tp.Task != "text2music" || tp.TaskClass != "turbo" || tp.Operator != "engine_port" || tp.CrossEngine != true {
		t.Errorf("take = %+v", tp)
	}
	if !hasWarning(e, generation.WarnDurationEstimated) {
		t.Errorf("port of a yue2 take must warn duration_estimated, got %v", warningCodes(e))
	}
	if hasWarning(e, generation.WarnCrossEngineEdit) {
		t.Error("port is the cross-engine render itself; cross_engine_edit is for the edit modes")
	}
	// the stored request is an ACE-Step text2music request pinned to the current parameters
	var req acestep.Request
	if err := json.Unmarshal([]byte(x.str(`SELECT json_extract(items_json, '$[0].request_json') FROM estimates WHERE id = ?`, e.ID)), &req); err != nil {
		t.Fatal(err)
	}
	if req.Task != "text2music" || req.ParamsVersion != acestep.CurrentParamsVersion() || req.DurationDS == 0 {
		t.Errorf("request = %+v", req)
	}
	if e.Cost.Ceiling <= 0 || e.Cost.Max <= 0 {
		t.Errorf("a port is a real spend: %+v", e.Cost)
	}
}

func TestEstimate_ACEEngineFromMode_NoBodyEngine(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	e := x.mustEstimate(agent, "", parentItem(lineage.ModePort, x.info.Takes["T1"]))
	if e.Engine != aceName {
		t.Errorf("engine = %q, want the mode's engine", e.Engine)
	}
}

func TestEstimate_Yue2UnchangedByE2Registry(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	e := x.mustEstimate(agent, "", newSeedItem(x.info.Takes["T1"], 2))
	if e.Engine != "yue2" || e.Backend != "fake" || e.Takes != 2 {
		t.Fatalf("estimate = %q %q %d", e.Engine, e.Backend, e.Takes)
	}
	tp := e.Items[0].Takes[0]
	if tp.Engine != "" || tp.Task != "" || tp.TaskClass != "" || tp.Edit != nil || tp.Reference != nil || tp.CrossEngine {
		t.Errorf("a yue2 take carries no E2 fields: %+v", tp)
	}
}

func TestEstimate_OneEnginePerBatch(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	before := x.count(`SELECT COUNT(*) FROM estimates`)
	for name, c := range map[string]struct {
		engine string
		items  []generation.Item
	}{
		"yue2 and port items":       {"", []generation.Item{newSeedItem(t1, 1), parentItem(lineage.ModePort, t1)}},
		"yue2 variant, ace variant": {"", []generation.Item{newSeedItem(t1, 1), newSeedItem(x.info.Root, 1)}},
		"body yue2, port item":      {"yue2", []generation.Item{parentItem(lineage.ModePort, t1)}},
		"body ace, yue2 variant":    {aceName, []generation.Item{newSeedItem(t1, 1)}},
	} {
		_, err := x.estimate(agent, c.engine, c.items...)
		pe := wantProblem(t, err, 422, "VALIDATION_FAILED")
		if pe.Details["rule"] != "one_engine_per_batch" {
			t.Errorf("%s: details = %v", name, pe.Details)
		}
	}
	if n := x.count(`SELECT COUNT(*) FROM estimates`); n != before {
		t.Errorf("%d estimates written by refused batches", n-before)
	}
}

func TestEstimate_ACEVariantsOfACEParent(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	port := x.info.Port
	bpm := 110
	for name, it := range map[string]generation.Item{
		"new_seed":       newSeedItem(port, 2),
		"same_seed_edit": {Mode: lineage.ModeSameSeedEdit, ParentTrackID: &port, EnginePatch: &generation.EnginePatch{BPM: &bpm}},
		"style_edit":     {Mode: lineage.ModeStyleEdit, ParentTrackID: &port, Patch: &generation.Patch{Style: strp("synthwave, warm pads")}},
		"lyric_edit":     {Mode: lineage.ModeLyricEdit, ParentTrackID: &port, Patch: &generation.Patch{Lyrics: strp("[verse]\nnew words")}},
	} {
		e, err := x.estimate(agent, "", it)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if e.Engine != aceName || e.Items[0].Takes[0].Task != "text2music" {
			t.Errorf("%s: %+v", name, e.Items[0].Takes[0])
		}
	}
}

func fp(v float64) *float64 { return &v }

func strp(s string) *string { return &s }

func composeACE(t *testing.T, id string, mutate func(map[string]any)) generation.Item {
	t.Helper()
	m := map[string]any{"id": id, "task": "text2music", "caption": "synth pop, warm", "lyrics": "[verse]\nla la la", "instrumental": false,
		"vocal_language": "en", "duration_ds": 600, "seed": 42, "thinking": true, "engine_params_version": acestep.CurrentParamsVersion(), "inference_steps": 8}
	if mutate != nil {
		mutate(m)
	}
	return generation.Item{Mode: lineage.ModeComposeNew, Engine: aceName, Recipe: rawJSON(m)}
}

func TestEstimate_E2_Table(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1, port, root := x.info.Takes["T1"], x.info.Port, x.info.Root
	withTracks := func(mode, parent string, tracks ...string) generation.Item {
		it := parentItem(mode, parent)
		it.Edit = &generation.Edit{Tracks: tracks}
		return it
	}
	extend := func(parent string, s float64) generation.Item {
		it := parentItem(lineage.ModeExtend, parent)
		it.Edit = &generation.Edit{ExtendS: &s}
		return it
	}
	reference := func(parent string, from, to *float64) generation.Item {
		it := parentItem(lineage.ModeReference, parent)
		it.Reference = &generation.Reference{TrackID: parent, FromS: from, ToS: to}
		return it
	}
	bpm, dur := 110, 20.0
	port2 := parentItem(lineage.ModePort, t1)
	port2.EnginePatch = &generation.EnginePatch{BPM: &bpm}
	portDur := parentItem(lineage.ModePort, t1)
	portDur.EnginePatch = &generation.EnginePatch{DurationS: &dur}
	seedBPM := newSeedItem(port, 1)
	seedBPM.EnginePatch = &generation.EnginePatch{BPM: &bpm}
	yueBPM := newSeedItem(t1, 1)
	yueBPM.EnginePatch = &generation.EnginePatch{BPM: &bpm}
	badCot := parentItem(lineage.ModeStyleEdit, port)
	badCot.Patch = &generation.Patch{Style: strp("x"), Cot: strp("full")}
	nSeeds := parentItem(lineage.ModeStems, t1)
	nSeeds.Edit = &generation.Edit{Tracks: []string{"vocals"}}
	nSeeds.NSeeds = 2
	repaintN := repaintItem(t1, 10, 25)
	repaintN.NSeeds = 3
	yueRepaint := repaintItem(t1, 10, 25)
	yueRepaint.Engine = "yue2"
	draftACE := generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{"x"}, Engine: aceName}
	extraEdit := repaintItem(t1, 10, 25)
	extraEdit.Edit.ExtendS = fp(5)
	refOnPort := parentItem(lineage.ModePort, t1)
	refOnPort.Reference = &generation.Reference{TrackID: t1}
	portOnACE := parentItem(lineage.ModePort, port)
	noParent := generation.Item{Mode: lineage.ModeRepaint, Edit: &generation.Edit{FromS: fp(1), ToS: fp(8)}}

	type want struct {
		status  int
		code    string
		details map[string]any
	}
	rows := []struct {
		name   string
		engine string
		items  []generation.Item
		err    *want
		takes  int
		warns  []string // warning codes that must be present
		noWarn []string
		class  string // task class of the first take
		task   string
	}{
		{name: "repaint ace parent", items: []generation.Item{repaintItem(port, 8, 14)}, takes: 1, class: "turbo", task: "repaint", noWarn: []string{generation.WarnCrossEngineEdit}},
		{name: "repaint yue2 parent is labelled cross-engine", engine: aceName, items: []generation.Item{repaintItem(t1, 10, 25)}, takes: 1, warns: []string{generation.WarnCrossEngineEdit}, task: "repaint"},
		{name: "repaint three seeds", items: []generation.Item{repaintN}, takes: 3, task: "repaint"},
		{name: "repaint range order", items: []generation.Item{repaintItem(port, 14, 8)}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "range_order", "item": 0}}},
		{name: "repaint equal ends", items: []generation.Item{repaintItem(port, 8, 8.04)}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "range_order"}}},
		{name: "repaint range required", items: []generation.Item{parentItem(lineage.ModeRepaint, port)}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "range_required"}}},
		{name: "repaint outside source", items: []generation.Item{repaintItem(port, 20, 40)}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "range_outside_source"}}},
		{name: "repaint too short", items: []generation.Item{repaintItem(port, 8, 10)}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "range_too_short"}}},
		{name: "repaint too long", items: []generation.Item{repaintItem(t1, 10, 105)}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "range_too_long"}}},
		{name: "repaint ab window truncated", items: []generation.Item{repaintItem(t1, 10, 70)}, takes: 1, warns: []string{generation.WarnABWindowTruncated}},
		{name: "repaint without parent", items: []generation.Item{noParent}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "required", "field": "parent_track_id"}}},
		{name: "repaint with extend field", items: []generation.Item{extraEdit}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "not_allowed_for_repaint"}}},
		{name: "repaint explicit yue2 engine", items: []generation.Item{yueRepaint}, err: &want{422, "ENGINE_CAPABILITY", map[string]any{"capability": "mode", "mode": "repaint"}}},
		{name: "extend", items: []generation.Item{extend(port, 30)}, takes: 1, task: "extend", warns: []string{generation.WarnExtendSeamExperimental}},
		{name: "extend required", items: []generation.Item{parentItem(lineage.ModeExtend, port)}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "extend_required"}}},
		{name: "extend too long", items: []generation.Item{extend(port, 100)}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "extend_too_long"}}},
		{name: "extend too short", items: []generation.Item{extend(port, 1)}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "extend_too_short"}}},
		{name: "complete", items: []generation.Item{withTracks(lineage.ModeComplete, port, "vocals", "drums")}, takes: 1, class: "base", task: "complete", warns: []string{generation.WarnBaseClassCost}},
		{name: "complete tracks required", items: []generation.Item{parentItem(lineage.ModeComplete, port)}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "tracks_required"}}},
		{name: "complete five tracks", items: []generation.Item{withTracks(lineage.ModeComplete, port, "vocals", "drums", "bass", "guitar", "synth")}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "tracks_too_many"}}},
		{name: "complete duplicate", items: []generation.Item{withTracks(lineage.ModeComplete, port, "vocals", "vocals")}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "tracks_duplicate"}}},
		{name: "complete unknown name", items: []generation.Item{withTracks(lineage.ModeComplete, port, "kazoo")}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "unknown_track_name"}}},
		{name: "stems one job per name", items: []generation.Item{withTracks(lineage.ModeStems, port, "vocals", "drums", "bass")}, takes: 3, class: "base", task: "extract"},
		{name: "stems seven names", items: []generation.Item{withTracks(lineage.ModeStems, port, "vocals", "drums", "bass", "guitar", "synth", "strings", "brass")}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "tracks_too_many"}}},
		{name: "stems n_seeds", items: []generation.Item{nSeeds}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "parent_seed_only"}}},
		{name: "reference window", items: []generation.Item{reference(t1, fp(5), fp(25))}, takes: 1, task: "text2music", warns: []string{generation.WarnReferenceNotReproducible}, noWarn: []string{generation.WarnReferenceWindowShort}},
		{name: "reference default tail", items: []generation.Item{reference(t1, nil, nil)}, takes: 1, warns: []string{generation.WarnReferenceNotReproducible}},
		{name: "reference too long", items: []generation.Item{reference(t1, fp(0), fp(40))}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "reference_window"}}},
		{name: "reference too short", items: []generation.Item{reference(t1, fp(0), fp(1))}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "reference_window"}}},
		{name: "reference past the end", items: []generation.Item{reference(port, fp(20), fp(40))}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "reference_window"}}},
		{name: "reference on a port item", items: []generation.Item{refOnPort}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "not_allowed_for_port"}}},
		{name: "port with bpm", items: []generation.Item{port2}, takes: 1, task: "text2music"},
		{name: "port with duration is forbidden", items: []generation.Item{portDur}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "duration_s_forbidden"}}},
		{name: "port of an ace parent", items: []generation.Item{portOnACE}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "parent_not_yue2"}}},
		{name: "ace new_seed with bpm is forbidden", items: []generation.Item{seedBPM}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "patch_forbidden"}}},
		{name: "yue2 variant with bpm", items: []generation.Item{yueBPM}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "patch_forbidden"}}},
		{name: "ace cot must be think or off", items: []generation.Item{badCot}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "cot_invalid"}}},
		{name: "from_draft is yue2 only", items: []generation.Item{draftACE}, err: &want{422, "ENGINE_CAPABILITY", map[string]any{"capability": "mode"}}},
		{name: "compose_new ace", items: []generation.Item{composeACE(t, "ace-new-1", nil)}, takes: 1, task: "text2music", class: "turbo"},
		{name: "compose_new ace instrumental with lyrics", items: []generation.Item{composeACE(t, "ace-new-2", func(m map[string]any) { m["instrumental"] = true })}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "instrumental_lyrics"}}},
		{name: "compose_new ace unknown key", items: []generation.Item{composeACE(t, "ace-new-3", func(m map[string]any) { m["abc"] = "X:1" })}, err: &want{422, "LINT_FAILED", nil}},
		{name: "compose_new ace repaint task", items: []generation.Item{composeACE(t, "ace-new-4", func(m map[string]any) { m["task"] = "repaint" })}, err: &want{422, "INVALID_ITEM", map[string]any{"rule": "task_not_text2music"}}},
		{name: "compose_new ace too long", items: []generation.Item{composeACE(t, "ace-new-5", func(m map[string]any) { m["duration_ds"] = 5000 })}, err: &want{422, "ENGINE_CAPABILITY", map[string]any{"capability": "max_seconds"}}},
		{name: "compose_new ace stale parameters", items: []generation.Item{composeACE(t, "ace-new-6", func(m map[string]any) { m["engine_params_version"] = "ancient" })}, err: &want{422, "ENGINE_PARAMS_STALE", nil}},
		{name: "compose_new ace id taken", items: []generation.Item{composeACE(t, "e2-ace-root", nil)}, takes: 1}, // keep-or-allocate: the taken id gets a seed suffix
	}
	if len(rows) < 30 {
		t.Fatalf("table has %d rows, want >= 30", len(rows))
	}
	for _, c := range rows {
		e, err := x.estimate(agent, c.engine, c.items...)
		if c.err != nil {
			pe := wantProblem(t, err, c.err.status, c.err.code)
			for k, v := range c.err.details {
				if pe.Details[k] != v {
					t.Errorf("%s: details[%s] = %v, want %v (all: %v)", c.name, k, pe.Details[k], v, pe.Details)
				}
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if e.Takes != c.takes {
			t.Errorf("%s: takes = %d, want %d", c.name, e.Takes, c.takes)
		}
		first := e.Items[0].Takes[0]
		if c.task != "" && first.Task != c.task {
			t.Errorf("%s: task = %q, want %q", c.name, first.Task, c.task)
		}
		if c.class != "" && first.TaskClass != c.class {
			t.Errorf("%s: class = %q, want %q", c.name, first.TaskClass, c.class)
		}
		for _, w := range c.warns {
			if !hasWarning(e, w) {
				t.Errorf("%s: missing warning %s, got %v", c.name, w, warningCodes(e))
			}
		}
		for _, w := range c.noWarn {
			if hasWarning(e, w) {
				t.Errorf("%s: unexpected warning %s", c.name, w)
			}
		}
	}
	_ = root
}

func TestEstimate_TemplateOverACEParents(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	tpl := generation.Item{Mode: lineage.ModeFromTemplate, TemplateMode: lineage.ModeNewSeed, ParentTrackIDs: []string{x.info.Port, x.info.Root}, NSeeds: 1}
	e := x.mustEstimate(agent, "", tpl)
	if e.Engine != aceName || e.Takes != 2 {
		t.Fatalf("estimate = %q %d", e.Engine, e.Takes)
	}
	mixed := tpl
	mixed.ParentTrackIDs = []string{x.info.Port, x.info.Takes["T1"]}
	_, err := x.estimate(agent, "", mixed)
	pe := wantProblem(t, err, 422, "VALIDATION_FAILED")
	if pe.Details["rule"] != "one_engine_per_batch" {
		t.Errorf("details = %v", pe.Details)
	}
	// an explicit item engine that contradicts the parent is refused on that item
	bad := newSeedItem(x.info.Port, 1)
	bad.Engine = "yue2"
	_, err = x.estimate(agent, "", bad)
	pe = wantProblem(t, err, 422, "INVALID_ITEM")
	if pe.Details["rule"] != "parent_engine_mismatch" || pe.Details["field"] != "engine" {
		t.Errorf("details = %v", pe.Details)
	}
	// an ACE-Step parent that is not a text2music take has no variants
	rep := newSeedItem(x.info.Repaint, 1)
	_, err = x.estimate(agent, "", rep)
	pe = wantProblem(t, err, 422, "INVALID_ITEM")
	if pe.Details["rule"] != "parent_not_text2music" {
		t.Errorf("details = %v", pe.Details)
	}
}

func TestEstimate_ACEPatchFields(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	port := x.info.Port
	bpm, dur := 128, 45.0
	key, sig := "A minor", "3"
	it := generation.Item{Mode: lineage.ModeSameSeedEdit, ParentTrackID: &port, Patch: &generation.Patch{Cot: strp("off")},
		EnginePatch: &generation.EnginePatch{BPM: &bpm, KeyScale: &key, TimeSignature: &sig, DurationS: &dur}}
	e := x.mustEstimate(agent, "", it)
	r := x.request(x.stored(e)[0])
	if r.BPM == nil || *r.BPM != 128 || r.KeyScale != "A Minor" || r.TimeSignature != "3" || r.DurationDS != 450 || r.Thinking {
		t.Errorf("request = %+v", r)
	}
	if e.Items[0].Takes[0].ExpectedAudioS != 45 {
		t.Errorf("expected audio = %d", e.Items[0].Takes[0].ExpectedAudioS)
	}
	// cot think
	on := generation.Item{Mode: lineage.ModeStyleEdit, ParentTrackID: &port, Patch: &generation.Patch{Style: strp("x"), Cot: strp("think")}}
	if r := x.request(x.stored(x.mustEstimate(agent, "", on))[0]); !r.Thinking {
		t.Error("cot think did not switch thinking on")
	}
	// a stored patch document for replay
	doc := string(x.stored(e)[0].Patch)
	for _, want := range []string{`"bpm":128`, `"keyscale":"A minor"`, `"timesignature":"3"`, `"duration_ds":450`, `"thinking":false`} {
		if !strings.Contains(doc, want) {
			t.Errorf("patch doc %s lacks %s", doc, want)
		}
	}
	// a non-finite length slips past the schema
	inf := 1 / zeroF()
	it.EnginePatch = &generation.EnginePatch{DurationS: &inf}
	_, err := x.estimate(agent, "", it)
	wantProblem(t, err, 422, "INVALID_ITEM")
}

func zeroF() float64 { return 0 }

func TestEstimate_UnknownItemEngine(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	it := newSeedItem(x.info.Takes["T1"], 1)
	it.Engine = "nope"
	_, err := x.estimate(agent, "", it)
	pe := wantProblem(t, err, 422, "VALIDATION_FAILED")
	if pe.Details["field"] != "engine" {
		t.Errorf("details = %v", pe.Details)
	}
	// the body engine must exist as well
	_, err = x.estimate(agent, "nope", it)
	wantProblem(t, err, 422, "VALIDATION_FAILED")
}

func TestEstimate_TrackWithOddEngineIsRefusedByMothers(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	// a take whose recipe engine the registry does not know (here: removed from the registry) has no engine to derive
	port := x.info.Port
	x.svc = newServiceWithoutACE(x)
	_, err := x.estimate(agent, "", newSeedItem(port, 1))
	if err == nil {
		t.Error("an unregistered engine cannot plan its takes")
	}
}
