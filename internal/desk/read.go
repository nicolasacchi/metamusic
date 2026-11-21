package desk

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

// Include selects the optional parts of a Plan (C359.1).
type Include struct{ ABC, Bars, Display, Auditions, Renders bool }

// DefaultInclude is every part but the bar spans.
var DefaultInclude = Include{ABC: true, Display: true, Auditions: true, Renders: true}

// ParseInclude reads a comma list of abc, bars, display, auditions, renders; empty is DefaultInclude.
func ParseInclude(s string) (Include, error) {
	toks := splitCSV(s)
	if len(toks) == 0 {
		return DefaultInclude, nil
	}
	var inc Include
	for _, t := range toks {
		switch t {
		case "abc":
			inc.ABC = true
		case "bars":
			inc.Bars = true
		case "display":
			inc.Display = true
		case "auditions":
			inc.Auditions = true
		case "renders":
			inc.Renders = true
		default:
			return inc, invalid("include", "include takes abc, bars, display, auditions and renders")
		}
	}
	return inc, nil
}

func spansOf(t *abc.Tune) []BarSpan {
	out := []BarSpan{}
	for _, sp := range t.BarSpans() {
		bs := BarSpan{Voice: sp.Voice, Bar: sp.Bar, Start: sp.Start, End: sp.End}
		if sp.StartS != nil {
			v := round6(*sp.StartS)
			bs.StartS = &v
		}
		out = append(out, bs)
	}
	return out
}

// Get returns one plan. The caller's identity only decides how links are signed.
func (s *Service) Get(ctx context.Context, p auth.Principal, id string, inc Include) (Plan, error) {
	if err := s.wired(); err != nil {
		return Plan{}, err
	}
	r, err := load(ctx, s.o.Store.R, id)
	if err != nil {
		return Plan{}, err
	}
	src, err := s.readScore(r.scorePath)
	if err != nil {
		return Plan{}, err
	}
	tune, err := abc.Parse(src, abc.Options{Dialect: abc.Dialect(r.dialect)})
	if err != nil {
		return Plan{}, scoreInvalid(err)
	}
	pl := Plan{PlanSummary: r.PlanSummary, Sections: r.sections, Edit: r.edit, Children: []Child{}}
	pl.Score = PlanScore{SHA256: r.scoreSHA, Bytes: r.scoreBytes, Dialect: r.dialect, Aligned: r.aligned, ABCTokens: r.abcTokens, Voices: []PlanVoice{}}
	for _, v := range tune.Voices {
		pv := PlanVoice{ID: v.ID, Bars: len(v.Bars)}
		if v.Name != "" {
			n := v.Name
			pv.Name = &n
		}
		pl.Score.Voices = append(pl.Score.Voices, pv)
	}
	if inc.ABC {
		t := string(src)
		pl.ABC = &t
	}
	if inc.Bars {
		pl.BarSpans = spansOf(tune)
	}
	if inc.Display {
		if text, changed := abc.Normalise(tune); changed {
			if nt, err := abc.Parse(text, abc.Options{Dialect: abc.DialectStandard}); err == nil {
				pl.Display = &Display{ABC: string(text), BarSpans: spansOf(nt)}
			}
		}
	}
	if err := s.fillRelations(ctx, p, &pl, inc); err != nil {
		return Plan{}, err
	}
	return pl, nil
}

