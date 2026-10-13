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

func TestCheckCaps_Order(t *testing.T) {
	t.Parallel()
	c := Caps{MonthlyMicro: 100, BatchMicro: 50, HumanDailyMicro: 10}
	u := Usage{MonthSettled: 100, PrincipalDaySettled: 10}
	e := CheckCaps(u, c, 10, 60, 60) // fails all three
	if e == nil || e.Cap != CapBatch || e.Requested != 60 || e.Limit != 50 {
		t.Fatalf("all three failing must report batch against the ceiling: %+v", e)
	}
	if e := CheckCaps(u, c, 10, 50, 1); e == nil || e.Cap != CapMonthly || e.Used != 100 {
		t.Errorf("monthly: %+v", e)
	}
	if e := CheckCaps(Usage{PrincipalDaySettled: 10}, c, 10, 50, 1); e == nil || e.Cap != CapDaily {
		t.Errorf("daily: %+v", e)
	}
}

func TestCheckCaps_Examples(t *testing.T) {
	t.Parallel()
	c := DefaultCaps
	e := CheckCaps(Usage{MonthSettled: 9_800_000}, c, c.AgentDailyMicro, 691_731, 538_013)
	if e == nil || e.Cap != CapMonthly || e.Used != 9_800_000 || e.Limit != 10_000_000 || e.Requested != 538_013 {
		t.Errorf("monthly example: %+v", e)
	}
	e = CheckCaps(Usage{PrincipalDaySettled: 800_000}, c, c.AgentDailyMicro, 400_000, 300_000)
	if e == nil || e.Cap != CapDaily || e.Used != 800_000 {
		t.Errorf("daily example: %+v", e)
	}
	e = CheckCaps(Usage{}, c, c.AgentDailyMicro, 5_000_001, 1)
	if e == nil || e.Cap != CapBatch {
		t.Errorf("batch example: %+v", e)
	}
	if e := CheckCaps(Usage{}, c, c.HumanDailyMicro, 5_000_000, 5_000_000); e != nil {
		t.Errorf("exactly equal fits: %+v", e)
	}
	if e := CheckCaps(Usage{MonthSettled: 5_000_000}, c, c.HumanDailyMicro, 5_000_000, 5_000_000); e != nil {
		t.Errorf("month exactly at the cap fits: %+v", e)
	}
	d := (&CapError{Cap: CapMonthly, Limit: 10_000_000, Used: 9_800_000, Requested: 538_013}).Details()
	if d["cap"] != "monthly" || d["limit_micro_usd"] != int64(10_000_000) || d["used_usd"] != "9.8" && d["used_usd"] != "9.800000" || d["requested_micro_usd"] != int64(538_013) {
		t.Errorf("details %+v", d)
	}
	if (&CapError{Cap: CapDaily}).Error() == "" {
		t.Error("empty error text")
	}
}

func TestPrincipalDaily(t *testing.T) {
	t.Parallel()
	c := DefaultCaps
	own := money.MicroUSD(42)
	if PrincipalDaily(auth.Principal{Kind: auth.KindHuman}, TokenLimits{}, c) != c.HumanDailyMicro {
		t.Error("human")
	}
	if PrincipalDaily(auth.Principal{Kind: auth.KindAgent}, TokenLimits{}, c) != c.AgentDailyMicro {
		t.Error("agent default")
	}
	if PrincipalDaily(auth.Principal{Kind: auth.KindAgent}, TokenLimits{DailyMicro: &own}, c) != 42 {
		t.Error("agent own")
	}
}

func seedOutstanding(t *testing.T, r *rig, sd seeded, principal string, amount money.MicroUSD) {
	t.Helper()
	r.must(func(tx *sql.Tx) error {
		return r.s.Ledger().Append(context.Background(), tx, Entry{Kind: KindReserve, MicroUSD: amount, BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: principal, Reason: "take"})
	})
}

