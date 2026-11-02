package ears_test

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/ears"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func TestEars_AnalyzeRunsKeyAndEnergy(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := lt.TrackTwinA
	out, err := r.svc.AnalyzeTrack(context.Background(), id, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(out.Ran, "key") || !slices.Contains(out.Ran, "energy") || len(out.Failed) != 0 {
		t.Fatalf("run: %+v", out)
	}
	for an, def := range map[string]string{"key": analysis.KeyDef, "energy": analysis.EnergyDef} {
		if v := r.str(`SELECT version FROM analyses WHERE track_id = ? AND analyzer = ?`, id, an); v != def+" ffmpeg fake" {
			t.Errorf("%s version = %q", an, v)
		}
		if tier := r.str(`SELECT tier FROM analyses WHERE track_id = ? AND analyzer = ?`, id, an); tier != "A" {
			t.Errorf("%s tier = %q", an, tier)
		}
	}
	var key map[string]any
	if err := json.Unmarshal([]byte(r.str(`SELECT metrics_json FROM analyses WHERE track_id = ? AND analyzer = 'key'`, id)), &key); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"key", "tonic", "mode", "camelot", "r", "margin", "ambiguous_relative", "tuning_cents", "alternatives", "analyzed_s"} {
		if _, ok := key[k]; !ok {
			t.Errorf("key metrics lack %q: %v", k, key)
		}
	}
	if key["key"] == nil {
		t.Errorf("the fake decode is a clear triad progression, yet no key was set: %v", key)
	}
	var en map[string]any
	if err := json.Unmarshal([]byte(r.str(`SELECT metrics_json FROM analyses WHERE track_id = ? AND analyzer = 'energy'`, id)), &en); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"energy", "onset_rate", "centroid_hz", "lufs", "components", "analyzed_s"} {
		if _, ok := en[k]; !ok {
			t.Errorf("energy metrics lack %q: %v", k, en)
		}
	}
	// a second run skips both; they are not Tier A requirements
	out, err = r.svc.AnalyzeTrack(context.Background(), id, ears.AnalyzeOptions{})
	if err != nil || slices.Contains(out.Ran, "key") || !slices.Contains(out.Skipped, "key") || !slices.Contains(out.Skipped, "energy") {
		t.Fatalf("second run: %+v %v", out, err)
	}
	d, err := r.svc.Dossier(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(d.Missing, "key") || slices.Contains(d.Missing, "energy") {
		t.Errorf("key and energy must not be Tier A requirements: %v", d.Missing)
	}
}

