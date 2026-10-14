package generation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

func TestService_NotConfigured(t *testing.T) {
	t.Parallel()
	svc := generation.New(generation.Options{})
	ctx := context.Background()
	check := func(name string, err error) {
		t.Helper()
		wantProblem(t, err, 503, "UNAVAILABLE")
		_ = name
	}
	_, err := svc.Estimate(ctx, agent, generation.EstimateIn{Items: []generation.Item{newSeedItem(lt.TrackLuna, 1)}})
	check("estimate", err)
	_, err = svc.Create(ctx, agent, generation.CreateIn{}, nil)
	check("create", err)
	_, err = svc.Approve(ctx, human, "b", "")
	check("approve", err)
	_, err = svc.Reject(ctx, human, "b", "r")
	check("reject", err)
	_, err = svc.Approval(ctx, agent, "b")
	check("approval", err)
	_, err = svc.RequestApproval(ctx, agent, "b")
	check("request", err)
	_, err = svc.Budget(ctx, agent)
	check("budget", err)
	_, err = svc.SpendReport(ctx, "")
	check("spend", err)
	_, err = svc.ListLedger(ctx, generation.LedgerQuery{})
	check("ledger", err)
	_, err = svc.SetCaps(ctx, human, spend.CapsPatch{MonthlyMicro: micro(1)})
	check("caps", err)
	if _, err := lineage.New(lineage.Options{}).Tree(ctx, "x", 1); !errors.Is(err, lineage.ErrUnconfigured) {
		t.Errorf("tree: %v", err)
	}
	if _, err := lineage.New(lineage.Options{}).BackfillRecipeKeys(ctx); !errors.Is(err, lineage.ErrUnconfigured) {
		t.Errorf("backfill: %v", err)
	}
}

// breakTable renames a table so every statement that touches it fails (a stand-in for a database error).
func (r *rig) breakTable(name string) {
	r.t.Helper()
	r.exec(`PRAGMA foreign_keys = OFF`)
	r.exec(fmt.Sprintf(`ALTER TABLE %s RENAME TO %s_gone`, name, name))
}

// TestFaults_BrokenTables: with any one table gone, no operation panics and the ones that read it fail
// with an error instead of a half-written result.
func TestFaults_BrokenTables(t *testing.T) {
	t.Parallel()
	tables := []string{"tracks", "recipes", "recipe_drafts", "jobs", "recipe_keys", "spend_caps", "api_tokens", "ledger", "batches", "approvals", "estimates",
		"rtf_stats", "provider_state", "spend_cap_changes", "mutations", "ratings"}
	for _, tbl := range tables {
		t.Run(tbl, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			// state made before the break: a pending agent batch and a ready estimate per principal
			pending := r.pending()
			r.tokens()
			ready := r.est(human, newSeedItem(lt.TrackLuna, 2))
			draft := r.est(human, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}, NSeeds: 2})
			r.breakTable(tbl)
			ctx := context.Background()
			failures := 0
			note := func(err error) {
				t.Helper()
				if err != nil {
					failures++
				}
			}
			luna := lt.TrackLuna
			items := [][]generation.Item{
				{newSeedItem(luna, 2)},
				{{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft2}, NSeeds: 2}},
				{{Mode: lineage.ModeLyricEdit, ParentTrackID: &luna, NSeeds: 2, Patch: &lineage.Patch{Lyrics: sp("[Verse 1]\nx")}}},
				{{Mode: lineage.ModeComposeNew, Recipe: []byte(`{"id":"zz-1","style":"pop","lyrics":"[Verse 1]\nline","cot":"full","seed":7}`)}},
				{{Mode: lineage.ModeFromTemplate, TemplateMode: lineage.ModeNewSeed, ParentTrackIDs: []string{luna, lt.TrackCitta}}},
			}
			for _, it := range items {
				_, err := r.svc.Estimate(ctx, agent, generation.EstimateIn{Items: it, Via: spend.ViaAPI})
				note(err)
			}
			_, err := r.create(human, ready, spend.ViaHumanCreate)
			note(err)
			_, err = r.create(human, draft, spend.ViaHumanCreate)
			note(err)
			_, err = r.svc.Approve(ctx, human, pending.BatchID, "x")
			note(err)
			_, err = r.svc.Reject(ctx, human, pending.BatchID, "x")
			note(err)
			_, err = r.svc.Approval(ctx, agent, pending.BatchID)
			note(err)
			_, err = r.svc.RequestApproval(ctx, agent, pending.BatchID)
			note(err)
			_, err = r.svc.Budget(ctx, agent)
			note(err)
			_, err = r.svc.Budget(ctx, human)
			note(err)
			_, err = r.svc.SpendReport(ctx, "")
			note(err)
			_, err = r.svc.ListLedger(ctx, generation.LedgerQuery{Limit: 5})
			note(err)
			_, err = r.svc.SetCaps(ctx, human, spend.CapsPatch{MonthlyMicro: micro(5), Tokens: []spend.TokenPatch{{TokenID: "fixture-token"}}})
			note(err)
			if failures == 0 {
				t.Errorf("breaking %s failed nothing", tbl)
			}
			// nothing half-written: a decision either happened completely or not at all
			if tbl != "batches" && tbl != "approvals" {
				st := r.str(`SELECT a.state || '/' || b.state FROM approvals a JOIN batches b ON b.id = a.batch_id WHERE a.batch_id = ?`, pending.BatchID)
				if st != "pending/awaiting_confirmation" && st != "approved/active" {
					t.Errorf("approval/batch after faults = %s", st)
				}
			}
		})
	}
}

