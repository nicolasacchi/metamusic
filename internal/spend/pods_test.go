package spend

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func (r *rig) podReserve(batch string, amt money.MicroUSD) (ce *CapError, err error) {
	err = r.tx(func(tx *sql.Tx) (e error) {
		ce, e = r.s.ReservePodOverhead(context.Background(), tx, PodOverhead{BatchID: batch, PodID: "pod-1", Amount: amt, Lease: r.lease})
		return
	})
	return
}

func TestLedger_PodOverheadInOverheadPot(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	sd := r.seedBatch(seedSpec{name: "pot", max: 5000, retry: 1000})
	seedOutstanding(t, r, sd, "human:h1", 50)
	if ce, err := r.podReserve(sd.Batch, 100); ce != nil || err != nil {
		t.Fatalf("reserve: %+v %v", ce, err)
	}
	if got := r.count(`SELECT COUNT(*) FROM ledger WHERE reason='pod_overhead' AND kind='reserve' AND job_id IS NULL AND batch_id=?`, sd.Batch); got != 1 {
		t.Fatalf("pod_overhead rows = %d", got)
	}
	l := r.s.Ledger()
	r.must(func(tx *sql.Tx) error {
		if v, err := l.OverheadRemaining(ctx, tx, sd.Batch); err != nil || v != 100 {
			t.Errorf("overhead pot = %d %v", v, err)
		}
		if v, err := l.ExecutionRemaining(ctx, tx, sd.Jobs[0]); err != nil || v != 50 {
			t.Errorf("execution pot = %d %v", v, err)
		}
		return nil
	})
	// FinishBatch settles the meter overhead capped at the pot, which now includes the pod reserve
	var res FinishResult
	r.must(func(tx *sql.Tx) (err error) {
		res, err = r.s.FinishBatch(ctx, tx, FinishInput{BatchID: sd.Batch, OverheadMicro: 400, Lease: r.lease})
		return
	})
	if res.Overhead != 100 {
		t.Errorf("settled overhead = %d, want the pot 100", res.Overhead)
	}
}

func TestReservePodOverhead_CapsInOrder(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.exec(`UPDATE spend_caps SET monthly_micro_usd = 1000, human_daily_micro_usd = 500, agent_daily_micro_usd = 300`)
	sd := r.seedBatch(seedSpec{name: "caps", max: 600, retry: 400}) // ceiling 1000
	other := r.seedBatch(seedSpec{name: "oth", principal: "human:h2"})
	seedOutstanding(t, r, other, "human:h2", 600)
	seedOutstanding(t, r, sd, "human:h1", 300)
	before := r.count(`SELECT COUNT(*) FROM ledger`)
	// batch ceiling first: 300 + 701 > 1000, though month and day are also exceeded
	if ce, err := r.podReserve(sd.Batch, 701); err != nil || ce == nil || ce.Cap != CapBatchCeiling || ce.Limit != 1000 || ce.Used != 300 || ce.Requested != 701 {
		t.Fatalf("ceiling: %+v %v", ce, err)
	}
	// then the month: 900 used + 200 > 1000
	if ce, err := r.podReserve(sd.Batch, 200); err != nil || ce == nil || ce.Cap != CapMonthly {
		t.Fatalf("month: %+v %v", ce, err)
	}
	// then the owner's day
	r.exec(`UPDATE spend_caps SET monthly_micro_usd = 100000`)
	if ce, err := r.podReserve(sd.Batch, 201); err != nil || ce == nil || ce.Cap != CapDaily || ce.Limit != 500 {
		t.Fatalf("day: %+v %v", ce, err)
	}
	if r.count(`SELECT COUNT(*) FROM ledger`) != before {
		t.Error("a refused reserve wrote a row")
	}
	if ce, err := r.podReserve(sd.Batch, 200); ce != nil || err != nil {
		t.Fatalf("fits: %+v %v", ce, err)
	}
	// errors: non-positive, unknown batch, lost lease
	if _, err := r.podReserve(sd.Batch, 0); !errors.Is(err, ErrBadEntry) {
		t.Errorf("zero: %v", err)
	}
	if _, err := r.podReserve("nope", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown batch: %v", err)
	}
	r.exec(`UPDATE spend_caps SET human_daily_micro_usd = 100000`)
	fresh := r.seedBatch(seedSpec{name: "lease"})
	err := r.tx(func(tx *sql.Tx) error {
		_, e := r.s.ReservePodOverhead(context.Background(), tx, PodOverhead{BatchID: fresh.Batch, Amount: 1, Lease: store.LeaseFence{Owner: "intruder", Now: t0}})
		return e
	})
	if !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("lost lease: %v", err)
	}
}

