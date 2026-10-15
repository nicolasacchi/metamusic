package generation_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

func (r *rig) tokens() {
	r.t.Helper()
	r.exec(`INSERT INTO api_tokens(id,name,prefix,hmac_hash,scopes,created_at,daily_budget_micro_usd,auto_approve_takes,auto_approve_micro_usd) VALUES
		('fixture-token','fixture-agent','p1','h1','read,generate','2026-10-01T00:00:00Z',200000,2,100000),
		('other-token','other-agent','p2','h2','read,generate','2026-10-02T00:00:00Z',NULL,NULL,NULL)`)
}

func TestBudget_View(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.tokens()
	ctx := context.Background()
	h, err := r.svc.Budget(ctx, human)
	if err != nil {
		t.Fatal(err)
	}
	// the fixture settled 18216 + 12000 today and billed -216; two agent takes are outstanding (2 × 38525)
	if h.Month.Settled != 30216 || h.Month.BillAdjustments != -216 || h.Month.Outstanding != 77050 || h.Month.Cap != 10_000_000 ||
		h.Month.Left != 10_000_000-30000-77050 || h.Month.Start.Format(time.RFC3339) != "2026-10-01T00:00:00Z" {
		t.Errorf("month = %+v", h.Month)
	}
	if h.Day.Settled != 30216 || h.Day.Outstanding != 0 || h.Day.Cap != 5_000_000 || h.Day.Left != 5_000_000-30216 || h.Day.Principal.Key != human.Key() {
		t.Errorf("human day = %+v", h.Day)
	}
	if h.AutoApproveTakes != 5 || h.AutoApprove != 500_000 || !h.Provider.Enabled || !h.Provider.Armed {
		t.Errorf("auto-approve %d %d provider %+v", h.AutoApproveTakes, h.AutoApprove, h.Provider)
	}
	if len(h.Caps.Tokens) != 2 || h.Caps.Caps != spend.DefaultCaps {
		t.Errorf("human caps = %+v", h.Caps)
	}
	if h.Caps.UpdatedBy.Kind != "system" || h.Caps.UpdatedBy.Name != "migration" {
		t.Errorf("updated by = %+v", h.Caps.UpdatedBy)
	}

	// an agent sees its own token's limits only, and its own day
	a, err := r.svc.Budget(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Caps.Tokens) != 1 || a.Caps.Tokens[0].TokenID != "fixture-token" || a.Caps.Tokens[0].Name != "fixture-agent" || *a.Caps.Tokens[0].Limits.DailyMicro != 200_000 {
		t.Errorf("agent tokens = %+v", a.Caps.Tokens)
	}
	if a.Day.Cap != 200_000 || a.Day.Outstanding != 77050 || a.Day.Settled != 0 || a.Day.Left != 200_000-77050 || a.Day.Principal.Name != "fixture-agent" {
		t.Errorf("agent day = %+v", a.Day)
	}
	if a.AutoApproveTakes != 2 || a.AutoApprove != 100_000 {
		t.Errorf("effective auto-approve = %d %d", a.AutoApproveTakes, a.AutoApprove)
	}
	o, _ := r.svc.Budget(ctx, other)
	if len(o.Caps.Tokens) != 1 || o.Caps.Tokens[0].TokenID != "other-token" || o.Day.Cap != 1_000_000 || o.AutoApproveTakes != 5 {
		t.Errorf("other agent = %+v / %+v", o.Caps.Tokens, o.Day)
	}
}

