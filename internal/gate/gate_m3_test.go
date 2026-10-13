package gate_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/money"
)

// txOp is a spend-shaped operation (D-M3-21): it completes its idempotency row and writes its audit row
// inside its own transaction. mode picks the path.
type txOp struct {
	r        *rig
	mode     atomic.Value // string: "ok" | "rollback" | "noaudit" | "sawmarks"
	sawMarks atomic.Bool
	handled  atomic.Int64
	before   func() // runs inside the handler, before CompleteTx
}

func (o *txOp) register(r *rig, a huma.API) {
	o.r = r
	op := huma.Operation{OperationID: "t_spend", Method: "POST", Path: "/api/t/spend", Summary: "s", Description: "d", DefaultStatus: 201}
	api.Register(a, op, api.Meta{Scope: auth.ScopeGenerate, Cost: "spend", MCP: "tool", UI: "none:test", Idempotency: "create"},
		func(ctx context.Context, in *createIn) (*okOut, error) {
			o.handled.Add(1)
			out := &okOut{}
			out.Body.OK, out.Body.Name = true, in.Body.Name
			pk, key, gen, ok := idem.KeyFrom(ctx)
			if !ok {
				return nil, errors.New("no idempotency identity in the context")
			}
			marks := idem.MarksFrom(ctx)
			if marks == nil {
				return nil, errors.New("gate did not install marks")
			}
			pending, ok := audit.PendingFrom(ctx)
			if !ok || pending.Op != "t_spend" || pending.ArgsSHA256 == "" || pending.Principal.Kind != auth.KindAgent {
				return nil, errors.New("gate did not provide the pending audit entry")
			}
			mode, _ := o.mode.Load().(string)
			if o.before != nil {
				o.before()
			}
			tx, err := r.st.W.BeginTx(ctx, nil)
			if err != nil {
				return nil, err
			}
			committed := false
			defer func() {
				if !committed {
					marks.Reset()
					_ = tx.Rollback()
				}
			}()
			body, err := api.EncodeResponse(out.Body)
			if err != nil {
				return nil, err
			}
			if err := idem.CompleteTx(ctx, tx, pk, key, gen, 201, "application/json", body); err != nil {
				if errors.Is(err, idem.ErrLeaseLost) {
					return nil, api.Err(409, "IDEMPOTENCY_IN_PROGRESS", "reclaimed", nil)
				}
				return nil, err
			}
			if mode != "noaudit" {
				e := pending
				e.Status, e.Idempotency, e.CreatedIDs, e.CostMicroUSD = 201, audit.IdemStored, []string{"batch-x"}, money.MicroUSD(691731)
				if err := audit.InsertTx(ctx, tx, e); err != nil {
					return nil, err
				}
			}
			o.sawMarks.Store(marks.Completed() && (mode == "noaudit" || marks.Audited()))
			if mode == "rollback" {
				return nil, api.Err(402, "BUDGET_EXCEEDED", "over the cap", nil)
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			committed = true
			return out, nil
		})
}

func spendReq(p auth.Principal, key, body string) req {
	return req{p: p, method: "POST", path: "/api/t/spend", body: body, hdr: map[string]string{"Idempotency-Key": key}}
}

func newTxRig(t *testing.T) (*rig, *txOp) {
	t.Helper()
	o := &txOp{}
	o.mode.Store("ok")
	r := newRig(t, rigOpts{extra: o.register})
	return r, o
}

var spender = agent("s1", auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)

func (r *rig) idemState(key string) (state string, found bool) {
	err := r.st.R.QueryRow(`SELECT state FROM idempotency WHERE key = ?`, key).Scan(&state)
	return state, err == nil
}

// TestGate_MarksVisibleAfterHandler: marks set inside the handler's transaction reach the gate, and are
// cleared on rollback (the refusal is then audited by the gate and the key released).
func TestGate_MarksVisibleAfterHandler(t *testing.T) {
	t.Parallel()
	r, o := newTxRig(t)
	w := r.do(spendReq(spender, "k1", `{"name":"x"}`))
	if w.Code != 201 || !o.sawMarks.Load() {
		t.Fatalf("%d %s sawMarks=%v", w.Code, w.Body, o.sawMarks.Load())
	}
	rows := r.rows()
	if len(rows) != 1 || rows[0].Status != 201 || rows[0].CostMicroUSD != 691731 || rows[0].Idempotency != "stored" ||
		len(rows[0].CreatedIDs) != 1 || rows[0].CreatedIDs[0] != "batch-x" {
		t.Fatalf("audit rows: %+v", rows)
	}
	if st, _ := r.idemState("k1"); st != "done" {
		t.Fatalf("idempotency state = %q", st)
	}

	// rollback path: the marks were set, then reset by the handler; the gate audits the refusal itself
	o.mode.Store("rollback")
	w = r.do(spendReq(spender, "k2", `{"name":"y"}`))
	if w.Code != 402 || code(w) != "BUDGET_EXCEEDED" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	rows = r.rows() // newest first
	if len(rows) != 2 || rows[0].Status != 402 || rows[0].ErrorCode != "BUDGET_EXCEEDED" || rows[0].CostMicroUSD != 0 {
		t.Fatalf("refusal must be audited once by the gate: %+v", rows)
	}
	if _, found := r.idemState("k2"); found {
		t.Fatal("refused request must release its idempotency key")
	}
	// the key is reusable after the refusal
	o.mode.Store("ok")
	if w = r.do(spendReq(spender, "k2", `{"name":"y"}`)); w.Code != 201 {
		t.Fatalf("retry after refusal: %d %s", w.Code, w.Body)
	}
}

// TestGate_SkipsWhenMarked: an operation that audited and completed in its transaction gets no second
// audit row and no gate-side Complete (the stored body is the one written in the transaction).
func TestGate_SkipsWhenMarked(t *testing.T) {
	t.Parallel()
	r, _ := newTxRig(t)
	w := r.do(spendReq(spender, "k1", `{"name":"x"}`))
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if n := len(r.rows()); n != 1 {
		t.Fatalf("audit rows = %d, want exactly the in-transaction one", n)
	}
	// the stored replay body is byte-identical to the first answer (EncodeResponse == Huma's body)
	var stored []byte
	if err := r.st.R.QueryRow(`SELECT response FROM idempotency WHERE key='k1'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) != w.Body.String() {
		t.Fatalf("stored %q != answered %q", stored, w.Body.String())
	}

	// an operation that completes but does not audit: the gate still audits (once) and does not Complete again
	r2, o2 := newTxRig(t)
	o2.mode.Store("noaudit")
	w = r2.do(spendReq(spender, "k1", `{"name":"x"}`))
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	rows := r2.rows()
	if len(rows) != 1 || rows[0].Idempotency != "stored" || rows[0].CostMicroUSD != 0 {
		t.Fatalf("gate-audited row: %+v", rows)
	}
}

// TestGate_ReplayAuditedByGate: a replay never reaches the handler, so the gate audits it as before.
func TestGate_ReplayAuditedByGate(t *testing.T) {
	t.Parallel()
	r, o := newTxRig(t)
	w1 := r.do(spendReq(spender, "k1", `{"name":"x"}`))
	w2 := r.do(spendReq(spender, "k1", `{ "name": "x" }`))
	if w1.Code != 201 || w2.Code != 201 || w2.Body.String() != w1.Body.String() || w2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %q hdr=%v", w2.Code, w2.Body, w2.Header())
	}
	if o.handled.Load() != 1 {
		t.Fatalf("handler ran %d times", o.handled.Load())
	}
	rows := r.rows() // newest first: replay, first
	if len(rows) != 2 || rows[0].Idempotency != "replayed" || rows[1].Idempotency != "stored" || rows[1].CostMicroUSD != 691731 || rows[0].CostMicroUSD != 0 {
		t.Fatalf("rows: %+v", rows)
	}
}

// TestGate_ReclaimedLeaseRefusesCompletion: another request reclaimed the key while this handler ran, so
// CompleteTx (fenced by the lease generation) refuses; the handler answers 409 and commits nothing.
func TestGate_ReclaimedLeaseRefusesCompletion(t *testing.T) {
	t.Parallel()
	r, o := newTxRig(t)
	sha, _ := idem.RequestSHA("t_spend", "POST", "/api/t/spend", nil, []byte(`{"name":"x"}`))
	o.before = func() {
		r.clk.Advance(idem.Lease + 5e9) // the lease lapses while the handler is still running
		if b, err := r.idem.Begin(context.Background(), spender.Key(), "kz", "t_spend", sha, false); err != nil || b.Outcome != idem.Proceed {
			t.Errorf("reclaim: %+v %v", b, err)
		}
	}
	w := r.do(spendReq(spender, "kz", `{"name":"x"}`))
	if w.Code != 409 || code(w) != "IDEMPOTENCY_IN_PROGRESS" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var n int
	if err := r.st.R.QueryRow(`SELECT count(*) FROM idempotency WHERE key='kz' AND state='done'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("completed rows = %d %v", n, err)
	}
	rows := r.rows()
	if len(rows) != 1 || rows[0].Status != 409 || rows[0].CostMicroUSD != 0 {
		t.Fatalf("audit rows: %+v", rows)
	}
}
