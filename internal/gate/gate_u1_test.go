package gate_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
)

// C304.4: the audit predicate after the listen retag, and the listen rate class.

var (
	listener = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "carol", Name: "carol", User: "carol", Scopes: auth.ScopesFor(auth.RoleListener), Via: "proxy"}
	editor   = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", User: "bob", Scopes: auth.ScopesFor(auth.RoleEditor), Via: "proxy"}
)

type u1In struct {
	Body struct {
		Verdict string `json:"verdict,omitempty"`
	}
}

func u1Ops(r *rig, a huma.API) {
	reg := func(id, method, path string) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: id, Description: id}
	}
	ok := func(ctx context.Context, _ *u1In) (*okOut, error) { o := &okOut{}; o.Body.OK = true; return o, nil }
	api.Register(a, reg("t_browse", "GET", "/api/t/browse"), api.Meta{Scope: auth.ScopeListen, Cost: "free", MCP: "tool", UI: "none:test"},
		func(ctx context.Context, _ *struct{}) (*okOut, error) { o := &okOut{}; o.Body.OK = true; return o, nil })
	api.Register(a, reg("t_rate", "POST", "/api/t/rate"), api.Meta{Scope: auth.ScopeRate, Cost: "free", MCP: "tool", UI: "none:test", Rate: "listen"}, ok)
	api.Register(a, reg("t_events", "POST", "/api/t/events"), api.Meta{Scope: auth.ScopeRate, Cost: "free", MCP: "tool", UI: "none:test", Rate: "listen", NoAudit: true}, ok)
}

func bigLimits() ratelimit.Config {
	return ratelimit.Config{
		ratelimit.Human: {PerMinute: 1e6, Burst: 100000}, ratelimit.Agent: {PerMinute: 1e6, Burst: 100000},
		ratelimit.Listen: {PerMinute: 1e6, Burst: 100000},
	}
}

func TestGate_BrowseGETNotAuditedNorHeld(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{limits: bigLimits(), extra: u1Ops})
	for _, p := range []auth.Principal{human, listener, editor} {
		if w := r.do(req{p: p, method: "GET", path: "/api/t/browse"}); w.Code != 200 {
			t.Fatalf("%s: %d %s", p.Name, w.Code, w.Body)
		}
	}
	if n := len(r.rows()); n != 0 {
		t.Fatalf("%d audit rows for browse GETs, want 0", n)
	}
	// An agent is audited whatever it calls (unchanged).
	r.do(req{p: agent("t1", auth.ScopeRead), method: "GET", path: "/api/t/browse"})
	if n := len(r.rows()); n != 1 {
		t.Fatalf("agent browse: %d rows, want 1", n)
	}
}

func TestGate_RateTrackAuditedWithoutVerdict(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{limits: bigLimits(), extra: u1Ops})
	w := r.do(req{p: listener, method: "POST", path: "/api/t/rate", body: `{"verdict":"CANARY-NOTE-900"}`})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	rows := r.rows()
	if len(rows) != 1 || rows[0].Op != "t_rate" || rows[0].PrincipalName != "carol" {
		t.Fatalf("rows = %+v", rows)
	}
	if strings.Contains(fmt.Sprintf("%+v", rows[0]), "CANARY-NOTE-900") {
		t.Fatalf("audit row carries the verdict: %+v", rows[0])
	}
}

func TestGate_NoAuditOpWritesNoRows(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{limits: bigLimits(), extra: u1Ops})
	for _, p := range []auth.Principal{listener, editor, human, agent("t1", auth.ScopeWrite)} {
		for range 200 {
			if w := r.do(req{p: p, method: "POST", path: "/api/t/events", body: `{}`}); w.Code != 200 {
				t.Fatalf("%s: %d %s", p.Name, w.Code, w.Body)
			}
		}
	}
	if n := len(r.rows()); n != 0 {
		t.Fatalf("%d audit rows for NoAudit calls, want 0", n)
	}
}

func TestGate_ListenRateClass(t *testing.T) {
	t.Parallel()
	lim := bigLimits()
	lim[ratelimit.Listen] = ratelimit.Limit{PerMinute: 60, Burst: 3}
	r := newRig(t, rigOpts{limits: lim, extra: u1Ops})
	call := func(p auth.Principal) int {
		return r.do(req{p: p, method: "POST", path: "/api/t/events", body: `{}`}).Code
	}
	for i := range 3 {
		if c := call(listener); c != 200 {
			t.Fatalf("call %d: %d", i, c)
		}
	}
	w := r.do(req{p: listener, method: "POST", path: "/api/t/events", body: `{}`})
	if w.Code != 429 || code(w) != "RATE_LIMITED" || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), `"listen"`) {
		t.Fatalf("%d %s %v", w.Code, w.Body, w.Header())
	}
	// Per principal: the Editor has its own bucket; the Owner and agents are not charged the listen class.
	if c := call(editor); c != 200 {
		t.Fatalf("editor: %d", c)
	}
	for range 10 {
		if c := call(human); c != 200 {
			t.Fatalf("owner limited by listen class: %d", c)
		}
	}
	// Browse GETs are not in the class.
	if w := r.do(req{p: listener, method: "GET", path: "/api/t/browse"}); w.Code != 200 {
		t.Fatalf("browse: %d", w.Code)
	}
}
