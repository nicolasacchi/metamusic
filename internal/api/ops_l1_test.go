package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/board"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/songview"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

type l1Op struct {
	id, method, path, mcp, ui string
	scope                     auth.Scope
	create, heavy             bool
	status                    int    // success status; 0 => 200
	body                      int    // MaxBodyBytes; 0 => no body
	component                 string // the 503 UNAVAILABLE component
	lane                      string // the lane that replaces the stub
	reqBody                   string // a body that passes validation
}

const (
	l1AgentFace = "none:agent-face (the Sound browser lists Songs through search_sounds)"
	l1Proposal  = "none:agent-face (a human pins directly with pin_pick)"
)

// l1Ops is the C96 table: 18 operations.
var l1Ops = []l1Op{
	{id: "list_muses", method: "GET", path: "/api/muses", scope: auth.ScopeRead, mcp: "tool", ui: "/board", component: "muses", lane: "L1-B5"},
	{id: "get_muse", method: "GET", path: "/api/muses/{id}", scope: auth.ScopeRead, mcp: "tool", ui: "/muses/:id", component: "muses", lane: "L1-B5"},
	{id: "create_muse", method: "POST", path: "/api/muses", scope: auth.ScopeWrite, mcp: "tool", ui: "/board", create: true, status: 201, body: 64 * kib, component: "muses", lane: "L1-B5",
		reqBody: `{"kind":"reference","title":"Synthetic Muse 001"}`},
	{id: "update_muse", method: "PATCH", path: "/api/muses/{id}", scope: auth.ScopeWrite, mcp: "generic", ui: "/muses/:id", body: 64 * kib, component: "muses", lane: "L1-B5", reqBody: `{"revision":1,"title":"x"}`},
	{id: "assign_muse_songs", method: "POST", path: "/api/muses/{id}/songs", scope: auth.ScopeWrite, mcp: "generic", ui: "/board", body: 64 * kib, component: "muses", lane: "L1-B5", reqBody: `{"song_ids":["s1"]}`},
	{id: "list_songs", method: "GET", path: "/api/songs", scope: auth.ScopeRead, mcp: "generic", ui: l1AgentFace, component: "songs", lane: "L1-B5"},
	{id: "get_song", method: "GET", path: "/api/songs/{id}", scope: auth.ScopeRead, mcp: "tool", ui: "/songs/:id", component: "songview", lane: "L1-B5"},
	{id: "update_song", method: "PATCH", path: "/api/songs/{id}", scope: auth.ScopeWrite, mcp: "generic", ui: "/songs/:id", body: 64 * kib, component: "songs", lane: "L1-B5", reqBody: `{"revision":1,"title":"x"}`},
	{id: "propose_pick", method: "POST", path: "/api/songs/{id}/pick-proposals", scope: auth.ScopeWrite, mcp: "tool", ui: l1Proposal, create: true, status: 201, body: 64 * kib, component: "picks", lane: "L1-B4",
		reqBody: `{"take_id":"t1","rationale":"why"}`},
	{id: "pin_pick", method: "PUT", path: "/api/songs/{id}/pin", scope: auth.ScopeWrite, mcp: "exclude:human-judgment", ui: "/songs/:id", body: 64 * kib, component: "picks", lane: "L1-B4", reqBody: `{"take_id":null}`},
	{id: "dismiss_pick_proposal", method: "POST", path: "/api/pick-proposals/{id}/dismiss", scope: auth.ScopeWrite, mcp: "exclude:human-judgment", ui: "/songs/:id", component: "picks", lane: "L1-B4"},
	{id: "list_pick_events", method: "GET", path: "/api/pick-events", scope: auth.ScopeRead, mcp: "generic", ui: "/songs/:id", component: "picks", lane: "L1-B4"},
	{id: "list_sessions", method: "GET", path: "/api/sessions", scope: auth.ScopeRead, mcp: "generic", ui: "/board", component: "sessions", lane: "L1-B5"},
	{id: "update_session", method: "PATCH", path: "/api/sessions/{id}", scope: auth.ScopeWrite, mcp: "generic", ui: "/board", body: 64 * kib, component: "sessions", lane: "L1-B5", reqBody: `{"revision":1,"name":"x"}`},
	{id: "search_sounds", method: "GET", path: "/api/sounds/search", scope: auth.ScopeRead, mcp: "tool", ui: "/sounds", component: "sounds", lane: "L1-B6"},
	{id: "get_sound_taxonomy", method: "GET", path: "/api/sounds/taxonomy", scope: auth.ScopeRead, mcp: "generic", ui: "/sounds", component: "sounds", lane: "L1-B6"},
	{id: "map_sound_term", method: "POST", path: "/api/sound-terms", scope: auth.ScopeWrite, mcp: "generic", ui: "/sounds", heavy: true, body: 64 * kib, component: "sounds", lane: "L1-B6",
		reqBody: `{"mappings":[{"term":"dub","facet":"genre_family","value":"reggae","op":"add"}]}`},
	{id: "get_muse_board", method: "GET", path: "/api/board", scope: auth.ScopeRead, mcp: "generic", ui: "/board", component: "board", lane: "L1-B5"},
}

