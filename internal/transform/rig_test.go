package transform_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/transform"
	"github.com/nicolasacchi/metamusic/internal/transform/transformtest"
)

var (
	human  = auth.Principal{Kind: auth.KindHuman, ID: "op", Name: "op", Via: "local"}
	agentA = auth.Principal{Kind: auth.KindAgent, ID: "ag-a", Name: "synthetic-agent-a", Via: "token"}
	agentB = auth.Principal{Kind: auth.KindAgent, ID: "ag-b", Name: "synthetic-agent-b", Via: "token"}

	fxNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
)

var (
	imageOnce sync.Once
	imageDB   []byte
	imageInfo map[string]string
)

// image is a migrated database holding the M1 fixture (five 6-12 s Takes: A, B and C name three of them), built once per process; every rig starts from a copy.
func image(t testing.TB) ([]byte, map[string]string) {
	imageOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-transform-image-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		p := filepath.Join(dir, "i.db")
		st, err := store.Open(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		librarytest.Fixture(t, st, fxNow)
		imageInfo = map[string]string{"A": librarytest.TrackTwinA, "B": librarytest.TrackCitta, "C": librarytest.TrackShort}
		if _, err := st.W.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatal(err)
		}
		st.Close()
		if imageDB, err = os.ReadFile(p); err != nil {
			t.Fatal(err)
		}
	})
	if imageDB == nil {
		t.Fatal("the database image could not be built")
	}
	return imageDB, imageInfo
}

type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *manualClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	t      *testing.T
	st     *store.Store
	data   *os.Root
	dir    string
	lib    *library.Service
	fx     map[string]string // label -> take id
	svc    *transform.Service
	child  *transformtest.FakeChild
	media  *transformtest.FakeMedia
	ingest *transformtest.FakeIngest
	clk    *manualClock
	n      int

	mu      sync.Mutex
	changed []string
}

func (r *rig) changedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.changed...)
}

