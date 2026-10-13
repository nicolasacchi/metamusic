package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

type m3Op struct {
	id, method, path, mcp, ui, cost string
	scope                           auth.Scope
	create, heavy, humanOnly        bool
	alias                           string
	status                          int    // success status; 0 => 200
	body                            int    // MaxBodyBytes; 0 => no body
	component                       string // the 503 UNAVAILABLE component
	lane                            string // the lane that replaces the stub
	reqBody                         string // a body that passes validation
}

const agentFaceUI = "none:agent-face (the SPA confirms through create_generation_human)"

var confirmBody = `{"confirmation_token":"mmc_` + strings.Repeat("a", 52) + `"}`

// m3Ops is the C81 table: 23 operations.
var m3Ops = []m3Op{
	{id: "estimate_generation", method: "POST", path: "/api/generations/estimate", scope: auth.ScopeGenerate, cost: "free", mcp: "tool", ui: "/human/generate", heavy: true, body: 2 * mib, component: "generation", lane: "M3-B4",
		reqBody: `{"items":[{"mode":"new_seed","parent_track_id":"t1"}]}`},
	{id: "create_generation", method: "POST", path: "/api/generations", scope: auth.ScopeGenerate, cost: "spend", mcp: "tool", ui: agentFaceUI, create: true, heavy: true, status: 201, body: 64 * kib, component: "generation", lane: "M3-B4", reqBody: confirmBody},
	{id: "create_generation_human", method: "POST", path: "/human/api/generations", scope: auth.ScopeGenerate, cost: "spend", mcp: "exclude:human-only", ui: "/human/generate", create: true, heavy: true, humanOnly: true, alias: "create_generation", status: 201, body: 64 * kib, component: "generation", lane: "M3-B4", reqBody: confirmBody},
	{id: "estimate_variant", method: "POST", path: "/api/tracks/{id}/variants/estimate", scope: auth.ScopeGenerate, cost: "free", mcp: "tool", ui: "/human/generate", heavy: true, body: 1 * mib, component: "generation", lane: "M3-B4", reqBody: `{"mode":"new_seed"}`},
	{id: "create_variant", method: "POST", path: "/api/tracks/{id}/variants", scope: auth.ScopeGenerate, cost: "spend", mcp: "tool", ui: agentFaceUI, create: true, heavy: true, status: 201, body: 64 * kib, component: "generation", lane: "M3-B4", reqBody: confirmBody},
	{id: "request_approval", method: "POST", path: "/api/batches/{id}/approval-request", scope: auth.ScopeGenerate, cost: "free", mcp: "generic", ui: "none:agent-face (humans approve directly)", component: "generation", lane: "M3-B4"},
	{id: "get_approval_status", method: "GET", path: "/api/batches/{id}/approval", scope: auth.ScopeRead, cost: "free", mcp: "generic", ui: "none:agent-face (get_batch carries the approval for the SPA)", component: "generation", lane: "M3-B4"},
	{id: "approve_batch", method: "POST", path: "/human/api/batches/{id}/approve", scope: auth.ScopeGenerate, cost: "approve", mcp: "exclude:human-only", ui: "/human/confirm/:batch", humanOnly: true, body: 64 * kib, component: "generation", lane: "M3-B4", reqBody: `{}`},
	{id: "reject_batch", method: "POST", path: "/human/api/batches/{id}/reject", scope: auth.ScopeGenerate, cost: "approve", mcp: "exclude:human-only", ui: "/human/confirm/:batch", humanOnly: true, body: 64 * kib, component: "generation", lane: "M3-B4", reqBody: `{"reason":"no"}`},
	{id: "list_batches", method: "GET", path: "/api/batches", scope: auth.ScopeRead, cost: "free", mcp: "generic", ui: "/jobs", component: "jobs", lane: "M3-B6"},
	{id: "get_batch", method: "GET", path: "/api/batches/{id}", scope: auth.ScopeRead, cost: "free", mcp: "generic", ui: "/batches/:id", component: "jobs", lane: "M3-B6"},
	{id: "cancel_batch", method: "POST", path: "/api/batches/{id}/cancel", scope: auth.ScopeWrite, cost: "free", mcp: "generic", ui: "/jobs", body: 64 * kib, component: "jobs", lane: "M3-B6", reqBody: `{}`},
	{id: "list_jobs", method: "GET", path: "/api/jobs", scope: auth.ScopeRead, cost: "free", mcp: "generic", ui: "/jobs", component: "jobs", lane: "M3-B6"},
	{id: "get_job", method: "GET", path: "/api/jobs/{id}", scope: auth.ScopeRead, cost: "free", mcp: "tool", ui: "/jobs/:id", component: "jobs", lane: "M3-B6"},
	{id: "wait_for_job", method: "GET", path: "/api/jobs/{id}/wait", scope: auth.ScopeRead, cost: "free", mcp: "tool", ui: "/jobs/:id", component: "jobs", lane: "M3-B5"},
	{id: "cancel_job", method: "POST", path: "/api/jobs/{id}/cancel", scope: auth.ScopeWrite, cost: "free", mcp: "tool", ui: "/jobs", body: 64 * kib, component: "jobs", lane: "M3-B6", reqBody: `{}`},
	{id: "get_budget", method: "GET", path: "/api/budget", scope: auth.ScopeRead, cost: "free", mcp: "generic", ui: "/jobs", component: "spend", lane: "M3-B4"},
	{id: "get_spend", method: "GET", path: "/api/spend", scope: auth.ScopeRead, cost: "free", mcp: "generic", ui: "/jobs", component: "spend", lane: "M3-B4"},
	{id: "list_ledger", method: "GET", path: "/api/ledger", scope: auth.ScopeRead, cost: "free", mcp: "generic", ui: "/jobs", component: "spend", lane: "M3-B4"},
	{id: "get_provider_status", method: "GET", path: "/api/provider", scope: auth.ScopeRead, cost: "free", mcp: "generic", ui: "/jobs", component: "provider", lane: "M3-B6"},
	{id: "set_spend_caps", method: "PUT", path: "/human/api/spend/caps", scope: auth.ScopeAdmin, cost: "free", mcp: "exclude:human-only", ui: "/human/spend", humanOnly: true, body: 64 * kib, component: "spend", lane: "M3-B4", reqBody: `{"monthly_micro_usd":1000000}`},
	{id: "update_provider_state", method: "POST", path: "/human/api/provider/state", scope: auth.ScopeAdmin, cost: "free", mcp: "exclude:human-only", ui: "/human/spend", humanOnly: true, body: 64 * kib, component: "provider", lane: "M3-B6", reqBody: `{"armed":true,"reason":"test"}`},
	{id: "get_lineage", method: "GET", path: "/api/tracks/{id}/lineage", scope: auth.ScopeRead, cost: "free", mcp: "generic", ui: "/lineage/:id", component: "lineage", lane: "M3-B4"},
}

