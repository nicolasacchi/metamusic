package pods_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

const (
	rigBatch  = "01900000-0000-7000-8000-00000000b001"
	rigBatch2 = "01900000-0000-7000-8000-00000000b002"
	rigPod    = "01900000-0000-7000-8000-00000000d001"
	rigPod2   = "01900000-0000-7000-8000-00000000d002"
	rigURL    = "https://mm.example.com"
)

var rigNow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// srig is a database with a batch (and jobs, attempts, assignments on demand), a clock, a queue and a poll handler.
type srig struct {
	t      *testing.T
	st     *store.Store
	clk    *clocktest.Manual
	q      *pods.Queue
	sig    *pods.Signer
	h      http.Handler
	logs   *syncBuf
	nudges atomic.Int32
	deadl  time.Time
	jobs   int
}

func newSRig(t *testing.T) *srig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &srig{t: t, st: st, clk: clocktest.NewManual(rigNow), q: pods.NewQueue(), sig: pods.NewSigner([]byte("synthetic-pod-poll-key-for-tests-01")), logs: &syncBuf{}, deadl: rigNow.Add(5 * time.Hour)}
	r.h = r.handler(-1)
	ts := store.FormatTime(rigNow)
	r.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES('01900000-0000-7000-8000-00000000f001','yue2','syn-a','{"id":"syn-a","seed":1}','pop','la',1,'full','h1','c1','import',?)`, ts)
	r.batch(rigBatch, "est1")
	return r
}

// handler is a poll handler over the rig; hold < 0 never waits for a take, 0 is the real HoldS.
func (r *srig) handler(hold time.Duration) http.Handler {
	return pods.NewPoller(pods.PollOptions{Store: r.st, Signer: r.sig, Queue: r.q, Clock: r.clk, Log: slog.New(slog.NewJSONHandler(r.logs, nil)),
		KillSlackS: 15, Nudge: func() { r.nudges.Add(1) }, Hold: hold}).Handler()
}

func (r *srig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *srig) str(q string, args ...any) string {
	r.t.Helper()
	var s *string
	if err := r.st.R.QueryRow(q, args...).Scan(&s); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	if s == nil {
		return "<null>"
	}
	return *s
}

func (r *srig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (r *srig) batch(id, est string) {
	r.t.Helper()
	ts := store.FormatTime(rigNow)
	r.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id)
		VALUES(?,'human:alice','human','yue2','fake','[]','[]',1,'{}',10,1000,500,1500,690000,1,20,0,'[]',?,?,?,?,?)`, est, "hash-"+est, ts, ts, ts, id)
	r.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at) VALUES(?,?,'human:alice','human','alice','api','yue2','fake',1,NULL,10,1000,500,1500,690000,0,15,1,20,'active',?)`, id, est, ts)
}

// take creates a job with one pending attempt and its queued assignment, and returns the attempt id.
func (r *srig) take(batchID string) string {
	r.t.Helper()
	r.jobs++
	ts := store.FormatTime(rigNow)
	job := fmt.Sprintf("01900000-0000-7000-8000-0000000a%04d", r.jobs)
	att := fmt.Sprintf("01900000-0000-7000-8000-0000000c%04d", r.jobs)
	r.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,
		timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
		VALUES(?,?,?,'generate','yue2','fake','01900000-0000-7000-8000-00000000f001','reroll_seed','{}','T',216,96,186,10,20,'queued',?,?,?)`, job, batchID, r.jobs, ts, ts, ts)
	r.exec(`INSERT INTO job_attempts(id,job_id,n,backend,state,ref,submitted_at,window_ends_at,price_pph_micro_usd,created_at)
		VALUES(?,?,1,'fake','pending',NULL,?,?,690000,?)`, att, job, ts, store.FormatTime(r.deadl), ts)
	if err := pods.QueueAssignment(context.Background(), r.st.W, att, batchID, rigNow); err != nil {
		r.t.Fatal(err)
	}
	return att
}

