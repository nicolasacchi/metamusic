package transform

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/features"
)

// build runs the create-order checks 2 to 5 over the reader pool and returns the plan.
func (r *rig) build(q Request) (*plan, error) {
	r.t.Helper()
	return r.svc.build(r.t.Context(), r.st.R, human, q)
}

func (r *rig) mustBuild(q Request) *plan {
	r.t.Helper()
	pl, err := r.build(q)
	if err != nil {
		r.t.Fatalf("build: %v", err)
	}
	return pl
}

func codes(ws []Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return out
}

// unfrozen runs fn with the frozen-column trigger of tracks dropped, the way no production path can. FixtureT1 takes all carry a
// "N bpm" style and are lossless known takes, so a take with no declared tempo, a lossy master or a long duration (shapes real
// libraries have) has to be made by hand.
func (r *rig) unfrozen(fn func()) {
	r.t.Helper()
	names := []string{"tracks_frozen", "tracks_lossy_frozen"}
	ddl := make([]string, len(names))
	for i, n := range names {
		if err := r.st.R.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, n).Scan(&ddl[i]); err != nil {
			r.t.Fatal(err)
		}
		r.exec(`DROP TRIGGER ` + n)
	}
	fn()
	for _, d := range ddl {
		r.exec(d)
	}
}

// clearDeclared removes the declared tempo of takes.
func (r *rig) clearDeclared(labels ...string) {
	r.t.Helper()
	r.unfrozen(func() {
		for _, l := range labels {
			r.exec(`UPDATE tracks SET bpm_declared = NULL WHERE id = ?`, r.tx(l))
		}
	})
}

func (r *rig) insertFeature(label, bpmX100 string, extra string) {
	r.t.Helper()
	r.exec(fmt.Sprintf(`INSERT INTO take_features(track_id,%s,digest,feature_def,computed_at) VALUES (?,%s,?,'mm-features/1','2026-10-06T00:00:00.000Z')`, bpmX100, extra),
		r.tx(label), librarytestSha())
}

func librarytestSha() string {
	return "0000000000000000000000000000000000000000000000000000000000000000"
}

func (r *rig) tempoAnalysis(label string, bpm, conf float64, octave bool) {
	r.t.Helper()
	r.exec(`INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'tempo','mm-tempo/1 test','A',?,'2026-10-06T00:00:00.000Z')`,
		r.tx(label), fmt.Sprintf(`{"bpm":%v,"confidence":%v,"octave_ambiguous":%t,"analyzed_s":12}`, bpm, conf, octave))
}

func (r *rig) section(label, source string, idx int, kind string, start float64) {
	r.t.Helper()
	r.exec(`INSERT INTO sections(track_id,source,idx,kind,label,raw_label,start_s,end_s) VALUES(?,?,?,?,?,?,?,?)`,
		r.tx(label), source, idx, kind, kind+" 1", "["+kind+"]", start, start+4)
}

func align(pl *plan, i int) InputAlign { return pl.Spec.Alignment.Inputs[i] }

