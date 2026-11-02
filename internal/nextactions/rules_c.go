package nextactions

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// unmappedSongsMin is the number of live Songs that must hold an unmapped term before the rule proposes a mapping.
const unmappedSongsMin = 5

// rulesC are the demotable rules of lane NA3 (C139.2, D-L3-35): cutting the lane deletes this file and its golden. Every Find is
// one batch statement; none runs the sequencer or any per-row query, and the Song-level ones restrict through SongScope.
var rulesC = []Rule{
	{
		Code: "song_tempo_mismatch", Priority: 50, Audience: AudienceAgent, MinScope: auth.ScopeRead,
		Find: findSongTempoMismatch, Text: textSongTempoMismatch, Hint: hintGetSong,
	},
	{
		Code: "record_unsequenced", Priority: 45, Audience: AudienceBoth, MinScope: auth.ScopeRead,
		Find: findRecordUnsequenced, Text: textRecordUnsequenced, Hint: hintSuggestSequence,
	},
	{
		Code: "taxonomy_unmapped", Priority: 35, Audience: AudienceAgent, MinScope: auth.ScopeRead,
		Find: findTaxonomyUnmapped, Text: textTaxonomyUnmapped, Hint: hintUnmappedTaxonomy,
	},
	{
		Code: "song_no_muse", Priority: 25, Audience: AudienceAgent, MinScope: auth.ScopeRead,
		Find: findSongNoMuse, Text: textSongNoMuse, Hint: hintGetSong,
	},
	{
		Code: "features_missing", Priority: 20, Audience: AudienceHuman, MinScope: auth.ScopeRead,
		Find: findFeaturesMissing, Text: textFeaturesMissing,
	},
}

// songScopeCTE restricts a statement to the Songs the call is about; pass SongScope(q) twice.
const songScopeCTE = `WITH sel AS (SELECT id FROM songs WHERE ? = '' OR id IN (SELECT value FROM json_each(?)))`

// findSongTempoMismatch: a live Song whose collapsed tempo_band row says the measured tempo contradicts the requested one and is
// not an octave error of the estimator. The evidence holds both tempi as whole BPM.
func findSongTempoMismatch(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	scope := SongScope(q)
	return queryCandidates(ctx, q, songScopeCTE+`
		SELECT ss.song_id, CAST(ROUND(COALESCE(json_extract(ss.detail_json, '$.requested_bpm'), 0)) AS INTEGER),
		       CAST(ROUND(COALESCE(json_extract(ss.detail_json, '$.measured_bpm'), 0)) AS INTEGER), COUNT(*) OVER ()
		FROM song_sounds ss JOIN songs s ON s.id = ss.song_id
		WHERE ss.song_id IN (SELECT id FROM sel) AND ss.facet = 'tempo_band' AND s.retired_at IS NULL AND s.hidden_at IS NULL
		  AND json_extract(ss.detail_json, '$.mismatch') IN (1, 'true')
		  AND COALESCE(json_extract(ss.detail_json, '$.octave'), 0) NOT IN (1, 'true')
		ORDER BY ss.song_id LIMIT ?`, []any{scope, scope, lim},
		func(rows *sql.Rows) (Candidate, error) {
			var id string
			var req, got, total int
			if err := rows.Scan(&id, &req, &got, &total); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectSong, ID: &id}, Evidence: map[string]any{"requested_bpm": req, "measured_bpm": got, evTotal: total}}, nil
		})
}

// findRecordUnsequenced: a non-hidden draft Record with at least SequencePrecheckSlotsMin live slots, no record_sequences row ever,
// and at least two live slots whose Song has a tempo or an energy feature. It only counts rows: the planner never runs here.
func findRecordUnsequenced(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	return queryCandidates(ctx, q, `SELECT rs.record_id, COUNT(*), COUNT(*) OVER ()
		FROM record_slots rs JOIN records r ON r.id = rs.record_id
		LEFT JOIN song_features sf ON sf.song_id = rs.song_id
		WHERE rs.removed_at IS NULL AND r.status = 'draft' AND r.hidden_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM record_sequences x WHERE x.record_id = rs.record_id)
		GROUP BY rs.record_id
		HAVING COUNT(*) >= ? AND SUM(sf.bpm_x100 IS NOT NULL OR sf.energy_x10000 IS NOT NULL) >= 2
		ORDER BY rs.record_id LIMIT ?`, []any{SequencePrecheckSlotsMin, lim}, scanSubjectCount(SubjectRecord, "slots"))
}