// failOn adds a trigger that aborts every insert or update on a table.
func (r *rig) failOn(event, table string) {
	r.t.Helper()
	r.exec(fmt.Sprintf(`CREATE TRIGGER fail_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'injected'); END`, event, table, event, table))
}

// TestFaults_CreateWriteFailuresRollBack: a failure of any write in the create transaction leaves nothing
// behind and the token usable.
func TestFaults_CreateWriteFailuresRollBack(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ event, table string }{
		{"INSERT", "recipes"}, {"INSERT", "recipe_keys"}, {"INSERT", "batches"}, {"INSERT", "approvals"}, {"INSERT", "jobs"}, {"INSERT", "ledger"},
		{"UPDATE", "estimates"},
	} {
		t.Run(c.event+" "+c.table, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			e := r.est(human, newSeedItem(lt.TrackLuna, 2))
			r.failOn(c.event, c.table)
			batches, recipes, ledger := r.count(`SELECT COUNT(*) FROM batches`), r.count(`SELECT COUNT(*) FROM recipes`), r.count(`SELECT COUNT(*) FROM ledger`)
			if _, err := r.create(human, e, spend.ViaHumanCreate); err == nil {
				t.Fatal("create succeeded")
			}
			if r.count(`SELECT COUNT(*) FROM batches`) != batches || r.count(`SELECT COUNT(*) FROM recipes`) != recipes || r.count(`SELECT COUNT(*) FROM ledger`) != ledger {
				t.Error("a failed create left rows behind")
			}
			if r.count(`SELECT COUNT(*) FROM estimates WHERE id = ? AND consumed_at IS NULL`, e.ID) != 1 {
				t.Error("a failed create consumed the token")
			}
			r.exec(fmt.Sprintf(`DROP TRIGGER fail_%s_%s`, c.event, c.table))
			if _, err := r.create(human, e, spend.ViaHumanCreate); err != nil {
				t.Errorf("retry after the fault: %v", err)
			}
		})
	}
}

func TestFaults_DecisionWritesFailRollBack(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ event, table string }{{"UPDATE", "approvals"}, {"UPDATE", "batches"}, {"INSERT", "ledger"}, {"INSERT", "audit_log"}} {
		t.Run(c.event+" "+c.table, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			b := r.pending()
			r.failOn(c.event, c.table)
			ctx := auditCtx(r)
			for _, f := range []func() error{
				func() error { _, err := r.svc.Approve(ctx, human, b.BatchID, ""); return err },
				func() error { _, err := r.svc.Reject(ctx, human, b.BatchID, "no"); return err },
			} {
				err := f()
				if c.table == "audit_log" && err == nil {
					t.Error("an audit failure must fail the operation: the row is part of its transaction")
				}
				if c.table != "audit_log" && c.table != "ledger" && err == nil {
					t.Errorf("%s fault went unnoticed", c.table)
				}
			}
			if got := r.str(`SELECT state FROM approvals WHERE batch_id = ?`, b.BatchID); got != "pending" && c.table != "ledger" {
				t.Errorf("approval = %s after a rolled-back decision", got)
			}
		})
	}
	// caps: the history insert fails
	r := newRig(t)
	r.failOn("INSERT", "spend_cap_changes")
	if _, err := r.svc.SetCaps(context.Background(), human, spend.CapsPatch{MonthlyMicro: micro(7)}); err == nil {
		t.Error("set caps survived a failing history row")
	}
	if r.val(`SELECT monthly_micro_usd FROM spend_caps`) != 10_000_000 {
		t.Error("the cap changed although its history row failed")
	}
	r2 := newRig(t)
	r2.failOn("INSERT", "audit_log")
	if _, err := r2.svc.SetCaps(auditCtx(r2), human, spend.CapsPatch{MonthlyMicro: micro(7)}); err == nil {
		t.Error("set caps survived a failing audit row")
	}
}

func auditCtx(r *rig) context.Context { return pendingCtx(r, human, "approve_batch") }

