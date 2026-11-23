package nightshift

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// withEars wires the Ears quote seams into the rig's generation service, as internal/app does: renders with lyric lines then get a
// listening check by default on a backend whose capabilities are open.
func (g *rig) withEars() *rig {
	g.gen.UseEarsQuote(generation.EarsOptions{LoadS: 40, RTFMilli: 150, TranscribeRTFMilli: 40})
	return g
}

// closeCaps appends a CLI row to the fake backend's contract log: no capability is open.
func (g *rig) closeCaps() {
	g.t.Helper()
	if err := provider.AppendContract(g.t.Context(), g.st.W, provider.WorkerContractRow{Backend: provider.BackendFake,
		ImageDigest: "sha256:" + strings.Repeat("3", 64), Caps: []string{}, Via: provider.ContractViaCLI, ChangedBy: "system:cli", ChangedAt: g.clock(), Reason: "test"}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) setListening(v string) {
	g.t.Helper()
	if _, err := g.r.UpdateSettings(g.t.Context(), alice, SettingsPatch{ListeningChecks: &v}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) needsOf(batch string) []string {
	g.t.Helper()
	rows, err := g.st.R.QueryContext(g.t.Context(), `SELECT needs_json FROM jobs WHERE batch_id = ? ORDER BY id`, batch)
	if err != nil {
		g.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			g.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// TestTierBOption: the night sends auto (nil) only when the Owner's setting and the mandate both allow it, an explicit false
// otherwise, and never true.
func TestTierBOption(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		setting string
		scope   bool
		auto    bool
	}{
		{ListeningOn, true, true}, {ListeningOn, false, false}, {ListeningOff, true, false}, {ListeningOff, false, false}, {"", true, false},
	} {
		got := TierBOption(c.setting, mandates.Scope{TierB: c.scope})
		switch {
		case c.auto && got != nil:
			t.Errorf("%q/%v: want auto, got %v", c.setting, c.scope, *got)
		case !c.auto && (got == nil || *got):
			t.Errorf("%q/%v: want an explicit false", c.setting, c.scope)
		}
	}
}

// TestNightShift_TierBFollowsSetting: with the capability open, listening_checks on gives the night's takes a listening check
// (recorded in plan_json, carried by the jobs' needs); off prices and creates plain takes.
func TestNightShift_TierBFollowsSetting(t *testing.T) {
	t.Parallel()
	on := newRig(t).withEars()
	on.enable()
	on.grant(2, 4)
	on.r.Run(t.Context(), on.key(on.night))
	run := on.run(on.night)
	if run.State != StateCreated || !strings.Contains(run.PlanJSON, `"tier_b":true`) || !strings.Contains(run.PlanJSON, `"tier_b_takes"`) {
		t.Fatalf("on: run = %+v", run)
	}
	for _, n := range on.needsOf(run.BatchID) {
		if n != `["ears","v2"]` {
			t.Errorf("on: job needs = %s", n)
		}
	}

	off := newRig(t).withEars()
	off.enable()
	off.setListening(ListeningOff)
	off.grant(2, 4)
	off.r.Run(t.Context(), off.key(off.night))
	run = off.run(off.night)
	if run.State != StateCreated || !strings.Contains(run.PlanJSON, `"tier_b":false`) || strings.Contains(run.PlanJSON, "tier_b_takes") || strings.Contains(run.PlanJSON, "tier_b_warnings") {
		t.Fatalf("off: run = %+v", run)
	}
	for _, n := range off.needsOf(run.BatchID) {
		if n != `[]` {
			t.Errorf("off: job needs = %s", n)
		}
	}
}

// TestNightShift_TierBAutoOffWhenCapClosed: a closed capability never refuses the night: the takes are plain, and plan_json says why.
func TestNightShift_TierBAutoOffWhenCapClosed(t *testing.T) {
	t.Parallel()
	g := newRig(t).withEars()
	g.enable()
	g.grant(2, 4)
	g.closeCaps()
	g.r.Run(t.Context(), g.key(g.night))
	run := g.run(g.night)
	if run.State != StateCreated {
		t.Fatalf("run = %+v", run)
	}
	if !strings.Contains(run.PlanJSON, `"tier_b":false`) || !strings.Contains(run.PlanJSON, `"code":"tier_b_unavailable"`) {
		t.Errorf("plan_json lacks the tier_b_unavailable reason: %s", run.PlanJSON)
	}
	for _, n := range g.needsOf(run.BatchID) {
		if n != `[]` {
			t.Errorf("job needs = %s, want plain", n)
		}
	}
}

// TestNightShift_TierBNeedsScope: a mandate that does not allow Tier B never sees a listening check, and never refuses the night with
// the tier_b reason either (the trap of C376.3 before the night was explicit).
func TestNightShift_TierBNeedsScope(t *testing.T) {
	t.Parallel()
	g := newRig(t).withEars()
	g.enable()
	g.grantTier(2, 4, false)
	g.r.Run(t.Context(), g.key(g.night))
	run := g.run(g.night)
	if run.State != StateCreated || strings.Contains(run.PlanJSON, `"tier_b":true`) {
		t.Fatalf("run = %+v", run)
	}
	for _, n := range g.needsOf(run.BatchID) {
		if n != `[]` {
			t.Errorf("job needs = %s, want plain", n)
		}
	}
	pv, err := g.r.Preview(t.Context())
	if err != nil || pv.Quote == nil || pv.Quote.TierB {
		t.Fatalf("preview = %+v %v", pv, err)
	}
}

// TestNightShift_TierBTrimsToCeiling: listening checks cost more per take, so under the same per-night ceiling the night plans no
// more takes than without them, and the committed ceiling never exceeds the mandate's.
func TestNightShift_TierBTrimsToCeiling(t *testing.T) {
	t.Parallel()
	takes := func(listening string) (int, money.MicroUSD, money.MicroUSD) {
		g := newRig(t).withEars()
		g.enable()
		g.setListening(listening)
		m := g.grantCeiling(ceilingOfTwo(g))
		g.r.Run(t.Context(), g.key(g.night))
		run := g.run(g.night)
		if run.State != StateCreated {
			t.Fatalf("%s: run = %+v", listening, run)
		}
		if want := listening == ListeningOn; strings.Contains(run.PlanJSON, `"tier_b":true`) != want {
			t.Fatalf("%s: tier_b not applied as set: %s", listening, run.PlanJSON)
		}
		return g.count(`SELECT n_takes FROM batches WHERE id = ?`, run.BatchID), money.MicroUSD(g.count(`SELECT ceiling_micro_usd FROM batches WHERE id = ?`, run.BatchID)), m.Scope.MaxBatchCeilingMicroUSD
	}
	nOff, cOff, maxOff := takes(ListeningOff)
	nOn, cOn, maxOn := takes(ListeningOn)
	if cOff > maxOff || cOn > maxOn {
		t.Errorf("ceilings %d (off) and %d (on) exceed the mandate's %d and %d", cOff, cOn, maxOff, maxOn)
	}
	if nOn >= nOff || nOn < 1 {
		t.Errorf("takes with checks %d, without %d", nOn, nOff)
	}
}

// ceilingOfTwo is the plain estimate ceiling of two night takes: a per-night ceiling that fits two plain takes but not two takes with
// listening checks, so the trim must drop one when checks are on.
func ceilingOfTwo(g *rig) money.MicroUSD {
	g.t.Helper()
	parent := lt.TrackLuna
	off := false
	pv, err := g.gen.Preview(g.t.Context(), Principal(), generation.EstimateIn{Items: []generation.Item{{Mode: lineage.ModeNewSeed, ParentTrackID: &parent, NSeeds: 2}}, Via: spend.ViaAPI, TierB: &off})
	if err != nil {
		g.t.Fatal(err)
	}
	return pv.Cost.Ceiling
}

// goldenQuote is the golden quote parameters of spend.QuoteTakes (quote_test.go) with the Ears calibration of C339.2.
var goldenQuote = spend.QuoteParams{PricePPH: 690000, RTFMilli: 680, ColdStartS: 180, IdleS: 20, WorkersMax: 2, MaxRuntimeS: 900, KillSlackS: 15}

// TestNightCeiling_GoldenTable pins the quote arithmetic only (the Runner's own take count is pinned by TestNightShift_TierBTrimsToCeiling).
// It recomputes the table of C374 through spend.QuoteTakes and the Ears term: for each per-night ceiling,
// the most 216 s takes whose estimate ceiling fits it, plain and with listening checks. A quote change fails here.
func TestNightCeiling_GoldenTable(t *testing.T) {
	t.Parallel()
	const audio = 216
	earsS := spend.EarsExpectedS(audio, 40, 150, 40, false)
	if earsS != 73 {
		t.Fatalf("ears_s = %d, want 73", earsS)
	}
	fit := func(ceiling money.MicroUSD, tierB bool) (n int, got, expected money.MicroUSD) {
		for k := 1; k <= 40; k++ {
			ts := make([]spend.TakeIn, k)
			for i := range ts {
				ts[i] = spend.TakeIn{AudioS: audio}
				if tierB {
					ts[i].EarsS = earsS
				}
			}
			q := spend.QuoteTakes(goldenQuote, ts)
			if q.CeilingMicro > ceiling {
				break
			}
			n, got, expected = k, q.CeilingMicro, q.ExpectedMicro
		}
		return n, got, expected
	}
	type cell struct {
		n               int
		ceiling, expect money.MicroUSD
	}
	for _, c := range []struct {
		ceiling money.MicroUSD
		plain   cell
		tierB   cell
	}{
		{500_000, cell{2, 419372, 102352}, cell{1, 251852, 84334}},
		{1_000_000, cell{7, 943587, 262397}, cell{5, 881482, 268334}},
		{1_500_000, cell{12, 1467802, 422442}, cell{9, 1385186, 452334}},
		{2_000_000, cell{17, 1992017, 582487}, cell{13, 1888890, 636334}},
	} {
		n, ceil, exp := fit(c.ceiling, false)
		if got := (cell{n, ceil, exp}); got != c.plain {
			t.Errorf("plain %d: %+v, want %+v", c.ceiling, got, c.plain)
		}
		n, ceil, exp = fit(c.ceiling, true)
		if got := (cell{n, ceil, exp}); got != c.tierB {
			t.Errorf("tier b %d: %+v, want %+v", c.ceiling, got, c.tierB)
		}
		if n > c.plain.n || ceil > c.ceiling {
			t.Errorf("%d: tier b takes %d (plain %d) or ceiling %d break the bound", c.ceiling, n, c.plain.n, ceil)
		}
	}
}