func TestReserveTopUp_BatchCeiling(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	sd := r.seedBatch(seedSpec{name: "x", max: 600, retry: 400}) // ceiling 1000
	seedOutstanding(t, r, sd, "human:h1", 900)
	before := r.count(`SELECT COUNT(*) FROM ledger`)
	top := TopUp{BatchID: sd.Batch, JobID: sd.Jobs[0], Reason: "topup_requeue", Amount: 101, Lease: r.lease}
	var ce *CapError
	r.must(func(tx *sql.Tx) (err error) { ce, err = r.s.ReserveTopUp(ctx, tx, top); return })
	if ce == nil || ce.Cap != CapBatchCeiling || ce.Limit != 1000 || ce.Used != 900 || ce.Requested != 101 {
		t.Fatalf("batch ceiling: %+v", ce)
	}
	if r.count(`SELECT COUNT(*) FROM ledger`) != before {
		t.Error("a refused top-up wrote a row")
	}
	top.Amount = 100
	r.must(func(tx *sql.Tx) (err error) { ce, err = r.s.ReserveTopUp(ctx, tx, top); return })
	if ce != nil {
		t.Fatalf("exactly the ceiling fits: %+v", ce)
	}
	if r.count(`SELECT COUNT(*) FROM ledger WHERE reason='topup_requeue'`) != 1 {
		t.Error("top-up not written")
	}
}

func TestReserveTopUp_MonthlyThenDaily(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.exec(`UPDATE spend_caps SET monthly_micro_usd = 1000, human_daily_micro_usd = 500, agent_daily_micro_usd = 300`)
	sd := r.seedBatch(seedSpec{name: "h", max: 4000, retry: 1000})
	other := r.seedBatch(seedSpec{name: "o", principal: "human:h2"})
	seedOutstanding(t, r, other, "human:h2", 600) // month: 600 used by another principal
	top := func(b seeded, amt money.MicroUSD) (ce *CapError) {
		r.must(func(tx *sql.Tx) (err error) {
			ce, err = r.s.ReserveTopUp(ctx, tx, TopUp{BatchID: b.Batch, JobID: b.Jobs[0], Reason: "topup_price", Amount: amt, Lease: r.lease})
			return
		})
		return
	}
	if ce := top(sd, 401); ce == nil || ce.Cap != CapMonthly || ce.Used != 600 {
		t.Fatalf("monthly: %+v", ce)
	}
	r.exec(`UPDATE spend_caps SET monthly_micro_usd = 100000`)
	if ce := top(sd, 501); ce == nil || ce.Cap != CapDaily || ce.Limit != 500 {
		t.Fatalf("daily of the owner: %+v", ce)
	}
	// an agent owner uses its own token limit
	r.token("a1")
	own := int64(50)
	r.exec(`UPDATE api_tokens SET daily_budget_micro_usd = ? WHERE id = 'a1'`, own)
	ag := r.seedBatch(seedSpec{name: "ag", principal: "agent:a1", max: 4000, retry: 1000})
	if ce := top(ag, 51); ce == nil || ce.Cap != CapDaily || ce.Limit != 50 {
		t.Fatalf("agent token daily: %+v", ce)
	}
	if ce := top(ag, 50); ce != nil {
		t.Fatalf("fits: %+v", ce)
	}
	// lowered caps block new top-ups while settlements still succeed
	r.exec(`UPDATE spend_caps SET monthly_micro_usd = 1`)
	if ce := top(sd, 1); ce == nil || ce.Cap != CapMonthly {
		t.Fatalf("lowered cap: %+v", ce)
	}
	att := r.attempt(ag.Jobs[0], 1)
	seedOutstanding(t, r, ag, "agent:a1", 20)
	r.must(func(tx *sql.Tx) error {
		_, err := r.s.SettleAttempt(ctx, tx, AttemptSettle{BatchID: ag.Batch, JobID: ag.Jobs[0], AttemptID: att, PrincipalID: "agent:a1",
			Basis: BasisTimeBound, TimeoutS: 1, KillSlackS: 0, PricePPH: 3_600_000, Lease: r.lease})
		return err
	})
}

