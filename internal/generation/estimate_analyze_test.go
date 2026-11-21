package generation_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobspec"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

func asProblem(err error) (*problem.Error, bool) {
	var pe *problem.Error
	ok := errors.As(err, &pe)
	return pe, ok
}

func analyzeItem(ids ...string) generation.Item {
	return generation.Item{Mode: lineage.ModeAnalyze, TrackIDs: ids}
}

func (r *rig) estimateErr(in generation.EstimateIn) error {
	r.t.Helper()
	_, err := r.svc.Estimate(context.Background(), human, in)
	if err == nil {
		r.t.Fatalf("estimate %+v was accepted", in.Items)
	}
	return err
}

// wantRule asserts a problem with the status and code and, when rule is not empty, its details.rule.
func wantRule(t *testing.T, err error, status int, code, rule string) {
	t.Helper()
	pe := wantProblem(t, err, status, code)
	if rule != "" && pe.Details["rule"] != rule {
		t.Errorf("rule = %v, want %s (%v)", pe.Details["rule"], rule, pe.Details)
	}
}

// longTrack makes a fixture take 700 s long: tracks.duration_s is frozen by a trigger, which the test drops.
func (r *rig) longTrack(id string) {
	r.exec(`DROP TRIGGER tracks_frozen`)
	r.exec(`UPDATE tracks SET duration_s = 700 WHERE id = ?`, id)
}

