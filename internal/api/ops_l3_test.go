package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/sequence"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// l3Op is one row of the C142 table: 7 operations, all cost free.
type l3Op struct {
	id, method, path, mcp, ui string
	scope                     auth.Scope
	create, heavy             bool
	status                    int    // success status; 0 => 200
	body                      int    // MaxBodyBytes; 0 => no body
	component                 string // the 503 UNAVAILABLE component
	task                      string // the lane that replaces the stub (details.task of the 501)
	reqBody                   string // a body that passes validation
}

// The C142 table. The operator decided on 2026-11-01 that L3 adds no dedicated MCP tool (D-L2-21 precedent): the plan's two
// tools (list_next_actions, propose_taxonomy) are generic. map_sound_term is unchanged: D-L3-22 is not approved.
var l3Ops = []l3Op{
	{id: "suggest_sequence", method: "GET", path: "/api/sequence", scope: auth.ScopeRead, mcp: "generic", ui: "/records/:id", component: "sequence", task: "L3-SR"},
	{id: "list_next_actions", method: "GET", path: "/api/next-actions", scope: auth.ScopeRead, mcp: "generic", ui: "/", component: "nextactions", task: "L3-NA1"},
	{id: "propose_taxonomy", method: "POST", path: "/api/taxonomy-proposals", scope: auth.ScopeWrite, mcp: "generic", ui: "none:agent-face (a human maps terms directly with map_sound_term)", create: true, heavy: true, status: 201, body: 64 * kib, component: "sounds", task: "L3-TP2",
		reqBody: `{"rationale":"r","mappings":[{"term":"t","facet":"mood","value":"chill","op":"add"}]}`},
	{id: "list_taxonomy_proposals", method: "GET", path: "/api/taxonomy-proposals", scope: auth.ScopeRead, mcp: "generic", ui: "/sounds", component: "sounds", task: "L3-TP2"},
	{id: "decide_taxonomy_proposal", method: "POST", path: "/api/taxonomy-proposals/{id}/decision", scope: auth.ScopeWrite, mcp: "exclude:human-judgment", ui: "/sounds", body: 64 * kib, component: "sounds", task: "L3-TP2",
		reqBody: `{"decision":"reject"}`},
	{id: "get_song_atlas", method: "GET", path: "/api/atlas", scope: auth.ScopeRead, mcp: "generic", ui: "/atlas", component: "atlas", task: "L3-AT2"},
	{id: "get_adherence", method: "GET", path: "/api/atlas/adherence", scope: auth.ScopeRead, mcp: "generic", ui: "/atlas", component: "atlas", task: "L3-AD"},
}

// implementedL3 lists the operations whose stub a lane has replaced (the lane adds its ids here and its own handler tests
// cover the behaviour).
var implementedL3 = map[string]bool{"suggest_sequence": true, "list_next_actions": true, "propose_taxonomy": true, "list_taxonomy_proposals": true, "decide_taxonomy_proposal": true, "get_adherence": true, "get_song_atlas": true}

func l3Deps() api.Deps {
	return api.Deps{
		Sequence: sequence.New(nil), NextActions: nextactions.New(nil, nil, nil, nil, nil), Atlas: atlas.New(nil, nil), Features: features.New(nil, nil),
		Sounds: sounds.New(nil, nil, nil), Records: records.New(nil, nil, nil), RecordView: recordview.New(recordview.Options{}),
	}
}

func l3Call(r *rig, p auth.Principal, w l3Op) (int, map[string]any) {
	path := strings.NewReplacer("{id}", "x").Replace(w.path)
	rec := r.callWith(p, w.method, path, w.reqBody, idemHdr(w.create)...)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func l3Rig(t *testing.T, d api.Deps) *rig {
	t.Helper()
	mux := http.NewServeMux()
	api.New(mux, d)
	return &rig{mux: mux}
}

func l3Agent(scopes ...auth.Scope) auth.Principal {
	return auth.Principal{Kind: auth.KindAgent, ID: "tok-l3", Name: "l3", Scopes: scopes, Via: "token"}
}

// TestOps_RegisteredWithMetaL3: the exact Meta of the C142 table: scopes, costs, the MCP faces (0 tools, 6 generic, 1
// excluded as human judgment), the UI routes, the create and heavy flags, the default statuses, and human_only false for all.
func TestOps_RegisteredWithMetaL3(t *testing.T) {
	t.Parallel()
	if len(l3Ops) != 7 {
		t.Fatalf("the C142 table has %d rows, want 7", len(l3Ops))
	}
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	tools, generic, excluded, withUI := 0, 0, 0, 0
	for _, w := range l3Ops {
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
			m.HumanOnly || m.Idempotency != idem || m.Rate != rate || m.AliasOf != "" || len(m.SecretFields) != 0 || m.MCPImage {
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
		if !strings.HasPrefix(m.MCP, "exclude:") && strings.HasPrefix(m.UI, "/") {
			withUI++
		}
		want := w.status
		if want == 0 {
			want = 200
		}
		if o := pathOperation(a, op); o.DefaultStatus != 0 && o.DefaultStatus != want || o.DefaultStatus == 0 && want != 200 {
			t.Errorf("%s: default status %d, want %d", w.id, o.DefaultStatus, want)
		}
	}
	// The plan had 2 tools and 4 generic; the operator decided (2026-11-01) that L3 adds no dedicated MCP tool.
	if tools != 0 || generic != 6 || excluded != 1 {
		t.Errorf("tools/generic/excluded = %d/%d/%d, want 0/6/1", tools, generic, excluded)
	}
	if withUI != 5 { // propose_taxonomy has a none: reason (D-L3-38) and decide is excluded, so 5 ops carry an MCP face and a UI route
		t.Errorf("%d ops have an MCP face and a UI route, want 5", withUI)
	}
	// D-L3-22 is not approved: agents keep map_sound_term.
	if m := byID["map_sound_term"].Meta; m.MCP != "generic" || m.UI != "/sounds" {
		t.Errorf("map_sound_term Meta = %+v, want the unchanged generic face", m)
	}
}

// TestL3_NoSpendOps: no L3 operation spends or asks for approval (D-L3-18).
func TestL3_NoSpendOps(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	ids := map[string]bool{}
	for _, w := range l3Ops {
		ids[w.id] = true
	}
	n := 0
	for _, op := range api.Operations(a) {
		if ids[op.ID] {
			n++
			if op.Meta.Cost != "free" {
				t.Errorf("%s has cost %q: no L3 operation spends", op.ID, op.Meta.Cost)
			}
		}
	}
	if n != 7 {
		t.Errorf("found %d L3 operations in the registry, want 7", n)
	}
}

// TestOps_BodyLimitsL3: propose_taxonomy and decide_taxonomy_proposal are bodySmall (64 KiB); the rest have no body.
func TestOps_BodyLimitsL3(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, w := range l3Ops {
		o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path})
		if o.MaxBodyBytes != int64(w.body) {
			t.Errorf("%s: MaxBodyBytes = %d, want %d", w.id, o.MaxBodyBytes, w.body)
		}
		if hasBody := o.RequestBody != nil; hasBody != (w.body != 0) {
			t.Errorf("%s: request body = %v but limit = %d", w.id, hasBody, w.body)
		}
	}
}