func TestFaults_ClosedPools(t *testing.T) {
	t.Parallel()
	for _, pool := range []string{"R", "W"} {
		t.Run(pool, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			e := r.est(human, newSeedItem(lt.TrackLuna, 1))
			b := r.pending()
			if pool == "R" {
				_ = r.st.R.Close()
			} else {
				_ = r.st.W.Close()
			}
			ctx := context.Background()
			if _, err := r.svc.Budget(ctx, human); pool == "R" && err == nil {
				t.Error("budget on a closed reader")
			}
			if _, err := r.svc.SetCaps(ctx, human, spend.CapsPatch{MonthlyMicro: micro(3)}); pool == "W" && err == nil {
				t.Error("set caps on a closed writer")
			}
			if _, err := r.svc.Approve(ctx, human, b.BatchID, ""); pool == "W" && err == nil {
				t.Error("approve on a closed writer")
			}
			if _, err := r.svc.Reject(ctx, human, b.BatchID, "x"); pool == "W" && err == nil {
				t.Error("reject on a closed writer")
			}
			if _, err := r.svc.RequestApproval(ctx, agent, b.BatchID); pool == "W" && err == nil {
				t.Error("request on a closed writer")
			}
			if _, err := r.create(human, e, spend.ViaHumanCreate); pool == "W" && err == nil {
				t.Error("create on a closed writer")
			}
			if _, err := r.svc.Estimate(ctx, human, generation.EstimateIn{Items: []generation.Item{newSeedItem(lt.TrackLuna, 1)}}); err == nil {
				t.Error("estimate with a closed pool")
			}
			_, _ = r.svc.Approval(ctx, agent, b.BatchID)
			_, _ = r.svc.SpendReport(ctx, "")
			_, _ = r.svc.ListLedger(ctx, generation.LedgerQuery{})
		})
	}
}

func TestEstimate_CachedFakeQuote(t *testing.T) {
	t.Parallel()
	set := func(r *rig, mut func(q *provider.Quote)) {
		q := r.goodQuote()
		q.Backend = provider.BackendFake
		mut(&q)
		b, _ := json.Marshal(q)
		r.exec(`UPDATE provider_state SET quote_json = ?, quote_at = ? WHERE backend = 'fake'`, string(b), "2026-10-06T11:59:00.000Z")
	}
	items := generation.EstimateIn{Items: []generation.Item{newSeedItem(lt.TrackLuna, 1)}}
	for _, c := range []struct {
		name   string
		mut    func(q *provider.Quote)
		reason string
	}{
		{"price missing", func(q *provider.Quote) { q.ListPricePPH = 0 }, provider.ReasonCatalogUnreadable},
		{"no workers", func(q *provider.Quote) { q.Endpoint.WorkersMax = 0 }, provider.ReasonEndpointInactive},
		{"no idle timeout", func(q *provider.Quote) { q.Endpoint.IdleTimeoutS = nil }, provider.ReasonEndpointConfig},
	} {
		r := newRig(t)
		set(r, c.mut)
		_, err := r.svc.Estimate(context.Background(), agent, items)
		pe := wantProblem(t, err, 503, "GENERATION_DISABLED")
		if pe.Details["reason"] != c.reason {
			t.Errorf("%s: %v", c.name, pe.Details)
		}
	}
	// a cached fake quote drives the price; with the margin on top
	r := newRig(t, withMargin(100_000))
	set(r, func(q *provider.Quote) { q.ListPricePPH = 1_000_000 })
	e, err := r.svc.Estimate(context.Background(), agent, items)
	if err != nil || e.PricePerHour != 1_100_000 {
		t.Errorf("price = %d %v", e.PricePerHour, err)
	}
	// no rtf rows at all: the constant 440 applies
	r.exec(`DROP TRIGGER rtf_stats_no_delete`)
	r.exec(`DELETE FROM rtf_stats`)
	if _, err := r.svc.Estimate(context.Background(), agent, items); err != nil {
		t.Errorf("no rtf rows: %v", err)
	}
}

func TestEstimate_UnknownEngineAndEmpty(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, err := r.svc.Estimate(context.Background(), agent, generation.EstimateIn{Engine: "nope", Items: []generation.Item{newSeedItem(lt.TrackLuna, 1)}})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	_, err = r.svc.Estimate(context.Background(), agent, generation.EstimateIn{})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	// a bad via defaults to api
	e, err := r.svc.Estimate(context.Background(), human, generation.EstimateIn{Engine: "yue2", Items: []generation.Item{newSeedItem(lt.TrackLuna, 1)}})
	if err != nil || !e.RequiresApproval {
		t.Errorf("default via: %+v %v", e.ApprovalReasons, err)
	}
	var pe *problem.Error
	if errors.As(err, &pe) {
		t.Error("unexpected problem")
	}
}
