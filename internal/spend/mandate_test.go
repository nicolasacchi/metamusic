package spend

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
)

func (r *rig) mandate(id string, budget int64) {
	r.t.Helper()
	r.exec(`INSERT INTO mandates(id,holder_key,title,budget_micro_usd,valid_from,valid_until,scope_json,state,created_by,created_at)
		VALUES(?,'agent:nightshift','m',?,'2026-10-01T00:00:00.000Z','2026-10-30T00:00:00.000Z','{}','approved','human:owner','2026-10-01T00:00:00.000Z')`, id, budget)
}

func (r *rig) usage(id string) MandateUsage {
	r.t.Helper()
	u, err := r.s.MandateUsage(context.Background(), r.st.R, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return u
}

func TestMandateUsage_Table(t *testing.T) {
	t.Parallel()
	const budget = 1_000_000
	type led struct {
		kind, reason string
		micro        int64
	}
	tracks := func(r *rig) []string {
		var ids []string
		rows, err := r.st.R.Query(`SELECT id FROM tracks ORDER BY id LIMIT 3`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		return ids
	}
	cases := []struct {
		name                                                string
		state                                               string
		ceiling                                             int64 // max+retry
		ledger                                              []led
		reconcile                                           bool
		used, committed, settled, reserved, excess, takesIn int64
	}{
		{name: "active batch commits its ceiling", state: "active", ceiling: 943_587,
			ledger: []led{{"reserve", "take", 300_000}, {"reserve", "attempt_overhead", 20_000}},
			used:   320_000, committed: 943_587, reserved: 320_000},
		{name: "finished adds the residual to what was settled", state: "finished", ceiling: 943_587,
			ledger: []led{{"reserve", "take", 300_731}, {"settle", "actual", 262_397}},
			used:   300_731, committed: 300_731, settled: 262_397, reserved: 38_334},
		{name: "reconciled counts only the settled amount", state: "finished", ceiling: 943_587, reconcile: true,
			ledger: []led{{"reserve", "take", 300_731}, {"settle", "actual", 262_397}, {"release", "reconcile", 38_334}},
			used:   262_397, committed: 262_397, settled: 262_397},
		{name: "rejected is nothing", state: "rejected", ceiling: 943_587,
			ledger: []led{{"reserve", "take", 300_000}, {"release", "rejected", 300_000}}},
		{name: "excess changes neither used nor committed", state: "active", ceiling: 500_000,
			ledger: []led{{"reserve", "take", 100_000}, {"settle", "actual", 90_000}, {"settle", "excess", 7_000}},
			used:   100_000, committed: 500_000, settled: 90_000, reserved: 10_000, excess: 7_000},
		{name: "settled above the ceiling still commits what was spent", state: "active", ceiling: 100,
			ledger: []led{{"reserve", "take", 500}, {"settle", "actual", 450}},
			used:   500, committed: 500, settled: 450, reserved: 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.mandate("m1", budget)
			b := r.seedBatch(seedSpec{name: "x", max: tc.ceiling, mandate: "m1"})
			for _, l := range tc.ledger {
				r.raw(t0, l.kind, l.micro, b.Batch, b.Jobs[0], "human:h1", l.reason, nil)
			}
			if tc.state != "active" {
				r.exec(`UPDATE batches SET state = ?, finished_at = ? WHERE id = ?`, tc.state, "2026-10-14T13:00:00.000Z", b.Batch)
			}
			if tc.reconcile {
				r.exec(`UPDATE batches SET reconciled_at = ? WHERE id = ?`, "2026-10-14T14:00:00.000Z", b.Batch)
			}
			u := r.usage("m1")
			if int64(u.Used) != tc.used || int64(u.Committed) != tc.committed || int64(u.Settled) != tc.settled ||
				int64(u.Reserved) != tc.reserved || int64(u.Excess) != tc.excess {
				t.Fatalf("usage = %+v, want used %d committed %d settled %d reserved %d excess %d", u, tc.used, tc.committed, tc.settled, tc.reserved, tc.excess)
			}
			if u.Used > u.Committed {
				t.Fatalf("used %d > committed %d", u.Used, u.Committed)
			}
			if tc.state == "rejected" && (u.Batches != 0 || u.Takes != 0) {
				t.Fatalf("a rejected batch counts: %+v", u)
			}
		})
	}

	t.Run("fit boundary on the active ceiling", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.mandate("m1", budget)
		r.seedBatch(seedSpec{name: "a", max: 943_587, mandate: "m1"})
		u := r.usage("m1")
		if !MandateFits(budget, u.Committed, 56_413) || MandateFits(budget, u.Committed, 56_414) {
			t.Fatalf("committed %d: 56413 must fit and 56414 must not", u.Committed)
		}
	})

	t.Run("takes per root and batches per local day", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.mandate("m1", budget)
		ids := tracks(r)
		var roots [3]string
		for i, id := range ids {
			roots[i] = r.str(`SELECT root_track_id FROM tracks WHERE id = ?`, id)
		}
		r.seedBatch(seedSpec{name: "a", jobs: 2, max: 1000, mandate: "m1", sources: []string{ids[0], ids[0]}})
		r.seedBatch(seedSpec{name: "b", jobs: 1, max: 1000, mandate: "m1", sources: []string{ids[1]}, created: t0.Add(30 * time.Hour)})
		r.seedBatch(seedSpec{name: "other", jobs: 1, max: 1000}) // no mandate: not counted
		u := r.usage("m1")
		want := map[string]int{}
		want[roots[0]] += 2
		want[roots[1]]++
		for k, v := range want {
			if u.ByRoot[k] != v {
				t.Fatalf("ByRoot[%s] = %d, want %d (%v)", k, u.ByRoot[k], v, u.ByRoot)
			}
		}
		if u.Takes != 3 || u.Batches != 2 {
			t.Fatalf("takes %d batches %d", u.Takes, u.Batches)
		}
		if u.DayBatches["2026-10-14"] != 1 || u.DayBatches["2026-10-15"] != 1 {
			t.Fatalf("DayBatches = %v", u.DayBatches)
		}
	})

	t.Run("a mandate with no batch is empty", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.mandate("m1", budget)
		u := r.usage("m1")
		if u.Used != 0 || u.Committed != 0 || u.Batches != 0 || len(u.ByRoot) != 0 {
			t.Fatalf("%+v", u)
		}
	})
}

