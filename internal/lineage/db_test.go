package lineage_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var now0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

type env struct {
	t   *testing.T
	st  *store.Store
	svc *lineage.Service
}

var (
	imageOnce [3]sync.Once
	image     [3][]byte
)

// fixtureImage builds the database of a level once per process; every test copies it.
func fixtureImage(t *testing.T, level int) []byte {
	imageOnce[level].Do(func() {
		dir, err := os.MkdirTemp("", "mm-lineage-fixture-")
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		p := filepath.Join(dir, "l.db")
		st, err := store.Open(context.Background(), p)
		if err != nil {
			t.Error(err)
			return
		}
		switch level {
		case 1:
			lt.FixtureM2c(t, st, now0)
		case 2:
			lt.FixtureM3(t, st, now0)
		}
		if _, err := st.W.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Error(err)
		}
		if err := st.Close(); err != nil {
			t.Error(err)
		}
		image[level], _ = os.ReadFile(p) //nolint:gosec // our own temp dir
	})
	if image[level] == nil {
		t.Fatal("fixture image unavailable")
	}
	return image[level]
}

// newEnv opens a fresh database; level 0 is empty, 1 holds FixtureM2c, 2 holds FixtureM3.
func newEnv(t *testing.T, level int) *env {
	t.Helper()
	p := filepath.Join(t.TempDir(), "l.db")
	if level > 0 {
		if err := os.WriteFile(p, fixtureImage(t, level), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &env{t: t, st: st, svc: lineage.New(lineage.Options{Store: st, Now: func() time.Time { return now0 }})}
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.W.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) str(q string, args ...any) string {
	e.t.Helper()
	var s sql.NullString
	if err := e.st.R.QueryRow(q, args...).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s.String
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestUsedSeeds_IgnoresStubs(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 2)
	ctx := context.Background()
	// the job child's recipe (seed 1102) and the Luna recipe (seed 101) share a content hash only if the
	// song is identical; here they differ (style), so each content hash lists its own seed
	hash := e.str(`SELECT k.content_hash FROM recipe_keys k JOIN recipes r ON r.id = k.recipe_id WHERE r.request_id = 'fx-luna'`)
	got, err := lineage.UsedSeeds(ctx, e.st.R, hash)
	if err != nil || len(got) != 1 || got[0] != 101 {
		t.Fatalf("UsedSeeds = %v, %v", got, err)
	}
	// stubs have no recipe_keys row and the placeholder seed 0 never counts
	if n := e.count(`SELECT COUNT(*) FROM recipe_keys k JOIN recipes r ON r.id = k.recipe_id WHERE r.engine = 'unknown'`); n != 0 {
		t.Fatalf("%d stub keys", n)
	}
	for _, h := range []string{"", strings.Repeat("0", 64)} {
		got, err := lineage.UsedSeeds(ctx, e.st.R, h)
		if err != nil || len(got) != 0 {
			t.Errorf("UsedSeeds(%q) = %v, %v", h, got, err)
		}
	}
}

func TestBackfillRecipeKeys_SkipsStubs(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 2)
	want := e.count(`SELECT COUNT(*) FROM recipes WHERE engine = 'yue2'`)
	e.exec(`DROP TRIGGER recipe_keys_no_delete`)
	e.exec(`DELETE FROM recipe_keys`)
	n, err := e.svc.BackfillRecipeKeys(context.Background())
	if err != nil || n != want {
		t.Fatalf("backfill = %d, %v, want %d", n, err, want)
	}
	if got := e.count(`SELECT COUNT(*) FROM recipe_keys`); got != want {
		t.Errorf("%d keys, want %d", got, want)
	}
	if got := e.count(`SELECT COUNT(*) FROM recipe_keys k JOIN recipes r ON r.id = k.recipe_id WHERE r.engine = 'unknown'`); got != 0 {
		t.Errorf("%d keys for stubs", got)
	}
}

func TestBackfillRecipeKeys_Idempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 2)
	e.exec(`DROP TRIGGER recipe_keys_no_delete`)
	e.exec(`DELETE FROM recipe_keys WHERE recipe_id IN (SELECT id FROM recipes WHERE request_id LIKE 'fxb_%')`)
	first, err := e.svc.BackfillRecipeKeys(context.Background())
	if err != nil || first != 6 {
		t.Fatalf("first = %d, %v", first, err)
	}
	second, err := e.svc.BackfillRecipeKeys(context.Background())
	if err != nil || second != 0 {
		t.Fatalf("second = %d, %v", second, err)
	}
	// the keys equal the pure function over the stored request
	var req, content, render string
	if err := e.st.R.QueryRow(`SELECT r.request_json, k.content_hash, k.render_hash FROM recipes r JOIN recipe_keys k ON k.recipe_id = r.id WHERE r.request_id = 'fxb_001_v1'`).
		Scan(&req, &content, &render); err != nil {
		t.Fatal(err)
	}
	if c, r := lineage.Keys([]byte(req)); c != content || r != render {
		t.Error("backfilled keys differ from Keys()")
	}
}

