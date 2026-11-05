package transform

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

const (
	idA = "id-a"
	idB = "id-b"
)

type vrow struct {
	name   string
	kind   Kind
	params string
	field  string // "" = accepted
	rule   string
}

func validateRows() []vrow {
	pair := func(extra string) string {
		return fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q%s}`, idA, idB, extra)
	}
	src := func(extra string) string { return fmt.Sprintf(`{"source_id":%q%s}`, idA, extra) }
	rows := []vrow{
		// shape
		{"morph minimal", KindMorph, pair(""), "", ""},
		{"morph same_input", KindMorph, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q}`, idA, idA), "track_b_id", RuleSameInput},
		{"polytempo same_input", KindPolytempo, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q}`, idA, idA), "track_b_id", RuleSameInput},
		{"morph missing a", KindMorph, `{"track_b_id":"x"}`, "track_a_id", RuleRequired},
		{"glacier missing source", KindGlacier, `{}`, "source_id", RuleRequired},
		{"glacier null params", KindGlacier, `null`, "source_id", RuleRequired},
		{"glacier unknown field", KindGlacier, src(`,"stereo":"wide"`), "stereo", RuleUnknownField},
		{"glacier path field", KindGlacier, src(`,"path":"/etc/x"`), "path", RuleUnknownField},
		{"glacier cut lowpass_hz", KindGlacier, src(`,"lowpass_hz":4000`), "lowpass_hz", RuleUnknownField},
		{"morph cut key_tail", KindMorph, pair(`,"key_tail":2`), "key_tail", RuleUnknownField},
		{"morph cut curve", KindMorph, pair(`,"curve":"x"`), "curve", RuleUnknownField},
		{"morph cut b_tail_bars", KindMorph, pair(`,"b_tail_bars":2`), "b_tail_bars", RuleUnknownField},
		{"polytempo cut mode", KindPolytempo, pair(`,"mode":"coincide"`), "mode", RuleUnknownField},
		{"ensemble cut custom space", KindEnsemble, src(`,"space":"custom"`), "space", RuleRange},
		{"ensemble voices not a parameter", KindEnsemble, src(`,"voices":3`), "voices", RuleUnknownField},
		{"glacier wrong type", KindGlacier, src(`,"factor":"20"`), "factor", RuleType},
		{"glacier not an object", KindGlacier, `[1]`, "params", RuleType},
		{"shepard layers fraction", KindShepard, src(`,"layers":4.5,"loop_s":4`), "layers", RuleType},
		{"morph a_at both", KindMorph, pair(`,"a_at":{"bar":1,"section":"chorus#1"}`), "a_at", RuleRequired},
		{"morph a_at neither", KindMorph, pair(`,"a_at":{}`), "a_at", RuleRequired},
		{"morph a_at bar", KindMorph, pair(`,"a_at":{"bar":3}`), "", ""},
		{"morph a_at negative bar", KindMorph, pair(`,"a_at":{"bar":-1}`), "a_at.bar", RuleRange},
		{"morph a_at section", KindMorph, pair(`,"b_at":{"section":"chorus#1"}`), "", ""},
		{"morph a_at bad section", KindMorph, pair(`,"a_at":{"section":"Chorus 1"}`), "a_at.section", RuleRange},
		{"morph a_at unknown member", KindMorph, pair(`,"a_at":{"second":3}`), "a_at.second", RuleUnknownField},
		{"morph downbeat auto", KindMorph, pair(`,"downbeat_a_s":"auto","downbeat_b_s":"auto"`), "", ""},
		{"morph downbeat other string", KindMorph, pair(`,"downbeat_a_s":"later"`), "downbeat_a_s", RuleType},
		{"morph downbeat negative", KindMorph, pair(`,"downbeat_a_s":-0.1`), "downbeat_a_s", RuleRange},
		{"morph key_policy", KindMorph, pair(`,"key_policy":"to_b"`), "key_policy", RuleRange},
		{"shepard direction", KindShepard, src(`,"direction":"sideways","loop_s":4`), "direction", RuleRange},
		{"shepard both loop sources", KindShepard, src(`,"loop_bars":4,"loop_s":4`), "loop_s", RuleLoopSourceRequired},
		{"shepard duration below period", KindShepard, src(`,"loop_s":4,"period_s":60,"duration_s":50`), "duration_s", RuleRange},
		{"shepard duration equals period", KindShepard, src(`,"loop_s":4,"period_s":60,"duration_s":60`), "", ""},
		{"polytempo pan", KindPolytempo, pair(`,"pan":"left"`), "pan", RuleRange},
		{"ensemble dry null is minus infinity", KindEnsemble, src(`,"dry_db":null`), "", ""},
	}
	// each range of C202.3: at min, at max and just outside
	type rng struct {
		kind     Kind
		base     string // valid params with %s for the tested member
		name     string
		min, max float64
		lo, hi   bool // the side is bounded
		integer  bool
	}
	ranges := []rng{
		{KindMorph, pair(`,%s`), "bars", 1, 32, true, true, true},
		{KindMorph, pair(`,%s`), "bpm_a", 40, 240, true, true, false},
		{KindMorph, pair(`,%s`), "bpm_b", 40, 240, true, true, false},
		{KindMorph, pair(`,%s`), "beats_per_bar", 2, 7, true, true, true},
		{KindMorph, pair(`,%s`), "semitones_b", -12, 12, true, true, false},
		{KindShepard, src(`,"loop_s":4,%s`), "layers", 3, 7, true, true, true},
		{KindShepard, src(`,"loop_s":4,%s`), "period_s", 8, 120, true, true, false},
		{KindShepard, src(`,"loop_s":4,"period_s":8,%s`), "duration_s", 8, 600, true, true, false},
		{KindShepard, src(`,"loop_s":4,%s`), "bell_sigma_octaves", 0.4, 2, true, true, false},
		{KindShepard, src(`,%s`), "loop_bars", 1, 8, true, true, true},
		{KindShepard, src(`,%s`), "loop_s", 1, 20, true, true, false},
		{KindShepard, src(`,"loop_s":4,%s`), "start_s", 0, 0, true, false, false},
		{KindGlacier, src(`,%s`), "factor", 10, 50, true, true, false},
		{KindGlacier, src(`,%s`), "window_s", 0.1, 2, true, true, false},
		{KindGlacier, src(`,%s`), "fade_in_s", 0, 30, true, true, false},
		{KindGlacier, src(`,%s`), "fade_out_s", 0, 30, true, true, false},
		{KindPolytempo, pair(`,%s`), "converge_bar", 4, 4, true, false, true},
		{KindPolytempo, pair(`,%s`), "unison_bars", 0, 64, true, true, true},
		{KindEnsemble, src(`,%s`), "wet_db", -40, 0, true, true, false},
		{KindEnsemble, src(`,%s`), "dry_db", -40, 0, true, true, false},
		{KindEnsemble, src(`,%s`), "tail_s", 0, 40, true, true, false},
	}
	for _, g := range ranges {
		step := 1.0
		if !g.integer {
			step = 0.5
		}
		add := func(label string, v float64, ok bool) {
			member := fmt.Sprintf(`"%s":%v`, g.name, v)
			row := vrow{name: g.name + " " + label, kind: g.kind, params: fmt.Sprintf(g.base, member)}
			if !ok {
				row.field, row.rule = g.name, RuleRange
			}
			rows = append(rows, row)
		}
		add("at min", g.min, true)
		if g.lo {
			add("below min", g.min-step, false)
		}
		if g.hi {
			add("at max", g.max, true)
			add("above max", g.max+step, false)
		}
	}
	return rows
}

// TestValidate_Table: every range of C202.3 at min, max and just outside, kind-specific rules, unknown fields (cut parameters
// among them) named by field, and the stable rule of each refusal.
func TestValidate_Table(t *testing.T) {
	t.Parallel()
	rows := validateRows()
	if len(rows) < 40 {
		t.Fatalf("only %d rows", len(rows))
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			ks, ok := LookupKind(r.kind)
			if !ok {
				t.Fatalf("%s is not registered", r.kind)
			}
			got, err := ks.Validate(json.RawMessage(r.params))
			if r.field == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if !json.Valid(got) {
					t.Fatalf("resolved params are not JSON: %s", got)
				}
				return
			}
			var re *RuleError
			if !errors.As(err, &re) {
				t.Fatalf("got %v (%s), want %s %s", err, got, r.field, r.rule)
			}
			if re.Field != r.field || re.Rule != r.rule {
				t.Fatalf("got %s %s, want %s %s", re.Field, re.Rule, r.field, r.rule)
			}
		})
	}
}

// TestValidate_DefaultsAndDerived: the static defaults are written out and the input-dependent ones are left for resolution.
func TestValidate_DefaultsAndDerived(t *testing.T) {
	t.Parallel()
	ks, _ := LookupKind(KindMorph)
	got, err := ks.Validate(json.RawMessage(fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q}`, idA, idB)))
	if err != nil {
		t.Fatal(err)
	}
	m := paramsMap(got)
	for name, want := range map[string]string{"bars": "8", "beats_per_bar": "4", "key_policy": `"to_a"`} {
		if string(m[name]) != want {
			t.Errorf("%s = %s, want %s", name, m[name], want)
		}
	}
	for _, name := range []string{"bpm_a", "bpm_b", "a_at", "b_at", "downbeat_a_s", "semitones_b"} {
		if _, ok := m[name]; ok {
			t.Errorf("%s was filled before resolution", name)
		}
	}
	// ensemble dry_db: absent keeps -12, null keeps null
	ks, _ = LookupKind(KindEnsemble)
	got, _ = ks.Validate(json.RawMessage(`{"source_id":"x"}`))
	if string(paramsMap(got)["dry_db"]) != "-12" {
		t.Errorf("dry_db default %s", got)
	}
	got, _ = ks.Validate(json.RawMessage(`{"source_id":"x","dry_db":null}`))
	if string(paramsMap(got)["dry_db"]) != "null" {
		t.Errorf("dry_db null %s", got)
	}
	// bell sigma follows the layers
	ks, _ = LookupKind(KindShepard)
	got, _ = ks.Validate(json.RawMessage(`{"source_id":"x","layers":7,"loop_s":4}`))
	if string(paramsMap(got)["bell_sigma_octaves"]) != "1.4" {
		t.Errorf("bell sigma %s", got)
	}
}

// TestCreate_PathNeverAccepted: no parameter takes a path; a path-like id is only ever a track id that does not exist, and
// path-shaped fields are unknown fields.
func TestCreate_PathNeverAccepted(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, id := range []string{"/etc/passwd", "../../takes/x/master.flac", "file:///x"} {
		_, err := r.svc.Create(t.Context(), human, glacier(id), nil)
		wantProblem(t, err, 404, "NOT_FOUND")
	}
	for _, f := range []string{"path", "file", "abc_path", "soundfont_path", "master_path"} {
		_, err := r.svc.Create(t.Context(), human, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,%q:"/x"}`, r.tx("A"), f)), nil)
		wantRule(t, err, f, RuleUnknownField)
	}
	if n := r.count(`SELECT COUNT(*) FROM transforms`); n != 0 {
		t.Errorf("%d transforms were created", n)
	}
}
