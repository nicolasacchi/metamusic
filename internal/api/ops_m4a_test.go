package api_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/prompts"
)

type m4aOp struct {
	id, method, path, mcp, ui string
	scope                     auth.Scope
	create, heavy             bool
	secrets                   []string
	status                    int    // success status; 0 => 200
	body                      int    // MaxBodyBytes; 0 => no body
	component                 string // the 503 UNAVAILABLE component
	task                      string // the lane that replaces the stub
	reqBody                   string // a body that passes validation
	query                     string // a query string that passes validation
}

// m4aOps is the C113 table: 8 operations, all cost free.
var m4aOps = []m4aOp{
	{id: "get_offline_set", method: "GET", path: "/api/offline-set", scope: auth.ScopeRead, mcp: "generic", ui: "/offline", component: "playlists", task: "M4a-A1"},
	{id: "get_download_link", method: "GET", path: "/api/tracks/{id}/download", scope: auth.ScopeRead, mcp: "generic", ui: "/tracks/:id", heavy: true, component: "downloads", task: "M4a-M2"},
	{id: "get_prompt", method: "GET", path: "/api/prompts/{name}", scope: auth.ScopeRead, mcp: "generic", ui: "/tracks/:id", component: "prompts", task: "M4a-A1", query: "?track_id=x"},
	{id: "list_connector_grants", method: "GET", path: "/api/connectors", scope: auth.ScopeAdmin, mcp: "generic", ui: "/human/connectors", component: "connectors", task: "M4a-C2"},
	{id: "set_connector_grant", method: "PUT", path: "/api/connectors/{id}", scope: auth.ScopeAdmin, mcp: "generic", ui: "/human/connectors", body: 64 * kib, component: "connectors", task: "M4a-C2", reqBody: `{"revoked":false}`},
	{id: "list_subsonic_clients", method: "GET", path: "/api/subsonic-clients", scope: auth.ScopeAdmin, mcp: "generic", ui: "/human/subsonic", component: "subsonic", task: "M4a-S1"},
	{id: "create_subsonic_client", method: "POST", path: "/api/subsonic-clients", scope: auth.ScopeAdmin, mcp: "generic", ui: "/human/subsonic", create: true, secrets: []string{"api_key", "password"}, status: 201, body: 64 * kib, component: "subsonic", task: "M4a-S1", reqBody: `{"name":"Test phone"}`},
	{id: "revoke_subsonic_client", method: "POST", path: "/api/subsonic-clients/{id}/revoke", scope: auth.ScopeAdmin, mcp: "generic", ui: "/human/subsonic", component: "subsonic", task: "M4a-S1"},
}

func m4aPath(w m4aOp) string {
	p := strings.NewReplacer("{id}", "x", "{name}", "critique_track").Replace(w.path)
	return p + w.query
}

func m4aIdem(w m4aOp) []string {
	if w.create {
		return []string{"Idempotency-Key", "k-1"}
	}
	return nil
}

var allScopes = []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}

// m4aDeps wires every M4a dependency with a bare value: registration never dereferences them and the stubs
// answer 501 before touching them.
func m4aDeps() api.Deps {
	return api.Deps{
		Connectors: connectors.NewStore(nil, nil, nil), Subsonic: &auth.SubsonicStore{}, Downloads: &tagged.Service{},
		Playlists: &playlists.Service{}, Prompts: stubSources{},
	}
}

func m4aRig(t *testing.T, d api.Deps) *rig {
	t.Helper()
	mux := http.NewServeMux()
	api.New(mux, d)
	return &rig{mux: mux}
}

