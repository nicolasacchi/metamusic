package spend

import (
	"context"
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestLedger_Reasons(t *testing.T) {
	t.Parallel()
	want := []ReasonInfo{
		{"reserve", "take", "execution", "positive"}, {"reserve", "attempt_overhead", "overhead", "positive"},
		{"reserve", "topup_ambiguous", "execution", "positive"}, {"reserve", "topup_requeue", "execution", "positive"},
		{"reserve", "topup_price", "execution", "positive"},
		{"settle", "execution", "execution", "nonnegative"}, {"settle", "execution_unknown", "execution", "nonnegative"},
		{"settle", "excess", "", "positive"}, {"settle", "overhead", "overhead", "nonnegative"},
		{"residual", "held", "", "nonnegative"},
		{"release", "never_ran", "execution", "nonnegative"}, {"release", "not_accepted", "overhead", "nonnegative"},
		{"release", "rejected", "", "nonnegative"}, {"release", "expired", "", "nonnegative"},
		{"release", "reconciled", "", "nonnegative"}, {"release", "hold_elapsed", "", "nonnegative"},
		{"bill", "reconcile", "", "any"}, {"bill", "late_adjust", "", "any"},
	}
	if got := LedgerReasons(); !slices.Equal(got, want) {
		t.Fatalf("reason table\n got %v\nwant %v", got, want)
	}
	r := newRig(t)
	ctx := context.Background()
	sd := r.seedBatch(seedSpec{name: "reasons"})
	ledger := r.s.Ledger()
	app := func(es ...Entry) error { return r.tx(func(tx *sql.Tx) error { return ledger.Append(ctx, tx, es...) }) }
	e := func(kind, reason string, micro int64) Entry {
		return Entry{Kind: kind, Reason: reason, MicroUSD: money.MicroUSD(micro), BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "human:h1"}
	}
	// signs
	if err := app(e("reserve", "take", 0)); !errors.Is(err, ErrBadEntry) {
		t.Errorf("zero reserve: %v", err)
	}
	if err := app(e("reserve", "take", -5)); !errors.Is(err, ErrBadEntry) {
		t.Errorf("negative reserve: %v", err)
	}
	if err := app(e("release", "rejected", -1)); !errors.Is(err, ErrBadEntry) {
		t.Errorf("negative release: %v", err)
	}
	if err := app(e("settle", "excess", 0)); !errors.Is(err, ErrBadEntry) {
		t.Errorf("zero excess: %v", err)
	}
	// unknown reason, kind/reason mismatch
	for _, bad := range []Entry{e("reserve", "execution", 5), e("settle", "take", 5), e("nope", "take", 5), e("reserve", "", 5)} {
		if err := app(bad); !errors.Is(err, ErrBadEntry) {
			t.Errorf("%s/%s: %v", bad.Kind, bad.Reason, err)
		}
	}
	// shape: batch required, execution pot needs a job, bill shape
	noBatch := e("reserve", "take", 5)
	noBatch.BatchID = nil
	noJob := e("reserve", "take", 5)
	noJob.JobID = nil
	noWho := e("reserve", "take", 5)
	noWho.PrincipalID = ""
	win := "2026-10-01T00:00:00.000Z"
	withWindow := e("reserve", "take", 5)
	withWindow.WindowStart = &win
	for name, bad := range map[string]Entry{"no batch": noBatch, "no job": noJob, "no principal": noWho, "window on non-bill": withWindow,
		"bill with batch":      {Kind: "bill", Reason: "reconcile", MicroUSD: 1, BatchID: &sd.Batch, PrincipalID: BillingPrincipal, WindowStart: &win},
		"bill wrong principal": {Kind: "bill", Reason: "reconcile", MicroUSD: 1, PrincipalID: "human:h1", WindowStart: &win},
		"bill no window":       {Kind: "bill", Reason: "reconcile", MicroUSD: 1, PrincipalID: BillingPrincipal}} {
		if err := app(bad); !errors.Is(err, ErrBadEntry) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a valid overhead reserve needs no job; a valid bill (negative) is accepted
	oh := e("reserve", "attempt_overhead", 5)
	oh.JobID = nil
	if err := app(oh); err != nil {
		t.Errorf("overhead without job: %v", err)
	}
	if err := app(Entry{Kind: "bill", Reason: "late_adjust", MicroUSD: -9, PrincipalID: BillingPrincipal, WindowStart: &win}); err != nil {
		t.Errorf("negative bill: %v", err)
	}
	if got := r.count(`SELECT COUNT(*) FROM ledger`); got != 2 {
		t.Errorf("rows = %d, want only the two valid ones", got)
	}
}

func TestLedger_Pots(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	sd := r.seedBatch(seedSpec{name: "pots"})
	l := r.s.Ledger()
	row := func(kind, reason string, micro int64) Entry {
		return Entry{Kind: kind, Reason: reason, MicroUSD: money.MicroUSD(micro), BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "human:h1"}
	}
	r.must(func(tx *sql.Tx) error {
		return l.Append(ctx, tx, row("reserve", "take", 50), row("reserve", "attempt_overhead", 100))
	})
	// an overhead settle never consumes the execution pot: 101 would fit the batch total (150) but not the pot
	err := r.tx(func(tx *sql.Tx) error { return l.Append(ctx, tx, row("settle", "overhead", 101)) })
	if !errors.Is(err, ErrLedgerInvariant) {
		t.Fatalf("overhead beyond its pot: %v", err)
	}
	// and an execution settle never consumes the overhead pot
	err = r.tx(func(tx *sql.Tx) error { return l.Append(ctx, tx, row("settle", "execution", 51)) })
	if !errors.Is(err, ErrLedgerInvariant) {
		t.Fatalf("execution beyond its pot: %v", err)
	}
	r.must(func(tx *sql.Tx) error {
		return l.Append(ctx, tx, row("settle", "overhead", 100), row("settle", "execution_unknown", 50))
	})
	if err := r.tx(func(tx *sql.Tx) error { return l.Append(ctx, tx, row("release", "never_ran", 1)) }); !errors.Is(err, ErrLedgerInvariant) {
		t.Errorf("release from an empty execution pot: %v", err)
	}
	if err := r.tx(func(tx *sql.Tx) error { return l.Append(ctx, tx, row("release", "not_accepted", 1)) }); !errors.Is(err, ErrLedgerInvariant) {
		t.Errorf("release from an empty overhead pot: %v", err)
	}
	r.must(func(tx *sql.Tx) error {
		if v, err := l.ExecutionRemaining(ctx, tx, sd.Jobs[0]); err != nil || v != 0 {
			t.Errorf("execution remaining %d %v", v, err)
		}
		if v, err := l.OverheadRemaining(ctx, tx, sd.Batch); err != nil || v != 0 {
			t.Errorf("overhead remaining %d %v", v, err)
		}
		if v, err := l.Outstanding(ctx, tx, sd.Batch); err != nil || v != 0 {
			t.Errorf("outstanding %d %v", v, err)
		}
		return nil
	})
}

func TestLedger_InvariantRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	sd := r.seedBatch(seedSpec{name: "inv"})
	l := r.s.Ledger()
	row := func(kind, reason string, micro int64) Entry {
		return Entry{Kind: kind, Reason: reason, MicroUSD: money.MicroUSD(micro), BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "human:h1"}
	}
	r.must(func(tx *sql.Tx) error { return l.Append(ctx, tx, row("reserve", "take", 100)) })
	// a failing multi-row append writes nothing, not even its valid first row, and leaves the tx usable
	r.must(func(tx *sql.Tx) error {
		err := l.Append(ctx, tx, row("settle", "execution", 60), row("settle", "execution", 60))
		if !errors.Is(err, ErrLedgerInvariant) {
			t.Errorf("beyond the pot: %v", err)
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ledger`).Scan(&n); err != nil || n != 1 {
			t.Errorf("rows after a refused append = %d (%v)", n, err)
		}
		return l.Append(ctx, tx, row("settle", "excess", 5000)) // excess is allowed beyond everything
	})
	if got := r.count(`SELECT COUNT(*) FROM ledger`); got != 2 {
		t.Errorf("rows = %d", got)
	}
	// batch level: releases count against reserves too
	if err := r.tx(func(tx *sql.Tx) error { return l.Append(ctx, tx, row("release", "rejected", 101)) }); !errors.Is(err, ErrLedgerInvariant) {
		t.Errorf("batch release beyond reserves: %v", err)
	}
	if err := r.tx(func(tx *sql.Tx) error { return l.Append(ctx, tx, row("release", "rejected", 100)) }); err != nil {
		t.Errorf("release of the full reserve: %v", err)
	}
}

func (r *rig) takeReserve(sd seeded, principal string, micro money.MicroUSD) {
	r.t.Helper()
	r.must(func(tx *sql.Tx) error {
		return r.s.Ledger().Append(context.Background(), tx, Entry{Kind: KindReserve, MicroUSD: micro, BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: principal, Reason: "take"})
	})
}

func (r *rig) settle(sd seeded, att string, basis string, ms int64) (SettleResult, error) {
	var res SettleResult
	err := r.tx(func(tx *sql.Tx) (err error) {
		a := AttemptSettle{BatchID: sd.Batch, JobID: sd.Jobs[0], AttemptID: att, PrincipalID: "human:h1", Basis: basis,
			TimeoutS: 186, KillSlackS: 15, PricePPH: 690000, Lease: r.lease}
		if basis == BasisMeasured {
			a.ExecutionMS = &ms
		}
		res, err = r.s.SettleAttempt(ctx0, tx, a)
		return
	})
	return res, err
}

var ctx0 = context.Background()

func TestSettleAttempt_Split(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sd := r.seedBatch(seedSpec{name: "split"})
	take := money.ForSeconds(186+15, 690000)
	r.takeReserve(sd, "human:h1", take)
	att := r.attempt(sd.Jobs[0], 1)
	res, err := r.settle(sd, att, BasisMeasured, 250_000) // 250 s > the 201 s reserve
	if err != nil {
		t.Fatal(err)
	}
	prov := money.ForMillis(250_000, 690000)
	if res.Provisional != prov || res.Settled != take || res.Excess != prov-take || res.Reason != "execution" {
		t.Errorf("split %+v (take %d, provisional %d)", res, take, prov)
	}
	if r.val(`SELECT micro_usd FROM ledger WHERE reason='execution'`) != int64(take) || r.val(`SELECT micro_usd FROM ledger WHERE reason='excess'`) != int64(prov-take) {
		t.Error("ledger rows do not match the split")
	}
	if r.val(`SELECT settled FROM job_attempts WHERE id=?`, att) != 1 {
		t.Error("attempt not marked settled")
	}
	if _, err := r.settle(sd, att, BasisMeasured, 1); !errors.Is(err, ErrAlreadySettled) {
		t.Errorf("second settle: %v", err)
	}
	if _, err := r.settle(sd, "ghost", BasisTimeBound, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown attempt: %v", err)
	}
	// time bound: execution_unknown at timeout_s + KillSlackS
	sd2 := r.seedBatch(seedSpec{name: "split2"})
	r.takeReserve(sd2, "human:h1", take)
	res, err = r.settle(sd2, r.attempt(sd2.Jobs[0], 1), BasisTimeBound, 0)
	if err != nil || res.Reason != "execution_unknown" || res.Settled != take || res.Excess != 0 {
		t.Errorf("time bound %+v %v", res, err)
	}
	// bad inputs
	sd3 := r.seedBatch(seedSpec{name: "split3"})
	a3 := r.attempt(sd3.Jobs[0], 1)
	if _, err := r.settle(sd3, a3, "guess", 0); !errors.Is(err, ErrBadEntry) {
		t.Errorf("bad basis: %v", err)
	}
	if err := r.tx(func(tx *sql.Tx) error {
		_, err := r.s.SettleAttempt(ctx0, tx, AttemptSettle{BatchID: sd3.Batch, JobID: sd3.Jobs[0], AttemptID: a3, Basis: BasisMeasured, Lease: r.lease})
		return err
	}); !errors.Is(err, ErrBadEntry) {
		t.Errorf("measured without execution_ms: %v", err)
	}
	// a lost lease rolls everything back
	r.takeReserve(sd3, "human:h1", take)
	lost := r.lease
	lost.Owner = "intruder"
	if err := r.tx(func(tx *sql.Tx) error {
		_, err := r.s.SettleAttempt(ctx0, tx, AttemptSettle{BatchID: sd3.Batch, JobID: sd3.Jobs[0], AttemptID: a3, Basis: BasisTimeBound, TimeoutS: 1, PricePPH: 1, Lease: lost})
		return err
	}); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("lost lease: %v", err)
	}
	if r.val(`SELECT settled FROM job_attempts WHERE id=?`, a3) != 0 {
		t.Error("settled flag set without the lease")
	}
}

func TestSettle_KillSlackNoExcess(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	take := money.ForSeconds(186+15, 690000)
	for name, tc := range map[string]struct {
		ms     int64
		excess money.MicroUSD
	}{
		"timeout+14s": {(186 + 14) * 1000, 0},
		"timeout+15s": {(186 + 15) * 1000, 0},
		"timeout+16s": {(186 + 16) * 1000, money.ForSeconds(186+16, 690000) - take},
	} {
		sd := r.seedBatch(seedSpec{name: "ks-" + name})
		r.takeReserve(sd, "human:h1", take)
		res, err := r.settle(sd, r.attempt(sd.Jobs[0], 1), BasisMeasured, tc.ms)
		if err != nil || res.Excess != tc.excess {
			t.Errorf("%s: %+v %v, want excess %d", name, res, err, tc.excess)
		}
	}
}

func TestLedger_ObserverAfterCommit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	sd := r.seedBatch(seedSpec{name: "obs"})
	row := Entry{Kind: KindReserve, MicroUSD: 70, BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "human:h1", Reason: "take"}
	calls := func() int { r.obsMu.Lock(); defer r.obsMu.Unlock(); return len(r.obs) }
	// rollback through an error
	_ = r.tx(func(tx *sql.Tx) error {
		if err := r.s.Ledger().Append(ctx, tx, row); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if calls() != 0 {
		t.Fatal("observer fired for a rolled-back transaction")
	}
	// explicit rollback
	tx, _ := r.st.W.BeginTx(ctx, nil)
	if err := r.s.Ledger().Append(ctx, tx, row); err != nil {
		t.Fatal(err)
	}
	if err := r.s.RollbackTx(tx); err != nil || calls() != 0 {
		t.Fatalf("rollback: %v, calls %d", err, calls())
	}
	// a transaction without ledger rows does not fire
	r.must(func(tx *sql.Tx) error { _, err := r.s.LoadCaps(ctx, tx); return err })
	if calls() != 0 {
		t.Fatal("observer fired without ledger rows")
	}
	r.must(func(tx *sql.Tx) error { return r.s.Ledger().Append(ctx, tx, row) })
	if calls() != 1 {
		t.Fatalf("calls = %d, want 1 after a commit", calls())
	}
	if u := r.obs[0]; u.Outstanding != 70 || u.PrincipalOutstanding != 70 {
		t.Errorf("observer usage %+v", u)
	}
}

func TestSpend_NoFloat(t *testing.T) {
	t.Parallel()
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		checked++
		af, err := parser.ParseFile(token.NewFileSet(), f.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if x.Name == "float32" || x.Name == "float64" {
					t.Errorf("%s: %s in a money package", f.Name(), x.Name)
				}
			case *ast.BasicLit:
				if x.Kind == token.FLOAT {
					t.Errorf("%s: float literal %s", f.Name(), x.Value)
				}
			}
			return true
		})
	}
	if checked < 8 {
		t.Errorf("only %d source files checked", checked)
	}
}
