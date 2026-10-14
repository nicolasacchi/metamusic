package spend

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestFinishBatch_OverheadFromMeter(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := ctx0
	sd := r.seedBatch(seedSpec{name: "fo"})
	r.takeReserve(sd, "human:h1", 400)
	r.must(func(tx *sql.Tx) error {
		return r.s.Ledger().Append(ctx, tx,
			Entry{Kind: KindReserve, MicroUSD: 100, BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "human:h1", Reason: "attempt_overhead"},
			Entry{Kind: KindReserve, MicroUSD: 100, BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "human:h1", Reason: "attempt_overhead"})
	})
	var fr FinishResult
	// the meter attributes more than was reserved: capped at the overhead pot (200)
	r.must(func(tx *sql.Tx) (err error) {
		fr, err = r.s.FinishBatch(ctx, tx, FinishInput{BatchID: sd.Batch, OverheadMicro: 999, Lease: r.lease})
		return
	})
	if fr.Overhead != 200 || fr.Held != 400 {
		t.Fatalf("finish %+v", fr)
	}
	if r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id=? AND kind='residual' AND reason='held' AND micro_usd=400`, sd.Batch) != 1 {
		t.Error("residual held row")
	}
	// held is informational: it does not change the outstanding
	if out, _ := r.s.Ledger().Outstanding(ctx, r.st.R, sd.Batch); out != 400 {
		t.Errorf("outstanding %d", out)
	}
	// hold elapsed release
	var rel money.MicroUSD
	r.must(func(tx *sql.Tx) (err error) {
		rel, err = r.s.ReconcileBatch(ctx, tx, sd.Batch, "hold_elapsed", r.lease)
		return
	})
	if rel != 400 || r.count(`SELECT COUNT(*) FROM ledger WHERE reason='hold_elapsed'`) != 1 {
		t.Errorf("hold_elapsed release %d", rel)
	}
}

func TestBatch_NothingRanSettlesZeroOverhead(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := ctx0
	sd := r.seedBatch(seedSpec{name: "zero", principal: "agent:a1"})
	r.takeReserve(sd, "agent:a1", 300)
	r.must(func(tx *sql.Tx) error {
		return r.s.Ledger().Append(ctx, tx, Entry{Kind: KindReserve, MicroUSD: 100, BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "agent:a1", Reason: "attempt_overhead"})
	})
	// the attempt was never accepted: its overhead and the job's execution pot are released
	r.must(func(tx *sql.Tx) error {
		return r.s.Ledger().Append(ctx, tx,
			Entry{Kind: KindRelease, MicroUSD: 100, BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "agent:a1", Reason: "not_accepted"},
			Entry{Kind: KindRelease, MicroUSD: 300, BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "agent:a1", Reason: "never_ran"})
	})
	var fr FinishResult
	r.must(func(tx *sql.Tx) (err error) {
		fr, err = r.s.FinishBatch(ctx, tx, FinishInput{BatchID: sd.Batch, OverheadMicro: 0, Lease: r.lease})
		return
	})
	if fr.Overhead != 0 || fr.Held != 0 {
		t.Errorf("finish %+v", fr)
	}
	u, _ := r.s.Usage(ctx, r.st.R, "agent:a1", r.clk.Now())
	if u.PrincipalDaySettled != 0 || u.MonthSettled != 0 {
		t.Errorf("nothing ran, nothing charged: %+v", u)
	}
	// negative meter values are clamped
	sd2 := r.seedBatch(seedSpec{name: "neg"})
	r.must(func(tx *sql.Tx) (err error) {
		fr, err = r.s.FinishBatch(ctx, tx, FinishInput{BatchID: sd2.Batch, OverheadMicro: -5, Lease: r.lease})
		return
	})
	if fr.Overhead != 0 {
		t.Errorf("negative overhead %+v", fr)
	}
}

func TestBatchWriters_Errors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := ctx0
	lost := store.LeaseFence{Owner: "intruder", Now: r.clk.Now()}
	sd := r.seedBatch(seedSpec{name: "er"})
	for name, err := range map[string]error{
		"finish unknown": r.tx(func(tx *sql.Tx) error {
			_, err := r.s.FinishBatch(ctx, tx, FinishInput{BatchID: "ghost", Lease: r.lease})
			return err
		}),
		"reconcile unknown": r.tx(func(tx *sql.Tx) error {
			_, err := r.s.ReconcileBatch(ctx, tx, "ghost", "reconciled", r.lease)
			return err
		}),
		"expire unknown": r.tx(func(tx *sql.Tx) error { return r.s.ExpireApproval(ctx, tx, "ghost", r.lease) }),
		"reconcile an active": r.tx(func(tx *sql.Tx) error {
			_, err := r.s.ReconcileBatch(ctx, tx, sd.Batch, "reconciled", r.lease)
			return err
		}),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := r.tx(func(tx *sql.Tx) error {
		_, err := r.s.FinishBatch(ctx, tx, FinishInput{BatchID: sd.Batch, Lease: lost})
		return err
	}); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("finish without the lease: %v", err)
	}
	if err := r.tx(func(tx *sql.Tx) error {
		_, err := r.s.ReconcileBatch(ctx, tx, sd.Batch, "reconciled", lost)
		return err
	}); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("reconcile without the lease: %v", err)
	}
	if r.str(`SELECT state FROM batches WHERE id=?`, sd.Batch) != "active" {
		t.Error("a fenced writer changed the batch")
	}
}
