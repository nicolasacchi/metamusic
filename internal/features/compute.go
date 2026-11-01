package features

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// rowColumns are the columns take_features and song_features share, in the order rowArgs emits them.
const rowColumns = `bpm_x100, bpm_basis, bpm_requested_x100, bpm_measured_x100, bpm_octave, energy_x10000, energy_basis,
	key_num, key_letter, key_basis, duration_s, duration_expected_s`

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullBasis(b Basis) any {
	if b == "" {
		return nil
	}
	return string(b)
}

func rowArgs(r Row) []any {
	var kn, kl any
	if r.Key != nil {
		kn, kl = r.Key.Num, r.Key.Letter
	}
	oct := 0
	if r.BPMOctave {
		oct = 1
	}
	return []any{nullInt(r.BPMx100), nullBasis(r.BPMBasis), nullInt(r.BPMRequestedx100), nullInt(r.BPMMeasuredx100), oct,
		nullInt(r.Energyx10000), nullBasis(r.EnergyBasis), kn, kl, nullBasis(r.KeyBasis), nullFloat(r.DurationS), nullFloat(r.DurationExpectedS)}
}

const setRowColumns = `bpm_x100 = excluded.bpm_x100, bpm_basis = excluded.bpm_basis, bpm_requested_x100 = excluded.bpm_requested_x100,
	bpm_measured_x100 = excluded.bpm_measured_x100, bpm_octave = excluded.bpm_octave, energy_x10000 = excluded.energy_x10000,
	energy_basis = excluded.energy_basis, key_num = excluded.key_num, key_letter = excluded.key_letter, key_basis = excluded.key_basis,
	duration_s = excluded.duration_s, duration_expected_s = excluded.duration_expected_s`

// run is one ComputeTx or sweep batch: the transaction's statements, the clock and the takes derived so far.
type run struct {
	ctx   context.Context
	c     *cache
	at    string
	takes map[string]takeResult
}

type takeResult struct {
	ok      bool
	row     Row
	digest  string
	changed bool
}

func newRun(ctx context.Context, tx *sql.Tx, env Env) *run {
	return &run{ctx: ctx, c: newCache(tx), at: store.FormatTime(env.Now), takes: map[string]takeResult{}}
}

func (r *run) close() { r.c.Close() }

// take derives and stores one take's row unless its digest is unchanged. A take processed earlier in the run is not
// derived again.
func (r *run) take(id string) (takeResult, error) {
	if t, ok := r.takes[id]; ok {
		return t, nil
	}
	tl, ok, err := loadTake(r.ctx, r.c, id)
	if err != nil || !ok {
		return takeResult{}, err
	}
	res := takeResult{ok: true, row: DeriveTake(tl.In), digest: tl.Digest}
	var have, haveDef string
	switch err := r.c.QueryRowContext(r.ctx, `SELECT digest, feature_def FROM take_features WHERE track_id = ?`, id).Scan(&have, &haveDef); err {
	case nil, sql.ErrNoRows:
	default:
		return res, fmt.Errorf("features: take digest: %w", err)
	}
	if have != res.digest || haveDef != Def {
		args := append([]any{id}, rowArgs(res.row)...)
		args = append(args, string(res.row.Detail), res.digest, Def, r.at)
		//nolint:gosec // G202: rowColumns and setRowColumns are constants
		if _, err := r.c.ExecContext(r.ctx, `INSERT INTO take_features(track_id, `+rowColumns+`, detail_json, digest, feature_def, computed_at)
			VALUES(?, ?,?,?,?,?,?,?,?,?,?,?,?, ?,?,?,?) ON CONFLICT(track_id) DO UPDATE SET `+setRowColumns+`,
			detail_json = excluded.detail_json, digest = excluded.digest, feature_def = excluded.feature_def, computed_at = excluded.computed_at`, args...); err != nil {
			return res, fmt.Errorf("features: write take row: %w", err)
		}
		res.changed = true
	}
	r.takes[id] = res
	return res, nil
}

