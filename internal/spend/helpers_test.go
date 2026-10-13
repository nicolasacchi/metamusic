package spend

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

// clockbox is a per-rig settable clock (no globals).
type clockbox struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clockbox) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clockbox) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

type rig struct {
	t      *testing.T
	st     *store.Store
	s      *Service
	clk    *clockbox
	recipe string
	n      int
	lease  store.LeaseFence
	obsMu  sync.Mutex
	obs    []Usage
}

var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

type rigOpt func(*Options)

func newRig(t *testing.T, opts ...rigOpt) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	librarytest.FixtureV2(t, st, t0)
	r := &rig{t: t, st: st, clk: &clockbox{t: t0}}
	o := Options{Store: st, Now: r.clk.Now, ConfirmKey: []byte("test-key"), Location: time.UTC,
		Observer: func(u Usage) { r.obsMu.Lock(); r.obs = append(r.obs, u); r.obsMu.Unlock() }}
	for _, f := range opts {
		f(&o)
	}
	r.s = New(o)
	if err := st.R.QueryRow(`SELECT id FROM recipes WHERE engine = 'yue2' ORDER BY id LIMIT 1`).Scan(&r.recipe); err != nil {
		t.Fatal(err)
	}
	r.exec(`INSERT INTO runner_lease VALUES(1,'o1',?,?,?)`, store.FormatTime(t0), store.FormatTime(t0), store.FormatTime(t0.Add(24*time.Hour)))
	r.lease = store.LeaseFence{Owner: "o1", Now: t0}
	return r
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) val(q string, args ...any) int64 {
	r.t.Helper()
	var n sql.NullInt64
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n.Int64
}

func (r *rig) str(q string, args ...any) string {
	r.t.Helper()
	var n sql.NullString
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n.String
}

func (r *rig) tx(fn func(tx *sql.Tx) error) error {
	r.t.Helper()
	return r.s.RunTx(context.Background(), fn)
}

func (r *rig) must(fn func(tx *sql.Tx) error) {
	r.t.Helper()
	if err := r.tx(fn); err != nil {
		r.t.Fatal(err)
	}
}

type seeded struct {
	Estimate, Batch string
	Jobs, Attempts  []string
}

type seedSpec struct {
	name       string
	principal  string // default human:h1
	state      string // active (default) | awaiting_confirmation
	jobs       int    // default 1
	max, retry int64  // default 1000 / 500
	created    time.Time
	timeoutS   int // default 186
	noApproval bool
}

// seedBatch inserts an estimate, a batch, its approval and jobs, with no ledger rows.
func (r *rig) seedBatch(sp seedSpec) seeded {
	r.t.Helper()
	r.n++
	if sp.name == "" {
		sp.name = fmt.Sprintf("b%d", r.n)
	}
	if sp.principal == "" {
		sp.principal = "human:h1"
	}
	if sp.state == "" {
		sp.state = "active"
	}
	if sp.jobs == 0 {
		sp.jobs = 1
	}
	if sp.max == 0 {
		sp.max, sp.retry = 1000, 500
	}
	if sp.created.IsZero() {
		sp.created = t0
	}
	if sp.timeoutS == 0 {
		sp.timeoutS = 186
	}
	kind := "human"
	if len(sp.principal) > 6 && sp.principal[:6] == "agent:" {
		kind = "agent"
	}
	s := seeded{Estimate: "est-" + sp.name, Batch: "bat-" + sp.name}
	ts := store.FormatTime(sp.created)
	exp := store.FormatTime(sp.created.Add(10 * time.Minute))
	r.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id)
		VALUES(?,?,?,'yue2','fake','[]','[]',?,'{}',10,?,?,?,690000,2,20,0,'[]',?,?,?,?,?)`,
		s.Estimate, sp.principal, kind, sp.jobs, sp.max, sp.retry, sp.max+sp.retry, "hash-"+sp.name, ts, exp, ts, s.Batch)
	r.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at) VALUES(?,?,?,?,?,'api','yue2','fake',?,NULL,10,?,?,?,690000,200,15,2,20,?,?)`,
		s.Batch, s.Estimate, sp.principal, kind, sp.name, sp.jobs, sp.max, sp.retry, sp.max+sp.retry, sp.state, ts)
	if !sp.noApproval && sp.state == "awaiting_confirmation" {
		r.exec(`INSERT INTO approvals(batch_id,state,reasons,created_at,expires_at) VALUES(?,'pending','["takes_over_limit"]',?,?)`,
			s.Batch, ts, store.FormatTime(sp.created.Add(24*time.Hour)))
	} else if !sp.noApproval {
		r.exec(`INSERT INTO approvals(batch_id,state,reasons,via,decided_by,decided_at,created_at,expires_at) VALUES(?,'auto','[]','auto',?,?,?,?)`,
			s.Batch, sp.principal, ts, ts, store.FormatTime(sp.created.Add(24*time.Hour)))
	}
	for i := range sp.jobs {
		id := fmt.Sprintf("job-%s-%d", sp.name, i)
		state := "queued"
		if sp.state == "awaiting_confirmation" {
			state = "awaiting_confirmation"
		}
		r.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,
			timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
			VALUES(?,?,?,'generate','yue2','fake',?,'reroll_seed','{}','T',216,96,?,10,20,?,?,?,?)`,
			id, s.Batch, i, r.recipe, sp.timeoutS, state, ts, ts, ts)
		s.Jobs = append(s.Jobs, id)
	}
	return s
}

// attempt inserts a pending attempt of a job.
func (r *rig) attempt(job string, n int) string {
	r.t.Helper()
	id := fmt.Sprintf("att-%s-%d", job, n)
	ts := store.FormatTime(t0)
	r.exec(`INSERT INTO job_attempts(id,job_id,n,backend,state,submitted_at,window_ends_at,price_pph_micro_usd,created_at)
		VALUES(?,?,?,'fake','pending',?,?,690000,?)`, id, job, n, ts, ts, ts)
	return id
}

func (r *rig) token(id string) {
	r.t.Helper()
	r.exec(`INSERT INTO api_tokens(id,name,prefix,hmac_hash,scopes,created_at) VALUES(?,?,?,?,?,?)`,
		id, "n-"+id, "p", "h-"+id, "read,generate", store.FormatTime(t0))
}

// raw writes a ledger row without any validation (for usage fixtures).
func (r *rig) raw(at time.Time, kind string, micro int64, batch, job, principal, reason string, window any) {
	r.t.Helper()
	var b, j any
	if batch != "" {
		b = batch
	}
	if job != "" {
		j = job
	}
	r.exec(`INSERT INTO ledger(at,kind,micro_usd,batch_id,job_id,window_start,principal_id,reason) VALUES(?,?,?,?,?,?,?,?)`,
		store.FormatTime(at), kind, micro, b, j, window, principal, reason)
}