func l1Deps() api.Deps {
	return api.Deps{
		Muses: muses.New(nil, nil, nil), Songs: songs.New(nil, nil, nil), SongView: songview.New(nil, nil, nil, nil, nil, nil, nil, nil, nil),
		Sessions: sessions.New(nil, nil), Picks: picks.New(nil, nil, nil, nil), Sounds: sounds.New(nil, nil, nil), Board: board.New(nil, nil),
	}
}

func l1Rig(t *testing.T, d api.Deps) *rig {
	t.Helper()
	mux := http.NewServeMux()
	api.New(mux, d)
	return &rig{mux: mux}
}

func TestOps_RegisteredWithMetaL1(t *testing.T) {
	t.Parallel()
	if len(l1Ops) != 18 {
		t.Fatalf("the C96 table has %d rows, want 18", len(l1Ops))
	}
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	tools, generic, excluded := 0, 0, 0
	for _, w := range l1Ops {
		op, ok := byID[w.id]
		if !ok {
			t.Errorf("%s not registered", w.id)
			continue
		}
		m := op.Meta
		idem, rate := "", ""
		if w.create {
			idem = "create"
		}
		if w.heavy {
			rate = "heavy"
		}
		if op.Method != w.method || op.Path != w.path || m.MCP != w.mcp || m.UI != w.ui || m.Scope != w.scope || m.Cost != "free" ||
			m.HumanOnly || m.Idempotency != idem || m.Rate != rate || m.AliasOf != "" || len(m.SecretFields) != 0 {
			t.Errorf("%s = %+v, want %+v", w.id, op, w)
		}
		switch m.MCP {
		case "tool":
			tools++
		case "generic":
			generic++
		default:
			excluded++
			if m.MCP != "exclude:human-judgment" {
				t.Errorf("%s: exclusion reason %q", w.id, m.MCP)
			}
		}
		want := w.status
		if want == 0 {
			want = 200
		}
		if o := pathOperation(a, op); o.DefaultStatus != 0 && o.DefaultStatus != want || o.DefaultStatus == 0 && want != 200 {
			t.Errorf("%s: default status %d, want %d", w.id, o.DefaultStatus, want)
		}
	}
	if tools != 6 || generic != 10 || excluded != 2 { // list_songs demoted by L1-B5 (D-L1-20)
		t.Errorf("tools/generic/excluded = %d/%d/%d, want 6/10/2 (D-L1-20)", tools, generic, excluded)
	}
}

func TestL1_NoSpendOps(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, w := range l1Ops {
		for _, op := range api.Operations(a) {
			if op.ID == w.id && op.Meta.Cost != "free" {
				t.Errorf("%s has cost %q: no L1 operation spends (D-L1-29)", w.id, op.Meta.Cost)
			}
		}
	}
}