func TestSettlePodExcess_NotCountedAsSpent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	sd := r.seedBatch(seedSpec{name: "ex", max: 5000, retry: 1000})
	if ce, err := r.podReserve(sd.Batch, 100); ce != nil || err != nil {
		t.Fatal(ce, err)
	}
	settle := func(amt money.MicroUSD) error {
		return r.tx(func(tx *sql.Tx) error { return r.s.SettlePodExcess(ctx, tx, sd.Batch, amt, r.lease) })
	}
	if err := settle(5000); err != nil { // far beyond the reserve: excess is allowed
		t.Fatal(err)
	}
	if err := settle(0); err != nil || r.count(`SELECT COUNT(*) FROM ledger WHERE reason='excess'`) != 1 {
		t.Fatalf("zero excess wrote a row: %v", err)
	}
	r.must(func(tx *sql.Tx) error {
		if v, err := r.s.Ledger().OverheadRemaining(ctx, tx, sd.Batch); err != nil || v != 100 {
			t.Errorf("excess touched the pot: %d %v", v, err)
		}
		return nil
	})
	// the invariant ignores excess: the full reserve can still be released
	r.must(func(tx *sql.Tx) error {
		return r.s.Ledger().Append(ctx, tx, Entry{Kind: KindRelease, Reason: "rejected", MicroUSD: 100, BatchID: &sd.Batch, PrincipalID: "human:h1"})
	})
	if got := r.str(`SELECT principal_id FROM ledger WHERE reason='excess'`); got != "human:h1" {
		t.Errorf("principal = %s", got)
	}
	if err := r.tx(func(tx *sql.Tx) error { return r.s.SettlePodExcess(ctx, tx, "nope", 1, r.lease) }); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown batch: %v", err)
	}
	if err := r.tx(func(tx *sql.Tx) error {
		return r.s.SettlePodExcess(ctx, tx, sd.Batch, 1, store.LeaseFence{Owner: "x", Now: t0})
	}); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("lost lease: %v", err)
	}
}