// TestEstimate_AnalyzeRules pins every rule of C340.1 and C340.2: the item shape, the batch shape, and the per-track checks.
func TestEstimate_AnalyzeRules(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	r.longTrack(lt.TrackTwinB)
	r.exec(`INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?, 'analysis.json', ?, 10, 'takes/x/analysis.json', ?)`,
		lt.TrackTwinA, strings.Repeat("a", 64), "2026-10-01T12:00:00Z")
	ptr := func(s string) *string { return &s }
	notYue := "acestep15"

	t.Run("item shape", func(t *testing.T) {
		for name, it := range map[string]generation.Item{
			"parent":   {Mode: lineage.ModeAnalyze, TrackIDs: []string{lt.TrackLuna}, ParentTrackID: ptr(lt.TrackLuna)},
			"n_seeds":  {Mode: lineage.ModeAnalyze, TrackIDs: []string{lt.TrackLuna}, NSeeds: 2},
			"seeds":    {Mode: lineage.ModeAnalyze, TrackIDs: []string{lt.TrackLuna}, Seeds: []int64{1}},
			"title":    {Mode: lineage.ModeAnalyze, TrackIDs: []string{lt.TrackLuna}, Title: ptr("x")},
			"recipe":   {Mode: lineage.ModeAnalyze, TrackIDs: []string{lt.TrackLuna}, Recipe: []byte(`{}`)},
			"patch":    {Mode: lineage.ModeAnalyze, TrackIDs: []string{lt.TrackLuna}, Patch: &generation.Patch{}},
			"draft":    {Mode: lineage.ModeAnalyze, TrackIDs: []string{lt.TrackLuna}, DraftIDs: []string{"d"}},
			"template": {Mode: lineage.ModeAnalyze, TrackIDs: []string{lt.TrackLuna}, TemplateMode: lineage.ModeNewSeed},
		} {
			wantRule(t, r.estimateErr(generation.EstimateIn{Items: []generation.Item{it}}), http.StatusUnprocessableEntity, "INVALID_ITEM", "analyze_fields")
			_ = name
		}
		wantRule(t, r.estimateErr(generation.EstimateIn{Items: []generation.Item{analyzeItem()}}), http.StatusUnprocessableEntity, "INVALID_ITEM", "track_ids_required")
		many := make([]string, 51)
		for i := range many {
			many[i] = strings.Repeat("x", i+1)
		}
		wantRule(t, r.estimateErr(generation.EstimateIn{Items: []generation.Item{analyzeItem(many...)}}), http.StatusUnprocessableEntity, "INVALID_ITEM", "track_ids_at_most_50")
		wantRule(t, r.estimateErr(generation.EstimateIn{Items: []generation.Item{analyzeItem(lt.TrackLuna, lt.TrackLuna)}}), http.StatusUnprocessableEntity, "INVALID_ITEM", "duplicate")
		wantRule(t, r.estimateErr(generation.EstimateIn{Items: []generation.Item{analyzeItem("")}}), http.StatusUnprocessableEntity, "INVALID_ITEM", "empty_id")
	})

	t.Run("track_ids belongs to analyze", func(t *testing.T) {
		it := generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: ptr(lt.TrackLuna), TrackIDs: []string{lt.TrackLuna}}
		wantRule(t, r.estimateErr(generation.EstimateIn{Items: []generation.Item{it}}), http.StatusUnprocessableEntity, "INVALID_ITEM", "not_allowed_for_new_seed")
	})

	t.Run("analyze alone", func(t *testing.T) {
		for name, in := range map[string]generation.EstimateIn{
			"mixed with another mode": {Items: []generation.Item{analyzeItem(lt.TrackLuna), newSeedItem(lt.TrackLuna, 1)}},
			"with tier_b true":        {Items: []generation.Item{analyzeItem(lt.TrackLuna)}, TierB: tbool(true)},
			"with tier_b false":       {Items: []generation.Item{analyzeItem(lt.TrackLuna)}, TierB: tbool(false)},
			"with a trial":            {Items: []generation.Item{analyzeItem(lt.TrackLuna)}, TrialID: "trial-1"},
			"other engine (batch)":    {Items: []generation.Item{analyzeItem(lt.TrackLuna)}, Engine: notYue},
			"other engine (item)":     {Items: []generation.Item{{Mode: lineage.ModeAnalyze, TrackIDs: []string{lt.TrackLuna}, Engine: notYue}}},
		} {
			err := r.estimateErr(in)
			t.Run(name, func(t *testing.T) {
				wantRule(t, err, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "analyze_alone")
			})
		}
	})

	t.Run("per track", func(t *testing.T) {
		wantRule(t, r.estimateErr(generation.EstimateIn{Items: []generation.Item{analyzeItem("01900000-0000-7000-8000-00000000ffff")}}), http.StatusUnprocessableEntity, "UNKNOWN_TRACK", "")
		wantRule(t, r.estimateErr(generation.EstimateIn{Items: []generation.Item{analyzeItem(lt.TrackTwinB)}}), http.StatusUnprocessableEntity, "INVALID_ITEM", "audio_too_long")
		wantRule(t, r.estimateErr(generation.EstimateIn{Items: []generation.Item{analyzeItem(lt.TrackLuna, lt.TrackTwinA)}}), http.StatusUnprocessableEntity, "INVALID_ITEM", "already_analyzed")
	})

	t.Run("references, lossy masters and lyric-less takes are allowed with their warnings", func(t *testing.T) {
		e := r.estWith(human, nil, analyzeItem(lt.TrackRefFlac, lt.TrackLossy, lt.TrackLuna))
		if e.Takes != 3 || len(e.Items[0].Takes) != 3 {
			t.Fatalf("takes = %d, items %+v", e.Takes, e.Items)
		}
		want := map[int]string{1: "lossy_master"}
		for _, w := range e.Warnings {
			if w.Severity != "warning" {
				t.Errorf("warning %+v is not a warning", w)
			}
		}
		for idx, code := range want {
			found := false
			for _, w := range e.Warnings {
				found = found || (w.TakeIndex == idx && w.Code == code)
			}
			if !found {
				t.Errorf("take %d has no %s warning: %+v", idx, code, e.Warnings)
			}
		}
		// the reference's stub recipe has no lyric line: the transcript still serves Recover
		if !estHasWarning(e, "no_lyrics") {
			t.Errorf("no no_lyrics warning: %+v", e.Warnings)
		}
	})
}

