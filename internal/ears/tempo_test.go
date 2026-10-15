package ears_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/ears"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func TestEars_AnalyzeRunsTempo(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := lt.TrackTwinA
	ctx := context.Background()

	out, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(out.Ran, "tempo") || len(out.Failed) != 0 {
		t.Fatalf("first run: %+v", out)
	}
	v := r.str(`SELECT version FROM analyses WHERE track_id = ? AND analyzer = 'tempo'`, id)
	if want := analysis.TempoDef + " ffmpeg fake"; v != want {
		t.Errorf("tempo version = %q, want %q", v, want)
	}
	if tier := r.str(`SELECT tier FROM analyses WHERE track_id = ? AND analyzer = 'tempo'`, id); tier != "A" {
		t.Errorf("tier = %q", tier)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(r.str(`SELECT metrics_json FROM analyses WHERE track_id = ? AND analyzer = 'tempo'`, id)), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"bpm", "confidence", "candidates", "octave_ambiguous", "analyzed_s"} {
		if _, ok := m[k]; !ok {
			t.Errorf("metrics_json lacks %q: %v", k, m)
		}
	}
	if got := r.tier.TempoCalls(); got != 1 {
		t.Fatalf("tempo calls = %d", got)
	}

	out, err = r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(out.Ran, "tempo") || !slices.Contains(out.Skipped, "tempo") || r.tier.TempoCalls() != 1 {
		t.Fatalf("second run must skip tempo: %+v (calls %d)", out, r.tier.TempoCalls())
	}

	out, err = r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(out.Ran, "tempo") || r.tier.TempoCalls() != 2 {
		t.Fatalf("forced run: %+v (calls %d)", out, r.tier.TempoCalls())
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE track_id = ? AND analyzer = 'tempo'`, id); n != 1 {
		t.Errorf("tempo rows = %d, want 1 (same tool version)", n)
	}
}

func TestEars_TempoFailureIsAWarning(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.tier.TempoErr = errors.New("decode exploded")
	id := lt.TrackTwinA
	out, err := r.svc.AnalyzeTrack(context.Background(), id, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Failed["tempo"] != "TOOL_FAILED" || len(out.Failed) != 1 {
		t.Fatalf("failed = %v", out.Failed)
	}
	for _, an := range []string{"silence", "clipping"} {
		if !slices.Contains(out.Ran, an) {
			t.Errorf("%s did not run: %+v", an, out)
		}
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE track_id = ? AND analyzer = 'tempo'`, id); n != 0 {
		t.Errorf("a failed tempo stored %d rows", n)
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE track_id = ? AND analyzer IN ('silence','clipping')`, id); n != 2 {
		t.Errorf("silence+clipping rows = %d, want 2", n)
	}
	// the dossier tier is untouched by tempo
	d, err := r.svc.Dossier(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(d.Missing, "tempo") {
		t.Errorf("tempo must not be a Tier A requirement: %v", d.Missing)
	}
}

func TestEars_TempoNoBPMStillStored(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.tier.SetTempo(analysis.Tempo{Candidates: []analysis.TempoCandidate{}})
	id := lt.TrackTwinA
	ctx := context.Background()
	if _, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE track_id = ? AND analyzer = 'tempo' AND json_extract(metrics_json, '$.bpm') IS NULL`, id); n != 1 {
		t.Fatalf("a tempo without bpm must still be stored, rows = %d", n)
	}
	if _, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); err != nil || r.tier.TempoCalls() != 1 {
		t.Fatalf("no-bpm result must not be recomputed: %v calls %d", err, r.tier.TempoCalls())
	}
	p, err := r.svc.Perception(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.TempoBPM != nil || p.TempoConfidence != nil {
		t.Errorf("perception tempo = %v / %v, want null", p.TempoBPM, p.TempoConfidence)
	}
}

