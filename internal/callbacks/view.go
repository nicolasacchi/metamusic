package callbacks

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/anchor"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Wire shapes of C255.2. The api package maps them one to one onto its frozen types; the json tags match.

// Child is the take a callback asks about.
type Child struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Seed  int64  `json:"seed"`
}

// CommentView is the comment a callback is about.
type CommentView struct {
	ID       string                `json:"id"`
	TrackID  string                `json:"track_id"`
	Kind     string                `json:"kind"`
	State    string                `json:"state"`
	Body     string                `json:"body"`
	AtSecond *float64              `json:"at_second"`
	Section  *judgments.SectionRef `json:"section"`
	Voice    *judgments.VoiceRef   `json:"voice,omitempty"`
}

// Spot is the same musical spot on the child and on the comment's take, computed on read (D-M5-7).
type Spot struct {
	ChildAtS   float64  `json:"child_at_s"`
	SourceAtS  *float64 `json:"source_at_s"`
	Basis      string   `json:"basis"`
	Confidence float64  `json:"confidence"`
	Label      *string  `json:"label"`
}

// Links are relative SPA links.
type Links struct {
	Child  string  `json:"child"`
	Source *string `json:"source"`
}

// Verdict is one stored answer.
type Verdict struct {
	ID                  string   `json:"id"`
	Source              string   `json:"source"`
	PrincipalKind       string   `json:"principal_kind"`
	Verdict             string   `json:"verdict"`
	Note                *string  `json:"note"`
	HeardAtS            *float64 `json:"heard_at_s"`
	MapBasis            *string  `json:"map_basis"`
	StateBefore         *string  `json:"state_before"`
	StateAfter          *string  `json:"state_after"`
	RegressionCommentID *string  `json:"regression_comment_id"`
	CreatedAt           string   `json:"created_at"`
}

// Callback is the view of one callback for one caller.
type Callback struct {
	ID             string      `json:"id"`
	Kind           string      `json:"kind"`
	Child          Child       `json:"child"`
	PatchID        string      `json:"patch_id"`
	JobID          string      `json:"job_id"`
	Comment        CommentView `json:"comment"`
	Relation       string      `json:"relation"`
	Spot           Spot        `json:"spot"`
	Links          Links       `json:"links"`
	State          string      `json:"state"`
	MyVerdict      *Verdict    `json:"my_verdict"`
	HumanVerdict   *Verdict    `json:"human_verdict"`
	CriticVerdicts []Verdict   `json:"critic_verdicts"`
	CreatedAt      string      `json:"created_at"`
}

// Filter selects and pages List.
type Filter struct {
	State, Kind  []string
	ChildTrackID string
	TrackID      string // callbacks whose comment sits on this take
	CommentID    string
	Limit        int
	Cursor       string
}

// Page is a page of callbacks: pending first (oldest first), then answered (newest first).
type Page struct {
	Items      []Callback `json:"items"`
	NextCursor *string    `json:"next_cursor"`
	Total      int        `json:"total"`
}

const (
	defaultLimit = 50
	maxLimit     = 100
)

