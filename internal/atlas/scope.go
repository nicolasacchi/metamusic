package atlas

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/songs"
)

// feat is the feature part of a scope row: the song_features columns the Atlas reads. A Song without a song_features row has
// has == false and every value NULL.
type feat struct {
	has                                        bool
	bpm, req, meas, energy, keyNum             sql.NullInt64
	bpmBasis, energyBasis, keyLetter, keyBasis sql.NullString
	dur                                        sql.NullFloat64
	family, vocal, era, mood, language         sql.NullString
	digest                                     sql.NullString
}

// row is one Song of the scope: everything that decides membership, order, coordinates, flags or overlay colour.
type row struct {
	id, title, muse, created string
	h, st, pk, rec           int
	f                        feat
}

// The scope is read in separate passes and merged in Go: the Songs of the filter (the one shared by list_songs), then the
// features and the Picks of those Songs, then the state inputs. A join per Song costs a B-tree lookup per table and row, and
// store.SongStateSQL counts takes per Song with correlated subqueries over the take_songs view; at 5 000 Songs both cost more
// than the page budget. A large scope reads the two tables sequentially, a small one by key (smallScope).
// TestAtlas_StateEqualsSongState holds the state rule and store.SongStateSQL together.
const smallScope = 64

// featureColumns lists the song_features columns a request reads besides the digest: those of its two axes, of the colour
// overlay and, with near_song_id, of the sequencer inputs. Reading only these keeps a 5 000-row scan cheap.
func featureColumns(xa, ya AxisDef, colorBy string, near bool) []string {
	var cols []string
	add := func(c ...string) {
		for _, x := range c {
			if !slices.Contains(cols, x) {
				cols = append(cols, x)
			}
		}
	}
	axis := func(a AxisDef) {
		switch a.Name {
		case "tempo":
			add("bpm_x100", "bpm_basis")
		case "bpm_requested":
			add("bpm_requested_x100", "bpm_basis")
		case "bpm_measured":
			add("bpm_measured_x100")
		case "energy":
			add("energy_x10000", "energy_basis")
		case "duration":
			add("duration_s")
		case "key":
			add("key_num", "key_letter", "key_basis")
		default:
			add(a.Column)
		}
	}
	axis(xa)
	axis(ya)
	if a, ok := Axis(colorBy); ok {
		axis(a)
	}
	if near {
		add("bpm_x100", "bpm_basis", "energy_x10000", "energy_basis", "key_num", "key_letter", "key_basis")
	}
	return cols
}

// dest returns the scan target of a song_features column.
func (f *feat) dest(col string) any {
	switch col {
	case "bpm_x100":
		return &f.bpm
	case "bpm_basis":
		return &f.bpmBasis
	case "bpm_requested_x100":
		return &f.req
	case "bpm_measured_x100":
		return &f.meas
	case "energy_x10000":
		return &f.energy
	case "energy_basis":
		return &f.energyBasis
	case "key_num":
		return &f.keyNum
	case "key_letter":
		return &f.keyLetter
	case "key_basis":
		return &f.keyBasis
	case "duration_s":
		return &f.dur
	case "family":
		return &f.family
	case "vocal":
		return &f.vocal
	case "era":
		return &f.era
	case "mood":
		return &f.mood
	case "language":
		return &f.language
	}
	panic("atlas: unknown feature column " + col)
}

