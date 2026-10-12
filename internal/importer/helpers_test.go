package importer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// env is a throwaway inbox + DataDir + library.
type env struct {
	t        *testing.T
	inboxDir string
	dataDir  string
	data     *os.Root
	st       *store.Store
	lib      *library.Service
	ing      *fakeIngester
	now      time.Time
}

var parallelized sync.Map

// newEnv builds an isolated environment and lets the test run in parallel
// with the others (every env has its own temp directories and database).
func newEnv(t *testing.T) *env {
	t.Helper()
	if _, again := parallelized.LoadOrStore(t, true); !again {
		t.Parallel()
	}
	root := t.TempDir()
	e := &env{t: t, inboxDir: filepath.Join(root, "inbox"), dataDir: filepath.Join(root, "data"),
		now: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC), ing: &fakeIngester{}}
	for _, d := range []string{e.inboxDir, e.dataDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.OpenRoot(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	e.data = data
	if err := os.WriteFile(filepath.Join(e.dataDir, "metamusic.db"), migratedDB(t), 0o644); err != nil {
		t.Fatal(err)
	}
	e.openStore()
	return e
}

var (
	dbOnce  sync.Once
	dbBytes []byte
)

// migratedDB is a freshly migrated database file, built once per test binary
// (migrating costs ~0.3 s, which adds up over a hundred tests).
func migratedDB(t *testing.T) []byte {
	dbOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-importer-tmpl")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		st, err := store.Open(context.Background(), filepath.Join(dir, "t.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		if dbBytes, err = os.ReadFile(filepath.Join(dir, "t.db")); err != nil {
			t.Fatal(err)
		}
	})
	return dbBytes
}

func (e *env) openStore() {
	e.t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(e.dataDir, "metamusic.db"))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { st.Close() })
	e.st = st
	e.lib = library.New(st, e.data, func() time.Time { return e.now })
}

func (e *env) importer(mod ...func(*Options)) *Importer {
	e.t.Helper()
	ro, err := inbox.Open(e.inboxDir)
	if err != nil {
		e.t.Fatal(err)
	}
	o := Options{Inbox: ro, Now: func() time.Time { return e.now }, Jobs: 2, Principal: "cli"}
	for _, m := range mod {
		m(&o)
	}
	im, err := New(e.st, e.lib, e.data, e.ing, o)
	if err != nil {
		e.t.Fatal(err)
	}
	return im
}

