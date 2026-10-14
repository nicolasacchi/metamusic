package upload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

const (
	publicURL = "https://mm.example.com"
	jobID     = "01900000-0000-7000-8000-00000000a001"
	batchID   = "01900000-0000-7000-8000-00000000b001"
	attemptID = "01900000-0000-7000-8000-00000000c001"
)

type rig struct {
	t      *testing.T
	st     *store.Store
	clk    *clocktest.Manual
	svc    *Service
	sig    *Signer
	data   *os.Root
	wakes  atomic.Int32
	logs   *syncBuf
	exp    int64
	n      int
	dbPath string
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newRig(t *testing.T) *rig { return newRigWith(t, "starting", "submitted") }

func newRigWith(t *testing.T, jobState, attState string) *rig {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "s.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	r := &rig{t: t, dbPath: dbPath, st: st, clk: clocktest.NewManual(t0), sig: NewSigner([]byte("synthetic-secret-key-for-tests-0001")), data: root, logs: &syncBuf{}}
	r.exp = t0.Add(3 * time.Hour).Unix()
	r.svc = NewService(ServiceOptions{Data: root, Signer: r.sig, Store: st, Clock: r.clk, PublicURL: publicURL,
		Wake: func() { r.wakes.Add(1) }, Log: slog.New(slog.NewJSONHandler(r.logs, nil)),
		NewID: func() string { r.n++; return fmt.Sprintf("01900000-0000-7000-8000-%012d", r.n) }})
	r.exec(`INSERT INTO runner_lease VALUES(1,'o1',?,?,?)`, store.FormatTime(t0), store.FormatTime(t0), store.FormatTime(t0.Add(24*time.Hour)))
	r.seed(jobState, attState)
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
	var s string
	if err := r.st.R.QueryRow(q, args...).Scan(&s); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return s
}

func (r *rig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// seed inserts a batch, a job in jobState and one attempt in attState (with a ref when submitted).
func (r *rig) seed(jobState, attState string) {
	r.t.Helper()
	const recipe = "01900000-0000-7000-8000-00000000f001"
	ts := store.FormatTime(t0)
	r.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'yue2','syn-a','{"id":"syn-a","seed":1}','pop','la',1,'full','h1','c1','import',?)`, recipe, ts)
	r.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id)
		VALUES('est1','human:h1','human','yue2','fake','[]','[]',1,'{}',10,1000,500,1500,690000,2,20,0,'[]','hash1',?,?,?,?)`, ts, ts, ts, batchID)
	r.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at) VALUES(?,'est1','human:h1','human','h1','api','yue2','fake',1,NULL,10,1000,500,1500,690000,200,15,2,20,'active',?)`, batchID, ts)
	r.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,
		timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
		VALUES(?,?,0,'generate','yue2','fake',?,'reroll_seed','{}','T',216,96,186,10,20,'queued',?,?,?)`, jobID, batchID, recipe, ts, ts, ts)
	if jobState != "queued" {
		r.exec(`UPDATE jobs SET state='submitting', lease_owner='o1', lease_expires_at=? WHERE id=?`, store.FormatTime(t0.Add(time.Hour)), jobID)
		r.exec(`UPDATE jobs SET state=? WHERE id=?`, jobState, jobID)
	}
	ref := any(nil)
	if attState == "submitted" {
		ref = "ref-1"
	}
	r.exec(`INSERT INTO job_attempts(id,job_id,n,backend,state,ref,submitted_at,window_ends_at,price_pph_micro_usd,created_at)
		VALUES(?,?,1,'fake','pending',NULL,?,?,690000,?)`, attemptID, jobID, ts, store.FormatTime(time.Unix(r.exp, 0)), ts)
	if attState != "pending" {
		r.exec(`UPDATE job_attempts SET state=?, ref=? WHERE id=?`, attState, ref, attemptID)
	}
	tx, err := r.st.W.Begin()
	if err != nil {
		r.t.Fatal(err)
	}
	err = r.svc.OpenAttempt(context.Background(), tx, JobRef{JobID: jobID, BatchID: batchID}, attemptID,
		map[string]int64{"audio.flac": 1000, "mm-result.json": 500, "request.json": 100}, r.exp, store.LeaseFence{Owner: "o1", Now: t0})
	if err != nil {
		tx.Rollback()
		r.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
}

// target returns the minted signed path+query of an artifact.
func (r *rig) target(artifact string) (id, rawURL string) {
	r.t.Helper()
	ups, _, err := r.svc.Mint(JobRef{JobID: jobID, BatchID: batchID}, attemptID, r.exp)
	if err != nil {
		r.t.Fatal(err)
	}
	u, ok := ups[artifact]
	if !ok {
		r.t.Fatalf("no target for %s", artifact)
	}
	id = r.str(`SELECT id FROM uploads WHERE attempt_id = ? AND artifact = ?`, attemptID, artifact)
	return id, u.URL[len(publicURL):]
}

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(h[:]) + ":"
}

// put sends a PUT through the handler without sockets.
func (r *rig) put(target string, body []byte, mod func(*http.Request)) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Digest", digestOf(body))
	if mod != nil {
		mod(req)
	}
	w := httptest.NewRecorder()
	r.svc.Handler().ServeHTTP(w, req)
	return w
}
