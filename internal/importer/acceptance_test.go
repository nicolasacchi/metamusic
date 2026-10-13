package importer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func TestLayout_ApplyWithFakeIngester(t *testing.T) {
	e := newEnv(t)
	acceptanceLayout(t, e)
	inv, res := e.applyAll(func(o *Options) { o.MinAge = 2 * time.Minute })
	checkAcceptanceSummary(t, inv.Report.Summary)
	if res.State != "completed" || res.TracksImported != 9 || res.TracksFailed != 0 {
		t.Fatalf("result = %+v", res)
	}
	want := map[string]int{"recipes": 23, "recipe_sources": 28, "tracks": 9, "recipe_drafts": 23, "recipe_refs": 11, "mutations": 2, "scores": 2, "draft_collections": 3}
	for k, v := range want {
		if got := e.count(k); got != v {
			t.Errorf("%s = %d, want %d", k, got, v)
		}
	}
	var backlog, fulfilled, varianti int
	e.st.R.QueryRow(`SELECT count(*) FROM recipe_drafts WHERE status='backlog'`).Scan(&backlog)
	e.st.R.QueryRow(`SELECT count(*) FROM recipe_drafts WHERE status='fulfilled'`).Scan(&fulfilled)
	e.st.R.QueryRow(`SELECT count(*) FROM recipe_drafts WHERE status='backlog' AND origin='varianti_import'`).Scan(&varianti)
	if backlog != 14 || fulfilled != 9 || varianti != 1 {
		t.Errorf("backlog=%d fulfilled=%d varianti=%d", backlog, fulfilled, varianti)
	}
	tr, d := e.tracks(), e.drafts()
	if d["syn-song-01-v2#4001"].Parent != tr["syn-song-01#1001"].ID || tr["syn-song-01#1001"].ID == "" {
		t.Errorf("varianti mother = %q", d["syn-song-01-v2#4001"].Parent)
	}
	// rules of C21
	rules := map[string]string{
		"syn-song-01#1001": "mapping", "syn-song-01#2001": "slug_seed", "syn-song-02#1002": "slug", "syn-song-02-s2002#2002": "slug",
		"syn-song-03#1003": "slug", "syn-song-04#1004": "slug", "syn-song-05#1005": "result_dir", "syn-song-06#1006": "title_slug",
		"syn-song-07#1007": "tag",
	}
	for k, rule := range rules {
		if tr[k].ImportRule != rule {
			t.Errorf("%s rule = %q, want %q", k, tr[k].ImportRule, rule)
		}
	}
	if c, r := tr["syn-song-01#2001"], tr["syn-song-01#1001"]; c.Rule != "same_request_id" || c.Parent != r.ID || c.Root != r.ID || r.Parent != "" {
		t.Errorf("same_request_id edge: %+v / %+v", r, c)
	}
	if c, r := tr["syn-song-02#1002"], tr["syn-song-02-s2002#2002"]; c.Rule != "seed_family" || c.Parent != r.ID || c.Root != r.ID || r.Parent != "" {
		t.Errorf("seed_family edge: %+v / %+v", r, c)
	}
	if tr["syn-song-06#1006"].Title != "Synthetic Six" {
		t.Errorf("title = %q", tr["syn-song-06#1006"].Title)
	}
	// the abc of syn-song-08 is inlined, its source raw still names abc_path
	if abc := e.scalar(`SELECT json_extract(request_json,'$.abc') FROM recipes WHERE request_id='syn-song-08'`); !strings.HasPrefix(abc, "X:1\nT:eight") {
		t.Errorf("abc = %q", abc)
	}
	if raw := e.scalar(`SELECT cast(raw as text) FROM recipe_sources s JOIN recipes r ON r.id=s.recipe_id WHERE r.request_id='syn-song-08'`); !strings.Contains(raw, `"abc_path": "abc/syn-song-08.abc"`) {
		t.Errorf("raw = %q", raw)
	}
	// syn-song-05: artifacts from the result dir, two recipe sources, truncated 0/1, score from result dir
	if names := strings.Join(e.ing.artifacts[tr["syn-song-05#1005"].ID], ","); names != "config.json,plan.json,request.json,result.json,score.abc,semantic.npy" {
		t.Errorf("artifacts = %s", names)
	}
	if got := e.scalar(`SELECT count(*) FROM recipe_sources s JOIN recipes r ON r.id=s.recipe_id WHERE r.request_id='syn-song-05'`); got != "2" {
		t.Errorf("syn-song-05 sources = %s", got)
	}
	// the invalid line is the only error => exit code 1 territory
	if len(inv.Report.Errors) != 1 || inv.Report.Errors[0].Code != "not_object" {
		t.Errorf("errors = %+v", inv.Report.Errors)
	}
	// second run: nothing but the run row
	before := e.counts()
	_, res2 := e.applyAll(func(o *Options) { o.MinAge = 2 * time.Minute })
	after := e.counts()
	for k := range before {
		w := before[k]
		if k == "import_runs" {
			w++
		}
		if after[k] != w {
			t.Errorf("%s: %d -> %d", k, before[k], after[k])
		}
	}
	for _, a := range res2.Report.Audio {
		if a.Status == "new" {
			t.Errorf("run 2 would import %s", a.Path)
		}
	}
	// run 4 of the incremental story: the rsync temp file becomes a second take of syn-song-06
	if err := os.Rename(e.abs("music/flac/.syn_song_06.flac.Xy12Ab"), e.abs("music/flac/syn_song_06.flac")); err != nil {
		t.Fatal(err)
	}
	e.age("music/flac/syn_song_06.flac", e.now.Add(-30*time.Minute))
	before = e.counts()
	e.applyAll(func(o *Options) { o.MinAge = 2 * time.Minute })
	after = e.counts()
	if after["tracks"] != before["tracks"]+1 || after["mutations"] != before["mutations"]+1 || after["recipe_drafts"] != before["recipe_drafts"] {
		t.Errorf("run 4: %v -> %v", before, after)
	}
	if got := e.tracks()["syn-song-06#1006"]; got.ImportRule != "slug" || got.Rule != "same_request_id" || got.Parent == "" || got.Parent == got.ID {
		t.Errorf("syn-song-06 = %+v", got)
	}
}

func inboxFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		fi, _ := d.Info()
		rel, _ := filepath.Rel(root, p)
		out = append(out, rel+"|"+fi.Mode().String()+"|"+fi.ModTime().UTC().Format(time.RFC3339Nano)+"|"+strconv.FormatInt(fi.Size(), 10))
		return nil
	})
	sort.Strings(out)
	return out
}

// dryRunImporter builds an importer the way the CLI does for --dry-run.
func dryRunImporter(t *testing.T, e *env, st *store.Store) *Importer {
	t.Helper()
	ro, err := inbox.Open(e.inboxDir)
	if err != nil {
		t.Fatal(err)
	}
	var lib *library.Service
	if st != nil {
		lib = library.New(st, e.data, func() time.Time { return e.now })
	}
	im, err := New(st, lib, e.data, nil, Options{Inbox: ro, MinAge: 2 * time.Minute, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func TestAcceptance_M1_ImportDryRunInventory(t *testing.T) {
	t.Run("no database", func(t *testing.T) {
		e := &env{t: t, now: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
		root := t.TempDir()
		e.inboxDir, e.dataDir = filepath.Join(root, "inbox"), filepath.Join(root, "data")
		for _, d := range []string{e.inboxDir, e.dataDir} {
			os.MkdirAll(d, 0o755)
		}
		var err error
		if e.data, err = os.OpenRoot(e.dataDir); err != nil {
			t.Fatal(err)
		}
		defer e.data.Close()
		acceptanceLayout(t, e)
		// the library has no database: ErrNoDatabase means an empty library
		if _, err := store.OpenReadOnly(context.Background(), filepath.Join(e.dataDir, "metamusic.db")); !errors.Is(err, store.ErrNoDatabase) {
			t.Fatalf("OpenReadOnly = %v", err)
		}
		before := inboxFiles(t, e.dataDir)
		inbefore := inboxFiles(t, e.inboxDir)
		im := dryRunImporter(t, e, nil)
		inv, err := im.Inventory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		checkAcceptanceSummary(t, inv.Report.Summary)
		files, err := im.WriteDryRun(inv)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(files.Report, "-dry-run.json") || files.MappingTodo != "import/mapping.todo.csv" {
			t.Errorf("files = %+v", files)
		}
		after := inboxFiles(t, e.dataDir)
		for _, p := range []string{"metamusic.db", "-wal", "-shm", "incoming", "audio", "derived", "takes"} {
			for _, f := range after {
				if strings.Contains(f, p) {
					t.Errorf("dry-run created %s", f)
				}
			}
		}
		assertOnlyImportAdded(t, before, after)
		if got := inboxFiles(t, e.inboxDir); strings.Join(got, "\n") != strings.Join(inbefore, "\n") {
			t.Error("inbox changed")
		}
	})

	t.Run("existing database", func(t *testing.T) {
		e := newEnv(t)
		librarytest.Fixture(t, e.st, e.now)
		e.st.Close()
		acceptanceLayout(t, e)
		dbPath := filepath.Join(e.dataDir, "metamusic.db")
		dbBefore, err := os.ReadFile(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		before := inboxFiles(t, e.dataDir)
		st, err := store.OpenReadOnly(context.Background(), dbPath)
		if err != nil {
			t.Fatal(err)
		}
		im := dryRunImporter(t, e, st)
		inv, err := im.Inventory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := im.WriteDryRun(inv); err != nil {
			t.Fatal(err)
		}
		st.Close()
		// the fixture library's recipes are unrelated to the archive
		if inv.Report.Summary.RecipesUnique != 23 || inv.Report.Summary.TracksNew != 9 {
			t.Errorf("summary = %+v", inv.Report.Summary)
		}
		dbAfter, _ := os.ReadFile(dbPath)
		if string(dbAfter) != string(dbBefore) {
			t.Error("database bytes changed")
		}
		assertOnlyImportAdded(t, before, inboxFiles(t, e.dataDir))
	})
}

// assertOnlyImportAdded checks the tree is unchanged outside import/, and that
// import/ gained only a report and mapping.todo.csv.
func assertOnlyImportAdded(t *testing.T, before, after []string) {
	t.Helper()
	in := map[string]bool{}
	for _, b := range before {
		in[b] = true
	}
	var added []string
	for _, a := range after {
		if !in[a] {
			added = append(added, strings.SplitN(a, "|", 2)[0])
		}
	}
	for _, b := range before {
		found := false
		for _, a := range after {
			if a == b {
				found = true
			}
		}
		// import/ itself may change mtime when files are added
		if !found && !strings.HasPrefix(b, "import") {
			t.Errorf("changed or removed: %s", b)
		}
	}
	sort.Strings(added)
	var files []string
	for _, a := range added {
		if a == "import" || a == "import/reports" {
			continue
		}
		files = append(files, a)
		if !strings.HasPrefix(a, "import/") {
			t.Errorf("new file outside import/: %s", a)
		}
	}
	if len(files) != 2 || !strings.HasPrefix(files[0], "import/mapping.todo.csv") && !strings.HasPrefix(files[1], "import/mapping.todo.csv") {
		t.Errorf("new files = %v", files)
	}
}

// ---- real-ingest acceptance (M1-B9): synth.Archive + ingest.Pipeline --------

var (
	tplMu       sync.Mutex
	tplRoots    = map[synth.Profile]string{}
	tplMan      = map[synth.Profile]synth.Manifest{}
	sharedOnce  sync.Once
	sharedRE    *realEnv
	cleanupMu   sync.Mutex
	cleanupList []func()
	tempRoots   []string
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	code := m.Run()
	cleanupMu.Lock()
	for i := len(cleanupList) - 1; i >= 0; i-- {
		cleanupList[i]()
	}
	for _, d := range tempRoots {
		_ = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err == nil && de.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
		_ = os.RemoveAll(d)
	}
	cleanupMu.Unlock()
	os.Exit(code)
}

func mkTemp(pattern string) string {
	d, err := os.MkdirTemp("", pattern)
	if err != nil {
		panic(err)
	}
	cleanupMu.Lock()
	tempRoots = append(tempRoots, d)
	cleanupMu.Unlock()
	return d
}

func addCleanup(f func()) { cleanupMu.Lock(); cleanupList = append(cleanupList, f); cleanupMu.Unlock() }

func profileTemplate(t testing.TB, p synth.Profile) (string, synth.Manifest) {
	t.Helper()
	tplMu.Lock()
	defer tplMu.Unlock()
	if d, ok := tplRoots[p]; ok {
		return d, tplMan[p]
	}
	d := mkTemp("mm-synth-" + string(p) + "-")
	m, err := synth.Archive(context.Background(), testtools.Tools(), d, p, 1)
	if err != nil {
		t.Fatalf("synth.Archive(%s): %v", p, err)
	}
	tplRoots[p], tplMan[p] = d, m
	return d, m
}

func cpTree(t testing.TB, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-a", src+"/.", dst).CombinedOutput(); err != nil {
		t.Fatalf("cp -a: %v %s", err, out)
	}
}

// realEnv is an isolated inbox + DataDir + library with the real ingest pipeline.
type realEnv struct {
	*env
	tools mediatool.Tools
	pipe  *ingest.Pipeline
	man   synth.Manifest

	shInv *Inventory // shared import only
	shRes *RunResult
}

// newRealEnv builds a realEnv under root; reg registers cleanups.
func newRealEnv(t *testing.T, root string, profile synth.Profile, reg func(func())) *realEnv {
	t.Helper()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tpl, man := profileTemplate(t, profile)
	e := &env{t: t, inboxDir: filepath.Join(root, "inbox"), dataDir: filepath.Join(root, "data"), now: time.Now()}
	cpTree(t, tpl, e.inboxDir)
	if err := os.MkdirAll(e.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.OpenRoot(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	reg(func() { _ = data.Close() })
	e.data = data
	if err := os.WriteFile(filepath.Join(e.dataDir, "metamusic.db"), migratedDB(t), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(e.dataDir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	reg(func() { _ = st.Close() })
	e.st = st
	e.lib = library.New(st, data, time.Now)
	tools := testtools.Tools()
	an, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	return &realEnv{env: e, tools: tools, pipe: ingest.New(data, e.dataDir, tools, an, time.Now), man: man}
}

// isolatedRealEnv is a realEnv living in t.TempDir(); the test runs in parallel.
func isolatedRealEnv(t *testing.T, profile synth.Profile) *realEnv {
	t.Helper()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	t.Parallel()
	return newRealEnv(t, t.TempDir(), profile, t.Cleanup)
}

func (re *realEnv) writeMapping() {
	re.t.Helper()
	if err := os.MkdirAll(filepath.Join(re.dataDir, "import"), 0o700); err != nil {
		re.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(re.dataDir, "import", "mapping.csv"), []byte(re.man.Mapping), 0o600); err != nil {
		re.t.Fatal(err)
	}
}

func (re *realEnv) importer(mod ...func(*Options)) *Importer {
	re.t.Helper()
	ro, err := inbox.Open(re.inboxDir)
	if err != nil {
		re.t.Fatal(err)
	}
	o := Options{Inbox: ro, Now: time.Now, Jobs: 2, Principal: "cli", MinAge: 2 * time.Minute}
	for _, m := range mod {
		m(&o)
	}
	im, err := New(re.st, re.lib, re.data, re.pipe, o)
	if err != nil {
		re.t.Fatal(err)
	}
	return im
}

// run is one reviewed dry-run + apply.
func (re *realEnv) run(mod ...func(*Options)) (*Inventory, *RunResult) {
	re.t.Helper()
	im := re.importer(mod...)
	inv, err := im.Inventory(context.Background())
	if err != nil {
		re.t.Fatal(err)
	}
	res, err := im.Apply(context.Background(), inv, ApplyOptions{ExpectDigest: inv.PlanDigest()})
	if err != nil {
		re.t.Fatalf("apply: %v", err)
	}
	return inv, res
}

// sharedImported is one acceptance archive imported once, read by many tests.
func sharedImported(t *testing.T) *realEnv {
	t.Helper()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	sharedOnce.Do(func() {
		re := newRealEnv(t, mkTemp("mm-shared-"), synth.ProfileAcceptance, addCleanup)
		re.writeMapping()
		inv, res := re.run()
		re.shInv, re.shRes = inv, res
		sharedRE = re
	})
	if sharedRE == nil {
		t.Fatal("shared import failed in an earlier test")
	}
	return sharedRE
}

func (re *realEnv) trackID(key string) string {
	re.t.Helper()
	x, ok := re.tracks()[key]
	if !ok {
		re.t.Fatalf("no track %s", key)
	}
	return x.ID
}

func fileSHA(t testing.TB, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func flacInfo(t testing.TB, p string) (flacmeta.Info, string) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	frames, err := flacmeta.FramesSHA256(f, st.Size(), info.FramesOffset)
	if err != nil {
		t.Fatal(err)
	}
	return info, frames
}

func TestAcceptance_M1_ImportAllSongs(t *testing.T) {
	t.Parallel()
	re := sharedImported(t)
	if re.shRes.State != "completed" || re.shRes.TracksImported != 9 || re.shRes.TracksFailed != 0 {
		t.Fatalf("result = %+v errors=%+v takes=%+v", re.shRes, re.shRes.Report.Errors, re.shRes.Report.Result)
	}
	checkAcceptanceSummary(t, re.shInv.Report.Summary)
	want := map[string]int{"recipes": 23, "recipe_sources": 28, "tracks": 9, "recipe_drafts": 23, "recipe_refs": 11, "mutations": 2,
		"scores": 2, "draft_collections": 3, "renditions": 18, "analyses": 18}
	for k, v := range want {
		if got := re.count(k); got != v {
			t.Errorf("%s = %d, want %d", k, got, v)
		}
	}
	// every track: non-empty lyrics and style, mode 0444 master, mp3 + peaks, loudness + spectrum, tag seed == recipe seed
	rows := re.query(`SELECT t.id, r.request_id, r.seed, r.lyrics, r.style, t.master_path, t.duration_s, t.import_rule FROM tracks t JOIN recipes r ON r.id=t.recipe_id`)
	type tr struct {
		id, req, lyrics, style, master, rule string
		seed                                 int64
		dur                                  float64
	}
	var all []tr
	for rows.Next() {
		var x tr
		if err := rows.Scan(&x.id, &x.req, &x.seed, &x.lyrics, &x.style, &x.master, &x.dur, &x.rule); err != nil {
			t.Fatal(err)
		}
		all = append(all, x)
	}
	rows.Close()
	specs := map[string]synth.AudioSpec{}
	for _, a := range re.man.Audio {
		if !strings.Contains(a.Path, "/.") && a.RequestID != "" {
			specs[fmt.Sprintf("%s#%d", a.RequestID, a.Seed)] = a
		}
	}
	for _, x := range all {
		key := fmt.Sprintf("%s#%d", x.req, x.seed)
		if x.lyrics == "" || x.style == "" {
			t.Errorf("%s: empty lyrics/style", key)
		}
		st, err := os.Stat(filepath.Join(re.dataDir, x.master))
		if err != nil || st.Mode().Perm() != 0o444 {
			t.Errorf("%s: master %v %v", key, st, err)
		}
		info, _ := flacInfo(t, filepath.Join(re.dataDir, x.master))
		var seedTag string
		for _, c := range info.Comments {
			if v, ok := strings.CutPrefix(c, "MM_SEED="); ok {
				seedTag = v
			}
		}
		if seedTag != fmt.Sprint(x.seed) {
			t.Errorf("%s: MM_SEED tag = %q", key, seedTag)
		}
		if sp, ok := specs[key]; !ok {
			t.Errorf("%s: no synth spec", key)
		} else if math.Abs(x.dur-sp.Seconds) > 0.01 {
			t.Errorf("%s: duration %.4f, want %.4f", key, x.dur, sp.Seconds)
		}
		for _, kind := range []string{"mp3_320", "peaks"} {
			if n := re.scalar(`SELECT count(*) FROM renditions WHERE track_id=? AND kind=?`, x.id, kind); n != "1" {
				t.Errorf("%s: %s renditions = %s", key, kind, n)
			}
		}
		for _, an := range []string{"loudness", "spectrum"} {
			if n := re.scalar(`SELECT count(*) FROM analyses WHERE track_id=? AND analyzer=?`, x.id, an); n != "1" {
				t.Errorf("%s: %s analyses = %s", key, an, n)
			}
		}
	}
	// syn-song-02 is band-limited to 8 kHz
	bw := re.scalar(`SELECT json_extract(a.metrics_json,'$.bandwidth_hz') FROM analyses a JOIN tracks t ON t.id=a.track_id JOIN recipes r ON r.id=t.recipe_id
		WHERE a.analyzer='spectrum' AND r.request_id='syn-song-02' AND r.seed=1002`)
	if f, _ := strconv.ParseFloat(bw, 64); math.Abs(f-8000) > 200 {
		t.Errorf("syn-song-02 bandwidth = %q", bw)
	}
	tr2, d := re.tracks(), re.drafts()
	// sibling edges and both timed scores
	if c, r := tr2["syn-song-01#2001"], tr2["syn-song-01#1001"]; c.Rule != "same_request_id" || c.Parent != r.ID || c.Root != r.ID {
		t.Errorf("syn-song-01 edge: %+v / %+v", r, c)
	}
	sf := 0
	for _, k := range []string{"syn-song-02#1002", "syn-song-02-s2002#2002"} {
		if tr2[k].Rule == "seed_family" && tr2[k].Parent != "" {
			sf++
		}
	}
	if sf != 1 {
		t.Errorf("seed_family edges = %d", sf)
	}
	timed := re.scalar(`SELECT group_concat(r.request_id) FROM scores s JOIN tracks t ON t.id=s.track_id JOIN recipes r ON r.id=t.recipe_id
		WHERE s.aligned=1 AND s.seconds_per_bar IS NOT NULL ORDER BY r.request_id`)
	if timed != "syn-song-03,syn-song-05" && timed != "syn-song-05,syn-song-03" {
		t.Errorf("timed scores = %q", timed)
	}
	// syn-song-04 (three copies) is ingested once
	if n := re.scalar(`SELECT count(*) FROM tracks t JOIN recipes r ON r.id=t.recipe_id WHERE r.request_id='syn-song-04'`); n != "1" {
		t.Errorf("syn-song-04 tracks = %s", n)
	}
	// syn-song-05 came from the result dir and has two recipe sources
	if tr2["syn-song-05#1005"].ImportRule != "result_dir" {
		t.Errorf("syn-song-05 rule = %q", tr2["syn-song-05#1005"].ImportRule)
	}
	if n := re.scalar(`SELECT count(*) FROM recipe_sources s JOIN recipes r ON r.id=s.recipe_id WHERE r.request_id='syn-song-05'`); n != "2" {
		t.Errorf("syn-song-05 sources = %s", n)
	}
	if n := re.scalar(`SELECT count(*) FROM artifacts WHERE track_id=?`, tr2["syn-song-05#1005"].ID); n != "6" {
		t.Errorf("syn-song-05 artifacts = %s", n)
	}
	// syn-song-08: abc inlined, raw still names abc_path
	abcFile, err := os.ReadFile(filepath.Join(re.inboxDir, "music/abc/syn-song-08.abc"))
	if err != nil {
		t.Fatal(err)
	}
	if abc := re.scalar(`SELECT json_extract(request_json,'$.abc') FROM recipes WHERE request_id='syn-song-08'`); abc != string(abcFile) {
		t.Errorf("abc = %q, want %q", abc, abcFile)
	}
	if raw := re.scalar(`SELECT cast(raw as text) FROM recipe_sources s JOIN recipes r ON r.id=s.recipe_id WHERE r.request_id='syn-song-08'`); !strings.Contains(raw, `"abc_path": "abc/syn-song-08.abc"`) {
		t.Errorf("raw = %q", raw)
	}
	// the varianti draft's mother is the root of the syn-song-01 lineage
	if m, r := d["syn-song-01-v2#4001"].Parent, tr2["syn-song-01#1001"].Root; m == "" || m != r {
		t.Errorf("varianti mother = %q, root = %q", m, r)
	}
	// reference metadata: synb_002_v1 has one (human view) and is in no collection; synb_003_v2 (seed mismatch) has none
	dd, err := re.lib.Draft(context.Background(), d["synb_002_v1#3021"].ID, true)
	if err != nil || !dd.HasReference || dd.Reference == nil || len(dd.Collections) != 0 {
		t.Errorf("synb_002_v1: %+v %v", dd, err)
	}
	dd, err = re.lib.Draft(context.Background(), d["synb_003_v2#3032"].ID, true)
	if err != nil || dd.HasReference {
		t.Errorf("synb_003_v2: %+v %v", dd, err)
	}
	// every dependent file exists and is not an orphan
	if n := len(re.shRes.Report.LibraryOrphans); n != 0 {
		t.Errorf("library orphans: %v", re.shRes.Report.LibraryOrphans)
	}
}

func TestAcceptance_M1_FrozenMasterInvariants(t *testing.T) {
	t.Parallel()
	re := sharedImported(t)
	// frames and STREAMINFO MD5 of every inbox FLAC, to find each master's source
	srcFrames, srcMD5 := map[string]bool{}, map[string]string{}
	_ = filepath.WalkDir(re.inboxDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && strings.EqualFold(filepath.Ext(p), ".flac") {
			info, frames := flacInfo(t, p)
			srcFrames[frames] = true
			srcMD5[frames] = fmt.Sprintf("%x", info.StreamInfo.MD5)
		}
		return nil
	})
	rows := re.query(`SELECT id, master_path, master_sha256, frames_sha256, audio_md5 FROM tracks`)
	type tk struct{ id, path, sha, frames, md5 string }
	var all []tk
	for rows.Next() {
		var x tk
		if err := rows.Scan(&x.id, &x.path, &x.sha, &x.frames, &x.md5); err != nil {
			t.Fatal(err)
		}
		all = append(all, x)
	}
	rows.Close()
	if len(all) != 9 {
		t.Fatalf("tracks = %d", len(all))
	}
	for _, x := range all {
		p := filepath.Join(re.dataDir, x.path)
		info, frames := flacInfo(t, p)
		if !srcFrames[frames] || frames != x.frames {
			t.Errorf("%s: frames sha does not match any source", x.id)
		}
		if got := fmt.Sprintf("%x", info.StreamInfo.MD5); got != srcMD5[frames] || got != x.md5 {
			t.Errorf("%s: md5 %s vs source %s", x.id, got, srcMD5[frames])
		}
		if n := info.Count(flacmeta.BlockVorbisComment); n != 1 {
			t.Errorf("%s: %d VORBIS_COMMENT blocks", x.id, n)
		}
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0o444 {
			t.Errorf("%s: mode %v", x.id, st.Mode())
		}
		if got := fileSHA(t, p); got != x.sha {
			t.Errorf("%s: file sha %s != /media address %s", x.id, got, x.sha)
		}
	}
	// a title edit through the database never touches the file
	x := all[0]
	p := filepath.Join(re.dataDir, x.path)
	before, _ := os.Stat(p)
	if _, err := re.st.W.Exec(`UPDATE tracks SET title=? WHERE id=?`, "Renamed In Database", x.id); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if fileSHA(t, p) != x.sha || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		t.Error("master changed after a title update")
	}
	td, err := re.lib.Track(context.Background(), x.id)
	if err != nil || td.Title != "Renamed In Database" {
		t.Errorf("get_track title = %q (%v)", td.Title, err)
	}
	// restore, so that other readers of the shared import keep seeing generated titles
	if _, err := re.st.W.Exec(`UPDATE tracks SET title=(SELECT title FROM tracks WHERE id=?) WHERE id=?`, x.id, x.id); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptance_M1_LoudnessSpreadReported(t *testing.T) {
	t.Parallel()
	re := sharedImported(t)
	st, err := re.lib.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l := st.Loudness
	if l.N != 9 || l.SpreadLU == nil || math.Abs(*l.SpreadLU-10.0) > 0.2 || !l.ListenFlacRecommended {
		t.Errorf("loudness = %+v spread=%v", l, l.SpreadLU)
	}
}

func TestAcceptance_M1_BacklogDrafts(t *testing.T) {
	t.Parallel()
	re := sharedImported(t)
	page, err := re.lib.ListDrafts(context.Background(), library.DraftParams{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 14 || page.GroupsTotal != 6 {
		t.Errorf("drafts total=%d groups=%d", page.Total, page.GroupsTotal)
	}
	page, err = re.lib.ListDrafts(context.Background(), library.DraftParams{Collection: "pilot-synthetic", Limit: 200})
	if err != nil || page.Total != 3 {
		t.Errorf("collection filter total=%d err=%v", page.Total, err)
	}
}

// recipeBearing are the inbox paths run 1 of the incremental story sees.
var recipeBearing = []string{
	"music/songs.jsonl", "music/requests", "music/abc", "music/backlog-variants.jsonl", "music/pilot.jsonl",
	"music/pilot.json", "music/index.csv", "music/playlist.json", "music/review.json", "varianti",
}

// stashAudio moves everything except the recipe-bearing files out of the inbox
// (rename keeps mtimes) and returns a function that moves it all back.
func (re *realEnv) stashAudio() (restore func()) {
	re.t.Helper()
	stash := filepath.Join(filepath.Dir(re.inboxDir), "stash")
	keep := func(rel string) bool {
		for _, k := range recipeBearing {
			if rel == k || strings.HasPrefix(rel, k+"/") {
				return true
			}
		}
		return false
	}
	var moved []string
	_ = filepath.WalkDir(re.inboxDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort walk
		}
		rel, _ := filepath.Rel(re.inboxDir, p)
		rel = filepath.ToSlash(rel)
		if keep(rel) {
			return nil
		}
		dst := filepath.Join(stash, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			re.t.Fatal(err)
		}
		if err := os.Rename(p, dst); err != nil {
			re.t.Fatal(err)
		}
		moved = append(moved, rel)
		return nil
	})
	return func() {
		for _, rel := range moved {
			dst := re.abs(rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				re.t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(stash, filepath.FromSlash(rel)), dst); err != nil {
				re.t.Fatal(err)
			}
		}
	}
}

func TestAcceptance_M1_ImportIdempotentAndIncremental(t *testing.T) {
	re := isolatedRealEnv(t, synth.ProfileAcceptance)
	restore := re.stashAudio()

	// run 1: recipes only
	inv1, res1 := re.run()
	c1 := re.counts()
	if c1["recipes"] != 23 || c1["tracks"] != 0 || c1["recipe_drafts"] != 23 || c1["recipe_sources"] != 27 {
		t.Fatalf("run 1 counts = %v", c1)
	}
	var backlog int
	re.st.R.QueryRow(`SELECT count(*) FROM recipe_drafts WHERE status='backlog'`).Scan(&backlog)
	if backlog != 23 || res1.TracksImported != 0 || !hasCode(inv1.Report.Warnings, "mother_not_found") {
		t.Errorf("run 1: backlog=%d res=%+v warnings=%v", backlog, res1, issueCodes(inv1.Report.Warnings))
	}

	// run 2: the audio and the result dir arrive
	restore()
	re.writeMapping()
	inv2, res2 := re.run()
	c2 := re.counts()
	if res2.TracksImported != 9 || c2["tracks"] != 9 || c2["recipes"] != 23 || c2["recipe_drafts"] != 23 || c2["recipe_sources"] != 28 {
		t.Fatalf("run 2: res=%+v counts=%v", res2, c2)
	}
	var fulfilled int
	re.st.R.QueryRow(`SELECT count(*) FROM recipe_drafts WHERE status='fulfilled'`).Scan(&fulfilled)
	if fulfilled != 9 {
		t.Errorf("run 2 fulfilled = %d", fulfilled)
	}
	if n := re.scalar(`SELECT count(*) FROM recipe_sources WHERE source_path LIKE '%results/syn-song-05/request.json'`); n != "1" {
		t.Errorf("result-dir request.json sources = %s", n)
	}
	tr, d := re.tracks(), re.drafts()
	if m := d["syn-song-01-v2#4001"].Parent; m == "" || m != tr["syn-song-01#1001"].Root {
		t.Errorf("varianti mother after run 2 = %q", m)
	}
	_ = inv2

	// run 3: nothing new
	inv3, _ := re.run()
	c3 := re.counts()
	for k, v := range c2 {
		w := v
		if k == "import_runs" {
			w++
		}
		if c3[k] != w {
			t.Errorf("run 3: %s %d -> %d", k, v, c3[k])
		}
	}
	s3 := inv3.Report.Summary
	if s3.TracksNew != 0 || s3.DraftsNew != 0 || s3.RecipesAlreadyImported != 23 || s3.AudioAlreadyImported == 0 {
		t.Errorf("run 3 summary = %+v", s3)
	}
	for _, a := range inv3.Report.Audio {
		if a.Status == "new" {
			t.Errorf("run 3 would import %s", a.Path)
		}
	}
	for _, r := range inv3.Report.Recipes {
		if r.Placement != "already_imported" && r.Placement != "draft_backlog" && r.Placement != "draft_varianti" {
			t.Errorf("run 3 recipe %s placement %q", r.RequestID, r.Placement)
		}
	}

	// run 4: the rsync temp file becomes a second take of syn-song-06
	if err := os.Rename(re.abs("music/flac/.syn_song_06.flac.Xy12Ab"), re.abs("music/flac/syn_song_06.flac")); err != nil {
		t.Fatal(err)
	}
	re.age("music/flac/syn_song_06.flac", time.Now().Add(-30*time.Minute))
	root06 := re.trackID("syn-song-06#1006")
	re.run()
	c4 := re.counts()
	if c4["tracks"] != c3["tracks"]+1 || c4["mutations"] != c3["mutations"]+1 || c4["recipe_drafts"] != c3["recipe_drafts"] {
		t.Errorf("run 4: %v -> %v", c3, c4)
	}
	got := re.scalar(`SELECT t.import_rule||'|'||coalesce(m.params_json,'')||'|'||(t.parent_track_id=?) FROM tracks t JOIN recipes r ON r.id=t.recipe_id
		LEFT JOIN mutations m ON m.child_track_id=t.id WHERE r.request_id='syn-song-06' AND t.import_rule='slug'`, root06)
	if !strings.HasPrefix(got, "slug|") || !strings.Contains(got, "same_request_id") || !strings.HasSuffix(got, "|1") {
		t.Errorf("syn-song-06 second take = %q", got)
	}
}

// libraryDump is the normalised content of a library: independent of generated ids and timestamps.
func libraryDump(t *testing.T, re *realEnv) []string {
	t.Helper()
	var out []string
	add := func(prefix, q string) {
		rows := re.query(q)
		defer rows.Close()
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(cols))
			for i, v := range vals {
				parts[i] = v.String
			}
			out = append(out, prefix+" "+strings.Join(parts, "|"))
		}
	}
	add("recipe", `SELECT hash FROM recipes`)
	add("source", `SELECT r.hash, s.source_path, s.line_no, s.raw_sha256 FROM recipe_sources s JOIN recipes r ON r.id=s.recipe_id`)
	add("draft", `SELECT r.hash, d.request_id, d.status, d.origin, coalesce(d.group_key,''), coalesce(d.label,''),
		coalesce((SELECT group_concat(name) FROM (SELECT name FROM draft_collections c WHERE c.draft_id=d.id ORDER BY name)),''),
		coalesce((SELECT frames_sha256 FROM tracks m WHERE m.id=d.parent_track_id),'')
		FROM recipe_drafts d JOIN recipes r ON r.id=d.source_recipe_id`)
	add("track", `SELECT t.frames_sha256||':'||t.total_samples, r.hash, (SELECT p.frames_sha256 FROM tracks p WHERE p.id=t.root_track_id), t.generation, t.import_rule,
		t.title, coalesce(t.truncated_abc,''), coalesce(t.truncated_semantic,'') FROM tracks t JOIN recipes r ON r.id=t.recipe_id`)
	add("ref", `SELECT r.hash, f.source, f.ref_json FROM recipe_refs f JOIN recipes r ON r.id=f.recipe_id`)
	sort.Strings(out)
	return out
}

func TestAcceptance_M1_AllAtOnceEqualsIncremental(t *testing.T) {
	a := isolatedRealEnv(t, synth.ProfileAcceptance)
	b := newRealEnv(t, t.TempDir(), synth.ProfileAcceptance, t.Cleanup)
	a.writeMapping()
	a.run()

	restore := b.stashAudio()
	b.run()
	restore()
	b.writeMapping()
	b.run()

	da, db := libraryDump(t, a), libraryDump(t, b)
	if len(da) != 94 {
		t.Fatalf("dump has %d lines, want 94", len(da))
	}
	if d := cmp.Diff(da, db); d != "" {
		t.Errorf("all-at-once vs incremental (-all +incremental):\n%s", d)
	}
}

type inboxStat struct {
	Size int64
	Mode fs.FileMode
	MTim time.Time
	SHA  string
}

func snapshotInbox(t *testing.T, root string) map[string]inboxStat {
	t.Helper()
	out := map[string]inboxStat{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		x := inboxStat{Size: st.Size(), Mode: st.Mode(), MTim: st.ModTime()}
		if st.Mode().IsRegular() {
			x.SHA = fileSHA(t, p)
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = x
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAcceptance_M1_InboxNeverModified(t *testing.T) {
	re := isolatedRealEnv(t, synth.ProfileAcceptance)
	re.writeMapping()
	// read-only throughout; restore before the temp dir is removed
	var dirs []string
	_ = filepath.WalkDir(re.inboxDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return nil //nolint:nilerr // best-effort walk
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		} else if d.Type().IsRegular() {
			_ = os.Chmod(p, 0o444)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Chmod(dirs[i], 0o555)
	}
	t.Cleanup(func() {
		for _, d := range dirs {
			_ = os.Chmod(d, 0o755)
		}
	})
	before := snapshotInbox(t, re.inboxDir)
	im := re.importer()
	inv, err := im.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := im.WriteDryRun(inv); err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(before, snapshotInbox(t, re.inboxDir)); d != "" {
		t.Fatalf("dry-run changed the inbox:\n%s", d)
	}
	if _, err := im.Apply(context.Background(), inv, ApplyOptions{ExpectDigest: inv.PlanDigest()}); err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(before, snapshotInbox(t, re.inboxDir)); d != "" {
		t.Fatalf("apply changed the inbox:\n%s", d)
	}
	if re.count("tracks") != 9 {
		t.Errorf("tracks = %d", re.count("tracks"))
	}
}
