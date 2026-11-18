package records_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// TestPublish_OnlyTheOwner: publishing is refused for an Editor and a Listener before anything is read (U1 C302.6).
func TestPublish_OnlyTheOwner(t *testing.T) {
	t.Parallel()
	s := &records.Service{}
	for _, role := range []auth.Role{auth.RoleEditor, auth.RoleListener} {
		p := auth.Principal{Kind: auth.KindHuman, Role: role, ID: "bob"}
		_, err := s.Publish(context.Background(), p, "r1", 1, true)
		if err == nil {
			t.Errorf("%s: want a refusal, got %v", role, err)
		}
	}
}
