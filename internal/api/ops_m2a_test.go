package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

type m2aOp struct {
	id, method, path, mcp, ui string
	scope                     auth.Scope
	create, heavy             bool
	secrets                   []string
	humanOnly                 bool
	status                    int // success status; 0 => 200
	body                      int // MaxBodyBytes; 0 => no body
}

const (
	kib = 1 << 10
	mib = 1 << 20
)

// m2aOps is the C38 table: 32 operations.
var m2aOps = []m2aOp{
	{id: "rate_track", method: "PUT", path: "/api/tracks/{id}/rating", mcp: "tool", ui: "/tracks/:id", scope: auth.ScopeWrite, body: 64 * kib},
	{id: "update_track", method: "PATCH", path: "/api/tracks/{id}", mcp: "generic", ui: "/tracks/:id", scope: auth.ScopeWrite, body: 64 * kib},
	{id: "set_tags", method: "PATCH", path: "/api/tracks/{id}/tags", mcp: "tool", ui: "/tracks/:id", scope: auth.ScopeWrite, body: 64 * kib},
	{id: "list_tags", method: "GET", path: "/api/tags", mcp: "generic", ui: "/", scope: auth.ScopeRead},
	{id: "add_comment", method: "POST", path: "/api/tracks/{id}/comments", mcp: "tool", ui: "/tracks/:id", scope: auth.ScopeWrite, create: true, status: 201, body: 64 * kib},
	{id: "update_comment", method: "PATCH", path: "/api/comments/{id}", mcp: "tool", ui: "/tracks/:id", scope: auth.ScopeWrite, body: 64 * kib},
	{id: "list_comments", method: "GET", path: "/api/tracks/{id}/comments", mcp: "generic", ui: "/tracks/:id", scope: auth.ScopeRead},
	{id: "get_track_context", method: "GET", path: "/api/tracks/{id}/context", mcp: "tool", ui: "/tracks/:id", scope: auth.ScopeRead},
	{id: "log_listen_events", method: "POST", path: "/api/listen-events", mcp: "generic", ui: "/tracks/:id", scope: auth.ScopeWrite, body: 256 * kib},
	{id: "lint_recipe", method: "POST", path: "/api/recipes/lint", mcp: "tool", ui: "/backlog/:id", scope: auth.ScopeRead, body: 2 * mib},
	{id: "create_draft", method: "POST", path: "/api/drafts", mcp: "tool", ui: "/backlog/new", scope: auth.ScopeWrite, create: true, status: 201, body: 2 * mib},
	{id: "update_draft", method: "PATCH", path: "/api/drafts/{id}", mcp: "generic", ui: "/backlog/:id", scope: auth.ScopeWrite, body: 2 * mib},
	{id: "upload_batch_jsonl", method: "POST", path: "/api/drafts/batch", mcp: "generic", ui: "/backlog/upload", scope: auth.ScopeWrite, create: true, heavy: true, status: 201, body: 6 * mib},
	{id: "list_playlists", method: "GET", path: "/api/playlists", mcp: "tool", ui: "/playlists", scope: auth.ScopeRead},
	{id: "get_playlist", method: "GET", path: "/api/playlists/{id}", mcp: "generic", ui: "/playlists/:id", scope: auth.ScopeRead},
	{id: "create_playlist", method: "POST", path: "/api/playlists", mcp: "tool", ui: "/playlists", scope: auth.ScopeWrite, create: true, status: 201, body: 64 * kib},
	{id: "update_playlist", method: "PATCH", path: "/api/playlists/{id}", mcp: "generic", ui: "/playlists/:id", scope: auth.ScopeWrite, body: 64 * kib},
	{id: "export_playlist_zip", method: "POST", path: "/api/playlists/{id}/exports", mcp: "generic", ui: "/playlists/:id", scope: auth.ScopeWrite, create: true, heavy: true, status: 201},
	{id: "list_export_sources", method: "GET", path: "/api/exports/sources", mcp: "generic", ui: "/export", scope: auth.ScopeRead},
	{id: "export_jsonl", method: "POST", path: "/api/exports/jsonl", mcp: "tool", ui: "/export", scope: auth.ScopeWrite, create: true, heavy: true, status: 201, body: 64 * kib},
	{id: "get_export", method: "GET", path: "/api/exports/{id}", mcp: "generic", ui: "/export", scope: auth.ScopeRead},
	{id: "create_backup", method: "POST", path: "/api/backups", mcp: "generic", ui: "/settings/backups", scope: auth.ScopeWrite, create: true, heavy: true, status: 202},
	{id: "list_backups", method: "GET", path: "/api/backups", mcp: "generic", ui: "/settings/backups", scope: auth.ScopeRead},
	{id: "get_audit", method: "GET", path: "/api/audit", mcp: "generic", ui: "/settings/audit", scope: auth.ScopeRead},
	{id: "list_tokens", method: "GET", path: "/api/tokens", mcp: "generic", ui: "/human/tokens", scope: auth.ScopeRead},
	{id: "revoke_own_token", method: "POST", path: "/api/tokens/self/revoke", mcp: "generic", ui: "none:agent-self-service", scope: auth.ScopeRead},
	{id: "create_token", method: "POST", path: "/human/api/tokens", mcp: "exclude:human-only", ui: "/human/tokens", scope: auth.ScopeAdmin, humanOnly: true, create: true, secrets: []string{"token"}, status: 201, body: 64 * kib},
	{id: "revoke_token", method: "POST", path: "/human/api/tokens/{id}/revoke", mcp: "exclude:human-only", ui: "/human/tokens", scope: auth.ScopeAdmin, humanOnly: true},
	{id: "list_devices", method: "GET", path: "/human/api/devices", mcp: "exclude:human-only", ui: "/human/devices", scope: auth.ScopeRead, humanOnly: true},
	{id: "create_pairing_code", method: "POST", path: "/human/api/devices/pairing-codes", mcp: "exclude:human-only", ui: "/human/devices", scope: auth.ScopeAdmin, humanOnly: true, create: true, secrets: []string{"code", "pair_url"}, status: 201, body: 64 * kib},
	{id: "revoke_device", method: "POST", path: "/human/api/devices/{id}/revoke", mcp: "exclude:human-only", ui: "/human/devices", scope: auth.ScopeAdmin, humanOnly: true},
	{id: "revoke_own_device", method: "POST", path: "/api/devices/self/revoke", mcp: "exclude:device-self-service", ui: "/settings", scope: auth.ScopeWrite},
}

