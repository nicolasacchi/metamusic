package callbacks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"modernc.org/sqlite"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// VerdictInput is the body of submit_callback_verdict.
type VerdictInput struct {
	Verdict  string
	Note     *string
	HeardAtS *float64
}

// SubmitResult is the answer of Submit: Replayed is true for an identical repeat by the same source.
type SubmitResult struct {
	Replayed            bool    `json:"replayed"`
	Verdict             Verdict `json:"verdict"`
	CommentState        *string `json:"comment_state"`
	RegressionCommentID *string `json:"regression_comment_id"`
}

// maxNoteRunes is the wire cap of a note (the column allows 2000).
const maxNoteRunes = 500

// sqliteConstraintUnique is SQLITE_CONSTRAINT_UNIQUE.
const sqliteConstraintUnique = 2067

func isUnique(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqliteConstraintUnique
}

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

func validVerdict(kind, v string) bool {
	switch kind {
	case "fix":
		return v == "fixed" || v == "not_yet" || v == "worse"
	case "keep":
		return v == "kept" || v == "lost"
	}
	return false
}

// regressionPrefix starts the body of the comment a lost keep verdict writes on the child.
const regressionPrefix = "Lost from parent: "

// Submit answers a callback (C255.4). The source comes from the principal (the operator and paired devices write the single human
// verdict that moves the comment, agents write critic verdicts that never do). One write transaction holds the verdict row, the
// human transition with state_before/state_after and the regression comment of a lost keep. An identical repeat by the same source
// is a replay (200); a different verdict is 409 CALLBACK_ALREADY_JUDGED.
func (s *Service) Submit(ctx context.Context, p auth.Principal, id string, in VerdictInput) (SubmitResult, error) {
	if err := s.unconfigured(); err != nil {
		return SubmitResult{}, err
	}
	src, err := sourceOf(p)
	if err != nil {
		return SubmitResult{}, err
	}
	note, err := normNote(in.Note)
	if err != nil {
		return SubmitResult{}, err
	}
	if in.HeardAtS != nil && (math.IsNaN(*in.HeardAtS) || math.IsInf(*in.HeardAtS, 0) || *in.HeardAtS < 0) {
		return SubmitResult{}, invalid("heard_at_s", "heard_at_s must be 0 or more")
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("callbacks: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	x, err := scanRow(tx.QueryRowContext(ctx, `SELECT `+rowCols+rowFrom+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SubmitResult{}, notFound(id)
	}
	if err != nil {
		return SubmitResult{}, fmt.Errorf("callbacks: load: %w", err)
	}
	if !validVerdict(x.kind, in.Verdict) {
		want := "fixed, not_yet or worse"
		if x.kind == "keep" {
			want = "kept or lost"
		}
		return SubmitResult{}, invalid("verdict", fmt.Sprintf("a %s callback takes %s", x.kind, want))
	}
	if ex, err := s.ownVerdict(ctx, tx, id, src, p); err != nil {
		return SubmitResult{}, err
	} else if ex != nil {
		return replayOrConflict(ex, in.Verdict, note)
	}
	sp, err := spotOf(ctx, tx, x)
	if err != nil {
		return SubmitResult{}, err
	}
	now := s.o.Now()
	vid := s.o.NewID()
	var stateBefore, stateAfter, regression any
	var commentState, regressionID *string
	if src == "human" {
		after, err := s.humanTransition(ctx, tx, p, x, in.Verdict, now)
		if err != nil {
			return SubmitResult{}, err
		}
		stateBefore, stateAfter = x.cState, after
		commentState = &after
		if x.kind == "keep" && in.Verdict == "lost" {
			rid := s.o.NewID()
			var at *float64
			if x.cAt.Valid {
				v := sp.ChildAtS
				at = &v
			}
			if err := judgments.AddCommentTx(ctx, tx, judgments.NewCommentTx{ID: rid, TrackID: x.childID, Body: regressionPrefix + x.cBody, Kind: "fix",
				Source: "callback", AtSecond: at, Author: p, OriginCallbackID: id, Now: now}); err != nil {
				return SubmitResult{}, err
			}
			regression, regressionID = rid, &rid
		}
	}
	var noteArg, heardArg any
	if note != nil {
		noteArg = *note
	}
	if in.HeardAtS != nil {
		heardArg = *in.HeardAtS
	}
	created := store.FormatTime(now)
	_, err = tx.ExecContext(ctx, `INSERT INTO callback_verdicts(id, callback_id, source, principal_kind, principal_id, verdict, note, heard_at_s, map_basis,
		state_before, state_after, regression_comment_id, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		vid, id, src, string(p.Kind), p.Key(), in.Verdict, noteArg, heardArg, sp.Basis, stateBefore, stateAfter, regression, created)
	if err != nil {
		if !isUnique(err) {
			return SubmitResult{}, fmt.Errorf("callbacks: insert verdict: %w", err)
		}
		// a concurrent submit won the unique index: resolve through the replay rule
		_ = tx.Rollback()
		ex, err2 := s.ownVerdict(ctx, s.o.Store.R, id, src, p)
		if err2 != nil || ex == nil {
			return SubmitResult{}, fmt.Errorf("callbacks: insert verdict: %w", err)
		}
		return replayOrConflict(ex, in.Verdict, note)
	}
	if err := tx.Commit(); err != nil {
		return SubmitResult{}, fmt.Errorf("callbacks: commit: %w", err)
	}
	v := Verdict{ID: vid, Source: src, PrincipalKind: string(p.Kind), Verdict: in.Verdict, Note: note, HeardAtS: in.HeardAtS, MapBasis: &sp.Basis, RegressionCommentID: regressionID, CreatedAt: created}
	if src == "human" {
		sb := x.cState
		v.StateBefore, v.StateAfter = &sb, commentState
	}
	return SubmitResult{Verdict: v, CommentState: commentState, RegressionCommentID: regressionID}, nil
}

// humanTransition applies a human verdict to the comment and returns the state after (C255.3, D-M5-4). A keep verdict never
// moves its comment; a callback on a resolved comment leaves it as it is; a fix verdict applies the order-free sibling rule over
// the human verdicts of every fix callback of the comment under the same patch: any fixed => resolved, else any worse => regressed,
// else open.
func (s *Service) humanTransition(ctx context.Context, tx *sql.Tx, p auth.Principal, x row, verdict string, now time.Time) (string, error) {
	if x.kind == "keep" || x.cState == "resolved" {
		return x.cState, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT v.verdict FROM callback_verdicts v JOIN callbacks c ON c.id = v.callback_id
		WHERE c.comment_id = ? AND c.patch_id = ? AND c.kind = 'fix' AND v.source = 'human'`, x.commentID, x.patchID)
	if err != nil {
		return "", fmt.Errorf("callbacks: sibling verdicts: %w", err)
	}
	all := []string{verdict}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return "", err
		}
		all = append(all, v)
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	to := siblingTarget(all)
	if _, err := judgments.SystemMoveTx(ctx, tx, judgments.SystemHumanCallback, x.commentID, to, p.Key(), now, nil); err != nil {
		return "", err
	}
	return to, nil
}

