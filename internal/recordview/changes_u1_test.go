package recordview_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/recordview"
)

func asRole(role auth.Role, name string) (context.Context, auth.Principal) {
	p := auth.Principal{Kind: auth.KindHuman, ID: name, User: name, Name: name, Role: role, Scopes: auth.ScopesFor(role), Via: "local"}
	return auth.WithPrincipal(context.Background(), p), p
}

// TestRecordView_ChangesHideDiscardedTakes: once a take a changelog row names is discarded, a Listener's changelog (by
// Get and by ChangeViews) names no such take and its text carries no title; the Owner's changelog is what it was (C308.2).
func TestRecordView_ChangesHideDiscardedTakes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := idOf(t, r, "records", r1Title)
	song := idOf(t, r, "songs", "Synthetic Song S3")
	b2 := idOf(t, r, "tracks", "Synthetic Take B2")
	if resp := r.Human(t).Do(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": b2}); resp.Status != 200 {
		t.Fatalf("pin: %d %s", resp.Status, resp.Body)
	}
	r.Settle(t)
	page, err := r.Services.Records.Changes(context.Background(), recordsFilter(id))
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("changes: %v %v", page.Items, err)
	}
	if _, err := r.Store.W.Exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, b2); err != nil {
		t.Fatal(err)
	}
	owner, ownerP := asRole(auth.RoleOwner, "alice")
	listener, listenerP := asRole(auth.RoleListener, "carol")
	opts := recordview.GetOptions{Include: []string{recordview.IncludeChanges}}
	namesB2 := func(cvs []recordview.ChangeView) (named bool) {
		for _, cv := range cvs {
			for _, ref := range []*recordview.TakeRef{cv.From, cv.To} {
				if ref != nil && ref.ID == b2 {
					named = true
				}
			}
		}
		return named
	}
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		p        auth.Principal
		wantName bool
	}{
		{"owner", owner, ownerP, true},
		{"listener", listener, listenerP, false},
	} {
		v, err := r.Services.RecordView.Get(tc.ctx, tc.p, id, opts)
		if err != nil {
			t.Fatalf("%s Get: %v", tc.name, err)
		}
		var onB2 int
		for _, sl := range v.Slots {
			if sl.Take != nil && sl.Take.ID == b2 {
				onB2++
				if sl.Take.Title == "" {
					t.Errorf("%s Get: slot on the discarded take has no title", tc.name)
				}
			}
		}
		if (onB2 == 1) != tc.wantName {
			t.Errorf("%s Get: slots on the discarded take = %d, want %v", tc.name, onB2, tc.wantName)
		}
		if len(v.Changes) != len(page.Items) {
			t.Errorf("%s Get: %d rows, want %d (rows stay, takes go)", tc.name, len(v.Changes), len(page.Items))
		}
		if named := namesB2(v.Changes); named != tc.wantName {
			t.Errorf("%s Get names the discarded take = %v, want %v", tc.name, named, tc.wantName)
		}
		vs, err := r.Services.RecordView.ChangeViews(tc.ctx, page.Items)
		if err != nil || len(vs) != len(page.Items) {
			t.Fatalf("%s ChangeViews: %d %v", tc.name, len(vs), err)
		}
		if named := namesB2(vs); named != tc.wantName {
			t.Errorf("%s ChangeViews names the discarded take = %v, want %v", tc.name, named, tc.wantName)
		}
	}
	// the input rows are never mutated by the hiding
	var still bool
	for _, c := range page.Items {
		still = still || (c.ToTakeID != nil && *c.ToTakeID == b2) || (c.FromTakeID != nil && *c.FromTakeID == b2)
	}
	if !still {
		t.Error("the caller's rows lost the take id")
	}
}