func (s *Service) fillRelations(ctx context.Context, p auth.Principal, pl *Plan, inc Include) error {
	db := s.o.Store.R
	rows, err := db.QueryContext(ctx, `SELECT id, created_at FROM desk_plans WHERE parent_plan_id = ? ORDER BY created_at, id`, pl.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c Child
		if err := rows.Scan(&c.PlanID, &c.CreatedAt); err != nil {
			_ = rows.Close()
			return err
		}
		pl.Children = append(pl.Children, c)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	vr, err := db.QueryContext(ctx, `SELECT rater, verdict, note, decided_by, decided_at FROM desk_plan_verdicts WHERE plan_id = ?`, pl.ID)
	if err != nil {
		return err
	}
	for vr.Next() {
		var rater, by, at string
		var v, note sql.NullString
		if err := vr.Scan(&rater, &v, &note, &by, &at); err != nil {
			_ = vr.Close()
			return err
		}
		if rater == "owner" {
			pl.Verdicts.Owner = &OwnerVerdict{Verdict: strp(v), Note: strp(note), DecidedAt: at}
		} else {
			pl.Verdicts.Critic = &CriticVerdict{Verdict: strp(v), Note: strp(note), By: refOf(by), DecidedAt: at}
		}
	}
	if err := closeRows(vr); err != nil {
		return err
	}
	if inc.Auditions {
		list := []AuditionRef{}
		ar, err := db.QueryContext(ctx, `SELECT id, soundfont, chords, seconds, mp3_sha256, created_at FROM desk_auditions WHERE plan_id = ? ORDER BY created_at, id`, pl.ID)
		if err != nil {
			return err
		}
		for ar.Next() {
			var a AuditionRef
			var sha string
			if err := ar.Scan(&a.ID, &a.Soundfont, &a.Chords, &a.Seconds, &sha, &a.CreatedAt); err != nil {
				_ = ar.Close()
				return err
			}
			a.URL = s.auditionURL(p, sha)
			list = append(list, a)
		}
		if err := closeRows(ar); err != nil {
			return err
		}
		pl.Auditions = &list
	}
	if inc.Renders {
		list := []RenderRef{}
		rr, err := db.QueryContext(ctx, `SELECT j.id, j.batch_id, j.state, r.seed, j.track_id FROM jobs j JOIN recipes r ON r.id = j.recipe_id
			WHERE j.operator = 'render_plan' AND json_extract(j.mutation_json, '$.params.plan_id') = ? ORDER BY j.created_at, j.id`, pl.ID)
		if err != nil {
			return err
		}
		for rr.Next() {
			var x RenderRef
			var tr sql.NullString
			if err := rr.Scan(&x.JobID, &x.BatchID, &x.State, &x.Seed, &tr); err != nil {
				_ = rr.Close()
				return err
			}
			x.TrackID = strp(tr)
			list = append(list, x)
		}
		if err := closeRows(rr); err != nil {
			return err
		}
		pl.Renders = &list
	}
	return nil
}

func closeRows(r *sql.Rows) error {
	if err := r.Close(); err != nil {
		return err
	}
	return r.Err()
}

// refOf turns a stored principal key ("kind:id") into a reference; the API layer masks it for the viewer.
func refOf(key string) auth.PrincipalRef {
	kind, id, ok := strings.Cut(key, ":")
	if !ok {
		return auth.PrincipalRef{Kind: "system", ID: key, Name: key}
	}
	return auth.PrincipalRef{Kind: kind, ID: key, Name: id}
}

// Filter narrows List (C358.6).
type Filter struct {
	JobID, BatchID, BaseTrackID string
	Source                      string // job | take | edit
	Verdict                     string // liked | rejected | none
	Rendered                    *bool
	Limit                       int
	Cursor                      string
}

// Page is one page of plans.
type Page struct {
	Items      []PlanSummary `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

// Limits of List.
const (
	DefaultLimit = 50
	MaxLimit     = 100
)

type cursor struct {
	C  string `json:"c"`
	S  int    `json:"s"`
	ID string `json:"i"`
}

// List pages plans, newest first, then by seed index. A person (Owner, Editor) filters by the Owner's verdict, an agent by the
// critic's.
func (s *Service) List(ctx context.Context, p auth.Principal, f Filter) (Page, error) {
	if err := s.wired(); err != nil {
		return Page{}, err
	}
	switch f.Source {
	case "", "job", "take", "edit":
	default:
		return Page{}, invalid("source", "source is job, take or edit")
	}
	switch f.Verdict {
	case "", "liked", "rejected", "none":
	default:
		return Page{}, invalid("verdict", "verdict is liked, rejected or none")
	}
	limit := f.Limit
	switch {
	case limit == 0:
		limit = DefaultLimit
	case limit < 1 || limit > MaxLimit:
		return Page{}, invalid("limit", "limit is 1 to 100")
	}
	var where []string
	var args []any
	add := func(cond string, a ...any) { where, args = append(where, cond), append(args, a...) }
	if f.JobID != "" {
		add(`p.job_id = ?`, f.JobID)
	}
	if f.BatchID != "" {
		add(`j.batch_id = ?`, f.BatchID)
	}
	if f.BaseTrackID != "" {
		add(`p.base_track_id = ?`, f.BaseTrackID)
	}
	if f.Source != "" {
		add(`p.source = ?`, f.Source)
	}
	if f.Verdict != "" {
		col := "ov.verdict"
		if !p.IsPerson() {
			col = "cv.verdict"
		}
		if f.Verdict == "none" {
			add(col + ` IS NULL`)
		} else {
			add(col+` = ?`, f.Verdict)
		}
	}
	if f.Rendered != nil {
		cond := `(SELECT COUNT(*) FROM jobs rj WHERE rj.operator = 'render_plan' AND json_extract(rj.mutation_json, '$.params.plan_id') = p.id AND rj.track_id IS NOT NULL)`
		if *f.Rendered {
			add(cond + ` > 0`)
		} else {
			add(cond + ` = 0`)
		}
	}
	if f.Cursor != "" {
		c, err := decodeCursor(f.Cursor)
		if err != nil {
			return Page{}, invalid("cursor", "the cursor is not valid")
		}
		add(`(p.created_at < ? OR (p.created_at = ? AND (COALESCE(p.seed_index, -1) > ? OR (COALESCE(p.seed_index, -1) = ? AND p.id > ?))))`, c.C, c.C, c.S, c.S, c.ID)
	}
	q := `SELECT ` + summaryCols + summaryFrom
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `) // #nosec G202 -- the conditions are fixed literals above; every value is a bound argument
	}
	q += ` ORDER BY p.created_at DESC, COALESCE(p.seed_index, -1), p.id LIMIT ?`
	rows, err := s.o.Store.R.QueryContext(ctx, q, append(args, limit+1)...)
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = rows.Close() }()
	page := Page{Items: []PlanSummary{}}
	for rows.Next() {
		sm, err := scanSummary(rows)
		if err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, sm)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		si := -1
		if last.SeedIndex != nil {
			si = *last.SeedIndex
		}
		b, _ := json.Marshal(cursor{C: last.CreatedAt, S: si, ID: last.ID})
		c := base64.RawURLEncoding.EncodeToString(b)
		page.NextCursor = &c
	}
	return page, nil
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.C == "" || c.ID == "" {
		return c, errors.New("incomplete cursor")
	}
	return c, nil
}

// CheckIn is a score to check (C359.1). Dialect is yue2 when empty.
type CheckIn struct {
	ABC     string
	BaseABC string
	Dialect string
}

// Check parses a score text and reports what the Desk shows about it; it reads and writes nothing.
func (s *Service) Check(_ context.Context, in CheckIn) (abc.Report, error) {
	d := abc.Dialect(in.Dialect)
	switch d {
	case "":
		d = abc.DialectYuE2
	case abc.DialectYuE2, abc.DialectStandard:
	default:
		return abc.Report{}, invalid("dialect", "dialect is yue2 or standard")
	}
	if in.ABC == "" {
		return abc.Report{}, invalid("abc", "abc is empty")
	}
	if len(in.ABC) > MaxScoreBytes || len(in.BaseABC) > MaxScoreBytes {
		return abc.Report{}, invalid("abc", "abc and base_abc are at most 64 KiB, the largest score a plan holds")
	}
	var base []byte
	if in.BaseABC != "" {
		base = []byte(in.BaseABC)
	}
	return abc.Check([]byte(in.ABC), base, abc.Options{Dialect: d}), nil
}