// TestPodOverhead_LedgerInvariants proves reserve + settle sums, no double settle, and a crash between reserve and settle.
func TestPodOverhead_LedgerInvariants(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	sd := r.seedBatch(seedSpec{name: "inv", max: 5000, retry: 1000})
	for _, amt := range []money.MicroUSD{100, 50} { // a pod and its re-creation
		if ce, err := r.podReserve(sd.Batch, amt); ce != nil || err != nil {
			t.Fatal(ce, err)
		}
	}
	// crash between reserve and settle: the settle transaction fails, the reserves stay and nothing else was written
	crash := errors.New("crash")
	err := r.tx(func(tx *sql.Tx) error {
		if _, e := r.s.FinishBatch(ctx, tx, FinishInput{BatchID: sd.Batch, OverheadMicro: 120, Lease: r.lease}); e != nil {
			return e
		}
		return crash
	})
	if !errors.Is(err, crash) {
		t.Fatal(err)
	}
	if got := r.val(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id=?`, sd.Batch); got != 150 {
		t.Fatalf("ledger after a crash = %d, want the two reserves only", got)
	}
	if r.str(`SELECT state FROM batches WHERE id=?`, sd.Batch) != "active" {
		t.Fatal("batch finished despite the rollback")
	}
	// the settle is retried once and sums to the reserves: 120 settled, 30 left outstanding (held)
	var res FinishResult
	r.must(func(tx *sql.Tx) (e error) {
		res, e = r.s.FinishBatch(ctx, tx, FinishInput{BatchID: sd.Batch, OverheadMicro: 120, Lease: r.lease})
		return
	})
	if res.Overhead != 120 || res.Held != 30 {
		t.Fatalf("finish: %+v", res)
	}
	// no double settle: a second FinishBatch is refused, and a manual overhead settle beyond the pot is refused
	if err := r.tx(func(tx *sql.Tx) error {
		_, e := r.s.FinishBatch(ctx, tx, FinishInput{BatchID: sd.Batch, OverheadMicro: 120, Lease: r.lease})
		return e
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("second finish: %v", err)
	}
	over := func(amt int64) error {
		return r.tx(func(tx *sql.Tx) error {
			return r.s.Ledger().Append(ctx, tx, Entry{Kind: KindSettle, Reason: "overhead", MicroUSD: money.MicroUSD(amt), BatchID: &sd.Batch, PrincipalID: "human:h1"})
		})
	}
	if err := over(31); !errors.Is(err, ErrLedgerInvariant) {
		t.Errorf("settle beyond the pot: %v", err)
	}
	if err := over(30); err != nil {
		t.Errorf("settle of the exact remainder: %v", err)
	}
	if err := over(1); !errors.Is(err, ErrLedgerInvariant) {
		t.Errorf("double settle: %v", err)
	}
	if got := r.val(`SELECT SUM(CASE kind WHEN 'reserve' THEN micro_usd WHEN 'settle' THEN -micro_usd END) FROM ledger WHERE batch_id=? AND reason IN ('pod_overhead','overhead')`, sd.Batch); got != 0 {
		t.Errorf("reserve and settle do not sum to zero: %d", got)
	}
}

func TestReserveTopUp_ZeroAttemptOverheadIsNoop(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sd := r.seedBatch(seedSpec{name: "zero"})
	ctx := context.Background()
	var ce *CapError
	// not even the lease or the batch is consulted: a pod backend has no per-attempt overhead
	err := r.tx(func(tx *sql.Tx) (e error) {
		ce, e = r.s.ReserveTopUp(ctx, tx, TopUp{BatchID: "nope", JobID: sd.Jobs[0], Reason: "attempt_overhead", Amount: 0, Lease: store.LeaseFence{Owner: "x", Now: t0}})
		return
	})
	if ce != nil || err != nil {
		t.Fatalf("zero attempt overhead: %+v %v", ce, err)
	}
	if r.count(`SELECT COUNT(*) FROM ledger`) != 0 {
		t.Error("a zero attempt overhead wrote rows")
	}
	if r.s.ledger.pending[nil] != "" || len(r.s.ledger.pending) != 0 {
		t.Error("a pending observer call was recorded")
	}
	if len(r.obs) != 0 {
		t.Errorf("observer fired: %d", len(r.obs))
	}
}

func TestReserveTopUp_ZeroOtherReasonRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sd := r.seedBatch(seedSpec{name: "zero2"})
	for _, reason := range []string{"topup_ambiguous", "topup_requeue", "topup_price"} {
		err := r.tx(func(tx *sql.Tx) error {
			_, e := r.s.ReserveTopUp(context.Background(), tx, TopUp{BatchID: sd.Batch, JobID: sd.Jobs[0], Reason: reason, Amount: 0, Lease: r.lease})
			return e
		})
		if !errors.Is(err, ErrBadEntry) {
			t.Errorf("%s with zero: %v", reason, err)
		}
	}
	err := r.tx(func(tx *sql.Tx) error {
		_, e := r.s.ReserveTopUp(context.Background(), tx, TopUp{BatchID: sd.Batch, JobID: sd.Jobs[0], Reason: "attempt_overhead", Amount: -1, Lease: r.lease})
		return e
	})
	if !errors.Is(err, ErrBadEntry) {
		t.Errorf("negative attempt overhead: %v", err)
	}
	if r.count(`SELECT COUNT(*) FROM ledger`) != 0 {
		t.Error("refusals wrote rows")
	}
}
