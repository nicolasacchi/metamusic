package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

type m2bOp struct {
	id, method, path, mcp, ui string
	scope                     auth.Scope
	create, heavy, image      bool
	status                    int // success status; 0 => 200
	body                      int // MaxBodyBytes; 0 => no body
	component, task, call     string
	reqBody                   string
}

// m2bOps is the C53 table: 10 operations. D-M2b-22 demoted create_duel, get_ladder and compare_tracks to mcp generic to hold the tools/list ceiling. call is the request path used by the stub tests.
var m2bOps = []m2bOp{
	{id: "get_clip", method: "GET", path: "/api/tracks/{id}/clip", mcp: "generic", ui: "none:clip links reach the SPA inside duel responses", scope: auth.ScopeRead, heavy: true,
		component: "clips", task: "M2b-M1", call: "/api/tracks/t1/clip"},
	{id: "create_duel", method: "POST", path: "/api/duels", mcp: "generic", ui: "/duels", scope: auth.ScopeWrite, create: true, heavy: true, status: 201, body: 64 * kib,
		component: "duels", task: "M2b-J2", call: "/api/duels", reqBody: `{"a_track_id":"a","b_track_id":"b"}`},
	{id: "next_duel", method: "GET", path: "/api/duels/next", mcp: "tool", ui: "/duels", scope: auth.ScopeRead,
		component: "duels", task: "M2b-J2", call: "/api/duels/next"},
	{id: "get_duel", method: "GET", path: "/api/duels/{id}", mcp: "generic", ui: "/duels/:id", scope: auth.ScopeRead,
		component: "duels", task: "M2b-J2", call: "/api/duels/d1"},
	{id: "list_duels", method: "GET", path: "/api/duels", mcp: "generic", ui: "/duels", scope: auth.ScopeRead,
		component: "duels", task: "M2b-J2", call: "/api/duels"},
	{id: "submit_duel", method: "POST", path: "/api/duels/{id}/verdicts", mcp: "tool", ui: "/duels", scope: auth.ScopeWrite, status: 201, body: 64 * kib,
		component: "duels", task: "M2b-J2", call: "/api/duels/d1/verdicts", reqBody: `{"verdict":"a"}`},
	{id: "get_ladder", method: "GET", path: "/api/ladder", mcp: "generic", ui: "/ladder", scope: auth.ScopeRead,
		component: "duels", task: "M2b-J2", call: "/api/ladder"},
	{id: "get_track_analysis", method: "GET", path: "/api/tracks/{id}/analysis", mcp: "tool", ui: "/tracks/:id", scope: auth.ScopeRead,
		component: "ears", task: "M2b-E3", call: "/api/tracks/t1/analysis"},
	{id: "get_track_spectrogram", method: "GET", path: "/api/tracks/{id}/spectrogram", mcp: "tool", ui: "/tracks/:id", scope: auth.ScopeRead, image: true,
		component: "ears", task: "M2b-E3", call: "/api/tracks/t1/spectrogram"},
	{id: "compare_tracks", method: "GET", path: "/api/compare", mcp: "generic", ui: "/compare", scope: auth.ScopeRead,
		component: "ears", task: "M2b-E3", call: "/api/compare?a=x&b=y"},
}

func TestOps_M2bRegisteredWithMeta(t *testing.T) {
	t.Parallel()
	if len(m2bOps) != 10 {
		t.Fatalf("the C53 table has %d rows, want 10", len(m2bOps))
	}
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	for _, w := range m2bOps {
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
			m.HumanOnly || m.Idempotency != idem || m.Rate != rate || m.MCPImage != w.image || len(m.SecretFields) != 0 {
			t.Errorf("%s = %+v, want %+v", w.id, op, w)
		}
		o := pathOperation(a, op)
		want := w.status
		if want == 0 {
			want = 200
		}
		if o.DefaultStatus != 0 && o.DefaultStatus != want || o.DefaultStatus == 0 && want != 200 {
			t.Errorf("%s: default status %d, want %d", w.id, o.DefaultStatus, want)
		}
		if o.MaxBodyBytes != int64(w.body) || (o.RequestBody != nil) != (w.body != 0) {
			t.Errorf("%s: body limit %d (body %v), want %d", w.id, o.MaxBodyBytes, o.RequestBody != nil, w.body)
		}
		if !strings.Contains(o.Description, "") || o.Summary == "" {
			t.Errorf("%s lacks a description", w.id)
		}
	}
	// submit_duel documents its 200 replay response next to the 201
	if sd := pathOperation(a, byID["submit_duel"]); sd.Responses["200"] == nil || sd.Responses["201"] == nil {
		t.Errorf("submit_duel responses = %v, want 200 and 201", sd.Responses)
	}
	// binding sentences of C53
	for id, phrase := range map[string]string{
		"submit_duel":           "Verdicts from agents are stored as critic opinions and never change the human ladder.",
		"get_ladder":            "u is a log-strength on a scale anchored at the virtual 'reject' item (0); compare u values, not probabilities of acceptance.",
		"get_track_spectrogram": "Returns the image as MCP ImageContent; never audio.",
		"create_duel":           "A replayed call returns the first result; call get_duel for fresh clip links.",
	} {
		if d := pathOperation(a, byID[id]).Description; !strings.Contains(d, phrase) {
			t.Errorf("%s description lacks %q", id, phrase)
		}
	}
	for _, id := range []string{"get_track_analysis", "get_track_spectrogram", "compare_tracks"} {
		if !strings.Contains(pathOperation(a, byID[id]).Description, "blind_spots") {
			t.Errorf("%s description does not mention blind_spots", id)
		}
	}
}

