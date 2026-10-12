package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
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
