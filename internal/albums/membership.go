package albums

import (
	"context"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// List returns the albums of the given kinds, all of them when none are given, ordered record (first_published_at, id),
// muse (ordinal), unfiled. Only the record kind exists until lane A2: asking for muse or unfiled yields nothing.
func (s *Service) List(ctx context.Context, kinds ...Kind) ([]Album, error) {
	if len(kinds) > 0 && !slices.Contains(kinds, KindRecord) {
		return nil, nil
	}
	pubs, err := s.rec.ResolvedPublished(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Album, 0, len(pubs))
	for _, p := range pubs {
		if a, songs := recordAlbum(p); len(songs) > 0 {
			out = append(out, a)
		}
	}
	return out, nil
}

// Get returns one album and its songs in order; ErrNotFound for an unknown, unpublished, hidden or empty album.
func (s *Service) Get(ctx context.Context, id string) (Album, []Song, error) {
	recID, ok := strings.CutPrefix(id, PrefixRecord)
	if !ok || recID == "" {
		return Album{}, nil, ErrNotFound
	}
	pubs, err := s.rec.ResolvedPublished(ctx)
	if err != nil {
		return Album{}, nil, err
	}
	for _, p := range pubs {
		if p.Record.ID != recID {
			continue
		}
		if a, songs := recordAlbum(p); len(songs) > 0 {
			return a, songs, nil
		}
	}
	return Album{}, nil, ErrNotFound
}

// listable reports whether a slot is a song of the album (C126.1): it resolves to a Take that exists and is not discarded.
// A pinned slot whose Take was discarded is pin_ineligible and stays out, as does an empty floating slot.
func listable(s records.PublishedSlot) bool {
	return s.State == records.StateOK && s.Take != nil && s.Take.Status != "discarded"
}

// recordAlbum builds the album of a published Record and its songs. Track numbers count the listed songs from 1 (a hole
// where an empty slot sits would show in a player as a missing track); Disc is the slot's side, A to D as 1 to 4, and 1 when
// the slot has none. A Take appears once.
func recordAlbum(p records.PublishedRecord) (Album, []Song) {
	var songs []Song
	seen := map[string]bool{}
	for _, s := range p.Slots {
		if !listable(s) || seen[s.Take.ID] {
			continue
		}
		seen[s.Take.ID] = true
		slotID := s.Slot.ID
		songs = append(songs, Song{TakeID: s.Take.ID, Position: len(songs) + 1, Disc: discOf(s.Slot.Side), SlotID: &slotID})
	}
	in := cover.Input{Kind: cover.KindRecord, ID: p.Record.ID, Seed: p.Record.CoverSeed}
	for _, s := range p.Slots {
		if len(in.Parts) == records.CoverParts {
			break
		}
		if s.RootID != "" {
			in.Parts = append(in.Parts, cover.Part{RootID: s.RootID, Seed: s.RootSeed})
		}
	}
	id := p.Record.ID
	return Album{
		ID: PrefixRecord + id, Kind: KindRecord, Name: p.Record.Title, SortName: textfold.Norm(p.Record.Title),
		SongCount: len(songs), CoverInput: in, RecordID: &id,
	}, songs
}

// discOf maps a side letter to a disc number: A is 1 up to D as 4; no side is 1.
func discOf(side *string) int {
	if side == nil {
		return 1
	}
	return max(slices.Index(records.Sides, *side)+1, 1)
}