func TestOps_M2bParameterShapes(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	ops := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		ops[op.ID] = op
	}
	params := func(id string) map[string]bool { // name => explode
		m := map[string]bool{}
		for _, p := range pathOperation(a, ops[id]).Parameters {
			e := false
			if p.Explode != nil {
				e = *p.Explode
			}
			m[p.Name] = e
		}
		return m
	}
	if p := params("next_duel"); !p["exclude"] {
		t.Errorf("next_duel exclude must explode: %v", p)
	}
	ld := params("list_duels")
	if !ld["state"] {
		t.Errorf("list_duels state must explode: %v", ld)
	}
	if _, ok := ld["mode"]; !ok {
		t.Errorf("list_duels has no mode parameter: %v", ld)
	}
	if _, ok := ld["blind"]; ok {
		t.Errorf("list_duels must not have a blind parameter (mode replaces it)")
	}
	for _, n := range []string{"track_id", "root_track_id", "limit", "cursor"} {
		if _, ok := ld[n]; !ok {
			t.Errorf("list_duels lacks %s", n)
		}
	}
	// create_duel: no note; blind and match_loudness carry no default (an explicit false must survive)
	body := a.OpenAPI().Components.Schemas.SchemaFromRef(pathOperation(a, ops["create_duel"]).RequestBody.Content["application/json"].Schema.Ref)
	if body.Properties["note"] != nil {
		t.Error("create_duel must not accept a note (D-M2b-19)")
	}
	for _, n := range []string{"blind", "match_loudness"} {
		p := body.Properties[n]
		if p == nil {
			t.Errorf("create_duel lacks %s", n)
		} else if p.Default != nil {
			t.Errorf("create_duel.%s carries default %v", n, p.Default)
		}
	}
	// the clip window parameters are numbers
	for _, p := range pathOperation(a, ops["get_clip"]).Parameters {
		if p.In == "query" && (p.Name == "from_s" || p.Name == "to_s" || p.Name == "match_lufs") && p.Schema.Type != "number" {
			t.Errorf("get_clip %s type = %q, want number", p.Name, p.Schema.Type)
		}
	}
	if p := params("get_track_spectrogram"); len(p) < 2 {
		t.Errorf("get_track_spectrogram params = %v", p)
	}
	// compare_tracks requires both ids
	for _, p := range pathOperation(a, ops["compare_tracks"]).Parameters {
		if !p.Required {
			t.Errorf("compare_tracks %s must be required", p.Name)
		}
	}
	// M2a's create-op rule: create_duel declares the key, submit_duel does not
	for id, want := range map[string]bool{"create_duel": true, "submit_duel": false} {
		has := slices.ContainsFunc(pathOperation(a, ops[id]).Parameters, func(p *huma.Param) bool { return p.Name == "Idempotency-Key" })
		if has != want {
			t.Errorf("%s declares Idempotency-Key = %v, want %v", id, has, want)
		}
	}
}

// TestOps_M2bNilDepsUnavailable: with zero Deps every M2b op answers 503 UNAVAILABLE naming its component.
func TestOps_M2bNilDepsUnavailable(t *testing.T) {
	t.Parallel()
	_, mux := newTestAPI(t)
	r := &rig{mux: mux}
	for _, w := range m2bOps {
		var rq = r.callWith(human, w.method, w.call, w.reqBody, hdrFor(w)...)
		m := decodeProblem(t, rq.Body.Bytes())
		details, _ := m["details"].(map[string]any)
		if rq.Code != 503 || m["code"] != "UNAVAILABLE" || details["component"] != w.component {
			t.Errorf("%s: %d %s, want 503 UNAVAILABLE %s", w.id, rq.Code, rq.Body, w.component)
		}
	}
}

