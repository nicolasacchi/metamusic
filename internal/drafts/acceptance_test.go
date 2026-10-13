package drafts_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestAcceptance_M2a_VariantiDraftsEditableLintClean: over the imported synthetic archive every
// varianti_import draft is listed, editable and free of lint errors; editing one lyric line bumps the
// revision and the edited request still lints clean. Repeated through MCP with an agent token.
func TestAcceptance_M2a_VariantiDraftsEditableLintClean(t *testing.T) {
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	r := servertest.New(t, servertest.Options{Archive: &tpl})
	human := r.Human(t)

	list := m(t, human.OK(t, "GET", "/api/drafts?origin=varianti_import&limit=100", nil))
	items := list["items"].([]any)
	if len(items) == 0 {
		t.Fatal("the archive has no varianti_import drafts")
	}
	if int(list["total"].(float64)) != len(items) {
		t.Fatalf("paged result: total %v, %d items", list["total"], len(items))
	}
	for _, it := range items {
		row := m(t, it)
		if row["origin"] != "varianti_import" || row["editable"] != true || m(t, row["lint"])["errors"] != 0.0 {
			t.Fatalf("draft row %v", row)
		}
		d := m(t, human.OK(t, "GET", "/api/drafts/"+row["id"].(string), nil))
		if errs := m(t, d["lint"])["errors"].([]any); len(errs) != 0 || d["editable"] != true {
			t.Fatalf("draft %v: lint errors %v", row["request_id"], errs)
		}
	}

	id := m(t, items[0])["id"].(string)
	edit := func(face string, revision float64, get func() map[string]any, patch func(map[string]any) map[string]any, lint func(map[string]any) map[string]any) float64 {
		t.Helper()
		d := get()
		if d["revision"] != revision {
			t.Fatalf("%s: revision %v, want %v", face, d["revision"], revision)
		}
		req := m(t, d["request"])
		lyrics := req["lyrics"].(string)
		lines := strings.Split(lyrics, "\n")
		for i, l := range lines {
			if strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimSpace(l), "[") {
				lines[i] = l + " edited-" + face
				break
			}
		}
		req["lyrics"] = strings.Join(lines, "\n")
		up := patch(map[string]any{"revision": revision, "request": req})
		if up["revision"] != revision+1 || up["editable"] != true || m(t, up["request"])["lyrics"] != req["lyrics"] {
			t.Fatalf("%s: update result %v", face, up)
		}
		rep := lint(req)
		if rep["ok"] != true {
			t.Fatalf("%s: lint_recipe on the edited request: %v", face, rep)
		}
		if got := get(); got["revision"] != revision+1 {
			t.Fatalf("%s: revision after %v", face, got["revision"])
		}
		return revision + 1
	}

	// REST, as the operator
	rev := edit("rest", 1,
		func() map[string]any { return m(t, human.OK(t, "GET", "/api/drafts/"+id, nil)) },
		func(b map[string]any) map[string]any { return m(t, human.OK(t, "PATCH", "/api/drafts/"+id, b)) },
		func(req map[string]any) map[string]any {
			return m(t, human.OK(t, "POST", "/api/recipes/lint", map[string]any{"items": []any{map[string]any{"request": req, "draft_id": id}}}))
		})

	// MCP, with an agent token
	sess := r.MCP(t, rw...)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		v, isErr := mcpCall(t, sess, name, args)
		if isErr {
			t.Fatalf("%s failed", name)
		}
		return v
	}
	edit("mcp", rev,
		func() map[string]any { return call("get_draft", map[string]any{"id": id}) },
		func(b map[string]any) map[string]any {
			b["id"] = id
			return call("call_operation", map[string]any{"operation_id": "update_draft", "arguments": b})
		},
		func(req map[string]any) map[string]any {
			return call("lint_recipe", map[string]any{"items": []any{map[string]any{"request": req, "draft_id": id}}})
		})

	// the MCP agent's edit is attributed to its own key
	d := call("get_draft", map[string]any{"id": id})
	if !strings.HasPrefix(fmt.Sprint(d["updated_by"]), "agent:") || d["created_by"] != "import" {
		t.Errorf("created_by %v updated_by %v", d["created_by"], d["updated_by"])
	}
	var n int
	_ = r.Store.R.QueryRow(`SELECT count(*) FROM recipe_drafts WHERE id=? AND revision=3 AND origin='varianti_import' AND source_recipe_id IS NOT NULL`, id).Scan(&n)
	if n != 1 {
		t.Error("the edited draft lost its origin or source")
	}
}