// TestResolve_Ladder: tempo is explicit > plan > declared > measured with the source recorded, a measured octave-ambiguous tempo
// folds toward the partner and nothing else does, a confidence below 0.30 warns but is accepted (D-T1-9 revision 3: the analyzer
// has its own gate, so no second threshold rejects a value), nothing at all is ALIGNMENT_UNRESOLVED naming bpm_a, and an unknown
// key means shift 0 and a warning.
func TestResolve_Ladder(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pair := func(a, b, extra string) Request {
		return req(KindMorph, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q,"bars":2,"downbeat_a_s":0.4,"downbeat_b_s":0.2%s}`, r.tx(a), r.tx(b), extra))
	}
	// declared: A through its feature row (requested tempo), B with no feature row through tracks.bpm_declared (the style's "128 bpm")
	pl := r.mustBuild(pair("A", "B", ""))
	if a := align(pl, 0); a.BPM != 92 || a.BPMSource != SrcDeclared {
		t.Errorf("A = %+v", a)
	}
	if b := align(pl, 1); b.BPM != 128 || b.BPMSource != SrcDeclared || b.OctaveFold != 0 {
		t.Errorf("B = %+v", b)
	}
	// measured: with no declared tempo anywhere, the analyses row is read directly (B has no feature row)
	r.clearDeclared("B", "MONO", "WIDE", "OOP", "I")
	pl = r.mustBuild(pair("A", "B", ""))
	if b := align(pl, 1); b.BPM != 128 || b.BPMSource != SrcMeasured || b.OctaveFold != 0 || pl.Res.Inputs[1].Confidence == nil {
		t.Errorf("B measured = %+v", b)
	}
	// explicit wins over everything, and is never folded
	pl = r.mustBuild(pair("A", "B", `,"bpm_a":100,"bpm_b":64`))
	if a, b := align(pl, 0), align(pl, 1); a.BPM != 100 || a.BPMSource != SrcExplicit || b.BPM != 64 || b.BPMSource != SrcExplicit || b.OctaveFold != 0 {
		t.Errorf("explicit = %+v %+v", a, b)
	}
	// plan: S has an aligned score (Q 120)
	pl = r.mustBuild(req(KindMorph, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q,"bars":2,"downbeat_a_s":0.4}`, r.tx("A"), r.tx("S"))))
	if b := align(pl, 1); b.BPM != 120 || b.BPMSource != SrcPlan || b.PlanBarS == nil || *b.PlanBarS != 2 {
		t.Errorf("S = %+v", b)
	}
	// a declared tempo is trusted even when the feature row says the measured one is an octave off (OCT: requested 128)
	pl = r.mustBuild(pair("A", "OCT", ""))
	if b := align(pl, 1); b.BPM != 128 || b.BPMSource != SrcDeclared || b.OctaveFold != 0 {
		t.Errorf("OCT = %+v", b)
	}
	// measured and octave-ambiguous: 64 folds toward A's 92 (Fold(92, 64) = +1 -> 128)
	r.insertFeature("MONO", "bpm_x100,bpm_basis,bpm_measured_x100,bpm_octave", "6400,'measured',6400,1")
	pl = r.mustBuild(pair("A", "MONO", ""))
	if b := align(pl, 1); b.BPM != 128 || b.BPMSource != SrcMeasured || b.OctaveFold != 1 {
		t.Errorf("MONO = %+v", b)
	}
	if !slices.Contains(codes(pl.Warnings), WarnTempoOctaveFolded) {
		t.Errorf("warnings %v lack tempo_octave_folded", codes(pl.Warnings))
	}
	// the same take as A folds toward B instead (A is the one folded when it is the ambiguous one)
	pl = r.mustBuild(pair("MONO", "B", ""))
	if a := align(pl, 0); a.BPM != 128 || a.OctaveFold != 1 {
		t.Errorf("MONO as A = %+v", a)
	}
	// confidence: 0.29 warns, 0.30 does not, both are accepted
	r.tempoAnalysis("WIDE", 100, 0.29, false)
	r.tempoAnalysis("OOP", 100, 0.30, false)
	for _, c := range []struct {
		label string
		warn  bool
	}{{"WIDE", true}, {"OOP", false}} {
		pl = r.mustBuild(pair("A", c.label, ""))
		if got := slices.Contains(codes(pl.Warnings), WarnTempoMeasuredLowConfidence); got != c.warn {
			t.Errorf("%s: low-confidence warning = %v, want %v", c.label, got, c.warn)
		}
		if b := align(pl, 1); b.BPM != 100 || b.BPMSource != SrcMeasured {
			t.Errorf("%s = %+v", c.label, b)
		}
	}
	// nothing anywhere: the instrumental take has no tempo
	_, err := r.build(pair("I", "B", ""))
	pe := wantProblem(t, err, 422, ErrAlignmentUnresolved)
	if pe.Details["quantity"] != "bpm" || pe.Details["pass"] != "bpm_a" || pe.Details["input"] != "a" {
		t.Errorf("details %v", pe.Details)
	}
	_, err = r.build(pair("A", "I", ""))
	if pe = wantProblem(t, err, 422, ErrAlignmentUnresolved); pe.Details["pass"] != "bpm_b" {
		t.Errorf("details %v", pe.Details)
	}
	// ... and an explicit tempo resolves it
	r.mustBuild(pair("I", "B", `,"bpm_a":70`))
}