func hdrFor(w m2bOp) []string {
	if w.create {
		return []string{"Idempotency-Key", "k1"}
	}
	return nil
}

func stubAPIRig(t *testing.T) *rig {
	t.Helper()
	mux := http.NewServeMux()
	api.New(mux, api.Deps{
		Duels: duels.New(duels.Options{}),
		Ears:  ears.New(ears.Options{}),
		Clips: clip.New(nil, "", mediatool.Tools{}, stubAnalyzer{}, nil, nil, clip.Options{}),
	})
	return &rig{mux: mux}
}

type stubAnalyzer struct{}

func (stubAnalyzer) ToolVersion() string { return "ffmpeg stub" }
func (stubAnalyzer) WindowLoudness(_ context.Context, _ string, _, _ int64) (clip.Measure, error) {
	return clip.Measure{}, errors.ErrUnsupported
}

// TestOps_StubNotImplemented: until the lanes land, every M2b op answers 501 NOT_IMPLEMENTED naming its task.
func TestOps_StubNotImplemented(t *testing.T) {
	t.Parallel()
	r := stubAPIRig(t)
	for _, w := range m2bOps {
		rq := r.callWith(human, w.method, w.call, w.reqBody, hdrFor(w)...)
		m := decodeProblem(t, rq.Body.Bytes())
		details, _ := m["details"].(map[string]any)
		if rq.Code != 501 || m["code"] != "NOT_IMPLEMENTED" || details["task"] != w.task {
			t.Errorf("%s: %d %s, want 501 NOT_IMPLEMENTED task %s", w.id, rq.Code, rq.Body, w.task)
		}
	}
}

// Agents (read/write scopes) reach the same ops; a read-only token cannot create or judge.
func TestOps_M2bScopes(t *testing.T) {
	t.Parallel()
	r := stubAPIRig(t)
	readOnly := auth.Principal{Kind: auth.KindAgent, ID: "t1", Name: "ro", Scopes: []auth.Scope{auth.ScopeRead}}
	for _, w := range m2bOps {
		rq := r.callWith(readOnly, w.method, w.call, w.reqBody, hdrFor(w)...)
		m := decodeProblem(t, rq.Body.Bytes())
		wantCode := 501
		if w.scope == auth.ScopeWrite {
			wantCode = 403
		}
		if rq.Code != wantCode {
			t.Errorf("%s as read-only agent: %d %s, want %d (%v)", w.id, rq.Code, rq.Body, wantCode, m["code"])
		}
	}
}

func TestServiceErrors_Mapping(t *testing.T) {
	t.Parallel()
	other := errors.New("boom: secret-ish detail")
	tests := []struct {
		name        string
		err         error
		status      int
		code        string
		passThrough bool
	}{
		{"problem error", problem.E(409, "DUEL_EXPIRED", "expired", map[string]any{"duel_id": "d1"}), 409, "DUEL_EXPIRED", false},
		{"wrapped problem error", fmt.Errorf("ctx: %w", problem.E(422, "DUEL_SAME_TRACK", "same", nil)), 422, "DUEL_SAME_TRACK", false},
		{"stub", errors.ErrUnsupported, 501, "NOT_IMPLEMENTED", false},
		{"wrapped stub", fmt.Errorf("x: %w", errors.ErrUnsupported), 501, "NOT_IMPLEMENTED", false},
		{"anything else", other, 0, "", true},
	}
	for _, tc := range tests {
		got := api.M2bErr(tc.err, "M2b-T")
		if tc.passThrough {
			// returned unchanged: it becomes a 500 INTERNAL whose cause the Huma hook logs through redact.Error
			if got != tc.err { //nolint:errorlint // identity: the error must be returned unchanged
				t.Errorf("%s: got %v, want the error unchanged", tc.name, got)
			}
			continue
		}
		var p *api.Problem
		if !errors.As(got, &p) || p.Status != tc.status || p.Code != tc.code {
			t.Errorf("%s: got %#v, want %d %s", tc.name, got, tc.status, tc.code)
		}
	}
	var p *api.Problem
	if errors.As(api.M2bErr(problem.E(409, "DUEL_EXPIRED", "m", map[string]any{"duel_id": "d1"}), ""), &p); p.Details["duel_id"] != "d1" {
		t.Errorf("details lost: %v", p.Details)
	}
	if err := api.M2bErr(nil, ""); err != nil {
		t.Errorf("nil => %v", err)
	}
}
