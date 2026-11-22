package pods_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// fakeAPI is a scriptable pods.API: every call is counted, create answers come from a queue, and the pods it made have a status the test sets.
type fakeAPI struct {
	mu        sync.Mutex
	pods      map[string]*fakePod
	seq       int
	createQ   []createAnswer
	creates   []pods.CreateBody
	deletes   []string
	gets      int
	lists     int
	logsCalls int
	cost      money.MicroUSD // reported hourly rate; 0 = unpriced
	getErr    error
	listErr   error
	deleteErr error
	logs      []string
	keepAlive bool // DELETE does not remove the pod
}

type fakePod struct {
	pods.Pod
	gone bool
}

type createAnswer struct {
	err    error
	effect bool // the pod is created although err is returned (an ambiguous outcome)
}

func newFakeAPI() *fakeAPI { return &fakeAPI{pods: map[string]*fakePod{}, cost: 300000} }

func (f *fakeAPI) CreatePod(_ context.Context, b pods.CreateBody) (pods.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, b)
	var ans createAnswer
	if len(f.createQ) > 0 {
		ans, f.createQ = f.createQ[0], f.createQ[1:]
	}
	if ans.err != nil && !ans.effect {
		return pods.Pod{}, ans.err
	}
	f.seq++
	p := &fakePod{Pod: pods.Pod{ID: fmt.Sprintf("rp%08d", f.seq), Name: b.Name, Status: pods.StatusProvisioning, Image: b.Image}}
	if f.cost > 0 {
		p.Priced, p.CostPPH = true, f.cost
	}
	f.pods[p.ID] = p
	if ans.err != nil {
		return pods.Pod{}, ans.err
	}
	return p.Pod, nil
}

func (f *fakeAPI) GetPod(_ context.Context, id string) (pods.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.getErr != nil {
		return pods.Pod{}, f.getErr
	}
	p, ok := f.pods[id]
	if !ok || p.gone {
		return pods.Pod{}, pods.ErrPodGone
	}
	return p.Pod, nil
}

func (f *fakeAPI) ListPods(context.Context) ([]pods.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []pods.Pod
	for _, p := range f.pods {
		if !p.gone {
			out = append(out, p.Pod)
		}
	}
	return out, nil
}

func (f *fakeAPI) DeletePod(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, id)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if p, ok := f.pods[id]; ok && !f.keepAlive {
		p.gone = true
	}
	return nil
}

func (f *fakeAPI) CancelQueued(context.Context, string) error { return nil }

func (f *fakeAPI) PodLogsTail(context.Context, string, int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logsCalls++
	return f.logs, nil
}

func (f *fakeAPI) CatalogSecurePrices(context.Context, []string) (map[string]money.MicroUSD, error) {
	return nil, errors.New("not used")
}

func (f *fakeAPI) PodBilling(context.Context, time.Time, time.Time, []string) ([]provider.BillBucket, error) {
	return nil, nil
}

func (f *fakeAPI) nCreates() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.creates) }
func (f *fakeAPI) nDeletes() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.deletes) }
func (f *fakeAPI) nMutations() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates) + len(f.deletes)
}
func (f *fakeAPI) nCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates) + len(f.deletes) + f.gets + f.lists + f.logsCalls
}

// setStatus sets the status of the pod made for the n-th create (1-based).
func (f *fakeAPI) setStatus(n int, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pods[fmt.Sprintf("rp%08d", n)].Status = status
}

// vanish makes the n-th pod disappear (a self-terminated or externally deleted pod).
func (f *fakeAPI) vanish(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pods[fmt.Sprintf("rp%08d", n)].gone = true
}

func (f *fakeAPI) env(n int) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates[n-1].Env
}

// lrig is the lifecycle rig: the poll rig (database, clock, queue, signer) plus a lease, a gate, the spend service and the loop.
type lrig struct {
	*srig
	api     *fakeAPI
	l       *pods.Lifecycle
	sp      *spend.Service
	gate    *provider.Gate
	key     atomic.Bool
	mu      sync.Mutex
	trips   []string
	disarms []string
	audits  []string
	opts    pods.LifecycleOptions
	poller  http.Handler
	batchID string
	lquote  string // quote_json of the next lbatch estimate (default {})
}

const (
	lBackend = provider.BackendFakePod
	lDigest  = "sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"
)

