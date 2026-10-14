package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

const (
	testPublicURL = "https://mm.example.com"
	testRecipe    = "01900000-0000-7000-8000-00000000f001"
	testPrice     = money.MicroUSD(690000)
)

// noteLog records notifications (no globals: one per rig).
type noteLog struct {
	mu sync.Mutex
	ms []notify.Message
}

func (n *noteLog) Notify(_ context.Context, m notify.Message) error {
	n.mu.Lock()
	n.ms = append(n.ms, m)
	n.mu.Unlock()
	return nil
}

func (n *noteLog) all() []notify.Message {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notify.Message(nil), n.ms...)
}

// stubIngest is the stub Ingester: it inserts one synthetic track per call unless told otherwise.
type stubIngest struct {
	mu      sync.Mutex
	st      *store.Store
	n       int
	calls   int
	err     error
	block   chan struct{} // when non-nil, IngestTake waits for it to close
	entered chan struct{}
	last    []upload.Received
	lastJob JobRow
	lastAtt AttemptRow
	lastOut provider.Output
	env     string // env_id written on the track (needs an env_fingerprints row)
}

func (s *stubIngest) IngestTake(ctx context.Context, job JobRow, att AttemptRow, files []upload.Received, out provider.Output) (string, error) {
	s.mu.Lock()
	s.calls++
	s.last, s.lastJob, s.lastAtt, s.lastOut = files, job, att, out
	block, entered, err := s.block, s.entered, s.err
	s.n++
	n := s.n
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("01900000-0000-7000-8000-%012d", 900000+n)
	ts := store.FormatTime(t0)
	h := sha256.Sum256([]byte(id))
	hx := hex.EncodeToString(h[:])
	var env any
	if s.env != "" {
		env = s.env
	}
	_, ierr := s.st.W.ExecContext(ctx, `INSERT INTO tracks(id, slug, title, recipe_id, parent_track_id, root_track_id, generation, status, status_set_by, origin,
		duration_s, sample_rate, bits_per_sample, channels, total_samples, master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5,
		import_rule, recorded_at, created_at, env_id) VALUES(?, ?, 'T', ?, NULL, ?, 0, 'draft', 'system:job', 'job', 216, 48000, 24, 2, 10368000, ?, ?, 1, ?, ?, ?, 'job', ?, ?, ?)`,
		id, "s"+hx[:8], job.RecipeID, id, "audio/masters/"+hx+".flac", hx, hx, hx, hx[:32], ts, ts, env)
	if ierr != nil {
		return "", ierr
	}
	return id, nil
}

func (s *stubIngest) callCount() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

type rig struct {
	t     *testing.T
	st    *store.Store
	clk   *clocktest.Manual
	data  *os.Root
	up    *upload.Service
	sp    *spend.Service
	be    *providertest.Backend
	run   *Runner
	ing   *stubIngest
	notes *noteLog
	hub   *Hub
	sig   *upload.Signer
	ids   atomic.Int64
	nBat  int

	mu       sync.Mutex
	status   map[string]provider.RemoteStatus
	submitFn func(spec provider.TakeSpec) (provider.Ref, error)
	cancels  map[string]int
	polls    map[string]int
	opts     RunnerOptions
	dbPath   string
	mods     []rigOpt
	fail     *failCtl // when set, the store goes through failing connectors

	// scripted reconciler-side backend behaviour (nil => the default of providertest)
	sweepFn   func() (provider.SweepReport, error)
	billingFn func(from, to time.Time) ([]provider.BillBucket, error)
	purgeFn   func() error
	scaleFn   func() error
	revertFn  func(d []provider.Drift) error
	quoteFn   func() (provider.Quote, error)
	pollErr   error
	cancelErr error
	caps      provider.Capabilities
	billCalls [][2]time.Time
	purges    int
	scales    int
	reverts   [][]provider.Drift
}

type rigOpt func(*rig, *RunnerOptions)

func (r *rig) newID() string {
	return fmt.Sprintf("01900000-0000-7000-8000-%012d", r.ids.Add(1))
}

func newRig(t *testing.T, mods ...rigOpt) *rig {
	t.Helper()
	return newRigAt(t, filepath.Join(t.TempDir(), "s.db"), t.TempDir(), true, mods...)
}

