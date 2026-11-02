package nextactions

import (
	"context"
	"database/sql"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// rulesB are the core record and song rules of lane NA2 (C139.2): with rulesA they are the seven rules of the core (D-L3-35)
// plus the three that keep Records and Songs honest. Every Find is one batch statement; none names a muses table (the live Muse
// ids arrive through LiveMuses) and none runs the sequencer or any per-row query.
var rulesB = []Rule{
	{
		Code: "record_undecided_slots", Priority: 80, Audience: AudienceBoth, MinScope: auth.ScopeRead,
		Find: findRecordUndecided, Text: textRecordUndecided, Hint: hintRecordChanges,
	},
	{
		Code: "muse_record_candidate", Priority: 75, Audience: AudienceBoth, MinScope: auth.ScopeRead,
		Find: findMuseRecordCandidate, Text: textMuseRecordCandidate, Hint: hintMuseRecordCandidate,
	},
	{
		Code: "song_duel", Priority: 68, Audience: AudienceBoth, MinScope: auth.ScopeRead,
		Find: findSongDuel, Text: textSongDuel, Hint: hintSongDuel,
	},
	{
		Code: "muse_partial_angles", Priority: 60, Audience: AudienceAgent, MinScope: auth.ScopeRead,
		Find: findMusePartialAngles, Text: textMusePartialAngles, Hint: hintMuseUnrendered,
	},
	{
		Code: "record_empty_slot", Priority: 60, Audience: AudienceBoth, MinScope: auth.ScopeRead,
		Find: findRecordEmptySlot, Text: textRecordEmptySlot, Hint: hintGetRecord,
	},
	{
		Code: "song_proposals_waiting", Priority: 55, Audience: AudienceBoth, MinScope: auth.ScopeRead,
		Find: findSongProposals, Text: textSongProposals, Hint: hintGetSong,
	},
}

// museSongsMinCTE is museSongsCTE without the Song state, for a rule that only needs the live Songs of the live Muses (a strong
// Song has a take, so it is never an archived draft). One parameter: the JSON array of live Muse ids.
const museSongsMinCTE = `WITH x AS (SELECT s.muse_id AS muse_id, s.id AS song_id FROM songs s
  WHERE s.muse_id IN (SELECT value FROM json_each(?)) AND s.retired_at IS NULL AND s.hidden_at IS NULL)`

// museSongsLiteCTE is museSongsCTE with `taken` (the Song has a take) in place of the state string: the rules that only tell a
// rendered Song from a draft need not compute judged and picked, which is most of the cost of the state. The fragment is the
// shared store.SongStateSQL, of which only takes, queued_jobs and archived are read. One parameter: the live Muse ids.
const museSongsLiteCTE = `WITH x AS (SELECT s.muse_id AS muse_id, s.id AS song_id, s.draft_id AS draft_id, s.angle AS angle, s.created_at AS created_at,
  (st.takes > 0) AS taken, st.queued_jobs AS queued
  FROM songs s JOIN (` + store.SongStateSQL + `) st ON st.song_id = s.id
  WHERE s.muse_id IN (SELECT value FROM json_each(?)) AND s.retired_at IS NULL AND s.hidden_at IS NULL AND st.archived = 0)`

// StrongStars is the human star rating from which a Song counts as strong (C139.2).
const strongStars = 4

// duelsDecide is the number of counted human blind verdicts that decide a Song's Pick (picks.LadderMinDuels).
const duelsDecide = 3

// subjectTakeSQL is the SQL expression of a Song's subject take (its Pick take, else its newest eligible take) for the Song id
// column col; it is the expression of list_songs, so "strong" here and `min_stars` there agree.
func subjectTakeSQL(col string) string {
	return `COALESCE((SELECT p.take_id FROM song_picks p WHERE p.song_id = ` + col + `),
	(SELECT t.id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = ` + col + ` AND t.status <> 'discarded'
	 ORDER BY t.recorded_at DESC, t.created_at DESC, t.id DESC LIMIT 1))`
}

// findRecordUndecided: a non-hidden Record (any status) with at least one floating slot whose Song's Pick source is newest or
// stars, that is, a slot that plays a Take no human decision backs.
func findRecordUndecided(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	return queryCandidates(ctx, q, `SELECT rs.record_id, COUNT(*), COUNT(*) OVER ()
		FROM record_slots rs JOIN records r ON r.id = rs.record_id JOIN song_picks sp ON sp.song_id = rs.song_id
		WHERE rs.removed_at IS NULL AND rs.pinned_take_id IS NULL AND r.hidden_at IS NULL
		  AND sp.take_id IS NOT NULL AND sp.source IN ('newest', 'stars')
		GROUP BY rs.record_id ORDER BY rs.record_id LIMIT ?`, []any{lim}, scanSubjectCount(SubjectRecord, "slots"))
}

// findMuseRecordCandidate: a live Muse with at least three strong Songs (the subject take has human stars >= 4) that are in no
// live slot of a non-hidden Record.
func findMuseRecordCandidate(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	return queryCandidates(ctx, q, museSongsMinCTE+`, y AS (SELECT muse_id, COUNT(*) AS n FROM x
		WHERE EXISTS (SELECT 1 FROM ratings hs WHERE hs.track_id = `+subjectTakeSQL("x.song_id")+` AND hs.source = 'human' AND hs.stars >= ?)
		  AND NOT EXISTS (SELECT 1 FROM record_slots rs JOIN records rc ON rc.id = rs.record_id
		                   WHERE rs.song_id = x.song_id AND rs.removed_at IS NULL AND rc.hidden_at IS NULL)
		GROUP BY muse_id HAVING COUNT(*) >= 3)
		SELECT muse_id, n, COUNT(*) OVER () FROM y ORDER BY muse_id LIMIT ?`,
		[]any{LiveMuses(q), strongStars, lim}, scanSubjectCount(SubjectMuse, "songs"))
}

// findSongDuel: a live Song with at least two eligible (non-discarded) takes, fewer than three counted human blind verdicts
// between them, and no Song-level pin. The evidence names the two newest takes (private: only the hint reads them).
func findSongDuel(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	scope := SongScope(q)
	return queryCandidates(ctx, q, `WITH sel AS (SELECT id FROM songs WHERE ? = '' OR id IN (SELECT value FROM json_each(?))),
		t0 AS (SELECT ts.song_id AS song_id, tr.id AS take_id, tr.recorded_at AS recorded_at, tr.created_at AS created_at
		  FROM take_songs ts JOIN tracks tr ON tr.id = ts.track_id JOIN songs s ON s.id = ts.song_id
		  WHERE ts.song_id IN (SELECT id FROM sel) AND s.retired_at IS NULL AND s.hidden_at IS NULL AND tr.status <> 'discarded'),
		t AS (SELECT song_id, take_id,
		  ROW_NUMBER() OVER (PARTITION BY song_id ORDER BY recorded_at DESC, created_at DESC, take_id DESC) AS rn,
		  COUNT(*) OVER (PARTITION BY song_id) AS n
		  FROM t0 WHERE song_id IN (SELECT song_id FROM t0 GROUP BY song_id HAVING COUNT(*) >= 2)),
		c AS (SELECT song_id, n, MAX(CASE WHEN rn = 1 THEN take_id END) AS a, MAX(CASE WHEN rn = 2 THEN take_id END) AS b
		  FROM t WHERE n >= 2 GROUP BY song_id),
		k AS (SELECT ta.song_id AS song_id, COUNT(*) AS k FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id
		  JOIN t ta ON ta.take_id = d.slot_a_track_id JOIN t tb ON tb.take_id = d.slot_b_track_id AND tb.song_id = ta.song_id
		  WHERE v.source = 'human' AND d.blind = 1 GROUP BY ta.song_id)
		SELECT c.song_id, c.n, COALESCE(k.k, 0), c.a, c.b, COUNT(*) OVER ()
		FROM c LEFT JOIN k ON k.song_id = c.song_id
		WHERE COALESCE(k.k, 0) < ? AND NOT EXISTS (SELECT 1 FROM song_picks sp WHERE sp.song_id = c.song_id AND sp.pinned_take_id IS NOT NULL)
		ORDER BY c.song_id LIMIT ?`, []any{scope, scope, duelsDecide, lim},
		func(rows *sql.Rows) (Candidate, error) {
			var id, a, b string
			var n, k, total int
			if err := rows.Scan(&id, &n, &k, &a, &b, &total); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectSong, ID: &id}, Evidence: map[string]any{"takes": n, "duels": k, "_a": a, "_b": b, evTotal: total}}, nil
		})
}

