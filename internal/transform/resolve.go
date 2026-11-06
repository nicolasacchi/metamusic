package transform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/align"
	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// This file is the resolution of C204.2 (D-T1-9): per input, tempo, key and downbeat are resolved once, at create time, and the
// numbers and their sources are written into the spec. Estimate and create call the same code, so they cannot disagree.

// queryer is the read surface shared by *sql.DB and *sql.Tx.
type queryer interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

// Tempo sources recorded in the spec (InputAlign.BPMSource).
const (
	SrcExplicit = "explicit"
	SrcPlan     = "plan"
	SrcDeclared = "declared"
	SrcMeasured = "measured"
)

// tempoLowConfidence is the confidence below which a measured tempo of the direct analyses fallback is flagged
// (tempo_measured_low_confidence). It is a warning, never a refusal: the analyzer already answers "no tempo" below its own
// gate (D-T1-9, revision 3), so there is no second threshold that rejects a value.
const tempoLowConfidence = 0.30

// inputInfo is one input Take as the resolution sees it: its row, and what the ladder found.
type inputInfo struct {
	Ref       InputRef
	Title     string
	DurationS float64
	Rate      int
	Frames    int64
	LUFS      *float64 // the measured integrated loudness (Ears), nil when none
	Align     InputAlign
	HasBPM    bool
	HasKey    bool
	// Confidence is the confidence of a measured tempo read from an analyses row (nil otherwise).
	Confidence *float64
	Camelot    string
	// rawBPM is the tempo before any octave fold; octave marks a measured tempo that is octave-ambiguous.
	rawBPM float64
	octave bool
}

// resolution is the outcome of resolving one request's inputs against its parameters.
type resolution struct {
	Params   json.RawMessage
	Align    *Alignment
	Inputs   []inputInfo
	Warnings []Warning
	OutS     float64 // the output length in seconds, exact for the closed-form kinds
}

// notFound is the 404 of an unknown input Take.
func notFound(trackID string) error {
	return problem.E(http.StatusNotFound, "NOT_FOUND", "no such take", map[string]any{"track_id": trackID})
}

// alignmentErr is 422 ALIGNMENT_UNRESOLVED naming the input, the quantity and the parameter that supplies it.
func alignmentErr(input, quantity, pass string) error {
	return problem.E(http.StatusUnprocessableEntity, ErrAlignmentUnresolved,
		"cannot resolve the "+quantity+" of "+input+"; pass "+pass,
		map[string]any{"input": input, "quantity": quantity, "pass": pass})
}

// invalidErr is 422 INVALID_TRANSFORM for a rule refusal.
func invalidErr(e *RuleError) error {
	return problem.E(http.StatusUnprocessableEntity, ErrInvalidTransform, "invalid transform: "+e.Field+": "+e.Rule,
		map[string]any{"field": e.Field, "rule": e.Rule})
}

// asProblem converts an error of this package or the library into the problem model the operations answer.
func asProblem(err error) error {
	var re *RuleError
	var pe *problem.Error
	var ru *library.RecipeUnknownError
	var lm *library.LossyMasterError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &pe):
		return err
	case errors.As(err, &re):
		return invalidErr(re)
	case errors.As(err, &ru):
		return problem.E(http.StatusUnprocessableEntity, "RECIPE_UNKNOWN", "the recipe of this take is unknown (reference take)", map[string]any{"track_id": ru.TrackID})
	case errors.As(err, &lm):
		return problem.E(http.StatusUnprocessableEntity, "LOSSY_MASTER", "the master of this take is a lossy MP3", map[string]any{"track_id": lm.TrackID})
	case errors.Is(err, library.ErrNotFound):
		return problem.E(http.StatusNotFound, "NOT_FOUND", "no such take", nil)
	}
	return err
}

