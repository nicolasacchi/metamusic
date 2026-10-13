package drafts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	archivetest.Cleanup()
	os.Exit(code)
}

var rw = []auth.Scope{auth.ScopeRead, auth.ScopeWrite}

const jsonCT = "Content-Type"

func rawDo(t *testing.T, c *servertest.Client, method, path, body string, hdr ...string) *servertest.Response {
	t.Helper()
	return c.Do(t, method, path, body, append([]string{jsonCT, "application/json"}, hdr...)...)
}

func m(t *testing.T, v any) map[string]any {
	t.Helper()
	o, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %v", v)
	}
	return o
}

func lintBody(reqs ...string) string {
	items := make([]string, len(reqs))
	for i, r := range reqs {
		items[i] = `{"request":` + r + `}`
	}
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

func mcpCall(t *testing.T, sess *mcp.ClientSession, name string, args any) (map[string]any, bool) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		return nil, true
	}
	b, _ := json.Marshal(res.StructuredContent)
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	return v, false
}

func TestLintRecipe_RawTokens(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	agent, _ := r.Agent(t, auth.ScopeRead)
	const tail = `"style":"english","lyrics":"[Verse]\nx"`
	reqs := []string{`{"id":"a",` + tail + `,"seed":9007199254740992}`, `{"id":"b",` + tail + `,"seed":1e3}`, `{"id":"c",` + tail + `,"seed":9007199254740991}`}
	check := func(face string, items []any) {
		t.Helper()
		for i, want := range []string{"seed_out_of_range", "seed_out_of_range", ""} {
			errs := m(t, items[i])["errors"].([]any)
			switch {
			case want == "" && len(errs) != 0:
				t.Errorf("%s item %d: %v", face, i, errs)
			case want != "" && (len(errs) != 1 || m(t, errs[0])["code"] != want):
				t.Errorf("%s item %d: %v", face, i, errs)
			}
		}
	}
	resp := rawDo(t, agent, "POST", "/api/recipes/lint", lintBody(reqs...))
	if resp.Status != 200 {
		t.Fatalf("REST: %d %s", resp.Status, resp.Body)
	}
	rest := m(t, resp.JSON(t))
	if rest["ok"] != false {
		t.Errorf("ok %v", rest["ok"])
	}
	check("REST", rest["items"].([]any))

	sess := r.MCP(t, auth.ScopeRead)
	var items []any
	for _, q := range reqs {
		items = append(items, map[string]any{"request": json.RawMessage(q)})
	}
	got, isErr := mcpCall(t, sess, "lint_recipe", map[string]any{"items": items})
	if isErr {
		t.Fatal("MCP lint_recipe errored")
	}
	check("MCP", got["items"].([]any))
}

func TestLintRecipe_BodyLimit(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	agent, _ := r.Agent(t, auth.ScopeRead)
	pad := func(total int) string {
		head := `{"items":[{"request":{"id":"a","style":"s","lyrics":"`
		tail := `"}}]}`
		return head + strings.Repeat("x", total-len(head)-len(tail)) + tail
	}
	if resp := rawDo(t, agent, "POST", "/api/recipes/lint", pad(2<<20-1)); resp.Status != 200 {
		t.Errorf("2 MiB - 1: %d", resp.Status)
	}
	resp := rawDo(t, agent, "POST", "/api/recipes/lint", pad(2<<20+1))
	if resp.Status != 413 || m(t, resp.JSON(t))["code"] != "PAYLOAD_TOO_LARGE" {
		t.Errorf("2 MiB + 1: %d %.200s", resp.Status, resp.Body)
	}
}

