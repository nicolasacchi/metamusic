package generation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// TestSetCaps_OwnerSide (C302.6): caps change only for Owner-side principals.
func TestSetCaps_OwnerSide(t *testing.T) {
	t.Parallel()
	s := &generation.Service{}
	for _, role := range []auth.Role{auth.RoleEditor, auth.RoleListener} {
		_, err := s.SetCaps(context.Background(), auth.Principal{Kind: auth.KindHuman, Role: role, ID: "x"}, spend.CapsPatch{})
		var pe *problem.Error
		if !errors.As(err, &pe) || pe.Code != "OWNER_ONLY" {
			t.Errorf("%s: %v", role, err)
		}
	}
}
