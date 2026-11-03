package library_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// TestInsertRecipe_EngineDispatch: the engine column, hashes and recipe_keys follow the engine named by the
// caller; the yue2.Parsed entry point still writes "yue2" with the same bytes.
func TestInsertRecipe_EngineDispatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	p := jobRecipe(t, "gen-e2-s1", 1)
	viaOld, created, err := e.svc.InsertRecipe(ctx, tx, p, "import")
	if err != nil || !created {
		t.Fatalf("legacy insert: %v %v", created, err)
	}
	viaNew, created, err := e.svc.InsertEngineRecipe(ctx, tx, yue2.EngineName, yue2.RecipeOf(p), "import")
	if err != nil || created || viaNew != viaOld {
		t.Fatalf("same recipe through the seam must find the legacy row: id=%q created=%v err=%v", viaNew, created, err)
	}

	// a recipe of another engine (synthetic canonical request) lands under that engine name
	canon := []byte(`{"caption":"warm pop","id":"syn-ace-1","seed":5}`)
	r := engine.Recipe{ID: "syn-ace-1", Style: "warm pop", Cot: "off", Seed: 5, Canonical: canon, Hash: "h-ace-1", CoreHash: "c-ace-1"}
	rid, created, err := e.svc.InsertEngineRecipe(ctx, tx, "acestep15", r, "agent:tok1")
	if err != nil || !created {
		t.Fatalf("insert acestep15: %v %v", created, err)
	}
	var eng, hash, content, render string
	if err := tx.QueryRow(`SELECT r.engine, r.hash, k.content_hash, k.render_hash FROM recipes r JOIN recipe_keys k ON k.recipe_id = r.id WHERE r.id = ?`, rid).
		Scan(&eng, &hash, &content, &render); err != nil {
		t.Fatal(err)
	}
	wantC, wantR := lineage.Keys(canon)
	if eng != "acestep15" || hash != "h-ace-1" || content != wantC || render != wantR {
		t.Errorf("row = %q %q %q %q", eng, hash, content, render)
	}
	var oldEng string
	if err := tx.QueryRow(`SELECT engine FROM recipes WHERE id = ?`, viaOld).Scan(&oldEng); err != nil || oldEng != "yue2" {
		t.Errorf("legacy row engine = %q (%v)", oldEng, err)
	}
	if _, _, err := e.svc.InsertEngineRecipe(ctx, tx, "", r, "import"); err == nil {
		t.Error("an empty engine name must be refused")
	}
}
