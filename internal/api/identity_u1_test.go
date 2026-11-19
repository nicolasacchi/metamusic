package api_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

// TestRefFor_Identity pins C309 / D-U1-15: the Owner and an Editor see usernames; a Listener sees only their own name and
// every other user as "user"; agents keep the single "human"; devices and system keys render as before.
func TestRefFor_Identity(t *testing.T) {
	t.Parallel()
	owner := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleOwner, ID: "alice", User: "alice"}
	editor := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", User: "bob"}
	listener := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "carol", User: "carol"}
	listenerDev := auth.Principal{Kind: auth.KindDevice, Role: auth.RoleListener, ID: "d9", User: "carol"}
	agent := auth.Principal{Kind: auth.KindAgent, ID: "t1", Name: "bot"}
	h := func(id, name string) api.PrincipalRef { return api.PrincipalRef{Kind: "human", ID: id, Name: name} }
	tests := []struct {
		name    string
		viewer  auth.Principal
		key     string
		kind, n string
		want    api.PrincipalRef
		wantKey string
	}{
		{"owner sees the editor", owner, "human:bob", "human", "bob", h("human:bob", "bob"), "human:bob"},
		{"editor sees the owner", editor, "human:alice", "human", "alice", h("human:alice", "alice"), "human:alice"},
		{"editor sees a listener", editor, "human:carol", "human", "carol", h("human:carol", "carol"), "human:carol"},
		{"listener sees themself", listener, "human:carol", "human", "carol", h("human:carol", "carol"), "human:carol"},
		{"listener sees the owner as user", listener, "human:alice", "human", "alice", h("user", "user"), "user"},
		{"listener sees the editor as user", listener, "human:bob", "human", "bob", h("user", "user"), "user"},
		{"listener device sees its user", listenerDev, "human:carol", "human", "carol", h("user", "user"), "user"},
		{"listener sees a device", listener, "device:d1", "device", "Phone", api.PrincipalRef{Kind: "device", ID: "device:d1", Name: "device"}, "device:d1"},
		{"listener sees an agent", listener, "agent:t1", "agent", "bot", api.PrincipalRef{Kind: "agent", ID: "agent:t1", Name: "bot"}, "agent:t1"},
		{"listener sees system", listener, "system:runner", "system", "runner", api.PrincipalRef{Kind: "system", ID: "system:runner", Name: "runner"}, "system:runner"},
		{"agent sees a user as human", agent, "human:bob", "human", "bob", h("human", "human"), "human"},
		{"listener sees an empty key as user", listener, "", "human", "", h("user", "user"), ""},
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
