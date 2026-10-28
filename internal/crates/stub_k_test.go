package crates_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// TestStubsK_FrozenMethodSet pins the method set of C125 and checks that the stubs answer errors.ErrUnsupported (the API maps
// it to 501). Lane K deletes stub_k.go and this file.
func TestStubsK_FrozenMethodSet(t *testing.T) {
	t.Parallel()
	var _ interface {
		Create(context.Context, auth.Principal, crates.NewCrate) (crates.Crate, int, error)
		Get(context.Context, string) (crates.Crate, int, error)
		List(context.Context, crates.ListFilter) (crates.Page, error)
		Update(context.Context, auth.Principal, string, crates.Patch) (crates.Crate, int, error)
		EditItems(context.Context, auth.Principal, string, crates.ItemsEdit) (crates.ItemsResult, error)
		ExpandSongs(context.Context, string, songs.ListFilter) (songs.ListFilter, error)
		ExpandTracks(context.Context, string, library.SearchParams) (library.SearchParams, error)
	} = (*crates.Service)(nil)
	s, ctx, p := crates.New(nil, nil, nil, nil), context.Background(), auth.Principal{Kind: auth.KindAgent}
	check := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("%s: %v, want errors.ErrUnsupported", name, err)
		}
	}
	_, _, err := s.Create(ctx, p, crates.NewCrate{})
	check("Create", err)
	_, _, err = s.Get(ctx, "x")
	check("Get", err)
	_, err = s.List(ctx, crates.ListFilter{})
	check("List", err)
	_, _, err = s.Update(ctx, p, "x", crates.Patch{})
	check("Update", err)
	_, err = s.EditItems(ctx, p, "x", crates.ItemsEdit{})
	check("EditItems", err)
	base := songs.ListFilter{Q: "keep"}
	got, err := s.ExpandSongs(ctx, "x", base)
	check("ExpandSongs", err)
	if got.Q != "keep" {
		t.Error("ExpandSongs must hand the base filter back")
	}
	_, err = s.ExpandTracks(ctx, "x", library.SearchParams{})
	check("ExpandTracks", err)
	f, err := crates.ParseFilter(crates.SubjectSong, nil)
	check("ParseFilter", err)
	if f.Subject() != "" || f.Songs().Q != "" || f.Tracks().Q != "" {
		t.Error("a stub filter is empty")
	}
}
