package desk

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// VerdictIn is a verdict on a plan (C358.5). Verdict is liked, rejected, or empty to clear it; Note is optional.
type VerdictIn struct {
	Verdict string
	Note    string
}

// Rater is the row a principal writes: people write the owner row, everything else (agents, grants, system) the critic row
// (D-DESK-14, D-G1-16).
func Rater(p auth.Principal) string {
	if p.IsPerson() {
		return "owner"
	}
	return "critic"
}

// SetVerdict records the caller's verdict. An empty verdict clears it and keeps the row (the ratings pattern); a row is never
// deleted. The API gate has already refused an Editor.
func (s *Service) SetVerdict(ctx context.Context, p auth.Principal, id string, in VerdictIn) (Plan, error) {
	if err := s.wired(); err != nil {
		return Plan{}, err
	}
	switch in.Verdict {
	case "", "liked", "rejected":
	default:
		return Plan{}, invalid("verdict", "verdict is liked, rejected or null")
	}
	if len([]rune(in.Note)) > MaxNote {
		return Plan{}, invalid("note", "the note is longer than 2000 characters")
	}
	var one int
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT 1 FROM desk_plans WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return Plan{}, notFound("plan")
	} else if err != nil {
		return Plan{}, err
	}
	var v, note any
	if in.Verdict != "" {
		v = in.Verdict
	}
	if in.Note != "" {
		note = in.Note
	}
	if _, err := s.o.Store.W.ExecContext(ctx, `INSERT INTO desk_plan_verdicts(plan_id, rater, verdict, note, decided_by, decided_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(plan_id, rater) DO UPDATE SET verdict = excluded.verdict, note = excluded.note, decided_by = excluded.decided_by, decided_at = excluded.decided_at`,
		id, Rater(p), v, note, p.Key(), s.now()); err != nil {
		return Plan{}, err
	}
	return s.Get(ctx, p, id, DefaultInclude)
}
