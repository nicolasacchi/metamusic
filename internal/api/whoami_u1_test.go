package api_test

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// C304.3: whoami reports role, user and allowed_ops, and allowed_ops is exactly the operations Permitted lets the
// caller through, per role. The SPA derives its Editor and Listener shells from it.
func TestWhoamiRoleAndAllowedOps(t *testing.T) {
	t.Parallel()
	rig := servertest.New(t, servertest.Options{})
	ops := api.Operations(rig.API)
	allowedFor := func(p auth.Principal) []string {
		out := []string{}
		for _, op := range ops {
			if api.Permitted(p, op.Meta) == nil {
				out = append(out, op.ID)
			}
		}
		slices.Sort(out)
		return out
	}
	rowOf := func(name string) auth.Principal {
		for _, r := range matrixRows {
			if r.Name == name {
				return r.P
			}
		}
		t.Fatalf("no matrix row %s", name)
		return auth.Principal{}
	}
	agentClient, _ := rig.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin)
	for _, tc := range []struct {
		name       string
		c          *servertest.Client
		row        string
		role, user any
	}{
		{"owner", rig.Human(t), "owner", "owner", "alice"},
		{"editor", rig.User(t, servertest.UserEditor, config.RoleEditor), "editor", "editor", "bob"},
		{"listener", rig.User(t, servertest.UserListener, config.RoleListener), "listener", "listener", "carol"},
		{"agent", agentClient, "agent-admin", nil, nil},
	} {
		m := respObj(t, tc.c.Do(t, "GET", "/api/me", nil))
		if m["role"] != tc.role || m["user"] != tc.user {
			t.Errorf("%s: role=%v user=%v, want %v %v", tc.name, m["role"], m["user"], tc.role, tc.user)
		}
		raw, ok := m["allowed_ops"].([]any)
		if !ok {
			t.Fatalf("%s: allowed_ops missing or not a list: %v", tc.name, m["allowed_ops"])
		}
		got := make([]string, len(raw))
		for i, v := range raw {
			got[i], _ = v.(string)
		}
		if want := allowedFor(rowOf(tc.row)); !slices.Equal(got, want) {
			t.Errorf("%s: allowed_ops differ from Permitted:\n got %d ops\nwant %d ops", tc.name, len(got), len(want))
		}
		if tc.name == "listener" {
			for _, id := range []string{"get_audit", "approve_batch", "create_generation", "get_spend"} {
				if slices.Contains(got, id) {
					t.Errorf("listener allowed_ops lists %s", id)
				}
			}
			for _, id := range []string{"whoami", "search_tracks", "rate_track"} {
				if !slices.Contains(got, id) {
					t.Errorf("listener allowed_ops lacks %s", id)
				}
			}
		}
	}
}
