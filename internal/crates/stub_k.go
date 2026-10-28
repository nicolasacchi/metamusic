package crates

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// STUB(L2-K): the frozen method set of C125. Lane K deletes this file and adds service.go behaviour in list.go, items.go,
// expand.go and count.go; no signature changes. Every stub answers errors.ErrUnsupported, which the API maps to 501.

// Create makes a Crate and returns it with its count (manual: items; smart: the matches now).
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewCrate) (Crate, int, error) {
	return Crate{}, 0, errors.ErrUnsupported
}

// Get returns a Crate with its count.
func (s *Service) Get(ctx context.Context, id string) (Crate, int, error) {
	return Crate{}, 0, errors.ErrUnsupported
}

// List returns Crates ordered by (updated_at DESC, id DESC) with a keyset cursor.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	return Page{}, errors.ErrUnsupported
}

// Update changes metadata or the filter under the revision lock and returns the Crate with its count.
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Crate, int, error) {
	return Crate{}, 0, errors.ErrUnsupported
}

// EditItems adds and removes members of a manual Crate; item edits do not bump the revision.
func (s *Service) EditItems(ctx context.Context, p auth.Principal, id string, e ItemsEdit) (ItemsResult, error) {
	return ItemsResult{}, errors.ErrUnsupported
}

// ExpandSongs merges a Crate into a Song list filter: a membership join for a manual Crate, the stored filter (AND, with
// FILTER_CONFLICT on a clash) for a smart one.
func (s *Service) ExpandSongs(ctx context.Context, crateID string, base songs.ListFilter) (songs.ListFilter, error) {
	return base, errors.ErrUnsupported
}

// ExpandTracks is ExpandSongs for Take searches.
func (s *Service) ExpandTracks(ctx context.Context, crateID string, base library.SearchParams) (library.SearchParams, error) {
	return base, errors.ErrUnsupported
}

// ParseFilter validates a saved filter of the subject against the vocabulary (C125.2): unknown key, wrong type, empty
// array, crate_id inside, or more than MaxFilterBytes are refused.
func ParseFilter(subject Subject, raw json.RawMessage) (Filter, error) {
	return Filter{}, errors.ErrUnsupported
}

// Songs maps a parsed Song filter onto the filter struct of list_songs.
func (f Filter) Songs() songs.ListFilter { return f.songs }

// Tracks maps a parsed Take filter onto the search parameters of search_tracks.
func (f Filter) Tracks() library.SearchParams { return f.tracks }