// loadInput reads one input Take and applies the qualification of D-T1-11: the recipe is known (a reference is refused with
// RECIPE_UNKNOWN) and the master is lossless (LOSSY_MASTER). Transform Takes qualify. The checks come from the library so they
// say the same thing everywhere; the transform_inputs_lossless trigger is the belt behind them.
func (s *Service) loadInput(ctx context.Context, q queryer, ref InputRef) (inputInfo, error) {
	in := inputInfo{Ref: ref}
	var lufs sql.NullFloat64
	err := q.QueryRowContext(ctx, `SELECT t.title, t.duration_s, t.sample_rate, t.total_samples, t.master_sha256,
		(SELECT json_extract(a.metrics_json, '$.integrated_lufs') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'loudness'
		 ORDER BY a.created_at DESC, a.version DESC LIMIT 1)
		FROM tracks t WHERE t.id = ?`, ref.TrackID).Scan(&in.Title, &in.DurationS, &in.Rate, &in.Frames, &in.Ref.MasterSHA256, &lufs)
	if errors.Is(err, sql.ErrNoRows) {
		return in, notFound(ref.TrackID)
	}
	if err != nil {
		return in, fmt.Errorf("transform: load input: %w", err)
	}
	if lufs.Valid {
		in.LUFS = &lufs.Float64
	}
	if err := s.o.Library.RequireRecipeKnown(ctx, ref.TrackID); err != nil {
		return in, asProblem(err)
	}
	if err := s.o.Library.RequireLosslessMaster(ctx, ref.TrackID); err != nil {
		return in, asProblem(err)
	}
	return in, nil
}

// ---- tempo ----

type tempoPick struct {
	bpm        float64
	source     string
	octave     bool
	confidence *float64
}

// planGrid reads the plan bar grid of a Take: scores.bpm and scores.seconds_per_bar of an aligned score.
func planGrid(ctx context.Context, q queryer, trackID string) (bpm, secPerBar sql.NullFloat64, err error) {
	err = q.QueryRowContext(ctx, `SELECT bpm, seconds_per_bar FROM scores WHERE track_id = ? AND aligned = 1`, trackID).Scan(&bpm, &secPerBar)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullFloat64{}, sql.NullFloat64{}, nil
	}
	return bpm, secPerBar, err
}

// resolveTempo walks the tempo ladder of D-T1-9 and C204.2, first hit wins: explicit, plan (scores.bpm when aligned), declared
// (take_features.bpm_requested, else tracks.bpm_declared), measured (take_features.bpm_measured, else the Ears tempo analysis).
// A Take without a feature row falls back to the direct reads, so create never waits for the feature sweep.
func (s *Service) resolveTempo(ctx context.Context, q queryer, trackID string, explicit *float64, row *features.Row) (tempoPick, bool, error) {
	if explicit != nil {
		return tempoPick{bpm: *explicit, source: SrcExplicit}, true, nil
	}
	bpm, _, err := planGrid(ctx, q, trackID)
	if err != nil {
		return tempoPick{}, false, fmt.Errorf("transform: plan tempo: %w", err)
	}
	if bpm.Valid && bpm.Float64 > 0 {
		return tempoPick{bpm: bpm.Float64, source: SrcPlan}, true, nil
	}
	if row != nil && row.BPMRequestedx100 != nil {
		return tempoPick{bpm: float64(*row.BPMRequestedx100) / 100, source: SrcDeclared}, true, nil
	}
	var declared sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT bpm_declared FROM tracks WHERE id = ?`, trackID).Scan(&declared); err != nil {
		return tempoPick{}, false, fmt.Errorf("transform: declared tempo: %w", err)
	}
	if row == nil && declared.Valid && declared.Int64 > 0 {
		return tempoPick{bpm: float64(declared.Int64), source: SrcDeclared}, true, nil
	}
	if row != nil && row.BPMMeasuredx100 != nil {
		return tempoPick{bpm: float64(*row.BPMMeasuredx100) / 100, source: SrcMeasured, octave: row.BPMOctave}, true, nil
	}
	if row != nil && declared.Valid && declared.Int64 > 0 { // a row without a requested tempo but a declared one on the Take
		return tempoPick{bpm: float64(declared.Int64), source: SrcDeclared}, true, nil
	}
	m, ok, err := s.analysisTempo(ctx, q, trackID)
	if err != nil || !ok {
		return tempoPick{}, false, err
	}
	return tempoPick{bpm: m.BPM, source: SrcMeasured, octave: m.OctaveAmbiguous, confidence: &m.Confidence}, true, nil
}

// analysisTempo reads the Ears tempo analysis directly (the fallback of a Take with no feature row).
func (s *Service) analysisTempo(ctx context.Context, q queryer, trackID string) (features.TempoMeasure, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT version, metrics_json FROM analyses WHERE track_id = ? AND analyzer = 'tempo'`, trackID)
	if err != nil {
		return features.TempoMeasure{}, false, fmt.Errorf("transform: tempo analysis: %w", err)
	}
	defer rows.Close()
	var all []features.AnalysisRow
	for rows.Next() {
		var r features.AnalysisRow
		if err := rows.Scan(&r.Version, &r.MetricsJSON); err != nil {
			return features.TempoMeasure{}, false, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return features.TempoMeasure{}, false, err
	}
	best, ok := features.SelectAnalysis(all, analysis.TempoDef)
	if !ok {
		return features.TempoMeasure{}, false, nil
	}
	var m struct {
		BPM        float64 `json:"bpm"`
		Confidence float64 `json:"confidence"`
		Octave     bool    `json:"octave_ambiguous"`
	}
	if json.Unmarshal([]byte(best.MetricsJSON), &m) != nil || m.BPM <= 0 {
		return features.TempoMeasure{}, false, nil
	}
	return features.TempoMeasure{BPM: m.BPM, Confidence: m.Confidence, OctaveAmbiguous: m.Octave}, true, nil
}