func TestOps_RegisteredWithMetaM3(t *testing.T) {
	t.Parallel()
	if len(m3Ops) != 23 {
		t.Fatalf("the C81 table has %d rows, want 23", len(m3Ops))
	}
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	tools, generic, excluded := 0, 0, 0
	for _, w := range m3Ops {
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
		if op.Method != w.method || op.Path != w.path || m.MCP != w.mcp || m.UI != w.ui || m.Scope != w.scope || m.Cost != w.cost ||
			m.HumanOnly != w.humanOnly || m.Idempotency != idem || m.Rate != rate || m.AliasOf != w.alias || len(m.SecretFields) != 0 {
			t.Errorf("%s = %+v, want %+v", w.id, op, w)
		}
		if strings.HasPrefix(w.path, "/human/api/") != w.humanOnly {
			t.Errorf("%s: human-only and path disagree", w.id)
		}
		switch m.MCP {
		case "tool":
			tools++
		case "generic":
			generic++
		default:
			excluded++
		}
		want := w.status
		if want == 0 {
			want = 200
		}
		if o := pathOperation(a, op); o.DefaultStatus != 0 && o.DefaultStatus != want || o.DefaultStatus == 0 && want != 200 {
			t.Errorf("%s: default status %d, want %d", w.id, o.DefaultStatus, want)
		}
	}
	if tools != 7 || generic != 11 || excluded != 5 { // C81 had 11/7/5; D-M3-36 demoted four tools
		t.Errorf("tools/generic/excluded = %d/%d/%d, want 7/11/5", tools, generic, excluded)
	}
	if len(byID) != 81 {
		t.Errorf("registry holds %d operations, want 81", len(byID))
	}
}