type primaryDoc struct {
	Family   string `json:"family"`
	Vocal    string `json:"vocal"`
	Era      string `json:"era"`
	Mood     string `json:"mood"`
	Language string `json:"language"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// song derives and stores the row of one Song from its subject (D-L3-39), or removes it when the Song is retired or has no
// subject. It reports whether a row was written or deleted.
func (r *run) song(id string) (bool, error) {
	var retired bool
	if err := r.c.QueryRowContext(r.ctx, `SELECT retired_at IS NOT NULL FROM songs WHERE id = ?`, id).Scan(&retired); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("features: song: %w", err)
	}
	if retired {
		return r.drop(id)
	}
	sub, err := sounds.SubjectOf(r.ctx, r.c, id)
	if err != nil {
		return false, err
	}
	var row Row
	var subjectDigest string
	var facets Facets
	switch sub.Kind {
	case sounds.SubjectTake:
		t, err := r.take(sub.ID)
		if err != nil {
			return false, err
		}
		if !t.ok {
			return r.drop(id)
		}
		row, subjectDigest = t.row, t.digest
	case sounds.SubjectDraft:
		in, td, ok, err := loadDraft(r.ctx, r.c, id, sub.ID)
		if err != nil {
			return false, err
		}
		if !ok {
			return r.drop(id)
		}
		row, subjectDigest, facets = DeriveTake(in), td, in.Facets
	default:
		return r.drop(id)
	}
	note, err := subjectNote(r.ctx, r.c, sub, id)
	if err != nil {
		return false, err
	}
	prim, err := primarySounds(r.ctx, r.c, id)
	if err != nil {
		return false, err
	}
	p := primaryDoc{prim["genre_family"], prim["vocal"], prim["era"], prim["mood"], prim["language"]}
	digest := sumJSON(struct {
		Def     string     `json:"def"`
		Kind    string     `json:"subject_kind"`
		ID      string     `json:"subject_id"`
		Note    *string    `json:"subject_note"`
		Subject string     `json:"subject_digest"`
		Facets  *Facets    `json:"facets,omitempty"`
		Primary primaryDoc `json:"primary"`
	}{Def, sub.Kind, sub.ID, note, subjectDigest, draftFacets(sub.Kind, &facets), p})
	var have, haveDef string
	switch err := r.c.QueryRowContext(r.ctx, `SELECT digest, feature_def FROM song_features WHERE song_id = ?`, id).Scan(&have, &haveDef); err {
	case nil, sql.ErrNoRows:
	default:
		return false, fmt.Errorf("features: song digest: %w", err)
	}
	if have == digest && haveDef == Def {
		return false, nil
	}
	args := append([]any{id, sub.Kind, sub.ID, nullStr(note)}, rowArgs(row)...)
	args = append(args, nullStr(strPtr(p.Family)), nullStr(strPtr(p.Vocal)), nullStr(strPtr(p.Era)), nullStr(strPtr(p.Mood)), nullStr(strPtr(p.Language)), digest, Def, r.at)
	//nolint:gosec // G202: rowColumns and setRowColumns are constants
	if _, err := r.c.ExecContext(r.ctx, `INSERT INTO song_features(song_id, subject_kind, subject_id, subject_note, `+rowColumns+`,
		family, vocal, era, mood, language, digest, feature_def, computed_at) VALUES(?,?,?,?, ?,?,?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?, ?,?,?)
		ON CONFLICT(song_id) DO UPDATE SET subject_kind = excluded.subject_kind, subject_id = excluded.subject_id, subject_note = excluded.subject_note,
		`+setRowColumns+`, family = excluded.family, vocal = excluded.vocal, era = excluded.era, mood = excluded.mood, language = excluded.language,
		digest = excluded.digest, feature_def = excluded.feature_def, computed_at = excluded.computed_at`, args...); err != nil {
		return false, fmt.Errorf("features: write song row: %w", err)
	}
	return true, nil
}

func draftFacets(kind string, f *Facets) *Facets {
	if kind != sounds.SubjectDraft {
		return nil
	}
	return f
}

// drop deletes a Song's row (retired Song, or a Song without a subject); it reports whether a row existed.
func (r *run) drop(id string) (bool, error) {
	res, err := r.c.ExecContext(r.ctx, `DELETE FROM song_features WHERE song_id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("features: drop song row: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// songsOfTakes lists the Songs that own the given takes.
func (r *run) songsOfTakes(takeIDs []string) ([]string, error) {
	var out []string
	for _, t := range takeIDs {
		rows, err := r.c.QueryContext(r.ctx, `SELECT song_id FROM take_songs WHERE track_id = ?`, t)
		if err != nil {
			return nil, fmt.Errorf("features: songs of take: %w", err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, s)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sortedUnique(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// ComputeTx derives the rows of the dirty takes and of the affected Songs inside the caller's transaction (C135.5): the
// Reconciler's phase B step, after the Sounds compute, and the entry of the sounds recompute hook (trackIDs nil). A row is
// rewritten only when its digest changed, so a repeated call writes nothing. It writes only take_features and song_features,
// and the Songs owning a take whose row changed are refreshed in the same call.
func (s *Service) ComputeTx(ctx context.Context, tx *sql.Tx, env Env, trackIDs, songIDs []string) (Changes, error) {
	var ch Changes
	r := newRun(ctx, tx, env)
	defer r.close()
	var changed []string
	for _, id := range sortedUnique(trackIDs) {
		t, err := r.take(id)
		if err != nil {
			return ch, err
		}
		if t.changed {
			ch.Takes++
			changed = append(changed, id)
		}
	}
	owners, err := r.songsOfTakes(changed)
	if err != nil {
		return ch, err
	}
	for _, id := range sortedUnique(append(slices.Clone(songIDs), owners...)) {
		w, err := r.song(id)
		if err != nil {
			return ch, err
		}
		if w {
			ch.Songs++
		}
	}
	return ch, nil
}

// decodeDetail returns detail_json as raw JSON; an absent value is nil.
func decodeDetail(s sql.NullString) json.RawMessage {
	if !s.Valid {
		return nil
	}
	return json.RawMessage(s.String)
}
