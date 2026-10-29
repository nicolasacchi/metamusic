package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// The batch readers of recordview (C124.2, lane V2): each answers any number of ids in one statement (TestBatchReaders_OneStatement).
// recordview declares the interfaces with its own value types (PR #54), so no L1 package gains a method; the adapters read the
// L1 tables through the shared reader pool and are the only code that does.

// listArgs turns ids into a placeholder list and its arguments.
func listArgs(ids []string) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

// l2Readers implements recordview's SessionReader, SoundReader, SongReader, PickReader and LoudnessReader.
type l2Readers struct{ st *store.Store }

// SessionsOf returns the visible Sessions of each Song (through its Takes and its draft), newest first.
func (r l2Readers) SessionsOf(ctx context.Context, songIDs []string) (map[string][]recordview.SessionRef, error) {
	out := map[string][]recordview.SessionRef{}
	if len(songIDs) == 0 {
		return out, nil
	}
	ph, a := listArgs(songIDs)
	args := append(append([]any{}, a...), a...)
	//nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := r.st.R.QueryContext(ctx, `SELECT song_id, id, kind, name, started_at FROM (
		SELECT ts.song_id AS song_id, x.id AS id, x.kind AS kind, x.name AS name, x.started_at AS started_at
		  FROM take_songs ts JOIN session_members m ON m.member_kind = 'track' AND m.member_id = ts.track_id
		  JOIN sessions x ON x.id = m.session_id WHERE x.hidden_at IS NULL AND ts.song_id IN (`+ph+`)
		UNION
		SELECT s.id, x.id, x.kind, x.name, x.started_at
		  FROM songs s JOIN session_members m ON m.member_kind = 'draft' AND m.member_id = s.draft_id
		  JOIN sessions x ON x.id = m.session_id WHERE x.hidden_at IS NULL AND s.id IN (`+ph+`))
		ORDER BY song_id, started_at DESC, id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("sessions of songs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var song, started string
		var s recordview.SessionRef
		if err := rows.Scan(&song, &s.ID, &s.Kind, &s.Name, &started); err != nil {
			return nil, err
		}
		s.Date = started
		if len(started) >= 10 {
			s.Date = started[:10]
		}
		out[song] = append(out[song], s)
	}
	return out, rows.Err()
}

// FamiliesOf returns the Sound families (genre_family values, sorted) of each Song.
func (r l2Readers) FamiliesOf(ctx context.Context, songIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(songIDs) == 0 {
		return out, nil
	}
	ph, args := listArgs(songIDs)
	//nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := r.st.R.QueryContext(ctx, `SELECT song_id, value FROM song_sounds WHERE facet = 'genre_family' AND song_id IN (`+ph+`) ORDER BY song_id, value`, args...)
	if err != nil {
		return nil, fmt.Errorf("families of songs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var song, v string
		if err := rows.Scan(&song, &v); err != nil {
			return nil, err
		}
		out[song] = append(out[song], v)
	}
	return out, rows.Err()
}

// Briefs returns the SongBrief of each known Song: identity, state, the primary root with its seed, the engine and the
// effective tempo (measured, else declared, else plan; L1 C93).
func (r l2Readers) Briefs(ctx context.Context, songIDs []string) (map[string]recordview.SongBrief, error) {
	out := map[string]recordview.SongBrief{}
	if len(songIDs) == 0 {
		return out, nil
	}
	ph, args := listArgs(songIDs)
	//nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := r.st.R.QueryContext(ctx, `SELECT s.id, s.title, s.angle, s.muse_id, `+store.SongStateCase+`, s.retired_at IS NOT NULL, s.retired_into,
		s.hidden_at IS NOT NULL, COALESCE(s.root_track_id, ''), COALESCE(rc.seed, 0), s.draft_id, COALESCE(rc.engine, ''),
		(SELECT ss.detail_json FROM song_sounds ss WHERE ss.song_id = s.id AND ss.facet = 'tempo_band' LIMIT 1)
		FROM (`+store.SongStateSQL+` WHERE s.id IN (`+ph+`)) st
		JOIN songs s ON s.id = st.song_id LEFT JOIN tracks t ON t.id = s.root_track_id LEFT JOIN recipes rc ON rc.id = t.recipe_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("briefs of songs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var b recordview.SongBrief
		var tempo *string
		if err := rows.Scan(&b.ID, &b.Title, &b.Angle, &b.MuseID, &b.State, &b.Retired, &b.RetiredInto, &b.Hidden, &b.RootTrackID, &b.RootSeed,
			&b.DraftID, &b.Engine, &tempo); err != nil {
			return nil, err
		}
		if b.Engine == "unknown" {
			b.Engine = ""
		}
		if tempo != nil {
			var d struct {
				Requested *float64 `json:"requested_bpm"`
				Plan      *float64 `json:"plan_bpm"`
				Measured  *float64 `json:"measured_bpm"`
				Basis     string   `json:"basis"`
			}
			if json.Unmarshal([]byte(*tempo), &d) == nil {
				switch d.Basis {
				case "measured":
					b.BPM = d.Measured
				case "declared":
					b.BPM = d.Requested
				case "plan":
					b.BPM = d.Plan
				}
				if b.BPM != nil {
					b.BPMBasis = d.Basis
				}
			}
		}
		out[b.ID] = b
	}
	return out, rows.Err()
}

// Current returns the song_picks row of each Song that has one, with its evidence.
func (r l2Readers) Current(ctx context.Context, songIDs []string) (map[string]recordview.PickRow, error) {
	out := map[string]recordview.PickRow{}
	if len(songIDs) == 0 {
		return out, nil
	}
	ph, args := listArgs(songIDs)
	//nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := r.st.R.QueryContext(ctx, `SELECT song_id, take_id, source, evidence_json FROM song_picks WHERE song_id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("current picks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var p recordview.PickRow
		var ev string
		if err := rows.Scan(&p.SongID, &p.TakeID, &p.Source, &ev); err != nil {
			return nil, err
		}
		p.Evidence = json.RawMessage(ev)
		out[p.SongID] = p
	}
	return out, rows.Err()
}

