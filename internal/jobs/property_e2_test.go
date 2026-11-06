package jobs

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The two-engine property suites (C162.3). Timelines are seeded random lives over BOTH real fake backends (`fake` serving yue2
// and `fake-acestep` serving acestep15) under one set of caps: after every committed ledger transaction the single-engine model of
// the property suite checks the caps and the conservation invariants (the observer is the rig's spend service), and every tick the
// checks below add what only two endpoints can break.

const e2WorkersMax = 2 // the fakes' default endpoint size

func e2Bound() spend.GlobalBound {
	return spend.ComputeGlobalBound([]spend.BackendBound{
		{Backend: provider.BackendFake, WorkersMax: e2WorkersMax, PricePPH: propPrice, MaxRuntimeS: 900, ColdStartS: 180, KillSlackS: 15},
		{Backend: aceBackendName, WorkersMax: e2WorkersMax, PricePPH: propPrice, MaxRuntimeS: 900, ColdStartS: 300, KillSlackS: 15},
	})
}

// openAttempts counts the attempts in flight on a backend and the worst-case exposure they stand for.
func (e *twoEnv) openAttempts(backend string) (n int, exposure money.MicroUSD) {
	rows, err := e.r.st.R.Query(`SELECT j.timeout_s FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE a.backend = ? AND a.state IN ('pending','submitted','unknown')`, backend)
	if err != nil {
		e.failf("model attempts: %v", err)
		return 0, 0
	}
	defer func() { _ = rows.Close() }()
	cold := map[string]int{provider.BackendFake: 180, aceBackendName: 300}[backend]
	for rows.Next() {
		var timeout int
		_ = rows.Scan(&timeout)
		n++
		exposure += money.ForSeconds(int64(timeout+cold+15), propPrice)
	}
	return n, exposure
}

// boundCheck: the sum of live workers x price never exceeds G, the exposure of the attempts in flight never exceeds X, and no
// endpoint runs more workers than it may.
func (e *twoEnv) boundCheck() {
	g := e2Bound()
	var burn, exposure money.MicroUSD
	for _, b := range []*fake.Backend{e.fb, e.ace} {
		rep, err := b.Sweep(e.ctx)
		if err != nil {
			e.failf("sweep: %v", err)
			return
		}
		if len(rep.Workers) > e2WorkersMax {
			e.failf("%d live workers on an endpoint of %d", len(rep.Workers), e2WorkersMax)
		}
		burn += money.MicroUSD(len(rep.Workers)) * propPrice
	}
	for _, name := range []string{provider.BackendFake, aceBackendName} {
		_, x := e.openAttempts(name)
		exposure += x
	}
	if burn > g.GMicroPerHour {
		e.failf("live burn %d µ$/h exceeds the global bound G %d", burn, g.GMicroPerHour)
	}
	if exposure > g.XMicro {
		e.failf("exposure %d of the attempts in flight exceeds X %d", exposure, g.XMicro)
	}
}

// realityCheck2: what both fakes accrued is covered by settled + outstanding (all time and per batch).
func (e *twoEnv) realityCheck2() {
	now := e.r.clk.Now()
	accrued := int64(e.fb.AccruedCost(now) + e.ace.AccruedCost(now))
	settled, err := settledAll(e.r.st.R)
	if err != nil {
		e.failf("model settled: %v", err)
		return
	}
	out, byBatch, _, err := outstandingBy(e.r.st.R, "")
	if err != nil {
		e.failf("model outstanding: %v", err)
		return
	}
	if accrued > settled+out+2*propRounding {
		e.failf("reality check: providers accrued %d > settled %d + outstanding %d", accrued, settled, out)
	}
	for _, b := range []*fake.Backend{e.fb, e.ace} {
		for batch, acc := range b.AccruedCostByBatch(now) {
			var reconciled int
			_ = e.r.st.R.QueryRow(`SELECT COUNT(*) FROM batches WHERE id = ? AND reconciled_at IS NOT NULL`, batch).Scan(&reconciled)
			if reconciled == 1 {
				continue
			}
			var s int64
			_ = e.r.st.R.QueryRow(`SELECT COALESCE(SUM(micro_usd),0) FROM ledger WHERE batch_id = ? AND kind = 'settle'`, batch).Scan(&s)
			if int64(acc) > s+byBatch[batch]+propRounding {
				e.failf("reality check, batch %s: accrued %d > settled %d + outstanding %d", batch, int64(acc), s, byBatch[batch])
			}
		}
	}
}

// createBatch2 is createBatch over a random backend: the arithmetic of generation.Create against the caps.
func (e *twoEnv) createBatch2() {
	n := 1 + e.rng.IntN(6)
	principal := propPrincipals[e.rng.IntN(len(propPrincipals))]
	timeoutS := 40 + e.rng.IntN(80)
	ace := (e.created+e.refused)%2 == 1 // alternate, so every timeline loads both backends
	for refusedOnce := false; ; n /= 2 {
		if e.tryCreate2(n, principal, timeoutS, ace) {
			return
		}
		if !refusedOnce {
			e.refused++
			refusedOnce = true
		}
		if n <= 1 {
			return
		}
	}
}

func (e *twoEnv) tryCreate2(n int, principal string, timeoutS int, ace bool) bool {
	take := money.ForSeconds(int64(timeoutS+15), propPrice)
	overhead := money.ForSeconds(200, propPrice)
	maxMicro := money.MicroUSD(n) * (take + overhead)
	ceiling := maxMicro + money.MicroUSD(min(n, 2))*(take+overhead)
	var u spend.Usage
	var capsNow spend.Caps
	err := e.r.sp.RunTx(e.ctx, func(tx *sql.Tx) error {
		var err error
		if capsNow, err = e.r.sp.LoadCaps(e.ctx, tx); err != nil {
			return err
		}
		u, err = e.r.sp.Usage(e.ctx, tx, principal, e.r.clk.Now())
		return err
	})
	if err != nil {
		e.failf("usage: %v", err)
		return true
	}
	pd := capsNow.HumanDailyMicro
	if principal[:6] == "agent:" {
		pd = capsNow.AgentDailyMicro
	}
	if spend.CheckCaps(u, capsNow, pd, ceiling, maxMicro) != nil {
		return false
	}
	marker := e.pickMarker(timeoutS)
	sp := batchSpec{jobs: n, principal: principal, timeoutS: timeoutS, ceiling: int64(ceiling), workersMax: e2WorkersMax}
	if ace {
		sp.backend, sp.engine, sp.recipe = aceBackendName, provider.EngineACEStep, e.aceRecipe(marker)
	} else {
		sp.recipe = e.recipeFor(marker)
	}
	e.r.seedBatch(sp)
	e.created++
	if ace {
		e.aceBatches++
	}
	return true
}

// run2 is propEnv.run over the two backends: batches land on either, arming flips per backend, runners crash and are replaced.
func (e *twoEnv) run2() {
	rng := e.rng
	monthly := money.MicroUSD(300_000 + rng.Int64N(2_700_000))
	e.setCaps(spend.Caps{MonthlyMicro: monthly, BatchMicro: monthly, HumanDailyMicro: monthly, AgentDailyMicro: money.MicroUSD(150_000 + rng.Int64N(1_000_000)),
		AutoApproveTakes: 100, AutoApproveMicro: monthly})
	e.capsOn = true
	e.pass()
	createAt := make([]int, 3+rng.IntN(4))
	for i := range createAt {
		createAt[i] = rng.IntN(20)
	}
	disarm, disarmAt, armAt := []string{provider.BackendFake, aceBackendName}[rng.IntN(2)], -1, -1
	if rng.IntN(3) == 0 {
		disarmAt = 4 + rng.IntN(25)
		armAt = disarmAt + 1 + rng.IntN(12)
	}
	tick := 0
	for ; tick < 70; tick++ {
		for _, at := range createAt {
			if at == tick {
				e.createBatch2()
			}
		}
		set := func(armed int) {
			e.r.exec(`UPDATE provider_state SET armed = ?, changed_by = 'human:h1', changed_at = ? WHERE backend = ?`, armed, store.FormatTime(e.r.clk.Now()), disarm)
		}
		if tick == disarmAt {
			set(0)
		}
		if tick == armAt {
			set(1)
		}
		if rng.IntN(25) == 0 {
			e.cancelRandom()
		}
		d := []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second, 2 * time.Minute, 5 * time.Minute}[rng.IntN(5)]
		if tick > 20 {
			d *= 3
		}
		e.r.clk.Advance(d)
		if rng.IntN(18) == 0 {
			e.setCrash(faultPoints[rng.IntN(len(faultPoints))])
		}
		if e.step() {
			e.setCrash("")
			e.r.clk.Advance(time.Duration(95+rng.IntN(60)) * time.Second)
			e.takeOver2()
		}
		if tick%3 == 0 {
			e.pass()
		}
		e.realityCheck2()
		e.boundCheck()
		if tick > 20 && e.allTerminal() {
			break
		}
	}
	e.t.Logf("seed %d: %d ticks, %d batches (%d on ACE-Step, %d refused), %d jobs, %d commits", e.seed, tick, e.created, e.aceBatches, e.refused, e.r.count(`SELECT COUNT(*) FROM jobs`), e.commits)
	e.setCrash("")
	e.r.exec(`UPDATE provider_state SET armed = 1`)
	for i := 0; i < 300 && !e.allTerminal(); i++ {
		e.r.clk.Advance(6 * time.Minute)
		if e.step() {
			e.r.clk.Advance(100 * time.Second)
			e.takeOver2()
		}
		if i%3 == 0 {
			e.pass()
		}
		e.realityCheck2()
		e.boundCheck()
	}
	if !e.allTerminal() {
		e.failf("jobs still not terminal after the drain\n%s", e.dumpStuck())
	}
	e.finalReconcile2()
}

