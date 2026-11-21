package generation_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// night is the fixed server-side agent the mandate names (C366.1); it owns no token.
var night = auth.Principal{Kind: auth.KindAgent, ID: "nightshift", Name: "nightshift", Via: "internal", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}}

var _ generation.MandateCover = (*mandates.Service)(nil)

// mrig is a rig with a real mandates service wired through UseMandates.
type mrig struct {
	*rig
	ms *mandates.Service
}

func newMandateRig(t *testing.T, opts ...rigOpt) *mrig {
	t.Helper()
	r := newRig(t, opts...)
	ms := mandates.New(mandates.Options{Store: r.st, Spend: r.sp, Now: r.clk.Now, NewID: r.id})
	r.svc.UseMandates(ms)
	return &mrig{rig: r, ms: ms}
}

// scope is a scope that covers one Luna new_seed batch of a few takes under a ceiling of maxCeiling.
func scope(maxCeiling int64) mandates.Scope {
	return mandates.Scope{Modes: []string{"new_seed", "style_edit"}, Engines: []string{"yue2"}, MinStars: 3, MaxTakesPerLineage: 50, MaxTakesPerBatch: 6,
		MaxBatchCeilingMicroUSD: moneyOf(maxCeiling), MaxBatchesPerDay: 4, TierB: true}
}

// grant creates the night's mandate as the Owner.
func (r *mrig) grant(budget, ceiling int64) mandates.Mandate {
	r.t.Helper()
	m, err := r.ms.Create(context.Background(), human, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "Night shift", Budget: moneyOf(budget),
		ValidUntil: r.clk.Now().Add(7 * 24 * time.Hour), Scope: scope(ceiling)})
	if err != nil {
		r.t.Fatalf("grant: %v", err)
	}
	return m
}

func (r *mrig) estimateUnder(p auth.Principal, mandate string, items ...generation.Item) (generation.Estimate, error) {
	return r.svc.Estimate(context.Background(), p, generation.EstimateIn{Items: items, Via: spend.ViaAPI, MandateID: mandate})
}

func mustProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
	return pe
}

func lunaSeeds(n int, seeds ...int64) generation.Item {
	it := newSeedItem(lt.TrackLuna, n)
	if len(seeds) > 0 {
		it.NSeeds, it.Seeds = 0, seeds
	}
	return it
}

// noAutoApprove makes every agent batch need the Owner's click, so a mandate is the only way around it.
var noAutoApprove = func() spend.Caps { c := spend.DefaultCaps; c.AutoApproveTakes = 0; return c }()

func TestEstimate_MandateCovered(t *testing.T) {
	t.Parallel()
	r := newMandateRig(t, withCaps(noAutoApprove))
	plain := r.est(night, lunaSeeds(2))
	if !plain.RequiresApproval {
		t.Fatalf("without a mandate the night needs approval: %+v", plain)
	}
	m := r.grant(900_000, int64(plain.Cost.Ceiling))
	e, err := r.estimateUnder(night, m.ID, lunaSeeds(2))
	if err != nil {
		t.Fatal(err)
	}
	if e.RequiresApproval || len(e.ApprovalReasons) != 0 || e.Mandate == nil || !e.Mandate.Covered || e.Token == "" {
		t.Fatalf("estimate = %+v mandate %+v", e, e.Mandate)
	}
	if got := r.str(`SELECT mandate_id FROM estimates WHERE id = ?`, e.ID); got != m.ID {
		t.Errorf("estimates.mandate_id = %q", got)
	}
	if got := r.count(`SELECT requires_approval FROM estimates WHERE id = ?`, e.ID); got != 0 {
		t.Errorf("requires_approval = %d", got)
	}
	if e.Cost != plain.Cost || e.Budget.Fits != plain.Budget.Fits {
		t.Errorf("the mandate changed the money: %+v vs %+v", e.Cost, plain.Cost)
	}
}

