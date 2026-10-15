package board

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Limits of C97.
const (
	// MaxPageBytes is the page cap of every Board response, for every detail and every caller.
	MaxPageBytes = 90000
	// UnassignedChips caps the chips of the Unassigned row per response (the counts stay complete).
	UnassignedChips = 200
	// DefaultLimit and MaxLimit bound the Muse rows of a page.
	DefaultLimit  = 200
	MaxLimit      = 500
	boardEnvelope = 512
)

func invalid(field, msg string) error {
	return problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

type museCursor struct {
	Fold string `json:"f"`
	ID   string `json:"i"`
}

type chipCursor struct {
	At string `json:"c"`
	ID string `json:"i"`
}

func decode[T any](c, field string) (v T, err error) {
	b, e := base64.RawURLEncoding.DecodeString(c)
	if e != nil || json.Unmarshal(b, &v) != nil {
		return v, invalid(field, "malformed cursor")
	}
	return v, nil
}

func encode(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

// filter reports the Song filter of a Query and whether any narrowing filter (not just visibility) is active.
func (q Query) filter() (songs.ListFilter, bool) {
	f := songs.ListFilter{MuseID: q.MuseID, Angle: q.Angle, SessionID: q.SessionID, Sounds: q.Sounds, Q: q.Q, IncludeHidden: q.IncludeHidden}
	for _, s := range q.States {
		f.States = append(f.States, songs.State(s))
	}
	narrowing := q.Angle != "" || q.SessionID != "" || len(q.States) > 0 || len(q.Sounds) > 0 || strings.TrimSpace(q.Q) != ""
	return f, narrowing
}

type group struct {
	museID, angle    sql.NullString
	state            string
	songs, queuedJob int
}

// Get assembles one Board page (C97): one read transaction, at most five statements — the Muse rows, their count, one
// grouped aggregate over the whole filter (row counts, columns, totals), the chips of the page's rows plus the
// first Unassigned chips (detail=chips only) and the taxonomy version.
func (s *Service) Get(ctx context.Context, q Query) (Board, error) {
	switch q.Detail {
	case "", "counts", "chips":
	default:
		return Board{}, invalid("detail", "detail must be chips or counts")
	}
	chips := q.Detail == "chips"
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	for _, st := range q.States {
		switch st {
		case "draft", "rendered", "judged", "picked":
		default:
			return Board{}, invalid("state", "unknown state")
		}
	}
	f, narrowing := q.filter()
	where, wargs := songs.FilterSQL(f, "")
	if !q.IncludeHidden {
		where += " AND (s.muse_id IS NULL OR s.muse_id IN (SELECT id FROM muses WHERE hidden_at IS NULL))"
	}

	tx, err := s.st.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Board{}, fmt.Errorf("board: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	b := Board{GeneratedAt: store.FormatTime(s.now()), Columns: []Column{}, Rows: []Row{}}
	unassignedOnly := q.UnassignedAfter != ""

	// (1) the Muse rows of this page
	type museRow struct {
		ref  muses.MuseRef
		fold string
	}
	var rows []museRow
	more := false
	mWhere := []string{"1 = 1"}
	var margs []any
	switch {
	case q.MuseID != "":
		mWhere = append(mWhere, "m.id = ?")
		margs = append(margs, q.MuseID)
	case !q.IncludeHidden:
		mWhere = append(mWhere, "m.hidden_at IS NULL")
	}
	if narrowing {
		mWhere = append(mWhere, "EXISTS (SELECT 1 FROM songs s WHERE s.muse_id = m.id AND "+where+")")
		margs = append(margs, wargs...)
	}
	base := strings.Join(mWhere, " AND ")
	if !unassignedOnly {
		pageWhere, pargs := base, append([]any(nil), margs...)
		if q.Cursor != "" {
			cur, err := decode[museCursor](q.Cursor, "cursor")
			if err != nil {
				return Board{}, err
			}
			pageWhere += " AND (m.title_fold > ? OR (m.title_fold = ? AND m.id > ?))"
			pargs = append(pargs, cur.Fold, cur.Fold, cur.ID)
		}
		pargs = append(pargs, limit+1)
		museQ := "" + //nolint:gosec // G202: pageWhere is built from fixed fragments with ? placeholders
			`SELECT m.id, m.kind, m.title, m.artist, m.title_fold FROM muses m WHERE ` + pageWhere + ` ORDER BY m.title_fold, m.id LIMIT ?`
		mr, err := tx.QueryContext(ctx, museQ, pargs...)
		if err != nil {
			return Board{}, fmt.Errorf("board: muses: %w", err)
		}
		for mr.Next() {
			var r museRow
			var artist sql.NullString
			if err := mr.Scan(&r.ref.ID, &r.ref.Kind, &r.ref.Title, &artist, &r.fold); err != nil {
				_ = mr.Close()
				return Board{}, err
			}
			if artist.Valid {
				a := artist.String
				r.ref.Artist = &a
			}
			if len(rows) == limit {
				more = true
				break
			}
			rows = append(rows, r)
		}
		if err := mr.Close(); err != nil {
			return Board{}, err
		}
	}
	// (2) the number of Muse rows over the whole filter
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM muses m WHERE `+base, margs...).Scan(&b.Totals.Muses); err != nil {
		return Board{}, fmt.Errorf("board: muse count: %w", err)
	}
	// (3) one grouped aggregate over the whole filter
	aggQ := "" + //nolint:gosec // G202: fragments are fixed strings built in this function
		`SELECT s.muse_id, s.angle, ` + store.SongStateCase + `, COUNT(*), COALESCE(SUM(st.queued_jobs), 0)
		FROM songs s JOIN (` + store.SongStateSQL + `) st ON st.song_id = s.id WHERE ` + where + ` GROUP BY s.muse_id, s.angle, 3`
	ag, err := tx.QueryContext(ctx, aggQ, wargs...)
	if err != nil {
		return Board{}, fmt.Errorf("board: aggregate: %w", err)
	}
	var groups []group
	for ag.Next() {
		var g group
		if err := ag.Scan(&g.museID, &g.angle, &g.state, &g.songs, &g.queuedJob); err != nil {
			_ = ag.Close()
			return Board{}, err
		}
		groups = append(groups, g)
	}
	if err := ag.Close(); err != nil {
		return Board{}, err
	}

	colCount := map[string]int{} // angle ("" = none) -> songs; keyed with a marker for none
	angleKey := func(n sql.NullString) string {
		if !n.Valid {
			return "\x00"
		}
		return n.String
	}
	rowCounts := map[string]*Counts{}
	rowCells := map[string]map[string]int{}
	rowKey := func(n sql.NullString) string {
		if !n.Valid {
			return "\x00"
		}
		return n.String
	}
	for _, g := range groups {
		colCount[angleKey(g.angle)] += g.songs
		b.Totals.Songs += g.songs
		b.Totals.Queued += g.queuedJob
		switch g.state {
		case "draft":
			b.Totals.Draft += g.songs
		case "rendered":
			b.Totals.Rendered += g.songs
		case "judged":
			b.Totals.Judged += g.songs
		case "picked":
			b.Totals.Picked += g.songs
		}
		k := rowKey(g.museID)
		c := rowCounts[k]
		if c == nil {
			c = &Counts{}
			rowCounts[k] = c
			rowCells[k] = map[string]int{}
		}
		c.Queued += g.queuedJob
		switch g.state {
		case "draft":
			c.Draft += g.songs
		case "rendered":
			c.Rendered += g.songs
		case "judged":
			c.Judged += g.songs
		case "picked":
			c.Picked += g.songs
		}
		rowCells[k][angleKey(g.angle)] += g.songs
	}
	// columns: distinct angles in the filter, by count desc then name, "(none)" last among equals
	for k, n := range colCount {
		c := Column{Songs: n}
		if k != "\x00" {
			a := k
			c.Angle = &a
		}
		b.Columns = append(b.Columns, c)
	}
	sort.Slice(b.Columns, func(i, j int) bool {
		x, y := b.Columns[i], b.Columns[j]
		if x.Songs != y.Songs {
			return x.Songs > y.Songs
		}
		if (x.Angle == nil) != (y.Angle == nil) {
			return y.Angle == nil
		}
		if x.Angle == nil {
			return false
		}
		return *x.Angle < *y.Angle
	})
	colOrder := map[string]int{}
	for i, c := range b.Columns {
		if c.Angle == nil {
			colOrder["\x00"] = i
		} else {
			colOrder[*c.Angle] = i
		}
	}

	// the Unassigned row exists when the filter matches Songs of no Muse and the caller is not looking at one Muse
	showUnassigned := q.MuseID == "" && rowCounts["\x00"] != nil && (unassignedOnly || !more)

	// (4) the chips of the page's rows and the first Unassigned chips (detail=chips)
	chipsBy := map[string]map[string][]Chip{} // muse key -> angle key -> chips
	var unassignedMore bool
	var lastUnassigned *chipCursor
	if chips && (len(rows) > 0 || showUnassigned) {
		cWhere := where
		var cargs []any
		cargs = append(cargs, wargs...)
		var conds []string
		if len(rows) > 0 {
			ph := strings.TrimSuffix(strings.Repeat("?,", len(rows)), ",")
			conds = append(conds, "s.muse_id IN ("+ph+")")
			for _, r := range rows {
				cargs = append(cargs, r.ref.ID)
			}
		}
		if showUnassigned {
			u := "s.muse_id IS NULL"
			if q.UnassignedAfter != "" {
				cur, err := decode[chipCursor](q.UnassignedAfter, "unassigned_after")
				if err != nil {
					return Board{}, err
				}
				u += " AND (s.created_at > ? OR (s.created_at = ? AND s.id > ?))"
				cargs = append(cargs, cur.At, cur.At, cur.ID)
			}
			conds = append(conds, "("+u+")")
		}
		cWhere += " AND (" + strings.Join(conds, " OR ") + ")"
		chipQ := "" + //nolint:gosec // G202: fragments are fixed strings built in this function
			`SELECT s.id, s.muse_id, s.angle, s.title, ` + store.SongStateCase + `, st.takes, p.source, p.take_id, st.queued_jobs, s.draft_id, s.created_at,
			EXISTS (SELECT 1 FROM song_sounds ss WHERE ss.song_id = s.id AND ss.facet = 'tempo_band' AND json_extract(ss.detail_json, '$.mismatch') IN (1, 'true'))
			FROM songs s JOIN (` + store.SongStateSQL + `) st ON st.song_id = s.id LEFT JOIN song_picks p ON p.song_id = s.id
			WHERE ` + cWhere + ` ORDER BY s.muse_id IS NULL, s.created_at, s.id`
		cr, err := tx.QueryContext(ctx, chipQ, cargs...)
		if err != nil {
			return Board{}, fmt.Errorf("board: chips: %w", err)
		}
		nUn := 0
		for cr.Next() {
			var c Chip
			var museID, angle, pickSource, pickTake, draft sql.NullString
			var queuedJobs int
			var created string
			if err := cr.Scan(&c.ID, &museID, &angle, &c.Title, &c.State, &c.Takes, &pickSource, &pickTake, &queuedJobs, &draft, &created, &c.TempoMismatch); err != nil {
				_ = cr.Close()
				return Board{}, err
			}
			if !museID.Valid {
				if nUn == UnassignedChips {
					unassignedMore = true
					break
				}
				nUn++
				lastUnassigned = &chipCursor{created, c.ID}
			}
			c.Queued = queuedJobs > 0
			if pickTake.Valid && pickSource.Valid && pickSource.String != "none" {
				ps, pt := pickSource.String, pickTake.String
				c.PickSource, c.PickTakeID = &ps, &pt
			}
			if c.State == "draft" && draft.Valid {
				d := draft.String
				c.DraftID = &d
			}
			rk, ak := rowKey(museID), angleKey(angle)
			if chipsBy[rk] == nil {
				chipsBy[rk] = map[string][]Chip{}
			}
			chipsBy[rk][ak] = append(chipsBy[rk][ak], c)
		}
		if err := cr.Close(); err != nil {
			return Board{}, err
		}
	}

	// (5) the taxonomy version
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM sound_taxonomy_versions`).Scan(&b.TaxonomyVersion); err != nil {
		return Board{}, fmt.Errorf("board: taxonomy: %w", err)
	}

	build := func(ref *muses.MuseRef, key string) Row {
		r := Row{Muse: ref, Cells: []Cell{}}
		if c := rowCounts[key]; c != nil {
			r.Counts = *c
			r.SongsTotal = c.Draft + c.Rendered + c.Judged + c.Picked
		}
		angles := make([]string, 0, len(rowCells[key]))
		for a := range rowCells[key] {
			angles = append(angles, a)
		}
		sort.Slice(angles, func(i, j int) bool { return colOrder[angles[i]] < colOrder[angles[j]] })
		for _, a := range angles {
			cell := Cell{Chips: []Chip{}}
			if a != "\x00" {
				v := a
				cell.Angle = &v
			}
			if cs := chipsBy[key][a]; len(cs) > 0 {
				cell.Chips = cs
			}
			if chips && key == "\x00" && len(cell.Chips) == 0 {
				continue // beyond the Unassigned cap or the cursor: the counts above stay complete
			}
			r.Cells = append(r.Cells, cell)
		}
		return r
	}

	size := boardEnvelope
	if bb, err := json.Marshal(b.Columns); err == nil {
		size += len(bb)
	}
	var last *museRow
	cut := false
	for i := range rows {
		r := build(&rows[i].ref, rows[i].ref.ID)
		rb, _ := json.Marshal(r)
		if len(b.Rows) > 0 && size+len(rb)+1 > MaxPageBytes {
			cut = true
			break
		}
		size += len(rb) + 1
		b.Rows = append(b.Rows, r)
		last = &rows[i]
	}
	if cut || more {
		if last != nil {
			c := encode(museCursor{last.fold, last.ref.ID})
			b.NextCursor = &c
		}
		return b, nil
	}
	if showUnassigned {
		r := build(nil, "\x00")
		r.ChipsMore = chips && unassignedMore
		if r.ChipsMore && lastUnassigned != nil {
			c := encode(*lastUnassigned)
			r.NextUnassignedCursor = &c
		}
		rb, _ := json.Marshal(r)
		if len(b.Rows) == 0 || size+len(rb)+1 <= MaxPageBytes {
			b.Rows = append(b.Rows, r)
		} else if last != nil {
			// no room: the next page starts after the last emitted Muse row and holds the Unassigned row first
			c := encode(museCursor{last.fold, last.ref.ID})
			b.NextCursor = &c
		}
	}
	return b, nil
}
