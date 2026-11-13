package transform

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/hrtf"
	"github.com/nicolasacchi/metamusic/internal/dsp/ir"
	"github.com/nicolasacchi/metamusic/internal/dsp/ops"
)

// TestBinauralValidate_Table: every default is written out from the preset, the hrtf model records the table's snapped values,
// the parametric model keeps what was sent, and the refusals name the field and the rule.
func TestBinauralValidate_Table(t *testing.T) {
	t.Parallel()
	m := resolved(t, KindBinaural, `{"source_id":"x"}`)
	want := map[string]any{"source_id": "x", "model": "hrtf", "preset": "studio", "left_az_deg": -30.0, "right_az_deg": 30.0,
		"elevation_deg": 0.0, "room": "studio", "room_mix_db": -24.0, "tail_s": 1.0}
	if len(m) != len(want) {
		t.Errorf("defaults %v", m)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("default %s = %v, want %v", k, m[k], v)
		}
	}
	for _, c := range []struct {
		params      string
		left, right float64
		el          float64
	}{
		{`{"source_id":"x","preset":"wide"}`, -45, 45, 0},
		{`{"source_id":"x","preset":"near"}`, -20, 20, 0}, // 22 degrees snapped to the 5 degree table
		{`{"source_id":"x","left_az_deg":-31,"right_az_deg":33,"elevation_deg":8}`, -30, 35, 15},
		{`{"source_id":"x","left_az_deg":-12,"right_az_deg":88,"elevation_deg":7}`, -10, 90, 0},
		{`{"source_id":"x","model":"parametric","preset":"near"}`, -22, 22, 0},
		{`{"source_id":"x","model":"parametric","left_az_deg":-31,"right_az_deg":33,"elevation_deg":8}`, -31, 33, 8},
	} {
		m := resolved(t, KindBinaural, c.params)
		if m["left_az_deg"] != c.left || m["right_az_deg"] != c.right || m["elevation_deg"] != c.el {
			t.Errorf("%s resolved to %v", c.params, m)
		}
	}
	m = resolved(t, KindBinaural, `{"source_id":"x","tail_s":2.504,"room":"hall","room_mix_db":-30}`)
	if m["tail_s"] != 2.5 || m["room"] != "hall" || m["room_mix_db"] != -30.0 {
		t.Errorf("overrides %v", m)
	}
	for _, c := range []struct{ params, field, rule string }{
		{`{}`, "source_id", RuleRequired},
		{`{"source_id":"x","model":"x"}`, "model", RuleRange},
		{`{"source_id":"x","preset":"x"}`, "preset", RuleRange},
		{`{"source_id":"x","left_az_deg":-9}`, "left_az_deg", RuleRange},
		{`{"source_id":"x","left_az_deg":-91}`, "left_az_deg", RuleRange},
		{`{"source_id":"x","left_az_deg":-30.5}`, "left_az_deg", RuleType},
		{`{"source_id":"x","right_az_deg":9}`, "right_az_deg", RuleRange},
		{`{"source_id":"x","right_az_deg":91}`, "right_az_deg", RuleRange},
		{`{"source_id":"x","elevation_deg":31}`, "elevation_deg", RuleRange},
		{`{"source_id":"x","room":"cave"}`, "room", RuleRange},
		{`{"source_id":"x","room_mix_db":-11}`, "room_mix_db", RuleRange},
		{`{"source_id":"x","room_mix_db":-41}`, "room_mix_db", RuleRange},
		{`{"source_id":"x","tail_s":10.1}`, "tail_s", RuleRange},
		{`{"source_id":"x","tail_s":-1}`, "tail_s", RuleRange},
		{`{"source_id":"x","gain":1}`, "gain", RuleUnknownField},
	} {
		if re := ruleOf(t, KindBinaural, c.params); re.Field != c.field || re.Rule != c.rule {
			t.Errorf("%s: %s/%s, want %s/%s", c.params, re.Field, re.Rule, c.field, c.rule)
		}
	}
}