// TestOps_StubsAnswer501L3: until its lane lands, every handler answers 501 NOT_IMPLEMENTED naming the lane (details.task).
// A lane that takes an operation over adds it to implementedL3.
func TestOps_StubsAnswer501L3(t *testing.T) {
	t.Parallel()
	r := l3Rig(t, l3Deps())
	for _, w := range l3Ops {
		if implementedL3[w.id] {
			continue
		}
		code, body := l3Call(r, l3Agent(auth.ScopeRead, auth.ScopeWrite), w)
		d, _ := body["details"].(map[string]any)
		if code != 501 || body["code"] != "NOT_IMPLEMENTED" || d["task"] != w.task {
			t.Errorf("%s: %d %v, want 501 NOT_IMPLEMENTED task %s", w.id, code, body, w.task)
		}
	}
}

// TestOps_NilDepsUnavailableL3: with zero Deps every op registers and answers 503 UNAVAILABLE naming its component.
func TestOps_NilDepsUnavailableL3(t *testing.T) {
	t.Parallel()
	r := l3Rig(t, api.Deps{})
	seen := map[string]bool{}
	for _, w := range l3Ops {
		code, body := l3Call(r, l3Agent(auth.ScopeRead, auth.ScopeWrite), w)
		d, _ := body["details"].(map[string]any)
		if code != 503 || body["code"] != "UNAVAILABLE" || d["component"] != w.component {
			t.Errorf("%s: %d %v, want 503 UNAVAILABLE component %s", w.id, code, body, w.component)
		}
		seen[w.component] = true
	}
	for _, c := range []string{"sequence", "nextactions", "sounds", "atlas"} {
		if !seen[c] {
			t.Errorf("no operation answers for component %s", c)
		}
	}
}

// TestOps_AuthorizationL3: a read-only agent cannot call a write op, and anonymous callers get 401.
func TestOps_AuthorizationL3(t *testing.T) {
	t.Parallel()
	r := l3Rig(t, l3Deps())
	for _, w := range l3Ops {
		if w.scope != auth.ScopeRead {
			if code, _ := l3Call(r, l3Agent(auth.ScopeRead), w); code != 403 {
				t.Errorf("%s with read scope only: %d, want 403", w.id, code)
			}
		}
		if code, _ := l3Call(r, auth.Principal{}, w); code != 401 {
			t.Errorf("%s anonymous: %d, want 401", w.id, code)
		}
	}
}

// TestEditSlots_UnknownOpEnum: the slot op enum of the schema rejects an unknown op before the handler runs; the sequence op
// itself is covered by TestEditRecordSlots_Sequence_REST.
func TestEditSlots_UnknownOpEnum(t *testing.T) {
	t.Parallel()
	r := l3Rig(t, l3Deps())
	p := l3Agent(auth.ScopeRead, auth.ScopeWrite)
	if bad := r.callWith(p, "POST", "/api/records/x/slots", `{"revision":1,"ops":[{"op":"bogus"}]}`); bad.Code != 422 {
		t.Errorf("an unknown op: %d, want 422 from the schema enum", bad.Code)
	}
}

// TestOps_L3Schemas: the frozen wire shapes are in the generated spec (get_song.features, the record sequence parts and the
// four new outputs), and the existing include enum accepts sequence.
func TestOps_L3Schemas(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	schemas := a.OpenAPI().Components.Schemas.Map()
	for _, n := range []string{"Suggestion", "SequenceExplanation", "NextActionsPage", "TaxonomyProposal", "DecideResult", "Atlas", "Adherence"} {
		if _, ok := schemas[n]; !ok {
			t.Errorf("schema %s is missing", n)
		}
	}
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	o := pathOperation(a, byID["get_record"])
	ok := false
	for _, p := range o.Parameters {
		if p.Name == "include" && p.Schema != nil && p.Schema.Items != nil {
			for _, e := range p.Schema.Items.Enum {
				ok = ok || e == "sequence"
			}
		}
	}
	if !ok {
		t.Error("get_record include does not accept sequence")
	}
}
