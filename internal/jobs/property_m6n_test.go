package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// mandateEnv is a real generation, spend, jobs and mandates stack over one store (M6N C368): the spend path a mandate stands in for.
type mandateEnv struct {
	t     *testing.T
	st    *store.Store
	gen   *generation.Service
	ms    *mandates.Service
	night auth.Principal
	owner auth.Principal
	ids   atomic.Int64
	seen  sync.Map // batch ids the revoke's canceller saw
}

func newMandateEnv(t *testing.T) *mandateEnv {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lt.FixtureM3(t, st, lt.FixedNow)
	e := &mandateEnv{t: t, st: st,
		night: auth.Principal{Kind: auth.KindAgent, ID: "nightshift", Name: "nightshift", Via: "internal", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}},
		owner: auth.Principal{Kind: auth.KindHuman, Role: auth.RoleOwner, ID: "alice", Name: "alice", Via: "proxy", User: "alice"}}
	clk := clocktest.NewManual(lt.FixedNow)
	newID := func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xc000+e.ids.Add(1)) }
	sp := spend.New(spend.Options{Store: st, Now: clk.Now, NewID: newID, ConfirmKey: []byte("test-key"), Location: time.UTC})
	if _, err := st.W.Exec(`UPDATE spend_caps SET auto_approve_takes = 0, updated_by = 'system:test', updated_at = ? WHERE id = 1`, store.FormatTime(lt.FixedNow)); err != nil {
		t.Fatal(err)
	}
	lib := library.New(st, nil, clk.Now)
	lib.UseIDFunc(newID)
	gate := provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, clk.Now)
	e.gen = generation.New(generation.Options{Store: st, Library: lib, Lineage: lineage.New(lineage.Options{Store: st, Now: clk.Now}), Spend: sp,
		Engines: registry.Default(registry.Options{}), Gate: gate, Now: clk.Now, NewID: newID, ColdStartS: 180, KillSlackS: 15, MaxRuntime: 15 * time.Minute})
	js := New(Options{Store: st, Clock: clk, Hub: NewHub(), Spend: sp})
	e.ms = mandates.New(mandates.Options{Store: st, Spend: sp, Now: clk.Now, NewID: newID,
		Canceller: func(ctx context.Context, tx *sql.Tx, ids []string, by string) error {
			for _, id := range ids {
				e.seen.Store(id, true)
			}
			return js.CancelIntentsTx(ctx, tx, ids, by)
		}})
	e.gen.UseMandates(e.ms)
	return e
}

func (e *mandateEnv) item(seed int64) generation.Item {
	parent := lt.TrackLuna // rated 5 stars by the Owner in the fixture
	return generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &parent, Seeds: []int64{seed}}
}

// grant creates the night's mandate with room for exactly fits batches of one take.
func (e *mandateEnv) grant(fits int) (mandates.Mandate, money.MicroUSD) {
	e.t.Helper()
	pv, err := e.gen.Preview(e.t.Context(), e.night, generation.EstimateIn{Items: []generation.Item{e.item(1)}, Via: spend.ViaAPI})
	if err != nil {
		e.t.Fatal(err)
	}
	c := pv.Cost.Ceiling
	m, err := e.ms.Create(e.t.Context(), e.owner, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "Night shift", Budget: money.MicroUSD(fits)*c + c/2,
		ValidUntil: lt.FixedNow.Add(24 * time.Hour), Scope: mandates.Scope{Modes: []string{"new_seed"}, Engines: []string{"yue2"}, MinStars: 3, MaxTakesPerLineage: 50,
			MaxTakesPerBatch: 1, MaxBatchCeilingMicroUSD: c, MaxBatchesPerDay: 4, TierB: true}})
	if err != nil {
		e.t.Fatal(err)
	}
	return m, c
}