func invalid(field, msg string) error {
	return problem.E(422, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

func notFound(id string) error {
	return problem.E(404, "NOT_FOUND", "callback not found", map[string]any{"callback_id": id})
}

// sourceOf maps a principal to its verdict source: the operator and paired devices are the human, agents are critics (D-M5-5).
func sourceOf(p auth.Principal) (string, error) {
	switch {
	case p.ActsAsHuman():
		return "human", nil
	case p.Kind == auth.KindAgent:
		return "critic", nil
	}
	return "", problem.E(403, "FORBIDDEN", "this principal cannot answer a callback", nil)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// row is one callbacks row joined with what the view needs.
type row struct {
	id, childID, commentID, sourceTrack, kind, patchID, jobID, createdAt string
	title                                                                string
	seed                                                                 int64
	baseTrack                                                            string
	cTrack, cKind, cState, cBody                                         string
	cAt                                                                  sql.NullFloat64
	cLabel, cSrc                                                         sql.NullString
	cConf                                                                sql.NullFloat64
}

const rowCols = `c.id, c.child_track_id, c.comment_id, c.source_track_id, c.kind, c.patch_id, c.job_id, c.created_at,
	t.title, r.seed, p.base_track_id,
	cm.track_id, cm.kind, cm.state, cm.body, cm.at_second, cm.section_label, cm.section_source, cm.section_confidence`

const rowFrom = ` FROM callbacks c JOIN tracks t ON t.id = c.child_track_id JOIN recipes r ON r.id = t.recipe_id
	JOIN patches p ON p.id = c.patch_id JOIN comments cm ON cm.id = c.comment_id`

func scanRow(sc interface{ Scan(...any) error }) (row, error) {
	var x row
	err := sc.Scan(&x.id, &x.childID, &x.commentID, &x.sourceTrack, &x.kind, &x.patchID, &x.jobID, &x.createdAt,
		&x.title, &x.seed, &x.baseTrack,
		&x.cTrack, &x.cKind, &x.cState, &x.cBody, &x.cAt, &x.cLabel, &x.cSrc, &x.cConf)
	return x, err
}

// spotOf computes the spot of a comment's time on the child (D-M5-7): the anchor of the comment's time in the comment's take, mapped
// onto the child by section kind, ordinal and fraction, else proportionally. A comment without a time has no spot (child time 0,
// proportional, confidence 0).
func spotOf(ctx context.Context, q querier, x row) (Spot, error) {
	if !x.cAt.Valid {
		return Spot{Basis: anchor.BasisProportional}, nil
	}
	src, err := library.Timing(ctx, q, x.cTrack)
	if err != nil {
		return Spot{}, err
	}
	child, err := library.Timing(ctx, q, x.childID)
	if err != nil {
		return Spot{}, err
	}
	at := x.cAt.Float64
	pt := anchor.Compute(src, at)
	childAt, basis, conf := anchor.Map(pt, child)
	sp := Spot{ChildAtS: round1(childAt), SourceAtS: &at, Basis: basis, Confidence: conf}
	if basis == anchor.BasisSection {
		if l := anchor.Compute(child, childAt).Label; l != "" {
			sp.Label = &l
		}
	}
	return sp, nil
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

func viewOf(x row, sp Spot) Callback {
	cv := CommentView{ID: x.commentID, TrackID: x.cTrack, Kind: x.cKind, State: x.cState, Body: x.cBody}
	if x.cAt.Valid {
		v := x.cAt.Float64
		cv.AtSecond = &v
	}
	if x.cLabel.Valid && x.cSrc.Valid {
		cv.Section = &judgments.SectionRef{Label: x.cLabel.String, Source: x.cSrc.String, Confidence: x.cConf.Float64}
	}
	rel := "ancestor"
	if x.cTrack == x.baseTrack {
		rel = "self"
	}
	links := Links{Child: fmt.Sprintf("/tracks/%s?t=%.1f", x.childID, sp.ChildAtS)}
	if sp.SourceAtS != nil {
		l := fmt.Sprintf("/tracks/%s?t=%.1f", x.cTrack, *sp.SourceAtS)
		links.Source = &l
	}
	return Callback{ID: x.id, Kind: x.kind, Child: Child{ID: x.childID, Title: x.title, Seed: x.seed}, PatchID: x.patchID, JobID: x.jobID,
		Comment: cv, Relation: rel, Spot: sp, Links: links, State: "pending", CriticVerdicts: []Verdict{}, CreatedAt: x.createdAt}
}

type verdictRow struct {
	Verdict
	principalID string
}

func loadVerdicts(ctx context.Context, q querier, ids []string) (map[string][]verdictRow, error) {
	out := map[string][]verdictRow{}
	if len(ids) == 0 {
		return out, nil
	}
	b, _ := json.Marshal(ids)
	rows, err := q.QueryContext(ctx, `SELECT callback_id, id, source, principal_kind, principal_id, verdict, note, heard_at_s, map_basis, state_before, state_after,
		regression_comment_id, created_at FROM callback_verdicts WHERE callback_id IN (SELECT value FROM json_each(?)) ORDER BY created_at, id`, string(b))
	if err != nil {
		return nil, fmt.Errorf("callbacks: verdicts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cb string
		var v verdictRow
		var note, mb, sb, sa, rc sql.NullString
		var heard sql.NullFloat64
		if err := rows.Scan(&cb, &v.ID, &v.Source, &v.PrincipalKind, &v.principalID, &v.Verdict.Verdict, &note, &heard, &mb, &sb, &sa, &rc, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Note, v.MapBasis, v.StateBefore, v.StateAfter, v.RegressionCommentID = strp(note), strp(mb), strp(sb), strp(sa), strp(rc)
		if heard.Valid {
			h := heard.Float64
			v.HeardAtS = &h
		}
		out[cb] = append(out[cb], v)
	}
	return out, rows.Err()
}

func strp(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

// apply fills state and the verdicts the caller may see (D-M5-5): the human verdict is the operator's decision and visible to
// everyone (it is what moves the comment); critic verdicts are visible only after the caller's own source has answered, so a human
// never sees them before answering and an agent only after its own.
func apply(cb *Callback, vs []verdictRow, p auth.Principal, src string) {
	var mine *Verdict
	for i := range vs {
		v := vs[i].Verdict
		switch {
		case vs[i].Source == "human":
			h := v
			cb.HumanVerdict = &h
			cb.State = "answered"
			if src == "human" {
				mine = &h
			}
		case src == "critic" && vs[i].principalID == p.Key():
			m := v
			mine = &m
		}
	}
	if mine != nil {
		m := *mine
		cb.MyVerdict = &m
		for i := range vs {
			if vs[i].Source == "critic" {
				cb.CriticVerdicts = append(cb.CriticVerdicts, vs[i].Verdict)
			}
		}
	}
}

// Get returns one callback as the caller may see it.
func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Callback, error) {
	if err := s.unconfigured(); err != nil {
		return Callback{}, err
	}
	src, err := sourceOf(p)
	if err != nil {
		return Callback{}, err
	}
	x, err := scanRow(s.o.Store.R.QueryRowContext(ctx, `SELECT `+rowCols+rowFrom+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Callback{}, notFound(id)
	}
	if err != nil {
		return Callback{}, fmt.Errorf("callbacks: get: %w", err)
	}
	return s.build(ctx, s.o.Store.R, p, src, []row{x})[0].cb, nil
}

type built struct {
	cb  Callback
	err error
}

func (s *Service) build(ctx context.Context, q querier, p auth.Principal, src string, xs []row) []built {
	ids := make([]string, len(xs))
	for i, x := range xs {
		ids[i] = x.id
	}
	vs, err := loadVerdicts(ctx, q, ids)
	out := make([]built, len(xs))
	for i, x := range xs {
		if err != nil {
			out[i].err = err
			continue
		}
		sp, serr := spotOf(ctx, q, x)
		if serr != nil {
			out[i].err = serr
			continue
		}
		out[i].cb = viewOf(x, sp)
		apply(&out[i].cb, vs[x.id], p, src)
	}
	return out
}

func offsetCursor(n int) string {
	b, _ := json.Marshal(struct {
		O int `json:"o"`
	}{n})
	return base64.RawURLEncoding.EncodeToString(b)
}

func cursorOffset(c string) (int, error) {
	if c == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, invalid("cursor", "malformed")
	}
	var v struct {
		O int `json:"o"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.O < 0 {
		return 0, invalid("cursor", "malformed")
	}
	return v.O, nil
}

const humanAnswered = `EXISTS (SELECT 1 FROM callback_verdicts v WHERE v.callback_id = c.id AND v.source = 'human')`

// List answers list_callbacks: pending callbacks first (oldest first), then answered ones (newest first).
func (s *Service) List(ctx context.Context, p auth.Principal, f Filter) (Page, error) {
	if err := s.unconfigured(); err != nil {
		return Page{}, err
	}
	src, err := sourceOf(p)
	if err != nil {
		return Page{}, err
	}
	limit := f.Limit
	switch {
	case limit == 0:
		limit = defaultLimit
	case limit < 1 || limit > maxLimit:
		return Page{}, invalid("limit", "limit must be between 1 and 100")
	}
	off, err := cursorOffset(f.Cursor)
	if err != nil {
		return Page{}, err
	}
	for _, st := range f.State {
		if st != "pending" && st != "answered" {
			return Page{}, invalid("state", "state must be pending or answered")
		}
	}
	for _, k := range f.Kind {
		if k != "fix" && k != "keep" {
			return Page{}, invalid("kind", "kind must be fix or keep")
		}
	}
	where, args := ` WHERE 1 = 1`, []any{}
	hasP, hasA := slices.Contains(f.State, "pending"), slices.Contains(f.State, "answered")
	switch {
	case hasP && !hasA:
		where += ` AND NOT ` + humanAnswered
	case hasA && !hasP:
		where += ` AND ` + humanAnswered
	}
	if len(f.Kind) > 0 {
		where += ` AND c.kind IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.Kind)), ",") + `)`
		for _, k := range f.Kind {
			args = append(args, k)
		}
	}
	if f.ChildTrackID != "" {
		where, args = where+` AND c.child_track_id = ?`, append(args, f.ChildTrackID)
	}
	if f.TrackID != "" {
		where, args = where+` AND cm.track_id = ?`, append(args, f.TrackID)
	}
	if f.CommentID != "" {
		where, args = where+` AND c.comment_id = ?`, append(args, f.CommentID)
	}
	page := Page{Items: []Callback{}}
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*)`+rowFrom+where, args...).Scan(&page.Total); err != nil { //nolint:gosec // G202: fixed fragments
		return page, fmt.Errorf("callbacks: count: %w", err)
	}
	order := ` ORDER BY ` + humanAnswered + `, CASE WHEN ` + humanAnswered + ` THEN NULL ELSE c.created_at END ASC,
		CASE WHEN ` + humanAnswered + ` THEN c.created_at END DESC, c.id LIMIT ? OFFSET ?`
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT `+rowCols+rowFrom+where+order, append(slices.Clone(args), limit, off)...) //nolint:gosec // G202: fixed fragments
	if err != nil {
		return page, fmt.Errorf("callbacks: list: %w", err)
	}
	var xs []row
	for rows.Next() {
		x, err := scanRow(rows)
		if err != nil {
			_ = rows.Close()
			return page, err
		}
		xs = append(xs, x)
	}
	if err := rows.Close(); err != nil {
		return page, err
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	for _, b := range s.build(ctx, s.o.Store.R, p, src, xs) {
		if b.err != nil {
			return page, b.err
		}
		page.Items = append(page.Items, b.cb)
	}
	if next := off + len(page.Items); next < page.Total {
		c := offsetCursor(next)
		page.NextCursor = &c
	}
	return page, nil
}
