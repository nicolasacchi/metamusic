package fallback

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/objstore"
	"github.com/nicolasacchi/metamusic/internal/objstore/objstoretest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

const (
	jobID     = "01900000-0000-7000-8000-00000000a001"
	batchID   = "01900000-0000-7000-8000-00000000b001"
	attemptID = "01900000-0000-7000-8000-00000000c001"
	prefix    = "mm/"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// harness is a database with one job and one attempt (uploads open), the real upload service, an objstoretest bucket and the
// fallback over them.
type harness struct {
	t     *testing.T
	st    *store.Store
	clk   *clocktest.Manual
	svc   *upload.Service
	bkt   *objstoretest.Server
	fb    *Fallback
	data  *os.Root
	exp   time.Time
	wakes atomic.Int32
	logs  *syncBuf
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	h := &harness{t: t, st: st, clk: clocktest.NewManual(t0), bkt: objstoretest.New(t), data: root, exp: t0.Add(3 * time.Hour), logs: &syncBuf{}}
	n := 0
	h.svc = upload.NewService(upload.ServiceOptions{Data: root, Signer: upload.NewSigner([]byte("synthetic-secret-key-for-tests-0001")), Store: st, Clock: h.clk,
		PublicURL: "https://mm.example.com", Wake: func() { h.wakes.Add(1) }, Log: slog.New(slog.DiscardHandler),
		NewID: func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012d", n) }})
	h.exec(`INSERT INTO runner_lease VALUES(1,'o1',?,?,?)`, store.FormatTime(t0), store.FormatTime(t0), store.FormatTime(t0.Add(24*time.Hour)))
	h.seed()
	client, err := objstore.New(h.bkt.Config(), h.bkt.HTTPClient(), time.Now) // the bucket runs on the real clock
	if err != nil {
		t.Fatal(err)
	}
	h.fb, err = New(Options{Store: st, Uploads: h.svc, Objects: client, Prefix: prefix, Clock: h.clk, Log: slog.New(slog.NewJSONHandler(h.logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) exec(q string, args ...any) {
	h.t.Helper()
	if _, err := h.st.W.Exec(q, args...); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
}

func (h *harness) str(q string, args ...any) string {
	h.t.Helper()
	var s sqlNull
	if err := h.st.R.QueryRow(q, args...).Scan(&s); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

func (h *harness) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// seed inserts a batch, a starting job and one submitted attempt with three open uploads.
func (h *harness) seed() {
	h.t.Helper()
	const recipe = "01900000-0000-7000-8000-00000000f001"
	ts := store.FormatTime(t0)
	h.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'yue2','syn-a','{"id":"syn-a","seed":1}','pop','la',1,'full','h1','c1','import',?)`, recipe, ts)
	h.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id)
		VALUES('est1','human:h1','human','yue2','fake','[]','[]',1,'{}',10,1000,500,1500,690000,2,20,0,'[]','hash1',?,?,?,?)`, ts, ts, ts, batchID)
	h.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at) VALUES(?,'est1','human:h1','human','h1','api','yue2','fake',1,NULL,10,1000,500,1500,690000,200,15,2,20,'active',?)`, batchID, ts)
	h.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,
		timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
		VALUES(?,?,0,'generate','yue2','fake',?,'reroll_seed','{}','T',216,96,186,10,20,'queued',?,?,?)`, jobID, batchID, recipe, ts, ts, ts)
	h.exec(`UPDATE jobs SET state='submitting', lease_owner='o1', lease_expires_at=? WHERE id=?`, store.FormatTime(t0.Add(time.Hour)), jobID)
	h.exec(`UPDATE jobs SET state='starting' WHERE id=?`, jobID)
	h.exec(`UPDATE jobs SET state='generating' WHERE id=?`, jobID)
	h.exec(`INSERT INTO job_attempts(id,job_id,n,backend,state,ref,submitted_at,window_ends_at,price_pph_micro_usd,created_at)
		VALUES(?,?,1,'fake','pending',NULL,?,?,690000,?)`, attemptID, jobID, ts, store.FormatTime(h.exp), ts)
	h.exec(`UPDATE job_attempts SET state='submitted', ref='ref-1' WHERE id=?`, attemptID)
	tx, err := h.st.W.Begin()
	if err != nil {
		h.t.Fatal(err)
	}
	err = h.svc.OpenAttempt(context.Background(), tx, upload.JobRef{JobID: jobID, BatchID: batchID}, attemptID,
		map[string]int64{"audio.flac": 1000, "mm-result.json": 65536, "request.json": 4000}, h.exp.Unix(), store.LeaseFence{Owner: "o1", Now: t0})
	if err != nil {
		tx.Rollback()
		h.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) uploadID(artifact string) string {
	return h.str(`SELECT id FROM uploads WHERE attempt_id = ? AND artifact = ?`, attemptID, artifact)
}

// mint mints the fallback URLs of the attempt.
func (h *harness) mint() map[string]string {
	h.t.Helper()
	urls, after, err := h.fb.MintFallback(context.Background(), attemptID, h.exp)
	if err != nil || after != DefaultAfterS || len(urls) != 3 {
		h.t.Fatalf("mint: %v %d %v", err, after, len(urls))
	}
	return urls
}

// workerPut is the worker's PUT of body to a fallback URL.
func (h *harness) workerPut(url string, body []byte) {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.ContentLength = int64(len(body))
	resp, err := h.bkt.HTTPClient().Do(req)
	if err != nil {
		h.t.Fatalf("worker put: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		h.t.Fatalf("worker put: %d", resp.StatusCode)
	}
}

func shaHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// finishWorker puts the artifacts and then the result of a /2 plain render into the bucket, as a worker that switched to the
// fallback would; lie replaces the digest of a named artifact.
func (h *harness) finishWorker(urls map[string]string, bodies map[string][]byte, lie map[string]string) []byte {
	h.t.Helper()
	out := provider.Output{Contract: provider.ContractV2, Kind: provider.KindRender, TakeID: jobID, AttemptID: attemptID, Status: "ok",
		Artifacts: map[string]provider.ArtifactDigest{}, Truncated: map[string]bool{"abc": false, "semantic": false},
		Env: provider.OutputEnv{Engine: "yue2", PkgVersion: "0.1.6", CodeCommit: "0000000000000000000000000000000000000003", Decoder: "m-a-p/YuE2-Vae",
			GPUName: "NVIDIA RTX A5000", CUDA: "12.8", Torch: "2.10.0", WorkerBuild: "test", Python: "3.12", Weights: map[string]string{"a": "b"}}}
	n := json.Number("215.8")
	out.AudioSeconds = &n
	for name, body := range bodies {
		h.workerPut(urls[name], body)
		sum := shaHex(body)
		if l, ok := lie[name]; ok {
			sum = l
		}
		out.Artifacts[name] = provider.ArtifactDigest{SHA256: sum, Bytes: int64(len(body))}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		h.t.Fatal(err)
	}
	h.workerPut(urls["mm-result.json"], raw)
	return raw
}

type sqlNull struct{ String string }

func (s *sqlNull) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		s.String = ""
	case string:
		s.String = x
	case []byte:
		s.String = string(x)
	default:
		s.String = fmt.Sprint(x)
	}
	return nil
}