// findTaxonomyUnmapped: a term unmapped in at least unmappedSongsMin live Songs and named by no mapping of an open proposal. The
// subject is the taxonomy with the term as its id (a style word, never Muse text), so items order and page by term.
func findTaxonomyUnmapped(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	return queryCandidates(ctx, q, `SELECT u.term, COUNT(*), COUNT(*) OVER ()
		FROM song_unmapped_terms u JOIN songs s ON s.id = u.song_id
		WHERE s.retired_at IS NULL AND s.hidden_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM taxonomy_proposals p, json_each(p.mappings_json) m
		                   WHERE p.state = 'open' AND lower(json_extract(m.value, '$.term')) = lower(u.term))
		GROUP BY u.term HAVING COUNT(*) >= ? ORDER BY u.term LIMIT ?`, []any{unmappedSongsMin, lim},
		func(rows *sql.Rows) (Candidate, error) {
			var term string
			var n, total int
			if err := rows.Scan(&term, &n, &total); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectTaxonomy, ID: &term}, Evidence: map[string]any{"term": term, "songs": n, evTotal: total}}, nil
		})
}

// findSongNoMuse: a live Song with a take whose subject take has a known recipe, no Muse and no manual clear. The originals and
// the reference Songs have no known recipe and are left out: by design they have no Muse and there is no dismissal.
func findSongNoMuse(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	scope := SongScope(q)
	return queryCandidates(ctx, q, songScopeCTE+`
		SELECT s.id, COUNT(*) OVER () FROM songs s
		WHERE s.id IN (SELECT id FROM sel) AND s.muse_id IS NULL AND s.muse_source IS NOT 'manual' AND s.retired_at IS NULL AND s.hidden_at IS NULL
		  AND EXISTS (SELECT 1 FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = `+subjectTakeSQL("s.id")+` AND r.engine <> 'unknown')
		ORDER BY s.id LIMIT ?`, []any{scope, scope, lim},
		func(rows *sql.Rows) (Candidate, error) {
			var id string
			var total int
			if err := rows.Scan(&id, &total); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectSong, ID: &id}, Evidence: map[string]any{evTotal: total}}, nil
		})
}

// findFeaturesMissing: one library-level item counting the live Songs whose subject is a take with a master and with no measured
// tempo, key or energy (the Ears analyzers have not run on it).
func findFeaturesMissing(ctx context.Context, q Reader, _ int) ([]Candidate, error) {
	return queryCandidates(ctx, q, `SELECT COUNT(*) FROM song_features sf JOIN songs s ON s.id = sf.song_id
		JOIN tracks t ON t.id = sf.subject_id AND sf.subject_kind = 'take'
		LEFT JOIN take_features tf ON tf.track_id = t.id
		WHERE s.retired_at IS NULL AND s.hidden_at IS NULL AND t.master_path <> ''
		  AND (tf.track_id IS NULL OR (tf.bpm_measured_x100 IS NULL AND COALESCE(tf.key_basis, '') <> 'measured' AND COALESCE(tf.energy_basis, '') <> 'measured'))
		HAVING COUNT(*) >= 1`, nil,
		func(rows *sql.Rows) (Candidate, error) {
			var n int
			if err := rows.Scan(&n); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectLibrary}, Evidence: map[string]any{"songs": n}}, nil
		})
}

func hintSuggestSequence(c Candidate, _ auth.Principal) *Hint {
	return &Hint{Op: "suggest_sequence", Args: map[string]any{"record_id": subjectID(c.Subject)}}
}

func hintUnmappedTaxonomy(Candidate, auth.Principal) *Hint {
	return &Hint{Op: "get_sound_taxonomy", Args: map[string]any{"unmapped": true}}
}

// The texts of the rules (C139.2).

func textSongTempoMismatch(c Candidate, n Names) string {
	return fmt.Sprintf("Song \"%s\": requested %d BPM, measured about %d — the tempo cue was probably ignored; write a stronger tempo cue into the style and estimate it (estimate_variant, mode style_edit, with your own patch.style)",
		n.Song(subjectID(c.Subject)), evidenceInt(c, "requested_bpm"), evidenceInt(c, "measured_bpm"))
}

func textRecordUnsequenced(c Candidate, n Names) string {
	return fmt.Sprintf("Record \"%s\" has not been sequenced — preview the order (suggest_sequence, arc)", n.Record(subjectID(c.Subject)))
}

func textTaxonomyUnmapped(c Candidate, _ Names) string {
	return fmt.Sprintf("The term \"%s\" is unmapped in %d Songs — propose a mapping (propose_taxonomy)", evidenceString(c, "term"), evidenceInt(c, "songs"))
}

func textSongNoMuse(c Candidate, n Names) string {
	return fmt.Sprintf("Song \"%s\" has no Muse — if you know its inspiration: create_muse, then assign_muse_songs", n.Song(subjectID(c.Subject)))
}

func textFeaturesMissing(c Candidate, _ Names) string {
	return fmt.Sprintf("%d Song(s) have no audio measurements yet — run \"metamusic analyze\" on the server", evidenceInt(c, "songs"))
}