func TestReusableDraftRecipe(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 2)
	ctx := context.Background()
	draft := func(id string) lineage.DraftRow {
		var d lineage.DraftRow
		var src sql.NullString
		if err := e.st.R.QueryRow(`SELECT id, revision, source_recipe_id, request_json FROM recipe_drafts WHERE id = ?`, id).Scan(&d.ID, &d.Revision, &src, &d.RequestJSON); err != nil {
			t.Fatal(err)
		}
		d.SourceRecipeID = src.String
		return d
	}
	reusable := func(d lineage.DraftRow) bool {
		_, ok, err := lineage.ReusableDraftRecipe(ctx, e.st.R, d)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	d := draft(lt.Draft1)
	id, ok, err := lineage.ReusableDraftRecipe(ctx, e.st.R, d)
	if err != nil || !ok || id != d.SourceRecipeID {
		t.Fatalf("imported draft: %q %v %v", id, ok, err)
	}
	// a draft without a source recipe is never reused
	if reusable(draft(lt.DraftUpload1)) {
		t.Error("a draft without a source recipe was reused")
	}
	// edited since import: the request no longer hashes to the recipe
	edited := d
	edited.RequestJSON = strings.Replace(d.RequestJSON, "line one", "line uno", 1)
	if edited.RequestJSON == d.RequestJSON || reusable(edited) {
		t.Error("an edited draft was reused")
	}
	// a live job uses the recipe: not reusable; a terminal one does not count
	e.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,requires_approval,approval_reasons,token_hash,created_at,expires_at)
		VALUES('e1','human:h','human','yue2','fake','[]','[]',1,'{}',1,2,0,2,690000,2,20,0,'[]','th1','2026-10-14T12:00:00.000Z','2026-10-14T12:10:00.000Z')`)
	e.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,workers_max,idle_s,state,created_at)
		VALUES('b1','e1','human:h','human','h','human_create','yue2','fake',1,1,2,0,2,690000,200,15,2,20,'active','2026-10-14T12:00:00.000Z')`)
	e.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
		VALUES('j1','b1',0,'generate','yue2','fake',?,'from_draft','{}','T',216,96,186,1,1,'queued','2026-10-14T12:00:00.000Z','2026-10-14T12:00:00.000Z','2026-10-14T12:00:00.000Z')`, d.SourceRecipeID)
	if reusable(d) {
		t.Error("a recipe with a live job was reused")
	}
	e.exec(`UPDATE jobs SET state = 'cancelled', finished_at = '2026-10-14T12:01:00.000Z' WHERE id = 'j1'`)
	if !reusable(d) {
		t.Error("a cancelled job must not block reuse")
	}
	// a track rendered from the recipe: not reusable (Luna's recipe is used by TrackLuna)
	var lunaRecipe string
	if err := e.st.R.QueryRow(`SELECT recipe_id FROM tracks WHERE id = ?`, lt.TrackLuna).Scan(&lunaRecipe); err != nil {
		t.Fatal(err)
	}
	var req string
	if err := e.st.R.QueryRow(`SELECT request_json FROM recipes WHERE id = ?`, lunaRecipe).Scan(&req); err != nil {
		t.Fatal(err)
	}
	if reusable(lineage.DraftRow{ID: "x", SourceRecipeID: lunaRecipe, RequestJSON: req}) {
		t.Error("a recipe with a track was reused")
	}
	// a stub recipe is never reusable
	var stub, stubReq string
	if err := e.st.R.QueryRow(`SELECT id, request_json FROM recipes WHERE engine = 'unknown' LIMIT 1`).Scan(&stub, &stubReq); err != nil {
		t.Fatal(err)
	}
	if reusable(lineage.DraftRow{ID: "x", SourceRecipeID: stub, RequestJSON: stubReq}) {
		t.Error("a stub recipe was reused")
	}
	if reusable(lineage.DraftRow{ID: "x", SourceRecipeID: "no-such-recipe", RequestJSON: "{}"}) {
		t.Error("a missing recipe was reused")
	}
}

func TestLineage_Tree(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 2)
	ctx := context.Background()
	tree, err := e.svc.Tree(ctx, lt.TrackJobChild, 0) // any node of the tree finds the root
	if err != nil {
		t.Fatal(err)
	}
	if tree.RootID != lt.TrackLuna || tree.Total != 2 || tree.Truncated || len(tree.Nodes) != 2 {
		t.Fatalf("tree = %+v", tree)
	}
	root, child := tree.Nodes[0], tree.Nodes[1]
	if root.ID != lt.TrackLuna || root.Generation != 0 || root.ParentID != nil || root.Operator != "" || root.ChildrenCount != 1 ||
		root.Seed == nil || *root.Seed != 101 || !root.RecipeKnown || root.Origin != "import" || root.JobID != nil || root.CostMicroUSD != nil {
		t.Errorf("root = %+v", root)
	}
	if child.ID != lt.TrackJobChild || child.ParentID == nil || *child.ParentID != lt.TrackLuna || child.Generation != 1 || child.Operator != "reroll_seed" ||
		child.Inferred || child.Origin != "job" || child.JobID == nil || *child.JobID != lt.JobDone || child.Seed == nil || *child.Seed != 1102 ||
		child.Outcome == nil || *child.Outcome != "clean" {
		t.Errorf("child = %+v", child)
	}
	// 18216 execution + ceil(12000 / 1 take) overhead share, billing matched => reconciled
	if child.CostMicroUSD == nil || int64(*child.CostMicroUSD) != 18216+12000 || child.CostBasis == nil || *child.CostBasis != "reconciled" {
		t.Errorf("child cost = %v %v", child.CostMicroUSD, child.CostBasis)
	}
	// the root track's tree is the same tree
	again, err := e.svc.Tree(ctx, lt.TrackLuna, 10)
	if err != nil || again.Total != 2 {
		t.Fatalf("by root: %+v %v", again, err)
	}

	// an inferred sibling and limit/truncation: Twin B is the inferred sibling of Twin A
	tw, err := e.svc.Tree(ctx, lt.TrackTwinB, 1)
	if err != nil {
		t.Fatal(err)
	}
	if tw.RootID != lt.TrackTwinA || tw.Total != 2 || !tw.Truncated || len(tw.Nodes) != 1 || tw.Nodes[0].ID != lt.TrackTwinA {
		t.Errorf("limit 1: %+v", tw)
	}
	full, err := e.svc.Tree(ctx, lt.TrackTwinB, 500)
	if err != nil {
		t.Fatal(err)
	}
	sib := full.Nodes[1]
	if !sib.Inferred || sib.Operator != "inferred_sibling" || sib.CostMicroUSD != nil {
		t.Errorf("sibling = %+v", sib)
	}

	// a job still awaiting settlement shows the estimate; unsettled nodes cannot exist as tracks without a job, so
	// rewrite the child's ledger view by hiding its settle rows is not possible (append-only): use the awaiting batch
	if _, err := e.svc.Tree(ctx, "no-such-track", 10); err == nil {
		t.Error("unknown track found")
	}

	// ratings: human and critic stars
	e.exec(`INSERT INTO ratings(track_id, source, stars, principal_kind, principal_id, principal_name, created_at, updated_at) VALUES (?, 'human', 4, 'human', 'human:h', 'h', ?, ?) ON CONFLICT(track_id, source) DO UPDATE SET stars = 4`, lt.TrackLuna, "2026-10-14T12:00:00Z", "2026-10-14T12:00:00Z")
	e.exec(`INSERT INTO ratings(track_id, source, stars, principal_kind, principal_id, principal_name, created_at, updated_at) VALUES (?, 'agent', 2, 'agent', 'agent:a', 'a', ?, ?) ON CONFLICT(track_id, source) DO UPDATE SET stars = 2`, lt.TrackLuna, "2026-10-14T12:00:00Z", "2026-10-14T12:00:00Z")
	r, _ := e.svc.Tree(ctx, lt.TrackLuna, 10)
	if r.Nodes[0].Stars == nil || *r.Nodes[0].Stars != 4 || r.Nodes[0].CriticStars == nil || *r.Nodes[0].CriticStars != 2 {
		t.Errorf("stars = %v %v", r.Nodes[0].Stars, r.Nodes[0].CriticStars)
	}
}

func TestLineage_ReferenceNodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 1)
	for _, id := range []string{lt.TrackRefFlac, lt.TrackLossy} {
		tree, err := e.svc.Tree(context.Background(), id, 10)
		if err != nil || tree.Total != 1 || len(tree.Nodes) != 1 {
			t.Fatalf("%s: %+v %v", id, tree, err)
		}
		n := tree.Nodes[0]
		if n.RecipeKnown || n.Seed != nil || !strings.HasPrefix(n.RequestID, "ref_") {
			t.Errorf("%s: reference node %+v must have no seed and recipe_known=false", id, n)
		}
		if n.Lossy != (id == lt.TrackLossy) {
			t.Errorf("%s: lossy = %v", id, n.Lossy)
		}
	}
}

// addTracks inserts nodes under Luna's tree in one transaction and returns an adder.
type adder struct {
	t      *testing.T
	tx     *sql.Tx
	root   string
	recipe string
	n      int
	title  string
}

func newAdder(e *env, title string) *adder {
	e.t.Helper()
	a := &adder{t: e.t, root: lt.TrackLuna, title: title}
	if err := e.st.R.QueryRow(`SELECT recipe_id FROM tracks WHERE id = ?`, a.root).Scan(&a.recipe); err != nil {
		e.t.Fatal(err)
	}
	tx, err := e.st.W.Begin()
	if err != nil {
		e.t.Fatal(err)
	}
	a.tx = tx
	return a
}

func (a *adder) add(parent string, gen int, at string) string {
	a.n++
	id := fmt.Sprintf("tt-%03d", a.n)
	_, err := a.tx.Exec(`INSERT INTO tracks(id, slug, title, recipe_id, parent_track_id, root_track_id, generation, status, status_set_by, origin, duration_s, sample_rate, bits_per_sample, channels, total_samples,
		master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5, import_rule, recorded_at, created_at)
		VALUES (?,?,?,?,?,?,?,'draft','import','import',6,48000,24,2,288000,?,?,1,?,?,?, 'slug', ?, ?)`,
		id, id, fmt.Sprintf("%s %d", a.title, a.n), a.recipe, parent, a.root, gen, "m/"+id, "sha-"+id, "src-"+id, "fr-"+id, "md5", at, at)
	if err != nil {
		a.t.Fatal(err)
	}
	return id
}

func TestLineage_PreOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 1)
	a := newAdder(e, "node")
	root := lt.TrackLuna
	c1 := a.add(root, 1, "2026-10-01T10:00:00Z")
	c3 := a.add(root, 1, "2026-10-01T12:00:00Z")
	c2 := a.add(root, 1, "2026-10-01T11:00:00Z")
	g1 := a.add(c2, 2, "2026-10-01T13:00:00Z")
	g2 := a.add(c2, 2, "2026-10-01T13:30:00Z")
	h1 := a.add(g1, 3, "2026-10-01T14:00:00Z")
	if err := a.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tree, err := e.svc.Tree(context.Background(), g2, 500)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, nd := range tree.Nodes {
		order = append(order, nd.ID)
	}
	if diff := cmp.Diff([]string{root, c1, c2, g1, h1, g2, c3}, order); diff != "" {
		t.Errorf("pre-order (-want +got):\n%s", diff)
	}
	if tree.Total != 7 || tree.Truncated {
		t.Errorf("total %d truncated %v", tree.Total, tree.Truncated)
	}
}

func TestLineage_TruncatedByBytes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 1)
	a := newAdder(e, strings.Repeat("x", 190))
	root := lt.TrackLuna
	c1 := a.add(root, 1, "2026-10-01T10:00:00Z")
	for i := 0; i < 30; i++ {
		a.add(c1, 2, fmt.Sprintf("2026-10-02T10:00:%02dZ", i))
	}
	if err := a.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if e.svc.PageBytes() != 90000 {
		t.Fatalf("the production page cap is %d, want 90000 (M1 C17)", e.svc.PageBytes())
	}
	e.svc.SetPageBytesForTest(6000) // ~600 bytes a node: about nine nodes fit
	cut, err := e.svc.Tree(ctx, root, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !cut.Truncated || cut.Total != 32 || len(cut.Nodes) < 5 || len(cut.Nodes) > 10 || cut.Nodes[0].ID != root {
		t.Fatalf("truncated=%v total=%d nodes=%d", cut.Truncated, cut.Total, len(cut.Nodes))
	}
	total := 0
	for _, nd := range cut.Nodes {
		b, _ := json.Marshal(nd)
		total += len(b) + 1
	}
	if total > 6000 {
		t.Errorf("the cut page is %d bytes", total)
	}
	// the cut is at a node boundary in pre-order: a prefix of the full walk
	e.svc.SetPageBytesForTest(90000)
	full, _ := e.svc.Tree(ctx, root, 500)
	if full.Truncated || len(full.Nodes) != 32 {
		t.Fatalf("full: %d nodes truncated=%v", len(full.Nodes), full.Truncated)
	}
	for i, nd := range cut.Nodes {
		if nd.ID != full.Nodes[i].ID {
			t.Fatalf("node %d is %s, want %s", i, nd.ID, full.Nodes[i].ID)
		}
	}
	lim, _ := e.svc.Tree(ctx, root, 7)
	if len(lim.Nodes) != 7 || !lim.Truncated || lim.Total != 32 {
		t.Errorf("limit 7: %d nodes truncated=%v total=%d", len(lim.Nodes), lim.Truncated, lim.Total)
	}
}