// findMusePartialAngles: a live Muse with at least one rendered Song (a take) and at least one draft Song and no queued job.
// The evidence lists the distinct angles of the rendered and of the draft Songs, at most three each in first-draft order (angle
// rank, then creation, then id; a Song with no angle is "untagged"), and names the first draft for the estimate hint.
func findMusePartialAngles(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	return queryCandidates(ctx, q, museSongsLiteCTE+`, g AS (SELECT muse_id, SUM(NOT taken) AS drafts, SUM(taken) AS taken, SUM(queued) AS queued
		FROM x GROUP BY muse_id), f AS (SELECT muse_id, draft_id FROM (SELECT muse_id, draft_id, ROW_NUMBER() OVER (PARTITION BY muse_id
		  ORDER BY CASE WHEN angle LIKE 'remake%' THEN 0 WHEN angle LIKE 'alt-take%' THEN 1 ELSE 2 END, created_at, song_id) AS rn
		  FROM x WHERE NOT taken) WHERE rn = 1),
		a AS (SELECT muse_id, (NOT taken) AS drafty, COALESCE(angle, 'untagged') AS lbl,
		  MIN(CASE WHEN angle LIKE 'remake%' THEN 0 WHEN angle LIKE 'alt-take%' THEN 1 ELSE 2 END) AS rk, MIN(created_at) AS c, MIN(song_id) AS sid
		  FROM x GROUP BY muse_id, (NOT taken), COALESCE(angle, 'untagged')),
		b AS (SELECT muse_id, drafty, lbl, ROW_NUMBER() OVER (PARTITION BY muse_id, drafty ORDER BY rk, c, sid) AS rn,
		  COUNT(*) OVER (PARTITION BY muse_id, drafty) AS cnt FROM a),
		l AS (SELECT muse_id, drafty, group_concat(lbl, ', ' ORDER BY rn) AS names, MAX(cnt) AS cnt FROM b WHERE rn <= 3 GROUP BY muse_id, drafty)
		SELECT g.muse_id, g.taken, g.drafts, f.draft_id, lr.names, lr.cnt, ld.names, ld.cnt, COUNT(*) OVER ()
		FROM g JOIN f ON f.muse_id = g.muse_id JOIN l lr ON lr.muse_id = g.muse_id AND lr.drafty = 0 JOIN l ld ON ld.muse_id = g.muse_id AND ld.drafty = 1
		WHERE g.drafts >= 1 AND g.taken >= 1 AND g.queued = 0 ORDER BY g.muse_id LIMIT ?`, []any{LiveMuses(q), lim},
		func(rows *sql.Rows) (Candidate, error) {
			var id, draft, rNames, dNames string
			var taken, drafts, rCnt, dCnt, total int
			if err := rows.Scan(&id, &taken, &drafts, &draft, &rNames, &rCnt, &dNames, &dCnt, &total); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectMuse, ID: &id}, Evidence: map[string]any{
				"rendered": taken, "drafts": drafts, "draft_id": draft,
				"_rendered_angles": rNames, "_rendered_n": rCnt, "_draft_angles": dNames, "_draft_n": dCnt, evTotal: total}}, nil
		})
}

