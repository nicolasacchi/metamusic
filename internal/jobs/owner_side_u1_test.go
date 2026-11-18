package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// TestProviderUpdateOwnerSide (C302.6): an Editor or Listener cannot arm, disarm or reset the provider, human or device.
func TestProviderUpdateOwnerSide(t *testing.T) {
	t.Parallel()
	a := &ProviderAdmin{}
	for _, role := range []auth.Role{auth.RoleEditor, auth.RoleListener} {
		for _, kind := range []auth.Kind{auth.KindHuman, auth.KindDevice} {
			p := auth.Principal{Kind: kind, Role: role, ID: "x"}
			_, err := a.Update(context.Background(), p, UpdateIn{})
			var pe *problem.Error
			if !errors.As(err, &pe) || pe.Code != "OWNER_ONLY" {
				t.Errorf("%s %s: %v", role, kind, err)
			}
		}
	}
}