// foldOctave applies D3's align.Fold (C204.5): the exponent k of the octave reading nearest the partner and the folded tempo.
func foldOctave(partner, measured float64) (int, float64) {
	k := align.Fold(partner, measured)
	return k, align.Folded(measured, k)
}

// ---- key ----

// camelotMajorPC is the inverse of features.CamelotOf for major keys: the tonic pitch class of each Camelot number 1..12 (index
// 0 unused). A minor key shares its number with its relative major, three semitones up. TestCamelot_Inverse pins the 24 rows
// against features.CamelotOf.
var camelotMajorPC = [13]int{0, 11, 6, 1, 8, 3, 10, 5, 0, 7, 2, 9, 4}

// camelotInverse maps a Camelot code to a tonic pitch class and a mode.
func camelotInverse(num int, letter string) (pc int, minor, ok bool) {
	if num < 1 || num > 12 || (letter != "A" && letter != "B") {
		return 0, false, false
	}
	rel := camelotMajorPC[num]
	if letter == "B" {
		return rel, false, true
	}
	return (rel + 9) % 12, true, true
}

// keyShift is D3's align.KeyShift (C204.4).
func keyShift(aPC int, aMinor bool, bPC int, bMinor bool) float64 {
	return align.KeyShift(aPC, aMinor, bPC, bMinor)
}

// resolveKey walks the key ladder: take_features.key_num/key_letter with its basis, else the ABC header of the Take's score
// (when a ScoreABC hook is wired), else none.
func (s *Service) resolveKey(ctx context.Context, trackID string, row *features.Row) (pc int, minor bool, source string, camelot string, ok bool) {
	if row != nil && row.Key != nil {
		if p, m, k := camelotInverse(row.Key.Num, row.Key.Letter); k {
			return p, m, string(row.KeyBasis), features.Camelot{Num: row.Key.Num, Letter: row.Key.Letter}.String(), true
		}
	}
	if s.o.ScoreABC != nil {
		if abc, found := s.o.ScoreABC(ctx, trackID); found {
			if tonic, mode, parsed := features.ParseABCKey(abc); parsed {
				c := features.CamelotOf(tonic, mode)
				return tonic, mode == "minor", SrcPlan, c.String(), true
			}
		}
	}
	return 0, false, "", "", false
}

// ---- sections ----

