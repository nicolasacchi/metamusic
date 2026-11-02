package features

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// proposalMaxOpen is the cap on open taxonomy proposals (C140.1); it mirrors sounds.ProposalMaxOpen.
const proposalMaxOpen = 50

// Verify checks the L3 invariants F1 to F6 of C135.6, read-only, and returns the findings (empty when the library is
// consistent). The checks that only hold once the Reconciler has drained its queue (F1 to F3 and the Sound order of F6) run
// only then; F4 and F5 hold at every moment.
func (s *Service) Verify(ctx context.Context) ([]Anomaly, error) {
	tx, err := s.st.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("features: verify: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var out []Anomaly
	add := func(code, q string, args ...any) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("features: verify %s: %w", code, err)
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
		return nil, fmt.Errorf("features: verify: %w", err)
	}
	if pending == 0 {
		if err := s.verifyRows(ctx, tx, add, &out); err != nil {
			return nil, err
		}
	}
	if err := verifySequences(ctx, tx, add, &out); err != nil {
		return nil, err
	}
	if err := verifyProposals(ctx, tx, add, &out); err != nil {
		return nil, err
	}
	if pending == 0 {
		// F6: the primary value of a facet is its smallest ord, so the ords of a Song's rows must be distinct (a row set
		// computed before the ord column existed has several rows at ord 0 until the Sounds compute rewrites it).
		if err := add("sound_ord_duplicate", `SELECT song_id, facet FROM song_sounds GROUP BY song_id, ord HAVING COUNT(*) > 1`); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// verifyRows is F1 to F3.
func (s *Service) verifyRows(ctx context.Context, tx *sql.Tx, add func(code, q string, args ...any) error, out *[]Anomaly) error {
	// F1: every live Song with a subject has one row that equals sounds.SubjectOf; none otherwise.
	rows, err := tx.QueryContext(ctx, `SELECT s.id, s.retired_at IS NOT NULL, f.subject_kind, f.subject_id, f.subject_note
		FROM songs s LEFT JOIN song_features f ON f.song_id = s.id ORDER BY s.id`)
	if err != nil {
		return fmt.Errorf("features: verify F1: %w", err)
	}
	type songRow struct {
		id                  string
		retired             bool
		kind, subject, note sql.NullString
	}
	var songs []songRow
	for rows.Next() {
		var r songRow
		if err := rows.Scan(&r.id, &r.retired, &r.kind, &r.subject, &r.note); err != nil {
			_ = rows.Close()
			return err
		}
		songs = append(songs, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	c := newCache(tx)
	defer c.Close()
	for _, r := range songs {
		have := r.kind.Valid
		if r.retired {
			if have {
				*out = append(*out, Anomaly{"song_features_extra", r.id, "retired Song has a row"})
			}
			continue
		}
		sub, err := sounds.SubjectOf(ctx, c, r.id)
		if err != nil {
			return err
		}
		if sub.Kind == sounds.SubjectNone {
			if have {
				*out = append(*out, Anomaly{"song_features_extra", r.id, "Song without a subject has a row"})
			}
			continue
		}
		if !have {
			*out = append(*out, Anomaly{"song_features_missing", r.id, "no row for subject " + sub.Kind + " " + sub.ID})
			continue
		}
		if r.kind.String != sub.Kind || r.subject.String != sub.ID {
			*out = append(*out, Anomaly{"song_features_subject", r.id, fmt.Sprintf("row has %s %s, subject is %s %s", r.kind.String, r.subject.String, sub.Kind, sub.ID)})
		}
		note, err := subjectNote(ctx, c, sub, r.id)
		if err != nil {
			return err
		}
		if (note != nil) != r.note.Valid {
			*out = append(*out, Anomaly{"song_features_note", r.id, "subject_note does not match the Song's takes"})
		}
	}
	// F2: every track has a row; a measured basis has its analysis row; the octave flag needs both tempo values.
	for _, q := range []struct{ code, q string }{
		{"take_features_missing", `SELECT t.id FROM tracks t WHERE NOT EXISTS (SELECT 1 FROM take_features f WHERE f.track_id = t.id)`},
		{"take_features_orphan", `SELECT f.track_id FROM take_features f WHERE NOT EXISTS (SELECT 1 FROM tracks t WHERE t.id = f.track_id)`},
		{"take_feature_unbacked", `SELECT f.track_id, 'bpm' FROM take_features f WHERE f.bpm_basis = 'measured' AND NOT EXISTS
			(SELECT 1 FROM analyses a WHERE a.track_id = f.track_id AND a.analyzer = 'tempo' AND substr(a.version, 1, length('` + tempoDef + ` ')) = '` + tempoDef + ` ')
			UNION ALL SELECT f.track_id, 'key' FROM take_features f WHERE f.key_basis = 'measured' AND NOT EXISTS
			(SELECT 1 FROM analyses a WHERE a.track_id = f.track_id AND a.analyzer = 'key' AND substr(a.version, 1, length('` + keyDef + ` ')) = '` + keyDef + ` ')
			UNION ALL SELECT f.track_id, 'energy' FROM take_features f WHERE f.energy_basis = 'measured' AND NOT EXISTS
			(SELECT 1 FROM analyses a WHERE a.track_id = f.track_id AND a.analyzer = 'energy' AND substr(a.version, 1, length('` + energyDef + ` ')) = '` + energyDef + ` ')`},
		{"bpm_octave_invalid", `SELECT track_id FROM take_features WHERE bpm_octave = 1 AND (bpm_requested_x100 IS NULL OR bpm_measured_x100 IS NULL)
			UNION ALL SELECT song_id FROM song_features WHERE bpm_octave = 1 AND (bpm_requested_x100 IS NULL OR bpm_measured_x100 IS NULL)`},
		// F3: a take-subject Song copies its take's row.
		{"song_features_copy", `SELECT s.song_id, s.subject_id FROM song_features s JOIN take_features t ON t.track_id = s.subject_id WHERE s.subject_kind = 'take' AND (
			s.bpm_x100 IS NOT t.bpm_x100 OR s.bpm_basis IS NOT t.bpm_basis OR s.bpm_requested_x100 IS NOT t.bpm_requested_x100 OR s.bpm_measured_x100 IS NOT t.bpm_measured_x100
			OR s.bpm_octave <> t.bpm_octave OR s.energy_x10000 IS NOT t.energy_x10000 OR s.energy_basis IS NOT t.energy_basis OR s.key_num IS NOT t.key_num
			OR s.key_letter IS NOT t.key_letter OR s.key_basis IS NOT t.key_basis OR s.duration_s IS NOT t.duration_s OR s.duration_expected_s IS NOT t.duration_expected_s)`},
		{"song_features_take_missing", `SELECT s.song_id, s.subject_id FROM song_features s WHERE s.subject_kind = 'take' AND NOT EXISTS (SELECT 1 FROM take_features t WHERE t.track_id = s.subject_id)`},
	} {
		if err := add(q.code, q.q); err != nil {
			return err
		}
	}
	return nil
}

// verifySequences is F4: the applied orderings are permutations, every sequenced change finds its explanation, and only an
// ordering without endpoints must not cost more than its input.
func verifySequences(ctx context.Context, tx *sql.Tx, add func(code, q string, args ...any) error, out *[]Anomaly) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, order_before_json, order_after_json FROM record_sequences ORDER BY id`)
	if err != nil {
		return fmt.Errorf("features: verify F4: %w", err)
	}
	for rows.Next() {
		var id, before, after string
		if err := rows.Scan(&id, &before, &after); err != nil {
			_ = rows.Close()
			return err
		}
		var b, a []string
		if json.Unmarshal([]byte(before), &b) != nil || json.Unmarshal([]byte(after), &a) != nil || !samePermutation(b, a) {
			*out = append(*out, Anomaly{"sequence_not_permutation", id, ""})
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, q := range []struct{ code, q string }{
		{"sequence_change_orphan", `SELECT c.id, json_extract(c.detail_json, '$.sequence_id') FROM record_changes c WHERE json_extract(c.detail_json, '$.sequence_id') IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM record_sequences q WHERE q.id = json_extract(c.detail_json, '$.sequence_id'))`},
		{"sequence_cost_regressed", `SELECT id, cost_input || ' -> ' || cost_output FROM record_sequences WHERE first_song_id IS NULL AND last_song_id IS NULL AND cost_output > cost_input`},
	} {
		if err := add(q.code, q.q); err != nil {
			return err
		}
	}
	return nil
}

func samePermutation(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// verifyProposals is F5: accepted proposals point at their taxonomy version, open ones are bounded and well formed. Whether
// an open proposal is still valid against today's index (a warning in the plan) is not checked here: it needs the Sound index.
func verifyProposals(ctx context.Context, tx *sql.Tx, add func(code, q string, args ...any) error, out *[]Anomaly) error {
	if err := add("proposal_version_missing", `SELECT p.id, p.applied_version FROM taxonomy_proposals p WHERE p.state = 'accepted' AND NOT EXISTS
		(SELECT 1 FROM sound_taxonomy_versions v WHERE v.version = p.applied_version AND v.note = 'proposal ' || p.id)`); err != nil {
		return err
	}
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM taxonomy_proposals WHERE state = 'open'`).Scan(&open); err != nil {
		return fmt.Errorf("features: verify F5: %w", err)
	}
	if open > proposalMaxOpen {
		*out = append(*out, Anomaly{"proposals_open_over_limit", "taxonomy_proposals", fmt.Sprintf("%d open, at most %d", open, proposalMaxOpen)})
	}
	return add("proposal_malformed", `SELECT p.id, 'mapping ' || j.key FROM taxonomy_proposals p, json_each(p.mappings_json) j WHERE p.state = 'open' AND NOT (
		json_type(j.value) = 'object' AND json_type(j.value, '$.term') = 'text' AND json_type(j.value, '$.facet') = 'text'
		AND json_type(j.value, '$.value') = 'text' AND json_extract(j.value, '$.op') IN ('add','remove'))`)
}
