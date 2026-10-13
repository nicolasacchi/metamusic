package spend

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("entropy exhausted") }

func TestToken_RandFailure(t *testing.T) {
	t.Parallel()
	if _, err := New(Options{Rand: failReader{}}).NewToken(); err == nil {
		t.Error("a failing entropy source must fail the mint")
	}
}

// TestSpend_StorageFaults breaks one table at a time inside a rolled-back transaction and checks that
// every writer and reader reports the failure instead of swallowing it or writing around it.
func TestSpend_StorageFaults(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.token("a1")
	r.exec(`UPDATE api_tokens SET daily_budget_micro_usd = 100000, auto_approve_micro_usd = 5, auto_approve_takes = 2 WHERE id = 'a1'`)
	act := r.seedBatch(seedSpec{name: "fa", principal: "agent:a1", max: 100000, retry: 50000})
	r.takeReserve(act, "agent:a1", 500)
	att := r.attempt(act.Jobs[0], 1)
	pend := r.awaiting("fp")
	r.seedTokenEstimate("fe", "ftok", "agent:a1", t0.Add(time.Hour))
	old := r.seedBatch(seedSpec{name: "fo", state: "awaiting_confirmation", principal: "agent:a1", created: t0.Add(-25 * time.Hour)})
	r.takeReserve(old, "agent:a1", 50)
	fin := r.seedBatch(seedSpec{name: "ff", principal: "agent:a1"})
	r.takeReserve(fin, "agent:a1", 50)
	r.exec(`UPDATE batches SET state='finished', finished_at=? WHERE id=?`, store.FormatTime(t0), fin.Batch)
	v := func(x int64) *money.MicroUSD { m := money.MicroUSD(x); return &m }
	ms := int64(1000)
	human := auth.Principal{Kind: auth.KindHuman, ID: "h1"}
	one := 1

	type fn struct {
		name string
		must []string // tables whose loss must surface as an error
		run  func(tx *sql.Tx) error
	}
	fns := []fn{
		{"Usage", []string{"ledger", "batches"}, func(tx *sql.Tx) error { _, err := r.s.Usage(ctx, tx, "agent:a1", t0); return err }},
		{"LoadCaps", []string{"spend_caps"}, func(tx *sql.Tx) error { _, err := r.s.LoadCaps(ctx, tx); return err }},
		{"TokenLimits", []string{"api_tokens"}, func(tx *sql.Tx) error { _, err := r.s.TokenLimitsFor(ctx, tx, "agent:a1"); return err }},
		{"SetCaps", []string{"spend_caps", "spend_cap_changes", "api_tokens"}, func(tx *sql.Tx) error {
			_, _, err := r.s.SetCaps(ctx, tx, human, CapsPatch{MonthlyMicro: v(7), Tokens: []TokenPatch{{TokenID: "a1", DailyMicro: v(9), AutoApproveTakes: &one, AutoApproveMicro: v(3)}}})
			return err
		}},
		{"ConsumeToken", []string{"estimates"}, func(tx *sql.Tx) error { _, err := r.s.ConsumeToken(ctx, tx, "ftok", "agent:a1", "bx"); return err }},
		{"ConsumeTokenMiss", []string{"estimates"}, func(tx *sql.Tx) error { _, err := r.s.ConsumeToken(ctx, tx, "nope", "agent:a1", "bx"); return err }},
		{"ReserveTopUp", []string{"batches", "ledger", "spend_caps", "api_tokens"}, func(tx *sql.Tx) error {
			_, err := r.s.ReserveTopUp(ctx, tx, TopUp{BatchID: act.Batch, JobID: act.Jobs[0], Reason: "topup_price", Amount: 5, Lease: r.lease})
			return err
		}},
		{"SettleAttempt", []string{"job_attempts", "ledger"}, func(tx *sql.Tx) error {
			_, err := r.s.SettleAttempt(ctx, tx, AttemptSettle{BatchID: act.Batch, JobID: act.Jobs[0], AttemptID: att, PrincipalID: "agent:a1",
				Basis: BasisMeasured, ExecutionMS: &ms, PricePPH: 690000, Lease: r.lease})
			return err
		}},
		{"ApproveBatch", []string{"approvals", "batches"}, func(tx *sql.Tx) error { _, err := r.s.ApproveBatch(ctx, tx, pend.Batch, human, ""); return err }},
		{"ApproveBatchMiss", []string{"approvals"}, func(tx *sql.Tx) error { _, err := r.s.ApproveBatch(ctx, tx, "ghost", human, ""); return err }},
		{"RejectBatch", []string{"approvals", "batches", "ledger"}, func(tx *sql.Tx) error { _, err := r.s.RejectBatch(ctx, tx, pend.Batch, human, ""); return err }},
		{"RejectBatchMiss", []string{"approvals"}, func(tx *sql.Tx) error { _, err := r.s.RejectBatch(ctx, tx, "ghost", human, ""); return err }},
		{"RequestApproval", []string{"batches", "approvals"}, func(tx *sql.Tx) error { _, err := r.s.RequestApproval(ctx, tx, pend.Batch, agentP); return err }},
		{"FinishBatch", []string{"batches", "ledger"}, func(tx *sql.Tx) error {
			_, err := r.s.FinishBatch(ctx, tx, FinishInput{BatchID: act.Batch, OverheadMicro: 1, Lease: r.lease})
			return err
		}},
		{"ExpireApproval", []string{"approvals", "batches", "ledger"}, func(tx *sql.Tx) error { return r.s.ExpireApproval(ctx, tx, old.Batch, r.lease) }},
		{"ReconcileBatch", []string{"batches", "ledger"}, func(tx *sql.Tx) error {
			_, err := r.s.ReconcileBatch(ctx, tx, fin.Batch, "reconciled", r.lease)
			return err
		}},
		{"RecordRTF", []string{"rtf_stats"}, func(tx *sql.Tx) error { return r.s.RecordRTF(ctx, tx, "A5000", 1000, 1000) }},
		{"RecordRTFNew", []string{"rtf_stats"}, func(tx *sql.Tx) error { return r.s.RecordRTF(ctx, tx, "NEW", 1000, 1000) }},
		{"RTFMilli", []string{"rtf_stats"}, func(tx *sql.Tx) error { _, _, err := r.s.RTFMilli(ctx, tx, []string{"A5000"}); return err }},
		{"PendingExpired", []string{"approvals"}, func(tx *sql.Tx) error { _, err := r.s.PendingExpired(ctx, tx, 5); return err }},
		{"Append", []string{"ledger"}, func(tx *sql.Tx) error {
			return r.s.Ledger().Append(ctx, tx, Entry{Kind: KindReserve, MicroUSD: 1, BatchID: &act.Batch, JobID: &act.Jobs[0], PrincipalID: "agent:a1", Reason: "take"})
		}},
		{"Outstanding", []string{"ledger"}, func(tx *sql.Tx) error { _, err := r.s.Ledger().Outstanding(ctx, tx, act.Batch); return err }},
		{"Remaining", []string{"ledger"}, func(tx *sql.Tx) error {
			if _, err := r.s.Ledger().ExecutionRemaining(ctx, tx, act.Jobs[0]); err != nil {
				return err
			}
			_, err := r.s.Ledger().OverheadRemaining(ctx, tx, act.Batch)
			return err
		}},
	}
	// healthy run first: nothing may fail without a fault (rolled back each time)
	run := func(f fn, prep func(tx *sql.Tx)) error {
		tx, err := r.st.W.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer r.s.RollbackTx(tx)
		if prep != nil {
			prep(tx)
		}
		return f.run(tx)
	}
	for _, f := range fns {
		err := run(f, nil)
		if err != nil && f.name != "ConsumeTokenMiss" && f.name != "ApproveBatchMiss" && f.name != "RejectBatchMiss" {
			t.Errorf("%s healthy: %v", f.name, err)
		}
	}
	// write faults: a table that refuses INSERT or UPDATE (reads still work); no function may panic
	for _, f := range fns {
		for _, tbl := range f.must {
			for _, op := range []string{"INSERT", "UPDATE"} {
				_ = run(f, func(tx *sql.Tx) {
					if _, err := tx.Exec(`CREATE TEMP TRIGGER fault_` + tbl + `_` + op + ` BEFORE ` + op + ` ON main.` + tbl + ` BEGIN SELECT RAISE(ABORT, 'fault'); END`); err != nil {
						t.Fatalf("fault %s %s: %v", tbl, op, err)
					}
				})
			}
		}
	}
	for _, f := range fns {
		for _, tbl := range f.must {
			err := run(f, func(tx *sql.Tx) {
				if _, err := tx.Exec(`ALTER TABLE ` + tbl + ` RENAME TO ` + tbl + `_gone`); err != nil {
					t.Fatalf("break %s: %v", tbl, err)
				}
			})
			if err == nil {
				t.Errorf("%s survived losing %s", f.name, tbl)
			}
		}
	}
}
