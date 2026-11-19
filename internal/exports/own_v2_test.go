package exports_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// TestGetIsOwnForEditorsAndListeners pins C305: get_export answers an Editor or a Listener only for an export it made, with
// the 404 of a missing id otherwise; the Owner side (the Owner, an agent, the Owner's device) reads every export.
func TestGetIsOwnForEditorsAndListeners(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	bob := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", User: "bob", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	carol := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "carol", Name: "carol", User: "carol", Scopes: []auth.Scope{auth.ScopeListen}}
	device := auth.Principal{Kind: auth.KindDevice, ID: "d1", Name: "phone", Scopes: []auth.Scope{auth.ScopeRead}}
	mine, err := e.svc.JSONL(ctx, bob, songs, "")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := e.svc.JSONL(ctx, human, songs, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, bob, mine.ID); err != nil {
		t.Errorf("an Editor's own export: %v", err)
	}
	_, err = e.svc.Get(ctx, bob, theirs.ID)
	problem(t, err, 404, "NOT_FOUND")
	_, missing := e.svc.Get(ctx, bob, "nope")
	if err.Error() != missing.Error() {
		t.Errorf("a foreign export is distinguishable from a missing one: %v vs %v", err, missing)
	}
	_, err = e.svc.Get(ctx, carol, mine.ID)
	problem(t, err, 404, "NOT_FOUND")
	for name, p := range map[string]auth.Principal{"owner": human, "agent": agent, "owner's device": device} {
		for _, id := range []string{mine.ID, theirs.ID} {
			if _, err := e.svc.Get(ctx, p, id); err != nil {
				t.Errorf("%s reads %s: %v", name, id, err)
			}
		}
	}
}