func TestOps_RegisteredWithMetaM4a(t *testing.T) {
	t.Parallel()
	if len(m4aOps) != 8 {
		t.Fatalf("the C113 table has %d rows, want 8", len(m4aOps))
	}
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	for _, w := range m4aOps {
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
			m.HumanOnly || m.Idempotency != idem || m.Rate != rate || m.AliasOf != "" || !slices.Equal(m.SecretFields, w.secrets) {
			t.Errorf("%s = %+v, want %+v", w.id, op, w)
		}
		if strings.HasPrefix(w.path, "/human/api/") {
			t.Errorf("%s must live under /api/ (D-M4a-24)", w.id)
		}
		want := w.status
		if want == 0 {
			want = 200
		}
		if o := pathOperation(a, op); o.DefaultStatus != 0 && o.DefaultStatus != want || o.DefaultStatus == 0 && want != 200 {
			t.Errorf("%s: default status %d, want %d", w.id, o.DefaultStatus, want)
		}
	}
	// three read ops, five admin management ops, all generic; none excluded and no first-class tool
	reads, admins := 0, 0
	for _, w := range m4aOps {
		if w.mcp != "generic" {
			t.Errorf("%s: mcp %q, want generic", w.id, w.mcp)
		}
		switch w.scope {
		case auth.ScopeRead:
			reads++
		case auth.ScopeAdmin:
			admins++
		}
	}
	if reads != 3 || admins != 5 {
		t.Errorf("read/admin = %d/%d, want 3/5", reads, admins)
	}
	// the human-only global caps stay out of reach of agents (D-M4a-24)
	for _, id := range []string{"set_spend_caps", "create_token", "revoke_token"} {
		if m := byID[id].Meta; !m.HumanOnly || !strings.HasPrefix(byID[id].Path, "/human/api/") {
			t.Errorf("%s must stay human-only: %+v", id, byID[id])
		}
	}
}

// TestValidateMeta_SecretFieldsM4a: a non-human create op may declare secret fields; a non-create op may not.
func TestValidateMeta_SecretFieldsM4a(t *testing.T) {
	t.Parallel()
	op := huma.Operation{OperationID: "make_thing", Method: "POST", Path: "/api/things", Summary: "s", Description: "d"}
	ok := api.Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/x", Idempotency: "create", SecretFields: []string{"api_key", "password"}}
	if err := api.ValidateMeta(op, ok); err != nil {
		t.Fatalf("non-human create op with secret fields refused: %v", err)
	}
	notCreate := ok
	notCreate.Idempotency = ""
	if err := api.ValidateMeta(op, notCreate); err == nil || !strings.Contains(err.Error(), "secret_fields") {
		t.Errorf("non-create op with secret fields accepted: %v", err)
	}
	get := op
	get.Method = "GET"
	if err := api.ValidateMeta(get, notCreate); err == nil {
		t.Error("GET with secret fields accepted")
	}
	bad := ok
	bad.SecretFields = []string{"ApiKey"}
	if err := api.ValidateMeta(op, bad); err == nil {
		t.Error("badly named secret field accepted")
	}
	// the human-only create case keeps working
	hop := op
	hop.Path = "/human/api/things"
	hm := ok
	hm.HumanOnly, hm.MCP = true, "exclude:human-only"
	if err := api.ValidateMeta(hop, hm); err != nil {
		t.Errorf("human-only create with secrets: %v", err)
	}
}

func TestOps_BodyLimitsM4a(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, w := range m4aOps {
		o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path})
		if o.MaxBodyBytes != int64(w.body) {
			t.Errorf("%s: MaxBodyBytes = %d, want %d", w.id, o.MaxBodyBytes, w.body)
		}
		if hasBody := o.RequestBody != nil; hasBody != (w.body != 0) {
			t.Errorf("%s: request body = %v but limit = %d", w.id, hasBody, w.body)
		}
	}
}

// TestOps_NilDepsUnavailableM4a: zero Deps registers every op, and each answers 503 naming its component.
func TestOps_NilDepsUnavailableM4a(t *testing.T) {
	t.Parallel()
	r := m4aRig(t, api.Deps{})
	seen := map[string]bool{}
	for _, w := range m4aOps {
		rec := r.callWith(fullAdmin, w.method, m4aPath(w), w.reqBody, m4aIdem(w)...)
		m := decodeProblem(t, rec.Body.Bytes())
		d, _ := m["details"].(map[string]any)
		if rec.Code != 503 || m["code"] != "UNAVAILABLE" || d["component"] != w.component {
			t.Errorf("%s: %d %s, want 503 UNAVAILABLE component %s", w.id, rec.Code, rec.Body, w.component)
		}
		seen[w.component] = true
	}
	for _, c := range []string{"playlists", "downloads", "prompts", "connectors", "subsonic"} {
		if !seen[c] {
			t.Errorf("no operation answers for component %s", c)
		}
	}
}