func newLRig(t *testing.T, mod ...func(*pods.LifecycleOptions)) *lrig {
	t.Helper()
	s := newSRig(t)
	r := &lrig{srig: s, api: newFakeAPI(), batchID: rigBatch2}
	r.key.Store(true)
	r.sp = spend.New(spend.Options{Store: s.st, Now: s.clk.Now, ConfirmKey: []byte("test-key"), Location: time.UTC})
	r.exec(`INSERT INTO runner_lease(id, owner, acquired_at, heartbeat_at, expires_at) VALUES (1, 'rig', ?, ?, ?)`,
		store.FormatTime(rigNow), store.FormatTime(rigNow), store.FormatTime(rigNow.Add(10000*time.Hour)))
	r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES (?, 1, 'human:alice', ?)`, lBackend, store.FormatTime(rigNow))
	r.gate = provider.NewGate(s.st.R, provider.GateConfig{Mode: provider.ModeFake, Backend: lBackend, KeysLoaded: true}, s.clk.Now)
	r.lbatch(rigBatch2, "est-life", 30, 5_000_000)
	r.opts = pods.LifecycleOptions{
		Store: s.st, API: r.api, Queue: s.q, Signer: s.sig, PublicURL: rigURL, Spend: r.sp, Clock: s.clk, Backend: lBackend,
		Image: "ghcr.io/example/mm-worker@" + lDigest, GPUTypes: []string{"NVIDIA GeForce RTX 4090", "NVIDIA RTX A5000"}, DiskGB: 40,
		Weights3B: strings.Repeat("a", 40), BootS: 1200, IdleS: 120, SlackS: 300, KillSlackS: 15, MaxRecreate: 1,
		MaxRuntime: 6 * time.Hour, Deadman: 10 * time.Minute, OrphanAfter: 180 * time.Second, WindowMax: 8 * time.Hour,
		KeyPresent: r.key.Load, Holding: func() bool { return true },
		Fence: func() store.LeaseFence { return store.LeaseFence{Owner: "rig", Now: s.clk.Now()} },
		State: r.gate.State,
		Trip: func(_ context.Context, reason string, _ map[string]any) error {
			r.mu.Lock()
			r.trips = append(r.trips, reason)
			r.mu.Unlock()
			return nil
		},
		Disarm: func(ctx context.Context, reason string) error {
			r.mu.Lock()
			r.disarms = append(r.disarms, reason)
			r.mu.Unlock()
			_, err := s.st.W.ExecContext(ctx, `UPDATE provider_state SET armed = 0, changed_by = 'system:bulk', changed_at = ?, reason = ? WHERE backend = ?`, store.FormatTime(s.clk.Now()), reason, lBackend)
			return err
		},
		Audit: func(_ context.Context, op string, d map[string]any, _ money.MicroUSD, _ bool) {
			b, _ := json.Marshal(d)
			r.mu.Lock()
			r.audits = append(r.audits, op+" "+string(b))
			r.mu.Unlock()
		},
	}
	for _, m := range mod {
		m(&r.opts)
	}
	r.l = pods.NewLifecycle(r.opts)
	r.poller = pods.NewPoller(pods.PollOptions{Store: s.st, Signer: s.sig, Queue: s.q, Clock: s.clk, KillSlackS: 15, Hold: -1, Nudge: r.l.Nudge}).Handler()
	return r
}

// lbatch inserts an active pod batch of the rig's backend with a roomy ceiling.
func (r *lrig) lbatch(id, est string, nTakes int, ceiling int64) {
	r.t.Helper()
	ts := store.FormatTime(rigNow)
	quote := cmp.Or(r.lquote, "{}")
	r.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id)
		VALUES(?,'human:alice','human','yue2',?,'[]','[]',?,?,10,?,?,?,300000,1,120,0,'[]',?,?,?,?,?)`,
		est, lBackend, nTakes, quote, ceiling-1000, 1000, ceiling, "hash-"+est, ts, ts, ts, id)
	r.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at) VALUES(?,?,'human:alice','human','alice','api','yue2',?,?,NULL,10,?,?,?,300000,0,15,1,120,'active',?)`,
		id, est, lBackend, nTakes, ceiling-1000, 1000, ceiling, ts)
}

// queue puts one take of the lifecycle batch in the queue (job, attempt, assignment and the in-memory spec).
func (r *lrig) queue() string { return r.queueTake(r.batchID, 186) }

// tick runs one pass of the loop.
func (r *lrig) tick() {
	r.t.Helper()
	if err := r.l.Tick(context.Background()); err != nil {
		r.t.Fatalf("tick: %v", err)
	}
}

// step advances the clock and ticks.
func (r *lrig) step(d time.Duration) {
	r.t.Helper()
	r.clk.Advance(d)
	r.tick()
}

func (r *lrig) row(batch string, n, try int) pods.Row {
	r.t.Helper()
	rows, err := pods.PodsOfBatch(context.Background(), r.st.R, batch)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, p := range rows {
		if p.N == n && p.GPUTry == try {
			return p
		}
	}
	r.t.Fatalf("no pod n=%d try=%d of %s (have %d rows)", n, try, batch, len(rows))
	return pods.Row{}
}

func (r *lrig) rows(batch string) []pods.Row {
	r.t.Helper()
	rows, err := pods.PodsOfBatch(context.Background(), r.st.R, batch)
	if err != nil {
		r.t.Fatal(err)
	}
	return rows
}

func (r *lrig) tripped() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.trips...)
}

func (r *lrig) events(podID string) string {
	return r.str(`SELECT COALESCE(group_concat(event, ','), '') FROM (SELECT event FROM pod_events WHERE pod_id = ? ORDER BY id)`, podID)
}

// agent posts a poll for pod n's link, as the pod's agent would (the link is the create's environment).
func (r *lrig) agent(n int, body any) *httptest.ResponseRecorder {
	r.t.Helper()
	u, err := url.Parse(r.api.env(n)["MM_POLL_URL"])
	if err != nil {
		r.t.Fatal(err)
	}
	return r.poll(u.RequestURI(), body)
}

var bootDoc = map[string]any{"gpu_name": "NVIDIA GeForce RTX 4090", "cuda": "12.8", "weights_ready": true, "load_s": 41.0}

// walkToReady drives the first pod of the batch to ready: the create, RUNNING, the boot report and the promotion.
func (r *lrig) walkToReady() pods.Row {
	r.t.Helper()
	r.tick()
	if r.api.nCreates() != 1 {
		r.t.Fatalf("expected one create, got %d", r.api.nCreates())
	}
	r.api.setStatus(1, pods.StatusRunning)
	r.step(30 * time.Second)
	if w := r.agent(1, state("booting")); w.Code != 200 {
		r.t.Fatalf("booting poll: %d %s", w.Code, w.Body)
	}
	r.clk.Advance(time.Minute)
	if w := r.agent(1, idle().with("boot", bootDoc)); w.Code != 200 {
		r.t.Fatalf("boot poll: %d %s", w.Code, w.Body)
	}
	r.tick()
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateReady {
		r.t.Fatalf("pod is %s, want ready (events %s)", row.State, r.events(row.ID))
	}
	return row
}