func newTestAPI(t *testing.T) (huma.API, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	return api.New(mux, api.Deps{}), mux
}

func TestOps_M2aRegisteredWithMeta(t *testing.T) {
	t.Parallel()
	if len(m2aOps) != 32 {
		t.Fatalf("the C38 table has %d rows, want 32", len(m2aOps))
	}
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	tools, generic, excluded := 0, 0, 0
	for _, w := range m2aOps {
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
		rate := ""
		if w.heavy {
			rate = "heavy"
		}
		if op.Method != w.method || op.Path != w.path || m.MCP != w.mcp || m.UI != w.ui || m.Scope != w.scope || m.Cost != "free" ||
			m.HumanOnly != w.humanOnly || m.Idempotency != idem || m.Rate != rate || !slices.Equal(m.SecretFields, w.secrets) {
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
		o := pathOperation(a, op)
		if o.DefaultStatus != 0 && o.DefaultStatus != want {
			t.Errorf("%s: default status %d, want %d", w.id, o.DefaultStatus, want)
		}
		if o.DefaultStatus == 0 && want != 200 {
			t.Errorf("%s: default status unset, want %d", w.id, want)
		}
	}
	// C41 demoted five tools to generic to hold the tools/list ceiling.
	if tools != 10 || generic != 16 || excluded != 6 {
		t.Errorf("tools/generic/excluded = %d/%d/%d, want 10/16/6", tools, generic, excluded)
	}
	if byID["revoke_own_device"].Meta.MCP != "exclude:device-self-service" {
		t.Error("revoke_own_device must be excluded as device self-service")
	}
}

func pathOperation(a huma.API, op api.OpInfo) *huma.Operation {
	item := a.OpenAPI().Paths[op.Path]
	return map[string]*huma.Operation{"GET": item.Get, "PUT": item.Put, "POST": item.Post, "DELETE": item.Delete, "PATCH": item.Patch}[op.Method]
}

func TestOps_CreateOpsDeclareIdempotencyKey(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, op := range api.Operations(a) {
		o := pathOperation(a, op)
		var found *huma.Param
		for _, p := range o.Parameters {
			if p.In == "header" && p.Name == "Idempotency-Key" {
				found = p
			}
		}
		if op.Meta.Idempotency != "create" {
			if found != nil {
				t.Errorf("%s declares Idempotency-Key but is not a create op", op.ID)
			}
			continue
		}
		if found == nil || !found.Required {
			t.Errorf("%s: create op must require the Idempotency-Key header", op.ID)
			continue
		}
		if s := a.OpenAPI().Components.Schemas.SchemaFromRef(found.Schema.Ref); found.Schema.Ref != "" && s == nil {
			t.Errorf("%s: dangling schema", op.ID)
		}
		if found.Schema.MaxLength == nil || *found.Schema.MaxLength != 255 {
			t.Errorf("%s: Idempotency-Key maxLength = %v, want 255", op.ID, found.Schema.MaxLength)
		}
	}
}

func TestOps_BodyLimits(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	const gateMax = 8 * mib // gate.defaultMaxBody
	for _, w := range m2aOps {
		o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path})
		if o.MaxBodyBytes != int64(w.body) {
			t.Errorf("%s: MaxBodyBytes = %d, want %d", w.id, o.MaxBodyBytes, w.body)
		}
		if o.MaxBodyBytes > gateMax {
			t.Errorf("%s: MaxBodyBytes %d exceeds the gate cap", w.id, o.MaxBodyBytes)
		}
		hasBody := o.RequestBody != nil
		if hasBody != (w.body != 0) {
			t.Errorf("%s: request body = %v but limit = %d", w.id, hasBody, w.body)
		}
	}
}

