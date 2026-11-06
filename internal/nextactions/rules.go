package nextactions

import (
	"context"
	"database/sql"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Rules is the registry (extension move 3): the rules of lanes NA1 (rules_a.go), NA2 (rules_b.go) and NA3 (rules_c.go) and M5-C1 (rules_d.go) in
// that order, pinned by testdata/rules_v1_{a,b,c,d}.golden.json. The engine orders by (priority desc, code, subject id), so the order here
// is only the order of evaluation.
var Rules = slices.Concat(rulesA, rulesB, rulesC, rulesD)

// museSongsCTE is the shared head of every statement about the Songs of the live Muses: one row per live, non-hidden, non-archived
// Song of a live Muse with its state (the SQL of C90.4), queued job count, angle and draft. It takes one parameter, the JSON
// array of live Muse ids from LiveMuses(q). Rules name no muses table: the ids arrive as a parameter.
const museSongsCTE = `WITH x AS (SELECT s.muse_id AS muse_id, s.id AS song_id, s.draft_id AS draft_id, s.angle AS angle, s.created_at AS created_at,
  ` + store.SongStateCase + ` AS state, st.queued_jobs AS queued
  FROM songs s JOIN (` + store.SongStateSQL + `) st ON st.song_id = s.id
  WHERE s.muse_id IN (SELECT value FROM json_each(?)) AND s.retired_at IS NULL AND s.hidden_at IS NULL AND st.archived = 0)`

// firstDraftCTE adds f(muse_id, draft_id): the first draft Song of each Muse in angle order (remake angles, then alt-take angles,
// then any other, including long), then Song creation time, then Song id (C139.2 detail 1). It extends museSongsCTE.
const firstDraftCTE = `, f AS (SELECT muse_id, draft_id FROM (SELECT muse_id, draft_id, ROW_NUMBER() OVER (PARTITION BY muse_id
  ORDER BY CASE WHEN angle LIKE 'remake%' THEN 0 WHEN angle LIKE 'alt-take%' THEN 1 ELSE 2 END, created_at, song_id) AS rn
  FROM x WHERE state = 'draft') WHERE rn = 1)`

// queryCandidates runs one batch statement and scans its rows with scan; rows are returned in statement order.
func queryCandidates(ctx context.Context, q Reader, query string, args []any, scan func(rows *sql.Rows) (Candidate, error)) ([]Candidate, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Candidate{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// evidenceString reads a string evidence value ("" when absent).
func evidenceString(c Candidate, key string) string {
	s, _ := c.Evidence[key].(string)
	return s
}

// evidenceInt reads an integer evidence value (0 when absent).
func evidenceInt(c Candidate, key string) int {
	n, _ := evInt(c.Evidence, key)
	return n
}