func TestReserveTopUp_Errors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	sd := r.seedBatch(seedSpec{name: "e"})
	try := func(top TopUp) (ce *CapError, err error) {
		err = r.tx(func(tx *sql.Tx) (e error) { ce, e = r.s.ReserveTopUp(ctx, tx, top); return })
		return
	}
	ok := TopUp{BatchID: sd.Batch, JobID: sd.Jobs[0], Reason: "attempt_overhead", Amount: 10, Lease: r.lease}
	bad := ok
	bad.Reason = "take"
	if _, err := try(bad); !errors.Is(err, ErrBadEntry) {
		t.Errorf("bad reason: %v", err)
	}
	bad = ok
	bad.Amount = 0
	if _, err := try(bad); !errors.Is(err, ErrBadEntry) {
		t.Errorf("zero amount: %v", err)
	}
	bad = ok
	bad.BatchID = "nope"
	if _, err := try(bad); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown batch: %v", err)
	}
	bad = ok
	bad.Lease = store.LeaseFence{Owner: "intruder", Now: t0}
	if _, err := try(bad); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("lost lease: %v", err)
	}
	if r.count(`SELECT COUNT(*) FROM ledger`) != 0 {
		t.Error("failed top-ups wrote rows")
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestUsage_SQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRig(t)
	a := r.seedBatch(seedSpec{name: "a"})
	b := r.seedBatch(seedSpec{name: "b", principal: "agent:a1"})
	c := r.seedBatch(seedSpec{name: "c"})
	now := mustTime(t, "2026-10-14T12:00:00Z")
	// a: reserve 100, settle 150 (floored at 0); b: reserve 300; c reserve 900 but reconciled
	r.raw(now, "reserve", 100, a.Batch, a.Jobs[0], "human:h1", "take", nil)
	r.raw(now, "settle", 150, a.Batch, a.Jobs[0], "human:h1", "execution", nil)
	r.raw(now, "reserve", 300, b.Batch, b.Jobs[0], "agent:a1", "take", nil)
	r.raw(now, "reserve", 900, c.Batch, c.Jobs[0], "human:h1", "take", nil)
	r.exec(`UPDATE batches SET state='finished', finished_at=?, reconciled_at=? WHERE id=?`, store.FormatTime(now), store.FormatTime(now), c.Batch)
	u, err := r.s.Usage(ctx, r.st.R, "agent:a1", now)
	if err != nil {
		t.Fatal(err)
	}
	if u.Outstanding != 300 || u.PrincipalOutstanding != 300 || u.MonthSettled != 150 || u.PrincipalDaySettled != 0 {
		t.Errorf("usage %+v", u)
	}
	u, _ = r.s.Usage(ctx, r.st.R, "human:h1", now)
	if u.PrincipalOutstanding != 0 || u.PrincipalDaySettled != 150 {
		t.Errorf("human usage %+v", u)
	}

	// calendar month depends on the location
	r.raw(mustTime(t, "2026-10-31T23:30:00Z"), "settle", 1000, a.Batch, a.Jobs[0], "human:h1", "execution", nil)
	utc, _ := r.s.Usage(ctx, r.st.R, "human:h1", mustTime(t, "2026-10-31T23:45:00Z"))
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata")
	}
	rb := New(Options{Store: r.st, Now: r.clk.Now, Location: berlin})
	nov, _ := rb.Usage(ctx, r.st.R, "human:h1", mustTime(t, "2026-11-01T00:45:00Z")) // 01:45 Berlin on 1 Nov
	oct, _ := rb.Usage(ctx, r.st.R, "human:h1", mustTime(t, "2026-10-31T20:00:00Z"))
	if utc.MonthSettled != 1150 {
		t.Errorf("UTC October = %d, want 1150", utc.MonthSettled)
	}
	if nov.MonthSettled != 1000 {
		t.Errorf("Berlin November = %d, want 1000 (the 23:30Z settle)", nov.MonthSettled)
	}
	if oct.MonthSettled != 150 {
		t.Errorf("Berlin October = %d, want 150", oct.MonthSettled)
	}

	// a bill row counts in the month of its window_start, never of at
	r.raw(mustTime(t, "2026-11-02T08:00:00Z"), "bill", -70, "", "", BillingPrincipal, "late_adjust", store.FormatTime(mustTime(t, "2026-10-20T10:00:00Z")))
	octU, _ := r.s.Usage(ctx, r.st.R, "human:h1", mustTime(t, "2026-10-31T23:45:00Z"))
	novU, _ := r.s.Usage(ctx, r.st.R, "human:h1", mustTime(t, "2026-11-03T00:00:00Z"))
	if octU.MonthSettled != 1150-70 {
		t.Errorf("October with the bill = %d, want %d", octU.MonthSettled, 1150-70)
	}
	if novU.MonthSettled != 0 {
		t.Errorf("November = %d, want 0 (bill rows count in the window's month)", novU.MonthSettled)
	}
	// bill rows never count in a principal's day
	if octU.PrincipalDaySettled != 1000 {
		t.Errorf("day settled on 31 Oct 23:45 = %d, want the 1000 settle only (bill rows must not count)", octU.PrincipalDaySettled)
	}
}

