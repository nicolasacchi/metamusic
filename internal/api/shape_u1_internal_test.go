package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/view"
)

type shapeSong struct {
	ID    string    `json:"id"`
	Muse  shapeMuse `json:"muse"`
	Seed  int64     `json:"seed"`
	Cost  *string   `json:"cost_display_usd"`
	Draft *string   `json:"draft"`
}
type shapeMuse struct {
	MuseID *string `json:"muse_id"`
}
type shapeOut struct{ Body shapeSong }

// shapeRig serves a fake get_song (a scope listen op whose rules are shapeRules["get_song"]) and a fake plain op that has no
// rules, to the principal named by the X-Role header.
func shapeRig(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	a := humago.New(mux, HumaConfig())
	muse, cost, draft := "m1", "0.5", "d1"
	h := func(ctx context.Context, _ *struct{}) (*shapeOut, error) {
		return &shapeOut{Body: shapeSong{ID: "s1", Muse: shapeMuse{MuseID: &muse}, Seed: 9007199254740993, Cost: &cost, Draft: &draft}}, nil
	}
	op := func(id, path string) huma.Operation {
		return huma.Operation{OperationID: id, Method: "GET", Path: path, Summary: id, Description: id}
	}
	Register(a, op("get_song", "/api/t/song"), Meta{Scope: auth.ScopeListen, Cost: "free", MCP: "generic", UI: "none:test"}, h)
	Register(a, op("get_stats", "/api/t/stats"), Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "none:test"}, h)
	Register(a, op("t_plain", "/api/t/plain"), Meta{Scope: auth.ScopeListen, Cost: "free", MCP: "generic", UI: "none:test"}, h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := map[string]auth.Principal{
			"owner":    {Kind: auth.KindHuman, ID: "alice", Role: auth.RoleOwner, Scopes: auth.ScopesFor(auth.RoleOwner)},
			"editor":   {Kind: auth.KindHuman, ID: "bob", User: "bob", Role: auth.RoleEditor, Scopes: auth.ScopesFor(auth.RoleEditor)},
			"listener": {Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener, Scopes: auth.ScopesFor(auth.RoleListener)},
			"agent":    {Kind: auth.KindAgent, ID: "t1", Scopes: []auth.Scope{auth.ScopeRead}},
		}[r.Header.Get("X-Role")]
		mux.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

func shapeGet(h http.Handler, role, path string) (int, string) {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("X-Role", role)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, strings.TrimSpace(w.Body.String())
}

// TestShapeTransformer pins the response step: the Owner side gets the typed body byte for byte, an Editor loses money, a
// Listener loses money, Muse and private keys (absent, not null), an op without rules is only shaped by the common set, and
// an error body is never rewritten.
func TestShapeTransformer(t *testing.T) {
	t.Parallel()
	h := shapeRig(t)
	const full = `{"id":"s1","muse":{"muse_id":"m1"},"seed":9007199254740993,"cost_display_usd":"0.5","draft":"d1"}`
	for _, role := range []string{"owner", "agent"} {
		if code, body := shapeGet(h, role, "/api/t/song"); role == "owner" && (code != 200 || body != full) {
			t.Errorf("%s: %d %s", role, code, body)
		}
	}
	if code, body := shapeGet(h, "editor", "/api/t/song"); code != 200 || body != `{"draft":"d1","id":"s1","muse":{"muse_id":"m1"},"seed":9007199254740993}` {
		t.Errorf("editor: %d %s", code, body)
	}
	code, body := shapeGet(h, "listener", "/api/t/song")
	if code != 200 || strings.Contains(body, "seed") || strings.Contains(body, "cost") || strings.Contains(body, "muse_id") || strings.Contains(body, "draft") || !strings.Contains(body, `"id":"s1"`) {
		t.Errorf("listener: %d %s", code, body)
	}
	// get_stats is scope read: an Editor sheds nothing of this body (its Editor list names other keys); a Listener has no scope
	if code, body := shapeGet(h, "editor", "/api/t/stats"); code != 200 || !strings.Contains(body, "cost_display_usd") || !strings.Contains(body, "muse_id") {
		t.Errorf("editor stats: %d %s", code, body)
	}
	if code, _ := shapeGet(h, "listener", "/api/t/stats"); code != 403 {
		t.Errorf("listener stats: %d", code)
	}
	// an op with no per-operation rules still gets the common Listener set (seed, money) and keeps Muse structure
	if code, body := shapeGet(h, "listener", "/api/t/plain"); code != 200 || strings.Contains(body, "seed") || strings.Contains(body, "cost") || !strings.Contains(body, "muse_id") {
		t.Errorf("plain: %d %s", code, body)
	}
	// an error is not rewritten
	if code, body := shapeGet(h, "listener", "/api/t/nothing"); code != 404 || strings.Contains(body, "seed") {
		t.Errorf("not found: %d %s", code, body)
	}
}

func TestShapeTransformerGuards(t *testing.T) {
	t.Parallel()
	if v, err := shapeTransformer(nil, "404", "x"); v != "x" || err != nil {
		t.Errorf("non-2xx: %v %v", v, err)
	}
	if v, err := shapeTransformer(nil, "", "x"); v != "x" || err != nil {
		t.Errorf("empty status: %v %v", v, err)
	}
	if v, err := shapeTransformer(nil, "200", nil); v != nil || err != nil {
		t.Errorf("nil body: %v %v", v, err)
	}
}

func TestRulesFor(t *testing.T) {
	t.Parallel()
	r := rulesFor("get_song", Meta{Scope: auth.ScopeListen})
	if !r.MoneyKeys || len(r.Private) <= len(shapeRules["get_song"].Private) {
		t.Errorf("a listen op lacks the common set: %+v", r)
	}
	if len(shapeRules["get_song"].Private) == len(r.Private) {
		t.Errorf("rulesFor mutated or ignored the table")
	}
	if r := rulesFor("get_stats", Meta{Scope: auth.ScopeRead}); r.MoneyKeys || len(r.Editor) == 0 {
		t.Errorf("a read op got the Listener set: %+v", r)
	}
	// every op the rules name is registered, so a rename cannot leave a rule behind
	ops := map[string]bool{}
	for _, op := range Operations(New(http.NewServeMux(), Deps{Version: "x"})) {
		ops[op.ID] = true
	}
	for id := range shapeRules {
		if !ops[id] {
			t.Errorf("shape rules name %q, which is not an operation", id)
		}
	}
}

// TestRelaxResponseSchemas pins that the spec says what the wire does: a key a role may not receive is not required in the
// response schemas that carry it, an owner-only operation's schema keeps its required keys, and a component that a request
// also uses is not loosened.
func TestRelaxResponseSchemas(t *testing.T) {
	t.Parallel()
	a := New(http.NewServeMux(), Deps{Version: "x"})
	schemas := a.OpenAPI().Components.Schemas.Map()
	required := func(comp, key string) bool {
		s := schemas[comp]
		if s == nil {
			t.Fatalf("no component %s", comp)
		}
		return slices.Contains(s.Required, key)
	}
	for comp, keys := range map[string][]string{
		"TrackSummary": {"seed", "request_id", "critic_stars", "cost_display_usd", "comments"},
		"TrackDetail":  {"generation", "import", "playlists", "listening"},
		"SongView":     {"muse", "draft", "proposals", "events", "sessions"},
		"SongSummary":  {"muse_id", "draft_id", "queued_jobs"},
		"Crate":        {"filter"},
	} {
		for _, k := range keys {
			if required(comp, k) {
				t.Errorf("%s.%s is still required", comp, k)
			}
		}
	}
	if !required("TrackSummary", "title") || !required("TrackSummary", "id") {
		t.Errorf("a key no role loses became optional")
	}
	if !required("LadderItem", "seed") {
		t.Errorf("the owner-only ladder lost a required key")
	}
	// a request component keeps its required list
	inRequest := map[string]bool{}
	for _, item := range a.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Put, item.Post, item.Patch} {
			if op != nil && op.RequestBody != nil {
				for _, mt := range op.RequestBody.Content {
					markComponents(a.OpenAPI().Components.Schemas, mt.Schema, inRequest, map[*huma.Schema]bool{})
				}
			}
		}
	}
	if len(inRequest) == 0 {
		t.Fatalf("no request components found")
	}
	reg := a.OpenAPI().Components.Schemas
	s := &huma.Schema{Properties: map[string]*huma.Schema{"seed": {}, "x": {}}, Required: []string{"seed", "x"}}
	dropRequired(s, "InputThing", map[string]bool{"InputThing": true}, "seed")
	if len(s.Required) != 2 {
		t.Errorf("a request component was loosened: %v", s.Required)
	}
	dropRequired(s, "Out", map[string]bool{"InputThing": true}, "seed")
	if len(s.Required) != 1 {
		t.Errorf("a response component was not loosened: %v", s.Required)
	}
	// cycles and a missing target do not loop or panic
	loop := &huma.Schema{Properties: map[string]*huma.Schema{}}
	loop.Properties["self"] = loop
	relaxPath(reg, map[string]bool{}, loop, []string{"**", "nope"}, 0)
	relaxWhere(reg, map[string]bool{}, loop, view.MoneyKey, map[*huma.Schema]bool{})
	markComponents(reg, loop, map[string]bool{}, map[*huma.Schema]bool{})
	relaxPath(reg, map[string]bool{}, nil, []string{"a"}, 0)
}