func TestGetSpend_Month(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	for _, month := range []string{"", "2026-10"} {
		v, err := r.svc.SpendReport(ctx, month)
		if err != nil {
			t.Fatal(err)
		}
		if v.Month != "2026-10" || !cmp.Equal(v.Days, []generation.SpendDay{{Date: "2026-10-06", Settled: 30216}}) {
			t.Errorf("%q: month %s days %+v", month, v.Month, v.Days)
		}
		if v.Settled != 30216 || v.BillAdjustments != -216 || v.Outstanding != 77050 || len(v.Batches) != 3 {
			t.Errorf("%q: totals %d %d %d, %d batches", month, v.Settled, v.BillAdjustments, v.Outstanding, len(v.Batches))
		}
		var ids []string
		for _, b := range v.Batches {
			ids = append(ids, b.ID)
		}
		if diff := cmp.Diff([]string{lt.BatchDone, lt.BatchFailed, lt.BatchAwaiting}, ids); diff != "" {
			t.Errorf("batches (-want +got):\n%s", diff)
		}
		done := v.Batches[0]
		if done.Settled != 30216 || !done.Reconciled || done.Takes != 1 || done.State != "finished" || done.CreatedBy.Key != "human:fixture" || done.CreatedBy.Kind != "human" {
			t.Errorf("done = %+v", done)
		}
		if aw := v.Batches[2]; aw.Settled != 0 || aw.Reconciled || aw.State != "awaiting_confirmation" || aw.CreatedBy.Key != lt.AgentKey || aw.Max <= 0 {
			t.Errorf("awaiting = %+v", aw)
		}
	}
	// a month without activity
	v, err := r.svc.SpendReport(ctx, "2026-09")
	if err != nil || v.Month != "2026-09" || len(v.Days) != 0 || len(v.Batches) != 0 || v.Settled != 0 || v.Outstanding != 77050 {
		t.Errorf("empty month = %+v %v", v, err)
	}
	_, err = r.svc.SpendReport(ctx, "2026-13")
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	// a bill belongs to the month of its window, not of its time of writing
	r.exec(`INSERT INTO ledger(at, kind, micro_usd, window_start, principal_id, reason) VALUES (?, 'bill', -50, ?, 'system:billing', 'late_adjust')`,
		"2026-10-20T10:00:00.000Z", "2026-09-30T22:00:00.000Z")
	r.clk.Advance(14 * 24 * time.Hour)
	sep, _ := r.svc.SpendReport(ctx, "2026-09")
	oct, _ := r.svc.SpendReport(ctx, "2026-10")
	if sep.BillAdjustments != -50 || oct.BillAdjustments != -216 {
		t.Errorf("bills: september %d october %d", sep.BillAdjustments, oct.BillAdjustments)
	}
}

