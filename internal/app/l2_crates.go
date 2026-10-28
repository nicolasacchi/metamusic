package app

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// crateCounter adapts the Song and Take lists to crates.Counter: the live match count is the list's own COUNT (one row of work, no page),
// so a Crate view and an ad-hoc query run the same Go filter (D-L2-13).
type crateCounter struct {
	songs *songs.Service
	lib   *library.Service
}

func (c crateCounter) CountSongs(ctx context.Context, f songs.ListFilter) (int, error) {
	return c.songs.Count(ctx, f)
}

func (c crateCounter) CountTracks(ctx context.Context, p library.SearchParams) (int, error) {
	p.Limit, p.Cursor = 1, ""
	page, err := c.lib.SearchTracks(ctx, p)
	return page.Total, err
}

// buildL2Crates wires the Crates service (lane K owns this file).
func buildL2Crates(in l2Inputs, s *l2Services) {
	s.Crates = crates.New(in.st, crateCounter{songs: in.songs, lib: in.lib}, in.now, in.newID)
}