// TestRulesForEditorWritesAndRecipeSources pins the review fixes: the write-scope ops an Editor reaches that return money
// shed it, and a Listener's get_track names neither the source take of an edit recipe nor its master or clip hash.
func TestRulesForEditorWritesAndRecipeSources(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"update_track", "create_playlist", "update_playlist"} {
		if r := rulesFor(id, Meta{Scope: auth.ScopeWrite}); !r.MoneyKeys {
			t.Errorf("%s keeps money for an Editor", id)
		}
	}
	body := map[string]any{"id": "t1", "recipe": map[string]any{"acestep": map[string]any{
		"task": "repaint",
		"src":  map[string]any{"track_id": "gone", "master_sha256": "abc", "duration_s": 1.5},
		"ref":  map[string]any{"track_id": "gone", "clip_sha256": "def", "from_s": 0.0},
	}}}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener})
	out, err := view.FromContext(ctx).Shape(body, rulesFor("get_track", Meta{Scope: auth.ScopeListen}))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	for _, leak := range []string{"gone", "abc", "def", "master_sha256", "clip_sha256"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("listener get_track keeps %q: %s", leak, raw)
		}
	}
	if !strings.Contains(string(raw), "duration_s") {
		t.Errorf("shaping removed more than the rules name: %s", raw)
	}
}