// TestBinauralValidate_AgreesWithOperator: what the service writes into the spec is what the operator reads back unchanged, the
// room enum is the generator's room list, and the ranges are the operator's.
func TestBinauralValidate_AgreesWithOperator(t *testing.T) {
	t.Parallel()
	var rooms []string
	for _, p := range binauralParams {
		if p.Name == "room" {
			rooms = p.Enum
		}
	}
	if !slices.Equal(rooms, append([]string{"none"}, ir.RoomNames()...)) {
		t.Errorf("room enum %v, generator rooms %v", rooms, ir.RoomNames())
	}
	for _, params := range []string{
		`{"source_id":"x"}`, `{"source_id":"x","preset":"near"}`, `{"source_id":"x","model":"parametric","elevation_deg":15,"room":"hall","tail_s":10}`,
		`{"source_id":"x","left_az_deg":-90,"right_az_deg":90,"elevation_deg":30,"room":"none","room_mix_db":-40}`,
		`{"source_id":"x","left_az_deg":-10,"right_az_deg":10,"room_mix_db":-12,"tail_s":0}`,
	} {
		raw, err := mustKind(t, KindBinaural).Validate(json.RawMessage(params))
		if err != nil {
			t.Fatal(err)
		}
		var spec map[string]any
		_ = json.Unmarshal(raw, &spec)
		p, err := ops.ParseBinaural(raw)
		if err != nil {
			t.Fatalf("%s: operator refused %s: %v", params, raw, err)
		}
		if float64(p.LeftAz) != spec["left_az_deg"] || float64(p.RightAz) != spec["right_az_deg"] || p.Room != spec["room"] || p.Model != spec["model"] {
			t.Errorf("%s: operator read %+v from %s", params, p, raw)
		}
		if spec["model"] == ops.ModelHRTF && (p.Elevation != int(spec["elevation_deg"].(float64)) || p.LeftAz != hrtf.SnapAz(p.LeftAz)) {
			t.Errorf("%s: the spec is not on the table: %s", params, raw)
		}
	}
}

func TestBinauralKind_RegisteredWithOps(t *testing.T) {
	t.Parallel()
	ks := mustKind(t, KindBinaural)
	_, units, ok := ops.Lookup(string(KindBinaural))
	if !ok {
		t.Fatal("binaural is not in the ops registry")
	}
	raw := json.RawMessage(`{"source_id":"x","model":"hrtf","preset":"studio","left_az_deg":-30,"right_az_deg":30,"elevation_deg":0,"room":"hall","room_mix_db":-24,"tail_s":2}`)
	if a, b := ks.Units(UnitsIn{InFrames: 44100 * 30, OutFrames: 44100 * 32, Channels: 2, Rate: 44100, Resolved: raw}), units(44100*30, 44100*32, 44100, raw); a != b {
		t.Errorf("service units %d, operator units %d", a, b)
	}
	if ks.MemFactor != 0 || ks.Warnings == nil {
		t.Errorf("registration %+v", ks)
	}
}

// TestCost_MonotonicBinaural: more input never costs fewer units, a bigger room costs more than none, and the answer is finite
// for a spec the validator would never write.
func TestCost_MonotonicBinaural(t *testing.T) {
	t.Parallel()
	ks := mustKind(t, KindBinaural)
	for _, room := range []string{"none", "studio", "hall"} {
		raw := json.RawMessage(fmt.Sprintf(`{"model":"hrtf","left_az_deg":-30,"right_az_deg":30,"room":%q,"tail_s":1}`, room))
		var prev int64
		for _, secs := range []int64{1, 10, 60, 600} {
			u := ks.Units(UnitsIn{InFrames: 48000 * secs, OutFrames: 48000 * secs, Channels: 2, Rate: 48000, Resolved: raw})
			if u <= prev || u <= ops.SafetyUnits(48000*secs) {
				t.Errorf("%s at %d s: %d units (previous %d)", room, secs, u, prev)
			}
			prev = u
		}
	}
	cost := func(room string) int64 {
		return ks.Units(UnitsIn{InFrames: 48000 * 60, Channels: 2, Rate: 48000, Resolved: json.RawMessage(fmt.Sprintf(`{"room":%q}`, room))})
	}
	if !(cost("none") < cost("studio") && cost("studio") < cost("hall")) {
		t.Errorf("room costs none %d studio %d hall %d", cost("none"), cost("studio"), cost("hall"))
	}
	if u := ks.Units(UnitsIn{InFrames: 48000, Channels: 2, Rate: 48000, Resolved: json.RawMessage(`{"model":"bogus"}`)}); u <= 0 {
		t.Errorf("units for a bad spec: %d", u)
	}
}

func TestBinauralTitle(t *testing.T) {
	t.Parallel()
	ks := mustKind(t, KindBinaural)
	if got := ks.Title(TitleIn{Resolved: json.RawMessage(`{"preset":"wide","model":"hrtf"}`)}); got != "binaural wide" {
		t.Errorf("title %q", got)
	}
	if got := ks.Title(TitleIn{Resolved: json.RawMessage(`{"preset":"near","model":"parametric"}`)}); got != "binaural near (parametric)" {
		t.Errorf("title %q", got)
	}
	if got := ks.Title(TitleIn{Resolved: json.RawMessage(`{}`)}); got != "binaural" {
		t.Errorf("title %q", got)
	}
}