// findRecordEmptySlot: a non-hidden Record with a live floating slot whose Song has no Pick (the slot resolves to no Take). One
// item per Record; the evidence holds the position of its first empty slot and how many there are.
func findRecordEmptySlot(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	return queryCandidates(ctx, q, `SELECT rs.record_id, MIN(j.key + 1), COUNT(*), COUNT(*) OVER ()
		FROM record_slots rs JOIN records r ON r.id = rs.record_id JOIN json_each(r.order_json) j ON j.value = rs.id
		JOIN songs so ON so.id = rs.song_id LEFT JOIN song_picks sp ON sp.song_id = rs.song_id
		WHERE rs.removed_at IS NULL AND rs.pinned_take_id IS NULL AND r.hidden_at IS NULL AND so.retired_at IS NULL AND sp.take_id IS NULL
		GROUP BY rs.record_id ORDER BY rs.record_id LIMIT ?`, []any{lim},
		func(rows *sql.Rows) (Candidate, error) {
			var id string
			var pos, n, total int
			if err := rows.Scan(&id, &pos, &n, &total); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectRecord, ID: &id}, Evidence: map[string]any{"slot": pos, "empty": n, evTotal: total}}, nil
		})
}

// findSongProposals: a live Song with at least one open Pick proposal.
func findSongProposals(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	return queryCandidates(ctx, q, `SELECT pp.song_id, COUNT(*), COUNT(*) OVER ()
		FROM pick_proposals pp JOIN songs s ON s.id = pp.song_id
		WHERE pp.state = 'open' AND s.retired_at IS NULL AND s.hidden_at IS NULL
		GROUP BY pp.song_id ORDER BY pp.song_id LIMIT ?`, []any{lim}, scanSubjectCount(SubjectSong, "proposals"))
}