// sectionStart resolves "chorus#2" to a start second from the Take's sections that carry times (source manual or measured; a
// manual row wins). A plan or lyrics section has no time on main, so section_unavailable is the common outcome (C204.2).
func sectionStart(ctx context.Context, q queryer, trackID, section string) (float64, bool, error) {
	var kind string
	var ord int
	if _, err := fmt.Sscanf(replaceHash(section), "%s %d", &kind, &ord); err != nil || ord < 1 {
		return 0, false, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT source, start_s FROM sections WHERE track_id = ? AND kind = ? AND source IN ('manual','measured')
		ORDER BY CASE source WHEN 'manual' THEN 0 ELSE 1 END, idx`, trackID, kind)
	if err != nil {
		return 0, false, fmt.Errorf("transform: sections: %w", err)
	}
	defer rows.Close()
	best, n := "", 0
	for rows.Next() {
		var src string
		var start float64
		if err := rows.Scan(&src, &start); err != nil {
			return 0, false, err
		}
		if best == "" {
			best = src
		}
		if src != best {
			break
		}
		n++
		if n == ord {
			return start, true, nil
		}
	}
	return 0, false, rows.Err()
}

func replaceHash(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == '#' {
			b[i] = ' '
		}
	}
	return string(b)
}

// ---- per-kind resolution ----

// maxChainHops bounds the primary-parent walk (C218.5).
const maxChainHops = 16

// chainKinds lists the transform kinds of a Take and its primary-parent ancestors, nearest first (at most maxChainHops).
func chainKinds(ctx context.Context, q queryer, trackID string) ([]Kind, error) {
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE c(id, parent, tid, depth) AS (
		SELECT id, parent_track_id, transform_id, 0 FROM tracks WHERE id = ?
		UNION ALL SELECT t.id, t.parent_track_id, t.transform_id, c.depth + 1 FROM tracks t JOIN c ON t.id = c.parent WHERE c.depth < ?)
		SELECT x.kind FROM c JOIN transforms x ON x.id = c.tid ORDER BY c.depth`, trackID, maxChainHops)
	if err != nil {
		return nil, fmt.Errorf("transform: chain: %w", err)
	}
	defer rows.Close()
	var out []Kind
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, Kind(k))
	}
	return out, rows.Err()
}

func rawFloat(m map[string]json.RawMessage, name string) *float64 {
	raw, ok := m[name]
	if !ok {
		return nil
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return &f
}

// resolveInputs fills the tempo (and, for morph, the key) of the inputs and returns the Alignment of the spec. It is the
// ladder of C204.2 over both inputs, with the octave fold of the measured ones toward their partner.
func (s *Service) resolveTempoPair(ctx context.Context, q queryer, kind Kind, m map[string]json.RawMessage, in []inputInfo) ([]string, error) {
	names := [2]string{"bpm_a", "bpm_b"}
	var rows [2]*features.Row
	for i := range in {
		r, ok, err := s.o.Features.Take(ctx, in[i].Ref.TrackID)
		if err != nil {
			return nil, err
		}
		if ok {
			rows[i] = &r
		}
	}
	for i := range in {
		pick, ok, err := s.resolveTempo(ctx, q, in[i].Ref.TrackID, rawFloat(m, names[i]), rows[i])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, alignmentErr(in[i].Ref.Role, "bpm", names[i])
		}
		in[i].rawBPM, in[i].octave, in[i].Confidence = pick.bpm, pick.octave, pick.confidence
		in[i].Align.BPM, in[i].Align.BPMSource, in[i].HasBPM = pick.bpm, pick.source, true
	}
	var warns []string
	// fold measured, octave-ambiguous tempos toward the partner (never explicit, plan or declared)
	if len(in) == 2 {
		for _, i := range []int{0, 1} {
			if in[i].Align.BPMSource == SrcMeasured && in[i].octave {
				k, folded := foldOctave(in[1-i].Align.BPM, in[i].rawBPM)
				if k != 0 && folded >= 40 && folded <= 240 {
					in[i].Align.OctaveFold, in[i].Align.BPM = k, round(folded, 2)
					warns = append(warns, WarnTempoOctaveFolded)
				}
			}
		}
	}
	for i := range in {
		if c := in[i].Confidence; c != nil && *c < tempoLowConfidence && in[i].Align.BPMSource == SrcMeasured {
			warns = append(warns, WarnTempoMeasuredLowConfidence)
		}
	}
	return warns, nil
}

