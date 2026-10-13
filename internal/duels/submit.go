package duels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"modernc.org/sqlite"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// sqliteConstraintUnique is SQLITE_CONSTRAINT_UNIQUE.
const sqliteConstraintUnique = 2067

func isUnique(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqliteConstraintUnique
}

// normNote trims Unicode space; empty means no note (C48.4).
func normNote(n *string) (*string, error) {
	if n == nil {
		return nil, nil
	}
	t := strings.TrimSpace(*n)
	if t == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(t) > maxNoteRunes {
		return nil, invalid("note", fmt.Sprintf("note is longer than %d characters", maxNoteRunes))
	}
	return &t, nil
}

// Submit records a verdict (C48.4).
func (s *Service) Submit(ctx context.Context, p auth.Principal, id string, in VerdictInput) (SubmitResult, error) {
	if s.unconfigured() {
		return SubmitResult{}, errors.ErrUnsupported
	}
	src, err := canJudge(p)
	if err != nil {
		return SubmitResult{}, err
	}
	switch in.Verdict {
	case "a", "b", "tie", "both_bad":
	default:
		return SubmitResult{}, invalid("verdict", "verdict must be one of a, b, tie, both_bad")
	}
	note, err := normNote(in.Note)
	if err != nil {
		return SubmitResult{}, err
	}
	if in.LatencyMS != nil && (*in.LatencyMS < 0 || *in.LatencyMS > 86400000) {
		return SubmitResult{}, invalid("latency_ms", "latency_ms must be between 0 and 86400000")
	}
	if in.Switches != nil && (*in.Switches < 0 || *in.Switches > 100000) {
		return SubmitResult{}, invalid("switches", "switches must be between 0 and 100000")
	}
	d, err := s.loadDuel(ctx, id)
	if err != nil {
		return SubmitResult{}, err
	}
	if !s.now().Before(d.ExpireAt) {
		return SubmitResult{}, problem.E(409, "DUEL_EXPIRED", "the duel has expired", map[string]any{"duel_id": id})
	}
	vs, err := s.loadVerdicts(ctx, id)
	if err != nil {
		return SubmitResult{}, err
	}
	if ex := ownVerdict(src, p, vs[id]); ex != nil {
		return s.replayOrConflict(ctx, p, d, ex, in.Verdict, note)
	}
	st := s.standingOf(p, d, vs[id])
	var winner any
	switch in.Verdict {
	case "a":
		winner = d.SlotA
	case "b":
		winner = d.SlotB
	}
	vid := s.newID()
	_, err = s.o.Store.W.ExecContext(ctx, `INSERT INTO duel_verdicts(id, duel_id, source, principal_kind, principal_id, verdict, winner_track_id, note,
		latency_ms, switches, saw_reveal, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		vid, id, src, string(p.Kind), p.Key(), in.Verdict, winner, ptr(note), ptr(in.LatencyMS), ptr(in.Switches), st.revealed, rfc(s.now()))
	if err != nil {
		if !isUnique(err) {
			return SubmitResult{}, fmt.Errorf("duels: insert verdict: %w", err)
		}
		// A concurrent submit won the unique index: resolve through the replay rule.
		vs2, err := s.loadVerdicts(ctx, id)
		if err != nil {
			return SubmitResult{}, err
		}
		ex := ownVerdict(src, p, vs2[id])
		if ex == nil {
			return SubmitResult{}, fmt.Errorf("duels: insert verdict: %w", err)
		}
		return s.replayOrConflict(ctx, p, d, ex, in.Verdict, note)
	}
	return s.result(ctx, p, d, vid, false)
}

func ptr[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

// ownVerdict is the verdict that occupies p's unique slot: the single human-source
// verdict for a human-equivalent principal, the principal's own critic verdict otherwise.
func ownVerdict(src string, p auth.Principal, vs []verdictRow) *verdictRow {
	for i := range vs {
		v := &vs[i]
		if v.Source == src && (src == "human" || v.Principal == p.Key()) {
			return v
		}
	}
	return nil
}

func (s *Service) replayOrConflict(ctx context.Context, p auth.Principal, d duelRow, ex *verdictRow, verdict string, note *string) (SubmitResult, error) {
	same := ex.Verdict == verdict && ex.Note.Valid == (note != nil) && (note == nil || ex.Note.String == *note)
	if !same {
		return SubmitResult{}, problem.E(409, "DUEL_ALREADY_JUDGED", "this duel already has your verdict", map[string]any{"verdict_id": ex.ID})
	}
	return s.result(ctx, p, d, ex.ID, true)
}

// result renders the caller's now-revealed view plus the stored verdict.
func (s *Service) result(ctx context.Context, p auth.Principal, d duelRow, verdictID string, replayed bool) (SubmitResult, error) {
	vs, err := s.loadVerdicts(ctx, d.ID)
	if err != nil {
		return SubmitResult{}, err
	}
	var stored *verdictRow
	for i := range vs[d.ID] {
		if vs[d.ID][i].ID == verdictID {
			stored = &vs[d.ID][i]
		}
	}
	if stored == nil {
		return SubmitResult{}, sql.ErrNoRows
	}
	view, err := s.view(ctx, p, d, vs[d.ID], nil, true)
	if err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{Duel: view, Verdict: verdictView(*stored, true), Replayed: replayed}, nil
}
