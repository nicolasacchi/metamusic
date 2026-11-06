package callbacks_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/callbacks"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	human  = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	device = auth.Principal{Kind: auth.KindDevice, ID: "dev-1", Name: "Phone", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	agent  = auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "claude", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	agent2 = auth.Principal{Kind: auth.KindAgent, ID: "tok-2", Name: "other", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
)

// fakeNotifier records messages; nothing leaves the process.
type fakeNotifier struct {
	mu   sync.Mutex
	msgs []notify.Message
	err  error
}

func (f *fakeNotifier) Notify(_ context.Context, m notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, m)
	return f.err
}

type rig struct {
	t    *testing.T
	st   *store.Store
	info lt.M5Info
	svc  *callbacks.Service
	note *fakeNotifier
	now  time.Time
	ids  int
	// the runner lease the take transaction's fence holds
	fence store.LeaseFence
}

// The M5 fixture is built once per test binary and copied: building it dominates a rig's cost.
var (
	templateOnce sync.Once
	templatePath string
	templateInfo lt.M5Info
	templateErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if templatePath != "" {
		_ = os.RemoveAll(filepath.Dir(templatePath))
	}
	os.Exit(code)
}

func template(t *testing.T) (string, lt.M5Info) {
	t.Helper()
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-callbacks-tpl")
		if err != nil {
			templateErr = err
			return
		}
		templatePath = filepath.Join(dir, "tpl.db")
		st, err := store.Open(context.Background(), templatePath)
		if err != nil {
			templateErr = err
			return
		}
		templateInfo = lt.FixtureM5(t, st, lt.FixedNow)
		templateErr = st.Close()
	})
	if templateErr != nil {
		t.Fatal(templateErr)
	}
	return templatePath, templateInfo
}

func newRig(t *testing.T, mod func(*callbacks.Options)) *rig {
	t.Helper()
	src, info := template(t)
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "t.db")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &rig{t: t, st: st, note: &fakeNotifier{}, now: lt.FixedNow.Add(24 * time.Hour)}
	r.info = info
	r.fence = store.LeaseFence{Owner: "runner-1", Now: r.now}
	r.exec(`INSERT INTO runner_lease(id, owner, acquired_at, heartbeat_at, expires_at) VALUES (1, 'runner-1', ?, ?, ?)`,
		store.FormatTime(r.now.Add(-time.Minute)), store.FormatTime(r.now), store.FormatTime(r.now.Add(time.Hour)))
	o := callbacks.Options{Store: st, Now: func() time.Time { return r.now }, NewID: r.newID, Notify: r.note, PublicURL: "https://mm.example.com/", BacklogMax: 6}
	if mod != nil {
		mod(&o)
	}
	r.svc = callbacks.New(o)
	return r
}

func (r *rig) newID() string {
	r.ids++
	return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe10000+r.ids)
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

func (r *rig) comment(key string) string { return r.info.Comments[key] }

func (r *rig) state(commentID string) string {
	return r.str(`SELECT state FROM comments WHERE id = ?`, commentID)
}

// stubPatch inserts a patches row (a stub: S1 owns the real writer) on base that addresses the comments.
func (r *rig) stubPatch(base string, comments ...string) string {
	r.t.Helper()
	id := r.newID()
	r.exec(`INSERT INTO patches(id, base_track_id, base_recipe_hash, operator, ops_json, compiled_json, scope_json, diff_json, rationale, principal_kind, principal_id, principal_name, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, base, "hash", "lyric_edit", "[]", "{}", "{}", "{}", "stub rationale", "agent", agent.Key(), "claude", store.FormatTime(r.now))
	for _, c := range comments {
		r.exec(`INSERT INTO patch_comments(patch_id, comment_id) VALUES (?,?)`, id, c)
	}
	return id
}

// stubJob inserts a job that rendered patch (jobs.patch_id is frozen, so the row is born with it) in a fresh batch and returns the
// take info the ingest bridge would hand to the hook for a take of it.
func (r *rig) stubJob(patch, child string, state string) jobs.TakeInfo {
	r.t.Helper()
	seed := lt.SeedJob(r.t, r.st, r.now, lt.JobSeed{Name: fmt.Sprintf("cb%d", r.ids+1), Principal: agent.Key()})
	r.ids++
	jid := fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe20000+r.ids)
	r.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
			expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at,patch_id)
		SELECT ?, batch_id, 7, kind, engine, backend, recipe_id, source_track_id, 'lyric_edit', mutation_json, 'T', expected_audio_s, expected_s, timeout_s, expected_micro_usd,
		       reserved_micro_usd, 'queued', next_attempt_at, queued_at, created_at, updated_at, ? FROM jobs WHERE id = ?`, jid, patch, seed.JobID)
	r.setJobState(seed.JobID, "cancelled") // the template job of the seed is not part of the scenario
	if state != "" && state != "queued" {
		r.setJobState(jid, state)
	}
	return jobs.TakeInfo{TrackID: child, JobID: jid, BatchID: seed.BatchID, PatchID: &patch, Fence: r.fence}
}

// setJobState walks the job through the only legal path to state (a helper for the notification tests).
func (r *rig) setJobState(id, state string) {
	r.t.Helper()
	switch state {
	case "cancelled":
		r.exec(`UPDATE jobs SET state = 'cancelled', finished_at = ? WHERE id = ?`, store.FormatTime(r.now), id)
	case "running":
		r.exec(`UPDATE jobs SET state = 'submitting', lease_owner = 'runner-1', lease_expires_at = ? WHERE id = ?`, store.FormatTime(r.now.Add(time.Hour)), id)
	default:
		r.t.Fatalf("unsupported job state %q", state)
	}
}

// hook runs AfterTakeTx in a transaction the way the ingest bridge does and commits it.
func (r *rig) hook(ti jobs.TakeInfo) error {
	r.t.Helper()
	tx, err := r.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.svc.AfterTakeTx(context.Background(), tx, ti); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func newZero() *callbacks.Service { return callbacks.New(callbacks.Options{}) }

// stubJobInBatch inserts a second job of the same batch as ti (a sibling render of the patch) in state and returns its id.
func (r *rig) stubJobInBatch(patch string, ti jobs.TakeInfo, state string) string {
	r.t.Helper()
	r.ids++
	jid := fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe30000+r.ids)
	r.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
			expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at,patch_id)
		SELECT ?, batch_id, 8, kind, engine, backend, recipe_id, source_track_id, operator, mutation_json, 'S', expected_audio_s, expected_s, timeout_s, expected_micro_usd,
		       reserved_micro_usd, 'queued', next_attempt_at, queued_at, created_at, updated_at, ? FROM jobs WHERE id = ?`, jid, patch, ti.JobID)
	if state != "queued" {
		r.setJobState(jid, state)
	}
	return jid
}