// resolveDownbeats records the downbeat anchor of each input: an explicit number, the "auto" opt-in, or the plan bar grid;
// none of them is ALIGNMENT_UNRESOLVED naming downbeat_a_s / downbeat_b_s. Nothing is ever silently guessed.
func (s *Service) resolveDownbeats(ctx context.Context, q queryer, m map[string]json.RawMessage, in []inputInfo) error {
	names := [2]string{"downbeat_a_s", "downbeat_b_s"}
	for i := range in {
		var raw any
		if r, ok := m[names[i]]; ok {
			_ = json.Unmarshal(r, &raw)
		}
		switch v := raw.(type) {
		case float64:
			d := v
			in[i].Align.DownbeatS = &d
			continue
		case string: // "auto", checked by validation
			in[i].Align.DownbeatAuto = true
			continue
		}
		_, spb, err := planGrid(ctx, q, in[i].Ref.TrackID)
		if err != nil {
			return fmt.Errorf("transform: plan grid: %w", err)
		}
		if !spb.Valid || spb.Float64 <= 0 {
			return alignmentErr(in[i].Ref.Role, "downbeat", names[i])
		}
		g := spb.Float64
		in[i].Align.PlanBarS = &g
	}
	return nil
}

// resolveKeys records the key of each input and, for morph with key_policy to_a and no explicit semitones_b, the shift of B
// into A's key. A key unknown for either input is not an error: the shift is 0 and the estimate warns.
func (s *Service) resolveKeys(ctx context.Context, in []inputInfo, out map[string]any, m map[string]json.RawMessage) (warn bool, err error) {
	if paramString(m, "key_policy") == "none" || len(in) != 2 {
		return false, nil
	}
	if raw := rawFloat(m, "semitones_b"); raw != nil {
		return false, nil
	}
	known := 0
	for i := range in {
		r, ok, err := s.o.Features.Take(ctx, in[i].Ref.TrackID)
		if err != nil {
			return false, err
		}
		var rp *features.Row
		if ok {
			rp = &r
		}
		pc, minor, src, cam, found := s.resolveKey(ctx, in[i].Ref.TrackID, rp)
		if found {
			in[i].Align.KeyPC, in[i].Align.KeyMinor, in[i].Align.KeySource, in[i].Camelot, in[i].HasKey = pc, minor, src, cam, true
			known++
		}
	}
	shift := 0.0
	if known == 2 {
		shift = keyShift(in[0].Align.KeyPC, in[0].Align.KeyMinor, in[1].Align.KeyPC, in[1].Align.KeyMinor)
	}
	out["semitones_b"] = shift
	return known < 2, nil
}

// rampSeconds is Total() of the transition for the resolved tempos and bars.
func rampSeconds(t0, t1 float64, bars, bpb int) float64 {
	return align.Ramp{T0: t0, T1: t1, Beats: float64(bars * bpb)}.Total()
}

// resolve runs the kind-specific resolution of the validated parameters against the loaded inputs: tempo, key, downbeat,
// sections and the defaults that depend on them, the closed-form output length and the checks that need the inputs' lengths.
func (s *Service) resolve(ctx context.Context, q queryer, kind Kind, params json.RawMessage, in []inputInfo) (resolution, error) {
	m := paramsMap(params)
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	res := resolution{Inputs: in}
	add := func(codes ...string) {
		for _, c := range codes {
			res.Warnings = append(res.Warnings, Warning{Code: c})
		}
	}
	var err error
	switch kind {
	case KindMorph, KindPolytempo:
		err = s.resolvePair(ctx, q, kind, m, out, in, &res, add)
	case KindShepard:
		err = s.resolveShepard(ctx, q, m, out, in, &res, add)
	case KindGlacier:
		err = s.resolveGlacier(m, out, in[0], &res)
	case KindEnsemble:
		res.OutS = in[0].DurationS + float64(ensembleTailFrames(m, in[0].Rate))/float64(in[0].Rate)
	default: // the hi-fi kinds (C223.1): master and widen keep the length, binaural adds its room tail
		res.OutS = in[0].DurationS
		if kind == KindBinaural && paramString(m, "room") != "none" {
			res.OutS += paramFloat(m, "tail_s", 1)
		}
	}
	if err != nil {
		return res, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return res, err
	}
	res.Params = raw
	if kind == KindMorph || kind == KindPolytempo || (kind == KindShepard && in[0].HasBPM) {
		a := &Alignment{}
		for _, i := range in {
			a.Inputs = append(a.Inputs, i.Align)
		}
		res.Align = a
	}
	return res, nil
}