func TestListLedger_Paging(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	total := r.count(`SELECT COUNT(*) FROM ledger`)
	var ids []int64
	cursor := ""
	pages := 0
	for {
		p, err := r.svc.ListLedger(ctx, generation.LedgerQuery{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, it := range p.Items {
			ids = append(ids, it.ID)
		}
		if p.NextCursor == "" {
			break
		}
		if len(p.Items) != 3 {
			t.Fatalf("a full page has %d items", len(p.Items))
		}
		cursor = p.NextCursor
		if pages > 20 {
			t.Fatal("paging does not terminate")
		}
	}
	if len(ids) != total {
		t.Fatalf("paged %d of %d rows", len(ids), total)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] >= ids[i-1] {
			t.Fatalf("not newest first: %v", ids)
		}
	}
	bills, _ := r.svc.ListLedger(ctx, generation.LedgerQuery{Kinds: []string{"bill"}})
	if len(bills.Items) != 1 || bills.Items[0].Kind != "bill" || bills.Items[0].Micro != -216 || bills.Items[0].BatchID != nil || bills.Items[0].Principal.Key != "system:billing" || bills.Items[0].Principal.Kind != "system" {
		t.Errorf("bills = %+v", bills.Items)
	}
	one, _ := r.svc.ListLedger(ctx, generation.LedgerQuery{BatchID: lt.BatchAwaiting, Kinds: []string{"reserve", "release"}})
	if len(one.Items) != 2 || one.Items[0].Principal.Key != lt.AgentKey || one.Items[0].Principal.Kind != "agent" {
		t.Errorf("batch filter = %+v", one.Items)
	}
	since := lt.FixedNow.Add(-45 * time.Minute)
	recent, _ := r.svc.ListLedger(ctx, generation.LedgerQuery{Since: &since})
	if want := r.count(`SELECT COUNT(*) FROM ledger WHERE at >= ?`, "2026-10-06T11:15:00.000Z"); len(recent.Items) != want || want < 2 {
		t.Errorf("since: %d rows, want %d (at least the two awaiting reserves)", len(recent.Items), want)
	}
	until := lt.FixedNow.Add(-45 * time.Minute)
	old, _ := r.svc.ListLedger(ctx, generation.LedgerQuery{Until: &until, Limit: 200})
	if len(old.Items)+len(recent.Items) != total || len(old.Items) == 0 {
		t.Errorf("until: %d rows + since %d of %d", len(old.Items), len(recent.Items), total)
	}
	_, err := r.svc.ListLedger(ctx, generation.LedgerQuery{Cursor: "abc"})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	// the limit is capped at 200
	big, _ := r.svc.ListLedger(ctx, generation.LedgerQuery{Limit: 5000})
	if len(big.Items) != total {
		t.Errorf("%d rows", len(big.Items))
	}
}

func micro(v int64) *money.MicroUSD { m := money.MicroUSD(v); return &m }

func TestSetSpendCaps_HistoryAndView(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.tokens()
	ctx := audit.WithPending(context.Background(), audit.Entry{At: r.clk.Now(), Principal: human, Transport: reqinfo.REST, Op: "set_spend_caps", Method: "PUT", ArgsSHA256: "x"})
	takes := 3
	v, err := r.svc.SetCaps(ctx, human, spend.CapsPatch{MonthlyMicro: micro(2_000_000), AutoApproveTakes: &takes,
		Tokens: []spend.TokenPatch{{TokenID: "fixture-token", DailyMicro: micro(300_000), AutoApproveTakes: &takes}, {TokenID: "other-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Caps.MonthlyMicro != 2_000_000 || v.Caps.AutoApproveTakes != 3 || v.Caps.BatchMicro != spend.DefaultCaps.BatchMicro || v.UpdatedBy.Key != human.Key() {
		t.Errorf("view = %+v", v)
	}
	if len(v.Tokens) != 2 || *v.Tokens[0].Limits.DailyMicro != 300_000 || v.Tokens[0].Limits.AutoApproveMicro != nil || v.Tokens[1].Limits.DailyMicro != nil {
		t.Errorf("tokens = %+v", v.Tokens)
	}
	// history: one row for the global caps and one for the token that changed (other-token was unchanged)
	rows, err := r.st.R.Query(`SELECT target, principal_id, before_json, after_json FROM spend_cap_changes ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var tg, who, b, a string
		if err := rows.Scan(&tg, &who, &b, &a); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, tg)
		if who != human.Key() || !json.Valid([]byte(b)) || !json.Valid([]byte(a)) || b == a {
			t.Errorf("history %s: %s %s -> %s", tg, who, b, a)
		}
	}
	if diff := cmp.Diff([]string{"global", "token:fixture-token"}, targets); diff != "" {
		t.Errorf("history targets (-want +got):\n%s", diff)
	}
	if got := r.str(`SELECT updated_by FROM spend_caps`); got != human.Key() {
		t.Errorf("updated_by = %s", got)
	}
	var cost int64
	if err := r.st.R.QueryRow(`SELECT cost_micro_usd FROM audit_log WHERE op = 'set_spend_caps'`).Scan(&cost); err != nil {
		t.Errorf("no audit row in the transaction: %v", err)
	}
	// clearing a token's limit with null puts it back on the global value
	if _, err := r.svc.SetCaps(context.Background(), human, spend.CapsPatch{Tokens: []spend.TokenPatch{{TokenID: "fixture-token"}}}); err != nil {
		t.Fatal(err)
	}
	if r.count(`SELECT COUNT(*) FROM api_tokens WHERE id = 'fixture-token' AND daily_budget_micro_usd IS NULL AND auto_approve_takes IS NULL`) != 1 {
		t.Error("the token override was not cleared")
	}
}

func TestSetSpendCaps_Errors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.tokens()
	ctx := context.Background()
	changes := r.count(`SELECT COUNT(*) FROM spend_cap_changes`)
	_, err := r.svc.SetCaps(ctx, human, spend.CapsPatch{MonthlyMicro: micro(1), Tokens: []spend.TokenPatch{{TokenID: "ghost"}}})
	pe := wantProblem(t, err, 422, "UNKNOWN_TOKEN")
	if pe.Details["token_id"] != "ghost" {
		t.Errorf("details = %v", pe.Details)
	}
	if r.count(`SELECT COUNT(*) FROM spend_cap_changes`) != changes || r.val(`SELECT monthly_micro_usd FROM spend_caps`) != 10_000_000 {
		t.Error("an unknown token left a partial update")
	}
	_, err = r.svc.SetCaps(ctx, human, spend.CapsPatch{MonthlyMicro: micro(1_000_000_001)})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	_, err = r.svc.SetCaps(ctx, human, spend.CapsPatch{})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	// $1000 exactly is allowed
	if _, err := r.svc.SetCaps(ctx, human, spend.CapsPatch{MonthlyMicro: micro(1_000_000_000)}); err != nil {
		t.Errorf("$1000: %v", err)
	}
}

func TestSetSpendCaps_LoweringStopsNewReserves(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	first := r.mustCreate(human, r.est(human, newSeedItem(lt.TrackLuna, 2)), spend.ViaHumanCreate)
	e := r.est(human, newSeedItem(lt.TrackLuna, 2))
	// lowering the monthly cap below what is already reserved: the reservation stays, the next create is refused
	if _, err := r.svc.SetCaps(context.Background(), human, spend.CapsPatch{MonthlyMicro: micro(1000)}); err != nil {
		t.Fatal(err)
	}
	if got := r.val(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ?`, first.BatchID); got != int64(first.Reserved) {
		t.Errorf("the existing reservation changed: %d", got)
	}
	_, err := r.create(human, e, spend.ViaHumanCreate)
	wantProblem(t, err, 402, "BUDGET_EXCEEDED")
}

func (r *rig) connectorGrants() {
	r.t.Helper()
	r.exec(`INSERT INTO connector_grants(id,client_id,subject,username,label,scopes,daily_budget_micro_usd,auto_approve_takes,auto_approve_micro_usd,
		created_at,updated_at,updated_by) VALUES
		('grant-a','c1','s1','user-a','Claude Test','read,write,generate',200000,0,50000,'2026-10-01T00:00:00Z','2026-10-01T00:00:00Z','system:oauth'),
		('grant-b','c2','s2','user-b','ChatGPT Test','read,write',NULL,NULL,NULL,'2026-10-02T00:00:00Z','2026-10-02T00:00:00Z','system:oauth')`)
}

// TestCapsView_ConnectorsVisibility: a human sees every grant, an OAuth caller its own, a PAT caller none.
func TestCapsView_ConnectorsVisibility(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.tokens()
	r.connectorGrants()
	ctx := context.Background()
	h, err := r.svc.Budget(ctx, human)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Caps.Connectors) != 2 || h.Caps.Connectors[0].GrantID != "grant-a" || h.Caps.Connectors[1].GrantID != "grant-b" {
		t.Fatalf("human connectors = %+v", h.Caps.Connectors)
	}
	if c := h.Caps.Connectors[0]; c.Label != "Claude Test" || *c.Limits.DailyMicro != 200_000 || *c.Limits.AutoApproveTakes != 0 || *c.Limits.AutoApproveMicro != 50_000 {
		t.Errorf("grant-a = %+v", c)
	}
	if c := h.Caps.Connectors[1]; c.Limits.DailyMicro != nil || c.Limits.AutoApproveTakes != nil || c.Limits.AutoApproveMicro != nil {
		t.Errorf("grant-b limits must be nil: %+v", c)
	}
	oauth := auth.Principal{Kind: auth.KindAgent, ID: "grant-a", Name: "Claude Test", Via: "oauth", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	o, err := r.svc.Budget(ctx, oauth)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Caps.Connectors) != 1 || o.Caps.Connectors[0].GrantID != "grant-a" || len(o.Caps.Tokens) != 0 {
		t.Errorf("oauth caller: connectors %+v tokens %+v", o.Caps.Connectors, o.Caps.Tokens)
	}
	a, err := r.svc.Budget(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Caps.Connectors) != 0 || a.Caps.Connectors == nil {
		t.Errorf("PAT caller must see an empty, non-nil list: %#v", a.Caps.Connectors)
	}
}

// TestGetBudget_ConnectorOwnLimits: a connector's day cap and auto-approve limits are its grant's.
func TestGetBudget_ConnectorOwnLimits(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.connectorGrants()
	ctx := context.Background()
	a := auth.Principal{Kind: auth.KindAgent, ID: "grant-a", Name: "Claude Test", Via: "oauth", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	v, err := r.svc.Budget(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if v.Day.Cap != 200_000 || v.AutoApproveTakes != 0 || v.AutoApprove != 50_000 {
		t.Errorf("grant-a: cap %d takes %d auto %d", v.Day.Cap, v.AutoApproveTakes, v.AutoApprove)
	}
	b := auth.Principal{Kind: auth.KindAgent, ID: "grant-b", Name: "ChatGPT Test", Via: "oauth", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	v, err = r.svc.Budget(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if v.Day.Cap != 1_000_000 || v.AutoApproveTakes != 5 || v.AutoApprove != 500_000 {
		t.Errorf("grant-b falls back to the global defaults: cap %d takes %d auto %d", v.Day.Cap, v.AutoApproveTakes, v.AutoApprove)
	}
}
