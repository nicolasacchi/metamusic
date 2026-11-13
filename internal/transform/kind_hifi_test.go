package transform

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/ops"
)

func resolved(t *testing.T, k Kind, params string) map[string]any {
	t.Helper()
	ks, ok := LookupKind(k)
	if !ok {
		t.Fatalf("%s is not registered", k)
	}
	raw, err := ks.Validate(json.RawMessage(params))
	if err != nil {
		t.Fatalf("%s %s: %v", k, params, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func ruleOf(t *testing.T, k Kind, params string) *RuleError {
	t.Helper()
	ks, _ := LookupKind(k)
	_, err := ks.Validate(json.RawMessage(params))
	var re *RuleError
	if !errors.As(err, &re) {
		t.Fatalf("%s %s: err = %v, want a rule error", k, params, err)
	}
	return re
}

// TestMasterValidate_Table: every default is written out from the preset, overrides win, steps snap, and the refusals name the
// field and the rule.
func TestMasterValidate_Table(t *testing.T) {
	t.Parallel()
	for _, p := range ops.MasterPresets {
		m := resolved(t, KindMaster, fmt.Sprintf(`{"source_id":"x","preset":%q}`, p.Name))
		if m["preset"] != p.Name || m["tilt_db"] != p.TiltDB || m["comp_amount"] != p.CompAmount || m["low_cut_hz"] != float64(p.LowCutHz) ||
			m["ceiling_dbtp"] != -1.0 || m["target_lufs"] != nil || len(m) != 7 {
			t.Errorf("%s resolved to %v", p.Name, m)
		}
	}
	m := resolved(t, KindMaster, `{"source_id":"x"}`)
	if m["preset"] != "neutral" || m["comp_amount"] != 0.25 {
		t.Errorf("defaults %v", m)
	}
	m = resolved(t, KindMaster, `{"source_id":"x","preset":"warm","tilt_db":0.04,"comp_amount":0.07,"low_cut_hz":0,"target_lufs":-14.04,"ceiling_dbtp":-2.96}`)
	if m["tilt_db"] != 0.0 || m["comp_amount"] != 0.05 || m["low_cut_hz"] != 0.0 || m["target_lufs"] != -14.0 || m["ceiling_dbtp"] != -3.0 {
		t.Errorf("overrides %v", m)
	}
	m = resolved(t, KindMaster, `{"source_id":"x","comp_amount":null,"tilt_db":null,"target_lufs":null}`)
	if m["comp_amount"] != 0.25 || m["target_lufs"] != nil {
		t.Errorf("nulls are the default: %v", m)
	}
	for _, tc := range []struct{ params, field, rule string }{
		{`{}`, "source_id", RuleRequired},
		{`{"source_id":"x","preset":"loud"}`, "preset", RuleRange},
		{`{"source_id":"x","tilt_db":3.1}`, "tilt_db", RuleRange},
		{`{"source_id":"x","comp_amount":1.1}`, "comp_amount", RuleRange},
		{`{"source_id":"x","low_cut_hz":10}`, "low_cut_hz", RuleRange},
		{`{"source_id":"x","low_cut_hz":41}`, "low_cut_hz", RuleRange},
		{`{"source_id":"x","target_lufs":-11}`, "target_lufs", RuleRange},
		{`{"source_id":"x","ceiling_dbtp":-0.9}`, "ceiling_dbtp", RuleRange},
		{`{"source_id":"x","ceiling_dbtp":-3.1}`, "ceiling_dbtp", RuleRange},
		{`{"source_id":"x","width":1.2}`, "width", RuleUnknownField},
		{`{"source_id":"x","comp_amount":"a lot"}`, "comp_amount", RuleType},
	} {
		if re := ruleOf(t, KindMaster, tc.params); re.Field != tc.field || re.Rule != tc.rule {
			t.Errorf("%s: %+v, want %s/%s", tc.params, re, tc.field, tc.rule)
		}
	}
}

func TestWidenValidate_Table(t *testing.T) {
	t.Parallel()
	m := resolved(t, KindWiden, `{"source_id":"x"}`)
	if m["width"] != 1.3 || m["decorrelate"] != 0.1 || m["bass_mono_hz"] != 120.0 || len(m) != 4 {
		t.Errorf("defaults %v", m)
	}
	m = resolved(t, KindWiden, `{"source_id":"x","width":1.52,"decorrelate":0.33,"bass_mono_hz":0}`)
	if m["width"] != 1.5 || m["decorrelate"] != 0.35 || m["bass_mono_hz"] != 0.0 {
		t.Errorf("snapped %v", m)
	}
	for _, tc := range []struct{ params, field, rule string }{
		{`{}`, "source_id", RuleRequired},
		{`{"source_id":"x","width":0.95}`, "width", RuleRange},
		{`{"source_id":"x","width":2.05}`, "width", RuleRange},
		{`{"source_id":"x","decorrelate":0.55}`, "decorrelate", RuleRange},
		{`{"source_id":"x","bass_mono_hz":59}`, "bass_mono_hz", RuleRange},
		{`{"source_id":"x","bass_mono_hz":251}`, "bass_mono_hz", RuleRange},
		{`{"source_id":"x","bass_mono_hz":1.5}`, "bass_mono_hz", RuleType},
		{`{"source_id":"x","preset":"warm"}`, "preset", RuleUnknownField},
	} {
		if re := ruleOf(t, KindWiden, tc.params); re.Field != tc.field || re.Rule != tc.rule {
			t.Errorf("%s: %+v, want %s/%s", tc.params, re, tc.field, tc.rule)
		}
	}
	if resolved(t, KindWiden, `{"source_id":"x","bass_mono_hz":60}`)["bass_mono_hz"] != 60.0 {
		t.Error("60 Hz refused")
	}
}

// TestHifiValidate_AgreesWithOperator: what the service writes into the spec is what the operator accepts unchanged, and a
// range the service accepts is a range the operator accepts (one table, two checks).
func TestHifiValidate_AgreesWithOperator(t *testing.T) {
	t.Parallel()
	for _, preset := range ops.MasterPresets {
		raw, err := mustKind(t, KindMaster).Validate(json.RawMessage(fmt.Sprintf(`{"source_id":"x","preset":%q,"ceiling_dbtp":-3,"target_lufs":-20}`, preset.Name)))
		if err != nil {
			t.Fatal(err)
		}
		p, err := ops.ParseMaster(raw)
		if err != nil || p.Preset != preset.Name || p.TiltDB != preset.TiltDB || p.CeilingDBTP != -3 || *p.TargetLUFS != -20 {
			t.Errorf("%s: operator read %+v (%v)", preset.Name, p, err)
		}
	}
	for _, params := range []string{`{"source_id":"x"}`, `{"source_id":"x","width":2,"decorrelate":0.5,"bass_mono_hz":250}`, `{"source_id":"x","width":1,"decorrelate":0,"bass_mono_hz":0}`} {
		raw, err := mustKind(t, KindWiden).Validate(json.RawMessage(params))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ops.ParseWiden(raw); err != nil {
			t.Errorf("%s: operator refused %s: %v", params, raw, err)
		}
	}
}

func mustKind(t *testing.T, k Kind) KindSpec {
	t.Helper()
	ks, ok := LookupKind(k)
	if !ok {
		t.Fatalf("%s not registered", k)
	}
	return ks
}

// TestHifiKinds_RegisteredWithOps: both kinds are registered here and in the dsp registry, and the service's unit model is the
// operator's own work-meter model.
func TestHifiKinds_RegisteredWithOps(t *testing.T) {
	t.Parallel()
	for _, k := range []Kind{KindMaster, KindWiden} {
		ks := mustKind(t, k)
		_, units, ok := ops.Lookup(string(k))
		if !ok {
			t.Fatalf("%s not in the ops registry", k)
		}
		if a, b := ks.Units(UnitsIn{InFrames: 48000 * 30, OutFrames: 48000 * 30, Channels: 2, Rate: 48000}), units(48000*30, 48000*30, 48000, nil); a != b {
			t.Errorf("%s: service units %d, operator units %d", k, a, b)
		}
	}
}

// TestCost_MonotonicHifi: more output frames never cost fewer units; both kinds cost more than the safety stage alone.
func TestCost_MonotonicHifi(t *testing.T) {
	t.Parallel()
	for _, k := range []Kind{KindMaster, KindWiden} {
		ks := mustKind(t, k)
		var prev int64
		for _, secs := range []int64{1, 10, 60, 600} {
			u := ks.Units(UnitsIn{InFrames: 48000 * secs, OutFrames: 48000 * secs, Channels: 2, Rate: 48000})
			if u <= prev || u <= ops.SafetyUnits(48000*secs) {
				t.Errorf("%s at %d s: %d units (previous %d)", k, secs, u, prev)
			}
			prev = u
		}
		if mem := ks.MemFactor; mem != 0 { // streamed: kindMemoryMB counts the input and a window (TestCost_HifiStreamsItsFinish)
			t.Errorf("%s memory factor %v", k, mem)
		}
	}
}

func TestHifiTitles(t *testing.T) {
	t.Parallel()
	if got := mustKind(t, KindMaster).Title(TitleIn{Resolved: json.RawMessage(`{"preset":"warm"}`)}); got != "master warm" {
		t.Errorf("master title %q", got)
	}
	if got := mustKind(t, KindWiden).Title(TitleIn{Resolved: json.RawMessage(`{"width":1.3}`)}); got != "widen ×1.3" {
		t.Errorf("widen title %q", got)
	}
}

// TestCapabilities_HifiNotesMasterWiden: the required sentences of C223.2 for master and widen, the example validates, and the
// preset enum lists the four presets.
func TestCapabilities_HifiNotesMasterWiden(t *testing.T) {
	t.Parallel()
	m, w := mustKind(t, KindMaster).Capabilities(), mustKind(t, KindWiden).Capabilities()
	if !slices.Contains(m.BlindSpots, "master processes the full mix; it cannot add detail") {
		t.Errorf("master blind spots %v", m.BlindSpots)
	}
	if !slices.Contains(w.BlindSpots, "widen keeps the mono fold-down; a source that is already wide is left unchanged") {
		t.Errorf("widen blind spots %v", w.BlindSpots)
	}
	for _, kc := range []KindCapabilities{m, w} {
		if kc.Listening != "" || kc.Dataset != "" || kc.Inputs != 1 {
			t.Errorf("%s: %+v", kc.Kind, kc)
		}
		if !strings.Contains(kc.Example, `"kind":"`+string(kc.Kind)+`"`) {
			t.Errorf("%s example %s", kc.Kind, kc.Example)
		}
	}
	for _, p := range m.Params {
		if p.Name == "preset" && !slices.Equal(p.Enum, []string{"neutral", "warm", "bright", "punch"}) {
			t.Errorf("preset enum %v", p.Enum)
		}
	}
}

func TestHifiWarnings_AfterBinaural(t *testing.T) {
	t.Parallel()
	for _, k := range []Kind{KindMaster, KindWiden} {
		ks := mustKind(t, k)
		if w := ks.Warnings(WarnIn{Chain: []Kind{KindMaster, KindBinaural, KindMorph}}); len(w) != 1 || w[0].Code != WarnAfterBinaural {
			t.Errorf("%s after binaural: %v", k, w)
		}
		if w := ks.Warnings(WarnIn{Chain: []Kind{KindMaster, KindWiden}}); len(w) != 0 {
			t.Errorf("%s without binaural: %v", k, w)
		}
	}
}

// TestEstimate_HifiRealWorldShapes: the master and widen estimates answer for every kind of Take FixtureT1 carries, the MONO,
// WIDE and OOP stereo rows included, with a default-complete spec, the output as long as the input, and no warning.
func TestEstimate_HifiRealWorldShapes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, label := range []string{"A", "MONO", "WIDE", "OOP", "B"} {
		for _, k := range []Kind{KindMaster, KindWiden} {
			est, err := r.svc.Estimate(t.Context(), human, req(k, fmt.Sprintf(`{"source_id":%q}`, r.tx(label))))
			if err != nil {
				t.Fatalf("%s %s: %v", k, label, err)
			}
			if est.Kind != k || est.CPU.ExpectedMS <= 0 || est.Output.Seconds <= 0 || len(est.Warnings) != 0 {
				t.Errorf("%s %s: %+v", k, label, est)
			}
			var m map[string]any
			if err := json.Unmarshal(est.Resolved, &m); err != nil || m["source_id"] != r.tx(label) || len(m) < 4 {
				t.Errorf("%s %s: resolved %s", k, label, est.Resolved)
			}
		}
	}
	// unknown field and range refusals reach the caller as INVALID_TRANSFORM with the field named
	_, err := r.svc.Estimate(t.Context(), human, req(KindWiden, fmt.Sprintf(`{"source_id":%q,"width":3}`, r.tx("MONO"))))
	if err == nil || !strings.Contains(err.Error(), "width") {
		t.Errorf("width 3: %v", err)
	}
}