// TestEstimate_AnalyzeQuoteAndSpec: the take of an analyze estimate is priced as fetch plus listening check (C340.4), carries the frozen
// pin of the master and the Ears block, needs [analyze ears v2], and create writes a job of kind analyze on the take's own recipe with
// no mutation and no new track.
func TestEstimate_AnalyzeQuoteAndSpec(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	e := r.estWith(human, nil, analyzeItem(lt.TrackLuna))
	tp := firstTake(e)
	if tp.TierB || tp.Operator != "analyze" || tp.ParentTrackID == nil || *tp.ParentTrackID != lt.TrackLuna || !strings.HasPrefix(tp.Title, "Listening check: ") {
		t.Errorf("take plan = %+v", tp)
	}
	earsS := spend.EarsExpectedS(12, 40, 150, 40, true) // Luna has a plan score: the pitch stage runs (D-EARS-4)
	budget := spend.EarsBudgetS(earsS)
	qp := spend.QuoteParams{PricePPH: 690_000, ColdStartS: 180, IdleS: 20, WorkersMax: 2, MaxRuntimeS: 900, KillSlackS: 15, RTFMilli: spend.DefaultRTFMilli}
	want := spend.QuoteTakes(qp, []spend.TakeIn{{AudioS: 0, LoadS: 20, EarsS: earsS}}).Takes[0]
	if tp.TimeoutS != want.TimeoutS || int64(tp.Max) != int64(want.MaxMicro) {
		t.Errorf("take timeout %d max %d, want %d and %d (fetch 20 s, ears_s %d, budget %d)", tp.TimeoutS, tp.Max, want.TimeoutS, want.MaxMicro, earsS, budget)
	}
	it := r.itemsJSON(e)[0]
	if got, _ := json.Marshal(it["needs"]); string(got) != `["analyze","ears","v2"]` {
		t.Errorf("needs = %s", got)
	}
	raw, _ := json.Marshal(it["spec"])
	spec, err := jobspec.Parse(raw)
	if err != nil || spec.Inputs == nil || spec.Inputs.SrcAudio == nil || spec.Ears == nil {
		t.Fatalf("spec = %s (%v)", raw, err)
	}
	pin := spec.Inputs.SrcAudio
	if pin.TrackID != lt.TrackLuna || pin.MasterSHA256 != r.str(`SELECT master_sha256 FROM tracks WHERE id = ?`, lt.TrackLuna) ||
		pin.Bytes != r.val(`SELECT master_bytes FROM tracks WHERE id = ?`, lt.TrackLuna) {
		t.Errorf("pin = %+v", pin)
	}
	if spec.Ears.BudgetS != budget || spec.Ears.KeepVocals || !slices.Equal(spec.Ears.Stages, []string{"separate", "asr", "transcribe"}) || spec.Ears.Lang != "" {
		t.Errorf("ears = %+v", spec.Ears)
	}

	tracks, muts := r.count(`SELECT COUNT(*) FROM tracks`), r.count(`SELECT COUNT(*) FROM mutations`)
	c := r.mustCreate(human, e, spend.ViaHumanCreate)
	var kind, needs, operator, recipe, source string
	var timeout int
	if err := r.st.R.QueryRow(`SELECT kind, needs_json, operator, recipe_id, source_track_id, timeout_s FROM jobs WHERE batch_id = ?`, c.BatchID).Scan(&kind, &needs, &operator, &recipe, &source, &timeout); err != nil {
		t.Fatal(err)
	}
	if kind != "analyze" || needs != `["analyze","ears","v2"]` || operator != "analyze" || source != lt.TrackLuna || timeout != tp.TimeoutS ||
		recipe != r.str(`SELECT recipe_id FROM tracks WHERE id = ?`, lt.TrackLuna) {
		t.Errorf("job = %s %s %s %s %s %d", kind, needs, operator, recipe, source, timeout)
	}
	if r.count(`SELECT COUNT(*) FROM tracks`) != tracks || r.count(`SELECT COUNT(*) FROM mutations`) != muts {
		t.Error("creating an analyze batch wrote a track or a mutation")
	}
	if r.count(`SELECT COUNT(*) FROM recipes WHERE id = ?`, recipe) != 1 {
		t.Error("the job's recipe is not the take's")
	}
	if got := r.val(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ? AND kind = 'reserve'`, c.BatchID); got != int64(want.TakeMicro) {
		t.Errorf("reserve = %d, want the take's quote %d", got, want.TakeMicro)
	}
}

// TestEstimate_AnalyzeClosedCapabilityRefused: with the capabilities closed (production today) an analyze estimate is 503
// GENERATION_DISABLED worker_outdated, and nothing is stored or reserved; each missing capability is named.
func TestEstimate_AnalyzeClosedCapabilityRefused(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	estimates, ledger, jobs := r.count(`SELECT COUNT(*) FROM estimates`), r.count(`SELECT COUNT(*) FROM ledger`), r.count(`SELECT COUNT(*) FROM jobs`)
	for _, c := range []struct {
		open    []string
		missing []string
	}{
		{nil, []string{"analyze", "ears", "v2"}},
		{[]string{"v2"}, []string{"analyze", "ears"}},
		{[]string{"v2", "ears"}, []string{"analyze"}},
	} {
		r.setCaps(c.open...)
		err := r.estimateErr(generation.EstimateIn{Items: []generation.Item{analyzeItem(lt.TrackLuna)}, Via: spend.ViaHumanCreate})
		wantRule(t, err, http.StatusServiceUnavailable, "GENERATION_DISABLED", "")
		pe, _ := asProblem(err)
		if pe.Details["reason"] != "worker_outdated" {
			t.Errorf("open %v: details = %v", c.open, pe.Details)
		}
		got, _ := pe.Details["missing"].([]string)
		if !slices.Equal(got, c.missing) {
			t.Errorf("open %v: missing %v, want %v", c.open, got, c.missing)
		}
	}
	if r.count(`SELECT COUNT(*) FROM estimates`) != estimates || r.count(`SELECT COUNT(*) FROM ledger`) != ledger || r.count(`SELECT COUNT(*) FROM jobs`) != jobs {
		t.Error("a refused analyze estimate stored an estimate, reserved money or created a job")
	}
	// and a create whose capability closed since the estimate is refused too, the token unspent
	r.setCaps("analyze", "ears", "v2")
	e := r.estWith(human, nil, analyzeItem(lt.TrackLuna))
	r.setCaps()
	if _, err := r.create(human, e, spend.ViaHumanCreate); err == nil {
		t.Fatal("create with a closed capability succeeded")
	} else {
		wantRule(t, err, http.StatusServiceUnavailable, "GENERATION_DISABLED", "")
	}
	if r.count(`SELECT COUNT(*) FROM ledger`) != ledger || r.count(`SELECT COUNT(*) FROM jobs`) != jobs {
		t.Error("a refused analyze create reserved money or created a job")
	}
}

// TestCreate_AnalyzeInProgressRechecked: two estimates for the same take are both fine, but once the first is created the second is
// 409 ANALYSIS_IN_PROGRESS {track_ids}, re-checked inside the create transaction (C340.2), and so is a fresh estimate.
func TestCreate_AnalyzeInProgressRechecked(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	first := r.estWith(human, nil, analyzeItem(lt.TrackLuna, lt.TrackCitta))
	second := r.estWith(human, nil, analyzeItem(lt.TrackLuna))
	r.mustCreate(human, first, spend.ViaHumanCreate)
	ledger := r.count(`SELECT COUNT(*) FROM ledger`)
	_, err := r.create(human, second, spend.ViaHumanCreate)
	wantRule(t, err, http.StatusConflict, "ANALYSIS_IN_PROGRESS", "")
	if pe, _ := asProblem(err); pe == nil || !slices.Contains(toStrings(pe.Details["track_ids"]), lt.TrackLuna) {
		t.Errorf("details = %v", pe)
	}
	if r.count(`SELECT COUNT(*) FROM ledger`) != ledger {
		t.Error("the refused create reserved money")
	}
	wantRule(t, r.estimateErr(generation.EstimateIn{Items: []generation.Item{analyzeItem(lt.TrackCitta)}}), http.StatusConflict, "ANALYSIS_IN_PROGRESS", "")
	// a finished (cancelled) job frees the take again
	r.exec(`UPDATE jobs SET state = 'cancelled', finished_at = '2026-10-02T12:00:00Z' WHERE kind = 'analyze'`)
	r.estWith(human, nil, analyzeItem(lt.TrackLuna))
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// TestEstimate_AnalyzeEditorAwaitsOwner: an Editor's analyze batch always waits for the Owner (D-EARS-12, U1 editor_batch), whatever the
// auto-approve limits are: analyze is an ordinary spend, never an auto-approved one.
func TestEstimate_AnalyzeEditorAwaitsOwner(t *testing.T) {
	t.Parallel()
	caps := spend.DefaultCaps
	caps.AutoApproveTakes, caps.AutoApproveMicro = 50, 1_000_000_000
	r := newRig(t, withCaps(caps))
	r.svc.UseEarsQuote(earsCfg)
	e, err := r.svc.Estimate(context.Background(), bobEditor, generation.EstimateIn{Items: []generation.Item{analyzeItem(lt.TrackLuna)}, Via: spend.ViaAPI})
	if err != nil {
		t.Fatal(err)
	}
	if !e.RequiresApproval || !contains(e.ApprovalReasons, "editor_batch") {
		t.Errorf("requires %v reasons %v", e.RequiresApproval, e.ApprovalReasons)
	}
	c := r.mustCreate(bobEditor, e, spend.ViaAPI)
	if c.State != "awaiting_confirmation" || c.ApprovalState != "pending" {
		t.Errorf("state %s approval %s", c.State, c.ApprovalState)
	}
}

// TestEstimate_AnalyzeNeverBackfills: nothing in the estimate or create path creates analyze work by itself; an estimate that is never
// created leaves no job (D-EARS-OP-5).
func TestEstimate_AnalyzeNeverBackfills(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	r.estWith(human, nil, analyzeItem(lt.TrackLuna))
	if r.count(`SELECT COUNT(*) FROM jobs WHERE kind = 'analyze'`) != 0 {
		t.Error("an estimate created an analyze job")
	}
	// an ordinary render batch never turns into analyze work
	r.est(human, newSeedItem(lt.TrackLuna, 1))
	if r.count(`SELECT COUNT(*) FROM jobs WHERE kind = 'analyze'`) != 0 {
		t.Error("a render batch created an analyze job")
	}
}

// TestEstimate_AnalyzeProductionStateRefused: production today is a RunPod backend whose contract log has no open row at all. An analyze
// estimate is refused by requireCaps (503 GENERATION_DISABLED worker_outdated, missing analyze, ears and v2) before anything is
// stored or reserved, and the create of a token minted earlier (none can be) has nothing to spend.
func TestEstimate_AnalyzeProductionStateRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.runpodService(r.goodQuote(), true, true)
	svc.UseEarsQuote(earsCfg)
	estimates, ledger, jobs := r.count(`SELECT COUNT(*) FROM estimates`), r.count(`SELECT COUNT(*) FROM ledger`), r.count(`SELECT COUNT(*) FROM jobs`)
	_, err := svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{analyzeItem(lt.TrackLuna)}, Via: spend.ViaHumanCreate})
	pe := wantProblem(t, err, http.StatusServiceUnavailable, "GENERATION_DISABLED")
	if pe.Details["reason"] != generation.ReasonWorkerOutdated || pe.Details["backend"] != provider.BackendRunPod {
		t.Fatalf("details = %v", pe.Details)
	}
	if got, _ := pe.Details["missing"].([]string); !slices.Equal(got, []string{"analyze", "ears", "v2"}) {
		t.Errorf("missing = %v", pe.Details["missing"])
	}
	if r.count(`SELECT COUNT(*) FROM estimates`) != estimates || r.count(`SELECT COUNT(*) FROM ledger`) != ledger || r.count(`SELECT COUNT(*) FROM jobs`) != jobs {
		t.Error("the refused estimate stored an estimate, reserved money or created a job")
	}
	// a plain render of the same take still prices: the closed gate refuses only what needs the capabilities
	if _, err := svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{newSeedItem(lt.TrackLuna, 1)}, Via: spend.ViaHumanCreate, TierB: tbool(false)}); err != nil {
		t.Errorf("a plain render was refused: %v", err)
	}
}
