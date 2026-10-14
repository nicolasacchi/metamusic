package generation_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/idem"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// gatedCtx is the context the gate builds for a create operation: an idempotency lease, the mutable marks
// and the precomputed audit entry.
func (r *rig) gatedCtx(p auth.Principal, key string) (context.Context, *idem.Marks, string) {
	r.t.Helper()
	ctx := context.Background()
	begun, err := idem.NewStore(r.st, r.clk.Now).Begin(ctx, p.Key(), key, "create_generation", "sha-"+key, false)
	if err != nil || begun.Outcome != idem.Proceed {
		r.t.Fatalf("idempotency begin: %v %v", begun.Outcome, err)
	}
	ctx, marks := idem.WithMarks(ctx)
	ctx = idem.WithKey(ctx, p.Key(), key, begun.Gen)
	ctx = audit.WithPending(ctx, audit.Entry{At: r.clk.Now(), Principal: p, Transport: reqinfo.REST, Client: "test", Op: "create_generation", Method: "POST",
		ArgsSHA256: "sha-" + key, Idempotency: audit.IdemNone})
	return ctx, marks, begun.Gen
}

func TestCreate_HappyPath(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	recipes := r.count(`SELECT COUNT(*) FROM recipes`)
	keys := r.count(`SELECT COUNT(*) FROM recipe_keys`)
	e := r.est(human, newSeedItem(lt.TrackLuna, 3))
	c := r.mustCreate(human, e, spend.ViaHumanCreate)

	if c.State != "active" || c.ApprovalState != "approved" || c.Takes != 3 || len(c.JobIDs) != 3 || c.Max != e.Cost.Max || c.Ceiling != e.Cost.Ceiling || c.Expected != e.Cost.Expected {
		t.Fatalf("created = %+v", c)
	}
	if c.Counts.Queued != 3 || c.Via != "human_create" || c.EstimateID != e.ID || c.CreatedBy.Key != human.Key() || c.CreatedBy.Kind != "human" || c.Rationale == nil || *c.Rationale != "test" {
		t.Errorf("view = %+v", c)
	}
	if r.wakes.Load() != 1 {
		t.Errorf("wakes = %d", r.wakes.Load())
	}
	// recipes + keys: three new rows owned by the principal
	if got := r.count(`SELECT COUNT(*) FROM recipes`) - recipes; got != 3 {
		t.Errorf("%d recipes inserted", got)
	}
	if got := r.count(`SELECT COUNT(*) FROM recipe_keys`) - keys; got != 3 {
		t.Errorf("%d recipe keys inserted", got)
	}
	if got := r.count(`SELECT COUNT(*) FROM recipes WHERE created_by = ? AND request_id LIKE 'fx-luna-s10%'`, human.Key()); got != 3 {
		t.Errorf("%d job recipes", got)
	}
	// batch
	var state, via, backend, estimate string
	var maxM, retry, ceiling, price, overheadS, slack, workers, idle int64
	if err := r.st.R.QueryRow(`SELECT state, via, backend, estimate_id, max_micro_usd, retry_allowance_micro_usd, ceiling_micro_usd, price_pph_micro_usd,
		attempt_overhead_s, kill_slack_s, workers_max, idle_s FROM batches WHERE id = ?`, c.BatchID).Scan(&state, &via, &backend, &estimate, &maxM, &retry, &ceiling, &price, &overheadS, &slack, &workers, &idle); err != nil {
		t.Fatal(err)
	}
	if state != "active" || via != "human_create" || backend != "fake" || estimate != e.ID || maxM != int64(e.Cost.Max) || ceiling != int64(e.Cost.Ceiling) ||
		ceiling != maxM+retry || price != 690_000 || overheadS != 200 || slack != 15 || workers != 2 || idle != 20 {
		t.Errorf("batch row = %s %s %s %s max %d retry %d ceiling %d price %d overhead_s %d slack %d workers %d idle %d", state, via, backend, estimate, maxM, retry, ceiling, price, overheadS, slack, workers, idle)
	}
	// approval
	if got := r.str(`SELECT state || '/' || via || '/' || decided_by FROM approvals WHERE batch_id = ?`, c.BatchID); got != "approved/human_create/"+human.Key() {
		t.Errorf("approval = %s", got)
	}
	if r.str(`SELECT decided_at FROM approvals WHERE batch_id = ?`, c.BatchID) == "" {
		t.Error("decided_at missing")
	}
	// jobs
	rows, err := r.st.R.Query(`SELECT take_index, state, queued_at, backend, source_track_id, operator, mutation_json, reserved_micro_usd, title FROM jobs WHERE batch_id = ? ORDER BY take_index`, c.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var takeSum int64
	n := 0
	for rows.Next() {
		var idx int
		var st, be, parent, op, mj, title string
		var queued *string
		var reserved int64
		if err := rows.Scan(&idx, &st, &queued, &be, &parent, &op, &mj, &reserved, &title); err != nil {
			t.Fatal(err)
		}
		if idx != n || st != "queued" || queued == nil || be != "fake" || parent != lt.TrackLuna || op != "reroll_seed" || !strings.HasPrefix(title, "Luna Park · s10") {
			t.Errorf("job %d = %s %v %s %s %s %q", idx, st, queued, be, parent, op, title)
		}
		var m struct {
			Operator  string         `json:"operator"`
			Params    map[string]any `json:"params"`
			Patch     any            `json:"recipe_patch"`
			Rationale string         `json:"rationale"`
			Principal string         `json:"principal_id"`
		}
		if err := json.Unmarshal([]byte(mj), &m); err != nil || m.Operator != "reroll_seed" || m.Params["mode"] != "new_seed" || m.Patch != nil || m.Rationale != "test" || m.Principal != human.Key() {
			t.Errorf("mutation_json = %s (%v)", mj, err)
		}
		takeSum += reserved
		n++
	}
	if n != 3 {
		t.Fatalf("%d jobs", n)
	}
	// ledger: one take reserve per job, no batch overhead row, Σ = Σ take
	if got := r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'reserve' AND reason = 'take'`, c.BatchID); got != 3 {
		t.Errorf("%d take reserves", got)
	}
	if got := r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND reason <> 'take'`, c.BatchID); got != 0 {
		t.Errorf("%d other ledger rows (no overhead row is written at create)", got)
	}
	if got := r.val(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ?`, c.BatchID); got != takeSum || money.MicroUSD(got) != c.Reserved || c.Outstanding != c.Reserved {
		t.Errorf("ledger Σ %d, Σ take %d, reserved %d, outstanding %d", got, takeSum, c.Reserved, c.Outstanding)
	}
	// the estimate is consumed
	if got := r.str(`SELECT consumed_batch_id FROM estimates WHERE id = ?`, e.ID); got != c.BatchID {
		t.Errorf("consumed_batch_id = %q", got)
	}
}

func TestCreate_AuditAndIdempotencyInTransaction(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := r.est(human, newSeedItem(lt.TrackLuna, 2))
	ctx, marks, _ := r.gatedCtx(human, "k-1")
	c, err := r.svc.Create(ctx, human, generation.CreateIn{Token: e.Token, Via: spend.ViaHumanCreate}, encode)
	if err != nil {
		t.Fatal(err)
	}
	if !marks.Completed() || !marks.Audited() {
		t.Fatalf("marks: completed=%v audited=%v", marks.Completed(), marks.Audited())
	}
	// idempotency row done, holding the exact bytes the encoder produced
	var state, resp string
	var status int
	if err := r.st.R.QueryRow(`SELECT state, status, response FROM idempotency WHERE principal_key = ? AND key = 'k-1'`, human.Key()).Scan(&state, &status, &resp); err != nil {
		t.Fatal(err)
	}
	_, want, _ := encode(c)
	if state != "done" || status != 201 || resp != string(want) {
		t.Errorf("idempotency = %s %d %q, want done 201 %q", state, status, resp, want)
	}
	// the retry replays the stored 201: nothing else runs
	begun, err := idem.NewStore(r.st, r.clk.Now).Begin(context.Background(), human.Key(), "k-1", "create_generation", "sha-k-1", false)
	if err != nil || begun.Outcome != idem.Replay || string(begun.Body) != string(want) {
		t.Errorf("replay = %+v %v", begun, err)
	}
	// the audit row: ceiling as cost, created ids = batch + jobs, stored
	var cost int64
	var ids, idemState, op string
	var st int
	if err := r.st.R.QueryRow(`SELECT cost_micro_usd, created_ids_json, idempotency, op, status FROM audit_log WHERE op = 'create_generation'`).Scan(&cost, &ids, &idemState, &op, &st); err != nil {
		t.Fatal(err)
	}
	var created []string
	_ = json.Unmarshal([]byte(ids), &created)
	if cost != int64(e.Cost.Ceiling) || idemState != "stored" || st != 201 || created[0] != c.BatchID || len(created) != 3 {
		t.Errorf("audit = cost %d idem %s status %d ids %v", cost, idemState, st, created)
	}
	if n := r.count(`SELECT COUNT(*) FROM batches WHERE estimate_id = ?`, e.ID); n != 1 {
		t.Errorf("%d batches", n)
	}

	// a lost lease (the key was reclaimed) rolls the whole create back and answers 409 IDEMPOTENCY_IN_PROGRESS
	e2 := r.est(human, newSeedItem(lt.TrackLuna, 1))
	ctx2, marks2, _ := r.gatedCtx(human, "k-2")
	r.exec(`UPDATE idempotency SET created_at = '2000-01-01T00:00:00Z' WHERE principal_key = ? AND key = 'k-2'`, human.Key())
	batches := r.count(`SELECT COUNT(*) FROM batches`)
	_, err = r.svc.Create(ctx2, human, generation.CreateIn{Token: e2.Token, Via: spend.ViaHumanCreate}, encode)
	wantProblem(t, err, 409, "IDEMPOTENCY_IN_PROGRESS")
	if marks2.Completed() || marks2.Audited() {
		t.Error("marks must be cleared on an error path")
	}
	if r.count(`SELECT COUNT(*) FROM batches`) != batches || r.str(`SELECT COALESCE(consumed_at, '') FROM estimates WHERE id = ?`, e2.ID) != "" {
		t.Error("a rolled-back create left rows behind")
	}
}

func TestCreate_Approval(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	type want struct{ batch, approval, job, reason string }
	check := func(name string, c generation.Created, w want) {
		t.Helper()
		if c.State != w.batch || c.ApprovalState != w.approval {
			t.Errorf("%s: batch %s approval %s, want %s %s", name, c.State, c.ApprovalState, w.batch, w.approval)
		}
		if got := r.str(`SELECT DISTINCT state FROM jobs WHERE batch_id = ?`, c.BatchID); got != w.job {
			t.Errorf("%s: job state %s, want %s", name, got, w.job)
		}
		if w.job == "awaiting_confirmation" && r.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND queued_at IS NOT NULL`, c.BatchID) != 0 {
			t.Errorf("%s: queued_at set on awaiting jobs", name)
		}
		if w.reason != "" && !contains(c.Approval.Reasons, w.reason) {
			t.Errorf("%s: reasons %v, want %s", name, c.Approval.Reasons, w.reason)
		}
	}
	// a human on the API path needs a human (D-M3-9)
	c := r.mustCreate(human, r.est(human, newSeedItem(lt.TrackLuna, 1)), spend.ViaAPI)
	check("human api", c, want{"awaiting_confirmation", "pending", "awaiting_confirmation", spend.ReasonHumanAPIPath})
	if c.Via != "api" || r.wakes.Load() != 1 {
		t.Errorf("via %s wakes %d", c.Via, r.wakes.Load())
	}
	// the operator's own confirmation counts
	c = r.mustCreate(human, r.est(human, newSeedItem(lt.TrackLuna, 1)), spend.ViaHumanCreate)
	check("human_create", c, want{"active", "approved", "queued", ""})
	// an agent within its limits is approved automatically; over them it waits
	c = r.mustCreate(agent, r.est(agent, newSeedItem(lt.TrackCitta, 1)), spend.ViaAPI)
	if c.ApprovalState != "auto" && c.ApprovalState != "pending" {
		t.Errorf("agent 1 take: %s", c.ApprovalState)
	}
	big := r.mustCreate(agent, r.est(agent, newSeedItem(lt.TrackCitta, 6)), spend.ViaAPI)
	check("agent 6 takes", big, want{"awaiting_confirmation", "pending", "awaiting_confirmation", spend.ReasonTakesOverLim})
	if got := r.str(`SELECT expires_at FROM approvals WHERE batch_id = ?`, big.BatchID); got != "2026-10-07T12:00:00.000Z" {
		t.Errorf("approval expires at %s, want 24 h after creation", got)
	}
	// only a human create path counts as the approval: an agent claiming human_create is treated as api
	c = r.mustCreate(agent, r.est(agent, newSeedItem(lt.TrackCitta, 6)), spend.ViaHumanCreate)
	if c.Via != "api" || c.ApprovalState != "pending" {
		t.Errorf("agent human_create: via %s approval %s", c.Via, c.ApprovalState)
	}
}