func TestEars_EnergyUsesStoredLoudness(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	id := lt.TrackCitta // has a loudness row in the fixture
	if _, err := r.svc.AnalyzeTrack(context.Background(), id, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE track_id = ? AND analyzer = 'energy' AND json_extract(metrics_json, '$.lufs') IS NOT NULL`, id); n != 1 {
		t.Errorf("the stored integrated LUFS must feed the energy loudness term (rows with lufs: %d)", n)
	}
}

func TestEars_OneDecodeForFeatures(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := lt.TrackTwinA
	ctx := context.Background()
	if _, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := r.tier.MonoCalls(); got != 1 {
		t.Fatalf("tempo, key and energy all missing: %d decodes, want 1", got)
	}
	r.dropTriggers(analysisTriggers(r)...)
	r.exec(`DELETE FROM analyses WHERE track_id = ? AND analyzer = 'key'`, id)
	out, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{})
	if err != nil || !slices.Equal(out.Ran, []string{"key"}) {
		t.Fatalf("only key missing: %+v %v", out, err)
	}
	if got := r.tier.MonoCalls(); got != 2 {
		t.Fatalf("only key missing: %d decodes in total, want 2", got)
	}
	if _, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := r.tier.MonoCalls(); got != 2 {
		t.Fatalf("everything present: %d decodes in total, want 2 (no decode)", got)
	}
	if got := r.tier.TempoCalls(); got != 1 {
		t.Errorf("tempo ran %d times, want 1", got)
	}
}

// analysisTriggers names the triggers that make analyses rows immutable, so a test can delete or edit one.
func analysisTriggers(r *rig) []string {
	rows, err := r.st.R.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'analyses'`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			r.t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

func TestEars_MonoFailureIsAWarning(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.tier.MonoErr = errors.New("decode exploded")
	id := lt.TrackTwinA
	out, err := r.svc.AnalyzeTrack(context.Background(), id, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, an := range []string{"tempo", "key", "energy"} {
		if out.Failed[an] != "TOOL_FAILED" {
			t.Errorf("%s: failed = %v", an, out.Failed)
		}
	}
	for _, an := range []string{"silence", "clipping"} {
		if !slices.Contains(out.Ran, an) {
			t.Errorf("%s did not run: %+v", an, out)
		}
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE track_id = ? AND analyzer IN ('tempo','key','energy')`, id); n != 0 {
		t.Errorf("a failed decode stored %d rows", n)
	}
	// the next run retries the three
	r.tier.MonoErr = nil
	out, err = r.svc.AnalyzeTrack(context.Background(), id, ears.AnalyzeOptions{})
	if err != nil || !slices.Equal(out.Ran, []string{"tempo", "key", "energy"}) {
		t.Fatalf("retry: %+v %v", out, err)
	}
}

func TestEars_FeatureBlindSpots(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := lt.TrackTwinA
	ctx := context.Background()
	has := func(spots []string, sub string) bool {
		return slices.ContainsFunc(spots, func(s string) bool { return strings.Contains(s, sub) })
	}
	d, err := r.svc.Dossier(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if has(d.BlindSpots, "key is an estimate") || has(d.BlindSpots, "energy mixes") {
		t.Fatalf("nothing measured yet: %v", d.BlindSpots)
	}
	if _, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	d, err = r.svc.Dossier(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"modulations", "modal or non-Western", "atonal or percussion-dominated", "drone that is not the tonic",
		"quiet but dense mix", "genre bias", "ambient music mastered loud", "not perceptual arousal"} {
		if !has(d.BlindSpots, want) {
			t.Errorf("dossier blind spots lack %q: %v", want, d.BlindSpots)
		}
	}
	// the conditional entries come after the tempo one, in the fixed order tempo, key, energy
	pos := func(sub string) int {
		return slices.IndexFunc(d.BlindSpots, func(s string) bool { return strings.Contains(s, sub) })
	}
	if !(pos("tempo is an estimate") < pos("key is an estimate") && pos("key is an estimate") < pos("energy mixes")) {
		t.Errorf("order: %v", d.BlindSpots)
	}
	p, err := r.svc.Perception(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !has(p.BlindSpots, "key is an estimate") || !has(p.BlindSpots, "energy mixes") {
		t.Errorf("perception blind spots: %v", p.BlindSpots)
	}
	if got := d.Provenance.Analyzers["key"]; !strings.HasPrefix(got, analysis.KeyDef+" ") {
		t.Errorf("provenance key = %q", got)
	}
	if got := d.Provenance.Analyzers["energy"]; !strings.HasPrefix(got, analysis.EnergyDef+" ") {
		t.Errorf("provenance energy = %q", got)
	}
}

func TestEars_DossierKeyEnergy(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := lt.TrackTwinA
	ctx := context.Background()
	d, err := r.svc.Dossier(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Key != nil || d.Energy != nil {
		t.Fatalf("before analysis: %+v %+v", d.Key, d.Energy)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), `"key"`) || strings.Contains(string(raw), `"energy"`) {
		t.Errorf("an unanalysed dossier must not carry key or energy: %s", raw)
	}
	if _, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	d, err = r.svc.Dossier(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Key == nil || d.Energy == nil {
		t.Fatalf("after analysis: %+v %+v", d.Key, d.Energy)
	}
	if !regexp.MustCompile(`^[A-G][b#]? (major|minor)$`).MatchString(d.Key.Name) || !regexp.MustCompile(`^(1[0-2]|[1-9])[AB]$`).MatchString(d.Key.Camelot) || d.Key.Confidence <= 0 {
		t.Errorf("key %+v", d.Key)
	}
	if d.Energy.Value < 0 || d.Energy.Value > 1 || d.Energy.OnsetRate == nil || d.Energy.CentroidHz == nil {
		t.Errorf("energy %+v", d.Energy)
	}
}

func TestPerception_KeyEnergyFields(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := lt.TrackTwinA
	ctx := context.Background()
	p, err := r.svc.Perception(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != nil || p.Energy != nil {
		t.Fatalf("before analysis: %+v %+v", p.Key, p.Energy)
	}
	if _, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	p, err = r.svc.Perception(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Key == nil || p.Energy == nil || p.Key.Name == "" || p.Energy.Value <= 0 {
		t.Fatalf("after analysis: %+v %+v", p.Key, p.Energy)
	}
	// a stored result without a key (or without energy) leaves the field absent, not zero
	r.dropTriggers(analysisTriggers(r)...)
	r.exec(`UPDATE analyses SET metrics_json = '{"key":null,"tonic":null,"mode":null,"camelot":null,"r":0.2,"margin":0.01,"ambiguous_relative":false,"tuning_cents":0,"alternatives":[],"analyzed_s":30}' WHERE track_id = ? AND analyzer = 'key'`, id)
	r.exec(`UPDATE analyses SET metrics_json = '{"energy":null,"onset_rate":null,"centroid_hz":null,"lufs":null,"components":null,"analyzed_s":12}' WHERE track_id = ? AND analyzer = 'energy'`, id)
	p, err = r.svc.Perception(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != nil || p.Energy != nil {
		t.Errorf("null results: %+v %+v", p.Key, p.Energy)
	}
	d, err := r.svc.Dossier(ctx, id)
	if err != nil || d.Key != nil || d.Energy != nil {
		t.Errorf("null results in the dossier: %+v %+v %v", d.Key, d.Energy, err)
	}
}

func TestBackfill_RanListsKeyAndEnergy(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	dry, err := r.svc.Backfill(context.Background(), ears.BackfillOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Ran["key"] != dry.Tracks || dry.Ran["energy"] != dry.Tracks || dry.Tracks == 0 {
		t.Errorf("dry run: %+v", dry)
	}
	rep, err := r.svc.Backfill(context.Background(), ears.BackfillOptions{Jobs: 2})
	if err != nil || rep.Ran["key"] != rep.Tracks || rep.Ran["energy"] != rep.Tracks {
		t.Errorf("report %+v %v", rep, err)
	}
	again, err := r.svc.Backfill(context.Background(), ears.BackfillOptions{})
	if err != nil || again.Ran["key"] != 0 || again.Ran["energy"] != 0 {
		t.Errorf("a second backfill must skip them: %+v %v", again, err)
	}
}