// newRigAt builds a rig over the database at dbPath and the data directory dataDir; fresh seeds the
// recipe and the provider state, a thawed rig finds them in the database already.
func newRigAt(t *testing.T, dbPath, dataDir string, fresh bool, mods ...rigOpt) *rig {
	t.Helper()
	probe := &rig{}
	for _, m := range mods {
		m(probe, &RunnerOptions{})
	}
	var st *store.Store
	if probe.fail != nil && !fresh {
		st = failingStore(t, dbPath, probe.fail) // a thawed database is migrated already
	} else {
		var err error
		if st, err = store.Open(t.Context(), dbPath); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if probe.fail != nil { // every component goes through the failing connectors
			st = failingStore(t, dbPath, probe.fail)
		}
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	r := &rig{t: t, mods: mods, dbPath: dbPath, fail: probe.fail, st: st, clk: clocktest.NewManual(t0), data: root, notes: &noteLog{}, hub: NewHub(), sig: upload.NewSigner([]byte("synthetic-secret-key-for-tests-0001")),
		status: map[string]provider.RemoteStatus{}, cancels: map[string]int{}, polls: map[string]int{}}
	r.ing = &stubIngest{st: st}
	r.sp = spend.New(spend.Options{Store: st, Now: r.clk.Now, NewID: r.newID, ConfirmKey: []byte("test-key"), Location: time.UTC})
	r.caps = provider.Capabilities{ScaleToZero: true, RevertDrift: true, Billing: "hour"}
	r.be = providertest.New(provider.BackendFake, providertest.Script{
		Capabilities: &r.caps,
		Quote: func(context.Context) (provider.Quote, error) {
			r.mu.Lock()
			fn := r.quoteFn
			r.mu.Unlock()
			if fn != nil {
				return fn()
			}
			return providertest.GoodQuote(provider.BackendFake, r.clk.Now()), nil
		},
		Sweep: func(context.Context) (provider.SweepReport, error) {
			r.mu.Lock()
			fn := r.sweepFn
			r.mu.Unlock()
			if fn != nil {
				return fn()
			}
			return provider.SweepReport{ObservedAt: r.clk.Now()}, nil
		},
		Billing: func(_ context.Context, from, to time.Time) ([]provider.BillBucket, error) {
			r.mu.Lock()
			fn := r.billingFn
			r.billCalls = append(r.billCalls, [2]time.Time{from, to})
			r.mu.Unlock()
			if fn != nil {
				return fn(from, to)
			}
			return nil, nil
		},
		PurgeQueue: func(context.Context) error {
			r.mu.Lock()
			fn := r.purgeFn
			r.purges++
			r.mu.Unlock()
			if fn != nil {
				return fn()
			}
			return nil
		},
		ScaleToZero: func(context.Context) error {
			r.mu.Lock()
			fn := r.scaleFn
			r.scales++
			r.mu.Unlock()
			if fn != nil {
				return fn()
			}
			return nil
		},
		RevertDrift: func(_ context.Context, d []provider.Drift) error {
			r.mu.Lock()
			fn := r.revertFn
			r.reverts = append(r.reverts, d)
			r.mu.Unlock()
			if fn != nil {
				return fn(d)
			}
			return nil
		},
		Submit: func(_ context.Context, spec provider.TakeSpec) (provider.Ref, error) {
			r.mu.Lock()
			fn := r.submitFn
			r.mu.Unlock()
			if fn != nil {
				return fn(spec)
			}
			return provider.Ref{Backend: provider.BackendFake, ID: "ref-" + spec.AttemptID}, nil
		},
		Poll: func(_ context.Context, ref provider.Ref) (provider.RemoteStatus, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.polls[ref.ID]++
			if r.pollErr != nil {
				return provider.RemoteStatus{}, r.pollErr
			}
			if s, ok := r.status[ref.ID]; ok {
				return s, nil
			}
			return provider.RemoteStatus{State: provider.Queued, Raw: "IN_QUEUE"}, nil
		},
		Cancel: func(_ context.Context, ref provider.Ref) error {
			r.mu.Lock()
			r.cancels[ref.ID]++
			err := r.cancelErr
			r.mu.Unlock()
			return err
		},
	})
	r.opts = RunnerOptions{
		Store: st, Backend: r.be, Engines: registry.Default(), Spend: r.sp, Ingest: r.ing, Notify: r.notes, Hub: r.hub,
		Clock: r.clk, NewID: r.newID, Owner: "runner-1", PublicURL: testPublicURL, TTL: 2 * time.Hour, MaxRuntime: 15 * time.Minute,
		ColdStartS: 180, KillSlackS: 15, Expect: provider.WorkerExpect{Weights: map[string]string{"m-a-p/YuE2-3B": "0000000000000000000000000000000000000001"}},
	}
	r.up = upload.NewService(upload.ServiceOptions{Data: root, Signer: r.sig, Store: st, Clock: r.clk, PublicURL: testPublicURL, NewID: r.newID,
		Log: slog.New(slog.DiscardHandler), Wake: func() {
			if r.run != nil {
				r.run.Wake()
			}
		}})
	r.opts.Uploads = r.up
	for _, m := range mods {
		m(r, &r.opts)
	}
	r.run = NewRunner(r.opts)
	if !fresh {
		return r
	}
	ts := store.FormatTime(t0)
	r.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'yue2','syn-a','{"cot":"full","id":"syn-a","lyrics":"la","seed":1,"style":"pop"}','pop','la',1,'full','h1','c1','import',?)`, testRecipe, ts)
	q, _ := json.Marshal(providertest.GoodQuote(provider.BackendFake, t0))
	r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES('fake', 1, 'system:default', ?, ?, ?)`, ts, string(q), ts)
	return r
}

