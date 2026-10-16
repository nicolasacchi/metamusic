package songview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/picks"
)

// BriefContextRunes cuts a brief Muse's text in the /context bundle (C98).
const BriefContextRunes = 500

// MaxSiblings is how many other takes of the Song /context lists.
const MaxSiblings = 12

// LibrarySections are the three /context sections of C98 (song, muse, sounds) for one take; each is nil when the take has no
// Song yet (the queue is pending). app.Build adapts it to trackctx.LibraryReader, so trackctx imports none of the L1
// packages (C98, C99.1).
type LibrarySections struct {
	Song, Muse, Sounds map[string]any
}

// ForTrack builds the song, muse and sounds sections of one take (C98). A take without a Song gives three nils, not an error.
func (s *Service) ForTrack(ctx context.Context, trackID string) (LibrarySections, error) {
	var songID string
	err := s.st.R.QueryRowContext(ctx, `SELECT song_id FROM take_songs WHERE track_id = ?`, trackID).Scan(&songID)
	if errors.Is(err, sql.ErrNoRows) {
		return LibrarySections{}, nil
	}
	if err != nil {
		return LibrarySections{}, fmt.Errorf("songview: context: %w", err)
	}
	row, err := s.songs.Get(ctx, songID)
	if err != nil {
		return LibrarySections{}, err
	}
	core := SongCore{ID: row.ID}
	if err := s.state(ctx, &core); err != nil {
		return LibrarySections{}, err
	}
	v := View{Sounds: SoundsBlock{Facets: map[string][]FacetEntry{}}}
	var out LibrarySections
	pick, err := s.pick(ctx, songID)
	if err != nil {
		return LibrarySections{}, err
	}
	var takes int
	if err := s.st.R.QueryRow(`SELECT COUNT(*) FROM take_songs WHERE song_id = ?`, songID).Scan(&takes); err != nil {
		return LibrarySections{}, err
	}
	var pickM map[string]any
	if pick != nil && pick.TakeID != nil {
		pickM = map[string]any{"take_id": *pick.TakeID, "source": string(pick.Source), "is_this": *pick.TakeID == trackID}
	}
	var pending, open int
	if err := s.st.R.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM provenance_dirty d WHERE d.kind = 'track' AND d.id IN (SELECT track_id FROM take_songs WHERE song_id = ?)),
		(SELECT COUNT(*) FROM pick_proposals WHERE song_id = ? AND state = 'open')`, songID, songID).Scan(&pending, &open); err != nil {
		return LibrarySections{}, err
	}
	sib, err := s.siblings(ctx, songID, trackID, pick)
	if err != nil {
		return LibrarySections{}, err
	}
	out.Song = map[string]any{"id": row.ID, "title": row.Title, "angle": row.Angle, "state": string(core.State), "takes": takes,
		"pick": pickM, "open_proposals": open, "pending": pending == 1, "siblings": sib}
	if row.MuseID != nil {
		m, err := s.muses.Get(ctx, *row.MuseID)
		if err != nil {
			return LibrarySections{}, err
		}
		var brief any
		if m.Brief != nil {
			b := *m.Brief
			if utf8.RuneCountInString(b) > BriefContextRunes {
				b = string([]rune(b)[:BriefContextRunes])
			}
			brief = b
		}
		out.Muse = map[string]any{"id": m.ID, "kind": m.Kind, "title": m.Title, "artist": m.Artist, "brief": brief}
	}
	if err := s.soundsBlock(ctx, &v, songID); err != nil {
		return LibrarySections{}, err
	}
	facets := map[string]any{}
	for f, es := range v.Sounds.Facets {
		vals := make([]string, len(es))
		for i, e := range es {
			vals[i] = e.Value
		}
		facets[f] = vals
	}
	var tempo any
	if t := v.Sounds.Tempo; t != nil {
		tempo = map[string]any{"requested_bpm": t.RequestedBPM, "measured_bpm": t.MeasuredBPM, "mismatch": t.Mismatch, "octave": t.Octave}
	}
	out.Sounds = map[string]any{"taxonomy_version": v.Sounds.TaxonomyVersion, "facets": facets, "tempo": tempo}
	return out, nil
}

// siblings lists the other takes of the Song, newest first (by parsed instant), at most MaxSiblings.
func (s *Service) siblings(ctx context.Context, songID, trackID string, pick *picks.PickView) ([]map[string]any, error) {
	trs, err := s.takeRows(ctx, songID)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(trs, func(a, b takeRow) int {
		if a.recorded != b.recorded {
			return int(b.recorded - a.recorded)
		}
		return strings.Compare(b.card.ID, a.card.ID)
	})
	out := []map[string]any{}
	for _, t := range trs {
		if t.card.ID == trackID {
			continue
		}
		isPick := pick != nil && pick.TakeID != nil && *pick.TakeID == t.card.ID
		out = append(out, map[string]any{"id": t.card.ID, "title": t.card.Title, "stars": t.card.Stars, "status": t.card.Status, "is_pick": isPick})
		if len(out) == MaxSiblings {
			break
		}
	}
	return out, nil
}
