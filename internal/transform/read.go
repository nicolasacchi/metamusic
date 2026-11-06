package transform

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// View is a transform as create, get, list and wait return it (C212.4). Times are the store's fixed-width UTC strings (RFC 3339
// with milliseconds). The principal is the row's: after a reuse (D-T1-2) it is the principal that first created the spec.
type View struct {
	ID                string // equals the id of its Take
	Kind              Kind
	State             string
	Phase             *string
	ProgressPermille  int
	Title             string
	SpecHash          string
	Reused, Restored  bool
	PrincipalID       string // Principal.Key()
	PrincipalKind     string // human | agent
	PrincipalName     string
	Rationale         *string
	Inputs            []InputView
	ExpectedMS, MaxMS int64
	UsedMS            *int64 // measured CPU, set when finished
	OutputSecondsEst  float64
	Warnings          []Warning // render-time; empty until done
	Error             *ErrorView
	CancelRequestedAt *string
	CreatedAt         string
	StartedAt         *string
	FinishedAt        *string
}

// InputView is an input of a transform as the view lists it.
type InputView struct{ Role, TrackID, Title string }

// ErrorView is the row error of a failed or cancelled transform; it never carries a path, argv or score text.
type ErrorView struct{ Code, Message string }

// errorMessages are the default sentences of the row error codes.
var errorMessages = map[string]string{
	ErrCancelledByUser:    "cancelled by the user",
	ErrInterrupted:        "the server restarted while the transform was running",
	ErrBudgetExceeded:     "the render used more CPU than its budget",
	ErrTimeout:            "the render ran past its wall-clock deadline",
	ErrMemoryExceeded:     "the render used more memory than its estimate",
	ErrDSPNonFinite:       "the render produced non-finite samples",
	ErrDSPSilentOutput:    "the render produced silence",
	ErrLoudnessUnsafe:     "the result failed the loudness check",
	ErrSynthFailed:        "the synthesizer failed",
	ErrInputChanged:       "an input changed since the transform was created",
	ErrInvalidTransform:   "the transform is invalid",
	ErrTransformFailed:    "the transform failed",
	ErrSoundfontUnknown:   "the soundfont is not configured",
	ErrIngestFailedPrefix: "the result could not be ingested",
}

func errorMessage(code, text string) string {
	if text != "" {
		return text
	}
	if m, ok := errorMessages[code]; ok {
		return m
	}
	if strings.HasPrefix(code, ErrIngestFailedPrefix) {
		return errorMessages[ErrIngestFailedPrefix]
	}
	return "the transform did not finish"
}

const viewSelect = `SELECT id, kind, state, phase, progress_permille, title, spec_hash, rationale, principal_id, principal_kind, principal_name,
	expected_cpu_ms, max_cpu_ms, cpu_ms, out_seconds_est, error_code, error_text, cancel_requested_at, created_at, started_at, finished_at
	FROM transforms`

type rowScanner interface{ Scan(dest ...any) error }

func scanView(sc rowScanner) (View, error) {
	var v View
	var kind string
	var phase, rationale, ecode, etext, cancelReq, started, finished sql.NullString
	var cpu sql.NullInt64
	if err := sc.Scan(&v.ID, &kind, &v.State, &phase, &v.ProgressPermille, &v.Title, &v.SpecHash, &rationale, &v.PrincipalID, &v.PrincipalKind,
		&v.PrincipalName, &v.ExpectedMS, &v.MaxMS, &cpu, &v.OutputSecondsEst, &ecode, &etext, &cancelReq, &v.CreatedAt, &started, &finished); err != nil {
		return View{}, err
	}
	v.Kind = Kind(kind)
	v.Phase, v.Rationale, v.CancelRequestedAt, v.StartedAt, v.FinishedAt = nstr(phase), nstr(rationale), nstr(cancelReq), nstr(started), nstr(finished)
	if cpu.Valid {
		v.UsedMS = &cpu.Int64
	}
	if ecode.Valid {
		v.Error = &ErrorView{Code: ecode.String, Message: errorMessage(ecode.String, etext.String)}
	}
	v.Inputs = []InputView{}
	v.Warnings = []Warning{}
	return v, nil
}

func nstr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}