// TestResolve_Key: the key ladder reads take_features.key_num/key_letter with its basis; unknown means shift 0 and a warning.
func TestResolve_Key(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	q := func(extra string) Request { return r.morphAB(extra) }
	pl := r.mustBuild(q(""))
	if !slices.Contains(codes(pl.Warnings), WarnKeyUnknownAssumedNone) {
		t.Errorf("warnings %v", codes(pl.Warnings))
	}
	if string(paramsMap(pl.Res.Params)["semitones_b"]) != "0" {
		t.Errorf("semitones_b = %s", paramsMap(pl.Res.Params)["semitones_b"])
	}
	// A is 8A (A minor) measured, B is 10B (D major) from a row with no tempo: shift of B into A's key = -2
	r.exec(`UPDATE take_features SET key_num = 8, key_letter = 'A', key_basis = 'measured' WHERE track_id = ?`, r.tx("A"))
	r.insertFeature("B", "key_num,key_letter,key_basis", "10,'B','declared'")
	pl = r.mustBuild(q(""))
	if a, b := align(pl, 0), align(pl, 1); a.KeyPC != 9 || !a.KeyMinor || a.KeySource != "measured" || b.KeyPC != 2 || b.KeyMinor || b.KeySource != "declared" {
		t.Errorf("keys %+v %+v", a, b)
	}
	if string(paramsMap(pl.Res.Params)["semitones_b"]) != "-2" {
		t.Errorf("semitones_b = %s", paramsMap(pl.Res.Params)["semitones_b"])
	}
	if slices.Contains(codes(pl.Warnings), WarnKeyUnknownAssumedNone) {
		t.Error("a known key warned")
	}
	// B's feature row names no tempo, so the declared one on the Take is used
	if b := align(pl, 1); b.BPM != 128 || b.BPMSource != SrcDeclared {
		t.Errorf("B tempo %+v", b)
	}
	// explicit semitones_b skips the ladder; key_policy none too
	pl = r.mustBuild(q(`,"semitones_b":3.5`))
	if string(paramsMap(pl.Res.Params)["semitones_b"]) != "3.5" || align(pl, 0).KeySource != "" {
		t.Errorf("explicit: %s %+v", paramsMap(pl.Res.Params)["semitones_b"], align(pl, 0))
	}
	pl = r.mustBuild(q(`,"key_policy":"none"`))
	if _, ok := paramsMap(pl.Res.Params)["semitones_b"]; ok && string(paramsMap(pl.Res.Params)["semitones_b"]) != "0" {
		t.Errorf("none: %s", paramsMap(pl.Res.Params)["semitones_b"])
	}
	// a shift beyond six semitones warns
	pl = r.mustBuild(q(`,"semitones_b":7`))
	if !slices.Contains(codes(pl.Warnings), WarnHeavyPitchShift) {
		t.Errorf("warnings %v", codes(pl.Warnings))
	}
}

// TestCamelot_Inverse: the 24-row inverse table is the inverse of features.CamelotOf.
func TestCamelot_Inverse(t *testing.T) {
	t.Parallel()
	n := 0
	for pc := 0; pc < 12; pc++ {
		for _, mode := range []string{"major", "minor"} {
			c := features.CamelotOf(pc, mode)
			gp, gm, ok := camelotInverse(c.Num, c.Letter)
			if !ok || gp != pc || gm != (mode == "minor") {
				t.Errorf("%d %s -> %s -> (%d, %v, %v)", pc, mode, c, gp, gm, ok)
			}
			n++
		}
	}
	if n != 24 {
		t.Fatal("not 24 rows")
	}
	for _, bad := range [][2]any{{0, "A"}, {13, "B"}, {5, "C"}} {
		if _, _, ok := camelotInverse(bad[0].(int), bad[1].(string)); ok {
			t.Errorf("%v accepted", bad)
		}
	}
}

