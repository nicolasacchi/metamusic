package judgments

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

var (
	commentKinds  = []string{"note", "fix", "keep", "marker"}
	commentStates = []string{"open", "addressed", "resolved", "regressed"}
	commentSrcs   = []string{"web", "api", "mcp", "stdio", "subsonic"}
)

// maxPageBytes is the serialized cap of a list page (M1 C17).
const maxPageBytes = 90000

// Transition reports whether a comment may move between two states through
// update_comment (C31 table). Moves into addressed and regressed belong to M5.
// The principal never changes the result.
func Transition(from, to string) error {
	if !slices.Contains(commentStates, from) || !slices.Contains(commentStates, to) {
		return ErrInvalidTransition
	}
	switch {
	case from == to: // no-op
		return nil
	case to == "open": // reopen, from any state
		return nil
	case to == "resolved": // from open, addressed and regressed
		return nil
	}
	return ErrInvalidTransition
}

func cleanBody(body string) (string, error) {
	b := strings.TrimSpace(body)
	if strings.ContainsRune(b, 0) {
		return "", inv("body", "must not contain NUL")
	}
	if n := utf8.RuneCountInString(b); n < 1 || n > 4000 {
		return "", inv("body", "1 to 4000 characters")
	}
	return b, nil
}

// sectionFor computes the stored section label of a comment (D-M2a-16): timed
// sections only, manual > measured > plan, no fallback to a lower source or the
// nearest section.
func sectionFor(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, trackID string, at float64) (*SectionRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT source, label, start_s, end_s, confidence FROM sections
		WHERE track_id = ? AND source IN ('manual','measured','plan') AND start_s IS NOT NULL AND end_s IS NOT NULL ORDER BY idx`, trackID)
	if err != nil {
		return nil, fmt.Errorf("judgments: sections: %w", err)
	}
	defer rows.Close()
	type sec struct {
		label      string
		start, end float64
		conf       sql.NullFloat64
	}
	by := map[string][]sec{}
	for rows.Next() {
		var src string
		var x sec
		if err := rows.Scan(&src, &x.label, &x.start, &x.end, &x.conf); err != nil {
			return nil, err
		}
		by[src] = append(by[src], x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, src := range []string{"manual", "measured", "plan"} {
		secs := by[src]
		if len(secs) == 0 {
			continue
		}
		last := 0
		for i, x := range secs {
			if x.end > secs[last].end {
				last = i
			}
		}
		for i, x := range secs {
			if at >= x.start && (at < x.end || (i == last && at == x.end)) {
				conf := 0.0
				switch {
				case src == "manual":
					conf = 1.0
				case x.conf.Valid:
					conf = x.conf.Float64
				}
				return &SectionRef{Label: x.label, Source: src, Confidence: conf}, nil
			}
		}
		return nil, nil // the highest source present decides, even when it leaves a gap
	}
	return nil, nil
}

// AddComment creates a comment; src is the transport label (web|api|mcp|stdio).
func (s *Service) AddComment(ctx context.Context, p auth.Principal, src string, trackID string, in NewComment) (Comment, error) {
	if !slices.Contains(commentSrcs, src) {
		return Comment{}, inv("source", "unknown comment source %q", src)
	}
	body, err := cleanBody(in.Body)
	if err != nil {
		return Comment{}, err
	}
	kind := in.Kind
	if kind == "" {
		kind = "note"
	}
	if !slices.Contains(commentKinds, kind) {
		return Comment{}, inv("kind", "must be note, fix, keep or marker")
	}
	var dur float64
	switch err := s.st.R.QueryRowContext(ctx, `SELECT duration_s FROM tracks WHERE id = ?`, trackID).Scan(&dur); {
	case errors.Is(err, sql.ErrNoRows):
		return Comment{}, library.ErrNotFound
	case err != nil:
		return Comment{}, fmt.Errorf("judgments: track lookup: %w", err)
	}
	var at any
	var sec *SectionRef
	if in.AtSecond != nil {
		t := *in.AtSecond
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 {
			return Comment{}, inv("at_second", "must be 0 or more")
		}
		if t > dur {
			return Comment{}, &Error{Status: http.StatusUnprocessableEntity, Code: "AT_SECOND_OUT_OF_RANGE", Field: "at_second",
				Msg: fmt.Sprintf("at_second %.1f is after the end of the track (%.1f s)", t, dur)}
		}
		t = math.Round(t*10) / 10
		at = t
		if sec, err = sectionFor(ctx, s.st.R, trackID, t); err != nil {
			return Comment{}, err
		}
	}
	id := s.newID()
	now := s.stamp()
	var label, lsrc, lconf any
	if sec != nil {
		label, lsrc, lconf = sec.Label, sec.Source, sec.Confidence
	}
	if _, err := s.st.W.ExecContext(ctx, `INSERT INTO comments(id, track_id, body, at_second, section_label, section_source, section_confidence, kind, state, source,
		principal_kind, principal_id, principal_name, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,'open',?,?,?,?,?,?)`,
		id, trackID, body, at, label, lsrc, lconf, kind, src, string(p.Kind), p.Key(), p.Name, now, now); err != nil {
		return Comment{}, fmt.Errorf("judgments: add comment: %w", err)
	}
	audit.NoteCreated(ctx, id)
	return s.comment(ctx, id)
}

const commentCols = `id, track_id, body, at_second, section_label, section_source, section_confidence, kind, state, source,
	principal_kind, principal_id, principal_name, created_at, updated_at, edited_at, state_changed_at, hidden_at`

type rowScanner interface{ Scan(...any) error }

func scanComment(r rowScanner) (Comment, error) {
	var (
		c                           Comment
		at, conf                    sql.NullFloat64
		label, lsrc                 sql.NullString
		edited, stateChanged, hidAt sql.NullString
	)
	if err := r.Scan(&c.ID, &c.TrackID, &c.Body, &at, &label, &lsrc, &conf, &c.Kind, &c.State, &c.Source,
		&c.Author.Kind, &c.Author.ID, &c.Author.Name, &c.CreatedAt, &c.UpdatedAt, &edited, &stateChanged, &hidAt); err != nil {
		return c, err
	}
	if at.Valid {
		v := at.Float64
		c.AtSecond = &v
		c.Link = fmt.Sprintf("/tracks/%s?t=%.1f", c.TrackID, v)
	}
	if label.Valid {
		c.Section = &SectionRef{Label: label.String, Source: lsrc.String, Confidence: conf.Float64}
	}
	if edited.Valid {
		c.EditedAt = &edited.String
	}
	if stateChanged.Valid {
		c.StateChangedAt = &stateChanged.String
	}
	c.Hidden = hidAt.Valid
	return c, nil
}

func (s *Service) comment(ctx context.Context, id string) (Comment, error) {
	c, err := scanComment(s.st.R.QueryRowContext(ctx, `SELECT `+commentCols+` FROM comments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, library.ErrNotFound
	}
	if err != nil {
		return c, fmt.Errorf("judgments: comment: %w", err)
	}
	return c, nil
}

