package library_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/view"
)

// TestVisible: a discarded take is not found for a viewer that hides discarded takes, whatever the reader (by id, search,
// facets, siblings, refs); the Owner, the Editor, an agent and a bare context see it (C308.2).
func TestVisible(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	exec(t, e, `UPDATE tracks SET status = 'discarded' WHERE id = ?`, lt.TrackTwinB)
	owner, editor, carol := asUser(auth.RoleOwner, "alice"), asUser(auth.RoleEditor, "bob"), asUser(auth.RoleListener, "carol")
	agent := auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindAgent, ID: "tok", Name: "tok"})
	for name, ctx := range map[string]context.Context{"owner": owner, "editor": editor, "agent": agent, "bare": context.Background()} {
		if err := e.svc.Visible(ctx, lt.TrackTwinB); err != nil {
			t.Errorf("%s: Visible: %v", name, err)
		}
		if _, err := e.svc.Track(ctx, lt.TrackTwinB); err != nil {
			t.Errorf("%s: Track: %v", name, err)
		}
	}
	if err := e.svc.Visible(carol, lt.TrackTwinB); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("listener Visible: %v", err)
	}
	if err := e.svc.Visible(carol, "no-such-id"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("listener Visible of a missing id: %v", err)
	}
	if err := e.svc.Visible(carol, lt.TrackTwinA); err != nil {
		t.Errorf("listener Visible of a live take: %v", err)
	}
	if err := library.Visible(carol, e.st.R, view.FromContext(carol), lt.TrackTwinB); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("package Visible: %v", err)
	}
	if _, err := e.svc.Track(carol, lt.TrackTwinB); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("listener Track: %v", err)
	}
	if _, err := e.svc.Peaks(carol, lt.TrackTwinB, ""); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("listener Peaks: %v", err)
	}
	if _, err := e.svc.Media(carol, lt.TrackTwinB); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("listener Media: %v", err)
	}
	// siblings of its twin
	tr, err := e.svc.Track(carol, lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tr.Lineage.Siblings {
		if s.ID == lt.TrackTwinB {
			t.Errorf("siblings list the discarded take to a Listener")
		}
	}
	// search: explicit status filter or not, the Listener never gets it
	for _, p := range []library.SearchParams{{}, {Status: []string{"discarded"}}, {Q: "Twin"}} {
		page, err := e.svc.SearchTracks(carol, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range page.Items {
			if it.ID == lt.TrackTwinB {
				t.Errorf("search %+v lists the discarded take to a Listener", p)
			}
		}
		if p.Status != nil && page.Total != 0 {
			t.Errorf("status=discarded total for a Listener: %d", page.Total)
		}
	}
	if page, err := e.svc.SearchTracks(editor, library.SearchParams{Status: []string{"discarded"}}); err != nil || page.Total != 1 {
		t.Errorf("the Editor's status=discarded: %v %v", page.Total, err)
	}
	// Listener q reads title and lyrics, not the style or request id columns
	if page, _ := e.svc.SearchTracks(carol, library.SearchParams{Q: "fx-twin"}); page.Total != 0 {
		t.Errorf("a Listener's q matched the request id: %d", page.Total)
	}
	if page, _ := e.svc.SearchTracks(owner, library.SearchParams{Q: "fx-twin"}); page.Total == 0 {
		t.Errorf("the Owner's q lost request-id matching")
	}
	// facets
	statuses := func(ctx context.Context) map[string]int {
		f, err := e.svc.Facets(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, s := range f.Statuses {
			out[s.Status] = s.Count
		}
		return out
	}
	if statuses(carol)["discarded"] != 0 || statuses(owner)["discarded"] != 1 {
		t.Errorf("facet status counts: listener %v, owner %v", statuses(carol), statuses(owner))
	}
	// refs (used by clips, downloads, ears, records)
	refs, err := e.svc.TrackRefs(carol, []string{lt.TrackTwinA, lt.TrackTwinB})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := refs[lt.TrackTwinB]; ok || len(refs) != 1 {
		t.Errorf("TrackRefs for a Listener: %v", refs)
	}
}
