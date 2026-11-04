package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/attention"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/callbacks"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/songpr"
	"github.com/nicolasacchi/metamusic/internal/voice"
)

// m5Op is one row of the C260 table: nine operations, all cost free.
type m5Op struct {
	id, method, path, mcp, ui string
	scope                     auth.Scope
	create                    bool
	status                    int    // success status; 0 => 200
	body                      int    // MaxBodyBytes; 0 => no body
	component                 string // the 503 UNAVAILABLE component
	task                      string // the lane that replaces the stub (details.task of the 501)
	reqBody                   string // a body that passes validation
	query                     string // a query string that passes validation
}

const m5Voice = 6 << 20

// The C260 table, frozen by M5-F2. No M5 operation is a dedicated MCP tool (operator decision, L2/L3 precedent):
// eight are generic, create_voice_note is excluded (a human recording, box B5).
var m5Ops = []m5Op{
	{id: "propose_patch", method: "POST", path: "/api/patches", scope: auth.ScopeWrite, mcp: "generic", ui: "/tracks/:id", create: true, status: 201, body: 64 * kib, component: "songpr", task: "M5-S1",
		reqBody: `{"base_track_id":"t","addresses_comment_ids":["c"],"rationale":"r","ops":[{"op":"set_style","style":"x"}]}`},
	{id: "get_patch", method: "GET", path: "/api/patches/{id}", scope: auth.ScopeRead, mcp: "generic", ui: "/patches/:id", component: "songpr", task: "M5-S1"},
	{id: "list_patches", method: "GET", path: "/api/patches", scope: auth.ScopeRead, mcp: "generic", ui: "/tracks/:id", component: "songpr", task: "M5-S1"},
	{id: "list_callbacks", method: "GET", path: "/api/callbacks", scope: auth.ScopeRead, mcp: "generic", ui: "/judge/callbacks", component: "callbacks", task: "M5-C1"},
	{id: "get_callback", method: "GET", path: "/api/callbacks/{id}", scope: auth.ScopeRead, mcp: "generic", ui: "/judge/callbacks/:id", component: "callbacks", task: "M5-C1"},
	{id: "submit_callback_verdict", method: "POST", path: "/api/callbacks/{id}/verdicts", scope: auth.ScopeWrite, mcp: "generic", ui: "/judge/callbacks/:id", status: 201, body: 64 * kib, component: "callbacks", task: "M5-C1",
		reqBody: `{"verdict":"fixed"}`},
	{id: "get_listen_heat", method: "GET", path: "/api/tracks/{id}/heat", scope: auth.ScopeRead, mcp: "generic", ui: "/tracks/:id", component: "attention", task: "M5-A1"},
	{id: "suggest_duel_pairs", method: "GET", path: "/api/duels/suggestions", scope: auth.ScopeRead, mcp: "generic", ui: "/duels", component: "duels", task: "M5-D1"},
	{id: "create_voice_note", method: "POST", path: "/api/tracks/{id}/voice-notes", scope: auth.ScopeWrite, mcp: "exclude:human-recording", ui: "/tracks/:id", create: true, status: 201, body: m5Voice, component: "voice", task: "M5-V1",
		reqBody: `{"audio_base64":"AAAA"}`},
}

// implementedM5 merges the per-lane tables (ops_<lane>_m5_test.go): a lane that takes an operation over adds it to its own map,
// so parallel lanes never edit the same lines.
func implementedM5() map[string]bool {
	all := map[string]bool{}
	for _, m := range []map[string]bool{implementedSongPRM5, implementedCallbacksM5, implementedAttentionM5, implementedDuelsM5, implementedVoiceM5} {
		for id := range m {
			all[id] = true
		}
	}
	return all
}

func m5Deps() api.Deps {
	return api.Deps{SongPR: songpr.New(songpr.Options{}), Callbacks: callbacks.New(callbacks.Options{}), Attention: attention.New(attention.Options{}),
		Voice: voice.New(voice.Options{}), Duels: duels.New(duels.Options{})}
}