func (s *Service) resolvePair(ctx context.Context, q queryer, kind Kind, m map[string]json.RawMessage, out map[string]any, in []inputInfo, res *resolution, add func(...string)) error {
	bpb := paramInt(m, "beats_per_bar", 4)
	for i := range in {
		in[i].Align.BeatsPerBar = bpb
	}
	tw, err := s.resolveTempoPair(ctx, q, kind, m, in)
	if err != nil {
		return err
	}
	add(tw...)
	if err := s.resolveDownbeats(ctx, q, m, in); err != nil {
		return err
	}
	out["bpm_a"], out["bpm_b"] = in[0].Align.BPM, in[1].Align.BPM
	a, b := &in[0], &in[1]
	t0, t1 := a.Align.BPM, b.Align.BPM
	barA, barB := float64(bpb)*60/t0, float64(bpb)*60/t1
	d0a, d0b := 0.0, 0.0
	if a.Align.DownbeatS != nil {
		d0a = *a.Align.DownbeatS
	}
	if b.Align.DownbeatS != nil {
		d0b = *b.Align.DownbeatS
	}
	for _, x := range []struct {
		n string
		i inputInfo
	}{{"downbeat_a_s", *a}, {"downbeat_b_s", *b}} {
		if x.i.Align.DownbeatS != nil && *x.i.Align.DownbeatS >= x.i.DurationS {
			return rule(x.n, RuleRange)
		}
	}
	if kind == KindMorph {
		return s.resolveMorph(ctx, q, m, out, a, b, bpb, t0, t1, barA, barB, d0a, d0b, res, add)
	}
	// polytempo (converge): output ends unison_bars after the meeting
	unison := paramInt(m, "unison_bars", 8)
	conv := paramInt(m, "converge_bar", 0)
	if raw := rawFloat(m, "converge_bar"); raw == nil {
		conv = max(4, int(math.Floor(0.4*a.DurationS/barA)))
	}
	out["converge_bar"] = conv
	tc := d0a + float64(conv)*barA
	l := t0
	if math.Abs(dsp.Log(t0/t1)) > 1e-12 {
		l = (t0 - t1) / dsp.Log(t0/t1)
	}
	if tc < 60*(d0b*t1/60)/l { // no integer m >= 0 keeps the entry offset non-negative (C205.2)
		return rule("converge_bar", RuleConvergeTooEarly)
	}
	res.OutS = tc + float64(unison)*barA
	if res.OutS > a.DurationS+1e-9 {
		return rule("converge_bar", RuleATooShort)
	}
	return nil
}

func (s *Service) resolveMorph(ctx context.Context, q queryer, m map[string]json.RawMessage, out map[string]any, a, b *inputInfo, bpb int,
	t0, t1, barA, barB, d0a, d0b float64, res *resolution, add func(...string)) error {
	bars := paramInt(m, "bars", 8)
	in := []inputInfo{*a, *b}
	warn, err := s.resolveKeys(ctx, in, out, m)
	if err != nil {
		return err
	}
	*a, *b = in[0], in[1]
	if warn {
		add(WarnKeyUnknownAssumedNone)
	}
	atA, tA, err := s.resolveAt(ctx, q, "a_at", m["a_at"], a, barA, d0a, true, bars, add)
	if err != nil {
		return err
	}
	atB, tB, err := s.resolveAt(ctx, q, "b_at", m["b_at"], b, barB, d0b, false, bars, add)
	if err != nil {
		return err
	}
	out["a_at"], out["b_at"] = atA, atB
	total := rampSeconds(t0, t1, bars, bpb)
	if tA+total > a.DurationS+1e-9 {
		return rule("a_at", RuleATooShort)
	}
	bTail := tB + float64(bars*bpb)*60/t1
	if bTail > b.DurationS+1e-9 {
		return rule("b_at", RuleBTooShort)
	}
	res.OutS = tA + total + (b.DurationS - bTail)
	return nil
}