func (e *twoEnv) finalReconcile2() {
	for range 400 {
		e.r.clk.Advance(2 * time.Hour)
		if e.step() {
			e.r.clk.Advance(100 * time.Second)
			e.takeOver2()
		}
		e.pass()
		e.realityCheck2()
		if e.r.count(`SELECT COUNT(*) FROM batches WHERE reconciled_at IS NULL`) == 0 {
			break
		}
	}
	if n := e.r.count(`SELECT COUNT(*) FROM batches WHERE reconciled_at IS NULL AND state IN ('active','finished')`); n != 0 {
		e.failf("%d batches not reconciled\n%s", n, e.dumpBatches())
	}
	if out, _, _, _ := outstandingBy(e.r.st.R, ""); out != 0 {
		e.failf("outstanding %d after the final reconciliation", out)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM job_attempts WHERE state NOT IN ('done','rejected')`); n != 0 {
		e.failf("%d attempts neither done nor rejected", n)
	}
	for _, b := range []*fake.Backend{e.fb, e.ace} {
		if rep, err := b.Sweep(e.ctx); err == nil && len(rep.Workers) != 0 {
			e.failf("%d workers still running on %s at the end", len(rep.Workers), b.Name())
		}
	}
	if n := e.r.count(`SELECT COUNT(*) FROM ledger WHERE kind='settle' AND reason='excess'`); n != 0 {
		e.failf("%d excess rows", n)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM provider_trips`); n != 0 {
		e.failf("%d provider trips in a timeline with no injected fault", n)
	}
	// per backend (migration 0016): the batches' settles plus the backend's bill rows are what that backend billed
	for _, b := range []*fake.Backend{e.fb, e.ace} {
		var settled, bills, billed int64
		_ = e.r.st.R.QueryRow(`SELECT COALESCE(SUM(l.micro_usd),0) FROM ledger l JOIN batches bt ON bt.id = l.batch_id WHERE l.kind = 'settle' AND bt.backend = ?`, b.Name()).Scan(&settled)
		_ = e.r.st.R.QueryRow(`SELECT COALESCE(SUM(micro_usd),0) FROM ledger WHERE kind = 'bill' AND backend = ?`, b.Name()).Scan(&bills)
		if bs, err := b.Billing(e.ctx, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), e.r.clk.Now().UTC().Truncate(time.Hour)); err == nil {
			for _, x := range bs {
				billed += int64(x.Total)
			}
		}
		if d := settled + bills - billed; d > 2*propRounding || d < -2*propRounding {
			e.failf("%s: settled %d + bill rows %d != billed %d", b.Name(), settled, bills, billed)
		}
	}
	all, _ := settledAll(e.r.st.R)
	var billed int64
	for _, b := range []*fake.Backend{e.fb, e.ace} {
		if bs, err := b.Billing(e.ctx, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), e.r.clk.Now().UTC().Truncate(time.Hour)); err == nil {
			for _, x := range bs {
				billed += int64(x.Total)
			}
		}
	}
	if d := all - billed; d > 2*propRounding || d < -2*propRounding {
		e.failf("settled %d != the two providers' billed total %d", all, billed)
	}
}