// TestOps_StubsAnswer501M4a: until the lanes land, each op answers 501 NOT_IMPLEMENTED naming its task.
func TestOps_StubsAnswer501M4a(t *testing.T) {
	t.Parallel()
	r := m4aRig(t, m4aDeps())
	for _, w := range m4aOps {
		if implementedM4a[w.id] { // a lane replaced the stub: its behaviour is covered by that lane's tests
			continue
		}
		rec := r.callWith(fullAdmin, w.method, m4aPath(w), w.reqBody, m4aIdem(w)...)
		m := decodeProblem(t, rec.Body.Bytes())
		d, _ := m["details"].(map[string]any)
		if rec.Code != 501 || m["code"] != "NOT_IMPLEMENTED" || d["task"] != w.task {
			t.Errorf("%s: %d %s, want 501 NOT_IMPLEMENTED task %s", w.id, rec.Code, rec.Body, w.task)
		}
	}
}

// implementedM4a lists the operations whose stub a lane has replaced (the lane adds the id here).
var implementedM4a = map[string]bool{}

// TestOps_AuthorizationM4a: the management ops need scope admin: a read+write agent, a paired device and an OAuth
// connector are refused with 403; read ops need only read.
func TestOps_AuthorizationM4a(t *testing.T) {
	t.Parallel()
	r := m4aRig(t, m4aDeps())
	rw := []auth.Scope{auth.ScopeRead, auth.ScopeWrite}
	principals := map[string]auth.Principal{
		"token agent read+write": {Kind: auth.KindAgent, ID: "tok", Name: "t", Scopes: rw, Via: "token"},
		"paired device":          {Kind: auth.KindDevice, ID: "dev", Name: "phone", Scopes: rw, Via: "device"},
		"oauth connector":        {Kind: auth.KindAgent, ID: "grant", Name: "Claude", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate}, Via: "oauth", Client: "test-claude"},
		"subsonic client":        {Kind: auth.KindDevice, ID: "sub", Name: "app", Scopes: rw, Via: "subsonic"},
	}
	for name, p := range principals {
		for _, w := range m4aOps {
			rec := r.callWith(p, w.method, m4aPath(w), w.reqBody, m4aIdem(w)...)
			if w.scope == auth.ScopeAdmin {
				if rec.Code != 403 {
					t.Errorf("%s calling %s: %d, want 403", name, w.id, rec.Code)
				}
			} else if rec.Code == 403 || rec.Code == 401 {
				t.Errorf("%s calling %s: %d, a read op needs only read", name, w.id, rec.Code)
			}
		}
	}
	// an admin-scope agent and the human pass the gate (the stub answers 501 behind it)
	adminAgent := auth.Principal{Kind: auth.KindAgent, ID: "adm", Name: "admin agent", Scopes: allScopes, Via: "token"}
	for _, p := range []auth.Principal{adminAgent, human} {
		p.Scopes = allScopes
		for _, w := range m4aOps {
			if implementedM4a[w.id] {
				continue
			}
			if rec := r.callWith(p, w.method, m4aPath(w), w.reqBody, m4aIdem(w)...); rec.Code != 501 {
				t.Errorf("%s as %s: %d, want 501", w.id, p.Kind, rec.Code)
			}
		}
	}
	// unauthenticated
	if rec := r.callWith(auth.Principal{}, "GET", "/api/connectors", ""); rec.Code != 401 {
		t.Errorf("anonymous: %d, want 401", rec.Code)
	}
}

var fullAdmin = auth.Principal{Kind: auth.KindAgent, ID: "tok-all", Name: "all", Scopes: allScopes, Via: "token"}