// withFailing routes the rig's store through failing connectors controlled by ctl.
func withFailing(ctl *failCtl) rigOpt { return func(r *rig, _ *RunnerOptions) { r.fail = ctl } }

// withRunner returns the rig option that edits the runner options before the runner is built.
func withOpts(f func(o *RunnerOptions)) rigOpt { return func(_ *rig, o *RunnerOptions) { f(o) } }

// second returns a runner on the same store under another owner.
func (r *rig) second(owner string, mods ...func(*RunnerOptions)) *Runner {
	o := r.opts
	o.Owner = owner
	for _, m := range mods {
		m(&o)
	}
	return NewRunner(o)
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
		r.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (r *rig) str(q string, args ...any) string {
	r.t.Helper()
	var s *string
	if err := r.st.R.QueryRow(q, args...).Scan(&s); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	if s == nil {
		return ""
	}
	return *s
}

func (r *rig) sum(q string, args ...any) int64 {
	r.t.Helper()
	var n *int64
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	if n == nil {
		return 0
	}
	return *n
}

type batchSpec struct {
	jobs       int
	principal  string
	state      string // active (default) | awaiting_confirmation | rejected | expired
	workersMax int
	timeoutS   int
	ceiling    int64
	backend    string // default fake
	source     string // jobs.source_track_id
	recipe     string // default testRecipe
	draft      string // jobs.draft_id
	operator   string // default reroll_seed
	mutation   string // jobs.mutation_json, default {}
	rationale  string // batches.rationale
}

type seeded struct {
	Batch string
	Jobs  []string
}

// seedBatch inserts an estimate, a batch, its approval, its jobs and their `take` reserves.
func (r *rig) seedBatch(sp batchSpec) seeded {
	r.t.Helper()
	r.nBat++
	if sp.jobs == 0 {
		sp.jobs = 1
	}
	if sp.principal == "" {
		sp.principal = "human:h1"
	}
	if sp.state == "" {
		sp.state = "active"
	}
	if sp.workersMax == 0 {
		sp.workersMax = 2
	}
	if sp.timeoutS == 0 {
		sp.timeoutS = 186
	}
	if sp.ceiling == 0 {
		sp.ceiling = 10_000_000
	}
	if sp.backend == "" {
		sp.backend = "fake"
	}
	if sp.recipe == "" {
		sp.recipe = testRecipe
	}
	if sp.operator == "" {
		sp.operator = "reroll_seed"
	}
	if sp.mutation == "" {
		sp.mutation = "{}"
	}
	var rationale any
	if sp.rationale != "" {
		rationale = sp.rationale
	}
	kind := "human"
	if len(sp.principal) > 6 && sp.principal[:6] == "agent:" {
		kind = "agent"
	}
	s := seeded{Batch: fmt.Sprintf("bat-%d", r.nBat)}
	est := fmt.Sprintf("est-%d", r.nBat)
	ts := store.FormatTime(r.clk.Now())
	exp := store.FormatTime(r.clk.Now().Add(24 * time.Hour))
	r.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id)
		VALUES(?,?,?,'yue2',?,'[]','[]',?,'{}',10,?,0,?,690000,?,20,0,'[]',?,?,?,?,?)`,
		est, sp.principal, kind, sp.backend, sp.jobs, sp.ceiling, sp.ceiling, sp.workersMax, "hash-"+est, ts, exp, ts, s.Batch)
	state := sp.state
	if state == "rejected" || state == "expired" {
		state = "awaiting_confirmation"
	}
	r.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at) VALUES(?,?,?,?,?,'api','yue2',?,?,?,10,?,0,?,690000,200,15,?,20,?,?)`,
		s.Batch, est, sp.principal, kind, "n", sp.backend, sp.jobs, rationale, sp.ceiling, sp.ceiling, sp.workersMax, state, ts)
	if state == "awaiting_confirmation" {
		r.exec(`INSERT INTO approvals(batch_id,state,reasons,created_at,expires_at) VALUES(?,'pending','["takes_over_limit"]',?,?)`, s.Batch, ts, exp)
	} else {
		r.exec(`INSERT INTO approvals(batch_id,state,reasons,via,decided_by,decided_at,created_at,expires_at) VALUES(?,'auto','[]','auto',?,?,?,?)`, s.Batch, sp.principal, ts, ts, exp)
	}
	jobState, queuedAt := "queued", any(ts)
	if state == "awaiting_confirmation" {
		jobState, queuedAt = "awaiting_confirmation", nil
	}
	take := money.ForSeconds(int64(sp.timeoutS+15), testPrice)
	for i := range sp.jobs {
		id := fmt.Sprintf("job-%d-%d", r.nBat, i)
		var src any
		if sp.source != "" {
			src = sp.source
		}
		var draft any
		if sp.draft != "" {
			draft = sp.draft
		}
		r.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,draft_id,operator,mutation_json,title,expected_audio_s,expected_s,
			timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at)
			VALUES(?,?,?,'generate','yue2',?,?,?,?,?,?,'T',216,96,?,10,?,?,?,?,?,?)`,
			id, s.Batch, i, sp.backend, sp.recipe, src, draft, sp.operator, sp.mutation, sp.timeoutS, int64(take), jobState, ts, queuedAt, ts, ts)
		bid, jid, p := s.Batch, id, sp.principal
		if err := r.sp.RunTx(r.t.Context(), func(tx *sql.Tx) error {
			return r.sp.Ledger().Append(r.t.Context(), tx, spend.Entry{Kind: spend.KindReserve, MicroUSD: take, BatchID: &bid, JobID: &jid, PrincipalID: p, Reason: "take"})
		}); err != nil {
			r.t.Fatal(err)
		}
		s.Jobs = append(s.Jobs, id)
	}
	switch sp.state {
	case "rejected":
		r.exec(`UPDATE approvals SET state='rejected', via='web', decided_by='human:h1', decided_at=? WHERE batch_id=?`, ts, s.Batch)
		r.exec(`UPDATE batches SET state='rejected', finished_at=? WHERE id=?`, ts, s.Batch)
	case "expired":
		r.exec(`UPDATE approvals SET state='expired', via='expiry', decided_by='system:reconciler', decided_at=? WHERE batch_id=?`, ts, s.Batch)
		r.exec(`UPDATE batches SET state='expired', finished_at=? WHERE id=?`, ts, s.Batch)
	}
	return s
}

// step runs one runner tick and waits for the ingest pool.
func (r *rig) step() StepReport {
	r.t.Helper()
	rep, err := r.run.Step(r.t.Context())
	if err != nil {
		r.t.Fatalf("Step: %v", err)
	}
	if err := r.run.Drain(r.t.Context()); err != nil {
		r.t.Fatalf("Drain: %v", err)
	}
	return rep
}

func (r *rig) steps(n int) {
	r.t.Helper()
	for range n {
		r.step()
	}
}

func (r *rig) advance(d time.Duration) { r.clk.Advance(d) }

func (r *rig) job(id string) jobRec {
	r.t.Helper()
	j, err := loadJob(r.t.Context(), r.st.R, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return j
}

func (r *rig) state(id string) string { r.t.Helper(); return r.job(id).State }

func (r *rig) atts(job string) []attRec {
	r.t.Helper()
	as, err := queryAtts(r.t.Context(), r.st.R, `job_id = ? ORDER BY n`, job)
	if err != nil {
		r.t.Fatal(err)
	}
	return as
}

func (r *rig) att(job string, n int) attRec {
	r.t.Helper()
	as := r.atts(job)
	if len(as) < n {
		r.t.Fatalf("job %s has %d attempts, want %d", job, len(as), n)
	}
	return as[n-1]
}

func (r *rig) events(job string) []string {
	r.t.Helper()
	rows, err := r.st.R.Query(`SELECT event || COALESCE(':' || code, '') FROM job_events WHERE job_id = ? ORDER BY id`, job)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (r *rig) hasEvent(job, ev string) bool {
	r.t.Helper()
	return r.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = ?`, job, ev) > 0
}