func TestLintRecipe_Behaviour(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	agent, _ := r.Agent(t, auth.ScopeRead)
	a := `{"id":"same","style":"english","lyrics":"[Verse]\nx"}`
	// batch defaults to true; batch=false turns the duplicate check off
	resp := rawDo(t, agent, "POST", "/api/recipes/lint", lintBody(a, a))
	if body := m(t, resp.JSON(t)); resp.Status != 200 || body["ok"] != false || len(body["batch_errors"].([]any)) != 1 {
		t.Errorf("batch default: %d %s", resp.Status, resp.Body)
	}
	resp = rawDo(t, agent, "POST", "/api/recipes/lint", `{"batch":false,"items":[{"request":`+a+`},{"request":`+a+`}]}`)
	if body := m(t, resp.JSON(t)); body["ok"] != true || len(body["batch_errors"].([]any)) != 0 {
		t.Errorf("batch=false: %s", resp.Body)
	}
	if resp := rawDo(t, agent, "POST", "/api/recipes/lint", `{"items":[]}`); resp.Status != 422 {
		t.Errorf("empty items: %d", resp.Status)
	}
	// a draft's own request does not duplicate its recipe when draft_id is given
	var rj string
	_ = r.Store.R.QueryRow(`SELECT request_json FROM recipe_drafts WHERE id=?`, lt.Draft1).Scan(&rj)
	resp = rawDo(t, agent, "POST", "/api/recipes/lint", `{"items":[{"request":`+rj+`,"draft_id":"`+lt.Draft1+`"}]}`)
	for _, w := range m(t, resp.JSON(t))["items"].([]any)[0].(map[string]any)["warnings"].([]any) {
		if m(t, w)["code"] == "duplicate_recipe" {
			t.Errorf("own recipe flagged: %s", resp.Body)
		}
	}
}

func goodRequest(id string) map[string]any {
	return map[string]any{"id": id, "style": "english, 100 bpm", "lyrics": lyricsOf(""), "cot": "full", "seed": 5}
}

