package mandates

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engineadopt"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

var (
	owner    = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Role: auth.RoleOwner, User: "alice", Via: "local"}
	editor   = auth.Principal{Kind: auth.KindHuman, ID: "bob", Name: "bob", Role: auth.RoleEditor, User: "bob", Via: "local"}
	listener = auth.Principal{Kind: auth.KindHuman, ID: "carol", Name: "carol", Role: auth.RoleListener, User: "carol", Via: "local"}
	ownerDev = auth.Principal{Kind: auth.KindDevice, ID: "dev1", Name: "phone", Role: auth.RoleOwner, User: "alice", Via: "device"}
	agent    = auth.Principal{Kind: auth.KindAgent, ID: "ag1", Name: "ag1", Via: "token", Scopes: []auth.Scope{auth.ScopeAdmin, auth.ScopeGenerate}}
)

type clockbox struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clockbox) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clockbox) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

type cancelCall struct {
	batches []string
	by      string
}

type rig struct {
	t       *testing.T
	st      *store.Store
	svc     *Service
	spend   *spend.Service
	adopt   *engineadopt.Service
	clk     *clockbox
	calls   []cancelCall
	cancel  func(ctx context.Context, tx *sql.Tx, ids []string, by string) error
	wakes   int
	n       int
	rootIDs []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	librarytest.FixtureV2(t, st, t0)
	r := &rig{t: t, st: st, clk: &clockbox{t: t0}}
	r.spend = spend.New(spend.Options{Store: st, Now: r.clk.Now, ConfirmKey: []byte("k"), Location: time.UTC})
	r.adopt = engineadopt.New(engineadopt.Options{Store: st, Now: r.clk.Now})
	r.svc = New(Options{Store: st, Spend: r.spend, Adoption: r.adopt, Now: r.clk.Now,
		NewID: func() string { r.n++; return fmt.Sprintf("m-%03d", r.n) }, Wake: func() { r.wakes++ },
		Canceller: func(ctx context.Context, tx *sql.Tx, ids []string, by string) error {
			r.calls = append(r.calls, cancelCall{ids, by})
			if r.cancel != nil {
				return r.cancel(ctx, tx, ids, by)
			}
			return nil
		}})
	rows, err := st.R.Query(`SELECT id FROM tracks WHERE root_track_id = id ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		r.rootIDs = append(r.rootIDs, id)
	}
	return r
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rig) str(q string, args ...any) string {
	r.t.Helper()
	var v sql.NullString
	if err := r.st.R.QueryRow(q, args...).Scan(&v); err != nil {
		r.t.Fatal(err)
	}
	return v.String
}

// goodScope is a valid scope for a budget of 1 000 000.
func goodScope() Scope {
	return Scope{Modes: []string{"new_seed", "style_edit"}, Engines: []string{"yue2"}, MinStars: 3, MaxTakesPerLineage: 3, MaxTakesPerBatch: 6,
		MaxBatchCeilingMicroUSD: 943_587, MaxBatchesPerDay: 1, TierB: true}
}

func (r *rig) input() CreateInput {
	return CreateInput{Holder: HolderNightShift, Title: "Night shift", Budget: 1_000_000, ValidUntil: t0.Add(7 * 24 * time.Hour), Scope: goodScope()}
}

func (r *rig) create() Mandate {
	r.t.Helper()
	m, err := r.svc.Create(context.Background(), owner, r.input())
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

// batch inserts an estimate and an active batch under a mandate with one queued job.
func (r *rig) batch(mandate string, ceiling int64) string {
	r.t.Helper()
	r.n++
	var mid any
	if mandate != "" {
		mid = mandate
	}
	e, b := fmt.Sprintf("est-%d", r.n), fmt.Sprintf("bat-%d", r.n)
	ts := store.FormatTime(t0)
	r.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id,mandate_id)
		VALUES(?,'agent:nightshift','agent','yue2','fake','[]','[]',1,'{}',10,?,0,?,690000,2,20,0,'[]',?,?,?,?,?,?)`, e, ceiling, ceiling, "h"+e, ts, ts, ts, b, mid)
	r.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at,mandate_id) VALUES(?,?,'agent:nightshift','agent','nightshift','api','yue2','fake',1,NULL,10,?,0,?,690000,200,15,2,20,'active',?,?)`,
		b, e, ceiling, ceiling, ts, mid)
	return b
}
