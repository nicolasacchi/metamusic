package sounds

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// soundRow is one derived facet row of a subject.
type soundRow struct {
	Facet, Value, Origin string
	Detail               any // nil or a value marshalled to detail_json
}

// tempoMeasure is the Ears tempo analysis of a take (C94).
type tempoMeasure struct {
	BPM        *float64 `json:"bpm"`
	Confidence *float64 `json:"confidence"`
	Version    string   `json:"-"`
	Sum        string   `json:"-"`
}

// input is everything the facets of one subject are a function of (C93.4).
type input struct {
	Engine, Style, Lyrics string
	Declared              *float64 // tracks.bpm_declared
	Plan                  *float64 // scores.bpm
	Measured              *tempoMeasure
}

func (in input) reference() bool { return in.Engine == "unknown" }

// stmts caches the prepared statements of one transaction so a batch of subjects prepares each statement once (the
// recompute of thousands of subjects was dominated by sqlite prepare). It satisfies querier and adds ExecContext.
type stmts struct {
	tx *sql.Tx
	m  map[string]*sql.Stmt
}

func newStmts(tx *sql.Tx) *stmts { return &stmts{tx: tx, m: map[string]*sql.Stmt{}} }

func (c *stmts) prep(ctx context.Context, q string) (*sql.Stmt, error) {
	if st, ok := c.m[q]; ok {
		return st, nil
	}
	st, err := c.tx.PrepareContext(ctx, q)
	if err != nil {
		return nil, err
	}
	c.m[q] = st
	return st, nil
}

func (c *stmts) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	st, err := c.prep(ctx, q)
	if err != nil {
		return nil, err
	}
	return st.QueryContext(ctx, args...)
}

func (c *stmts) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	st, err := c.prep(ctx, q)
	if err != nil {
		return c.tx.QueryRowContext(ctx, q, args...) // surfaces the prepare error through Scan
	}
	return st.QueryRowContext(ctx, args...)
}

func (c *stmts) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	st, err := c.prep(ctx, q)
	if err != nil {
		return nil, err
	}
	return st.ExecContext(ctx, args...)
}

func (c *stmts) Close() {
	for _, st := range c.m {
		_ = st.Close()
	}
}

func sum256(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // the digests marshal plain structs
	}
	return string(b)
}

// takeDigest is the input digest of a take subject (C93.4).
func takeDigest(version int, in input) string {
	var mv, ms string
	if in.Measured != nil {
		mv, ms = in.Measured.Version, in.Measured.Sum
	}
	return sum256(jsonString(struct {
		Version  int      `json:"taxonomy_version"`
		Engine   string   `json:"engine"`
		Style    string   `json:"style"`
		Lyrics   string   `json:"lyrics_sha256"`
		Declared *float64 `json:"declared_bpm"`
		Plan     *float64 `json:"score_bpm"`
		TempoV   string   `json:"tempo_version"`
		TempoSum string   `json:"tempo_sha256"`
	}{version, in.Engine, in.Style, sum256(in.Lyrics), in.Declared, in.Plan, mv, ms}))
}

func fptr(v float64) *float64 { return &v }

// buildRows derives the facet rows of a subject (C93.4). Reference takes yield only the measured tempo; everything
// else comes from the recipe: engine, the style phrases, the lyrics language and one tempo row per available basis.
func buildRows(ix *Index, in input) ([]soundRow, Parsed) {
	var rows []soundRow
	var parsed Parsed
	if !in.reference() {
		rows = append(rows, soundRow{Facet: "engine", Value: in.Engine, Origin: "recipe"})
		parsed = Parse(ix, in.Style)
		for _, m := range parsed.Matches {
			rows = append(rows, soundRow{Facet: m.Facet, Value: m.Value, Origin: "style"})
		}
		if parsed.Language == "" {
			if l := yue2.DetectLanguage(in.Lyrics); l != "" {
				rows = append(rows, soundRow{Facet: "language", Value: l, Origin: "lyrics"})
			}
		}
		declared := in.Declared
		if declared == nil && parsed.BPM > 0 {
			declared = fptr(float64(parsed.BPM))
		}
		if declared != nil {
			rows = append(rows, tempoRow(*declared, "declared", map[string]any{"bpm": *declared}))
		}
		if in.Plan != nil && (declared == nil || math.Abs(*in.Plan-*declared) >= 0.5) {
			rows = append(rows, tempoRow(*in.Plan, "plan", map[string]any{"bpm": *in.Plan}))
		}
	}
	if m := in.Measured; m != nil && m.BPM != nil {
		d := map[string]any{"bpm": *m.BPM}
		if m.Confidence != nil {
			d["confidence"] = *m.Confidence
		}
		rows = append(rows, tempoRow(*m.BPM, "measured", d))
	}
	return dedupe(rows, true), parsed
}

