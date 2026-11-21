package generation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobspec"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

var earsCfg = generation.EarsOptions{LoadS: 40, RTFMilli: 150, TranscribeRTFMilli: 40, Language: yue2.DetectLanguage, LyricLines: nil}

func tbool(b bool) *bool { return &b }

func earsRecipe(id, lyrics, cot string, extra map[string]any) generation.Item {
	m := map[string]any{"id": id, "style": "indie pop, 100 bpm, female vocal", "lyrics": lyrics, "cot": cot, "seed": 4000}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return generation.Item{Mode: lineage.ModeComposeNew, Recipe: b}
}

const (
	enLyrics = "[Verse 1]\nline one\nline two\n[Chorus]\nline three\nline four"
	itLyrics = "[Verse 1]\nla chiave del mare non è nella casa\nsotto il cielo di sera che non ha una luna\n[Chorus]\nsono io che canto per te e con te\nnon so se è un sogno o una cosa che ho"
)

// earsRig is a rig whose service has the Ears quote seams wired (as internal/app does).
func earsRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.svc.UseEarsQuote(earsCfg)
	return r
}

func (r *rig) estWith(p auth.Principal, tierB *bool, items ...generation.Item) generation.Estimate {
	r.t.Helper()
	e, err := r.svc.Estimate(context.Background(), p, generation.EstimateIn{Items: items, Via: viaFor(p), TierB: tierB})
	if err != nil {
		r.t.Fatalf("estimate: %v", err)
	}
	return e
}

func firstTake(e generation.Estimate) generation.TakePlan { return e.Items[0].Takes[0] }