// attachFeatures sets row.f (the digest and cols) and row.pk of every row, by key for a small scope and by a sequential scan of
// song_features and song_picks otherwise.
func attachFeatures(ctx context.Context, q queryer, rows []row, cols []string) error {
	idx := make(map[string]int, len(rows))
	for i := range rows {
		idx[rows[i].id] = i
	}
	small := len(rows) <= smallScope
	var keys string
	if small {
		ids := make([]string, len(rows))
		for i := range rows {
			ids[i] = rows[i].id
		}
		b, _ := json.Marshal(ids) // a slice of strings always marshals
		keys = string(b)
	}
	sel := "song_id, digest"
	for _, c := range cols {
		sel += ", " + c
	}
	stmt, args := "SELECT "+sel+" FROM song_features", []any{}
	if small {
		stmt, args = stmt+" WHERE song_id IN (SELECT value FROM json_each(?))", []any{keys}
	}
	rs, err := q.QueryContext(ctx, stmt, args...)
	if err != nil {
		return fmt.Errorf("atlas: features: %w", err)
	}
	defer rs.Close()
	var id string
	var tmp feat
	dest := []any{&id, &tmp.digest}
	for _, c := range cols {
		dest = append(dest, tmp.dest(c))
	}
	for rs.Next() {
		if err := rs.Scan(dest...); err != nil {
			return fmt.Errorf("atlas: features: %w", err)
		}
		if i, ok := idx[id]; ok {
			rows[i].f = tmp
			rows[i].f.has = true
		}
	}
	if err := rs.Err(); err != nil {
		return fmt.Errorf("atlas: features: %w", err)
	}
	stmt, args = "SELECT song_id, source FROM song_picks", []any{}
	if small {
		stmt, args = stmt+" WHERE song_id IN (SELECT value FROM json_each(?))", []any{keys}
	}
	ps, err := q.QueryContext(ctx, stmt, args...)
	if err != nil {
		return fmt.Errorf("atlas: picks: %w", err)
	}
	defer ps.Close()
	for ps.Next() {
		var src string
		if err := ps.Scan(&id, &src); err != nil {
			return fmt.Errorf("atlas: picks: %w", err)
		}
		if i, ok := idx[id]; ok {
			rows[i].pk = slices.Index(pickNames, src)
		}
	}
	return ps.Err()
}

func scanRow(sc interface{ Scan(...any) error }) (row, error) {
	var r row
	var muse sql.NullString
	if err := sc.Scan(&r.id, &r.title, &muse, &r.h, &r.created); err != nil {
		return row{}, err
	}
	r.muse = muse.String
	return r, nil
}

// songState is what the state and the Record colour of a Song are made of.
type songState struct {
	takes    int
	nondraft bool
	rated    bool // a human star rating on some take
	dueled   bool // a human duel verdict on some take
	rec      int  // 1 a live slot in a draft or proposed Record, 2 in a published one
}

