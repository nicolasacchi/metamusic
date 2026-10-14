package lineage_test

import (
	"context"
	"strings"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

func TestLineage_ClosedStoreErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, 2)
	_ = e.st.R.Close()
	if _, err := e.svc.Tree(ctx, lt.TrackLuna, 10); err == nil {
		t.Error("Tree on a closed reader")
	}
	if _, err := e.svc.BackfillRecipeKeys(ctx); err == nil {
		t.Error("Backfill on a closed reader")
	}
	if _, err := lineage.UsedSeeds(ctx, e.st.R, "h"); err == nil {
		t.Error("UsedSeeds on a closed reader")
	}
	if _, _, err := lineage.ReusableDraftRecipe(ctx, e.st.R, lineage.DraftRow{SourceRecipeID: "x"}); err == nil {
		t.Error("ReusableDraftRecipe on a closed reader")
	}

	// a closed writer: the read side finds work, the transaction cannot start
	w := newEnv(t, 2)
	w.exec(`DROP TRIGGER recipe_keys_no_delete`)
	w.exec(`DELETE FROM recipe_keys WHERE recipe_id = (SELECT id FROM recipes WHERE request_id = 'fxb_001_v1')`)
	_ = w.st.W.Close()
	if _, err := w.svc.BackfillRecipeKeys(ctx); err == nil {
		t.Error("Backfill on a closed writer")
	}
}

func TestBackfill_RefusesAnInvalidRequest(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 2)
	e.exec(`DROP TRIGGER recipe_keys_no_delete`)
	e.exec(`DELETE FROM recipe_keys WHERE recipe_id = (SELECT id FROM recipes WHERE request_id = 'fxb_001_v1')`)
	e.exec(`DROP TRIGGER recipes_immutable`)
	e.exec(`UPDATE recipes SET request_json = '{"id":"fxb_001_v1","seed":201}' WHERE request_id = 'fxb_001_v1'`) // still an object: keys computable
	if n, err := e.svc.BackfillRecipeKeys(context.Background()); err != nil || n != 1 {
		t.Fatalf("object: %d %v", n, err)
	}
	e.exec(`DELETE FROM recipe_keys WHERE recipe_id = (SELECT id FROM recipes WHERE request_id = 'fxb_001_v1')`)
	e.exec(`ALTER TABLE recipes RENAME TO recipes_gone`)
	if _, err := e.svc.BackfillRecipeKeys(context.Background()); err == nil {
		t.Error("a missing table was ignored")
	}
}

func TestKeysAndApplyEdges(t *testing.T) {
	t.Parallel()
	if c, r := lineage.Keys([]byte(`[1]`)); c != "" || r != "" {
		t.Error("an array has keys")
	}
	if !lineage.IsTemplateMode("lyric_edit") || lineage.IsTemplateMode("from_draft") {
		t.Error("IsTemplateMode")
	}
	if _, err := lineage.RequestOf([]byte(`{"id":"a"}`)); err == nil {
		t.Error("RequestOf accepted a request without style")
	}
	req, err := lineage.RequestOf([]byte(`{"cot":"full","id":"a","lyrics":"[Verse 1]\nx","seed":3,"style":"pop"}`))
	if err != nil || req.Seed != 3 {
		t.Fatalf("RequestOf = %+v %v", req, err)
	}
	errs, warns := lineage.Lint([]byte(`{"cot":"full","id":"a","lyrics":"[Verse 1]\nx","seed":3,"style":"pop"}`))
	if len(errs) != 0 {
		t.Errorf("lint errors %v (warnings %v)", errs, warns)
	}
	if errs, _ := lineage.Lint([]byte(`{"id":"a"}`)); len(errs) == 0 {
		t.Error("lint accepted an incomplete request")
	}
	if _, err := lineage.ApplyParsed(req, lineage.OpRerollSeed, lineage.Params{Seed: -1}, nil); err == nil {
		t.Error("a negative seed was accepted")
	}
	// ids: the stem cut keeps a rune boundary; long multi-byte stems stay valid UTF-8 (ids are ASCII, but the cut must not split)
	got, err := lineage.AllocateID(strings.Repeat("é", 100), 1, false, func(string) bool { return false })
	if err != nil || len(got) > 180 || !strings.HasSuffix(got, "-s1") {
		t.Errorf("multi-byte stem: %q %v", got, err)
	}
	if got, _ := lineage.AllocateID("-s5", 5, false, func(string) bool { return false }); !strings.HasSuffix(got, "-s5") {
		t.Errorf("empty stem: %q", got)
	}
}