func TestCreate_AgentAutoApprovedWithinLimits(t *testing.T) {
	t.Parallel()
	caps := spend.DefaultCaps
	caps.AutoApproveMicro = 5_000_000
	r := newRig(t, withCaps(caps))
	c := r.mustCreate(agent, r.est(agent, newSeedItem(lt.TrackCitta, 2)), spend.ViaAPI)
	if c.State != "active" || c.ApprovalState != "auto" || c.Approval.Via != "auto" || c.Approval.DecidedBy != agent.Key() {
		t.Errorf("created = %+v / %+v", c, c.Approval)
	}
	if got := r.str(`SELECT DISTINCT state FROM jobs WHERE batch_id = ?`, c.BatchID); got != "queued" {
		t.Errorf("job state %s", got)
	}
}

func TestCreate_FromDraftReusesTheDraftsRecipe(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	src := r.str(`SELECT source_recipe_id FROM recipe_drafts WHERE id = ?`, lt.Draft1)
	recipes := r.count(`SELECT COUNT(*) FROM recipes`)
	e := r.est(human, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}, NSeeds: 2})
	c := r.mustCreate(human, e, spend.ViaHumanCreate)
	if got := r.count(`SELECT COUNT(*) FROM recipes`) - recipes; got != 1 { // the reused row is not inserted; only the second seed is
		t.Errorf("%d recipes inserted, want 1", got)
	}
	rows, err := r.st.R.Query(`SELECT recipe_id, draft_id, operator FROM jobs WHERE batch_id = ? ORDER BY take_index`, c.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var recipeIDs []string
	for rows.Next() {
		var rid, did, op string
		if err := rows.Scan(&rid, &did, &op); err != nil {
			t.Fatal(err)
		}
		if did != lt.Draft1 || op != "from_draft" {
			t.Errorf("job draft %s op %s", did, op)
		}
		recipeIDs = append(recipeIDs, rid)
	}
	if recipeIDs[0] != src || recipeIDs[1] == src {
		t.Errorf("recipes %v, source %s", recipeIDs, src)
	}
	// the draft now has a live job: estimating it again is refused
	_, err = r.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}}}, Via: spend.ViaHumanCreate})
	wantProblem(t, err, 409, "DRAFT_NOT_GENERATABLE")
}