// resolveAt resolves one a_at / b_at: a bar, a section (only from sections that carry times) or the default. It returns the
// value written into the spec and the position of the transition in seconds on the grid (the downbeat assumed 0 when it is a
// plan anchor or "auto": the render measures it locally, C204.3).
func (s *Service) resolveAt(ctx context.Context, q queryer, name string, raw json.RawMessage, in *inputInfo, barS, d0 float64, isA bool, bars int, add func(...string)) (map[string]any, float64, error) {
	if len(raw) > 0 {
		var at struct {
			Bar     *int    `json:"bar"`
			Section *string `json:"section"`
		}
		_ = json.Unmarshal(raw, &at)
		if at.Bar != nil {
			return map[string]any{"bar": *at.Bar}, d0 + float64(*at.Bar)*barS, nil
		}
		if at.Section != nil {
			start, ok, err := sectionStart(ctx, q, in.Ref.TrackID, *at.Section)
			if err != nil {
				return nil, 0, err
			}
			if !ok {
				add(WarnSectionUnavailable)
				return nil, 0, rule(name+".section", RuleSectionUnavailable)
			}
			res := map[string]any{"section": *at.Section, "start_s": round(start, 3)}
			bar := int(math.Max(0, math.Round((start-d0)/barS)))
			if !in.Align.DownbeatAuto {
				res["bar"] = bar
			}
			return res, d0 + float64(bar)*barS, nil
		}
	}
	if !isA {
		return map[string]any{"bar": 0}, d0, nil
	}
	// default for A: the last bars+2 bars before its end
	if in.Align.DownbeatAuto {
		if float64(bars+2)*barS > in.DurationS {
			return nil, 0, rule(name, RuleATooShort)
		}
		return map[string]any{"from_end_bars": bars + 2}, in.DurationS - float64(bars+2)*barS, nil
	}
	totalBars := int(math.Floor((in.DurationS - d0) / barS))
	bar := totalBars - (bars + 2)
	if bar < 0 {
		return nil, 0, rule(name, RuleATooShort)
	}
	return map[string]any{"bar": bar}, d0 + float64(bar)*barS, nil
}

func (s *Service) resolveShepard(ctx context.Context, q queryer, m map[string]json.RawMessage, out map[string]any, in []inputInfo, res *resolution, add func(...string)) error {
	x := &in[0]
	dur := paramFloat(m, "duration_s", 120)
	res.OutS = dur
	loopS := rawFloat(m, "loop_s")
	loopBars := rawFloat(m, "loop_bars")
	var loopLen float64
	if loopS != nil {
		loopLen = *loopS
	} else {
		row, ok, err := s.o.Features.Take(ctx, x.Ref.TrackID)
		if err != nil {
			return err
		}
		var rp *features.Row
		if ok {
			rp = &row
		}
		pick, found, err := s.resolveTempo(ctx, q, x.Ref.TrackID, nil, rp)
		if err != nil {
			return err
		}
		if !found {
			if loopBars != nil {
				return alignmentErr(x.Ref.Role, "bpm", "loop_s")
			}
			return rule("loop_s", RuleLoopSourceRequired)
		}
		bars := 4
		if loopBars != nil {
			bars = int(*loopBars)
		}
		out["loop_bars"] = bars
		x.Align = InputAlign{BPM: pick.bpm, BPMSource: pick.source, BeatsPerBar: 4}
		x.HasBPM, x.rawBPM, x.octave = true, pick.bpm, pick.octave
		loopLen = float64(bars) * 4 * 60 / pick.bpm
	}
	start := 0.0
	if f := rawFloat(m, "start_s"); f != nil {
		start = *f
	} else if sec, ok, err := sectionStart(ctx, q, x.Ref.TrackID, "chorus#1"); err != nil {
		return err
	} else if ok {
		start = round(sec, 3)
	} else {
		add(WarnStartDefaulted)
	}
	out["start_s"] = start
	if start >= x.DurationS {
		return rule("start_s", RuleStartTooLate)
	}
	if start+loopLen > x.DurationS+1e-9 {
		return rule("loop_s", RuleRange)
	}
	return nil
}

func (s *Service) resolveGlacier(m map[string]json.RawMessage, out map[string]any, x inputInfo, res *resolution) error {
	start := paramFloat(m, "start_s", 0)
	if start >= x.DurationS {
		return rule("start_s", RuleStartTooLate)
	}
	length := min(30, x.DurationS-start)
	if f := rawFloat(m, "length_s"); f != nil {
		length = *f
		if start+length > x.DurationS+1e-9 {
			return rule("length_s", RuleRange)
		}
	}
	length = round(length, 3)
	out["length_s"] = length
	res.OutS = length * paramFloat(m, "factor", 20)
	if res.OutS > float64(s.o.Config.TransformMaxOutS) {
		return rule("length_s", RuleTooLong)
	}
	return nil
}