func TestOps_BodyLimitsL1(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, w := range l1Ops {
		o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path})
		if o.MaxBodyBytes != int64(w.body) {
			t.Errorf("%s: MaxBodyBytes = %d, want %d", w.id, o.MaxBodyBytes, w.body)
		}
		if hasBody := o.RequestBody != nil; hasBody != (w.body != 0) {
			t.Errorf("%s: request body = %v but limit = %d", w.id, hasBody, w.body)
		}
	}
}

func l1Call(r *rig, w l1Op) (int, map[string]any) {
	p := agent
	p.Scopes = []auth.Scope{auth.ScopeRead, auth.ScopeWrite}
	if strings.Contains(w.mcp, "human") { // the human-judgment ops answer for the operator too
		p = human
	}
	path := strings.NewReplacer("{id}", "x").Replace(w.path)
	rec := r.callWith(p, w.method, path, w.reqBody, idemHdr(w.create)...)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

// TestOps_StubsAnswer501L1: until its lane lands, every handler answers 501 NOT_IMPLEMENTED naming the lane (details.task).
func TestOps_StubsAnswer501L1(t *testing.T) {
	t.Parallel()
	r := l1Rig(t, l1Deps())
	for _, w := range l1Ops {
		if implementedL1[w.id] { // a lane replaced the stub: its behaviour is covered by that lane's tests
			continue
		}
		code, body := l1Call(r, w)
		d, _ := body["details"].(map[string]any)
		if code != 501 || body["code"] != "NOT_IMPLEMENTED" || d["task"] != w.lane {
			t.Errorf("%s: %d %v, want 501 NOT_IMPLEMENTED task %s", w.id, code, body, w.lane)
		}
	}
}

// implementedL1 lists the operations a lane has taken over from their stub; each lane adds its ids here.
var implementedL1 = map[string]bool{
	// L1-B4 (Picks)
	"propose_pick": true, "pin_pick": true, "dismiss_pick_proposal": true, "list_pick_events": true,
	// L1-B5 (Library)
	"list_muses": true, "get_muse": true, "create_muse": true, "update_muse": true, "assign_muse_songs": true,
	"list_songs": true, "get_song": true, "update_song": true, "list_sessions": true, "update_session": true, "get_muse_board": true,
}

func TestOps_NilDepsUnavailableL1(t *testing.T) {
	t.Parallel()
	r := l1Rig(t, api.Deps{}) // zero Deps must register every op without panicking
	seen := map[string]bool{}
	for _, w := range l1Ops {
		code, body := l1Call(r, w)
		d, _ := body["details"].(map[string]any)
		if code != 503 || body["code"] != "UNAVAILABLE" || d["component"] != w.component {
			t.Errorf("%s: %d %v, want 503 UNAVAILABLE component %s", w.id, code, body, w.component)
		}
		seen[w.component] = true
	}
	for _, c := range []string{"muses", "songs", "songview", "sessions", "picks", "sounds", "board"} {
		if !seen[c] {
			t.Errorf("no operation answers for component %s", c)
		}
	}
}

func TestOps_AuthorizationL1(t *testing.T) {
	t.Parallel()
	r := l1Rig(t, l1Deps())
	ro := auth.Principal{Kind: auth.KindAgent, ID: "tok-ro", Name: "ro", Scopes: []auth.Scope{auth.ScopeRead}, Via: "token"}
	for _, w := range l1Ops {
		if w.scope == auth.ScopeRead {
			continue
		}
		path := strings.NewReplacer("{id}", "x").Replace(w.path)
		if rec := r.callWith(ro, w.method, path, w.reqBody, idemHdr(w.create)...); rec.Code != 403 {
			t.Errorf("%s with read scope only: %d, want 403", w.id, rec.Code)
		}
	}
}