func estHasWarning(e generation.Estimate, code string) bool {
	for _, w := range e.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func (r *rig) itemsJSON(e generation.Estimate) []map[string]any {
	r.t.Helper()
	var out []map[string]any
	if err := json.Unmarshal([]byte(r.str(`SELECT items_json FROM estimates WHERE id = ?`, e.ID)), &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

// The default is on: a lyric render gets a listening check whose budget is added to the timeout and to the reservation.
func TestEstimate_TierBDefaultOnForRenders(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	item := earsRecipe("ears-a", enLyrics, "melody", nil)
	plain := r.estWith(human, tbool(false), item)
	e := r.est(human, item) // tier_b absent = auto
	tp, pp := firstTake(e), firstTake(plain)
	if !tp.TierB || pp.TierB {
		t.Fatalf("tier_b = %v (auto), %v (false)", tp.TierB, pp.TierB)
	}
	earsS := spend.EarsExpectedS(tp.ExpectedAudioS, 40, 150, 40, false)
	budget := spend.EarsBudgetS(earsS)
	if tp.TimeoutS != pp.TimeoutS+budget {
		t.Errorf("timeout %d, want the /1 timeout %d + the Tier B budget %d", tp.TimeoutS, pp.TimeoutS, budget)
	}
	if tp.Max <= pp.Max || e.Cost.Ceiling <= plain.Cost.Ceiling || e.Cost.Expected <= plain.Cost.Expected {
		t.Errorf("the money does not cover the listening check: max %d vs %d, ceiling %d vs %d", tp.Max, pp.Max, e.Cost.Ceiling, plain.Cost.Ceiling)
	}
	// the stored take: spec and needs
	it := r.itemsJSON(e)[0]
	if got, _ := json.Marshal(it["needs"]); string(got) != `["ears","v2"]` {
		t.Errorf("needs = %s", got)
	}
	var spec jobspec.Spec
	raw, _ := json.Marshal(it["spec"])
	spec, err := jobspec.Parse(raw)
	if err != nil || spec.Ears == nil {
		t.Fatalf("spec = %s (%v)", raw, err)
	}
	if spec.Ears.BudgetS != budget || spec.Ears.KeepVocals || spec.Ears.Lang != "" || strings.Join(spec.Ears.Stages, ",") != "separate,asr" {
		t.Errorf("ears spec = %+v", spec.Ears)
	}
	// create: the job's timeout and the reservation include the budget (they fail without the change)
	c := r.mustCreate(human, e, spend.ViaHumanCreate)
	if got := r.val(`SELECT timeout_s FROM jobs WHERE batch_id = ?`, c.BatchID); got != int64(tp.TimeoutS) {
		t.Errorf("jobs.timeout_s = %d, want %d", got, tp.TimeoutS)
	}
	wantReserve := money.ForSeconds(int64(tp.TimeoutS+15), 690_000)
	if got := r.val(`SELECT reserved_micro_usd FROM jobs WHERE batch_id = ?`, c.BatchID); got != int64(wantReserve) {
		t.Errorf("jobs.reserved_micro_usd = %d, want the timeout's worth %d", got, wantReserve)
	}
	if got := r.val(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ? AND kind = 'reserve'`, c.BatchID); got != int64(wantReserve) {
		t.Errorf("ledger reserve = %d, want %d", got, wantReserve)
	}
	if got := r.str(`SELECT needs_json FROM jobs WHERE batch_id = ?`, c.BatchID); got != `["ears","v2"]` {
		t.Errorf("job needs = %s", got)
	}
}

func TestEstimate_TierBAutoOffWhenCapClosed(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	item := earsRecipe("ears-b", enLyrics, "melody", nil)
	want := r.estWith(human, tbool(false), item)
	for _, closed := range [][]string{{}, {"v2"}, {"ears"}} {
		r.setCaps(closed...)
		e := r.est(human, item)
		if firstTake(e).TierB {
			t.Errorf("caps %v: tier_b on", closed)
		}
		if !estHasWarning(e, "tier_b_unavailable") {
			t.Errorf("caps %v: no tier_b_unavailable warning: %+v", closed, e.Warnings)
		}
		if firstTake(e).TimeoutS != firstTake(want).TimeoutS || e.Cost.Ceiling != want.Cost.Ceiling {
			t.Errorf("caps %v: the quote is not the plain one", closed)
		}
		if strings.Contains(r.str(`SELECT items_json FROM estimates WHERE id = ?`, e.ID), `"needs"`) {
			t.Errorf("caps %v: a plain take carries needs", closed)
		}
		c := r.mustCreate(human, e, spend.ViaHumanCreate)
		if got := r.str(`SELECT needs_json || spec_json FROM jobs WHERE batch_id = ?`, c.BatchID); got != `[]{}` {
			t.Errorf("caps %v: plain job needs+spec = %s", closed, got)
		}
	}
	r.setCaps("ears", "v2")
	if !firstTake(r.est(human, item)).TierB {
		t.Error("open caps: tier_b off")
	}
}

func TestEstimate_TierBExplicitClosedCap503(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	item := earsRecipe("ears-c", enLyrics, "melody", nil)
	r.setCaps() // production today: no capability is open
	_, err := r.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{item}, Via: spend.ViaHumanCreate, TierB: tbool(true)})
	wantWorkerOutdated(t, err, "ears", "v2")
	// create re-checks: open, estimate, close, create
	r.setCaps("ears", "v2")
	e := r.estWith(human, tbool(true), item)
	r.setCaps("v2")
	_, err = r.create(human, e, spend.ViaHumanCreate)
	wantWorkerOutdated(t, err, "ears")
	if n := r.count(`SELECT COUNT(*) FROM estimates WHERE id = ? AND consumed_at IS NOT NULL`, e.ID); n != 0 {
		t.Error("the token was consumed by a refusal")
	}
}

// A plain take is quoted and stored exactly as before: with the seams unwired, with tier_b false, and for takes that cannot have a check.
func TestEstimate_TierBOff(t *testing.T) {
	t.Parallel()
	plainRig := newRig(t) // seams never wired
	r := earsRig(t)
	for name, item := range map[string]generation.Item{
		"lyrics": earsRecipe("ears-d", enLyrics, "melody", nil), "full": earsRecipe("ears-d2", enLyrics, "full", nil), "seeds": newSeedItem(lt.TrackLuna, 2),
	} {
		want := plainRig.est(human, item)
		for label, e := range map[string]generation.Estimate{"false": r.estWith(human, tbool(false), item), "unwired": plainRig.est(human, item)} {
			if diff := cmp.Diff(want, e, cmpopts.IgnoreFields(generation.Estimate{}, "ID", "Token", "ExpiresAt"), cmpopts.IgnoreFields(generation.TakePlan{}, "RequestID"),
				cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s/%s: estimate differs from the plain one: %s", name, label, diff)
			}
			if got := plainRig.str(`SELECT items_json FROM estimates WHERE id = ?`, want.ID); strings.Contains(got, `"ears"`) || strings.Contains(got, `"needs"`) || strings.Contains(got, `"spec"`) {
				t.Errorf("%s: plain items_json has Tier B keys: %.200s", name, got)
			}
		}
		if firstTake(r.estWith(human, tbool(false), item)).TierB {
			t.Errorf("%s: tier_b false is on", name)
		}
	}
	// the stored plain items are byte-identical between an unwired service and tier_b false
	item := earsRecipe("ears-d", enLyrics, "melody", nil)
	a, b := plainRig.est(human, item), r.estWith(human, tbool(false), item)
	norm := func(rr *rig, e generation.Estimate) string {
		return rr.str(`SELECT items_json || quote_json || max_micro_usd || '|' || ceiling_micro_usd || '|' || expected_micro_usd FROM estimates WHERE id = ?`, e.ID)
	}
	if norm(plainRig, a) != norm(r, b) {
		t.Errorf("stored estimates differ:\n%s\n%s", norm(plainRig, a), norm(r, b))
	}
}

func TestEstimate_TierBNoLyricLines(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	for _, tb := range []*bool{nil, tbool(true)} {
		e := r.estWith(human, tb, earsRecipe("ears-e", "[Intro]\n[Outro]\n", "melody", nil))
		if firstTake(e).TierB || !estHasWarning(e, "tier_b_no_lyrics") {
			t.Errorf("tier_b %v: take on = %v, warnings %+v", tb, firstTake(e).TierB, e.Warnings)
		}
	}
	e := r.estWith(human, tbool(false), earsRecipe("ears-e", "[Intro]\n[Outro]\n", "melody", nil))
	if estHasWarning(e, "tier_b_no_lyrics") {
		t.Error("tier_b false warns")
	}
}

func TestEstimate_TierBRuntimeAutoDropsExplicitRefuses(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	item := earsRecipe("ears-f", enLyrics, "melody", nil)
	base := r.estWith(human, tbool(false), item)
	// a cap that holds the /1 timeout but leaves less room than the budget
	o := r.opts
	o.MaxRuntime = durS(firstTake(base).TimeoutS + 30)
	small := generation.New(o)
	small.UseEarsQuote(earsCfg)
	e, err := small.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{item}, Via: spend.ViaHumanCreate})
	if err != nil {
		t.Fatal(err)
	}
	if firstTake(e).TierB || !estHasWarning(e, "tier_b_exceeds_runtime") || firstTake(e).TimeoutS != firstTake(base).TimeoutS {
		t.Errorf("auto: on %v, warnings %+v, timeout %d", firstTake(e).TierB, e.Warnings, firstTake(e).TimeoutS)
	}
	_, err = small.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{item}, Via: spend.ViaHumanCreate, TierB: tbool(true)})
	pe := wantProblem(t, err, http.StatusUnprocessableEntity, "INVALID_ITEM")
	if pe.Details["rule"] != "tier_b_exceeds_runtime" {
		t.Errorf("details = %v", pe.Details)
	}
	// room for both: on
	o.MaxRuntime = durS(firstTake(base).TimeoutS + 400)
	big := generation.New(o)
	big.UseEarsQuote(earsCfg)
	e, err = big.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{item}, Via: spend.ViaHumanCreate})
	if err != nil || !firstTake(e).TierB {
		t.Errorf("roomy cap: %v on=%v", err, err == nil && firstTake(e).TierB)
	}
}

func TestEstimate_TierBStagesFollowScore(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	for _, c := range []struct {
		name  string
		item  generation.Item
		stage string
	}{
		{"melody", earsRecipe("ears-g1", enLyrics, "melody", nil), "separate,asr"},
		{"off", earsRecipe("ears-g2", enLyrics, "off", nil), "separate,asr"},
		{"full", earsRecipe("ears-g3", enLyrics, "full", nil), "separate,asr,transcribe"},
		{"abc", earsRecipe("ears-g4", enLyrics, "melody", map[string]any{"abc": "X:1\nK:C\nCDEF|"}), "separate,asr,transcribe"},
	} {
		e := r.est(human, c.item)
		raw, _ := json.Marshal(r.itemsJSON(e)[0]["spec"])
		spec, err := jobspec.Parse(raw)
		if err != nil || spec.Ears == nil {
			t.Fatalf("%s: spec %s: %v", c.name, raw, err)
		}
		if got := strings.Join(spec.Ears.Stages, ","); got != c.stage {
			t.Errorf("%s: stages %s, want %s", c.name, got, c.stage)
		}
		earsS := spend.EarsExpectedS(firstTake(e).ExpectedAudioS, 40, 150, 40, strings.Contains(c.stage, "transcribe"))
		if spec.Ears.BudgetS != spend.EarsBudgetS(earsS) {
			t.Errorf("%s: budget %d, want %d", c.name, spec.Ears.BudgetS, spend.EarsBudgetS(earsS))
		}
	}
}

func TestEstimate_TierBLanguageForced(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	for lyrics, want := range map[string]string{itLyrics: "it", enLyrics: ""} {
		e := r.est(human, earsRecipe("ears-h", lyrics, "melody", nil))
		raw, _ := json.Marshal(r.itemsJSON(e)[0]["spec"])
		spec, err := jobspec.Parse(raw)
		if err != nil || spec.Ears == nil || spec.Ears.Lang != want {
			t.Errorf("lang = %+v (%v), want %q", spec.Ears, err, want)
		}
	}
}

// A canary render stays plain under auto; an explicit tier_b (the tier-b canary case) is honoured.
func TestCanary_MainStaysPlain(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	for _, id := range []string{"mm-canary-20261121-main-1", "mm-canary-20261121-cot-full-2", "mm-canary-20261121-v2-wire-1"} {
		item := earsRecipe(id, enLyrics, "melody", nil)
		e := r.est(human, item)
		if firstTake(e).TierB {
			t.Errorf("%s: tier_b on", id)
		}
		for _, w := range e.Warnings {
			if strings.HasPrefix(w.Code, "tier_b_") {
				t.Errorf("%s: warning %s", id, w.Code)
			}
		}
	}
	if !firstTake(r.estWith(human, tbool(true), earsRecipe("mm-canary-20261121-tier-b-1", enLyrics, "melody", nil))).TierB {
		t.Error("explicit tier_b on a canary id is off")
	}
}

// The variant path has no options: it gets auto (the zero EstimateIn.TierB) and gives the same answer as an explicit estimate.
func TestEstimateVariant_TierBAuto(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	e := r.est(human, newSeedItem(lt.TrackLuna, 2))
	for _, tp := range e.Items[0].Takes {
		if !tp.TierB {
			t.Errorf("variant take %d: tier_b off", tp.TakeIndex)
		}
	}
	c := r.mustCreate(human, e, spend.ViaHumanCreate)
	if got := r.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND needs_json = '["ears","v2"]'`, c.BatchID); got != 2 {
		t.Errorf("%d of 2 jobs carry the Tier B needs", got)
	}
}

func TestCreate_TierBSpecFrozen(t *testing.T) {
	t.Parallel()
	r := earsRig(t)
	e := r.est(human, earsRecipe("ears-i", itLyrics, "full", nil), earsRecipe("ears-i2", enLyrics, "melody", nil))
	stored := r.itemsJSON(e)
	c := r.mustCreate(human, e, spend.ViaHumanCreate)
	rows, err := r.st.R.Query(`SELECT take_index, needs_json, spec_json, timeout_s, reserved_micro_usd FROM jobs WHERE batch_id = ? ORDER BY take_index`, c.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var idx int
		var needs, spec string
		var to int
		var resv int64
		if err := rows.Scan(&idx, &needs, &spec, &to, &resv); err != nil {
			t.Fatal(err)
		}
		sn, _ := json.Marshal(stored[idx]["needs"])
		want, err := jobspec.Parse(mustJSON(t, stored[idx]["spec"]))
		if err != nil {
			t.Fatal(err)
		}
		got, err := jobspec.Parse([]byte(spec))
		if err != nil || cmp.Diff(want, got) != "" || needs != string(sn) {
			t.Errorf("take %d: job needs %s spec %s, estimate needs %s spec %+v (%v)", idx, needs, spec, sn, want, err)
		}
		tp := e.Items[idx].Takes[0]
		if got.Ears == nil || to != tp.TimeoutS {
			t.Errorf("take %d: ears %+v timeout %d, estimate %d", idx, got.Ears, to, tp.TimeoutS)
		}
		if want := int64(money.ForSeconds(int64(tp.TimeoutS+15), 690_000)); resv != want {
			t.Errorf("take %d: reserved %d, want %d", idx, resv, want)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("%d jobs", n)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func durS(s int) time.Duration { return time.Duration(s) * time.Second }

// Tier B exists for yue2 only: an ACE-Step batch gets a warning under auto and a 422 for an explicit true.
func TestEstimate_TierBNotForACE(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	x.svc.UseEarsQuote(earsCfg)
	item := composeACE(t, "ace-tb-1", nil)
	e, err := x.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{item}, Via: spend.ViaHumanCreate})
	if err != nil {
		t.Fatal(err)
	}
	if firstTake(e).TierB || !estHasWarning(e, "tier_b_engine") {
		t.Errorf("auto: on %v, warnings %+v", firstTake(e).TierB, e.Warnings)
	}
	_, err = x.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{item}, Via: spend.ViaHumanCreate, TierB: tbool(true)})
	pe := wantProblem(t, err, http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	if pe.Details["field"] != "options.tier_b" {
		t.Errorf("details = %v", pe.Details)
	}
	e, err = x.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{item}, Via: spend.ViaHumanCreate, TierB: tbool(false)})
	if err != nil || estHasWarning(e, "tier_b_engine") {
		t.Errorf("false: %v %+v", err, e.Warnings)
	}
}