// ledger returns Σ micro_usd of a kind and reason (reason "" = any) of a batch.
func (r *rig) ledger(batch, kind, reason string) int64 {
	r.t.Helper()
	if reason == "" {
		return r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ? AND kind = ?`, batch, kind)
	}
	return r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ? AND kind = ? AND reason = ?`, batch, kind, reason)
}

func (r *rig) outstanding(batch string) int64 {
	r.t.Helper()
	v, err := spend.BatchOutstanding(r.t.Context(), r.st.R, batch)
	if err != nil {
		r.t.Fatal(err)
	}
	return int64(v)
}

// setStatus scripts what Poll answers for an attempt's ref.
func (r *rig) setStatus(ref string, st provider.RemoteStatus) {
	r.mu.Lock()
	r.status[ref] = st
	r.mu.Unlock()
}

func (r *rig) refOf(job string, n int) string { r.t.Helper(); return r.att(job, n).Ref }

func (r *rig) cancelCount(ref string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancels[ref]
}

func (r *rig) pollCount(ref string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.polls[ref]
}

func (r *rig) onSubmit(fn func(spec provider.TakeSpec) (provider.Ref, error)) {
	r.mu.Lock()
	r.submitFn = fn
	r.mu.Unlock()
}

func ms(n int64) *int64 { return &n }