// view reads one transform with its inputs (and, once done, its render-time warnings). ErrNotFound-shaped 404 for an unknown id.
func (s *Service) view(ctx context.Context, q queryer, id string) (View, error) {
	v, err := scanView(q.QueryRowContext(ctx, viewSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return View{}, problem.E(http.StatusNotFound, "NOT_FOUND", "no such transform", nil)
	}
	if err != nil {
		return View{}, fmt.Errorf("transform: view: %w", err)
	}
	if err := s.fillInputs(ctx, q, []*View{&v}); err != nil {
		return View{}, err
	}
	if v.State == StateDone && s.o.RenderWarnings != nil {
		if w, err := s.o.RenderWarnings(ctx, v.ID); err == nil && w != nil {
			v.Warnings = w
		}
	}
	return v, nil
}

// fillInputs loads the inputs (role, track, title) of several views in one query.
func (s *Service) fillInputs(ctx context.Context, q queryer, vs []*View) error {
	if len(vs) == 0 {
		return nil
	}
	byID := map[string]*View{}
	args := make([]any, 0, len(vs))
	marks := make([]string, 0, len(vs))
	for _, v := range vs {
		byID[v.ID] = v
		args = append(args, v.ID)
		marks = append(marks, "?")
	}
	rows, err := q.QueryContext(ctx, `SELECT i.transform_id, i.role, i.track_id, t.title FROM transform_inputs i JOIN tracks t ON t.id = i.track_id
		WHERE i.transform_id IN (`+strings.Join(marks, ",")+`) ORDER BY i.transform_id, i.ord`, args...) //nolint:gosec // G202: placeholders only
	if err != nil {
		return fmt.Errorf("transform: inputs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var in InputView
		if err := rows.Scan(&id, &in.Role, &in.TrackID, &in.Title); err != nil {
			return err
		}
		byID[id].Inputs = append(byID[id].Inputs, in)
	}
	return rows.Err()
}

// Get answers get_transform: every principal with read scope sees every transform (the library has one owner).
func (s *Service) Get(ctx context.Context, _ auth.Principal, id string) (View, error) {
	if !s.Enabled() {
		return View{}, unavailable("transform")
	}
	return s.view(ctx, s.o.Store.R, id)
}

// ListFilter is the filter of List; Limit is clamped to 1..100 (default 50).
type ListFilter struct {
	State   string
	Kind    Kind
	TrackID string // an input Take
	Limit   int
	Cursor  string
}

// Page is a page of List, newest first; NextCursor is empty on the last page.
type Page struct {
	Items      []View
	NextCursor string
}

func encodeCursor(createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "|" + id))
}

func decodeCursor(c string) (createdAt, id string, ok bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", "", false
	}
	createdAt, id, ok = strings.Cut(string(b), "|")
	return createdAt, id, ok && createdAt != "" && id != ""
}

// List answers list_transforms, ordered by created_at DESC, id DESC (the order of the cursor).
func (s *Service) List(ctx context.Context, _ auth.Principal, f ListFilter) (Page, error) {
	if !s.Enabled() {
		return Page{}, unavailable("transform")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	where, args := []string{"1=1"}, []any{}
	if f.State != "" {
		where, args = append(where, "state = ?"), append(args, f.State)
	}
	if f.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, string(f.Kind))
	}
	if f.TrackID != "" {
		where, args = append(where, "id IN (SELECT transform_id FROM transform_inputs WHERE track_id = ?)"), append(args, f.TrackID)
	}
	if f.Cursor != "" {
		at, id, ok := decodeCursor(f.Cursor)
		if !ok {
			return Page{}, problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "the cursor is not valid", map[string]any{"field": "cursor"})
		}
		where, args = append(where, "(created_at < ? OR (created_at = ? AND id < ?))"), append(args, at, at, id)
	}
	rows, err := s.o.Store.R.QueryContext(ctx, viewSelect+` WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, limit+1)...) //nolint:gosec // G202: fixed fragments
	if err != nil {
		return Page{}, fmt.Errorf("transform: list: %w", err)
	}
	var vs []View
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			_ = rows.Close()
			return Page{}, err
		}
		vs = append(vs, v)
	}
	if err := rows.Close(); err != nil {
		return Page{}, err
	}
	var pg Page
	if len(vs) > limit {
		vs = vs[:limit]
		pg.NextCursor = encodeCursor(vs[limit-1].CreatedAt, vs[limit-1].ID)
	}
	ptrs := make([]*View, len(vs))
	for i := range vs {
		ptrs[i] = &vs[i]
	}
	if err := s.fillInputs(ctx, s.o.Store.R, ptrs); err != nil {
		return Page{}, err
	}
	pg.Items = vs
	if pg.Items == nil {
		pg.Items = []View{}
	}
	return pg, nil
}