// TestCreate_ReadsThroughTx: a service with no store at all runs every create-path read and write through
// the open transaction (a pool use would be a nil dereference).
func TestCreate_ReadsThroughTx(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	sd := r.seedBatch(seedSpec{name: "tx"})
	r.seedTokenEstimate("e1", "tok", "human:h1", t0.Add(time.Hour))
	// a service with no store at all: any s.st use is a nil dereference
	blind := New(Options{Now: r.clk.Now, ConfirmKey: []byte("test-key"), Location: time.UTC})
	tx, err := r.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	steps := []func() error{
		func() error { _, err := blind.Usage(ctx, tx, "human:h1", t0); return err },
		func() error { _, err := blind.LoadCaps(ctx, tx); return err },
		func() error { _, err := blind.TokenLimitsFor(ctx, tx, "agent:zz"); return err },
		func() error { _, _, err := blind.RTFMilli(ctx, tx, nil); return err },
		func() error { _, err := blind.ConsumeToken(ctx, tx, "tok", "human:h1", "b-new"); return err },
		func() error {
			return blind.Ledger().Append(ctx, tx, Entry{Kind: KindReserve, MicroUSD: 10, BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "human:h1", Reason: "take"})
		},
		func() error {
			_, err := blind.ReserveTopUp(ctx, tx, TopUp{BatchID: sd.Batch, JobID: sd.Jobs[0], Reason: "attempt_overhead", Amount: 5, Lease: r.lease})
			return err
		},
		func() error { _, err := blind.Ledger().Outstanding(ctx, tx, sd.Batch); return err },
		func() error {
			_, _, err := blind.SetCaps(ctx, tx, auth.Principal{Kind: auth.KindHuman, ID: "h1"}, CapsPatch{})
			return err
		},
		func() error {
			_, err := blind.RequestApproval(ctx, tx, sd.Batch, auth.Principal{Kind: auth.KindHuman, ID: "h1"})
			return err
		},
	}
	for i, f := range steps {
		if err := f(); err != nil {
			t.Errorf("step %d: %v", i, err)
		}
	}
}

