package provenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Verify checks the invariants I1-I9 of C95.5 read-only and returns the anomalies (empty when the library is
// consistent). It is the oracle of the property tests and of the backfill acceptance. Checks that concern the pick or
// sound steps run only when those steps are wired (the no-op steps of L1-B3 derive nothing to verify); checks that only
// hold once the queue is empty (pick_stale, sound_stale, I9) run only then.
func (s *Service) Verify(ctx context.Context) ([]Anomaly, error) {
	tx, err := s.o.Store.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("provenance: verify: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var out []Anomaly
	add := func(code string, q string, args ...any) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("provenance: verify %s: %w", code, err)
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		for rows.Next() {
			var subject string
			var detail sql.NullString
			dest := []any{&subject}
			if len(cols) > 1 {
				dest = append(dest, &detail)
			}
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			out = append(out, Anomaly{Code: code, Subject: subject, Detail: detail.String})
		}
		return rows.Err()
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provenance_dirty`).Scan(&pending); err != nil {
		return nil, fmt.Errorf("provenance: verify: %w", err)
	}
	queries := []struct{ code, q string }{
		// I1
		{"track_without_song", `SELECT t.id FROM tracks t WHERE (SELECT COUNT(*) FROM take_songs ts WHERE ts.track_id = t.id) <> 1`},
		{"take_in_retired_song", `SELECT ts.track_id, ts.song_id FROM take_songs ts JOIN songs s ON s.id = ts.song_id WHERE s.retired_at IS NOT NULL`},
		{"root_without_song_root", `SELECT s.id FROM songs s WHERE s.root_track_id IS NOT NULL AND NOT EXISTS
			(SELECT 1 FROM song_roots r WHERE r.root_track_id = s.root_track_id AND r.song_id = s.id)`},
		// I2
		{"song_without_anchor", `SELECT s.id FROM songs s WHERE s.retired_at IS NULL AND s.root_track_id IS NULL
			AND (s.draft_id IS NULL OR NOT EXISTS (SELECT 1 FROM recipe_drafts d WHERE d.id = s.draft_id))`},
		{"draft_song_should_retire", `SELECT s.id, s.draft_id FROM songs s JOIN recipe_drafts d ON d.id = s.draft_id
			JOIN tracks t ON t.id = d.fulfilled_by_track_id WHERE s.retired_at IS NULL AND s.root_track_id IS NULL AND t.generation > 0`},
		// I3
		{"duplicate_live_song_draft", `SELECT draft_id FROM songs WHERE retired_at IS NULL AND draft_id IS NOT NULL GROUP BY draft_id HAVING COUNT(*) > 1`},
		{"duplicate_live_song_root", `SELECT root_track_id FROM songs WHERE retired_at IS NULL AND root_track_id IS NOT NULL GROUP BY root_track_id HAVING COUNT(*) > 1`},
		{"song_for_mothered_draft", `SELECT s.id, s.draft_id FROM songs s JOIN recipe_drafts d ON d.id = s.draft_id
			WHERE s.retired_at IS NULL AND d.parent_track_id IS NOT NULL`},
		// I7
		{"session_member_missing", `SELECT m.session_id, m.member_id FROM session_members m WHERE
			(m.member_kind = 'draft' AND NOT EXISTS (SELECT 1 FROM recipe_drafts WHERE id = m.member_id))
			OR (m.member_kind = 'track' AND NOT EXISTS (SELECT 1 FROM tracks WHERE id = m.member_id))`},
		{"run_without_session", `SELECT r.id FROM import_runs r WHERE NOT EXISTS (SELECT 1 FROM sessions WHERE source_key = 'import:' || r.id)`},
		{"batch_without_session", `SELECT b.id FROM batches b WHERE b.state IN ('active','finished') AND EXISTS (SELECT 1 FROM jobs WHERE batch_id = b.id)
			AND NOT EXISTS (SELECT 1 FROM sessions WHERE source_key = 'batch:' || b.id)`},
		// I9 (lease)
		{"lease_rows", `SELECT 'provenance_lease' WHERE (SELECT COUNT(*) FROM provenance_lease) <> 1`},
	}
	for _, q := range queries {
		if err := add(q.code, q.q); err != nil {
			return nil, err
		}
	}
	// collections: the session key may be shortened for long names, so compare through the shared function
	if err := s.verifyCollections(ctx, tx, &out); err != nil {
		return nil, err
	}
	if err := s.verifyMuses(ctx, tx, &out); err != nil {
		return nil, err
	}
	if err := s.verifyEvents(ctx, tx, &out); err != nil {
		return nil, err
	}
	if err := s.verifySoundValues(ctx, tx, &out); err != nil {
		return nil, err
	}
	if s.o.Picks != nil && pending == 0 {
		if err := s.verifyPicks(ctx, tx, &out); err != nil {
			return nil, err
		}
		if err := add("proposal_open_but_dead", `SELECT p.id FROM pick_proposals p WHERE p.state = 'open' AND (
			NOT EXISTS (SELECT 1 FROM tracks t WHERE t.id = p.take_id AND t.status <> 'discarded')
			OR EXISTS (SELECT 1 FROM song_picks sp WHERE sp.song_id = p.song_id AND sp.take_id = p.take_id))`); err != nil {
			return nil, err
		}
	}
	if s.o.Sounds != nil && pending == 0 {
		if err := add("sound_stale", `SELECT s.id FROM songs s WHERE s.retired_at IS NULL AND s.hidden_at IS NULL AND NOT EXISTS
			(SELECT 1 FROM sound_state ss WHERE ss.subject_kind = 'song' AND ss.subject_id = s.id
			   AND ss.taxonomy_version = (SELECT COALESCE(MAX(version), 0) FROM sound_taxonomy_versions))`); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) verifyCollections(ctx context.Context, tx *sql.Tx, out *[]Anomaly) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT name FROM draft_collections`)
	if err != nil {
		return fmt.Errorf("provenance: verify collections: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return err
		}
		names = append(names, n)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, n := range names {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE source_key = ?`, sessions.CollectionKey(n)).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			// the subject is the collection's session key hash-free form: names are Muse-adjacent text, so only a count is reported
			*out = append(*out, Anomaly{Code: "collection_without_session", Subject: "collection", Detail: ""})
		} else if err != nil {
			return err
		}
	}
	return nil
}

// verifyMuses runs I6 through the muses package (only it may read the muses tables).
func (s *Service) verifyMuses(ctx context.Context, tx *sql.Tx, out *[]Anomaly) error {
	f, err := muses.VerifyMuses(ctx, tx)
	if err != nil {
		return err
	}
	for _, id := range f.KeyMismatch {
		*out = append(*out, Anomaly{Code: "muse_key_mismatch", Subject: id})
	}
	for _, id := range f.DanglingSongs {
		*out = append(*out, Anomaly{Code: "song_muse_missing", Subject: id})
	}
	return nil
}

// verifyEvents is I5: per Song the pick_events sequence is 1..n, each from equals the previous to, and the last event
// equals song_picks.
func (s *Service) verifyEvents(ctx context.Context, tx *sql.Tx, out *[]Anomaly) error {
	rows, err := tx.QueryContext(ctx, `SELECT e.song_id, e.id, e.seq, e.from_take_id, e.from_source, e.to_take_id, e.to_source FROM pick_events e ORDER BY e.song_id, e.seq`)
	if err != nil {
		return fmt.Errorf("provenance: verify events: %w", err)
	}
	type last struct {
		id, source string
		take       sql.NullString
		seq        int
	}
	lasts := map[string]last{}
	for rows.Next() {
		var song, id, fromSrc, toSrc string
		var seq int
		var from, to sql.NullString
		if err := rows.Scan(&song, &id, &seq, &from, &fromSrc, &to, &toSrc); err != nil {
			_ = rows.Close()
			return err
		}
		prev, seen := lasts[song]
		switch {
		case !seen && (seq != 1 || fromSrc != "none"):
			*out = append(*out, Anomaly{Code: "pick_chain_broken", Subject: song, Detail: id})
		case seen && (seq != prev.seq+1 || fromSrc != prev.source || from != prev.take):
			*out = append(*out, Anomaly{Code: "pick_chain_broken", Subject: song, Detail: id})
		}
		lasts[song] = last{id: id, source: toSrc, take: to, seq: seq}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	prows, err := tx.QueryContext(ctx, `SELECT song_id, take_id, source, last_event_id FROM song_picks`)
	if err != nil {
		return fmt.Errorf("provenance: verify picks: %w", err)
	}
	defer prows.Close()
	for prows.Next() {
		var song, source, ev string
		var take sql.NullString
		if err := prows.Scan(&song, &take, &source, &ev); err != nil {
			return err
		}
		l, ok := lasts[song]
		if !ok || l.id != ev || l.source != source || l.take != take {
			*out = append(*out, Anomaly{Code: "pick_event_mismatch", Subject: song, Detail: ev})
		}
	}
	return prows.Err()
}

// verifySoundValues is the value part of I8: take_sounds rows hold allowed facet values.
func (s *Service) verifySoundValues(ctx context.Context, tx *sql.Tx, out *[]Anomaly) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT facet, value FROM take_sounds`)
	if err != nil {
		return fmt.Errorf("provenance: verify sounds: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var facet, value string
		if err := rows.Scan(&facet, &value); err != nil {
			return err
		}
		ok := true
		switch facet {
		case "tempo_band":
			ok = slices.Contains(sounds.TempoBands, value)
		case "engine":
		default:
			ok = sounds.ValidValue(facet, value)
		}
		if !ok {
			*out = append(*out, Anomaly{Code: "sound_value_invalid", Subject: facet})
		}
	}
	return rows.Err()
}

// verifyPicks is I4: song_picks exists for every Song with an eligible take, names a take of the Song, honours the pin
// rules, and equals what picks.Derive computes now (pick_stale; checked only on an empty queue).
func (s *Service) verifyPicks(ctx context.Context, tx *sql.Tx, out *[]Anomaly) error {
	songRows, err := tx.QueryContext(ctx, `SELECT s.id FROM songs s WHERE s.retired_at IS NULL ORDER BY s.id`)
	if err != nil {
		return fmt.Errorf("provenance: verify picks: %w", err)
	}
	var ids []string
	for songRows.Next() {
		var id string
		if err := songRows.Scan(&id); err != nil {
			_ = songRows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := songRows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		in, eligible, err := s.deriveInput(ctx, tx, id)
		if err != nil {
			return err
		}
		var take, pinned sql.NullString
		var source string
		err = tx.QueryRowContext(ctx, `SELECT take_id, source, pinned_take_id FROM song_picks WHERE song_id = ?`, id).Scan(&take, &source, &pinned)
		if errors.Is(err, sql.ErrNoRows) {
			if eligible > 0 {
				*out = append(*out, Anomaly{Code: "pick_missing", Subject: id})
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("provenance: verify picks: %w", err)
		}
		if take.Valid {
			var inSong int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM take_songs WHERE song_id = ? AND track_id = ?`, id, take.String).Scan(&inSong); err != nil {
				return err
			}
			if inSong != 1 {
				*out = append(*out, Anomaly{Code: "pick_take_not_in_song", Subject: id})
			}
		}
		if source == "pinned" && pinned != take {
			*out = append(*out, Anomaly{Code: "pick_pin_mismatch", Subject: id})
		}
		in.PinnedTakeID = nullPtr(pinned)
		if source == string(picks.SourceLadder) && take.Valid {
			in.IncumbentTakeID = nullPtr(take)
		}
		res := picks.Derive(in)
		if string(res.Source) != source || ptrStr(res.TakeID) != take.String {
			*out = append(*out, Anomaly{Code: "pick_stale", Subject: id, Detail: source + "->" + string(res.Source)})
		}
	}
	return nil
}

func nullPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// deriveInput loads a Song's takes for picks.Derive (human stars, instants, ladder strengths) and the count of
// eligible takes.
func (s *Service) deriveInput(ctx context.Context, tx *sql.Tx, song string) (picks.Input, int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.id, t.status, t.recorded_at, t.created_at, r.stars FROM take_songs ts JOIN tracks t ON t.id = ts.track_id
		LEFT JOIN ratings r ON r.track_id = t.id AND r.source = 'human' WHERE ts.song_id = ?`, song)
	if err != nil {
		return picks.Input{}, 0, fmt.Errorf("provenance: verify takes: %w", err)
	}
	var in picks.Input
	var ids []string
	eligible := 0
	for rows.Next() {
		var ti picks.TakeInput
		var rec, cre string
		var stars sql.NullInt64
		if err := rows.Scan(&ti.ID, &ti.Status, &rec, &cre, &stars); err != nil {
			_ = rows.Close()
			return in, 0, err
		}
		r, err := store.ParseInstant(rec)
		if err != nil {
			_ = rows.Close()
			return in, 0, err
		}
		c, err := store.ParseInstant(cre)
		if err != nil {
			_ = rows.Close()
			return in, 0, err
		}
		ti.RecordedAt, ti.CreatedAt = r.UnixMilli(), c.UnixMilli()
		if stars.Valid {
			n := int(stars.Int64)
			ti.HumanStars = &n
		}
		if ti.Status != "discarded" {
			eligible++
		}
		in.Takes = append(in.Takes, ti)
		ids = append(ids, ti.ID)
	}
	if err := rows.Close(); err != nil {
		return in, 0, err
	}
	if len(ids) > 1 {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id
			WHERE v.source = 'human' AND d.blind = 1
			  AND d.slot_a_track_id IN (SELECT track_id FROM take_songs WHERE song_id = ?1)
			  AND d.slot_b_track_id IN (SELECT track_id FROM take_songs WHERE song_id = ?1)`, song).Scan(&in.IntraDuels); err != nil {
			return in, 0, err
		}
	}
	if s.o.Ladder != nil && in.IntraDuels >= picks.LadderMinDuels {
		st, _, err := s.o.Ladder.Strengths(ctx, ids)
		if err != nil {
			return in, 0, fmt.Errorf("provenance: verify ladder: %w", err)
		}
		for i, t := range in.Takes {
			if x, ok := st[t.ID]; ok && x.NDuels > 0 {
				u, sd := x.U, x.SD
				in.Takes[i].U, in.Takes[i].SD = &u, &sd
			}
		}
	}
	return in, eligible, nil
}
