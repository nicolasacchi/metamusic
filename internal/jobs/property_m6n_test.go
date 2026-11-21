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
	est, err := e.gen.Estimate(context.Background(), e.night, generation.EstimateIn{Items: []generation.Item{e.item(seed)}, Via: spend.ViaAPI, MandateID: mandate})
	if err != nil {
		return false, err
	}
	_, err = e.gen.Create(context.Background(), e.night, generation.CreateIn{Token: est.Token, Via: spend.ViaAPI, Rationale: "night"},
		func(c generation.Created) (int, []byte, error) { return 201, []byte(c.BatchID), nil })
	return err == nil, err
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
			for rows.Next() {
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
			// nothing is left to spend: a create after the revoke is refused
			if _, err := e.attempt(m.ID, 999); !isMandateRefusal(err) {
				t.Errorf("create after revoke: %v", err)
			}
		})
	}
}