// scanSubjectCount scans (id, n, total) rows into candidates of the subject kind with the count under key.
func scanSubjectCount(kind, key string) func(rows *sql.Rows) (Candidate, error) {
	return func(rows *sql.Rows) (Candidate, error) {
		var id string
		var n, total int
		if err := rows.Scan(&id, &n, &total); err != nil {
			return Candidate{}, err
		}
		return Candidate{Subject: Subject{Kind: kind, ID: &id}, Evidence: map[string]any{key: n, evTotal: total}}, nil
	}
}

func hintRecordChanges(c Candidate, _ auth.Principal) *Hint {
	return &Hint{Op: "get_record", Args: map[string]any{"id": subjectID(c.Subject), "include": []any{"changes"}}}
}

func hintGetRecord(c Candidate, _ auth.Principal) *Hint {
	return &Hint{Op: "get_record", Args: map[string]any{"id": subjectID(c.Subject)}}
}

func hintGetSong(c Candidate, _ auth.Principal) *Hint {
	return &Hint{Op: "get_song", Args: map[string]any{"id": subjectID(c.Subject)}}
}

func hintMuseRecordCandidate(c Candidate, _ auth.Principal) *Hint {
	return &Hint{Op: "list_songs", Args: map[string]any{"muse_id": subjectID(c.Subject), "min_stars": strongStars, "in_record": false}}
}

// hintSongDuel is the duel between the two newest takes when the viewer may write, else the Song itself.
func hintSongDuel(c Candidate, viewer auth.Principal) *Hint {
	a, b := evidenceString(c, "_a"), evidenceString(c, "_b")
	if viewer.Has(auth.ScopeWrite) && a != "" && b != "" {
		return &Hint{Op: "create_duel", Args: map[string]any{"a_track_id": a, "b_track_id": b, "blind": true}}
	}
	return hintGetSong(c, viewer)
}

// angleList renders the angles of the evidence key, with an ellipsis when more exist than the three listed.
func angleList(c Candidate, key string) string {
	s := evidenceString(c, "_"+key+"_angles")
	if evidenceInt(c, "_"+key+"_n") > 3 {
		s = strings.TrimSpace(s) + ", …"
	}
	return s
}