func TestMandateFits_Boundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		budget, committed, ceiling money.MicroUSD
		want                       bool
	}{
		{1_000_000, 0, 1_000_000, true},
		{1_000_000, 0, 1_000_001, false},
		{1_000_000, 943_587, 56_413, true},
		{1_000_000, 943_587, 56_414, false},
		{1_000_000, 1_000_000, 0, true},
		{1_000_000, 1_000_001, 0, false},
		{1, 0, 1, true},
	} {
		if got := MandateFits(tc.budget, tc.committed, tc.ceiling); got != tc.want {
			t.Errorf("MandateFits(%d, %d, %d) = %v", tc.budget, tc.committed, tc.ceiling, got)
		}
	}
}

func TestLocalDay_Zone(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Skip("no tzdata")
	}
	r := newRig(t, func(o *Options) { o.Location = loc })
	at := time.Date(2026, 10, 14, 23, 30, 0, 0, time.UTC) // 01:30 on the 15th in Rome
	if got := r.s.localDay(at); got != "2026-10-15" {
		t.Fatalf("localDay = %s", got)
	}
}

// failingQuerier delegates the first okQueries QueryContext calls to q and fails the rest; QueryRowContext always delegates.
type failingQuerier struct {
	q         Querier
	okQueries int
	narrow    int // 1-based number of the call whose query is replaced by a one-column SELECT (a Scan with nine destinations then fails)
	calls     int
}

func (f *failingQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if f.okQueries <= 0 {
		return nil, errors.New("boom")
	}
	f.okQueries--
	f.calls++
	if f.calls == f.narrow {
		return f.q.QueryContext(ctx, `SELECT 1`)
	}
	return f.q.QueryContext(ctx, query, args...)
}

func (f *failingQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return f.q.QueryRowContext(ctx, query, args...)
}

func TestMandateUsage_Errors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mandate("m1", 1_000_000)
	r.seedBatch(seedSpec{name: "a", mandate: "m1"})
	ctx := context.Background()
	for _, ok := range []int{0, 1} { // the batch query fails, then the takes query fails
		if _, err := r.s.MandateUsage(ctx, &failingQuerier{q: r.st.R, okQueries: ok}, "m1"); err == nil {
			t.Errorf("okQueries %d: no error", ok)
		}
	}
	for _, n := range []int{1, 2} { // a result of the wrong shape is a Scan error, in the batch rows and in the take rows
		if _, err := r.s.MandateUsage(ctx, &failingQuerier{q: r.st.R, okQueries: 2, narrow: n}, "m1"); err == nil {
			t.Errorf("narrow %d: no error", n)
		}
	}
	// a batch whose created_at cannot be read is an error, not a silent zero
	r.exec(`DROP TRIGGER batches_frozen`) // created_at is frozen; only a damaged database holds a bad one
	r.exec(`UPDATE batches SET created_at = 'not a time' WHERE mandate_id = 'm1'`)
	if _, err := r.s.MandateUsage(ctx, r.st.R, "m1"); err == nil {
		t.Error("a batch with a bad created_at was accepted")
	}
}

func TestMandateUsage_Location(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if r.s.Location() != time.UTC {
		t.Fatalf("location %v", r.s.Location())
	}
}