func TestEstimate_MandateStrictRefusal_NoRow(t *testing.T) {
	t.Parallel()
	r := newMandateRig(t, withCaps(noAutoApprove))
	plain := r.est(night, lunaSeeds(2))
	m := r.grant(900_000, int64(plain.Cost.Ceiling))
	rows, toks := r.count(`SELECT COUNT(*) FROM estimates`), r.count(`SELECT COUNT(*) FROM estimates WHERE mandate_id IS NOT NULL`)
	cases := []struct {
		name   string
		p      auth.Principal
		id     string
		items  []generation.Item
		reason string
	}{
		{"too many takes", night, m.ID, []generation.Item{lunaSeeds(7)}, "per_batch_takes"},
		{"a principal it does not name", other, m.ID, []generation.Item{lunaSeeds(2)}, "holder"},
		{"unrated parent", night, m.ID, []generation.Item{newSeedItem(lt.TrackTwinA, 1)}, "min_stars"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := r.estimateUnder(c.p, c.id, c.items...)
			pe := mustProblem(t, err, http.StatusConflict, "MANDATE_UNAVAILABLE")
			rs, _ := pe.Details["reasons"].([]string)
			if pe.Details["mandate_id"] != c.id || !contains(rs, c.reason) {
				t.Errorf("details = %v", pe.Details)
			}
		})
	}
	if r.count(`SELECT COUNT(*) FROM estimates`) != rows || r.count(`SELECT COUNT(*) FROM estimates WHERE mandate_id IS NOT NULL`) != toks {
		t.Error("a refused estimate wrote a row")
	}
	// a revoked and an unknown mandate
	if _, err := r.ms.Revoke(context.Background(), human, m.ID, "test"); err != nil {
		t.Fatal(err)
	}
	_, err := r.estimateUnder(night, m.ID, lunaSeeds(2))
	pe := mustProblem(t, err, http.StatusConflict, "MANDATE_UNAVAILABLE")
	if rs, _ := pe.Details["reasons"].([]string); !contains(rs, "not_live") {
		t.Errorf("revoked: %v", pe.Details)
	}
	_, err = r.estimateUnder(night, "no-such-mandate", lunaSeeds(2))
	mustProblem(t, err, http.StatusNotFound, "NOT_FOUND")
	if r.count(`SELECT COUNT(*) FROM estimates`) != rows {
		t.Error("a refused estimate wrote a row")
	}
}

func moneyOf(v int64) money.MicroUSD { return money.MicroUSD(v) }

func TestEstimate_MandateSeamAbsent(t *testing.T) {
	t.Parallel()
	r := newRig(t) // UseMandates never called
	_, err := r.svc.Estimate(context.Background(), night, generation.EstimateIn{Items: []generation.Item{lunaSeeds(1)}, MandateID: "m1"})
	pe := mustProblem(t, err, http.StatusServiceUnavailable, "UNAVAILABLE")
	if pe.Details["component"] != "mandates" {
		t.Errorf("details = %v", pe.Details)
	}
	_, err = r.svc.Preview(context.Background(), night, generation.EstimateIn{Items: []generation.Item{lunaSeeds(1)}, MandateID: "m1"})
	mustProblem(t, err, http.StatusServiceUnavailable, "UNAVAILABLE")
}

func TestCreate_MandateApprovalRow(t *testing.T) {
	t.Parallel()
	r := newMandateRig(t, withCaps(noAutoApprove))
	plain := r.est(night, lunaSeeds(2))
	m := r.grant(900_000, int64(plain.Cost.Ceiling))
	e, err := r.estimateUnder(night, m.ID, lunaSeeds(2))
	if err != nil {
		t.Fatal(err)
	}
	c := r.mustCreate(night, e, spend.ViaAPI)
	if c.State != "active" || c.ApprovalState != "auto" || c.Counts.Queued != 2 {
		t.Fatalf("created = %+v", c)
	}
	if got := r.str(`SELECT mandate_id FROM batches WHERE id = ?`, c.BatchID); got != m.ID {
		t.Errorf("batches.mandate_id = %q", got)
	}
	want := [4]string{"auto", "mandate", `["mandate"]`, "system:mandate:" + m.ID}
	got := [4]string{
		r.str(`SELECT state FROM approvals WHERE batch_id = ?`, c.BatchID), r.str(`SELECT via FROM approvals WHERE batch_id = ?`, c.BatchID),
		r.str(`SELECT reasons FROM approvals WHERE batch_id = ?`, c.BatchID), r.str(`SELECT decided_by FROM approvals WHERE batch_id = ?`, c.BatchID)}
	if got != want {
		t.Errorf("approval = %v, want %v", got, want)
	}
	if r.str(`SELECT decided_at FROM approvals WHERE batch_id = ?`, c.BatchID) != r.str(`SELECT created_at FROM approvals WHERE batch_id = ?`, c.BatchID) {
		t.Error("decided_at is not created_at")
	}
	if c.Approval.Via != "mandate" || c.Approval.Decider.Key != "system:mandate:"+m.ID {
		t.Errorf("approval view = %+v", c.Approval)
	}
	u, err := r.ms.Usage(context.Background(), m.ID)
	if err != nil || u.Batches != 1 || u.Committed != c.Ceiling {
		t.Errorf("usage = %+v %v", u, err)
	}
}