func TestOps_PromptNameEnum(t *testing.T) {
	t.Parallel()
	r := m4aRig(t, m4aDeps())
	if rec := r.callWith(fullAdmin, "GET", "/api/prompts/nope?track_id=x", ""); rec.Code != 422 {
		t.Errorf("unknown prompt name: %d %s, want 422", rec.Code, rec.Body)
	}
	for _, n := range []string{"critique_track", "write_variant", "session_review"} {
		if rec := r.callWith(fullAdmin, "GET", "/api/prompts/"+n+"?track_id=x", ""); rec.Code == 422 {
			t.Errorf("%s refused by the enum: %s", n, rec.Body)
		}
	}
	if !slices.ContainsFunc(prompts.Catalog, func(s prompts.Spec) bool { return s.Name == "session_review" }) {
		t.Error("catalog lost session_review")
	}
}

// TestOps_SubsonicClientInputsM4a: the validation of the create input is contractual (C113).
func TestOps_SubsonicClientInputsM4a(t *testing.T) {
	t.Parallel()
	r := m4aRig(t, m4aDeps())
	for _, bad := range []string{
		`{}`, `{"name":""}`, `{"name":"` + strings.Repeat("x", 81) + `"}`, `{"name":"a","username":"Bad Name"}`,
		`{"name":"a","default_format":"flac"}`, `{"name":"a","write":"yes"}`,
	} {
		if rec := r.callWith(fullAdmin, "POST", "/api/subsonic-clients", bad, "Idempotency-Key", "k"); rec.Code != 422 {
			t.Errorf("%s: %d, want 422", bad, rec.Code)
		}
	}
	for _, good := range []string{`{"name":"Phone"}`, `{"name":"Phone","username":"phone.1","write":false,"default_format":"mp3","api_key":false,"password":true}`} {
		if rec := r.callWith(fullAdmin, "POST", "/api/subsonic-clients", good, "Idempotency-Key", "k"); rec.Code == 422 || rec.Code == 400 {
			t.Errorf("%s refused: %d %s", good, rec.Code, rec.Body)
		}
	}
}

// TestOps_ConnectorGrantInputsM4a: set_connector_grant accepts scopes, a whole limits object and revoked.
func TestOps_ConnectorGrantInputsM4a(t *testing.T) {
	t.Parallel()
	r := m4aRig(t, m4aDeps())
	for _, bad := range []string{
		`{"scopes":["read","read"]}`, `{"scopes":["read","admin"]}`, `{"revoked":"yes"}`,
		`{"limits":{"daily_budget_micro_usd":-1,"auto_approve_takes":1,"auto_approve_micro_usd":1}}`,
		`{"limits":{"daily_budget_micro_usd":1000000001,"auto_approve_takes":1,"auto_approve_micro_usd":1}}`,
		`{"limits":{"daily_budget_micro_usd":1,"auto_approve_takes":101,"auto_approve_micro_usd":1}}`,
		`{"limits":{"daily_budget_micro_usd":1}}`,
	} {
		if rec := r.callWith(fullAdmin, "PUT", "/api/connectors/x", bad); rec.Code != 422 {
			t.Errorf("%s: %d %s, want 422", bad, rec.Code, rec.Body)
		}
	}
	for _, good := range []string{
		`{"scopes":["read","write","generate"]}`, `{"revoked":true}`,
		`{"limits":{"daily_budget_micro_usd":null,"auto_approve_takes":null,"auto_approve_micro_usd":null}}`,
		`{"limits":{"daily_budget_micro_usd":500000,"auto_approve_takes":0,"auto_approve_micro_usd":0},"revoked":false}`,
	} {
		if rec := r.callWith(fullAdmin, "PUT", "/api/connectors/x", good); rec.Code == 422 || rec.Code == 400 {
			t.Errorf("%s refused: %d %s", good, rec.Code, rec.Body)
		}
	}
}

type stubSources struct{}

func (stubSources) ContextJSON(context.Context, auth.Principal, string) ([]byte, error) {
	return nil, nil
}
func (stubSources) Session(context.Context, time.Time, int) (listening.Session, error) {
	return listening.Session{}, nil
}
func (stubSources) Title(context.Context, string) (string, error)     { return "", nil }
func (stubSources) RecipeKnown(context.Context, string) (bool, error) { return true, nil }