// ---- the simulated worker ----

type deliverOpts struct {
	skip        map[string]bool // artifacts not uploaded
	lieSHA      map[string]bool // artifacts reported with a wrong sha
	errorOut    bool            // an error mm-result.json
	noResult    bool            // mm-result.json never uploaded
	truncated   [2]bool
	badTrunc    bool // truncated dict with a wrong key set
	noAudioSec  bool
	allOptional bool              // also upload the optional artifacts
	claim       map[string]bool   // artifacts reported in the output but never uploaded
	forgeTake   bool              // the output names another take
	unreported  map[string]bool   // artifacts uploaded but left out of the output
	bodies      map[string][]byte // artifact bodies (default: a synthetic text)
	e2e         float64           // timings.e2e_seconds of the output
}

func digestHeader(b []byte) string {
	h := sha256.Sum256(b)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(h[:]) + ":"
}

// put sends one upload through the real handler without sockets and returns the status.
func (r *rig) put(rawURL string, body []byte) int {
	r.t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		r.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, u.RequestURI(), bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Digest", digestHeader(body))
	w := httptest.NewRecorder()
	r.up.Handler().ServeHTTP(w, req)
	return w.Code
}

// start performs the start handshake of a spec and returns the status.
func (r *rig) start(spec provider.TakeSpec, ref string) int {
	r.t.Helper()
	u, err := url.Parse(spec.StartURL)
	if err != nil {
		r.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, u.RequestURI(), bytes.NewReader([]byte(`{"runpod_job_id":"`+ref+`"}`)))
	w := httptest.NewRecorder()
	r.up.StartHandler().ServeHTTP(w, req)
	return w.Code
}