// newRig builds a service over a copy of the image with the three fakes. useWallClock makes the service read time.Now (for
// testing/synctest bubbles); otherwise a manual clock starts at fxNow.
func newRig(t *testing.T, useWallClock bool, mod ...func(*transform.Options)) *rig {
	t.Helper()
	img, fx := image(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.db")
	if err := os.WriteFile(dbPath, img, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	r := &rig{t: t, st: st, data: root, dir: dataDir, fx: fx, clk: &manualClock{t: fxNow},
		child: &transformtest.FakeChild{}, media: &transformtest.FakeMedia{Sources: map[string]transformtest.FakeSrc{}},
		ingest: &transformtest.FakeIngest{Data: root}}
	now := r.clk.now
	if useWallClock {
		now = time.Now
	}
	r.lib = library.New(st, root, now)
	// the fixture takes: a master file each (empty: FakeMedia stands in for ffmpeg) and the decode shape of its row
	rows, err := st.R.Query(`SELECT master_path, total_samples, channels FROM tracks WHERE import_rule NOT IN ('reference') AND origin = 'import'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p string
		var n int64
		var ch int
		if err := rows.Scan(&p, &n, &ch); err != nil {
			t.Fatal(err)
		}
		r.media.Sources[filepath.Base(p)] = transformtest.FakeSrc{Frames: n, Channels: ch}
		if err := root.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := root.WriteFile(p, nil, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	_ = rows.Close()
	o := transform.Options{Store: st, Library: r.lib, Now: now, NewID: func() string { r.n++; return fmt.Sprintf("tx-%03d", r.n) },
		Config:   testConfig(),
		Pipeline: r.ingest, Media: r.media, Child: r.child, Data: root, DataDir: dataDir, Owner: "runner-a",
		Changed: func(id string) { r.mu.Lock(); r.changed = append(r.changed, id); r.mu.Unlock() }}
	for _, m := range mod {
		m(&o)
	}
	r.svc = transform.New(o)
	return r
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// qspec describes a transform row to insert.
type qspec struct {
	kind      transform.Kind
	principal auth.Principal
	inputs    []string // fixture labels; default A
	created   time.Time
	seed      int64
	maxCPU    int64 // ms; default 60 000
	state     string
	tag       string // makes the spec (and so its hash) unique
	id        string
}

// queue inserts a recipe, a transform row and its inputs the way create does, and returns the transform id.
func (r *rig) queue(q qspec) string {
	r.t.Helper()
	if q.kind == "" {
		q.kind = transform.KindGlacier
	}
	if q.principal.Kind == "" {
		q.principal = human
	}
	if len(q.inputs) == 0 {
		q.inputs = []string{"A"}
	}
	if q.created.IsZero() {
		q.created = fxNow
	}
	if q.maxCPU == 0 {
		q.maxCPU = 60_000
	}
	if q.state == "" {
		q.state = "queued"
	}
	r.n++
	if q.seed == 0 {
		q.seed = int64(r.n) // distinct specs render distinct audio (FakeChild varies with the seed)
	}
	if q.id == "" {
		q.id = fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe10000+r.n)
	}
	if q.tag == "" {
		q.tag = q.id
	}
	var ins []transform.InputRef
	roles := []string{"source", "b"}
	if q.kind == transform.KindMorph || q.kind == transform.KindPolytempo {
		roles = []string{"a", "b"}
	}
	for i, l := range q.inputs {
		id := r.fx[l]
		var msha string
		if err := r.st.R.QueryRow(`SELECT master_sha256 FROM tracks WHERE id = ?`, id).Scan(&msha); err != nil {
			r.t.Fatal(err)
		}
		ins = append(ins, transform.InputRef{Role: roles[i], TrackID: id, MasterSHA256: msha})
	}
	specHash := sha("spec|" + q.tag)
	spec := transform.Spec{ID: "dsp-" + string(q.kind) + "-" + specHash[:20], Seed: q.seed, V: 1, Kind: q.kind, DSPVersion: transform.DSPVersion,
		SampleRate: 48000, Inputs: ins, Params: json.RawMessage(`{"factor":20}`), TargetLUFS: -16}
	canon, err := json.Marshal(spec)
	if err != nil {
		r.t.Fatal(err)
	}
	tx, err := r.st.W.Begin()
	if err != nil {
		r.t.Fatal(err)
	}
	rid, _, err := r.lib.InsertTransformRecipe(context.Background(), tx, library.SpecRecipe{Kind: string(q.kind), Canonical: canon, Hash: sha(string(canon)),
		RequestID: spec.ID, Seed: q.seed}, q.principal.Key())
	if err != nil {
		r.t.Fatal(err)
	}
	pk := "human"
	if q.principal.Kind == auth.KindAgent {
		pk = "agent"
	}
	cols, vals := `id, kind, recipe_id, spec_hash, title, principal_id, principal_kind, principal_name, state, expected_cpu_ms, max_cpu_ms, out_seconds_est, mem_mb_est, created_at`,
		[]any{q.id, string(q.kind), rid, specHash, "Synthetic " + string(q.kind), q.principal.Key(), pk, q.principal.Name, "queued", q.maxCPU / 4, q.maxCPU, 20.0, 64, store.FormatTime(q.created)}
	if _, err := tx.Exec(`INSERT INTO transforms(`+cols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, vals...); err != nil {
		r.t.Fatal(err)
	}
	for i, in := range ins {
		if _, err := tx.Exec(`INSERT INTO transform_inputs(transform_id, ord, role, track_id, master_sha256) VALUES (?,?,?,?,?)`, q.id, i, in.Role, in.TrackID, in.MasterSHA256); err != nil {
			r.t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
	switch q.state {
	case "queued":
	case "running":
		r.exec(`UPDATE transforms SET state='running', attempts=1, started_at=?, owner='runner-x', lease_expires_at=? WHERE id=?`,
			store.FormatTime(r.clk.now()), store.FormatTime(r.clk.now().Add(transform.LeaseTTL)), q.id)
	default:
		r.t.Fatalf("queue: unsupported state %q", q.state)
	}
	return q.id
}

func (r *rig) exec(q string, a ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, a...); err != nil {
		r.t.Fatal(err)
	}
}

// rowState is a transform row as the tests read it.
type rowState struct {
	State, Phase, Owner, ErrCode, ErrText, PCM string
	Attempts                                   int
	CPU                                        sql.NullInt64
	Progress                                   int
	Cancel                                     bool
	Lease                                      string
}

func (r *rig) row(id string) rowState {
	r.t.Helper()
	var s rowState
	var phase, owner, code, text, pcm, lease, cancel sql.NullString
	err := r.st.R.QueryRow(`SELECT state, phase, owner, error_code, error_text, pcm_sha256, attempts, cpu_ms, progress_permille, cancel_requested_at, lease_expires_at
		FROM transforms WHERE id = ?`, id).Scan(&s.State, &phase, &owner, &code, &text, &pcm, &s.Attempts, &s.CPU, &s.Progress, &cancel, &lease)
	if err != nil {
		r.t.Fatal(err)
	}
	s.Phase, s.Owner, s.ErrCode, s.ErrText, s.PCM, s.Lease, s.Cancel = phase.String, owner.String, code.String, text.String, pcm.String, lease.String, cancel.Valid
	return s
}

func (r *rig) count(q string, a ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.R.QueryRow(q, a...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// runOne drives the runner once.
func (r *rig) runOne() bool {
	r.t.Helper()
	ok, err := r.svc.RunOne(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return ok
}

func (r *rig) exists(rel string) bool {
	_, err := r.data.Stat(rel)
	return err == nil
}

// fakeEars records AnalyzeTrack calls and answers a fixed loudness.
type fakeEars struct {
	mu       sync.Mutex
	analyzed []string
	lufs     *float64
	err      error
}

func (e *fakeEars) AnalyzeTrack(_ context.Context, id string, _ ears.AnalyzeOptions) (ears.Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.analyzed = append(e.analyzed, id)
	return ears.Outcome{TrackID: id}, e.err
}

func (e *fakeEars) Perception(context.Context, string) (ears.Perception, error) {
	return ears.Perception{LUFSI: e.lufs}, nil
}

func (e *fakeEars) calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.analyzed...)
}