func TestEars_TempoBlindSpots(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := lt.TrackTwinA
	ctx := context.Background()
	has := func(spots []string) bool {
		return slices.ContainsFunc(spots, func(s string) bool { return strings.Contains(s, "tempo is an estimate") })
	}
	d, err := r.svc.Dossier(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if has(d.BlindSpots) {
		t.Fatalf("no tempo measured yet, yet the blind spot is listed: %v", d.BlindSpots)
	}
	if _, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	d, err = r.svc.Dossier(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !has(d.BlindSpots) {
		t.Fatalf("dossier lacks the tempo blind spot: %v", d.BlindSpots)
	}
	for _, want := range []string{"tempo changes", "rubato", "free-time intros", "half/double"} {
		if !slices.ContainsFunc(d.BlindSpots, func(s string) bool { return strings.Contains(s, want) }) {
			t.Errorf("blind spots lack %q: %v", want, d.BlindSpots)
		}
	}
	if got := d.Provenance.Analyzers["tempo"]; !strings.HasPrefix(got, analysis.TempoDef+" ") {
		t.Errorf("provenance tempo = %q", got)
	}
	p, err := r.svc.Perception(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !has(p.BlindSpots) {
		t.Errorf("perception lacks the tempo blind spot: %v", p.BlindSpots)
	}
}

func TestPerception_TempoFields(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := lt.TrackTwinA
	ctx := context.Background()
	p, err := r.svc.Perception(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.TempoBPM != nil || p.TempoConfidence != nil {
		t.Fatalf("before analysis: %v %v", p.TempoBPM, p.TempoConfidence)
	}
	bpm := 128.5
	r.tier.SetTempo(analysis.Tempo{BPM: &bpm, Confidence: 7.25, Candidates: []analysis.TempoCandidate{{BPM: bpm, Strength: 1}}})
	if _, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	p, err = r.svc.Perception(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.TempoBPM == nil || *p.TempoBPM != 128.5 || p.TempoConfidence == nil || *p.TempoConfidence != 7.25 {
		t.Fatalf("after analysis: %v %v", p.TempoBPM, p.TempoConfidence)
	}
	raw, _ := json.Marshal(p)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["tempo_bpm"]) != "128.5" || string(m["tempo_confidence"]) != "7.25" {
		t.Errorf("json tempo keys: %s %s", m["tempo_bpm"], m["tempo_confidence"])
	}
}

func TestEars_FlagCodes(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	ctx := context.Background()
	got, err := r.svc.FlagCodes(ctx, []string{lt.TrackCitta, lt.TrackLuna, lt.TrackTwinA})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"clipping", "internal_silence", "band_limited"}; !slices.Equal(got[lt.TrackCitta], want) {
		t.Errorf("citta = %v, want %v", got[lt.TrackCitta], want)
	}
	if !slices.Contains(got[lt.TrackLuna], "token_loop_suspected") {
		t.Errorf("luna = %v", got[lt.TrackLuna])
	}
	if codes, ok := got[lt.TrackTwinA]; !ok || codes == nil || len(codes) != 0 {
		t.Errorf("a take without analyses must map to an empty list, got %v (present %v)", codes, ok)
	}
	// they are the codes of the perception block
	p, err := r.svc.Perception(ctx, lt.TrackCitta)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Flags, got[lt.TrackCitta]) {
		t.Errorf("FlagCodes %v != perception flags %v", got[lt.TrackCitta], p.Flags)
	}
	// read-only: no analysis is started
	if sig, pic := r.tier.Calls(); sig != 0 || pic != 0 || r.tier.TempoCalls() != 0 {
		t.Errorf("FlagCodes started analysis: %d %d %d", sig, pic, r.tier.TempoCalls())
	}
	// unknown ids are absent, an empty batch is empty
	got, err = r.svc.FlagCodes(ctx, []string{"nope"})
	if err != nil || len(got) != 0 {
		t.Errorf("unknown id: %v %v", got, err)
	}
	if got, err = r.svc.FlagCodes(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("empty batch: %v %v", got, err)
	}
	// the batch cap
	ids := make([]string, ears.MaxFlagCodeIDs+1)
	for i := range ids {
		ids[i] = lt.TrackLuna
	}
	if _, err := r.svc.FlagCodes(ctx, ids); !errors.Is(err, ears.ErrTooManyIDs) {
		t.Errorf("101 ids: err = %v, want ErrTooManyIDs", err)
	}
	if _, err := r.svc.FlagCodes(ctx, ids[:ears.MaxFlagCodeIDs]); err != nil {
		t.Errorf("100 ids: %v", err)
	}
}