func TestOps_RawRequestSchema(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	b, err := api.OpenAPIJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "application/octet-stream") {
		t.Error("no operation may declare an application/octet-stream body")
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	n := 0
	for name, raw := range schemas {
		props, _ := raw.(map[string]any)["properties"].(map[string]any)
		req, ok := props["request"].(map[string]any)
		if !ok {
			continue
		}
		if _, isRef := req["$ref"]; isRef {
			continue // a response shape (DraftDetail.request), not a RawRequest
		}
		n++
		if req["type"] != "object" || req["properties"] != nil || req["additionalProperties"] != nil {
			t.Errorf("%s.request = %v, want a bare {type: object}", name, req)
		}
	}
	if n < 3 { // lint item, create_draft, update_draft
		t.Errorf("found only %d request fields", n)
	}
}

// TestRawRequest_KeepsTokens: a RawRequest reaches the handler as the client's
// exact bytes (seed 2^53 and 1e3 survive; a float64 round trip would lose them).
func TestRawRequest_KeepsTokens(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	a := humago.New(mux, api.HumaConfig())
	var got string
	type in struct {
		Body struct {
			Request api.RawRequest `json:"request"`
		}
	}
	type out struct{ Body struct{ OK bool } }
	api.Register(a, huma.Operation{OperationID: "t_raw", Method: "POST", Path: "/api/raw", Summary: "s", Description: "d"},
		api.Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "none:test"},
		func(_ context.Context, i *in) (*out, error) { got = string(i.Body.Request); return &out{}, nil })
	body := `{"request":{"seed":9007199254740992,"cfg":1e3,"s":"<&>"}}`
	r := &rig{mux: mux}
	if w := r.call(agent, "POST", "/api/raw", body); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if want := `{"seed":9007199254740992,"cfg":1e3,"s":"<&>"}`; got != want {
		t.Errorf("request bytes = %s, want %s", got, want)
	}
	if w := r.call(agent, "POST", "/api/raw", `{"request":[1]}`); w.Code != 422 {
		t.Errorf("array request: %d, want 422", w.Code)
	}
}

func TestRefFor_Table(t *testing.T) {
	t.Parallel()
	humanP := auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice"}
	deviceP := auth.Principal{Kind: auth.KindDevice, ID: "d1", Name: "Phone"}
	agentP := auth.Principal{Kind: auth.KindAgent, ID: "t1", Name: "bot"}
	tests := []struct {
		name         string
		viewer       auth.Principal
		key, kind, n string
		want         api.PrincipalRef
		wantKey      string
	}{
		{"human sees human", humanP, "human:alice", "human", "alice", api.PrincipalRef{Kind: "human", ID: "human:alice", Name: "alice"}, "human:alice"},
		{"device sees device", deviceP, "device:d1", "device", "Phone", api.PrincipalRef{Kind: "device", ID: "device:d1", Name: "Phone"}, "device:d1"},
		{"device sees human", deviceP, "human:alice", "human", "alice", api.PrincipalRef{Kind: "human", ID: "human:alice", Name: "alice"}, "human:alice"},
		{"human sees agent", humanP, "agent:t1", "agent", "bot", api.PrincipalRef{Kind: "agent", ID: "agent:t1", Name: "bot"}, "agent:t1"},
		{"agent sees human", agentP, "human:alice", "human", "alice", api.PrincipalRef{Kind: "human", ID: "human", Name: "human"}, "human"},
		{"agent sees device", agentP, "device:d1", "device", "Phone", api.PrincipalRef{Kind: "device", ID: "device:d1", Name: "device"}, "device:d1"},
		{"agent sees agent", agentP, "agent:t1", "agent", "bot", api.PrincipalRef{Kind: "agent", ID: "agent:t1", Name: "bot"}, "agent:t1"},
		{"agent sees system", agentP, "system:cli", "system", "cli", api.PrincipalRef{Kind: "system", ID: "system:cli", Name: "cli"}, "system:cli"},
	}
	for _, tc := range tests {
		if got := api.RefFor(tc.viewer, tc.key, tc.kind, tc.n); got != tc.want {
			t.Errorf("%s: RefFor = %+v, want %+v", tc.name, got, tc.want)
		}
		if got := api.KeyFor(tc.viewer, tc.key); got != tc.wantKey {
			t.Errorf("%s: KeyFor = %q, want %q", tc.name, got, tc.wantKey)
		}
	}
}