func notAuthor() error {
	return &Error{Status: http.StatusForbidden, Code: "NOT_AUTHOR", Msg: "only the author may change this comment"}
}

// UpdateComment edits, hides or transitions a comment. Editing the body or
// kind: an operator-side principal (human, device) on human and device
// comments, an agent on its own. Hiding: an operator-side principal on any
// comment, an agent on its own. State changes: any write principal.
func (s *Service) UpdateComment(ctx context.Context, p auth.Principal, id string, in CommentPatch) (Comment, error) {
	if in.Body == nil && in.Kind == nil && in.State == nil && in.Hidden == nil {
		return Comment{}, inv("body", "give a body, kind, state and/or hidden")
	}
	var body string
	if in.Body != nil {
		var err error
		if body, err = cleanBody(*in.Body); err != nil {
			return Comment{}, err
		}
	}
	if in.Kind != nil && !slices.Contains(commentKinds, *in.Kind) {
		return Comment{}, inv("kind", "must be note, fix, keep or marker")
	}
	if in.State != nil && !slices.Contains(commentStates, *in.State) {
		return Comment{}, inv("state", "must be open, addressed, resolved or regressed")
	}
	cur, err := s.comment(ctx, id)
	if err != nil {
		return Comment{}, err
	}
	own := cur.Author.ID == p.Key()
	authorIsOperator := cur.Author.Kind == string(auth.KindHuman) || cur.Author.Kind == string(auth.KindDevice)
	if (in.Body != nil || in.Kind != nil) && !(own || (p.ActsAsHuman() && authorIsOperator)) {
		return Comment{}, notAuthor()
	}
	if in.Hidden != nil && !(own || p.ActsAsHuman()) {
		return Comment{}, notAuthor()
	}
	if in.State != nil {
		if err := Transition(cur.State, *in.State); err != nil {
			return Comment{}, &Error{Status: http.StatusConflict, Code: "INVALID_TRANSITION", cause: err,
				Msg: fmt.Sprintf("a comment cannot move from %s to %s here", cur.State, *in.State), Field: "state"}
		}
	}
	now := s.stamp()
	set, args := []string{}, []any{}
	if in.Body != nil && body != cur.Body {
		set, args = append(set, "body = ?", "edited_at = ?"), append(args, body, now)
	}
	if in.Kind != nil && *in.Kind != cur.Kind {
		set, args = append(set, "kind = ?"), append(args, *in.Kind)
		if in.Body == nil || body == cur.Body {
			set, args = append(set, "edited_at = ?"), append(args, now)
		}
	}
	if in.State != nil && *in.State != cur.State {
		set, args = append(set, "state = ?", "state_changed_at = ?", "state_changed_by = ?"), append(args, *in.State, now, p.Key())
	}
	if in.Hidden != nil && *in.Hidden != cur.Hidden {
		if *in.Hidden {
			set, args = append(set, "hidden_at = ?", "hidden_by = ?"), append(args, now, p.Key())
		} else {
			set = append(set, "hidden_at = NULL", "hidden_by = NULL")
		}
	}
	if len(set) == 0 {
		return cur, nil
	}
	set, args = append(set, "updated_at = ?"), append(args, now)
	if _, err := s.st.W.ExecContext(ctx, `UPDATE comments SET `+strings.Join(set, ", ")+` WHERE id = ?`, append(args, id)...); err != nil { //nolint:gosec // G202: fixed column list
		return Comment{}, fmt.Errorf("judgments: update comment: %w", err)
	}
	return s.comment(ctx, id)
}