func (e *env) inventory(mod ...func(*Options)) (*Importer, *Inventory) {
	e.t.Helper()
	im := e.importer(mod...)
	inv, err := im.Inventory(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return im, inv
}

// applyAll runs a dry inventory and applies it with the reviewed digest.
func (e *env) applyAll(mod ...func(*Options)) (*Inventory, *RunResult) {
	e.t.Helper()
	im, inv := e.inventory(mod...)
	res, err := im.Apply(context.Background(), inv, ApplyOptions{ExpectDigest: inv.PlanDigest()})
	if err != nil {
		e.t.Fatalf("apply: %v", err)
	}
	return inv, res
}

func (e *env) abs(rel string) string { return filepath.Join(e.inboxDir, filepath.FromSlash(rel)) }

func (e *env) age(rel string, t time.Time) {
	e.t.Helper()
	if err := os.Chtimes(e.abs(rel), t, t); err != nil {
		e.t.Fatal(err)
	}
}

// put writes an inbox file with an old mtime (settled).
func (e *env) put(rel, content string) {
	e.t.Helper()
	p := e.abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.age(rel, e.now.Add(-time.Hour))
}

// flac writes a header-valid FLAC whose audio frames are `frames`.
func (e *env) flac(rel, frames string, comments ...string) {
	e.t.Helper()
	p := e.abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	flactest.WriteFile(e.t, p, flactest.Spec{SampleRate: 48000, Channels: 2, BitsPerSample: 24, TotalSamples: 288000,
		Vendor: "test", Comments: comments, Frames: []byte("frames:" + frames)})
	e.age(rel, e.now.Add(-time.Hour))
}

func (e *env) rm(rel string) {
	e.t.Helper()
	if err := os.Remove(e.abs(rel)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(table string) int {
	e.t.Helper()
	var n int
	if err := e.st.R.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) query(q string, a ...any) *sql.Rows {
	e.t.Helper()
	rows, err := e.st.R.Query(q, a...)
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func (e *env) scalar(q string, a ...any) string {
	e.t.Helper()
	var s sql.NullString
	if err := e.st.R.QueryRow(q, a...).Scan(&s); err != nil && err != sql.ErrNoRows {
		e.t.Fatal(err)
	}
	return s.String
}

var allTables = []string{"import_runs", "recipes", "recipe_sources", "recipe_refs", "tracks", "track_style_terms", "mutations",
	"renditions", "analyses", "artifacts", "scores", "sections", "recipe_drafts", "draft_collections"}

func (e *env) counts() map[string]int {
	m := map[string]int{}
	for _, t := range allTables {
		m[t] = e.count(t)
	}
	return m
}

// req is a JSONL request line in yue2 batch format (python separators).
func req(id string, seed int, extra ...string) string {
	return fmt.Sprintf(`{"cot": "full", "id": %q, "lyrics": "[Verse 1]\nla %s\n[Chorus]\nsing it", "seed": %d, "style": "pop, 120 bpm"%s}`,
		id, id, seed, strings.Join(extra, ""))
}

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

func shaOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func issueCodes(is []Issue) []string {
	var out []string
	for _, i := range is {
		out = append(out, i.Code)
	}
	return out
}

func hasCode(is []Issue, code string) bool {
	for _, i := range is {
		if i.Code == code {
			return true
		}
	}
	return false
}

func audioByPath(r *Report, p string) *AudioReport {
	for i := range r.Audio {
		if r.Audio[i].Path == p {
			return &r.Audio[i]
		}
	}
	return nil
}

func recipeByID(r *Report, id string, seed int64) *RecipeReport {
	for i := range r.Recipes {
		if r.Recipes[i].RequestID == id && r.Recipes[i].Seed == seed {
			return &r.Recipes[i]
		}
	}
	return nil
}

// ---- fake ingester ---------------------------------------------------------

type fakeIngester struct {
	mu        sync.Mutex
	fail      func(ingest.Request) error
	delay     time.Duration
	requests  []ingest.Request
	artifacts map[string][]string // TrackID -> artifact names
	discarded []ingest.Result
	peak      int
	running   int
}

func (f *fakeIngester) Ingest(ctx context.Context, r ingest.Request) (ingest.Result, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.running++
	f.peak = max(f.peak, f.running)
	fail := f.fail
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.running--; f.mu.Unlock() }()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ingest.Result{}, ctx.Err()
		}
	}
	if fail != nil {
		if err := fail(r); err != nil {
			return ingest.Result{}, err
		}
	}
	st, err := r.Source.Stat()
	if err != nil {
		return ingest.Result{}, err
	}
	ra, ok := r.Source.(io.ReaderAt)
	if !ok {
		return ingest.Result{}, fmt.Errorf("source is not an io.ReaderAt")
	}
	info, err := flacmeta.Read(ra, st.Size())
	if err != nil {
		return ingest.Result{}, err
	}
	frames, err := flacmeta.FramesSHA256(ra, st.Size(), info.FramesOffset)
	if err != nil {
		return ingest.Result{}, err
	}
	tail := r.TrackID[len(r.TrackID)-12:]
	master := fmt.Sprintf("audio/masters/%s__%d__%s.flac", r.Slug, r.Seed, tail)
	mp3sha, pksha := shaOf("mp3:"+r.SourceSHA256), shaOf("peaks:"+r.SourceSHA256)
	res := ingest.Result{
		MasterRelPath: master, MasterSHA256: shaOf("master:" + r.SourceSHA256), FramesSHA256: frames,
		AudioMD5: hex.EncodeToString(info.StreamInfo.MD5[:]), MasterBytes: st.Size(), Info: info,
		MP3:         ingest.Rendition{Kind: "mp3_320", RelPath: fmt.Sprintf("derived/%s/%s.mp3", mp3sha[:2], mp3sha), SHA256: mp3sha, Bytes: 100, Params: map[string]any{"encoder": "libmp3lame"}},
		Peaks:       ingest.Rendition{Kind: "peaks", RelPath: fmt.Sprintf("derived/%s/%s.peaks.json", pksha[:2], pksha), SHA256: pksha, Bytes: 50, Params: map[string]any{"format": "mm.peaks/1"}},
		ToolVersion: "ffmpeg fake", Created: []string{master},
	}
	res.Loudness.IntegratedLUFS = -14
	for _, a := range r.Artifacts {
		n, _ := io.Copy(io.Discard, a.File)
		res.Artifacts = append(res.Artifacts, ingest.Rendition{Kind: "artifact", RelPath: "takes/" + r.TrackID + "/" + a.Name, SHA256: a.SHA256, Bytes: n})
		res.Created = append(res.Created, "takes/"+r.TrackID+"/"+a.Name)
	}
	f.mu.Lock()
	if f.artifacts == nil {
		f.artifacts = map[string][]string{}
	}
	for _, a := range r.Artifacts {
		f.artifacts[r.TrackID] = append(f.artifacts[r.TrackID], a.Name)
	}
	f.mu.Unlock()
	return res, nil
}

func (f *fakeIngester) Discard(r ingest.Result) error {
	f.mu.Lock()
	f.discarded = append(f.discarded, r)
	f.mu.Unlock()
	return nil
}

func (f *fakeIngester) requestFor(id string, seed int64) (ingest.Request, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if r.RequestID == id && r.Seed == seed {
			return r, true
		}
	}
	return ingest.Request{}, false
}