func TestProperty_SpendNeverExceedsCap_TwoEngines(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy property suite")
	}
	n := propTimelines(t, 4)
	for i := range n {
		seed := uint64(7000 + i + 1)
		if !propInShard(t, seed) {
			continue
		}
		t.Run(fmt.Sprintf("two/%d", seed), func(t *testing.T) {
			t.Parallel()
			e := newTwoEnv(t, seed)
			e.run2()
			if e.created == 0 && e.refused == 0 {
				t.Fatalf("seed %d created no batch", seed)
			}
		})
	}
}

// TestProperty_PerBackendInFlightBound: with many jobs queued on both backends, the attempts in flight on each backend never exceed
// that endpoint's workers.max, and the two bounds are independent (both backends reach their own maximum at the same time, one
// backend's backlog never takes the other's slots).
func TestProperty_PerBackendInFlightBound(t *testing.T) {
	t.Parallel()
	e := newTwoEnv(t, 21)
	e.quietCaps()
	e.seedYue("", 5, 60)
	e.seedACE("", 5, 60)
	peak := map[string]int{}
	for i := 0; i < 400 && !e.allTerminal(); i++ {
		e.r.clk.Advance(20 * time.Second)
		if e.step() {
			e.r.clk.Advance(100 * time.Second)
			e.takeOver2()
		}
		if i%3 == 0 {
			e.pass()
		}
		for _, b := range []string{provider.BackendFake, aceBackendName} {
			n, _ := e.openAttempts(b)
			peak[b] = max(peak[b], n)
			if n > e2WorkersMax {
				t.Fatalf("%d attempts in flight on %s, endpoint workers.max %d", n, b, e2WorkersMax)
			}
		}
	}
	if !e.allTerminal() {
		t.Fatalf("jobs never finished\n%s", e.dumpStuck())
	}
	if peak[provider.BackendFake] != e2WorkersMax || peak[aceBackendName] != e2WorkersMax {
		t.Errorf("peaks %v, want both backends at %d at some point (independent bounds)", peak, e2WorkersMax)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM jobs WHERE state = 'completed'`); n != 10 {
		t.Errorf("%d of 10 jobs completed", n)
	}
	var _ = errors.Is
}