func cursorOffset(c string) (int, error) {
	if c == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, inv("cursor", "malformed")
	}
	var v struct {
		O int `json:"o"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.O < 0 {
		return 0, inv("cursor", "malformed")
	}
	return v.O, nil
}

func offsetCursor(n int) string {
	b, _ := json.Marshal(struct {
		O int `json:"o"`
	}{n})
	return base64.RawURLEncoding.EncodeToString(b)
}

// Comments lists a track's comments, capped at 90 000 serialized bytes per page.
func (s *Service) Comments(ctx context.Context, trackID string, f CommentFilter) (CommentPage, error) {
	off, err := cursorOffset(f.Cursor)
	if err != nil {
		return CommentPage{}, err
	}
	for _, st := range f.State {
		if !slices.Contains(commentStates, st) {
			return CommentPage{}, inv("state", "unknown state %q", st)
		}
	}
	for _, k := range f.Kind {
		if !slices.Contains(commentKinds, k) {
			return CommentPage{}, inv("kind", "unknown kind %q", k)
		}
	}
	order := `created_at DESC, id DESC`
	switch f.Order {
	case "", "newest":
	case "time":
		order = `(at_second IS NULL) ASC, at_second ASC, created_at ASC, id ASC`
	default:
		return CommentPage{}, inv("order", "must be newest or time")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 200)
	if err := s.requireTrack(ctx, trackID); err != nil {
		return CommentPage{}, err
	}
	where, args := ` WHERE track_id = ?`, []any{trackID}
	if !f.IncludeHidden {
		where += ` AND hidden_at IS NULL`
	}
	if len(f.State) > 0 {
		where += ` AND state IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.State)), ",") + `)`
		for _, x := range f.State {
			args = append(args, x)
		}
	}
	if len(f.Kind) > 0 {
		where += ` AND kind IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.Kind)), ",") + `)`
		for _, x := range f.Kind {
			args = append(args, x)
		}
	}
	page := CommentPage{Items: []Comment{}}
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments`+where, args...).Scan(&page.Total); err != nil {
		return page, fmt.Errorf("judgments: count comments: %w", err)
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT `+commentCols+` FROM comments`+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(slices.Clone(args), limit, off)...) //nolint:gosec // G202: fixed fragments
	if err != nil {
		return page, fmt.Errorf("judgments: list comments: %w", err)
	}
	defer rows.Close()
	env, _ := json.Marshal(page)
	size := len(env) + 128 // envelope plus the cursor
	more := false
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return page, err
		}
		b, _ := json.Marshal(c)
		if len(page.Items) > 0 && size+len(b)+1 > maxPageBytes {
			more = true
			break
		}
		size += len(b) + 1
		page.Items = append(page.Items, c)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if next := off + len(page.Items); more || next < page.Total {
		c := offsetCursor(next)
		page.NextCursor = &c
	}
	return page, nil
}