// siblingTarget is the order-free rule of D-M5-4 over the human verdicts of one comment under one patch.
func siblingTarget(verdicts []string) string {
	worse := false
	for _, v := range verdicts {
		switch v {
		case "fixed":
			return "resolved"
		case "worse":
			worse = true
		}
	}
	if worse {
		return "regressed"
	}
	return "open"
}

func (s *Service) ownVerdict(ctx context.Context, q querier, callbackID, src string, p auth.Principal) (*Verdict, error) {
	vs, err := loadVerdicts(ctx, q, []string{callbackID})
	if err != nil {
		return nil, err
	}
	for i := range vs[callbackID] {
		v := vs[callbackID][i]
		if v.Source == src && (src == "human" || v.principalID == p.Key()) {
			return &v.Verdict, nil
		}
	}
	return nil, nil
}

func replayOrConflict(ex *Verdict, verdict string, note *string) (SubmitResult, error) {
	same := ex.Verdict == verdict && (ex.Note == nil) == (note == nil) && (note == nil || *ex.Note == *note)
	if !same {
		return SubmitResult{}, problem.E(409, "CALLBACK_ALREADY_JUDGED", "this callback already has your answer", map[string]any{"verdict_id": ex.ID})
	}
	return SubmitResult{Replayed: true, Verdict: *ex, CommentState: ex.StateAfter, RegressionCommentID: ex.RegressionCommentID}, nil
}
