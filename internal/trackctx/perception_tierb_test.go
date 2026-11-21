package trackctx_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// seedTierB stores a listening check of Luna the way ears.AnalyzeTierB does: a raw row, a diction row and an adherence row.
func seedTierB(t *testing.T, e *env, withAdherence bool) {
	t.Helper()
	const raw = `{"status":"ok","code":null,"stages":{"separate":"ok","asr":"ok","transcribe":"ok"},"models":{"separator":null,"asr":null,"pitch":null},` +
		`"timings":{"load_s":1,"separate_s":1,"asr_s":1,"transcribe_s":1,"total_s":4},"resources":{"device":"x","peak_vram_mib":1,"parent_reserved_mib":null},` +
		`"language":"it","forced":true,"counts":{"words":6,"segments":2,"ref_words":8},"artifact":{"sha256":"` + "0000000000000000000000000000000000000000000000000000000000000000" + `","bytes":10}}`
	const diction = `{"overall":{"cer":0.2,"wer":0.25,"ref_runes":30,"char_edits":6,"ref_words":8,"word_edits":2},"sections":[` +
		`{"section":0,"label":"verse","cer":0.1,"wer":0.1,"ref_words":4,"word_edits":0,"line_count":1,"lines":[]},` +
		`{"section":1,"label":"chorus","cer":0.4,"wer":0.5,"ref_words":4,"word_edits":2,"line_count":1,"lines":[]}],"mangled":[],"measured":{"status":"kept","sections":0}}`
	const adherence = `{"basis":"coarse","melody_chroma":0.82,"transpose_mode":0,"tempo_ratio":1.04,"key_relation":"relative","timing_source":"plan_timing","sections":[]}`
	ins := func(analyzer, version, metrics string) {
		t.Helper()
		if _, err := e.st.W.Exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?,?,?, 'B', ?, ?)`,
			lt.TrackLuna, analyzer, version, metrics, lt.FixedNow.Format("2006-01-02T15:04:05Z")); err != nil {
			t.Fatal(err)
		}
	}
	ins("tier_b_raw", "mm-tierb-raw/1 faster-whisper-large-v3@000000000000", raw)
	ins("tier_b_diction", "mm-diction/1 go1", diction)
	if withAdherence {
		ins("tier_b_adherence", "mm-adherence/1 go1", adherence)
	}
}

// TestContext_PerceptionGoldensUnchanged: without a listening check the perception block has no diction or adherence key, and the
// key list is the L3 one (the golden-bearing tests in this package pin the rest).
func TestContext_PerceptionGoldensUnchanged(t *testing.T) {
	t.Parallel()
	e, fx := newEarsEnv(t)
	_, raw := perceptionOf(t, e, fx.LoopTrack)
	for _, k := range keys(t, raw) {
		if k == "diction" || k == "adherence" {
			t.Errorf("perception has %q without Tier B data", k)
		}
	}
}

// TestContext_TierBBlocks: with a listening check, perception.diction and perception.adherence follow energy in the key order, hold
// at most 600 characters each, and carry the numbers of the stored rows.
func TestContext_TierBBlocks(t *testing.T) {
	t.Parallel()
	e, fx := newEarsEnv(t)
	seedTierB(t, e, true)
	got, raw := perceptionOf(t, e, fx.LoopTrack)
	if got.Tier != "B" {
		t.Errorf("tier = %q, want B", got.Tier)
	}
	ks := keys(t, raw)
	if n := len(ks); n < 2 || ks[n-2] != "diction" || ks[n-1] != "adherence" {
		t.Errorf("key order = %v, want diction and adherence last", ks)
	}
	var blocks struct {
		Diction   json.RawMessage `json:"diction"`
		Adherence json.RawMessage `json:"adherence"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]json.RawMessage{"diction": blocks.Diction, "adherence": blocks.Adherence} {
		if len(b) == 0 || len(b) > 600 {
			t.Errorf("%s block is %d characters", name, len(b))
		}
	}
	var d map[string]any
	_ = json.Unmarshal(blocks.Diction, &d)
	want := map[string]any{"cer": 0.2, "wer": 0.25, "worst_section": map[string]any{"label": "chorus", "cer": 0.4}}
	if diff := cmp.Diff(want, d); diff != "" {
		t.Errorf("diction (-want +got):\n%s", diff)
	}
	var a map[string]any
	_ = json.Unmarshal(blocks.Adherence, &a)
	wantA := map[string]any{"melody_chroma": 0.82, "transpose": float64(0), "tempo_ratio": 1.04, "key_relation": "relative", "basis": "coarse"}
	if diff := cmp.Diff(wantA, a); diff != "" {
		t.Errorf("adherence (-want +got):\n%s", diff)
	}
	// without the adherence row only the diction block appears
	e2, fx2 := newEarsEnv(t)
	seedTierB(t, e2, false)
	if _, raw := perceptionOf(t, e2, fx2.LoopTrack); len(keys(t, raw)) == 0 || !slices.Contains(keys(t, raw), "diction") || slices.Contains(keys(t, raw), "adherence") {
		t.Errorf("keys without adherence = %v", keys(t, raw))
	}
}
