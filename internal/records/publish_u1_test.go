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

// TestRecords_EditorEditsDraftsOnly (C305): an Editor, human or paired device, edits a draft Record (text and slots) and is
// refused OWNER_ONLY on a published one, with nothing written.
func TestRecords_EditorEditsDraftsOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	bob := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", User: "bob"}
	bobPhone := auth.Principal{Kind: auth.KindDevice, Role: auth.RoleEditor, ID: "d1", Name: "phone", User: "bob"}
	pub := r.get(r.fx.Records["R1"]) // published
	rows := len(r.changes(pub.ID))
	for _, p := range []auth.Principal{bob, bobPhone} {
		_, err := r.svc.Update(ctx, p, pub.ID, records.Patch{Revision: pub.Revision, Subtitle: sp("by " + p.Name)})
		wantProblem(t, err, 403, "OWNER_ONLY")
		_, err = r.svc.EditSlots(ctx, p, pub.ID, pub.Revision, []records.SlotOp{{Op: "set_side", SlotID: r.fx.Slots["A"], Side: records.OptString{Set: true, Value: "C"}}})
		wantProblem(t, err, 403, "OWNER_ONLY")
	}
	if n := len(r.changes(pub.ID)); n != rows {
		t.Errorf("refused edits changed the changelog: %d rows, want %d", n, rows)
	}
	d := r.get(r.fx.Records["R2"]) // draft
	if _, err := r.svc.Update(ctx, bob, d.ID, records.Patch{Revision: d.Revision, Subtitle: sp("bob's liner")}); err != nil {
		t.Errorf("Editor Update of a draft: %v", err)
	}
}