func (e *mandateEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// attempt estimates one take under the mandate and creates it. A refusal is only ever the mandate's 409 (never a half-written batch).
func (e *mandateEnv) attempt(mandate string, seed int64) (created bool, err error) {
	token, err := e.estimate(mandate, seed)
	if err != nil {
		return false, err
	}
	err = e.create(token)
	return err == nil, err
}

func (e *mandateEnv) estimate(mandate string, seed int64) (string, error) {
	est, err := e.gen.Estimate(context.Background(), e.night, generation.EstimateIn{Items: []generation.Item{e.item(seed)}, Via: spend.ViaAPI, MandateID: mandate})
	if err != nil {
		return "", err
	}
	return est.Token, nil
}

func (e *mandateEnv) create(token string) error {
	_, err := e.gen.Create(context.Background(), e.night, generation.CreateIn{Token: token, Via: spend.ViaAPI, Rationale: "night"},
		func(c generation.Created) (int, []byte, error) { return 201, []byte(c.BatchID), nil })
	return err
}

func isMandateRefusal(err error) bool {
	var pe *problem.Error
	return errors.As(err, &pe) && pe.Status == 409 && pe.Code == "MANDATE_UNAVAILABLE"
}

// TestProperty_MandateNeverOverCommits (C368): concurrent estimates and creates against one mandate's budget never commit more than
// the budget, whatever the interleaving; each accepted batch carries the mandate and its mandate approval, and each refusal is the
// mandate's own 409 with nothing written.
func TestProperty_MandateNeverOverCommits(t *testing.T) {
	t.Parallel()
	for seed := uint64(1); seed <= 4; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, 99))
			e := newMandateEnv(t)
			fits := 1 + rng.IntN(3)
			m, ceiling := e.grant(fits)
			workers := fits + 2 + rng.IntN(4)
			var wg sync.WaitGroup
			var ok, refused atomic.Int64
			errc := make(chan error, workers)
			for w := range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					created, err := e.attempt(m.ID, int64(5100+w))
					switch {
					case created:
						ok.Add(1)
					case isMandateRefusal(err):
						refused.Add(1)
					default:
						errc <- fmt.Errorf("worker %d: %w", w, err)
					}
				}()
			}
			wg.Wait()
			close(errc)
			for err := range errc {
				t.Error(err)
			}
			if int(ok.Load()) != fits || int(ok.Load()+refused.Load()) != workers {
				t.Fatalf("%d accepted, %d refused of %d workers; room for %d", ok.Load(), refused.Load(), workers, fits)
			}
			u, err := e.ms.Usage(t.Context(), m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if u.Committed > m.Budget || u.Committed != money.MicroUSD(fits)*ceiling || u.Batches != fits {
				t.Errorf("usage %+v budget %d ceiling %d", u, m.Budget, ceiling)
			}
			if n := e.count(`SELECT COUNT(*) FROM batches b JOIN approvals a ON a.batch_id = b.id WHERE b.mandate_id = ? AND a.state = 'auto' AND a.via = 'mandate'`, m.ID); n != fits {
				t.Errorf("%d mandate batches with a mandate approval", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM batches WHERE mandate_id IS NULL AND principal_id = 'agent:nightshift'`); n != 0 {
				t.Errorf("%d night batches without a mandate", n)
			}
		})
	}
}

// TestProperty_RevokeRacingCreate: a revoke that races creates leaves every batch of the mandate either refused or cancelled in the
// revoke's own transaction; no create commits after the revoke (it would escape the cancel intents).
func TestProperty_RevokeRacingCreate(t *testing.T) {
	t.Parallel()
	for seed := uint64(1); seed <= 4; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, 7))
			e := newMandateEnv(t)
			m, _ := e.grant(8)
			// one batch committed before the race, so the cancel check below always has a batch to judge
			if _, err := e.attempt(m.ID, 5100); err != nil {
				t.Fatalf("pre-race create: %v", err)
			}
			workers := 4 + rng.IntN(4)
			start := make(chan struct{})
			var wg sync.WaitGroup
			errc := make(chan error, workers+1)
			for w := range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if _, err := e.attempt(m.ID, int64(5200+w)); err != nil && !isMandateRefusal(err) {
						errc <- fmt.Errorf("worker %d: %w", w, err)
					}
				}()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				time.Sleep(time.Duration(rng.IntN(40)) * time.Millisecond)
				if _, err := e.ms.Revoke(context.Background(), e.owner, m.ID, "racing"); err != nil {
					errc <- fmt.Errorf("revoke: %w", err)
				}
			}()
			close(start)
			wg.Wait()
			close(errc)
			for err := range errc {
				t.Error(err)
			}
			rows, err := e.st.R.Query(`SELECT id FROM batches WHERE mandate_id = ?`, m.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			batches := 0
			for rows.Next() {
				batches++
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				if _, saw := e.seen.Load(id); !saw {
					t.Errorf("batch %s committed after the revoke and escaped its cancel intents", id)
				}
				if n := e.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND cancel_requested_by IS NULL AND state NOT IN ('completed','failed','cancelled','expired')`, id); n != 0 {
					t.Errorf("batch %s has %d jobs the revoke did not cancel", id, n)
				}
			}
			if batches == 0 {
				t.Error("no batch was committed, the race checked nothing")
			}
			// nothing is left to spend: a create after the revoke is refused
			if _, err := e.attempt(m.ID, 999); !isMandateRefusal(err) {
				t.Errorf("create after revoke: %v", err)
			}
		})
	}
}

