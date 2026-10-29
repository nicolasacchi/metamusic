package recordview

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// chunk keeps an IN list well under SQLite's variable limit.
const chunk = 500

// takeFacts are the Take columns a View needs that library.TrackRef does not carry.
type takeFacts struct {
	Stars *int // human stars
	Bytes int64
	Bits  int
}

// data is everything one assembly reads, loaded once whatever the number of slots (TestRecordView_NoN1).
type data struct {
	rec    records.Record
	rs     []records.ResolvedSlot
	briefs map[string]SongBrief
	picks  map[string]PickRow
	refs   map[string]library.TrackRef
	facts  map[string]takeFacts
	loud   map[string]Loudness
	fams   map[string][]string     // memoised by families: the liner and the preview share one read
	sess   map[string][]SessionRef // memoised by sessions
}

// ids returns the distinct ids in order of first appearance.
func distinct(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// playable reports whether a resolved slot plays a Take now.
func playable(r records.ResolvedSlot) bool { return r.State == records.StateOK && r.Take != nil }

// load reads the batch facts of a resolved Record. extraTakes are further Takes whose titles the caller needs (changelog rows).
func (s *Service) load(ctx context.Context, rec records.Record, rs []records.ResolvedSlot, extraTakes []string) (*data, error) {
	d := &data{rec: rec, rs: rs, briefs: map[string]SongBrief{}, picks: map[string]PickRow{}, refs: map[string]library.TrackRef{},
		facts: map[string]takeFacts{}, loud: map[string]Loudness{}}
	var songIDs, takeIDs, loudIDs []string
	for _, r := range rs {
		songIDs = append(songIDs, r.Slot.SongID)
		if r.Take != nil {
			takeIDs = append(takeIDs, r.Take.ID)
		}
		if playable(r) {
			loudIDs = append(loudIDs, r.Take.ID)
		}
	}
	songIDs, takeIDs = distinct(songIDs), distinct(append(takeIDs, extraTakes...))
	var err error
	if s.o.Songs != nil && len(songIDs) > 0 {
		if d.briefs, err = s.o.Songs.Briefs(ctx, songIDs); err != nil {
			return nil, fmt.Errorf("recordview: songs: %w", err)
		}
	}
	if s.o.Picks != nil && len(songIDs) > 0 {
		if d.picks, err = s.o.Picks.Current(ctx, songIDs); err != nil {
			return nil, fmt.Errorf("recordview: picks: %w", err)
		}
	}
	if s.o.Library != nil && len(takeIDs) > 0 {
		if d.refs, err = s.o.Library.TrackRefs(ctx, takeIDs); err != nil {
			return nil, fmt.Errorf("recordview: takes: %w", err)
		}
	}
	if d.facts, err = s.takeFacts(ctx, takeIDs); err != nil {
		return nil, err
	}
	if s.o.Ears != nil && len(loudIDs) > 0 {
		if d.loud, err = s.o.Ears.Loudness(ctx, distinct(loudIDs)); err != nil {
			return nil, fmt.Errorf("recordview: loudness: %w", err)
		}
	}
	return d, nil
}

// takeFacts reads the human stars and the master size of Takes in one statement per 500 ids.
func (s *Service) takeFacts(ctx context.Context, ids []string) (map[string]takeFacts, error) {
	out := make(map[string]takeFacts, len(ids))
	if s.o.Store == nil {
		return out, nil
	}
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		//nolint:gosec // G202: only "?" placeholders are concatenated
		rows, err := s.o.Store.R.QueryContext(ctx, `SELECT t.id, t.master_bytes, t.bits_per_sample, r.stars FROM tracks t
			LEFT JOIN ratings r ON r.track_id = t.id AND r.source = 'human' WHERE t.id IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("recordview: take facts: %w", err)
		}
		for rows.Next() {
			var id string
			var f takeFacts
			var stars sql.NullInt64
			if err := rows.Scan(&id, &f.Bytes, &f.Bits, &stars); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if stars.Valid {
				n := int(stars.Int64)
				f.Stars = &n
			}
			out[id] = f
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// title returns a Take's title for the changelog and the chapter names ("" when unknown).
func (d *data) title(takeID string) string { return d.refs[takeID].Title }

// families reads the Sound families of the slot Songs once per assembly.
func (s *Service) families(ctx context.Context, d *data, songIDs []string) (map[string][]string, error) {
	if d.fams != nil {
		return d.fams, nil
	}
	d.fams = map[string][]string{}
	if s.o.Sounds != nil && len(songIDs) > 0 {
		m, err := s.o.Sounds.FamiliesOf(ctx, songIDs)
		if err != nil {
			return nil, fmt.Errorf("recordview: sounds: %w", err)
		}
		d.fams = m
	}
	return d.fams, nil
}

// sessions reads the Sessions of the slot Songs once per assembly.
func (s *Service) sessions(ctx context.Context, d *data, songIDs []string) (map[string][]SessionRef, error) {
	if d.sess != nil {
		return d.sess, nil
	}
	d.sess = map[string][]SessionRef{}
	if s.o.Sessions != nil && len(songIDs) > 0 {
		m, err := s.o.Sessions.SessionsOf(ctx, songIDs)
		if err != nil {
			return nil, fmt.Errorf("recordview: sessions: %w", err)
		}
		d.sess = m
	}
	return d.sess, nil
}