func TestCreate_CapsStillApplyUnderMandate(t *testing.T) {
	t.Parallel()
	r := newMandateRig(t, withCaps(noAutoApprove))
	plain := r.est(night, lunaSeeds(2))
	m := r.grant(900_000, int64(plain.Cost.Ceiling))
	e, err := r.estimateUnder(night, m.ID, lunaSeeds(2))
	if err != nil {
		t.Fatal(err)
	}
	// the Owner lowers the agent's daily cap below the batch: the mandate does not lift it
	r.exec(`UPDATE spend_caps SET agent_daily_micro_usd = ?, updated_by='system:test', updated_at=? WHERE id = 1`, int64(e.Cost.Max)-1, "2026-10-06T12:00:00.000Z")
	_, err = r.create(night, e, spend.ViaAPI)
	mustProblem(t, err, http.StatusPaymentRequired, "BUDGET_EXCEEDED")
	if r.count(`SELECT COUNT(*) FROM batches WHERE mandate_id IS NOT NULL`) != 0 {
		t.Error("a batch was written over a cap")
	}
	if r.count(`SELECT COUNT(*) FROM estimates WHERE id = ? AND consumed_at IS NULL`, e.ID) != 1 {
		t.Error("the token was consumed by a refused create")
	}
}

func TestCreate_MandateRecheckInTx(t *testing.T) {
	t.Parallel()
	t.Run("revoked between estimate and create", func(t *testing.T) {
		r := newMandateRig(t, withCaps(noAutoApprove))
		plain := r.est(night, lunaSeeds(2))
		m := r.grant(900_000, int64(plain.Cost.Ceiling))
		e, err := r.estimateUnder(night, m.ID, lunaSeeds(2))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.ms.Revoke(context.Background(), human, m.ID, "no more"); err != nil {
			t.Fatal(err)
		}
		_, err = r.create(night, e, spend.ViaAPI)
		pe := mustProblem(t, err, http.StatusConflict, "MANDATE_UNAVAILABLE")
		if rs, _ := pe.Details["reasons"].([]string); !contains(rs, "not_live") {
			t.Errorf("details = %v", pe.Details)
		}
		if r.count(`SELECT COUNT(*) FROM batches WHERE mandate_id IS NOT NULL`) != 0 || r.count(`SELECT COUNT(*) FROM estimates WHERE id = ? AND consumed_at IS NULL`, e.ID) != 1 {
			t.Error("a refused create left a batch or consumed the token")
		}
	})
	t.Run("another batch used the budget", func(t *testing.T) {
		r := newMandateRig(t, withCaps(noAutoApprove))
		plain := r.est(night, lunaSeeds(2))
		c := int64(plain.Cost.Ceiling)
		m := r.grant(c+c/2, c) // room for one batch
		e1, err := r.estimateUnder(night, m.ID, lunaSeeds(0, 11, 12))
		if err != nil {
			t.Fatal(err)
		}
		e2, err := r.estimateUnder(night, m.ID, lunaSeeds(0, 21, 22))
		if err != nil {
			t.Fatal(err)
		}
		r.mustCreate(night, e1, spend.ViaAPI)
		_, err = r.create(night, e2, spend.ViaAPI)
		pe := mustProblem(t, err, http.StatusConflict, "MANDATE_UNAVAILABLE")
		if rs, _ := pe.Details["reasons"].([]string); !contains(rs, "budget") {
			t.Errorf("details = %v", pe.Details)
		}
		// the same estimate is still valid for a retry once room exists: its token was not consumed
		if r.count(`SELECT COUNT(*) FROM estimates WHERE id = ? AND consumed_at IS NULL`, e2.ID) != 1 {
			t.Error("the refused create consumed the token")
		}
	})
}