// grantWith creates a night mandate of the given budget (in whole ceilings plus half) and batches per day.
func (e *mandateEnv) grantWith(fits, perDay int, scope func(*mandates.Scope)) (mandates.Mandate, money.MicroUSD) {
	e.t.Helper()
	pv, err := e.gen.Preview(e.t.Context(), e.night, generation.EstimateIn{Items: []generation.Item{e.item(1)}, Via: spend.ViaAPI})
	if err != nil {
		e.t.Fatal(err)
	}
	c := pv.Cost.Ceiling
	sc := mandates.Scope{Modes: []string{"new_seed"}, Engines: []string{"yue2"}, MinStars: 3, MaxTakesPerLineage: 50,
		MaxTakesPerBatch: 1, MaxBatchCeilingMicroUSD: c, MaxBatchesPerDay: perDay, TierB: true}
	if scope != nil {
		scope(&sc)
	}
	m, err := e.ms.Create(e.t.Context(), e.owner, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "Night shift",
		Budget: money.MicroUSD(fits)*c + c/2, ValidUntil: lt.FixedNow.Add(24 * time.Hour), Scope: sc})
	if err != nil {
		e.t.Fatal(err)
	}
	return m, c
}

func isCapRefusal(err error) bool {
	var pe *problem.Error
	return errors.As(err, &pe) && pe.Status == 402 && pe.Code == "BUDGET_EXCEEDED"
}

// TestProperty_MandateNeverExceeded (M6N-X4): over fixed-seed random nights (mandate budget, batches per day, a global monthly cap
// of a random size, a random number of racing creates, an optional revoke in the middle) the committed spend of a mandate never
// exceeds its budget, never exceeds the global monthly cap and never exceeds the batches-per-day limit; every refusal is the
// mandate's 409 or the cap's 402; money is integer micro-USD throughout.
func TestProperty_MandateNeverExceeded(t *testing.T) {
	t.Parallel()
	for seed := uint64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, 2026))
			e := newMandateEnv(t)
			fits := 1 + rng.IntN(5)
			perDay := 1 + rng.IntN(mandates.MaxBatchesPerDay)
			capFits := 1 + rng.IntN(6)
			m, c := e.grantWith(fits, perDay, nil)
			monthly := money.MicroUSD(capFits)*c + c/3
			// one seed in four makes the per-batch cap the binding one (below one ceiling: nothing may commit); otherwise the monthly
			// cap binds. The revoke interleaving below is wall-clock and so not reproducible, but every assertion holds for any outcome.
			workers := 3 + rng.IntN(8)
			batchCap, limit := int64(c)*2, min(fits, capFits, perDay, workers)
			if rng.IntN(4) == 0 {
				batchCap, limit = int64(c)/2, 0
			}
			if _, err := e.st.W.Exec(`UPDATE spend_caps SET monthly_micro_usd = ?, batch_micro_usd = ?, human_daily_micro_usd = ?, agent_daily_micro_usd = ?,
				updated_by = 'system:test', updated_at = ? WHERE id = 1`, int64(monthly), batchCap, int64(monthly)*2, int64(monthly)*2, store.FormatTime(lt.FixedNow)); err != nil {
				t.Fatal(err)
			}
			revoke := rng.IntN(3) == 0
			start := make(chan struct{})
			var wg sync.WaitGroup
			errc := make(chan error, workers+1)
			for w := range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if _, err := e.attempt(m.ID, int64(7000+w)); err != nil && !isMandateRefusal(err) && !isCapRefusal(err) {
						errc <- fmt.Errorf("worker %d: %w", w, err)
					}
				}()
			}
			if revoke {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					time.Sleep(time.Duration(rng.IntN(30)) * time.Millisecond)
					if _, err := e.ms.Revoke(context.Background(), e.owner, m.ID, "random night"); err != nil {
						errc <- fmt.Errorf("revoke: %w", err)
					}
				}()
			}
			close(start)
			wg.Wait()
			close(errc)
			for err := range errc {
				t.Error(err)
			}
			var committed, batches int64
			if err := e.st.R.QueryRow(`SELECT COALESCE(SUM(ceiling_micro_usd), 0), COUNT(*) FROM batches WHERE mandate_id = ?`, m.ID).Scan(&committed, &batches); err != nil {
				t.Fatal(err)
			}
			if committed > int64(m.Budget) {
				t.Errorf("committed %d exceeds the mandate budget %d", committed, m.Budget)
			}
			if committed > int64(monthly) {
				t.Errorf("committed %d exceeds the global monthly cap %d", committed, monthly)
			}
			if int(batches) > perDay || int(batches) > limit {
				t.Errorf("%d batches with room for fits=%d cap=%d perDay=%d workers=%d", batches, fits, capFits, perDay, workers)
			}
			if committed != batches*int64(c) {
				t.Errorf("committed %d is not %d whole ceilings of %d", committed, batches, c)
			}
			u, err := e.ms.Usage(t.Context(), m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if int64(u.Committed) != committed || u.Committed > m.Budget {
				t.Errorf("usage %+v differs from the batches (%d) or the budget %d", u, committed, m.Budget)
			}
			if !revoke && int(batches) != limit {
				t.Errorf("%d batches, want %d (a refusal below the limits means a lost create)", batches, limit)
			}
		})
	}
}

