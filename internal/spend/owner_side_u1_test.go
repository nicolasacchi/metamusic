package spend

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
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

// TestNeedsApproval_EditorBatch (D-U1-11, C310.2): an Editor, human or device, always needs the Owner, reason editor_batch, on
// either path, with zero takes or a zero ceiling, and whatever the auto-approve limits; the Owner's and agents' rules hold.
func TestNeedsApproval_EditorBatch(t *testing.T) {
	t.Parallel()
	roomy := Caps{AutoApproveTakes: 100, AutoApproveMicro: 1 << 40}
	tokRoomy := TokenLimits{AutoApproveTakes: new(int), AutoApproveMicro: new(money.MicroUSD)}
	*tokRoomy.AutoApproveTakes, *tokRoomy.AutoApproveMicro = 100, 1<<40
	for _, kind := range []auth.Kind{auth.KindHuman, auth.KindDevice} {
		p := auth.Principal{Kind: kind, Role: auth.RoleEditor, ID: "bob"}
		for _, via := range []string{ViaHumanCreate, ViaAPI} {
			for _, tok := range []TokenLimits{{}, tokRoomy} {
				n := NeedsApproval(p, via, 0, 0, tok, roomy)
				if !n.Needed || len(n.Reasons) != 1 || n.Reasons[0] != ReasonEditorBatch {
					t.Errorf("%s via %s: %+v", kind, via, n)
				}
			}
		}
	}
	owner := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleOwner, ID: "alice"}
	if n := NeedsApproval(owner, ViaHumanCreate, 3, 10, TokenLimits{}, roomy); n.Needed {
		t.Errorf("owner human_create: %+v", n)
	}
	if n := NeedsApproval(owner, ViaAPI, 3, 10, TokenLimits{}, roomy); !n.Needed || n.Reasons[0] != ReasonHumanAPIPath {
		t.Errorf("owner api: %+v", n)
	}
	agent := auth.Principal{Kind: auth.KindAgent, ID: "tok"}
	if n := NeedsApproval(agent, ViaAPI, 3, 10, TokenLimits{}, roomy); n.Needed {
		t.Errorf("agent within limits: %+v", n)
	}
}
