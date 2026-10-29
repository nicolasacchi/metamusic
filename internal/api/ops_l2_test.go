package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// l2Op is one row of the C129 table: 17 operations, all cost free.
type l2Op struct {
	id, method, path, mcp, ui string
	scope                     auth.Scope
	create, heavy, humanOnly  bool
	status                    int    // success status; 0 => 200
	body                      int    // MaxBodyBytes; 0 => no body
	component                 string // the 503 UNAVAILABLE component
	task                      string // the lane that replaces the stub (details.task of the 501)
	reqBody                   string // a body that passes validation
}

var l2Ops = []l2Op{
	{id: "list_records", method: "GET", path: "/api/records", scope: auth.ScopeRead, mcp: "generic", ui: "/records", component: "records", task: "L2-V2"},
	{id: "get_record", method: "GET", path: "/api/records/{id}", scope: auth.ScopeRead, mcp: "generic", ui: "/records/:id", component: "records", task: "L2-V2"},
	{id: "create_record", method: "POST", path: "/api/records", scope: auth.ScopeWrite, mcp: "generic", ui: "/records", create: true, status: 201, body: 64 * kib, component: "records", task: "L2-V2", reqBody: `{"title":"Synthetic Record 001"}`},
	{id: "update_record", method: "PATCH", path: "/api/records/{id}", scope: auth.ScopeWrite, mcp: "generic", ui: "/records/:id", body: 64 * kib, component: "records", task: "L2-V2", reqBody: `{"revision":1,"title":"x"}`},
	{id: "edit_record_slots", method: "POST", path: "/api/records/{id}/slots", scope: auth.ScopeWrite, mcp: "generic", ui: "/records/:id", body: 64 * kib, component: "records", task: "L2-V2", reqBody: `{"revision":1,"ops":[{"op":"pin_all"}]}`},
	{id: "get_record_changes", method: "GET", path: "/api/records/{id}/changes", scope: auth.ScopeRead, mcp: "generic", ui: "/records/:id", component: "records", task: "L2-V2"},
	{id: "propose_record", method: "POST", path: "/api/records/{id}/proposal", scope: auth.ScopeWrite, mcp: "generic", ui: "/records/:id", body: 64 * kib, component: "records", task: "L2-V2", reqBody: `{"revision":1,"rationale":"ready"}`},
	{id: "publish_record", method: "POST", path: "/human/api/records/{id}/publish", scope: auth.ScopeWrite, mcp: "exclude:human-only", ui: "/human/records/:id/publish", humanOnly: true, body: 64 * kib, component: "records", task: "L2-V2", reqBody: `{"revision":1,"published":true}`},
	{id: "export_record_tape", method: "POST", path: "/api/records/{id}/tapes", scope: auth.ScopeWrite, mcp: "generic", ui: "/records/:id", create: true, heavy: true, status: 201, body: 64 * kib, component: "tape", task: "L2-T2", reqBody: `{}`},
	{id: "get_tape", method: "GET", path: "/api/tapes/{id}", scope: auth.ScopeRead, mcp: "generic", ui: "/records/:id", component: "tape", task: "L2-T2"},
	{id: "list_crates", method: "GET", path: "/api/crates", scope: auth.ScopeRead, mcp: "generic", ui: "/crates", component: "crates", task: "L2-K"},
	{id: "get_crate", method: "GET", path: "/api/crates/{id}", scope: auth.ScopeRead, mcp: "generic", ui: "/crates/:id", component: "crates", task: "L2-K"},
	{id: "create_crate", method: "POST", path: "/api/crates", scope: auth.ScopeWrite, mcp: "generic", ui: "/crates", create: true, status: 201, body: 64 * kib, component: "crates", task: "L2-K", reqBody: `{"name":"Synthetic Crate 001","kind":"manual"}`},
	{id: "update_crate", method: "PATCH", path: "/api/crates/{id}", scope: auth.ScopeWrite, mcp: "generic", ui: "/crates/:id", body: 64 * kib, component: "crates", task: "L2-K", reqBody: `{"revision":1,"name":"x"}`},
	{id: "edit_crate_items", method: "POST", path: "/api/crates/{id}/items", scope: auth.ScopeWrite, mcp: "generic", ui: "/crates/:id", body: 64 * kib, component: "crates", task: "L2-K", reqBody: `{"add":[{"song_id":"s1"}]}`},
	{id: "get_curation_settings", method: "GET", path: "/api/curation/settings", scope: auth.ScopeRead, mcp: "generic", ui: "/settings/curation", component: "curation", task: "L2-A"},
	{id: "set_curation_settings", method: "PUT", path: "/human/api/curation/settings", scope: auth.ScopeAdmin, mcp: "exclude:human-only", ui: "/human/curation", humanOnly: true, body: 64 * kib, component: "curation", task: "L2-A", reqBody: `{"export_muse_names":true}`},
}