func m5Call(r *rig, p auth.Principal, w m5Op) (int, map[string]any) {
	path := strings.NewReplacer("{id}", "x").Replace(w.path)
	if w.query != "" {
		path += "?" + w.query
	}
	rec := r.callWith(p, w.method, path, w.reqBody, idemHdr(w.create)...)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func m5Rig(t *testing.T, d api.Deps) *rig {
	t.Helper()
	mux := http.NewServeMux()
	api.New(mux, d)
	return &rig{mux: mux}
}

func m5Agent(scopes ...auth.Scope) auth.Principal {
	return auth.Principal{Kind: auth.KindAgent, ID: "tok-m5", Name: "m5", Scopes: scopes, Via: "token"}
}

// TestOps_RegisteredWithMetaM5: the exact Meta of the C260 table: scopes, costs, the MCP faces (0 tools, 8 generic, 1 excluded
// as a human recording), the UI routes, the create flags, the default statuses, and human_only false for all.
func TestOps_RegisteredWithMetaM5(t *testing.T) {
	t.Parallel()
	if len(m5Ops) != 9 {
		t.Fatalf("the C260 table has %d rows, want 9", len(m5Ops))
	}
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	tools, generic, excluded := 0, 0, 0
	for _, w := range m5Ops {
		op, ok := byID[w.id]
		if !ok {
			t.Errorf("%s not registered", w.id)
			continue
		}
		m := op.Meta
		idem := ""
		if w.create {
			idem = "create"
		}
		if op.Method != w.method || op.Path != w.path || m.MCP != w.mcp || m.UI != w.ui || m.Scope != w.scope || m.Cost != "free" ||
			m.HumanOnly || m.Idempotency != idem || m.Rate != "" || m.AliasOf != "" || len(m.SecretFields) != 0 || m.MCPImage {
			t.Errorf("%s = %+v, want %+v", w.id, op, w)
		}
		switch m.MCP {
		case "tool":
			tools++
		case "generic":
			generic++
		default:
			excluded++
			if m.MCP != "exclude:human-recording" {
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
	if tools != 0 || generic != 8 || excluded != 1 {
		t.Errorf("tools/generic/excluded = %d/%d/%d, want 0/8/1", tools, generic, excluded)
	}
}

// TestM5_NoSpendOps: no M5 operation spends or asks for approval; a Song PR renders through the existing M3 paths (D-M5-R3-3).
func TestM5_NoSpendOps(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	ids := map[string]bool{}
	for _, w := range m5Ops {
		ids[w.id] = true
	}
	n := 0
	for _, op := range api.Operations(a) {
		if ids[op.ID] {
			n++
			if op.Meta.Cost != "free" {
				t.Errorf("%s has cost %q: no M5 operation spends", op.ID, op.Meta.Cost)
			}
		}
	}
	if n != 9 {
		t.Errorf("found %d M5 operations in the registry, want 9", n)
	}
}

// TestOps_BodyLimitsM5: the body cap of each write operation (64 KiB, 6 MiB for a voice note); reads have no body.
func TestOps_BodyLimitsM5(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, w := range m5Ops {
		o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path})
		if o.MaxBodyBytes != int64(w.body) {
			t.Errorf("%s: MaxBodyBytes = %d, want %d", w.id, o.MaxBodyBytes, w.body)
		}
		if hasBody := o.RequestBody != nil; hasBody != (w.body != 0) {
			t.Errorf("%s: request body = %v but limit = %d", w.id, hasBody, w.body)
		}
	}
}

// TestOps_StubsAnswer501M5: an operation whose lane has not landed answers 501 NOT_IMPLEMENTED naming the lane (details.task).
// A lane that takes an operation over adds it to its own implemented<Lane>M5 map (ops_<lane>_m5_test.go) and covers the behaviour
// in its own tests.
func TestOps_StubsAnswer501M5(t *testing.T) {
	t.Parallel()
	r := m5Rig(t, m5Deps())
	done := implementedM5()
	for _, w := range m5Ops {
		if done[w.id] {
			continue
		}
		code, body := m5Call(r, m5Agent(auth.ScopeRead, auth.ScopeWrite), w)
		d, _ := body["details"].(map[string]any)
		if code != 501 || body["code"] != "NOT_IMPLEMENTED" || d["task"] != w.task {
			t.Errorf("%s: %d %v, want 501 NOT_IMPLEMENTED task %s", w.id, code, body, w.task)
		}
	}
}

// TestOps_NilDepsUnavailableM5: with zero Deps every op registers and answers 503 UNAVAILABLE naming its component.
func TestOps_NilDepsUnavailableM5(t *testing.T) {
	t.Parallel()
	r := m5Rig(t, api.Deps{})
	seen := map[string]bool{}
	for _, w := range m5Ops {
		code, body := m5Call(r, m5Agent(auth.ScopeRead, auth.ScopeWrite), w)
		d, _ := body["details"].(map[string]any)
		if code != 503 || body["code"] != "UNAVAILABLE" || d["component"] != w.component {
			t.Errorf("%s: %d %v, want 503 UNAVAILABLE component %s", w.id, code, body, w.component)
		}
		seen[w.component] = true
	}
	for _, c := range []string{"songpr", "callbacks", "attention", "duels", "voice"} {
		if !seen[c] {
			t.Errorf("no operation answers for component %s", c)
		}
	}
}

// TestOps_AuthorizationM5: a read-only agent cannot call a write op, and anonymous callers get 401.
func TestOps_AuthorizationM5(t *testing.T) {
	t.Parallel()
	r := m5Rig(t, m5Deps())
	for _, w := range m5Ops {
		if w.scope != auth.ScopeRead {
			if code, _ := m5Call(r, m5Agent(auth.ScopeRead), w); code != 403 {
				t.Errorf("%s with read scope only: %d, want 403", w.id, code)
			}
		}
		if code, _ := m5Call(r, auth.Principal{}, w); code != 401 {
			t.Errorf("%s anonymous: %d, want 401", w.id, code)
		}
	}
}

// TestOps_M5Schemas: the frozen wire shapes are in the generated spec, including the fields M5 adds to existing shapes.
func TestOps_M5Schemas(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	schemas := a.OpenAPI().Components.Schemas.Map()
	for _, n := range []string{"SongPR", "SongPRSummary", "PatchOp", "PatchDiff", "PatchScope", "Callback", "CallbackVerdict", "CallbackSubmitResult",
		"CallbackPage", "ListenHeat", "ListenHotspot", "PairSuggestions", "VoiceNote", "DuelSectionSelector", "DuelSection", "DuelSlotWindow"} {
		if _, ok := schemas[n]; !ok {
			t.Errorf("schema %s is missing", n)
		}
	}
	has := func(schema, prop string) bool {
		s, ok := schemas[schema]
		if !ok {
			return false
		}
		_, ok = s.Properties[prop]
		return ok
	}
	for _, c := range [][2]string{{"ItemIn", "patch_id"}, {"Comment", "voice"}, {"Comment", "origin_callback_id"}, {"Duel", "section"}, {"Slot", "window"}} {
		if !has(c[0], c[1]) {
			t.Errorf("%s lost the M5 property %s", c[0], c[1])
		}
	}
	if s, ok := schemas["ItemIn"]; ok {
		if p := s.Properties["mode"]; p == nil || !containsEnum(p.Enum, "song_pr") {
			t.Error("ItemIn.mode does not accept song_pr")
		}
	}
}

func containsEnum(enum []any, v string) bool {
	for _, e := range enum {
		if e == v {
			return true
		}
	}
	return false
}
