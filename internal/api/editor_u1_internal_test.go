package api

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// An Editor sees the run environment (GPU class, image digest) of its own jobs only; the Owner side sees all.
func TestRedactJob_Env(t *testing.T) {
	t.Parallel()
	bob := auth.Principal{Kind: auth.KindHuman, ID: "bob", Role: auth.RoleEditor}
	owner := auth.Principal{Kind: auth.KindHuman, ID: "alice", Role: auth.RoleOwner}
	for _, tc := range []struct {
		name    string
		v       auth.Principal
		creator string
		want    bool
	}{
		{"own", bob, bob.Key(), true},
		{"other", bob, "human:carol", false},
		{"owner", owner, "human:carol", true},
	} {
		j := Job{Env: &library.Env{}}
		redactJob(tc.v, tc.creator, &j)
		if (j.Env != nil) != tc.want {
			t.Errorf("%s: env present = %v, want %v", tc.name, j.Env != nil, tc.want)
		}
	}
}