func TestSetCaps_History(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRig(t)
	r.token("a1")
	r.token("a2")
	human := auth.Principal{Kind: auth.KindHuman, ID: "h1"}
	v := func(x int64) *money.MicroUSD { m := money.MicroUSD(x); return &m }
	n := 4
	var changes []CapChange
	var caps Caps
	r.must(func(tx *sql.Tx) (err error) {
		caps, changes, err = r.s.SetCaps(ctx, tx, human, CapsPatch{MonthlyMicro: v(2_000_000), AutoApproveTakes: &n,
			Tokens: []TokenPatch{{TokenID: "a1", DailyMicro: v(77), AutoApproveTakes: &n}, {TokenID: "a2"}, {TokenID: "a1", DailyMicro: v(1)}}})
		return
	})
	if caps.MonthlyMicro != 2_000_000 || caps.AutoApproveTakes != 4 || caps.BatchMicro != DefaultCaps.BatchMicro {
		t.Errorf("caps %+v", caps)
	}
	if len(changes) != 2 || changes[0].Target != "global" || changes[1].Target != "token:a1" {
		t.Fatalf("changes %+v (a2 is unchanged and must not be recorded)", changes)
	}
	if got := r.count(`SELECT COUNT(*) FROM spend_cap_changes`); got != 2 {
		t.Errorf("history rows = %d", got)
	}
	if b, a := r.str(`SELECT before_json FROM spend_cap_changes WHERE target='global'`), r.str(`SELECT after_json FROM spend_cap_changes WHERE target='global'`); b == a ||
		r.val(`SELECT json_extract(before_json,'$.monthly_micro_usd') FROM spend_cap_changes WHERE target='global'`) != 10_000_000 ||
		r.val(`SELECT json_extract(after_json,'$.monthly_micro_usd') FROM spend_cap_changes WHERE target='global'`) != 2_000_000 {
		t.Errorf("before %s after %s", b, a)
	}
	if r.str(`SELECT principal_id FROM spend_cap_changes WHERE target='token:a1'`) != "human:h1" ||
		r.val(`SELECT json_extract(after_json,'$.daily_budget_micro_usd') FROM spend_cap_changes WHERE target='token:a1'`) != 77 {
		t.Error("token row")
	}
	tl, err := r.s.TokenLimitsFor(ctx, r.st.R, "agent:a1")
	if err != nil || tl.DailyMicro == nil || *tl.DailyMicro != 77 || tl.AutoApproveTakes == nil || tl.AutoApproveMicro != nil {
		t.Errorf("token limits %+v %v", tl, err)
	}
	// the same patch again changes nothing and records nothing
	r.must(func(tx *sql.Tx) (err error) {
		_, changes, err = r.s.SetCaps(ctx, tx, human, CapsPatch{MonthlyMicro: v(2_000_000), Tokens: []TokenPatch{{TokenID: "a1", DailyMicro: v(77), AutoApproveTakes: &n}}})
		return
	})
	if len(changes) != 0 || r.count(`SELECT COUNT(*) FROM spend_cap_changes`) != 2 {
		t.Errorf("idempotent patch recorded %+v", changes)
	}
	// clearing a token's limits records a change back to null
	r.must(func(tx *sql.Tx) (err error) {
		_, changes, err = r.s.SetCaps(ctx, tx, human, CapsPatch{Tokens: []TokenPatch{{TokenID: "a1"}}})
		return
	})
	if len(changes) != 1 || r.count(`SELECT COUNT(*) FROM api_tokens WHERE id='a1' AND daily_budget_micro_usd IS NULL AND auto_approve_takes IS NULL`) != 1 {
		t.Errorf("clear: %+v", changes)
	}
	// refusals write nothing
	var unk *UnknownTokenError
	if err := r.tx(func(tx *sql.Tx) error {
		_, _, err := r.s.SetCaps(ctx, tx, human, CapsPatch{MonthlyMicro: v(1), Tokens: []TokenPatch{{TokenID: "ghost"}}})
		return err
	}); !errors.As(err, &unk) || unk.TokenID != "ghost" || unk.Error() == "" {
		t.Errorf("unknown token: %v", err)
	}
	if r.val(`SELECT monthly_micro_usd FROM spend_caps`) != 2_000_000 {
		t.Error("a failed SetCaps leaked a change")
	}
	big, neg, ten := v(int64(MaxCapMicro)+1), v(-1), 101
	for _, p := range []CapsPatch{{MonthlyMicro: big}, {BatchMicro: neg}, {AutoApproveTakes: &ten}, {Tokens: []TokenPatch{{TokenID: "a1", DailyMicro: big}}},
		{Tokens: []TokenPatch{{TokenID: "a1", AutoApproveTakes: &ten}}}} {
		if err := r.tx(func(tx *sql.Tx) error { _, _, err := r.s.SetCaps(ctx, tx, human, p); return err }); !errors.Is(err, ErrCapRange) {
			t.Errorf("range %+v: %v", p, err)
		}
	}
}
