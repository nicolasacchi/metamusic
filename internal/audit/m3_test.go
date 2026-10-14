package audit_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func newStoreAndDB(t *testing.T) (*audit.Store, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := 0
	return audit.NewStore(st, func() string { n++; return "id-" + string(rune('a'+n)) }), st
}

func TestAudit_InsertTx(t *testing.T) {
	t.Parallel()
	s, st := newStoreAndDB(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	e := audit.Entry{At: at, Principal: auth.Principal{Kind: auth.KindAgent, ID: "t1", Name: "bot"}, Transport: reqinfo.REST,
		Op: "create_generation", Method: "POST", ArgsSHA256: "abc", Status: 201, Idempotency: audit.IdemStored,
		CreatedIDs: []string{"batch-1"}, Duration: 7 * time.Millisecond, CostMicroUSD: money.MicroUSD(691731), ID: "row-1"}

	count := func() int {
		var n int
		if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM audit_log`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// rollback ⇒ no row (the mark is set by the statement; the handler resets it on its error path)
	ctxM, marks := idem.WithMarks(ctx)
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.InsertTx(ctxM, tx, e); err != nil {
		t.Fatal(err)
	}
	if !marks.Audited() || marks.Completed() {
		t.Fatalf("marks after InsertTx: audited=%v completed=%v", marks.Audited(), marks.Completed())
	}
	marks.Reset()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count() != 0 || marks.Audited() {
		t.Fatalf("rollback left %d rows / audited=%v", count(), marks.Audited())
	}

	// commit ⇒ one row with the cost
	tx, _ = st.W.BeginTx(ctx, nil)
	if err := audit.InsertTx(ctxM, tx, e); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	p, err := s.List(ctx, audit.Filter{})
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("list: %v %+v", err, p)
	}
	r := p.Items[0]
	if r.ID != "row-1" || r.CostMicroUSD != 691731 || r.Status != 201 || r.Idempotency != "stored" || r.PrincipalID != "agent:t1" ||
		r.CreatedIDs[0] != "batch-1" || r.DurationMS != 7 || r.Op != "create_generation" {
		t.Fatalf("row = %+v", r)
	}

	// without an id the transaction path generates a UUIDv7; without marks installed it still works
	tx, _ = st.W.BeginTx(ctx, nil)
	e2 := e
	e2.ID = ""
	if err := audit.InsertTx(ctx, tx, e2); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	p, _ = s.List(ctx, audit.Filter{})
	gen := 0
	for _, it := range p.Items {
		if len(it.ID) == 36 {
			gen++
		}
	}
	if len(p.Items) != 2 || gen != 1 {
		t.Fatalf("generated id: %+v", p.Items)
	}

	// Store.Insert writes the cost too (one SQL text for both paths)
	if err := s.Insert(ctx, audit.Entry{At: at, Principal: auth.CLI, Transport: reqinfo.CLI, Op: "x", Method: "SYSTEM", Status: 200, CostMicroUSD: 5}); err != nil {
		t.Fatal(err)
	}
	var cost int64
	if err := st.R.QueryRowContext(ctx, `SELECT cost_micro_usd FROM audit_log WHERE op='x'`).Scan(&cost); err != nil || cost != 5 {
		t.Fatalf("cost = %d %v", cost, err)
	}
}

func TestAudit_PendingContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, ok := audit.PendingFrom(ctx); ok {
		t.Fatal("pending on empty context")
	}
	e := audit.Entry{Op: "create_variant", Method: "POST", ArgsSHA256: "sha", Transport: reqinfo.MCP}
	got, ok := audit.PendingFrom(audit.WithPending(ctx, e))
	if !ok || got.Op != "create_variant" || got.ArgsSHA256 != "sha" || got.Transport != reqinfo.MCP {
		t.Fatalf("PendingFrom = %+v %v", got, ok)
	}
}

// TestAudit_SystemOpsTable pins the D-M3-22 op list and the fill values of M2a's NOT NULL columns.
func TestAudit_SystemOpsTable(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"runner":     "submit topup cancel batch_finish fail_remainder",
		"reconciler": "adopt ghost settle_time_bound approval_expired trip purge_queue scale_to_zero revert_drift billing_window late_adjust hold_release upload_sweep",
		"cli":        "runpod-arm runpod-disarm runpod-reset-trip runpod-provision restore-disarm",
	}
	var wantOps []string
	for actor, ops := range want {
		for _, op := range strings.Fields(ops) {
			wantOps = append(wantOps, actor+":"+op)
		}
	}
	if len(audit.SystemOps) != len(wantOps) || len(wantOps) != 22 {
		t.Fatalf("SystemOps has %d entries, D-M3-22 lists %d", len(audit.SystemOps), len(wantOps))
	}
	for _, op := range wantOps {
		if !audit.IsSystemOp(op) {
			t.Errorf("%s missing", op)
		}
	}
	if audit.IsSystemOp("runner:unknown") || audit.IsSystemOp("create_variant") {
		t.Error("IsSystemOp accepted a non-system op")
	}
	if _, err := audit.SystemEntry("runner:unknown", time.Now(), nil, 0, 0, false); err == nil {
		t.Error("unknown system op accepted")
	}

	s, st := newStoreAndDB(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, op := range audit.SystemOps {
		d := map[string]any{"b": 2, "a": []int{1}}
		e, err := audit.SystemEntry(op, at, d, 1500*time.Millisecond, money.MicroUSD(38334), false)
		if err != nil {
			t.Fatal(op, err)
		}
		if err := s.Insert(ctx, e); err != nil {
			t.Fatalf("%s: NOT NULL fill values rejected: %v", op, err)
		}
	}
	// failed rows
	f, _ := audit.SystemEntry("reconciler:trip", at, nil, time.Second, 0, true)
	if err := s.Insert(ctx, f); err != nil {
		t.Fatal(err)
	}

	p, err := s.List(ctx, audit.Filter{Limit: 200})
	if err != nil || len(p.Items) != 23 {
		t.Fatalf("list: %d %v", len(p.Items), err)
	}
	// canonical details ⇒ key order irrelevant
	a1, _ := audit.SystemEntry("runner:submit", at, map[string]any{"a": 1, "b": 2}, 0, 0, false)
	a2, _ := audit.SystemEntry("runner:submit", at, map[string]any{"b": 2, "a": 1}, 0, 0, false)
	if a1.ArgsSHA256 != a2.ArgsSHA256 || len(a1.ArgsSHA256) != 64 {
		t.Errorf("args sha: %q vs %q", a1.ArgsSHA256, a2.ArgsSHA256)
	}
	var kind, pid, transport, method, client, idemv string
	var status int
	var cost int64
	var code sql.NullString
	for op, wantRow := range map[string]struct {
		pid, transport string
		status         int
	}{
		"runner:submit":      {"system:runner", "runner", 200},
		"reconciler:adopt":   {"system:reconciler", "runner", 200},
		"cli:runpod-arm":     {"system:cli", "cli", 200},
		"cli:restore-disarm": {"system:cli", "cli", 200},
	} {
		err := st.R.QueryRowContext(ctx, `SELECT principal_kind, principal_id, transport, method, client, idempotency, status, cost_micro_usd, error_code
			FROM audit_log WHERE op=?`, op).Scan(&kind, &pid, &transport, &method, &client, &idemv, &status, &cost, &code)
		if err != nil {
			t.Fatal(op, err)
		}
		if kind != "system" || pid != wantRow.pid || transport != wantRow.transport || method != "SYSTEM" || client != "" ||
			idemv != "none" || status != wantRow.status || cost != 38334 || code.Valid {
			t.Errorf("%s: %s %s %s %s %q %s %d %d %v", op, kind, pid, transport, method, client, idemv, status, cost, code)
		}
	}
	var fs int
	var fc string
	if err := st.R.QueryRowContext(ctx, `SELECT status, error_code FROM audit_log WHERE op='reconciler:trip' AND status=500`).Scan(&fs, &fc); err != nil || fc == "" {
		t.Fatalf("failed row: %v %q", err, fc)
	}
}