func m3Rig(t *testing.T, d api.Deps) *rig {
	t.Helper()
	mux := http.NewServeMux()
	api.New(mux, d)
	return &rig{mux: mux}
}

func fullM3Deps() api.Deps {
	return api.Deps{
		Generation: generation.New(generation.Options{}), Spend: spend.New(spend.Options{}), Jobs: jobs.New(jobs.Options{}),
		Provider: jobs.NewProviderAdmin(jobs.ProviderOptions{}), Lineage: lineage.New(lineage.Options{}),
	}
}

func idemHdr(create bool) []string {
	if create {
		return []string{"Idempotency-Key", "k-1"}
	}
	return nil
}

func m3Call(r *rig, w m3Op) *struct {
	code int
	body map[string]any
} {
	p := agent
	if w.humanOnly {
		p = human
	}
	p.Scopes = []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}
	path := strings.NewReplacer("{id}", "x").Replace(w.path)
	rec := r.callWith(p, w.method, path, w.reqBody, idemHdr(w.create)...)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return &struct {
		code int
		body map[string]any
	}{rec.Code, m}
}

func TestOps_StubsAnswer501(t *testing.T) {
	t.Parallel()
	r := m3Rig(t, fullM3Deps())
	for _, w := range m3Ops {
		res := m3Call(r, w)
		d, _ := res.body["details"].(map[string]any)
		if res.code != 501 || res.body["code"] != "NOT_IMPLEMENTED" || d["stub"] != w.lane {
			t.Errorf("%s: %d %v, want 501 NOT_IMPLEMENTED stub %s", w.id, res.code, res.body, w.lane)
		}
	}
}

func TestOps_NilDepsUnavailableM3(t *testing.T) {
	t.Parallel()
	r := m3Rig(t, api.Deps{}) // zero Deps must register every op without panicking
	seen := map[string]bool{}
	for _, w := range m3Ops {
		res := m3Call(r, w)
		d, _ := res.body["details"].(map[string]any)
		if res.code != 503 || res.body["code"] != "UNAVAILABLE" || d["component"] != w.component {
			t.Errorf("%s: %d %v, want 503 UNAVAILABLE component %s", w.id, res.code, res.body, w.component)
		}
		seen[w.component] = true
	}
	for _, c := range []string{"generation", "spend", "jobs", "provider", "lineage"} {
		if !seen[c] {
			t.Errorf("no operation answers for component %s", c)
		}
	}
}

func TestOps_BodyLimitsM3(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	const gateMax = 8 * mib
	for _, w := range m3Ops {
		o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path})
		if o.MaxBodyBytes != int64(w.body) || o.MaxBodyBytes > gateMax {
			t.Errorf("%s: MaxBodyBytes = %d, want %d", w.id, o.MaxBodyBytes, w.body)
		}
		if hasBody := o.RequestBody != nil; hasBody != (w.body != 0) {
			t.Errorf("%s: request body = %v but limit = %d", w.id, hasBody, w.body)
		}
	}
}