func tempoRow(bpm float64, origin string, detail map[string]any) soundRow {
	return soundRow{Facet: "tempo_band", Value: TempoBand(bpm), Origin: origin, Detail: detail}
}

// dedupe drops repeated (facet, value[, origin]) keys, first wins.
func dedupe(rows []soundRow, byOrigin bool) []soundRow {
	seen := map[[3]string]bool{}
	out := rows[:0:0]
	for _, r := range rows {
		k := [3]string{r.Facet, r.Value, ""}
		if byOrigin {
			k[2] = r.Origin
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

// collapseTempo turns the tempo rows of a subject into the single tempo_band row of a Song (D-L1-17): the value
// comes from the effective BPM (measured, else declared, else plan), the detail is a TempoDetail.
func collapseTempo(in input, declaredParsed int) (soundRow, bool) {
	var requested, plan, measured *float64
	if !in.reference() {
		requested = in.Declared
		if requested == nil && declaredParsed > 0 {
			requested = fptr(float64(declaredParsed))
		}
		plan = in.Plan
	}
	if in.Measured != nil {
		measured = in.Measured.BPM
	}
	var bpm float64
	var basis string
	switch {
	case measured != nil:
		bpm, basis = *measured, "measured"
	case requested != nil:
		bpm, basis = *requested, "declared"
	case plan != nil:
		bpm, basis = *plan, "plan"
	default:
		return soundRow{}, false
	}
	cmp := requested
	if cmp == nil {
		cmp = plan
	}
	mismatch, octave := CompareTempo(cmp, measured)
	d := TempoDetail{RequestedBPM: cmp, PlanBPM: plan, MeasuredBPM: measured, Basis: basis, Mismatch: mismatch, Octave: octave}
	return soundRow{Facet: "tempo_band", Value: TempoBand(bpm), Origin: basis, Detail: d}, true
}

func detailJSON(d any) any {
	if d == nil {
		return nil
	}
	return jsonString(d)
}

// loadTake reads the inputs of one take; ok is false when the take does not exist.
func loadTake(ctx context.Context, q querier, id string) (in input, ok bool, err error) {
	var declared, plan sql.NullFloat64
	err = q.QueryRowContext(ctx, `SELECT r.engine, r.style, r.lyrics, t.bpm_declared, sc.bpm
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id LEFT JOIN scores sc ON sc.track_id = t.id WHERE t.id = ?`, id).
		Scan(&in.Engine, &in.Style, &in.Lyrics, &declared, &plan)
	if err == sql.ErrNoRows {
		return in, false, nil
	}
	if err != nil {
		return in, false, fmt.Errorf("sounds: load take: %w", err)
	}
	if declared.Valid {
		in.Declared = fptr(declared.Float64)
	}
	if plan.Valid {
		in.Plan = fptr(plan.Float64)
	}
	if strings.TrimSpace(in.Style) == "" {
		terms, err := stringsOf(ctx, q, `SELECT term FROM track_style_terms WHERE track_id = ? ORDER BY term`, id)
		if err != nil {
			return in, false, err
		}
		in.Style = strings.Join(terms, ", ")
	}
	var ver, metrics string
	switch err := q.QueryRowContext(ctx, `SELECT version, metrics_json FROM analyses WHERE track_id = ? AND analyzer = 'tempo'
		ORDER BY created_at DESC, version DESC LIMIT 1`, id).Scan(&ver, &metrics); err {
	case nil:
		m := &tempoMeasure{Version: ver, Sum: sum256(metrics)}
		_ = json.Unmarshal([]byte(metrics), m) // an unreadable analysis is "no tempo", never an error
		in.Measured = m
	case sql.ErrNoRows:
	default:
		return in, false, fmt.Errorf("sounds: load tempo: %w", err)
	}
	return in, true, nil
}

func stringsOf(ctx context.Context, q querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sounds: query: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// subject is the representative of a Song: its Pick take, else its newest eligible take, else its draft.
type subject struct {
	Kind   string // take | draft | none
	ID     string
	In     input
	Digest string
}

func loadSubject(ctx context.Context, q querier, version int, songID string) (subject, error) {
	var takeID sql.NullString
	err := q.QueryRowContext(ctx, `SELECT take_id FROM song_picks WHERE song_id = ? AND take_id IS NOT NULL`, songID).Scan(&takeID)
	if err != nil && err != sql.ErrNoRows {
		return subject{}, fmt.Errorf("sounds: song pick: %w", err)
	}
	if !takeID.Valid {
		err = q.QueryRowContext(ctx, `SELECT t.id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id
			WHERE ts.song_id = ? AND t.status <> 'discarded' ORDER BY t.recorded_at DESC, t.id DESC LIMIT 1`, songID).Scan(&takeID)
		if err != nil && err != sql.ErrNoRows {
			return subject{}, fmt.Errorf("sounds: newest take: %w", err)
		}
	}
	if takeID.Valid {
		in, ok, err := loadTake(ctx, q, takeID.String)
		if err != nil {
			return subject{}, err
		}
		if ok {
			return subject{Kind: "take", ID: takeID.String, In: in, Digest: takeDigest(version, in)}, nil
		}
	}
	var draft, req sql.NullString
	var rev int
	err = q.QueryRowContext(ctx, `SELECT d.id, d.request_json, d.revision FROM songs s JOIN recipe_drafts d ON d.id = s.draft_id WHERE s.id = ?`, songID).Scan(&draft, &req, &rev)
	if err != nil && err != sql.ErrNoRows {
		return subject{}, fmt.Errorf("sounds: song draft: %w", err)
	}
	if draft.Valid {
		var r struct {
			Style  string `json:"style"`
			Lyrics string `json:"lyrics"`
		}
		_ = json.Unmarshal([]byte(req.String), &r)
		in := input{Engine: "yue2", Style: r.Style, Lyrics: r.Lyrics}
		return subject{Kind: "draft", ID: draft.String, In: in,
			Digest: sum256(jsonString(struct {
				Revision int    `json:"revision"`
				Style    string `json:"style"`
				Lyrics   string `json:"lyrics_sha256"`
			}{rev, in.Style, sum256(in.Lyrics)}))}, nil
	}
	return subject{Kind: "none", Digest: sum256("none")}, nil
}

func songDigest(version int, sub subject) string {
	return sum256(jsonString(struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		Digest  string `json:"digest"`
		Version int    `json:"taxonomy_version"`
	}{sub.Kind, sub.ID, sub.Digest, version}))
}

// stateOf returns the stored freshness of a subject; found is false without a row.
func stateOf(ctx context.Context, q querier, kind, id string) (version int, digest string, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT taxonomy_version, input_digest FROM sound_state WHERE subject_kind = ? AND subject_id = ?`, kind, id).Scan(&version, &digest)
	if err == sql.ErrNoRows {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("sounds: sound state: %w", err)
	}
	return version, digest, true, nil
}

func writeState(ctx context.Context, tx *stmts, env Env, kind, id string, version int, digest string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO sound_state(subject_kind, subject_id, taxonomy_version, input_digest, computed_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(subject_kind, subject_id) DO UPDATE SET taxonomy_version = excluded.taxonomy_version,
		input_digest = excluded.input_digest, computed_at = excluded.computed_at`, kind, id, version, digest, store.FormatTime(env.Now))
	if err != nil {
		return fmt.Errorf("sounds: write state: %w", err)
	}
	return nil
}

// insertSounds writes all rows of a subject with one multi-row INSERT (the statement text is cached per row count).
// table and keyCol are constants of this package.
func insertSounds(ctx context.Context, tx *stmts, table, keyCol, id string, rows []soundRow) error {
	if len(rows) == 0 {
		return nil
	}
	args := make([]any, 0, len(rows)*5)
	for _, r := range rows {
		args = append(args, id, r.Facet, r.Value, r.Origin, detailJSON(r.Detail))
	}
	q := `INSERT INTO ` + table + `(` + keyCol + `, facet, value, origin, detail_json) VALUES ` +
		strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?),", len(rows)), ",")
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

// computeTake recomputes the facets of one take unless its digest and taxonomy version are current.
func (s *Service) computeTake(ctx context.Context, tx *stmts, env Env, ix *Index, id string) (bool, error) {
	in, ok, err := loadTake(ctx, tx, id)
	if err != nil || !ok {
		return false, err
	}
	digest := takeDigest(ix.Version, in)
	v, d, found, err := stateOf(ctx, tx, "take", id)
	if err != nil {
		return false, err
	}
	if found && v == ix.Version && d == digest {
		return false, nil
	}
	rows, _ := buildRows(ix, in)
	if _, err := tx.ExecContext(ctx, `DELETE FROM take_sounds WHERE track_id = ?`, id); err != nil {
		return false, fmt.Errorf("sounds: clear take sounds: %w", err)
	}
	if err := insertSounds(ctx, tx, "take_sounds", "track_id", id, rows); err != nil {
		return false, fmt.Errorf("sounds: write take sounds: %w", err)
	}
	return true, writeState(ctx, tx, env, "take", id, ix.Version, digest)
}

// computeSong recomputes the facets of one Song from its representative subject unless fresh.
func (s *Service) computeSong(ctx context.Context, tx *stmts, env Env, ix *Index, id string) (bool, error) {
	sub, err := loadSubject(ctx, tx, ix.Version, id)
	if err != nil {
		return false, err
	}
	digest := songDigest(ix.Version, sub)
	v, d, found, err := stateOf(ctx, tx, "song", id)
	if err != nil {
		return false, err
	}
	if found && v == ix.Version && d == digest {
		return false, nil
	}
	var rows []soundRow
	var parsed Parsed
	if sub.Kind != "none" {
		var all []soundRow
		all, parsed = buildRows(ix, sub.In)
		for _, r := range all {
			if r.Facet != "tempo_band" {
				rows = append(rows, r)
			}
		}
		if c, ok := collapseTempo(sub.In, parsed.BPM); ok {
			rows = append(rows, c)
		}
		rows = dedupe(rows, false)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM song_sounds WHERE song_id = ?`, id); err != nil {
		return false, fmt.Errorf("sounds: clear song sounds: %w", err)
	}
	if err := insertSounds(ctx, tx, "song_sounds", "song_id", id, rows); err != nil {
		return false, fmt.Errorf("sounds: write song sounds: %w", err)
	}
	if err := replaceUnmapped(ctx, tx, id, parsed.Unmapped); err != nil {
		return false, err
	}
	return true, writeState(ctx, tx, env, "song", id, ix.Version, digest)
}

// ComputeTx recomputes the facets of the dirty takes and the affected Songs inside the Reconciler's transaction (C93.4).
func (s *Service) ComputeTx(ctx context.Context, tx *sql.Tx, env Env, trackIDs, songIDs []string) (Changes, error) {
	var ch Changes
	ix, err := s.indexIn(ctx, tx)
	if err != nil {
		return ch, err
	}
	c := newStmts(tx)
	defer c.Close()
	for _, id := range sortedUnique(trackIDs) {
		changed, err := s.computeTake(ctx, c, env, ix, id)
		if err != nil {
			return ch, err
		}
		if changed {
			ch.Takes++
		}
	}
	for _, id := range sortedUnique(songIDs) {
		var retired bool
		if err := c.QueryRowContext(ctx, `SELECT retired_at IS NOT NULL FROM songs WHERE id = ?`, id).Scan(&retired); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return ch, fmt.Errorf("sounds: song: %w", err)
		}
		if retired {
			continue
		}
		changed, err := s.computeSong(ctx, c, env, ix, id)
		if err != nil {
			return ch, err
		}
		if changed {
			ch.Songs++
		}
	}
	return ch, nil
}

func sortedUnique(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// SweepStaleTx recomputes up to limit stale subjects (no state, an older taxonomy version, or a changed digest).
func (s *Service) SweepStaleTx(ctx context.Context, tx *sql.Tx, env Env, limit int) (n int, err error) {
	ch, err := s.sweep(ctx, tx, env, limit, true, nil)
	return ch.Takes + ch.Songs, err
}

// sweepCursor is the keyset position of a multi-batch sweep.
type sweepCursor struct{ take, song string }

// sweep recomputes up to limit stale subjects. Subjects without a state row or on an older taxonomy version are found
// by SQL; with scanDigests the remaining budget also re-derives every other subject's digest to catch changed inputs.
//
// A non-nil cur makes consecutive calls resume after the last stale id handled (keyset), so a recompute of n subjects
// in batches costs O(n) instead of rescanning the handled prefix on every batch.
func (s *Service) sweep(ctx context.Context, tx *sql.Tx, env Env, limit int, scanDigests bool, cur *sweepCursor) (Changes, error) {
	var ch Changes
	if limit <= 0 {
		return ch, nil
	}
	ix, err := s.indexIn(ctx, tx)
	if err != nil {
		return ch, err
	}
	c := newStmts(tx)
	defer c.Close()
	stale := func(table, kind, extra, after string) ([]string, error) {
		return stringsOf(ctx, tx, `SELECT x.id FROM `+table+` x WHERE `+extra+` AND x.id > ? AND NOT EXISTS (SELECT 1 FROM sound_state ss
			WHERE ss.subject_kind = ? AND ss.subject_id = x.id AND ss.taxonomy_version = ?) ORDER BY x.id LIMIT ?`, after, kind, ix.Version, limit)
	}
	var afterTake, afterSong string
	if cur != nil {
		afterTake, afterSong = cur.take, cur.song
	}
	takes, err := stale("tracks", "take", "1 = 1", afterTake)
	if err != nil {
		return ch, err
	}
	songIDs, err := stale("songs", "song", "x.retired_at IS NULL", afterSong)
	if err != nil {
		return ch, err
	}
	budget := limit
	for _, id := range takes {
		if budget == 0 {
			return ch, nil
		}
		if changed, err := s.computeTake(ctx, c, env, ix, id); err != nil {
			return ch, err
		} else if changed {
			ch.Takes++
			budget--
		}
		if cur != nil {
			cur.take = id
		}
	}
	for _, id := range songIDs {
		if budget == 0 {
			return ch, nil
		}
		if changed, err := s.computeSong(ctx, c, env, ix, id); err != nil {
			return ch, err
		} else if changed {
			ch.Songs++
			budget--
		}
		if cur != nil {
			cur.song = id
		}
	}
	if !scanDigests {
		return ch, nil
	}
	for _, t := range []struct{ query, kind string }{
		{`SELECT id FROM tracks ORDER BY id`, "take"},
		{`SELECT id FROM songs WHERE retired_at IS NULL ORDER BY id`, "song"},
	} {
		ids, err := stringsOf(ctx, tx, t.query)
		if err != nil {
			return ch, err
		}
		for _, id := range ids {
			if budget == 0 {
				return ch, nil
			}
			var changed bool
			if t.kind == "take" {
				changed, err = s.computeTake(ctx, c, env, ix, id)
			} else {
				changed, err = s.computeSong(ctx, c, env, ix, id)
			}
			if err != nil {
				return ch, err
			}
			if changed {
				budget--
				if t.kind == "take" {
					ch.Takes++
				} else {
					ch.Songs++
				}
			}
		}
	}
	return ch, nil
}