// deliver plays a worker: it uploads the artifacts of spec and mm-result.json last, and returns the
// worker output (also what a Completed status carries).
func (r *rig) deliver(spec provider.TakeSpec, o deliverOpts) provider.Output {
	r.t.Helper()
	out := provider.Output{Contract: provider.Contract, TakeID: spec.JobID, AttemptID: spec.AttemptID, Status: "ok",
		Artifacts: map[string]provider.ArtifactDigest{}, Truncated: map[string]bool{"abc": o.truncated[0], "semantic": o.truncated[1]},
		Env: provider.OutputEnv{Engine: "yue2", PkgVersion: "0.1.6", CodeCommit: "0000000000000000000000000000000000000003", Decoder: "m-a-p/YuE2-Vae",
			GPUName: "NVIDIA RTX A5000", CUDA: "12.8", Torch: "2.10.0", WorkerBuild: "test", Python: "3.12", Weights: spec.Expect.Weights}}
	if !o.noAudioSec {
		n := json.Number("215.8")
		out.AudioSeconds = &n
	}
	if o.e2e > 0 {
		n := json.Number(fmt.Sprint(o.e2e))
		out.Timings = map[string]*json.Number{"e2e_seconds": &n}
	}
	if o.badTrunc {
		out.Truncated = map[string]bool{"abc": false}
	}
	if o.errorOut {
		out.Status = "error"
		out.Error = &provider.OutputError{Code: "PIPELINE_ERROR", Message: "synthetic failure"}
	}
	for _, name := range sortedKeys(spec.Uploads) {
		if name == provider.ResultArtifact || o.skip[name] || (!o.allOptional && !requiredName(name)) {
			continue
		}
		body := []byte("synthetic " + name + " " + spec.AttemptID)
		if b, ok := o.bodies[name]; ok {
			body = b
		}
		if code := r.put(spec.Uploads[name].URL, body); code != http.StatusCreated {
			r.t.Fatalf("upload %s: status %d (job %s attempt %s: %v)", name, code, r.str(`SELECT state FROM jobs WHERE id = ?`, spec.JobID),
				r.str(`SELECT state FROM job_attempts WHERE id = ?`, spec.AttemptID), r.events(spec.JobID))
		}
		h := sha256.Sum256(body)
		sha := hex.EncodeToString(h[:])
		if o.lieSHA[name] {
			sha = hex.EncodeToString(make([]byte, 32))
		}
		if o.unreported[name] {
			continue
		}
		out.Artifacts[name] = provider.ArtifactDigest{SHA256: sha, Bytes: int64(len(body))}
	}
	for name := range o.claim {
		out.Artifacts[name] = provider.ArtifactDigest{SHA256: strings.Repeat("a", 64), Bytes: 7}
	}
	if o.forgeTake {
		out.TakeID = "some-other-take"
	}
	if !o.noResult {
		raw, err := json.Marshal(out)
		if err != nil {
			r.t.Fatal(err)
		}
		if code := r.put(spec.Uploads[provider.ResultArtifact].URL, raw); code != http.StatusCreated {
			r.t.Fatalf("upload mm-result: status %d", code)
		}
	}
	return out
}

func requiredName(n string) bool {
	switch n {
	case "audio.flac", "request.json", "config.json", "result.json":
		return true
	}
	return false
}

func rawOutput(o provider.Output) json.RawMessage {
	b, _ := json.Marshal(o)
	return b
}

// completed is the status of a finished render with the given output and execution time.
func completed(o provider.Output, execMS int64) provider.RemoteStatus {
	return provider.RemoteStatus{State: provider.Completed, Raw: "COMPLETED", ExecutionMS: &execMS, Output: rawOutput(o), WorkerID: "w1"}
}

// lastSpec returns the most recently submitted TakeSpec.
func (r *rig) lastSpec() provider.TakeSpec {
	r.t.Helper()
	specs := r.be.Specs()
	if len(specs) == 0 {
		r.t.Fatal("nothing was submitted")
	}
	return specs[len(specs)-1]
}

// runToStarting claims and submits the first job so it is `starting` with attempt 1 submitted.
func (r *rig) submitOne(job string) provider.TakeSpec {
	r.t.Helper()
	r.step()
	if s := r.state(job); s != StateStarting {
		r.t.Fatalf("job %s is %s, want starting", job, s)
	}
	return r.lastSpec()
}

func providerOutput() provider.Output { return provider.Output{} }
