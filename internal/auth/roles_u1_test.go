package auth

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
)

func rolesCfg() (config.Config, config.Secrets) {
	c, s := cfg("proxy")
	c.Users = map[string]config.Role{"alice": config.RoleOwner, "bob": config.RoleEditor, "carol": config.RoleListener}
	return c, s
}

func TestProxyRolePerUser(t *testing.T) {
	t.Parallel()
	c, s := rolesCfg()
	for _, tc := range []struct {
		header string
		ok     bool
		role   Role
		user   string
	}{
		{"alice", true, RoleOwner, "alice"},
		{"bob", true, RoleEditor, "bob"},
		{"carol", true, RoleListener, "carol"},
		{"Carol", true, RoleListener, "carol"}, // the header is folded to lowercase
		{"ALICE", true, RoleOwner, "alice"},
		{"mallory", false, "", ""}, // unknown user: anonymous, never a role
		{"alice,bob", false, "", ""},
	} {
		r := proxyReq("10.0.0.1:1")
		r.Header.Set("Remote-User", tc.header)
		rec, p := runP(c, s, r)
		if !tc.ok {
			if rec.Code != http.StatusUnauthorized || p.Kind == KindHuman {
				t.Errorf("%s: want 401 and no human principal, got %d %+v", tc.header, rec.Code, p)
			}
			continue
		}
		if p.Kind != KindHuman || p.Role != tc.role || p.User != tc.user || p.ID != tc.user || !slices.Equal(p.Scopes, ScopesFor(tc.role)) {
			t.Errorf("%s: principal %+v", tc.header, p)
		}
	}
}

func TestProxyHeaderRulesUnchangedForNonOwner(t *testing.T) {
	t.Parallel()
	c, s := rolesCfg()
	for name, mut := range map[string]func(*http.Request){
		"untrusted peer":   func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1" },
		"wrong edge":       func(r *http.Request) { r.Header.Set("X-MM-Edge", "nope") },
		"duplicate edge":   func(r *http.Request) { r.Header.Add("X-MM-Edge", edge) },
		"duplicate remote": func(r *http.Request) { r.Header.Add("Remote-User", "carol") },
	} {
		r := proxyReq("10.0.0.1:1")
		r.Header.Set("Remote-User", "carol")
		mut(r)
		if rec, p := runP(c, s, r); rec.Code != http.StatusUnauthorized || p.Kind == KindHuman {
			t.Errorf("%s: want 401, got %d %+v", name, rec.Code, p)
		}
	}
}

func TestScopesForAndHasImplication(t *testing.T) {
	t.Parallel()
	want := map[Role][]Scope{
		RoleOwner:    {ScopeListen, ScopeRead, ScopeRate, ScopeWrite, ScopeGenerate, ScopeAdmin},
		RoleEditor:   {ScopeListen, ScopeRead, ScopeRate, ScopeWrite, ScopeGenerate},
		RoleListener: {ScopeListen, ScopeRate},
	}
	for r, w := range want {
		if got := ScopesFor(r); !slices.Equal(got, w) {
			t.Errorf("ScopesFor(%s) = %v, want %v", r, got, w)
		}
	}
	if ScopesFor("") != nil || ScopesFor("root") != nil {
		t.Error("unknown role must hold no scope")
	}
	ScopesFor(RoleOwner)[0] = "x" // the slice is fresh
	if ScopesFor(RoleOwner)[0] != ScopeListen {
		t.Error("ScopesFor must return a fresh slice")
	}
	for _, tc := range []struct {
		have []Scope
		s    Scope
		want bool
	}{
		{[]Scope{ScopeRead}, ScopeListen, true},
		{[]Scope{ScopeWrite}, ScopeRate, true},
		{[]Scope{ScopeListen}, ScopeRead, false},
		{[]Scope{ScopeRate}, ScopeWrite, false},
		{[]Scope{ScopeRead}, ScopeRate, false},
		{[]Scope{ScopeWrite}, ScopeListen, false},
		{[]Scope{ScopeAdmin}, ScopeListen, false},
		{[]Scope{ScopeRead, ScopeWrite}, ScopeGenerate, false},
	} {
		if got := (Principal{Scopes: tc.have}).Has(tc.s); got != tc.want {
			t.Errorf("Has(%v, %s) = %v, want %v", tc.have, tc.s, got, tc.want)
		}
	}
}

func TestRolePredicates(t *testing.T) {
	t.Parallel()
	mk := func(k Kind, r Role) Principal { return Principal{Kind: k, Role: r} }
	for _, tc := range []struct {
		name                                 string
		p                                    Principal
		person, owner, ownerSide, see, agent bool
	}{
		{"owner human", mk(KindHuman, RoleOwner), true, true, true, true, false},
		{"editor human", mk(KindHuman, RoleEditor), true, false, false, true, false},
		{"listener human", mk(KindHuman, RoleListener), true, false, false, false, false},
		{"owner device (no role yet)", mk(KindDevice, ""), true, false, true, true, false},
		{"editor device", mk(KindDevice, RoleEditor), true, false, false, true, false},
		{"listener device", mk(KindDevice, RoleListener), true, false, false, false, false},
		{"agent", mk(KindAgent, ""), false, false, true, true, true},
		{"system", mk(KindSystem, ""), false, false, true, true, false},
	} {
		if tc.p.IsPerson() != tc.person || tc.p.IsOwner() != tc.owner || tc.p.IsOwnerSide() != tc.ownerSide ||
			tc.p.CanSeePrivate() != tc.see || tc.p.IsAgent() != tc.agent {
			t.Errorf("%s: person=%v owner=%v ownerSide=%v see=%v agent=%v", tc.name, tc.p.IsPerson(), tc.p.IsOwner(), tc.p.IsOwnerSide(), tc.p.CanSeePrivate(), tc.p.IsAgent())
		}
	}
}

func TestRaterKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		p    Principal
		want string
	}{
		{Principal{Kind: KindHuman, Role: RoleOwner, User: "alice"}, "owner"},
		{Principal{Kind: KindHuman, Role: RoleEditor, User: "bob"}, "bob"},
		{Principal{Kind: KindHuman, Role: RoleListener, User: "carol"}, "carol"},
		{Principal{Kind: KindDevice, ID: "d1"}, "owner"},
		{Principal{Kind: KindAgent, ID: "t1"}, "critic"},
		{Principal{Kind: KindSystem, ID: "cli"}, ""},
		{Principal{Kind: KindAnonymous}, ""},
	} {
		if got := RaterKey(tc.p); got != tc.want {
			t.Errorf("RaterKey(%+v) = %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestCreatePairingCodeOwnerOnly(t *testing.T) {
	t.Parallel()
	ds, _ := newDevices(t)
	for _, role := range []Role{RoleEditor, RoleListener} {
		by := Principal{Kind: KindHuman, ID: "human:bob", User: "bob", Role: role}
		if _, _, err := ds.CreatePairingCode(t.Context(), by, "Phone"); !errors.Is(err, ErrNotHuman) {
			t.Errorf("%s: err = %v, want ErrNotHuman", role, err)
		}
	}
	owner := Principal{Kind: KindHuman, ID: "human:alice", User: "alice", Role: RoleOwner}
	if _, _, err := ds.CreatePairingCode(t.Context(), owner, "Phone"); err != nil {
		t.Errorf("owner: %v", err)
	}
}