func TestDraftOps_REST(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	human := r.Human(t)
	agent, agentID := r.Agent(t, rw...)

	// create: needs an idempotency key, answers 201, replays on the same key
	if resp := human.Do(t, "POST", "/api/drafts", map[string]any{"request": goodRequest("rest-a")}); resp.Status != 428 {
		t.Errorf("no key: %d", resp.Status)
	}
	resp := human.Do(t, "POST", "/api/drafts", map[string]any{"request": goodRequest("rest-a_v1"), "label": "first"}, "Idempotency-Key", "k1")
	d := m(t, resp.JSON(t))
	if resp.Status != 201 || d["origin"] != "editor" || d["revision"] != 1.0 || d["editable"] != true || d["created_by"] != "human:alice" || d["label"] != "first" {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	again := human.Do(t, "POST", "/api/drafts", map[string]any{"request": goodRequest("rest-a_v1"), "label": "first"}, "Idempotency-Key", "k1")
	if m(t, again.JSON(t))["id"] != d["id"] {
		t.Errorf("replay created another draft: %s", again.Body)
	}
	id := d["id"].(string)

	// the agent sees the operator masked, and creates as origin agent
	g := m(t, agent.Do(t, "GET", "/api/drafts/"+id, nil).JSON(t))
	if g["created_by"] != "human" || g["updated_by"] != "human" {
		t.Errorf("agent view: %v %v", g["created_by"], g["updated_by"])
	}
	resp = agent.Do(t, "POST", "/api/drafts", map[string]any{"request": goodRequest("rest-b")}, "Idempotency-Key", "k2")
	ad := m(t, resp.JSON(t))
	if resp.Status != 201 || ad["origin"] != "agent" || ad["created_by"] != "agent:"+agentID {
		t.Fatalf("agent create: %d %s", resp.Status, resp.Body)
	}
	if hv := m(t, human.Do(t, "GET", "/api/drafts/"+ad["id"].(string), nil).JSON(t)); hv["created_by"] != "agent:"+agentID {
		t.Errorf("human view: %v", hv["created_by"])
	}

	// update with revision; stale => 409 with the current revision
	resp = human.Do(t, "PATCH", "/api/drafts/"+id, map[string]any{"revision": 1, "label": "second"})
	if u := m(t, resp.JSON(t)); resp.Status != 200 || u["revision"] != 2.0 || u["label"] != "second" {
		t.Fatalf("update: %d %s", resp.Status, resp.Body)
	}
	resp = human.Do(t, "PATCH", "/api/drafts/"+id, map[string]any{"revision": 1, "label": "third"})
	if p := m(t, resp.JSON(t)); resp.Status != 409 || p["code"] != "REVISION_CONFLICT" || m(t, p["details"])["current_revision"] != 2.0 {
		t.Errorf("stale: %d %s", resp.Status, resp.Body)
	}
	// parse errors answer LINT_FAILED with the issues
	resp = human.Do(t, "PATCH", "/api/drafts/"+id, map[string]any{"revision": 2, "request": map[string]any{"id": "x"}})
	p := m(t, resp.JSON(t))
	items, _ := m(t, p["details"])["items"].([]any)
	if resp.Status != 422 || p["code"] != "LINT_FAILED" || len(items) != 1 || len(m(t, items[0])["errors"].([]any)) == 0 {
		t.Errorf("lint failed: %d %s", resp.Status, resp.Body)
	}
	// fulfilled drafts
	if _, err := r.Store.W.Exec(`UPDATE recipe_drafts SET status='fulfilled', fulfilled_by_track_id=? WHERE id=?`, lt.TrackLuna, id); err != nil {
		t.Fatal(err)
	}
	resp = human.Do(t, "PATCH", "/api/drafts/"+id, map[string]any{"revision": 2, "label": "x"})
	if p := m(t, resp.JSON(t)); resp.Status != 409 || p["code"] != "DRAFT_NOT_EDITABLE" {
		t.Errorf("fulfilled: %d %s", resp.Status, resp.Body)
	}
	if resp := human.Do(t, "PATCH", "/api/drafts/nope", map[string]any{"revision": 1, "label": "x"}); resp.Status != 404 {
		t.Errorf("unknown: %d", resp.Status)
	}
	// a read-only token cannot write
	ro, _ := r.Agent(t, auth.ScopeRead)
	if resp := ro.Do(t, "PATCH", "/api/drafts/"+id, map[string]any{"revision": 2, "label": "x"}); resp.Status != 403 {
		t.Errorf("read-only: %d", resp.Status)
	}
	// list rows carry lint counts and revision
	list := m(t, human.Do(t, "GET", "/api/drafts?limit=100", nil).JSON(t))
	for _, it := range list["items"].([]any) {
		row := m(t, it)
		l := m(t, row["lint"])
		if _, ok := l["errors"].(float64); !ok || row["revision"] == nil || row["editable"] == nil {
			t.Fatalf("row %v", row)
		}
		if row["origin"] == "editor" && l["warnings"] == nil {
			t.Errorf("lint counts %v", l)
		}
	}
}

func TestDraftOps_MCPIdempotentCreate(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	sess := r.MCP(t, rw...)
	args := map[string]any{"request": goodRequest("mcp-a")}
	first, isErr := mcpCall(t, sess, "create_draft", args)
	if isErr {
		t.Fatal("create_draft errored")
	}
	second, isErr := mcpCall(t, sess, "create_draft", args)
	if isErr || second["id"] != first["id"] {
		t.Fatalf("second identical create: %v vs %v", second["id"], first["id"])
	}
	var n int
	_ = r.Store.R.QueryRow(`SELECT count(*) FROM recipe_drafts WHERE request_id='mcp-a'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d drafts", n)
	}
	up, isErr := mcpCall(t, sess, "call_operation", map[string]any{"operation_id": "update_draft", "arguments": map[string]any{"id": first["id"], "revision": 1, "status": "archived"}})
	if isErr || up["status"] != "archived" || up["revision"] != 2.0 {
		t.Errorf("update via generic: %v %v", up, isErr)
	}
}

func uploadBody(jsonl string, extra string) string {
	b, _ := json.Marshal(jsonl)
	return `{"jsonl":` + string(b) + extra + `}`
}

func TestUploadOp_REST(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	human := r.Human(t)
	good := func(id string) string { b, _ := json.Marshal(goodRequest(id)); return string(b) }
	n := 0
	post := func(body string) *servertest.Response {
		n++
		return rawDo(t, human, "POST", "/api/drafts/batch", body, "Idempotency-Key", fmt.Sprintf("u%d", n))
	}
	resp := post(uploadBody(good("up-1")+"\n"+good("up-2")+"\n", `,"collection":"c1"`))
	if body := m(t, resp.JSON(t)); resp.Status != 201 || body["created"] != 2.0 || body["collection"] != "c1" {
		t.Fatalf("upload: %d %s", resp.Status, resp.Body)
	}
	resp = post(uploadBody(good("up-3")+"\n{\"id\":\"x\"}\n", ""))
	if p := m(t, resp.JSON(t)); resp.Status != 422 || p["code"] != "LINT_FAILED" {
		t.Errorf("all-or-nothing: %d %s", resp.Status, resp.Body)
	}
	resp = post(uploadBody(good("up-3")+"\n{\"id\":\"x\"}\n", `,"allow_partial":true`))
	if body := m(t, resp.JSON(t)); resp.Status != 201 || body["created"] != 1.0 || len(body["errors"].([]any)) != 1 {
		t.Errorf("partial: %d %s", resp.Status, resp.Body)
	}

	// limits
	resp = post(uploadBody(strings.Repeat("{}\n", 1001), ""))
	if p := m(t, resp.JSON(t)); resp.Status != 422 || p["code"] != "VALIDATION_FAILED" || m(t, p["details"])["field"] != "jsonl" {
		t.Errorf("1001 lines: %d %s", resp.Status, resp.Body)
	}
	files := map[string]string{}
	for i := 0; i < 51; i++ {
		files[fmt.Sprintf("f%d.abc", i)] = "X:1\nK:C\nC|"
	}
	fb, _ := json.Marshal(files)
	resp = post(uploadBody(good("up-4")+"\n", `,"abc_files":`+string(fb)))
	if p := m(t, resp.JSON(t)); resp.Status != 422 || m(t, p["details"])["field"] != "abc_files" {
		t.Errorf("51 files: %d %s", resp.Status, resp.Body)
	}
	big, _ := json.Marshal(map[string]string{"a.abc": strings.Repeat("x", 1<<20+1)})
	resp = post(uploadBody(good("up-4")+"\n", `,"abc_files":`+string(big)))
	if p := m(t, resp.JSON(t)); resp.Status != 422 || m(t, p["details"])["field"] != "abc_files" {
		t.Errorf("1 MiB + 1: %d %s", resp.Status, resp.Body)
	}
	resp = post(uploadBody(good("up-5")+"\n", `,"junk":"`+strings.Repeat("x", 7<<20)+`"`))
	if p := m(t, resp.JSON(t)); resp.Status != 413 || p["code"] != "PAYLOAD_TOO_LARGE" {
		t.Errorf("7 MiB body: %d %.200s", resp.Status, resp.Body)
	}
	// 4 MiB of JSONL (within its own cap) whose JSON escaping pushes the body past 6 MiB
	resp = post(uploadBody(strings.Repeat("\n", 4<<20), ""))
	if p := m(t, resp.JSON(t)); resp.Status != 413 || p["code"] != "PAYLOAD_TOO_LARGE" {
		t.Errorf("escaped 4 MiB: %d %.200s", resp.Status, resp.Body)
	}
	// a body just under the cap still reaches the handler
	five := map[string]string{}
	for i := 0; i < 5; i++ {
		five[fmt.Sprintf("pad%d.abc", i)] = strings.Repeat("x", 1<<20)
	}
	fiveJSON, _ := json.Marshal(five)
	resp = post(uploadBody(good("up-6")+"\n", `,"abc_files":`+string(fiveJSON)))
	if resp.Status != 201 {
		t.Errorf("5 MiB body: %d %.200s", resp.Status, resp.Body)
	}
}

func TestUploadOp_ABCFilesREST(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	human := r.Human(t)
	req := map[string]any{"id": "abc-1", "style": "english", "lyrics": lyricsOf(""), "cot": "melody", "seed": 3, "abc_path": "s/a.abc"}
	b, _ := json.Marshal(req)
	files, _ := json.Marshal(map[string]string{"s/a.abc": abcText})
	resp := rawDo(t, human, "POST", "/api/drafts/batch", uploadBody(string(b)+"\n", `,"abc_files":`+string(files)), "Idempotency-Key", "a1")
	body := m(t, resp.JSON(t))
	if resp.Status != 201 || body["created"] != 1.0 {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
	id := m(t, body["drafts"].([]any)[0])["id"].(string)
	d := m(t, human.Do(t, "GET", "/api/drafts/"+id, nil).JSON(t))
	if m(t, d["request"])["abc"] == nil || len(m(t, d["lint"])["errors"].([]any)) != 0 {
		t.Errorf("draft %s", d)
	}
	req["abc_path"] = "../x.abc"
	b, _ = json.Marshal(req)
	resp = rawDo(t, human, "POST", "/api/drafts/batch", uploadBody(string(b)+"\n", `,"abc_files":`+string(files)), "Idempotency-Key", "a2")
	if resp.Status != 422 || !strings.Contains(string(resp.Body), "abc_path_unresolved") {
		t.Errorf("../x.abc: %d %s", resp.Status, resp.Body)
	}
}