// TestCreate_NoMandateByteIdentical: wiring the mandate seam changes nothing for an estimate and create that name no mandate.
func TestCreate_NoMandateByteIdentical(t *testing.T) {
	t.Parallel()
	run := func(wire bool) (generation.Estimate, generation.Created, []string) {
		r := newRig(t, withCaps(noAutoApprove))
		if wire {
			r.svc.UseMandates(mandates.New(mandates.Options{Store: r.st, Spend: r.sp, Now: r.clk.Now, NewID: r.id}))
		}
		e := r.est(night, lunaSeeds(2))
		c := r.mustCreate(human, r.est(human, lunaSeeds(0, 5, 6)), spend.ViaHumanCreate)
		var rows []string
		for _, q := range []string{
			`SELECT quote_json||requires_approval||approval_reasons||ifnull(mandate_id,'-') FROM estimates ORDER BY created_at, principal_id`,
			`SELECT via||state||ifnull(mandate_id,'-')||ceiling_micro_usd FROM batches ORDER BY created_at`,
			`SELECT state||ifnull(via,'-')||reasons||ifnull(decided_by,'-') FROM approvals ORDER BY created_at`,
		} {
			rs, err := r.st.R.Query(q)
			if err != nil {
				t.Fatal(err)
			}
			for rs.Next() {
				var s string
				if err := rs.Scan(&s); err != nil {
					t.Fatal(err)
				}
				rows = append(rows, s)
			}
			_ = rs.Close()
		}
		return e, c, rows
	}
	e0, c0, rows0 := run(false)
	e1, c1, rows1 := run(true)
	if d := cmp.Diff(e0, e1, cmpopts.IgnoreFields(generation.Estimate{}, "Provider")); d != "" {
		t.Errorf("estimate differs: %s", d)
	}
	if d := cmp.Diff(c0, c1); d != "" {
		t.Errorf("created differs: %s", d)
	}
	if d := cmp.Diff(rows0, rows1); d != "" {
		t.Errorf("rows differ: %s", d)
	}
	for _, row := range rows0 {
		if strings.Contains(row, "mandate") && !strings.Contains(row, "-") {
			t.Errorf("a mandate leaked into a plain row: %s", row)
		}
	}
}

func TestPreview_EqualsEstimate(t *testing.T) {
	t.Parallel()
	r := newMandateRig(t, withCaps(noAutoApprove))
	in := generation.EstimateIn{Items: []generation.Item{lunaSeeds(2)}, Via: spend.ViaAPI}
	pv, err := r.svc.Preview(context.Background(), night, in)
	if err != nil {
		t.Fatal(err)
	}
	es, err := r.svc.Estimate(context.Background(), night, in)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Token != "" || pv.Mandate != nil {
		t.Errorf("preview token %q mandate %v", pv.Token, pv.Mandate)
	}
	if d := cmp.Diff(es, pv, cmpopts.IgnoreFields(generation.Estimate{}, "ID", "Token")); d != "" {
		t.Errorf("preview differs from estimate: %s", d)
	}
	// under a mandate: the preview reports coverage (covered and not), the estimate refuses the same plan
	m := r.grant(900_000, int64(es.Cost.Ceiling))
	in.MandateID = m.ID
	pv, err = r.svc.Preview(context.Background(), night, in)
	if err != nil || pv.Mandate == nil || !pv.Mandate.Covered || pv.RequiresApproval {
		t.Fatalf("covered preview = %+v %v", pv.Mandate, err)
	}
	in.Items = []generation.Item{lunaSeeds(7)}
	pv, err = r.svc.Preview(context.Background(), night, in)
	if err != nil || pv.Mandate == nil || pv.Mandate.Covered || !contains(pv.Mandate.Reasons, "per_batch_takes") || !pv.RequiresApproval {
		t.Fatalf("uncovered preview = %+v %v", pv.Mandate, err)
	}
	if _, err := r.svc.Estimate(context.Background(), night, in); err == nil {
		t.Error("the estimate of an uncovered plan succeeded")
	}
}

func (r *rig) checksum() string {
	r.t.Helper()
	rows, err := r.st.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		r.t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			r.t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = rows.Close()
	var sb strings.Builder
	for _, n := range names {
		var c int
		if err := r.st.R.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&c); err != nil { // #nosec G202 -- table names come from sqlite_master
			r.t.Fatal(err)
		}
		fmt.Fprintf(&sb, "%s=%d;", n, c)
	}
	return sb.String()
}

func TestPreview_WritesNothing(t *testing.T) {
	t.Parallel()
	r := newMandateRig(t, withCaps(noAutoApprove))
	m := r.grant(900_000, int64(r.est(night, lunaSeeds(2)).Cost.Ceiling))
	before := r.checksum()
	for _, in := range []generation.EstimateIn{
		{Items: []generation.Item{lunaSeeds(2)}, Via: spend.ViaAPI},
		{Items: []generation.Item{lunaSeeds(2)}, Via: spend.ViaAPI, MandateID: m.ID},
		{Items: []generation.Item{lunaSeeds(9)}, Via: spend.ViaAPI, MandateID: m.ID},
	} {
		if _, err := r.svc.Preview(pendingCtx(r.rig, night, "estimate_generation"), night, in); err != nil {
			t.Fatal(err)
		}
	}
	if after := r.checksum(); after != before {
		t.Errorf("preview wrote rows:\n%s\n%s", before, after)
	}
}
