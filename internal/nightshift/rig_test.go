package nightshift

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/schedule"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	alice = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleOwner, ID: "alice", Name: "alice", Via: "local", User: "alice",
		Scopes: auth.ScopesFor(auth.RoleOwner), StepUpAt: lt.FixedNow}
	bob   = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", Via: "local", User: "bob"}
	carol = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "carol", Name: "carol", Via: "local", User: "carol"}
	phone = auth.Principal{Kind: auth.KindDevice, Role: auth.RoleOwner, ID: "dev1", Name: "phone", Via: "device", User: "alice"}
	agent = auth.Principal{Kind: auth.KindAgent, ID: "ag1", Name: "ag1", Via: "token", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
)

// rig is a real generation, spend, jobs and mandates stack over one store (the spend path a night goes through), with the night runner on top.
type rig struct {
	t     *testing.T
	st    *store.Store
	gen   *generation.Service
	js    *jobs.Service
	ms    *mandates.Service
	sp    *spend.Service
	r     *Runner
	ids   atomic.Int64
	mu    sync.Mutex
	now   time.Time
	night string
}

func (g *rig) clock() time.Time { g.mu.Lock(); defer g.mu.Unlock(); return g.now }
func (g *rig) advance(d time.Duration) {
	g.mu.Lock()
	g.now = g.now.Add(d)
	g.mu.Unlock()
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lt.FixtureM3(t, st, lt.FixedNow)
	return rigOn(t, st)
}

// rigOn builds the stack over an already seeded store.
func rigOn(t *testing.T, st *store.Store) *rig {
	t.Helper()
	g := &rig{t: t, st: st, now: lt.FixedNow, night: "2026-10-15"}
	clk := clocktest.NewManual(lt.FixedNow)
	newID := func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd000+g.ids.Add(1)) }
	g.sp = spend.New(spend.Options{Store: st, Now: g.clock, NewID: newID, ConfirmKey: []byte("test-key"), Location: time.UTC})
	if _, err := st.W.Exec(`UPDATE spend_caps SET auto_approve_takes = 0, updated_by = 'system:test', updated_at = ? WHERE id = 1`, store.FormatTime(lt.FixedNow)); err != nil {
		t.Fatal(err)
	}
	lib := library.New(st, nil, g.clock)
	lib.UseIDFunc(newID)
	gate := provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, g.clock)
	g.gen = generation.New(generation.Options{Store: st, Library: lib, Lineage: lineage.New(lineage.Options{Store: st, Now: g.clock}), Spend: g.sp,
		Engines: registry.Default(registry.Options{}), Gate: gate, Now: g.clock, NewID: newID, ColdStartS: 180, KillSlackS: 15, MaxRuntime: 15 * time.Minute})
	g.js = jobs.New(jobs.Options{Store: st, Clock: clk, Hub: jobs.NewHub(), Spend: g.sp})
	g.ms = mandates.New(mandates.Options{Store: st, Spend: g.sp, Now: g.clock, NewID: newID, Canceller: g.js.CancelIntentsTx})
	g.gen.UseMandates(g.ms)
	g.r = New(Options{Store: st, Audit: audit.NewStore(st, newID), Generation: g.gen, Mandates: g.ms,
		Reader: &Reader{Strengths: fakeStrengths{}}, Now: g.clock, NewID: newID,
		Cancel: func(ctx context.Context, p auth.Principal, batchID, reason string) error {
			_, err := g.js.CancelBatch(ctx, p, batchID, reason)
			return err
		}})
	return g
}

// enable switches the shift on as the Owner does in the app.
func (g *rig) enable() {
	g.t.Helper()
	on := true
	if _, err := g.r.UpdateSettings(g.t.Context(), alice, SettingsPatch{Enabled: &on, MinStars: new(3)}); err != nil {
		g.t.Fatal(err)
	}
}

// ceilingOfOne is the estimate ceiling of one planned night take (the golden quote parameters of the fake gate).
func (g *rig) ceilingOfOne() money.MicroUSD {
	g.t.Helper()
	parent := lt.TrackLuna
	pv, err := g.gen.Preview(g.t.Context(), Principal(), generation.EstimateIn{Items: []generation.Item{{Mode: lineage.ModeNewSeed, ParentTrackID: &parent, NSeeds: 1}}, Via: spend.ViaAPI})
	if err != nil {
		g.t.Fatal(err)
	}
	return pv.Cost.Ceiling
}

// grant creates the night's mandate as the Owner: room for fits one-take batches of budget, a per-night ceiling of ceilings one-take ceilings.
func (g *rig) grant(fits, takes int) mandates.Mandate { return g.grantTier(fits, takes, true) }

// grantCeiling is grant with an exact per-night ceiling and a budget that never binds.
func (g *rig) grantCeiling(ceiling money.MicroUSD) mandates.Mandate {
	g.t.Helper()
	m, err := g.ms.Create(g.t.Context(), alice, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "Night shift",
		Budget: ceiling * 10, ValidUntil: lt.FixedNow.Add(24 * time.Hour),
		Scope: mandates.Scope{Modes: []string{"new_seed", "style_edit"}, Engines: []string{"yue2"}, MinStars: 3, MaxTakesPerLineage: 50,
			MaxTakesPerBatch: 12, MaxBatchCeilingMicroUSD: ceiling, MaxBatchesPerDay: 4, TierB: true}})
	if err != nil {
		g.t.Fatal(err)
	}
	return m
}

// grantTier is grant with the mandate's tier_b allowance chosen.
func (g *rig) grantTier(fits, takes int, tierB bool) mandates.Mandate {
	g.t.Helper()
	c := g.ceilingOfOne()
	m, err := g.ms.Create(g.t.Context(), alice, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "Night shift",
		Budget: money.MicroUSD(fits)*c*money.MicroUSD(takes) + c, ValidUntil: lt.FixedNow.Add(24 * time.Hour),
		Scope: mandates.Scope{Modes: []string{"new_seed", "style_edit"}, Engines: []string{"yue2"}, MinStars: 3, MaxTakesPerLineage: 50,
			MaxTakesPerBatch: 12, MaxBatchCeilingMicroUSD: money.MicroUSD(takes) * c, MaxBatchesPerDay: 4, TierB: tierB}})
	if err != nil {
		g.t.Fatal(err)
	}
	return m
}

func (g *rig) key(night string) schedule.Key {
	return schedule.Key{Day: night, At: "01:30", Planned: g.clock()}
}

func (g *rig) count(q string, args ...any) int {
	g.t.Helper()
	var n int
	if err := g.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		g.t.Fatal(err)
	}
	return n
}

func (g *rig) run(night string) Run {
	g.t.Helper()
	run, ok, err := g.r.RunOf(g.t.Context(), night)
	if err != nil || !ok {
		g.t.Fatalf("no run for %s: %v", night, err)
	}
	return run
}

// auditOps returns the ops of the audit rows with the prefix, oldest first.
func (g *rig) auditRows(prefix string) []string {
	g.t.Helper()
	rows, err := g.st.R.Query(`SELECT op || '|' || principal_id || '|' || transport || '|' || status || '|' || cost_micro_usd || '|' || created_ids_json FROM audit_log WHERE op LIKE ? ORDER BY id`, prefix+"%")
	if err != nil {
		g.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			g.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

var _ = sql.ErrNoRows
