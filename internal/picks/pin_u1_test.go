package picks_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/picks"
)

// TestPin_EditorListenerRefused: pinning is for the Owner and the Owner's devices (U1 C302.6); the refusal comes before any read.
func TestPin_EditorListenerRefused(t *testing.T) {
	t.Parallel()
	s := &picks.Service{}
	take := "t1"
	for _, role := range []auth.Role{auth.RoleEditor, auth.RoleListener} {
		for _, kind := range []auth.Kind{auth.KindHuman, auth.KindDevice} {
			if _, err := s.Pin(context.Background(), auth.Principal{Kind: kind, Role: role, ID: "x"}, "s1", &take, nil); err == nil {
				t.Errorf("%s %s: Pin must be refused", role, kind)
			}
		}
	}
}
