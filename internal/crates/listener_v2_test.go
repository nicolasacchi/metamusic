package crates_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// TestExpandListenerBannedFilter pins the V1 hand-off (b): a smart Crate whose stored filter holds a C308.3b filter is refused
// to a Listener (422 FILTER_NOT_ALLOWED naming crate_id, never the stored key) and expands for everyone else; a smart Crate of
// harmless filters expands for a Listener too; a manual Crate is untouched.
func TestExpandListenerBannedFilter(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	carol := auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener})
	bob := auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "bob", User: "bob", Role: auth.RoleEditor})
	notAllowed := func(err error) bool {
		var pe *problem.Error
		return err != nil && errors.As(err, &pe) && pe.Code == "FILTER_NOT_ALLOWED" && pe.Details["filter"] == "crate_id"
	}
	song := w.crate("Synthetic Unheard Reggae") // its stored filter has unheard
	take := w.crate("Synthetic Unheard Drafts")
	for _, ctx := range []context.Context{w.ctx, bob} {
		if _, err := w.svc.ExpandSongs(ctx, song, songs.ListFilter{}); err != nil {
			t.Errorf("a smart Song crate for the Owner side: %v", err)
		}
		if _, err := w.svc.ExpandTracks(ctx, take, library.SearchParams{}); err != nil {
			t.Errorf("a smart Take crate for the Owner side: %v", err)
		}
	}
	if _, err := w.svc.ExpandSongs(carol, song, songs.ListFilter{}); !notAllowed(err) {
		t.Errorf("a Listener expands a smart Song crate with a hidden filter: %v", err)
	}
	if _, err := w.svc.ExpandTracks(carol, take, library.SearchParams{}); !notAllowed(err) {
		t.Errorf("a Listener expands a smart Take crate with a hidden filter: %v", err)
	}
	if _, err := w.svc.ExpandSongs(carol, w.crate("Synthetic Remake Candidates"), songs.ListFilter{}); err != nil {
		t.Errorf("a manual crate for a Listener: %v", err)
	}
	for key, raw := range map[string]string{"muse": `{"no_muse":true}`, "angle": `{"angle":"a"}`, "session": `{"session_id":"x"}`} {
		c, _, err := w.svc.Create(w.ctx, human, crates.NewCrate{Name: "Banned " + key, Kind: crates.KindSmart, Subject: crates.SubjectSong, Filter: json.RawMessage(raw)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.ExpandSongs(carol, c.ID, songs.ListFilter{}); !notAllowed(err) {
			t.Errorf("%s: a Listener expands it: %v", key, err)
		}
	}
	ok, _, err := w.svc.Create(w.ctx, human, crates.NewCrate{Name: "Harmless", Kind: crates.KindSmart, Subject: crates.SubjectSong, Filter: json.RawMessage(`{"state":["rendered"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.ExpandSongs(carol, ok.ID, songs.ListFilter{}); err != nil {
		t.Errorf("a harmless smart crate for a Listener: %v", err)
	}
}

// TestListenerCountAndWarningsNoOracle pins that a Listener gets neither a count nor a warning derived from a banned
// stored filter (C308.3b), while the Owner side keeps both.
func TestListenerCountAndWarningsNoOracle(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	carol := auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener})
	c, _, err := w.svc.Create(w.ctx, human, crates.NewCrate{Name: "Hidden session", Kind: crates.KindSmart, Subject: crates.SubjectSong, Filter: json.RawMessage(`{"session_id":"gone"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if warn, err := w.svc.Warnings(w.ctx, c); err != nil || len(warn) == 0 {
		t.Fatalf("owner warnings = %v, %v; want one", warn, err)
	}
	if warn, err := w.svc.Warnings(carol, c); err != nil || len(warn) != 0 {
		t.Errorf("listener warnings = %v, %v; want none", warn, err)
	}
	unheard := w.crate("Synthetic Unheard Reggae")
	if _, n, err := w.svc.Get(w.ctx, unheard); err != nil || n == 0 {
		t.Fatalf("owner count = %d, %v; want > 0", n, err)
	}
	if _, n, err := w.svc.Get(carol, unheard); err != nil || n != 0 {
		t.Errorf("listener count = %d, %v; want 0", n, err)
	}
}