// yue2Request renders the request exactly like YuE2 writes request.json
// (asdict order, indent 2, explicit nulls); same recipe as req(id, seed).
func yue2Request(id string, seed int) string {
	return fmt.Sprintf("{\n  \"style\": \"pop, 120 bpm\",\n  \"lyrics\": \"[Verse 1]\\nla %s\\n[Chorus]\\nsing it\",\n  \"cot\": \"full\",\n  \"seed\": %d,\n  \"abc\": null,\n  \"cfg_scale\": null,\n  \"id\": %q\n}\n", id, seed, id)
}

type trackRow struct {
	ID, ReqID, Parent, Root, Rule, Title, Slug, ImportRule string
	Seed                                                   int64
	Gen                                                    int
}

// tracks returns library tracks keyed by "<request id>#<seed>" (last wins).
func (e *env) tracks() map[string]trackRow {
	e.t.Helper()
	rows := e.query(`SELECT t.id, r.request_id, r.seed, coalesce(t.parent_track_id,''), t.root_track_id, t.generation,
		coalesce(json_extract(m.params_json,'$.rule'),''), t.title, t.slug, t.import_rule
		FROM tracks t JOIN recipes r ON r.id=t.recipe_id LEFT JOIN mutations m ON m.child_track_id=t.id ORDER BY t.recorded_at, t.id`)
	defer rows.Close()
	out := map[string]trackRow{}
	for rows.Next() {
		var x trackRow
		if err := rows.Scan(&x.ID, &x.ReqID, &x.Seed, &x.Parent, &x.Root, &x.Gen, &x.Rule, &x.Title, &x.Slug, &x.ImportRule); err != nil {
			e.t.Fatal(err)
		}
		out[fmt.Sprintf("%s#%d", x.ReqID, x.Seed)] = x
	}
	return out
}

// req3 has three lyric sections (verse, chorus, outro) matching abc3.
func req3(id string, seed int) string {
	return fmt.Sprintf(`{"cot": "full", "id": %q, "lyrics": "[Verse 1]\nla\n[Chorus]\nsing\n[Outro]\nfade", "seed": %d, "style": "pop, 120 bpm"}`, id, seed)
}

// abc3 is a timed score: 3 sections x 2 bars, 4/4 at 120 = 2 s per bar.
const abc3 = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|\n% chorus\nCDEF|CDEF|\n% outro\nCDEF|CDEF|]\n"

type draftRow struct {
	ID, ReqID, Status, Origin, Group, Label, Parent, FulfilledBy string
	Seed                                                         int64
}

func (e *env) drafts() map[string]draftRow {
	e.t.Helper()
	rows := e.query(`SELECT d.id, d.request_id, r.seed, d.status, d.origin, coalesce(d.group_key,''), coalesce(d.label,''),
		coalesce(d.parent_track_id,''), coalesce(d.fulfilled_by_track_id,'') FROM recipe_drafts d JOIN recipes r ON r.id=d.source_recipe_id`)
	defer rows.Close()
	out := map[string]draftRow{}
	for rows.Next() {
		var x draftRow
		if err := rows.Scan(&x.ID, &x.ReqID, &x.Seed, &x.Status, &x.Origin, &x.Group, &x.Label, &x.Parent, &x.FulfilledBy); err != nil {
			e.t.Fatal(err)
		}
		out[fmt.Sprintf("%s#%d", x.ReqID, x.Seed)] = x
	}
	return out
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func parseOne(s string) (yue2.Parsed, error) {
	return yue2.ParseRequest([]byte(s), yue2.ParseOptions{})
}