func TestCreate_OrderOfChecks(t *testing.T) {
	t.Parallel()
	// 1. the provider gate answers before the token is looked at
	t.Run("gate before token", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		svc := r.runpodService(r.goodQuote(), false, true) // disarmed
		_, err := svc.Create(context.Background(), human, generation.CreateIn{Token: "mmc_" + strings.Repeat("a", 52), Via: spend.ViaAPI}, encode)
		wantProblem(t, err, 503, "PROVIDER_DISARMED")
	})
	t.Run("tripped", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		svc := r.runpodService(r.goodQuote(), true, true)
		r.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES ('trip-1', 'runpod-serverless', ?, 'meter_exceeds_reserved', '{}', '[]')`, "2026-10-06T11:00:00.000Z")
		_, err := svc.Create(context.Background(), human, generation.CreateIn{Token: "mmc_" + strings.Repeat("a", 52), Via: spend.ViaAPI}, encode)
		pe := wantProblem(t, err, 503, "PROVIDER_TRIPPED")
		if pe.Details["trip_id"] != "trip-1" || pe.Details["reason"] != "meter_exceeds_reserved" {
			t.Errorf("details = %v", pe.Details)
		}
	})
	t.Run("generation disabled", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, err := r.withGate(r.gate(provider.ModeNone)).Create(context.Background(), human, generation.CreateIn{Token: "mmc_x", Via: spend.ViaAPI}, encode)
		wantProblem(t, err, 503, "GENERATION_DISABLED")
	})
	// 2. an expired token with exhausted caps answers 410, not 402
	t.Run("expired before caps", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		e := r.est(human, newSeedItem(lt.TrackLuna, 2))
		r.exec(`UPDATE spend_caps SET monthly_micro_usd = 1 WHERE id = 1`)
		r.clk.Advance(11 * time.Minute)
		_, err := r.create(human, e, spend.ViaHumanCreate)
		wantProblem(t, err, 410, "CONFIRMATION_EXPIRED")
	})
	// 3. a valid token with exhausted caps answers 402 and the token stays consumable
	t.Run("caps leave the token valid", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		e := r.est(human, newSeedItem(lt.TrackLuna, 2))
		r.exec(`UPDATE spend_caps SET monthly_micro_usd = 1000 WHERE id = 1`)
		batches := r.count(`SELECT COUNT(*) FROM batches`)
		_, err := r.create(human, e, spend.ViaHumanCreate)
		pe := wantProblem(t, err, 402, "BUDGET_EXCEEDED")
		if pe.Details["cap"] != "monthly" || pe.Details["limit_micro_usd"] != int64(1000) || pe.Details["requested_micro_usd"] != int64(e.Cost.Max) {
			t.Errorf("details = %v", pe.Details)
		}
		if r.count(`SELECT COUNT(*) FROM batches`) != batches || r.str(`SELECT COALESCE(consumed_at, '') FROM estimates WHERE id = ?`, e.ID) != "" {
			t.Error("a refused create left rows behind or consumed the token")
		}
		r.exec(`UPDATE spend_caps SET monthly_micro_usd = 10000000 WHERE id = 1`)
		if c, err := r.create(human, e, spend.ViaHumanCreate); err != nil || c.State != "active" {
			t.Errorf("retry with the same token: %+v %v", c, err)
		}
	})
	t.Run("batch cap uses the ceiling", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		e := r.est(human, newSeedItem(lt.TrackLuna, 2))
		r.exec(`UPDATE spend_caps SET batch_micro_usd = ? WHERE id = 1`, int64(e.Cost.Ceiling)-1)
		_, err := r.create(human, e, spend.ViaHumanCreate)
		if pe := wantProblem(t, err, 402, "BUDGET_EXCEEDED"); pe.Details["cap"] != "batch" {
			t.Errorf("details = %v", pe.Details)
		}
		r.exec(`UPDATE spend_caps SET batch_micro_usd = ? WHERE id = 1`, int64(e.Cost.Ceiling)) // exactly equal fits
		if _, err := r.create(human, e, spend.ViaHumanCreate); err != nil {
			t.Errorf("an exactly fitting ceiling: %v", err)
		}
	})
	t.Run("daily cap of the principal", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		e := r.est(agent, newSeedItem(lt.TrackLuna, 1))
		r.exec(`UPDATE spend_caps SET agent_daily_micro_usd = 10 WHERE id = 1`)
		if pe := wantProblem(t, func() error { _, err := r.create(agent, e, spend.ViaAPI); return err }(), 402, "BUDGET_EXCEEDED"); pe.Details["cap"] != "daily" {
			t.Errorf("details = %v", pe.Details)
		}
	})
}

func TestCreate_EstimateStale(t *testing.T) {
	t.Parallel()
	t.Run("request id inserted meanwhile", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		e := r.est(human, newSeedItem(lt.TrackLuna, 2))
		taken := e.Items[0].Takes[1].RequestID
		r.exec(`INSERT INTO recipes(id, engine, request_id, request_json, style, lyrics, seed, cot, hash, core_hash, created_by, created_at)
			VALUES ('rcp-squat', 'yue2', ?, ?, 's', 'l', 9, 'full', 'h-squat', 'c-squat', 'import', '2026-10-06T12:00:00Z')`, taken,
			`{"id":"`+taken+`","seed":9}`)
		recipes := r.count(`SELECT COUNT(*) FROM recipes`)
		_, err := r.create(human, e, spend.ViaHumanCreate)
		pe := wantProblem(t, err, 409, "ESTIMATE_STALE")
		if diff := cmp.Diff([]string{taken}, pe.Details["request_ids"]); diff != "" {
			t.Error(diff)
		}
		if r.count(`SELECT COUNT(*) FROM recipes`) != recipes || r.str(`SELECT COALESCE(consumed_at, '') FROM estimates WHERE id = ?`, e.ID) != "" {
			t.Error("a stale create left rows behind")
		}
	})
	t.Run("draft revision bumped", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		e := r.est(human, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1, lt.Draft2}})
		r.exec(`UPDATE recipe_drafts SET revision = revision + 1 WHERE id = ?`, lt.Draft2)
		_, err := r.create(human, e, spend.ViaHumanCreate)
		pe := wantProblem(t, err, 409, "ESTIMATE_STALE")
		if diff := cmp.Diff([]string{lt.Draft2}, pe.Details["draft_ids"]); diff != "" {
			t.Error(diff)
		}
	})
	t.Run("reused recipe rendered meanwhile", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		e := r.est(human, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}})
		src := r.str(`SELECT source_recipe_id FROM recipe_drafts WHERE id = ?`, lt.Draft1)
		lt.SeedJob(t, r.st, lt.FixedNow, lt.JobSeed{Name: "squat", RecipeID: src})
		_, err := r.create(human, e, spend.ViaHumanCreate)
		pe := wantProblem(t, err, 409, "ESTIMATE_STALE")
		if diff := cmp.Diff([]string{lt.Draft1}, pe.Details["draft_ids"]); diff != "" {
			t.Error(diff)
		}
	})
	t.Run("draft archived meanwhile", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		e := r.est(human, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}})
		r.exec(`UPDATE recipe_drafts SET status = 'archived' WHERE id = ?`, lt.Draft1)
		_, err := r.create(human, e, spend.ViaHumanCreate)
		wantProblem(t, err, 409, "ESTIMATE_STALE")
	})
}

func TestCreate_BackendChangedStale(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := r.est(human, newSeedItem(lt.TrackLuna, 1)) // under the fake backend
	svc := r.runpodService(r.goodQuote(), true, true)
	_, err := svc.Create(context.Background(), human, generation.CreateIn{Token: e.Token, Via: spend.ViaHumanCreate}, encode)
	pe := wantProblem(t, err, 409, "ESTIMATE_STALE")
	if pe.Details["reason"] != "backend_changed" {
		t.Errorf("details = %v", pe.Details)
	}
	if r.str(`SELECT COALESCE(consumed_at, '') FROM estimates WHERE id = ?`, e.ID) != "" {
		t.Error("the token was consumed")
	}
	// the same token still works under the backend it was estimated for
	if _, err := r.create(human, e, spend.ViaHumanCreate); err != nil {
		t.Errorf("create under the estimated backend: %v", err)
	}
}

func TestCreate_TokenErrors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := r.est(agent, newSeedItem(lt.TrackCitta, 1))
	// another principal's token reads as unknown, same message as a made-up one
	_, err := r.create(other, e, spend.ViaAPI)
	pe1 := wantProblem(t, err, 409, "CONFIRMATION_INVALID")
	_, err = r.svc.Create(context.Background(), other, generation.CreateIn{Token: "mmc_" + strings.Repeat("b", 52), Via: spend.ViaAPI}, encode)
	pe2 := wantProblem(t, err, 409, "CONFIRMATION_INVALID")
	if pe1.Message != pe2.Message || len(pe1.Details) != len(pe2.Details) {
		t.Errorf("distinguishable: %v / %v", pe1, pe2)
	}
	// the owner uses it; a second use with a fresh key is refused and names the batch
	c := r.mustCreate(agent, e, spend.ViaAPI)
	_, err = r.create(agent, e, spend.ViaAPI)
	pe := wantProblem(t, err, 409, "CONFIRMATION_INVALID")
	if pe.Details["reason"] != "consumed" || pe.Details["batch_id"] != c.BatchID {
		t.Errorf("same principal: %v", pe.Details)
	}
	// another principal learns nothing about the batch
	_, err = r.create(other, e, spend.ViaAPI)
	pe = wantProblem(t, err, 409, "CONFIRMATION_INVALID")
	if _, ok := pe.Details["batch_id"]; ok || pe.Details["reason"] == "consumed" {
		t.Errorf("other principal: %v", pe.Details)
	}
	if r.count(`SELECT COUNT(*) FROM batches WHERE estimate_id = ?`, e.ID) != 1 {
		t.Error("a consumed token created a second batch")
	}
}

func TestCreate_ReadsThroughTx(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := r.est(human, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}, NSeeds: 2})
	// a closed reader pool fails any read that does not go through the create transaction
	if err := r.st.R.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := r.create(human, e, spend.ViaHumanCreate)
	if err != nil {
		t.Fatalf("create reads outside its transaction: %v", err)
	}
	var n int
	if err := r.st.W.QueryRow(`SELECT COUNT(*) FROM jobs WHERE batch_id = ?`, c.BatchID).Scan(&n); err != nil || n != 2 {
		t.Errorf("jobs = %d %v", n, err)
	}
}

func TestCreateVariant_WrongEstimateShape(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	luna := lt.TrackLuna
	variant := func(c generation.CreateIn, e generation.Estimate) (generation.Created, error) {
		c.Token = e.Token
		return r.svc.Create(context.Background(), human, c, encode)
	}
	// a draft estimate is not a variant estimate
	e := r.est(human, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}})
	_, err := variant(generation.CreateIn{Via: spend.ViaHumanCreate, VariantTrackID: &luna}, e)
	pe := wantProblem(t, err, 409, "CONFIRMATION_INVALID")
	if pe.Details["reason"] != "wrong_shape" {
		t.Errorf("details = %v", pe.Details)
	}
	if r.str(`SELECT COALESCE(consumed_at, '') FROM estimates WHERE id = ?`, e.ID) != "" {
		t.Error("a wrong-shape token was consumed")
	}
	// a variant estimate of another track
	citta := lt.TrackCitta
	e = r.est(human, newSeedItem(lt.TrackLuna, 1))
	_, err = variant(generation.CreateIn{Via: spend.ViaHumanCreate, VariantTrackID: &citta}, e)
	wantProblem(t, err, 409, "CONFIRMATION_INVALID")
	// two items are not a variant estimate either
	e = r.est(human, newSeedItem(lt.TrackLuna, 1), newSeedItem(lt.TrackLuna, 1))
	_, err = variant(generation.CreateIn{Via: spend.ViaHumanCreate, VariantTrackID: &luna}, e)
	wantProblem(t, err, 409, "CONFIRMATION_INVALID")
	// the right one works
	e = r.est(human, newSeedItem(lt.TrackLuna, 2))
	if c, err := variant(generation.CreateIn{Via: spend.ViaHumanCreate, VariantTrackID: &luna}, e); err != nil || c.Takes != 2 {
		t.Errorf("variant create: %+v %v", c, err)
	}
	// create_generation accepts any shape
	e = r.est(human, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft2}})
	if _, err := r.create(human, e, spend.ViaHumanCreate); err != nil {
		t.Errorf("create_generation: %v", err)
	}
}