// TestOps_M2aStubsAnswer501: with zero Deps every stub still registers and
// answers 501 NOT_IMPLEMENTED naming its task (a human principal is allowed all of them).
func TestOps_M2aStubsAnswer501(t *testing.T) {
	t.Parallel()
	_, mux := newTestAPI(t)
	r := &rig{mux: mux}
	raw := `{"request":{"id":"x"}}`
	bodies := map[string]string{
		"rate_track": `{"stars":4}`, "update_track": `{"status":"final"}`, "set_tags": `{"add":["a"]}`,
		"add_comment": `{"body":"x"}`, "update_comment": `{"state":"resolved"}`,
		"log_listen_events": `{"events":[{"id":"i","track_id":"t","session_id":"s","kind":"start","client_at":"2026-10-06T11:59:00Z"}]}`,
		"lint_recipe":       `{"items":[{"request":{"id":"x"}}]}`, "create_draft": raw, "update_draft": `{"revision":1}`,
		"upload_batch_jsonl": `{"jsonl":"{}"}`, "create_playlist": `{"name":"n"}`, "update_playlist": `{"revision":1}`,
		"export_jsonl": `{"selection":"tracks"}`, "create_token": `{"name":"n","scopes":["read"]}`, "create_pairing_code": `{"name":"n"}`,
	}
	// M2a-B4b and M2a-B8 implemented these operations: without their service they answer 503 UNAVAILABLE.
	implemented := map[string]bool{"rate_track": true, "update_track": true, "set_tags": true, "list_tags": true, "add_comment": true, "update_comment": true, "list_comments": true,
		"get_track_context": true, "log_listen_events": true, "get_audit": true,
		// M2a-B9: tokens and devices
		"list_tokens": true, "create_token": true, "revoke_token": true, "list_devices": true, "create_pairing_code": true, "revoke_device": true,
		// merged lanes
		"list_playlists": true, "get_playlist": true, "create_playlist": true, "update_playlist": true,
		"export_playlist_zip": true, "list_export_sources": true, "export_jsonl": true, "get_export": true,
		"create_backup": true, "list_backups": true,
		"lint_recipe": true, "create_draft": true, "update_draft": true, "upload_batch_jsonl": true}
	for _, w := range m2aOps {
		path := strings.NewReplacer("{id}", "x").Replace(w.path)
		req := bodies[w.id]
		wantCode, wantStatus := "NOT_IMPLEMENTED", 501
		if implemented[w.id] {
			wantCode, wantStatus = "UNAVAILABLE", 503
		}
		// the self-service operations answer by caller kind first; the test caller is a human
		switch w.id {
		case "revoke_own_token":
			wantCode, wantStatus = "NOT_A_TOKEN", 403
		case "revoke_own_device":
			wantCode, wantStatus = "NOT_A_DEVICE", 403
		}
		if w.create {
			// the gate (not mounted here) owns Idempotency-Key; Huma requires the header itself
			rq := r.callWith(human, w.method, path, req, "Idempotency-Key", "k1")
			m := decodeProblem(t, rq.Body.Bytes())
			if rq.Code != wantStatus || m["code"] != wantCode {
				t.Errorf("%s: %d %s", w.id, rq.Code, rq.Body)
			}
			continue
		}
		rq := r.call(human, w.method, path, req)
		m := decodeProblem(t, rq.Body.Bytes())
		if rq.Code != wantStatus || m["code"] != wantCode {
			t.Errorf("%s: %d %s", w.id, rq.Code, rq.Body)
		}
	}
}