func TestOps_AuthorizationM3(t *testing.T) {
	t.Parallel()
	r := m3Rig(t, fullM3Deps())
	readOnlyAgent := auth.Principal{Kind: auth.KindAgent, ID: "tok-ro", Name: "ro", Scopes: []auth.Scope{auth.ScopeRead}, Via: "token"}
	for _, w := range m3Ops {
		if w.scope == auth.ScopeRead {
			continue
		}
		path := strings.NewReplacer("{id}", "x").Replace(w.path)
		rec := r.callWith(readOnlyAgent, w.method, path, w.reqBody, idemHdr(w.create)...)
		if rec.Code != 403 {
			t.Errorf("%s with read scope only: %d, want 403", w.id, rec.Code)
		}
	}
	// human-only ops refuse every non-human principal, even with all scopes
	full := auth.Principal{Kind: auth.KindAgent, ID: "tok-all", Name: "all", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}, Via: "token"}
	for _, w := range m3Ops {
		if !w.humanOnly {
			continue
		}
		path := strings.NewReplacer("{id}", "x").Replace(w.path)
		if rec := r.callWith(full, w.method, path, w.reqBody, idemHdr(w.create)...); rec.Code != 403 {
			t.Errorf("%s as an agent: %d, want 403", w.id, rec.Code)
		}
	}
}

func TestNullablePatch_SchemasM3(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	b, err := api.OpenAPIJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	patch, ok := spec.Components.Schemas["Patch"]
	if !ok {
		t.Fatal("Patch schema missing")
	}
	for field, want := range map[string]string{"abc": `["string","null"]`, "cfg_scale": `["number","null"]`} {
		var s struct {
			Type json.RawMessage `json:"type"`
		}
		if err := json.Unmarshal(patch.Properties[field], &s); err != nil || compact(s.Type) != want {
			t.Errorf("Patch.%s type = %s (%v), want %s", field, s.Type, err, want)
		}
	}
	// REST: abc must be a string or null
	r := m3Rig(t, fullM3Deps())
	gen := auth.Principal{Kind: auth.KindAgent, ID: "tok-g", Name: "gen", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}, Via: "token"}
	rec := r.callWith(gen, "POST", "/api/tracks/x/variants/estimate", `{"mode":"lyric_edit","patch":{"abc":5}}`)
	if rec.Code != 422 {
		t.Errorf("abc: 5 => %d, want 422", rec.Code)
	}
	var s []string
	for _, ok := range []string{`{"mode":"lyric_edit","patch":{"abc":null}}`, `{"mode":"lyric_edit","patch":{"abc":"X:1"}}`, `{"mode":"style_edit","patch":{"cfg_scale":null}}`} {
		rec := r.callWith(gen, "POST", "/api/tracks/x/variants/estimate", ok)
		if rec.Code == 422 || rec.Code == 400 {
			s = append(s, ok)
		}
	}
	if len(s) != 0 {
		t.Errorf("valid patches refused: %v", s)
	}
}

func compact(b []byte) string {
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		return string(b)
	}
	return out.String()
}

func TestNullableValues_RoundTrip(t *testing.T) {
	t.Parallel()
	var p api.Patch
	if err := json.Unmarshal([]byte(`{"abc":null,"cfg_scale":1.5,"style":"x"}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.ABC.Set || !p.ABC.Null || !p.CFGScale.Set || p.CFGScale.Null || p.CFGScale.Value != 1.5 || p.Style == nil || *p.Style != "x" {
		t.Errorf("decoded %+v", p)
	}
	var absent api.Patch
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil || absent.ABC.Set || absent.CFGScale.Set {
		t.Errorf("absent keys decoded as set: %+v %v", absent, err)
	}
	out, err := json.Marshal(absent)
	if err != nil || string(out) != `{}` {
		t.Errorf("unset values must be omitted: %s %v", out, err)
	}
}

// TestOps_DescriptionsShortM3: D-M3-36 shortens the M3 descriptions to 200 characters; they are part of
// every agent's tools/list.
func TestOps_DescriptionsShortM3(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, w := range m3Ops {
		o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path})
		if n := len([]rune(o.Description)); n > 200 {
			t.Errorf("%s: description is %d characters, want <= 200", w.id, n)
		}
	}
}