// TestMandate_NeverRaisesCaps (M6N-X4): a mandate cannot raise or bypass any spend cap. A budget far above the global caps still
// refuses at the monthly cap, the per-batch cap and the agent daily cap with the cap's 402, writes no batch and consumes no token,
// and creating, using and revoking a mandate leaves the spend_caps row byte-identical.
func TestMandate_NeverRaisesCaps(t *testing.T) {
	t.Parallel()
	capsRow := func(e *mandateEnv) string {
		var s string
		if err := e.st.R.QueryRow(`SELECT monthly_micro_usd||'|'||batch_micro_usd||'|'||human_daily_micro_usd||'|'||agent_daily_micro_usd||'|'||
			auto_approve_takes||'|'||auto_approve_micro_usd||'|'||updated_by||'|'||updated_at FROM spend_caps WHERE id = 1`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, tc := range []struct{ name, set string }{
		{"monthly", `monthly_micro_usd = ?`},
		{"agent daily", `agent_daily_micro_usd = ?`},
		{"batch", `batch_micro_usd = ?`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newMandateEnv(t)
			// the mandate's budget is 50 batches; every cap is set to a tenth of one batch's ceiling
			before := capsRow(e) // before the mandate exists, so its creation is compared too
			m, c := e.grantWith(50, 4, nil)
			if after := capsRow(e); after != before {
				t.Errorf("creating a mandate changed the caps:\n before %s\n after  %s", before, after)
			}
			// the estimate is taken under the default caps, then the cap is tightened, so the refusal is Create's own enforcement
			token, err := e.estimate(m.ID, 8001)
			if err != nil {
				t.Fatalf("estimate: %v", err)
			}
			if _, err := e.st.W.Exec(`UPDATE spend_caps SET `+tc.set+`, updated_by = 'system:test', updated_at = ? WHERE id = 1`, int64(c)/10, store.FormatTime(lt.FixedNow)); err != nil {
				t.Fatal(err)
			}
			before = capsRow(e)
			if err := e.create(token); !isCapRefusal(err) {
				t.Fatalf("create over the %s cap: err=%v", tc.name, err)
			}
			if n := e.count(`SELECT COUNT(*) FROM batches WHERE mandate_id IS NOT NULL`); n != 0 {
				t.Errorf("%d batches written over the %s cap", n, tc.name)
			}
			if n := e.count(`SELECT COUNT(*) FROM estimates WHERE consumed_at IS NOT NULL AND mandate_id IS NOT NULL`); n != 0 {
				t.Errorf("%d tokens consumed by a refused create", n)
			}
			if _, err := e.ms.Revoke(t.Context(), e.owner, m.ID, "done"); err != nil {
				t.Fatal(err)
			}
			if after := capsRow(e); after != before {
				t.Errorf("a mandate changed the caps:\n before %s\n after  %s", before, after)
			}
		})
	}
	t.Run("a mandate batch counts against the global monthly cap", func(t *testing.T) {
		t.Parallel()
		e := newMandateEnv(t)
		m, c := e.grantWith(50, 4, nil)
		if _, err := e.st.W.Exec(`UPDATE spend_caps SET monthly_micro_usd = ?, updated_by = 'system:test', updated_at = ? WHERE id = 1`, int64(c)+int64(c)/2, store.FormatTime(lt.FixedNow)); err != nil {
			t.Fatal(err)
		}
		before := capsRow(e)
		if created, err := e.attempt(m.ID, 8101); !created {
			t.Fatalf("first batch: %v", err)
		}
		if after := capsRow(e); after != before {
			t.Errorf("a successful mandate batch changed the caps:\n before %s\n after  %s", before, after)
		}
		if created, err := e.attempt(m.ID, 8102); created || !isCapRefusal(err) {
			t.Fatalf("second batch past the monthly cap: created=%v err=%v", created, err)
		}
		if n := e.count(`SELECT COUNT(*) FROM batches WHERE mandate_id = ?`, m.ID); n != 1 {
			t.Errorf("%d batches, want 1", n)
		}
	})
}