// implementedL2 lists the operations whose stub a lane has replaced (the lane adds its ids here and the handler tests of
// that lane cover the behaviour).
var implementedL2 = map[string]bool{
	"list_crates": true, "get_crate": true, "create_crate": true, "update_crate": true, "edit_crate_items": true, // L2-K
	"get_curation_settings": true, "set_curation_settings": true, // L2-A
	"list_records": true, "get_record": true, "create_record": true, "update_record": true, "edit_record_slots": true,
	"get_record_changes": true, "propose_record": true, "publish_record": true, // L2-V2
	"export_record_tape": true, "get_tape": true, // L2-T2
}

func l2Deps() api.Deps {
	return api.Deps{
		Records: records.New(nil, nil, nil), RecordView: recordview.New(recordview.Options{}), Crates: crates.New(nil, nil, nil, nil),
		Tapes: tape.New(tape.Options{}), Curation: curation.New(nil, nil),
	}
}

func l2Rig(t *testing.T, d api.Deps) *rig {
	t.Helper()
	mux := http.NewServeMux()
	api.New(mux, d)
	return &rig{mux: mux}
}

func l2Path(w l2Op) string { return strings.NewReplacer("{id}", "x").Replace(w.path) }

func l2Call(r *rig, p auth.Principal, w l2Op) (int, map[string]any) {
	rec := r.callWith(p, w.method, l2Path(w), w.reqBody, idemHdr(w.create)...)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

// l2Agent is an agent with read and write scope; the human-only ops must refuse it whatever scopes it holds.
func l2Agent(scopes ...auth.Scope) auth.Principal {
	return auth.Principal{Kind: auth.KindAgent, ID: "tok-l2", Name: "l2", Scopes: scopes, Via: "token"}
}

// TestOps_RegisteredWithMetaL2: the exact Meta of the C129 table: scopes, costs, the MCP faces (5 tools, 10 generic, 2
// excluded as human-only), the UI routes, the create and heavy flags, the default statuses, and human_only for exactly two.
func TestOps_RegisteredWithMetaL2(t *testing.T) {
	t.Parallel()
	if len(l2Ops) != 17 {
		t.Fatalf("the C129 table has %d rows, want 17", len(l2Ops))
	}
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	tools, generic, excluded, humanOnly, withUI := 0, 0, 0, 0, 0
	for _, w := range l2Ops {
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
			m.HumanOnly != w.humanOnly || m.Idempotency != idem || m.Rate != rate || m.AliasOf != "" || len(m.SecretFields) != 0 || m.MCPImage {
			t.Errorf("%s = %+v, want %+v", w.id, op, w)
		}
		if strings.HasPrefix(w.path, "/human/api/") != w.humanOnly {
			t.Errorf("%s: human_only and path disagree", w.id)
		}
		switch m.MCP {
		case "tool":
			tools++
		case "generic":
			generic++
		default:
			excluded++
			if m.MCP != "exclude:human-only" {
				t.Errorf("%s: exclusion reason %q", w.id, m.MCP)
			}
		}
		if m.HumanOnly {
			humanOnly++
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
	// D-L2-21 planned 5 tools and 10 generic; the operator decided (2026-10-28) that L2 adds no dedicated MCP tool: 0 tools, 15 generic.
	if tools != 0 || generic != 15 || excluded != 2 || humanOnly != 2 {
		t.Errorf("tools/generic/excluded/human-only = %d/%d/%d/%d, want 0/15/2/2 (D-L2-21 as decided)", tools, generic, excluded, humanOnly)
	}
	if withUI != 15 { // every op with an MCP face has a UI route: the 15 parity cases and every_ui_action entries
		t.Errorf("%d ops have an MCP face and a UI route, want 15", withUI)
	}
}

// TestL2_NoSpendOps: no L2 operation spends or asks for approval (D-L2-22).
func TestL2_NoSpendOps(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	ids := map[string]bool{}
	for _, w := range l2Ops {
		ids[w.id] = true
	}
	n := 0
	for _, op := range api.Operations(a) {
		if ids[op.ID] {
			n++
			if op.Meta.Cost != "free" {
				t.Errorf("%s has cost %q: no L2 operation spends (D-L2-22)", op.ID, op.Meta.Cost)
			}
		}
	}
	if n != 17 {
		t.Errorf("found %d L2 operations in the registry, want 17", n)
	}
}

// TestOps_BodyLimitsL2: every L2 op with a body is bodySmall (64 KiB) and an op without one has no request body.
func TestOps_BodyLimitsL2(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, w := range l2Ops {
		o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path})
		if o.MaxBodyBytes != int64(w.body) {
			t.Errorf("%s: MaxBodyBytes = %d, want %d", w.id, o.MaxBodyBytes, w.body)
		}
		if hasBody := o.RequestBody != nil; hasBody != (w.body != 0) {
			t.Errorf("%s: request body = %v but limit = %d", w.id, hasBody, w.body)
		}
	}
}

// TestOps_ToolDescriptionsShortL2: the first-class tools keep their descriptions at 200 characters (their schemas
// count against the tools/list ceiling).
func TestOps_ToolDescriptionsShortL2(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, w := range l2Ops {
		if w.mcp != "tool" {
			continue
		}
		o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path})
		if n := len([]rune(o.Description)); n > 200 {
			t.Errorf("%s: description is %d characters, want at most 200", w.id, n)
		}
	}
}

