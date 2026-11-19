package spend

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func isOwnerOnly(err error) bool {
	var pe *problem.Error
	return errors.As(err, &pe) && pe.Status == 403 && pe.Code == "OWNER_ONLY"
}

// TestOwnerSideGuards (C302.6): approving, rejecting a batch and changing caps refuse an Editor or a Listener, human or
// device, before touching the database (the zero Service and nil tx would panic otherwise).
func TestOwnerSideGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := &Service{}
	for _, role := range []auth.Role{auth.RoleEditor, auth.RoleListener} {
		for _, kind := range []auth.Kind{auth.KindHuman, auth.KindDevice} {
			p := auth.Principal{Kind: kind, Role: role, ID: "x", Scopes: auth.ScopesFor(role)}
			if _, err := s.ApproveBatch(ctx, nil, "b", p, ""); !isOwnerOnly(err) {
				t.Errorf("%s %s ApproveBatch: %v", role, kind, err)
			}
			if _, err := s.RejectBatch(ctx, nil, "b", p, ""); !isOwnerOnly(err) {
				t.Errorf("%s %s RejectBatch: %v", role, kind, err)
			}
			if _, _, err := s.SetCaps(ctx, nil, p, CapsPatch{}); !isOwnerOnly(err) {
				t.Errorf("%s %s SetCaps: %v", role, kind, err)
			}
		}
	}
}
