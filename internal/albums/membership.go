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
// muse (ordinal), unfiled. Without a Muse directory only the record kind exists (the auto-kinds need the Muses' neutral
// facts), so asking for muse or unfiled then yields nothing.
func (s *Service) List(ctx context.Context, kinds ...Kind) ([]Album, error) {
	es, err := s.ListWithSongs(ctx, kinds...)
	if err != nil {
		return nil, err
	}
	out := make([]Album, len(es))
	for i, e := range es {
		out[i] = e.Album
	}
	return out, nil
}

// Entry is one album with its songs in order.
type Entry struct {
	Album
	Songs []Song
}

// ListWithSongs is List with every album's songs, in a constant number of statements (one published-Records read, one
// Muse-facts read, one Pick read, plus one Labels call when the opt-in is on): the Subsonic consumer's list path.
func (s *Service) ListWithSongs(ctx context.Context, kinds ...Kind) ([]Entry, error) {
	want := func(k Kind) bool { return len(kinds) == 0 || slices.Contains(kinds, k) }
	var out []Entry
	if want(KindRecord) {
		pubs, err := s.rec.ResolvedPublished(ctx)
		if err != nil {
			return nil, err
		}
		out = make([]Entry, 0, len(pubs))
		for _, p := range pubs {
			if a, songs := recordAlbum(p); len(songs) > 0 {
				out = append(out, Entry{a, songs})
			}
		}
	}
	auto, err := s.auto(ctx, want(KindMuse), want(KindUnfiled), "")
	if err != nil {
		return nil, err
	}
	for _, a := range auto {
		out = append(out, Entry{a.Album, a.songs})
	}
	return out, nil
}

// auto builds the named auto-albums wanted; nothing without a directory.
func (s *Service) auto(ctx context.Context, wantMuse, wantUnfiled bool, only string) ([]autoAlbum, error) {
	if s.muses == nil || !wantMuse && !wantUnfiled {
		return nil, nil
	}
	auto, err := s.autoAlbums(ctx, wantMuse, wantUnfiled)
	if err != nil {
		return nil, err
	}
	if only != "" { // Get names (and so may label) just the one album it returns
		auto = slices.DeleteFunc(auto, func(a autoAlbum) bool { return a.ID != only })
	}
	if len(auto) == 0 {
		return nil, nil
	}
	if err := s.applyNames(ctx, auto); err != nil {
		return nil, err
	}
	return auto, nil
}

// Get returns one album and its songs in order; ErrNotFound for an unknown, unpublished, hidden or empty album.
func (s *Service) Get(ctx context.Context, id string) (Album, []Song, error) {
	if id == IDUnfiled || strings.HasPrefix(id, PrefixMuse) {
		return s.getAuto(ctx, id)
	}
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

// getAuto finds one auto-album by id; its Labels call (opt-in only) covers that one Muse.
func (s *Service) getAuto(ctx context.Context, id string) (Album, []Song, error) {
	if s.muses == nil || id == PrefixMuse {
		return Album{}, nil, ErrNotFound
	}
	auto, err := s.auto(ctx, id != IDUnfiled, id == IDUnfiled, id)
	if err != nil {
		return Album{}, nil, err
	}
	for _, a := range auto {
		if a.ID == id {
			return a.Album, a.songs, nil
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