// TestOps_StubsAnswer501L2: until its lane lands, every handler answers 501 NOT_IMPLEMENTED naming the lane (details.task).
// An operation a lane takes over is added to implementedL2 by that lane.
func TestOps_StubsAnswer501L2(t *testing.T) {
	t.Parallel()
	r := l2Rig(t, l2Deps())
	for _, w := range l2Ops {
		if implementedL2[w.id] {
			continue
		}
		p := l2Agent(auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
		if w.humanOnly {
			p = human
		}
		code, body := l2Call(r, p, w)
		d, _ := body["details"].(map[string]any)
		if code != 501 || body["code"] != "NOT_IMPLEMENTED" || d["task"] != w.task {
			t.Errorf("%s: %d %v, want 501 NOT_IMPLEMENTED task %s", w.id, code, body, w.task)
		}
	}
}

// TestOps_NilDepsUnavailableL2: with zero Deps every op registers and answers 503 UNAVAILABLE naming its component.
func TestOps_NilDepsUnavailableL2(t *testing.T) {
	t.Parallel()
	r := l2Rig(t, api.Deps{})
	seen := map[string]bool{}
	for _, w := range l2Ops {
		p := l2Agent(auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
		if w.humanOnly {
			p = human
		}
		code, body := l2Call(r, p, w)
		d, _ := body["details"].(map[string]any)
		if code != 503 || body["code"] != "UNAVAILABLE" || d["component"] != w.component {
			t.Errorf("%s: %d %v, want 503 UNAVAILABLE component %s", w.id, code, body, w.component)
		}
		seen[w.component] = true
	}
	for _, c := range []string{"records", "crates", "tape", "curation"} {
		if !seen[c] {
			t.Errorf("no operation answers for component %s", c)
		}
	}
}

// TestOps_AuthorizationL2: a read-only agent cannot call a write or admin op; the two human-only ops refuse every
// non-human principal with 403 HUMAN_ONLY before any dependency is touched (agents with every scope, a paired device, an
// OAuth connector, a Subsonic client, the system) and let the operator through to the stub.
func TestOps_AuthorizationL2(t *testing.T) {
	t.Parallel()
	r := l2Rig(t, l2Deps())
	all := []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}
	ro := l2Agent(auth.ScopeRead)
	nonHuman := map[string]auth.Principal{
		"agent with every scope": l2Agent(all...),
		"paired device":          {Kind: auth.KindDevice, ID: "dev", Name: "phone", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "device"},
		"oauth connector":        {Kind: auth.KindAgent, ID: "grant", Name: "Claude", Scopes: all, Via: "oauth", Client: "test-claude"},
		"subsonic client":        {Kind: auth.KindDevice, ID: "sub", Name: "app", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "subsonic"},
		"system":                 auth.System("runner"),
	}
	for _, w := range l2Ops {
		if w.scope != auth.ScopeRead {
			if code, _ := l2Call(r, ro, w); code != 403 {
				t.Errorf("%s with read scope only: %d, want 403", w.id, code)
			}
		}
		if !w.humanOnly {
			continue
		}
		for name, p := range nonHuman {
			code, body := l2Call(r, p, w)
			if code != 403 || body["code"] != "HUMAN_ONLY" {
				t.Errorf("%s as %s: %d %v, want 403 HUMAN_ONLY", w.id, name, code, body)
			}
		}
		// the operator reaches the handler; an implemented op needs a real store, so ops_records_l2_test.go covers it end to end
		if !implementedL2[w.id] {
			if code, _ := l2Call(r, human, w); code != 501 {
				t.Errorf("%s as the operator: %d, want 501 from the stub", w.id, code)
			}
		}
		if code, _ := l2Call(r, auth.Principal{}, w); code != 401 {
			t.Errorf("%s anonymous: %d, want 401", w.id, code)
		}
	}
}

// TestOps_L2FilterParamsInSpec: the new parameters are in the generated schema of the three list operations, so the API
// surface they add is frozen by F2 and never changes in a lane.
func TestOps_L2FilterParamsInSpec(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	want := map[string][]string{
		"list_songs":    {"pick_decided", "unheard", "min_duration_s", "max_duration_s", "min_stars", "unrated", "tag", "lossy", "recipe_known", "in_record", "crate_id"},
		"search_sounds": {"pick_decided", "unheard", "min_duration_s", "max_duration_s", "min_stars", "unrated", "tag", "lossy", "recipe_known", "in_record", "crate_id"},
		"search_tracks": {"unheard", "in_record", "sound", "crate_id"},
	}
	for id, names := range want {
		o := pathOperation(a, byID[id])
		have := map[string]bool{}
		for _, p := range o.Parameters {
			if p.In == "query" {
				have[p.Name] = true
			}
		}
		for _, n := range names {
			if !have[n] {
				t.Errorf("%s lacks the query parameter %s", id, n)
			}
		}
	}
}

// TestOptSide_Decode: the slot-op side is absent, null or A to D; anything else is a 422 from the schema.
func TestOptSide_Decode(t *testing.T) {
	t.Parallel()
	var op struct {
		Side api.OptSide `json:"side"`
	}
	for _, tc := range []struct {
		in         string
		set, null  bool
		val        string
		wantFailed bool
	}{
		{in: `{}`},
		{in: `{"side":null}`, set: true, null: true},
		{in: `{"side":"A"}`, set: true, val: "A"},
		{in: `{"side":"D"}`, set: true, val: "D"},
		{in: `{"side":"E"}`, wantFailed: true},
		{in: `{"side":1}`, wantFailed: true},
	} {
		op.Side = api.OptSide{}
		err := json.Unmarshal([]byte(tc.in), &op)
		if (err != nil) != tc.wantFailed {
			t.Errorf("%s: err = %v", tc.in, err)
			continue
		}
		if err == nil && (op.Side.Set != tc.set || op.Side.Null != tc.null || op.Side.Value != tc.val) {
			t.Errorf("%s: %+v", tc.in, op.Side)
		}
	}
	r := l2Rig(t, api.Deps{}) // no services: a body the schema accepts reaches the handler and answers 503
	call := func(side string) int {
		body := `{"revision":1,"ops":[{"op":"set_side","slot_id":"s","side":` + side + `}]}`
		return r.callWith(l2Agent(auth.ScopeRead, auth.ScopeWrite), "POST", "/api/records/x/slots", body).Code
	}
	for _, s := range []string{`"A"`, `"D"`, `null`} {
		if c := call(s); c != 503 {
			t.Errorf("side %s: %d, want 503 from the handler (the schema accepts it)", s, c)
		}
	}
	for _, s := range []string{`"E"`, `"a"`, `1`, `""`} {
		if c := call(s); c != 422 {
			t.Errorf("side %s: %d, want 422", s, c)
		}
	}
}

// TestOps_L2SchemaNames: no wire shape of L2 collides with a name another package registered in the one OpenAPI namespace.
func TestOps_L2SchemaNames(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	schemas := a.OpenAPI().Components.Schemas.Map()
	for _, n := range []string{"Record", "RecordView", "RecordSummary", "SlotView", "ChangeView", "Tape", "TapeBrief", "Crate", "CrateSummary", "CrateDetail", "ItemsResult", "CurationSettings", "RecordStats", "CrateStats", "TapeStats"} {
		if _, ok := schemas[n]; !ok {
			t.Errorf("schema %s is missing", n)
		}
	}
}
