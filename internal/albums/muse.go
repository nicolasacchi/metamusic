package albums

import (
	"context"
	"fmt"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// autoRow is one listed Take of an auto-album: the Pick of a live, non-hidden Song that no published Record holds.
type autoRow struct {
	museID  string // "" for a Song with no Muse
	takeID  string
	rootID  string
	rootSed int64
}

// autoRows reads every candidate Pick in ONE statement, ordered by Song (created_at, id): the Pick (song_picks.take_id) of each
// live (not retired), non-hidden Song, never a discarded Take, for Songs with no live slot in a published, non-hidden Record
// (those are in the catalog already as a record album, and a hidden Record is not one). Muse text is not read: songs.muse_id is
// the only Muse column touched, and the Muse tables stay behind MuseDirectory (D-L2-16).
func (s *Service) autoRows(ctx context.Context) ([]autoRow, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT COALESCE(g.muse_id, ''), sp.take_id, COALESCE(g.root_track_id, g.draft_id, ''), COALESCE(rc.seed, 0)
		FROM songs g
		JOIN song_picks sp ON sp.song_id = g.id AND sp.take_id IS NOT NULL
		JOIN tracks pt ON pt.id = sp.take_id AND pt.status <> 'discarded'
		LEFT JOIN tracks rt ON rt.id = g.root_track_id
		LEFT JOIN recipes rc ON rc.id = rt.recipe_id
		WHERE g.hidden_at IS NULL AND g.retired_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM record_slots sl JOIN records r ON r.id = sl.record_id
		                  WHERE sl.song_id = g.id AND sl.removed_at IS NULL AND r.status = 'published' AND r.hidden_at IS NULL)
		ORDER BY g.created_at, g.id`)
	if err != nil {
		return nil, fmt.Errorf("albums: auto-albums: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []autoRow
	for rows.Next() {
		var r autoRow
		if err := rows.Scan(&r.museID, &r.takeID, &r.rootID, &r.rootSed); err != nil {
			return nil, fmt.Errorf("albums: auto-albums: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("albums: auto-albums: %w", err)
	}
	return out, nil
}

// autoAlbum is a built muse or unfiled album and its songs.
type autoAlbum struct {
	Album
	songs []Song
}

// autoAlbums builds the auto-albums wanted, in listing order: muse albums by ordinal (the Muses are the directory's
// non-hidden ones; a Song of an unknown or hidden Muse is in no album), then unfiled. Names are neutral here; the opt-in
// names are applied by the caller. dir is non-nil.
func (s *Service) autoAlbums(ctx context.Context, wantMuse, wantUnfiled bool) ([]autoAlbum, error) {
	var meta map[string]MuseMeta
	if wantMuse {
		var err error
		if meta, err = s.muses.Ordinals(ctx); err != nil {
			return nil, fmt.Errorf("albums: muses: %w", err)
		}
	}
	rows, err := s.autoRows(ctx)
	if err != nil {
		return nil, err
	}
	byMuse := map[string][]autoRow{}
	for _, r := range rows {
		if r.museID == "" && !wantUnfiled || r.museID != "" && !wantMuse {
			continue
		}
		byMuse[r.museID] = append(byMuse[r.museID], r)
	}
	var out []autoAlbum
	if wantMuse {
		ids := make([]string, 0, len(byMuse))
		for id := range byMuse {
			if m, ok := meta[id]; ok && !m.Hidden && id != "" {
				ids = append(ids, id)
			}
		}
		slices.SortFunc(ids, func(a, b string) int {
			if d := meta[a].Ordinal - meta[b].Ordinal; d != 0 {
				return d
			}
			return compareStrings(a, b)
		})
		for _, id := range ids {
			out = append(out, buildAuto(PrefixMuse+id, KindMuse, NeutralName(meta[id].Ordinal), &id, byMuse[id]))
		}
	}
	if len(byMuse[""]) > 0 && wantUnfiled {
		out = append(out, buildAuto(IDUnfiled, KindUnfiled, NameUnfiled, nil, byMuse[""]))
	}
	return out, nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// buildAuto numbers the songs from 1 on disc 1 (each Take once) and draws the cover from the lineage of the first Songs.
func buildAuto(id string, kind Kind, name string, museID *string, rows []autoRow) autoAlbum {
	a := autoAlbum{Album: Album{ID: id, Kind: kind, Name: name, MuseID: museID, CoverInput: cover.Input{Kind: cover.KindAlbum, ID: id}}}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.takeID] {
			continue
		}
		seen[r.takeID] = true
		a.songs = append(a.songs, Song{TakeID: r.takeID, Position: len(a.songs) + 1, Disc: 1})
		if len(a.CoverInput.Parts) < records.CoverParts && r.rootID != "" {
			a.CoverInput.Parts = append(a.CoverInput.Parts, cover.Part{RootID: r.rootID, Seed: r.rootSed})
		}
	}
	a.SongCount = len(a.songs)
	return a
}

// MuseAlbumRef names a Muse's auto-album for get_muse.catalog: the catalog id and the neutral name, never the opted-in one.
type MuseAlbumRef struct {
	ID          string
	NeutralName string
}

// MuseAlbum returns the auto-album of one Muse, ok false while the Muse has none (empty, hidden, unknown, no directory).
// It never reads settings and never calls Labels: the name is always the neutral one. A nil Service has no albums.
func (s *Service) MuseAlbum(ctx context.Context, museID string) (MuseAlbumRef, bool, error) {
	if s == nil || s.muses == nil || museID == "" {
		return MuseAlbumRef{}, false, nil
	}
	albs, err := s.autoAlbums(ctx, true, false)
	if err != nil {
		return MuseAlbumRef{}, false, err
	}
	for _, a := range albs {
		if a.MuseID != nil && *a.MuseID == museID {
			return MuseAlbumRef{ID: a.ID, NeutralName: a.Name}, true, nil
		}
	}
	return MuseAlbumRef{}, false, nil
}