// loadState reads the state inputs of every Song, one pass per table (the grouped reads touch each table once, whatever the
// scope size).
func loadState(ctx context.Context, q queryer) (map[string]*songState, error) {
	out := map[string]*songState{}
	at := func(id string) *songState {
		if out[id] == nil {
			out[id] = &songState{}
		}
		return out[id]
	}
	each := func(what, stmt string, fn func(rs *sql.Rows) error) error {
		rs, err := q.QueryContext(ctx, stmt)
		if err != nil {
			return fmt.Errorf("atlas: state %s: %w", what, err)
		}
		defer rs.Close()
		for rs.Next() {
			if err := fn(rs); err != nil {
				return fmt.Errorf("atlas: state %s: %w", what, err)
			}
		}
		return rs.Err()
	}
	roots := map[string]string{} // root take -> its one Song (song_roots.root_track_id is the primary key; take_songs is tracks joined to it)
	if err := each("roots", `SELECT root_track_id, song_id FROM song_roots`, func(rs *sql.Rows) error {
		var root, song string
		err := rs.Scan(&root, &song)
		roots[root] = song
		return err
	}); err != nil {
		return nil, err
	}
	if err := each("takes", `SELECT root_track_id, COUNT(*), MAX(status <> 'draft') FROM tracks GROUP BY root_track_id`, func(rs *sql.Rows) error {
		var root string
		var n, nd int
		if err := rs.Scan(&root, &n, &nd); err != nil {
			return err
		}
		if song, ok := roots[root]; ok {
			st := at(song)
			st.takes += n
			st.nondraft = st.nondraft || nd == 1
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := each("ratings", `SELECT DISTINCT r.song_id FROM ratings g JOIN tracks t ON t.id = g.track_id JOIN song_roots r ON r.root_track_id = t.root_track_id
		WHERE g.source = 'human' AND g.stars IS NOT NULL`, func(rs *sql.Rows) error {
		var id string
		err := rs.Scan(&id)
		at(id).rated = true
		return err
	}); err != nil {
		return nil, err
	}
	if err := each("duels", `SELECT DISTINCT r.song_id FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id
		JOIN tracks t ON t.id IN (d.slot_a_track_id, d.slot_b_track_id) JOIN song_roots r ON r.root_track_id = t.root_track_id WHERE v.source = 'human'`, func(rs *sql.Rows) error {
		var id string
		err := rs.Scan(&id)
		at(id).dueled = true
		return err
	}); err != nil {
		return nil, err
	}
	if err := each("records", `SELECT rs.song_id, MAX(CASE rc.status WHEN 'published' THEN 2 ELSE 1 END) FROM record_slots rs JOIN records rc ON rc.id = rs.record_id
		WHERE rs.removed_at IS NULL AND rc.hidden_at IS NULL GROUP BY rs.song_id`, func(rs *sql.Rows) error {
		var id string
		var n int
		err := rs.Scan(&id, &n)
		at(id).rec = n
		return err
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// fill sets the state index (0 draft, 1 rendered, 2 judged, 3 picked, the C90.4 rule) and the Record colour of r.
func (r *row) fill(m map[string]*songState) {
	st := m[r.id]
	if st == nil {
		st = &songState{}
	}
	r.rec = st.rec
	switch {
	case r.pk == 3 || r.pk == 4:
		r.st = 3
	case st.rated || st.dueled || st.nondraft:
		r.st = 2
	case st.takes > 0:
		r.st = 1
	}
}

// scope reads the Songs of the Atlas scope in id order, the newest limit of them, with the number of Songs the scope holds
// before the cap. With dim the scope is the unfiltered visible set and h marks the Songs the filter selects; without it the
// scope is the filtered set and h is 1 throughout.
func scope(ctx context.Context, q queryer, f songs.ListFilter, dim bool, limit int, cols []string) (rows []row, total int, err error) {
	where, args := songs.FilterSQL(f, "")
	hExpr := "1"
	var hArgs []any
	if dim {
		base := songs.ListFilter{IncludeHidden: f.IncludeHidden, IncludeArchived: f.IncludeArchived}
		var hw string
		hw, hArgs = songs.FilterSQL(f, "")
		where, args = songs.FilterSQL(base, "")
		hExpr = "CASE WHEN s.id IN (SELECT s.id FROM songs s WHERE " + hw + ") THEN 1 ELSE 0 END"
	}
	// In id order (the primary key, no sort): the token and the pages want that order. The length of the result is the scope's
	// size, so no second pass over the filter counts it; only a scope past the cap is re-ordered by age below.
	stmt := "SELECT s.id, s.title, s.muse_id, " + hExpr + ", s.created_at FROM songs s WHERE " + where + " ORDER BY s.id" //nolint:gosec // G202: fragments are fixed strings built in this package
	rs, err := q.QueryContext(ctx, stmt, append(hArgs, args...)...)
	if err != nil {
		return nil, 0, fmt.Errorf("atlas: scope: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		r, err := scanRow(rs)
		if err != nil {
			return nil, 0, fmt.Errorf("atlas: scope: %w", err)
		}
		rows = append(rows, r)
	}
	if err := rs.Err(); err != nil {
		return nil, 0, fmt.Errorf("atlas: scope: %w", err)
	}
	total = len(rows)
	if total > limit {
		slices.SortFunc(rows, func(a, b row) int {
			if c := strings.Compare(b.created, a.created); c != 0 {
				return c
			}
			return strings.Compare(b.id, a.id)
		})
		rows = rows[:limit]
		slices.SortFunc(rows, func(a, b row) int { return strings.Compare(a.id, b.id) })
	}
	if err := attachFeatures(ctx, q, rows, cols); err != nil {
		return nil, 0, err
	}
	state, err := loadState(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		rows[i].fill(state)
	}
	return rows, total, nil
}

// one reads a single Song as a scope row (for near_song_id outside the scope); ok is false for an unknown Song.
func one(ctx context.Context, q queryer, id string, cols []string) (row, bool, error) {
	r, err := scanRow(q.QueryRowContext(ctx, "SELECT s.id, s.title, s.muse_id, 1, s.created_at FROM songs s WHERE s.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, fmt.Errorf("atlas: song: %w", err)
	}
	one := []row{r}
	if err := attachFeatures(ctx, q, one, cols); err != nil {
		return row{}, false, err
	}
	r = one[0]
	state, err := loadState(ctx, q)
	if err != nil {
		return row{}, false, err
	}
	r.fill(state)
	return r, true, nil
}

// pendingFor reports whether a dirty track or draft key exists for any Song in ids.
func pendingFor(ctx context.Context, q queryer, ids map[string]bool) (bool, error) {
	rs, err := q.QueryContext(ctx, `SELECT ts.song_id FROM provenance_dirty d JOIN take_songs ts ON d.kind = 'track' AND d.id = ts.track_id
		UNION ALL SELECT s.id FROM provenance_dirty d JOIN songs s ON d.kind = 'draft' AND d.id = s.draft_id`)
	if err != nil {
		return false, fmt.Errorf("atlas: pending: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var id string
		if err := rs.Scan(&id); err != nil {
			return false, fmt.Errorf("atlas: pending: %w", err)
		}
		if ids[id] {
			return true, nil
		}
	}
	return false, rs.Err()
}

func taxonomyVersion(ctx context.Context, q queryer) (int, error) {
	var v int
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM sound_taxonomy_versions`).Scan(&v); err != nil {
		return 0, fmt.Errorf("atlas: taxonomy version: %w", err)
	}
	return v, nil
}

// queryer is the read part of *sql.Tx.
type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Plot values -------------------------------------------------------------------------------------------------------

// basisLetter maps a feature basis to its wire code.
func basisLetter(b string) string {
	switch b {
	case "measured":
		return "m"
	case "declared":
		return "d"
	case "plan":
		return "l"
	case "predicted":
		return "p"
	}
	return ""
}

// numeric returns the value of a numeric axis in the axis' fixed-point units and its basis code; ok is false when the Song
// has no value (the point is not plotted).
func (r row) numeric(a AxisDef) (v int, basis string, ok bool) {
	f := r.f
	switch a.Name {
	case "tempo":
		return iv(f.bpm), basisLetter(f.bpmBasis.String), f.bpm.Valid
	case "bpm_requested":
		// The column carries no basis of its own: the request is the declared tempo unless the position tempo itself fell back to the plan.
		b := "d"
		if f.bpmBasis.String == "plan" {
			b = "l"
		}
		return iv(f.req), b, f.req.Valid
	case "bpm_measured":
		return iv(f.meas), "m", f.meas.Valid
	case "energy":
		return iv(f.energy), basisLetter(f.energyBasis.String), f.energy.Valid
	case "duration":
		return int(math.Round(f.dur.Float64 * 100)), "m", f.dur.Valid
	}
	return 0, "", false
}

// category returns the value of a categorical axis ("" when the Song has none) and its basis code.
func (r row) category(a AxisDef) (v, basis string) {
	f := r.f
	switch a.Name {
	case "family":
		return f.family.String, ""
	case "vocal":
		return f.vocal.String, ""
	case "era":
		return f.era.String, ""
	case "mood":
		return f.mood.String, ""
	case "language":
		return f.language.String, ""
	case "key":
		if f.keyNum.Valid {
			return fmt.Sprintf("%d%s", f.keyNum.Int64, f.keyLetter.String), basisLetter(f.keyBasis.String)
		}
	}
	return "", ""
}

func iv(n sql.NullInt64) int { return int(n.Int64) }