// spec is a v1 TakeSpec for an attempt; it builds through provider.BuildWorkerInput.
func (r *srig) spec(attempt string, timeoutS int) provider.TakeSpec {
	r.t.Helper()
	return provider.TakeSpec{
		JobID: "01900000-0000-7000-8000-0000000a0001", AttemptID: attempt, Engine: "yue2", Request: json.RawMessage(`{"id":"syn-a","seed":1}`),
		TimeoutS: timeoutS, TTL: time.Hour, UploadBudgetS: 600, StartURL: rigURL + "/worker/v1/attempts/x/start?exp=1&sig=s",
		Uploads: map[string]provider.UploadTarget{
			"audio.flac":     {URL: rigURL + "/worker/v1/uploads/u1?exp=1&sig=s", MaxBytes: 1 << 20},
			"mm-result.json": {URL: rigURL + "/worker/v1/uploads/u2?exp=1&sig=s", MaxBytes: 1 << 16},
			"request.json":   {URL: rigURL + "/worker/v1/uploads/u3?exp=1&sig=s", MaxBytes: 1 << 16},
		},
		Expect: provider.WorkerExpect{}, Deadline: r.deadl,
	}
}

// queueTake makes a take and puts its spec into the in-memory queue.
func (r *srig) queueTake(batchID string, timeoutS int) string {
	att := r.take(batchID)
	r.q.Add(batchID, r.spec(att, timeoutS))
	return att
}

// pod inserts a pod of the default batch and walks it to state through legal moves.
func (r *srig) pod(id, name, state string) {
	r.t.Helper()
	r.podOf(id, name, rigBatch, state)
}

func (r *srig) podOf(id, name, batchID, state string) { r.podTry(id, name, batchID, state, 1) }

func (r *srig) podTry(id, name, batchID, state string, try int) {
	r.t.Helper()
	ctx := context.Background()
	err := pods.InsertIntent(ctx, r.st.W, pods.NewPod{ID: id, Name: name, Kind: pods.KindBatch, BatchID: batchID, N: 1, GPUTry: try, GPUType: "NVIDIA GeForce RTX 4090",
		ImageDigest: "ghcr.io/example/mm-worker@sha256:" + strings.Repeat("a", 64), PricePPH: 300000, OverheadReserved: 163750,
		DeadlineAt: r.deadl, PollExp: pods.PollExpFor(r.deadl), At: rigNow})
	if err != nil {
		r.t.Fatal(err)
	}
	path := map[string][]string{"intent": {}, "creating": {"creating"}, "booting": {"creating", "booting"}, "ready": {"creating", "booting", "ready"},
		"busy": {"creating", "booting", "ready", "busy"}, "orphaned": {"creating", "booting", "orphaned"},
		"terminating": {"creating", "booting", "terminating"}}[state]
	from := "intent"
	for _, to := range path {
		if err := pods.Transition(ctx, r.st.W, id, from, to, pods.Move{At: rigNow}); err != nil {
			r.t.Fatal(err)
		}
		from = to
	}
}

func (r *srig) setState(id, to string) {
	r.t.Helper()
	row, err := pods.GetRow(context.Background(), r.st.R, id)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := pods.Transition(context.Background(), r.st.W, id, row.State, to, pods.Move{At: r.clk.Now()}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *srig) link(podID string) string {
	exp := pods.PollExpFor(r.deadl)
	return strings.TrimPrefix(r.sig.URL(rigURL, podID, exp), rigURL)
}

type pollIn map[string]any

func idle() pollIn { return state("idle") }

func state(s string) pollIn {
	return pollIn{"state": s, "agent": map[string]any{"version": "1.0", "build": "b1"}, "boot": nil, "attempt_id": nil, "done": nil}
}

func (p pollIn) with(k string, v any) pollIn {
	c := pollIn{}
	for a, b := range p {
		c[a] = b
	}
	c[k] = v
	return c
}

func done(attempt, status string, execMS int64) map[string]any {
	return map[string]any{"attempt_id": attempt, "status": status, "execution_ms": execMS, "error_code": nil}
}

func (r *srig) poll(target string, body any) *httptest.ResponseRecorder {
	r.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case []byte:
		raw = b
	case string:
		raw = []byte(b)
	default:
		var err error
		if raw, err = json.Marshal(b); err != nil {
			r.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

// noTake: the response carries no take (an absent take is the JSON null).
func noTake(resp pods.PollResponse) bool { return len(resp.Take) == 0 || string(resp.Take) == "null" }

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) pods.PollResponse {
	t.Helper()
	var resp pods.PollResponse
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func problemCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct{ Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("not a problem body: %q", w.Body)
	}
	return b.Code
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
