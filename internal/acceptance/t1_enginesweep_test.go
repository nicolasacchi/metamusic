package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// insertDSPTake writes one transform Take (recipe, transform row, take, ledger row) into the rig's library, as the T1
// bridge does, and returns its id. The parent is the first lossless YuE2 Take of the fixture.
func insertDSPTake(t *testing.T, r *servertest.Rig, kind string) (id, parent string) {
	t.Helper()
	ctx := context.Background()
	if err := r.Store.R.QueryRow(`SELECT t.id FROM tracks t JOIN recipes c ON c.id = t.recipe_id WHERE c.engine = 'yue2' AND t.lossy = 0 ORDER BY t.id LIMIT 1`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	tx, err := r.Store.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	raw := `{"dsp_version":"mm-dsp/1","id":"dsp-` + kind + `-sweep","kind":"` + kind + `","seed":0,"v":1}`
	h := sha256.Sum256([]byte(raw))
	rid, _, err := r.Services.Deps.Library.InsertTransformRecipe(ctx, tx, library.SpecRecipe{Kind: kind, Canonical: []byte(raw), Hash: hex.EncodeToString(h[:]), RequestID: "dsp-" + kind + "-sweep"}, "human:sweep")
	if err != nil {
		t.Fatal(err)
	}
	id = "01900000-0000-7000-8000-00000000e0aa"
	if _, err := tx.ExecContext(ctx, `INSERT INTO transforms(id,kind,recipe_id,spec_hash,title,principal_id,principal_kind,principal_name,state,expected_cpu_ms,max_cpu_ms,out_seconds_est,mem_mb_est,created_at)
		VALUES(?,?,?,?,'polytempo 90/120 sweep','human:sweep','human','sweep','queued',1,1,1.0,1,?)`, id, kind, rid, librarytest.Sha("spec-"+kind), "2026-11-03T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	var root string
	var gen int
	if err := tx.QueryRowContext(ctx, `SELECT root_track_id, generation FROM tracks WHERE id = ?`, parent).Scan(&root, &gen); err != nil {
		t.Fatal(err)
	}
	if err := r.Services.Deps.Library.InsertTake(ctx, tx, library.NewTake{
		ID: id, Title: "polytempo 90/120 sweep", Slug: "sweep", RecipeID: rid, ParentTrackID: parent, RootTrackID: root, Generation: gen + 1,
		DurationS: 5, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 240000,
		MasterPath: "audio/masters/sweep.flac", MasterSHA256: librarytest.Sha("sweep-m"), MasterBytes: 10,
		SourceSHA256: librarytest.Sha("sweep-s"), FramesSHA256: librarytest.Sha("sweep-f"), AudioMD5: "00000000000000000000000000000000",
		ImportRule: "transform", PrincipalID: "human:sweep", RecordedAt: librarytest.FixedNow, Origin: "transform", TransformID: id,
		ToolVersion: "test", Mutation: &library.NewMutation{Operator: kind},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id, parent
}

// TestEngineSweep_Dsp: a library holding a transform Take (engine dsp) answers every reader that means "a YuE2 recipe"
// exactly as the library without it (C211.1c): songs.jsonl byte-identical, the seed and style-term statistics and the
// generation counters unchanged, and every mother-taking planner mode refuses the transform Take with
// TRANSFORM_TAKE_NOT_MOTHER naming its nearest renderable ancestor.
func TestEngineSweep_Dsp(t *testing.T) {
	r := l3Rig(t, servertest.Options{Backend: "fake"}) // a backend, so the planner is enabled
	h := r.Human(t)

	exportAll := func(key string) (sha string, rows float64) {
		t.Helper()
		resp := h.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks"}, "Idempotency-Key", key)
		if resp.Status/100 != 2 {
			t.Fatalf("export answered %d: %s", resp.Status, resp.Body)
		}
		m := obj(resp.JSON(t))
		return fmt.Sprint(m["sha256"]), obj(m["jsonl"])["rows"].(float64)
	}
	statsOf := func() string {
		t.Helper()
		s := obj(h.OK(t, "GET", "/api/stats", nil))
		b, err := json.Marshal(map[string]any{"generation": s["generation"], "breakdown": s["breakdown"]})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	shaBefore, rowsBefore := exportAll("sweep-before")
	statsBefore := statsOf()

	id, parent := insertDSPTake(t, r, "polytempo")
	r.Settle(t)

	shaAfter, rowsAfter := exportAll("sweep-after")
	if shaAfter != shaBefore || rowsAfter != rowsBefore {
		t.Errorf("songs.jsonl changed by a transform Take: %s (%v rows) -> %s (%v rows)", shaBefore, rowsBefore, shaAfter, rowsAfter)
	}
	if got := statsOf(); got != statsBefore {
		t.Errorf("seed/style statistics or generation counters changed by a transform Take:\n%s\n%s", statsBefore, got)
	}
	// the RelinkVariantiMothers sweep and the snapshot reader are neutral: they run over the library without error
	tx, err := r.Store.W.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Services.Deps.Library.RelinkVariantiMothers(context.Background(), tx); err != nil {
		t.Errorf("RelinkVariantiMothers over a library with a dsp take: %v", err)
	}
	_ = tx.Rollback()

	// lyrics_of reads recipe lyrics: a dsp target answers nothing and the transform Take is nobody's partner
	if items, _ := obj(h.OK(t, "GET", "/api/tracks?lyrics_of="+id, nil))["items"].([]any); len(items) != 0 {
		t.Errorf("lyrics_of a transform Take = %v, want none", items)
	}
	for _, it := range obj(h.OK(t, "GET", "/api/tracks?lyrics_of="+parent, nil))["items"].([]any) {
		if obj(it)["id"] == id {
			t.Error("a transform Take is a lyrics_of candidate")
		}
	}

	// every mother-taking mode refuses the transform Take
	for _, item := range []map[string]any{
		{"mode": "new_seed", "parent_track_id": id, "n_seeds": 2},
		{"mode": "same_seed_edit", "parent_track_id": id, "patch": map[string]any{"style": "x"}},
		{"mode": "lyric_edit", "parent_track_id": id, "patch": map[string]any{"lyrics": "x"}},
		{"mode": "style_edit", "parent_track_id": id, "patch": map[string]any{"style": "x"}},
		{"mode": "from_template", "parent_track_ids": []string{parent, id}, "template_mode": "new_seed"},
	} {
		resp := h.Do(t, "POST", "/api/generations/estimate", map[string]any{"items": []map[string]any{item}})
		if resp.Status != 422 {
			t.Errorf("%v: status %d: %s", item["mode"], resp.Status, resp.Body)
			continue
		}
		pr := obj(resp.JSON(t))
		d := obj(pr["details"])
		if pr["code"] != "TRANSFORM_TAKE_NOT_MOTHER" || d["track_id"] != id || d["nearest_renderable_ancestor_id"] != parent {
			t.Errorf("%v: problem = %v", item["mode"], pr)
		}
	}
}