// TestCapabilities_BinauralNotes: the required sentence of C223.2, the headphones label, the dataset credit and an example
// that names the kind; the parameter table lists the three presets, both models and the rooms.
func TestCapabilities_BinauralNotes(t *testing.T) {
	t.Parallel()
	kc := mustKind(t, KindBinaural).Capabilities()
	const sentence = "binaural is for headphones only; it places the stereo pair in front of the head, not individual instruments; a binaural Take is never processed twice"
	if !slices.Contains(kc.BlindSpots, sentence) {
		t.Errorf("blind spots %v", kc.BlindSpots)
	}
	if kc.Listening != "headphones" || !strings.Contains(kc.Dataset, "SADIE II") || !strings.Contains(kc.Dataset, "Apache-2.0") || kc.Inputs != 1 {
		t.Errorf("%+v", kc)
	}
	if !strings.Contains(kc.Example, `"kind":"binaural"`) {
		t.Errorf("example %s", kc.Example)
	}
	enums := map[string][]string{}
	for _, p := range kc.Params {
		enums[p.Name] = p.Enum
	}
	if !slices.Equal(enums["preset"], []string{"studio", "wide", "near"}) || !slices.Equal(enums["model"], []string{"hrtf", "parametric"}) {
		t.Errorf("enums %v", enums)
	}
}

func TestBinauralWarnings_EstimateTime(t *testing.T) {
	t.Parallel()
	ks := mustKind(t, KindBinaural)
	codesOf := func(raw string) []string {
		var out []string
		for _, w := range ks.Warnings(WarnIn{Resolved: json.RawMessage(raw)}) {
			out = append(out, w.Code)
		}
		return out
	}
	if got := codesOf(`{"model":"hrtf","elevation_deg":15}`); !slices.Equal(got, []string{WarnBinauralHeadphonesOnly, WarnBinauralMonoIncompatible}) {
		t.Errorf("hrtf: %v", got)
	}
	if got := codesOf(`{"model":"parametric","elevation_deg":0}`); !slices.Equal(got, []string{WarnBinauralHeadphonesOnly, WarnBinauralMonoIncompatible, WarnBinauralParametricBasic}) {
		t.Errorf("parametric: %v", got)
	}
	if got := codesOf(`{"model":"parametric","elevation_deg":15}`); !slices.Equal(got, []string{WarnBinauralHeadphonesOnly, WarnBinauralMonoIncompatible, WarnBinauralParametricBasic, WarnElevationIgnored}) {
		t.Errorf("parametric with elevation: %v", got)
	}
}

// TestEstimate_Binaural: the estimate of a binaural copy answers for ordinary Takes, adds the room tail to the output length (at
// most tail_s), carries the always-present warnings, and the hrtf block is in the capabilities.
func TestEstimate_Binaural(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, label := range []string{"A", "MONO", "WIDE", "OOP", "B"} {
		est, err := r.svc.Estimate(t.Context(), human, req(KindBinaural, fmt.Sprintf(`{"source_id":%q}`, r.tx(label))))
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		got := codes(est.Warnings)
		if est.Kind != KindBinaural || est.CPU.ExpectedMS <= 0 || !slices.Contains(got, WarnBinauralHeadphonesOnly) || !slices.Contains(got, WarnBinauralMonoIncompatible) || slices.Contains(got, WarnBinauralParametricBasic) {
			t.Errorf("%s: %+v", label, est)
		}
	}
	a := r.mustBuild(req(KindBinaural, fmt.Sprintf(`{"source_id":%q,"room":"none"}`, r.tx("A"))))
	b := r.mustBuild(req(KindBinaural, fmt.Sprintf(`{"source_id":%q,"room":"studio","tail_s":3}`, r.tx("A"))))
	if !(b.OutFrames > a.OutFrames) || b.OutFrames-a.OutFrames > 3*int64(b.Spec.SampleRate)+1 {
		t.Errorf("output frames none %d, studio %d", a.OutFrames, b.OutFrames)
	}
	est, err := r.svc.Estimate(t.Context(), human, req(KindBinaural, fmt.Sprintf(`{"source_id":%q,"model":"parametric","elevation_deg":15}`, r.tx("A"))))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{WarnBinauralParametricBasic, WarnElevationIgnored} {
		if !slices.Contains(codes(est.Warnings), c) {
			t.Errorf("parametric estimate lacks %s: %v", c, codes(est.Warnings))
		}
	}
	_, err = r.svc.Estimate(t.Context(), human, req(KindBinaural, fmt.Sprintf(`{"source_id":%q,"right_az_deg":5}`, r.tx("A"))))
	if err == nil || !strings.Contains(err.Error(), "right_az_deg") {
		t.Errorf("right_az_deg 5: %v", err)
	}
	caps, err := r.svc.Capabilities(t.Context(), human)
	if err != nil {
		t.Fatal(err)
	}
	if bn, ok := kindCaps(caps, KindBinaural); !ok || bn.Listening != "headphones" || !strings.Contains(bn.Dataset, "SADIE II") {
		t.Errorf("capabilities %+v", bn)
	}
}
