package api_test

import (
	"errors"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
)

func humanOf(role config.Role) auth.Principal {
	return auth.Principal{Kind: auth.KindHuman, ID: string(role), User: string(role), Role: role, Scopes: auth.ScopesFor(role), Via: "proxy"}
}

func problemCode(p *api.Problem) string {
	if p == nil {
		return ""
	}
	return p.Code
}

func TestPermitted(t *testing.T) {
	t.Parallel()
	agent := func(sc ...auth.Scope) auth.Principal {
		return auth.Principal{Kind: auth.KindAgent, ID: "t1", Scopes: sc, Via: "token"}
	}
	device := auth.Principal{Kind: auth.KindDevice, ID: "d1", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "device"}
	browse := api.Meta{Scope: auth.ScopeListen}
	read := api.Meta{Scope: auth.ScopeRead}
	oo := api.Meta{Scope: auth.ScopeRead, OwnerOnly: true}
	ooWrite := api.Meta{Scope: auth.ScopeWrite, OwnerOnly: true}
	human := api.Meta{Scope: auth.ScopeAdmin, HumanOnly: true, OwnerOnly: true}
	for _, tc := range []struct {
		name string
		p    auth.Principal
		m    api.Meta
		want string
	}{
		{"anonymous", auth.Principal{Kind: auth.KindAnonymous}, browse, "UNAUTHENTICATED"},
		{"zero principal", auth.Principal{}, browse, "UNAUTHENTICATED"},
		// order: human-only before owner-only before scope
		{"agent human-only", agent(auth.ScopeAdmin), human, "HUMAN_ONLY"},
		{"device human-only", device, human, "HUMAN_ONLY"},
		{"editor human-only is owner-only", humanOf(config.RoleEditor), human, "OWNER_ONLY"},
		{"owner human-only", humanOf(config.RoleOwner), human, ""},
		{"editor owner-only", humanOf(config.RoleEditor), oo, "OWNER_ONLY"},
		{"listener owner-only", humanOf(config.RoleListener), oo, "OWNER_ONLY"},
		{"owner owner-only", humanOf(config.RoleOwner), oo, ""},
		// owner-only skips non-human delegates: they pass or fail on scope as before
		{"agent owner-only passes by scope", agent(auth.ScopeRead), oo, ""},
		{"agent owner-only lacks scope", agent(auth.ScopeRead), ooWrite, "FORBIDDEN_SCOPE"},
		{"owner device owner-only", device, oo, ""},
		{"editor device owner-only", auth.Principal{Kind: auth.KindDevice, Role: config.RoleEditor, Scopes: auth.ScopesFor(config.RoleEditor)}, oo, "OWNER_ONLY"},
		{"listener device owner-only", auth.Principal{Kind: auth.KindDevice, Role: config.RoleListener, Scopes: auth.ScopesFor(config.RoleListener)}, oo, "OWNER_ONLY"},
		// the classes of D-U1-6 are owner-only even when untagged
		{"untagged admin op, editor", humanOf(config.RoleEditor), api.Meta{Scope: auth.ScopeAdmin}, "OWNER_ONLY"},
		{"untagged approve op, listener", humanOf(config.RoleListener), api.Meta{Scope: auth.ScopeGenerate, Cost: "approve"}, "OWNER_ONLY"},
		// scopes
		{"listener browses", humanOf(config.RoleListener), browse, ""},
		{"listener cannot read", humanOf(config.RoleListener), read, "FORBIDDEN_SCOPE"},
		{"listener rates", humanOf(config.RoleListener), api.Meta{Scope: auth.ScopeRate}, ""},
		{"listener cannot write", humanOf(config.RoleListener), api.Meta{Scope: auth.ScopeWrite}, "FORBIDDEN_SCOPE"},
		{"editor reads and generates", humanOf(config.RoleEditor), api.Meta{Scope: auth.ScopeGenerate}, ""},
		{"editor has no admin", humanOf(config.RoleEditor), api.Meta{Scope: auth.ScopeAdmin}, "OWNER_ONLY"},
		{"legacy read agent browses", agent(auth.ScopeRead), browse, ""},
		{"legacy write agent rates", agent(auth.ScopeWrite), api.Meta{Scope: auth.ScopeRate}, ""},
		{"legacy read agent cannot rate", agent(auth.ScopeRead), api.Meta{Scope: auth.ScopeRate}, "FORBIDDEN_SCOPE"},
	} {
		if got := problemCode(api.Permitted(tc.p, tc.m)); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPermittedConnectorHintUnchanged(t *testing.T) {
	t.Parallel()
	p := auth.Principal{Kind: auth.KindAgent, Via: "oauth", Scopes: []auth.Scope{auth.ScopeRead}}
	pr := api.Permitted(p, api.Meta{Scope: auth.ScopeWrite})
	if pr == nil || pr.Status != 403 || pr.Details["scope"] != "write" || pr.Details["hint"] == nil {
		t.Fatalf("%+v", pr)
	}
}

func TestAuthorizeIsPermitted(t *testing.T) {
	t.Parallel()
	ctx := auth.WithPrincipal(t.Context(), humanOf(config.RoleListener))
	err := api.Authorize(ctx, api.Meta{Scope: auth.ScopeRead, OwnerOnly: true})
	var pr *api.Problem
	if !errors.As(err, &pr) || pr.Code != "OWNER_ONLY" || pr.Status != 403 {
		t.Fatalf("%v", err)
	}
	if err := api.Authorize(auth.WithPrincipal(t.Context(), humanOf(config.RoleOwner)), api.Meta{Scope: auth.ScopeRead}); err != nil {
		t.Fatal(err)
	}
}

// TestRegisterAcceptsNewScopes: listen and rate are valid op scopes; OwnerOnly is carried into x-mm.
func TestValidateMetaListenRateAndOwnerOnly(t *testing.T) {
	t.Parallel()
	op := huma.Operation{OperationID: "do_thing", Method: "GET", Path: "/api/x", Summary: "s", Description: "d"}
	m := api.Meta{Scope: auth.ScopeListen, Cost: "free", MCP: "tool", UI: "none:test"}
	if err := api.ValidateMeta(op, m); err != nil {
		t.Fatal(err)
	}
	m.Scope = auth.ScopeRate
	if err := api.ValidateMeta(op, m); err != nil {
		t.Fatal(err)
	}
	m.Scope = "bogus"
	if err := api.ValidateMeta(op, m); err == nil {
		t.Fatal("bogus scope must be invalid")
	}
}