// TestKeyShift_Table: C204.4.
func TestKeyShift_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		aPC    int
		aMin   bool
		bPC    int
		bMin   bool
		want   float64
		labels string
	}{
		{9, true, 0, false, 0, "A min, C maj"},
		{0, false, 2, false, -2, "C, D"},
		{0, false, 6, false, -6, "C, F#"},
		{6, false, 0, false, -6, "F#, C (6 folds to -6)"},
		{9, true, 11, true, -2, "A min, B min"},
		{3, false, 0, true, 0, "Eb maj, C min"},
		{0, false, 10, false, 2, "C, Bb"},
	} {
		if got := keyShift(c.aPC, c.aMin, c.bPC, c.bMin); got != c.want {
			t.Errorf("%s: %v, want %v", c.labels, got, c.want)
		}
	}
}

// TestFold_Table: C204.5.
func TestFold_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		partner, measured float64
		k                 int
		bpm               float64
	}{{92, 128, 0, 128}, {140, 70, 1, 140}, {90, 200, -1, 100}, {120, 60, 1, 120}, {100, 100, 0, 100}} {
		k, bpm := foldOctave(c.partner, c.measured)
		if k != c.k || math.Abs(bpm-c.bpm) > 1e-9 {
			t.Errorf("Fold(%v, %v) = %d, %v; want %d, %v", c.partner, c.measured, k, bpm, c.k, c.bpm)
		}
	}
}

// TestResolve_Sections: a section resolves only from sections that carry times. A lyrics section has no start_s, which is
// the common case on real Takes: section_unavailable is the first row. A plan section does not resolve either.
func TestResolve_Sections(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	at := func(a string) Request { return r.morphAB(`,"a_at":` + a) }
	// 1. the lyrics sections of A have no times
	_, err := r.build(at(`{"section":"chorus#1"}`))
	wantRule(t, err, "a_at.section", RuleSectionUnavailable)
	// 2. a plan section is not enough
	r.section("A", "plan", 0, "chorus", 8)
	_, err = r.build(at(`{"section":"chorus#1"}`))
	wantRule(t, err, "a_at.section", RuleSectionUnavailable)
	// 3. a measured chorus resolves; the second ordinal does not exist
	r.section("A", "measured", 0, "chorus", 8.1)
	pl := r.mustBuild(at(`{"section":"chorus#1"}`))
	a := paramsMap(pl.Res.Params)["a_at"]
	if got := string(a); got != `{"bar":3,"section":"chorus#1","start_s":8.1}` {
		t.Errorf("a_at = %s", got)
	}
	_, err = r.build(at(`{"section":"chorus#2"}`))
	wantRule(t, err, "a_at.section", RuleSectionUnavailable)
	// 4. a manual section wins over a measured one
	r.section("A", "manual", 0, "chorus", 6)
	pl = r.mustBuild(at(`{"section":"chorus#1"}`))
	if got := string(paramsMap(pl.Res.Params)["a_at"]); got != `{"bar":2,"section":"chorus#1","start_s":6}` {
		t.Errorf("manual a_at = %s", got)
	}
	// 5. shepard's default start_s: the first timed chorus; without one, 0 with a warning
	sh := func(label string) Request {
		return req(KindShepard, fmt.Sprintf(`{"source_id":%q,"loop_s":2}`, r.tx(label)))
	}
	pl = r.mustBuild(sh("A"))
	if got := string(paramsMap(pl.Res.Params)["start_s"]); got != "6" {
		t.Errorf("shepard start_s = %s", got)
	}
	if slices.Contains(codes(pl.Warnings), WarnStartDefaulted) {
		t.Error("a timed section still warned start_defaulted")
	}
	pl = r.mustBuild(sh("NF"))
	if got := string(paramsMap(pl.Res.Params)["start_s"]); got != "0" || !slices.Contains(codes(pl.Warnings), WarnStartDefaulted) {
		t.Errorf("default start_s = %s warnings %v", got, codes(pl.Warnings))
	}
}