// Loudness returns the stored loudness analysis (current definition, newest row) of each analysed Take.
func (r l2Readers) Loudness(ctx context.Context, takeIDs []string) (map[string]recordview.Loudness, error) {
	out := map[string]recordview.Loudness{}
	if len(takeIDs) == 0 {
		return out, nil
	}
	ph, a := listArgs(takeIDs)
	args := append([]any{analysis.LoudnessDef + " %"}, a...)
	//nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := r.st.R.QueryContext(ctx, `SELECT track_id, version, created_at, metrics_json FROM analyses
		WHERE analyzer = 'loudness' AND version LIKE ? AND track_id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("loudness of takes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type cand struct{ version, at, metrics string }
	best := map[string]cand{}
	for rows.Next() {
		var id string
		var c cand
		if err := rows.Scan(&id, &c.version, &c.at, &c.metrics); err != nil {
			return nil, err
		}
		if cur, ok := best[id]; ok && (cur.at > c.at || cur.at == c.at && cur.version >= c.version) {
			continue
		}
		best[id] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(best))
	for id := range best {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var l analysis.Loudness
		if json.Unmarshal([]byte(best[id].metrics), &l) != nil {
			continue // an unreadable analysis is "no measurement", never an error
		}
		out[id] = recordview.Loudness{Measure: tape.Measure{LUFS: l.IntegratedLUFS, TruePeakDBTP: l.TruePeakDBTP, OK: true}, ShortTerm1Hz: l.ShortTerm1Hz}
	}
	return out, nil
}

// coverLinker adapts cover.Service.EnsureMosaic and the link signer to recordview.CoverLinker: the 512 and 1024 px mosaics,
// signed for the caller's principal (the same TTLs as a track's cover).
type coverLinker struct {
	covers *cover.Service
	signer *mediaserve.Signer
}

func (c coverLinker) Links(ctx context.Context, in cover.Input) (recordview.CoverRef, error) {
	if c.covers == nil || c.signer == nil {
		return recordview.CoverRef{}, nil
	}
	ttl, bucket := mediaserve.TTLFor(auth.FromContext(ctx))
	var ref recordview.CoverRef
	var exp time.Time
	for i, size := range [2]int{512, 1024} {
		_, key, err := c.covers.EnsureMosaic(ctx, in, size)
		if err != nil {
			return recordview.CoverRef{}, err
		}
		l := c.signer.Sign(key, "png", ttl, bucket)
		if i == 0 {
			ref.URL512, exp = l.URL, l.Expires
		} else {
			ref.URL1024 = l.URL
			if l.Expires.Before(exp) {
				exp = l.Expires
			}
		}
	}
	ref.ExpiresAt = exp.UTC().Format(time.RFC3339)
	return ref, nil
}