// TestResolve_DownbeatRequired: the downbeat is explicit, a plan bar grid, or the "auto" opt-in; nothing is guessed.
func TestResolve_DownbeatRequired(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pair := func(a, b, extra string) Request {
		return req(KindMorph, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q,"bars":2%s}`, r.tx(a), r.tx(b), extra))
	}
	_, err := r.build(pair("A", "B", ""))
	pe := wantProblem(t, err, 422, ErrAlignmentUnresolved)
	if pe.Details["quantity"] != "downbeat" || pe.Details["pass"] != "downbeat_a_s" {
		t.Errorf("details %v", pe.Details)
	}
	_, err = r.build(pair("A", "B", `,"downbeat_a_s":0.5`))
	if pe = wantProblem(t, err, 422, ErrAlignmentUnresolved); pe.Details["pass"] != "downbeat_b_s" {
		t.Errorf("details %v", pe.Details)
	}
	// the plan bar grid of S anchors B without a number; no downbeat_source is guessed for A
	pl := r.mustBuild(pair("A", "S", `,"downbeat_a_s":0.5`))
	if b := align(pl, 1); b.PlanBarS == nil || b.DownbeatS != nil || b.DownbeatAuto {
		t.Errorf("S = %+v", b)
	}
	// auto opts in and warns
	pl = r.mustBuild(pair("A", "B", `,"downbeat_a_s":"auto","downbeat_b_s":0.1`))
	if a := align(pl, 0); !a.DownbeatAuto || a.DownbeatS != nil {
		t.Errorf("A = %+v", a)
	}
	if w := codes(pl.Warnings); !slices.Contains(w, WarnDownbeatAuto) || !slices.Contains(w, WarnBeatPhasePending) {
		t.Errorf("warnings %v", w)
	}
	// a downbeat at or after the end of the input is refused
	_, err = r.build(pair("A", "B", `,"downbeat_a_s":20,"downbeat_b_s":0.1`))
	wantRule(t, err, "downbeat_a_s", RuleRange)
}

// TestResolve_PairLimits: the morph and polytempo closed forms refuse what cannot fit.
func TestResolve_PairLimits(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// 20 s at 92 bpm is 7 bars; 8 bars of transition cannot fit: a_too_short
	_, err := r.build(r.morphAB(`,"bars":8,"a_at":{"bar":3}`))
	wantRule(t, err, "a_at", RuleATooShort)
	// B too short: 32 bars of 128 bpm do not fit in 20 s
	_, err = r.build(r.morphAB(`,"bars":2,"b_at":{"bar":9}`))
	wantRule(t, err, "b_at", RuleBTooShort)
	// a fitting morph has a closed-form output length
	pl := r.mustBuild(r.morphAB(`,"bars":2,"a_at":{"bar":1},"b_at":{"bar":0}`))
	barA := 4 * 60 / 92.0
	tA := 0.4 + barA
	total := rampTotal(92, 128, 8)
	bTail := 0.2 + 8*60/128.0
	if want := tA + total + (20 - bTail); math.Abs(pl.Res.OutS-want) > 1e-9 {
		t.Errorf("out %v, want %v", pl.Res.OutS, want)
	}
	// polytempo: converge_bar defaults to the bar at 40 % of A (at least 4); too early is refused with explicit downbeats
	pt := func(extra string) Request {
		return req(KindPolytempo, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q,"downbeat_a_s":0.4,"downbeat_b_s":0.2%s}`, r.tx("A"), r.tx("B"), extra))
	}
	pl = r.mustBuild(pt(`,"unison_bars":1`))
	if got := string(paramsMap(pl.Res.Params)["converge_bar"]); got != "4" {
		t.Errorf("converge_bar default %s", got)
	}
	_, err = r.build(pt(`,"downbeat_b_s":19,"converge_bar":4`))
	wantRule(t, err, "converge_bar", RuleConvergeTooEarly)
	_, err = r.build(pt(`,"converge_bar":6,"unison_bars":8`))
	wantRule(t, err, "converge_bar", RuleATooShort)
	// glacier: length defaults to min(30, dur - start); too_long and start past the end are refused
	g := func(extra string) Request {
		return req(KindGlacier, fmt.Sprintf(`{"source_id":%q%s}`, r.tx("A"), extra))
	}
	pl = r.mustBuild(g(`,"start_s":5`))
	if got := string(paramsMap(pl.Res.Params)["length_s"]); got != "15" {
		t.Errorf("length_s %s", got)
	}
	_, err = r.build(g(`,"start_s":25`))
	wantRule(t, err, "start_s", RuleStartTooLate)
	r.svc.o.Config.TransformMaxOutS = 60
	_, err = r.build(g(`,"factor":20,"length_s":4`)) // 80 s of output
	wantRule(t, err, "length_s", RuleTooLong)
}
