package callbacks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var _ jobs.TakeHook = (*Service)(nil)

// systemJob is the writer recorded as state_changed_by when a child arrives (D-M5-4).
const systemJob = "system:job"

const insertCallbackSQL = `INSERT INTO callbacks(id, child_track_id, comment_id, source_track_id, kind, patch_id, job_id, created_at)
	SELECT ?, ?, ?, ?, ?, ?, ?, ? WHERE `

// AfterTakeTx runs inside the fenced take transaction right after the take row exists (C255.1). For a take of a Song PR job it
// moves the addressed comments open|regressed -> addressed, creates one fix callback per patch_comments row and one keep
// callback per open non-hidden keep comment of the patch's base take. Every statement repeats the lease predicate and the inserts
// are idempotent (ON CONFLICT DO NOTHING). Any error rolls the take back: a Song PR child never exists without its callbacks. A
// take that is not a Song PR render costs nothing.
func (s *Service) AfterTakeTx(ctx context.Context, tx *sql.Tx, t jobs.TakeInfo) error {
	if t.PatchID == nil {
		return nil
	}
	if !s.configured() {
		return errors.ErrUnsupported
	}
	fence := t.Fence
	if err := fence.Confirm(ctx, tx, 0); err != nil {
		return err
	}
	var base string
	if err := tx.QueryRowContext(ctx, `SELECT base_track_id FROM patches WHERE id = ?`, *t.PatchID).Scan(&base); err != nil {
		return fmt.Errorf("callbacks: patch %s: %w", *t.PatchID, err)
	}
	type target struct{ comment, track, kind string }
	var targets []target
	rows, err := tx.QueryContext(ctx, `SELECT c.id, c.track_id FROM patch_comments pc JOIN comments c ON c.id = pc.comment_id
		WHERE pc.patch_id = ? ORDER BY pc.comment_id`, *t.PatchID)
	if err != nil {
		return fmt.Errorf("callbacks: patch comments: %w", err)
	}
	for rows.Next() {
		x := target{kind: "fix"}
		if err := rows.Scan(&x.comment, &x.track); err != nil {
			_ = rows.Close()
			return err
		}
		targets = append(targets, x)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	keeps, err := tx.QueryContext(ctx, `SELECT id FROM comments WHERE track_id = ? AND kind = 'keep' AND state = 'open' AND hidden_at IS NULL
		AND id NOT IN (SELECT comment_id FROM patch_comments WHERE patch_id = ?) ORDER BY created_at, id`, base, *t.PatchID)
	if err != nil {
		return fmt.Errorf("callbacks: keep comments: %w", err)
	}
	for keeps.Next() {
		x := target{kind: "keep", track: base}
		if err := keeps.Scan(&x.comment); err != nil {
			_ = keeps.Close()
			return err
		}
		targets = append(targets, x)
	}
	if err := keeps.Close(); err != nil {
		return err
	}
	if err := keeps.Err(); err != nil {
		return err
	}
	now := s.o.Now()
	at := store.FormatTime(now)
	for _, x := range targets {
		if x.kind == "fix" {
			// open|regressed -> addressed; a comment already addressed, or resolved, keeps its state and still gets its callback
			if _, err := judgments.SystemMoveTx(ctx, tx, judgments.SystemJob, x.comment, "addressed", systemJob, now, &fence); err != nil &&
				!errors.Is(err, judgments.ErrInvalidTransition) {
				return err
			}
		}
		args := append([]any{s.o.NewID(), t.TrackID, x.comment, x.track, x.kind, *t.PatchID, t.JobID, at}, fence.Args()...)
		if _, err := tx.ExecContext(ctx, insertCallbackSQL+fence.Where()+` ON CONFLICT(child_track_id, comment_id) DO NOTHING`, args...); err != nil { //nolint:gosec // G202: fixed C69 predicate
			return fmt.Errorf("callbacks: insert: %w", err)
		}
	}
	return nil
}

// AfterTakeCommit runs once after the take committed (C255.6): when the take is the last of its batch to arrive it sends one
// count-only "N callbacks ready" notification. It never reads or sends comment text, titles or lyrics, and a failure is only a
// warning (the callbacks exist either way).
func (s *Service) AfterTakeCommit(ctx context.Context, t jobs.TakeInfo) {
	if t.PatchID == nil || !s.configured() || s.o.Notify == nil {
		return
	}
	var others int
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT state FROM jobs WHERE batch_id = ? AND id <> ?`, t.BatchID, t.JobID)
	if err != nil {
		s.o.Log.Warn("callbacks: batch state", "err", redact.Error(err))
		return
	}
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			_ = rows.Close()
			s.o.Log.Warn("callbacks: batch state", "err", redact.Error(err))
			return
		}
		if !jobs.IsTerminal(st) {
			others++
		}
	}
	_ = rows.Close()
	if rows.Err() != nil || others > 0 {
		return // a later child of the batch notifies
	}
	var n int
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM callbacks c JOIN jobs j ON j.id = c.job_id
		WHERE j.batch_id = ? AND NOT EXISTS (SELECT 1 FROM callback_verdicts v WHERE v.callback_id = c.id AND v.source = 'human')`, t.BatchID).Scan(&n); err != nil {
		s.o.Log.Warn("callbacks: count", "err", redact.Error(err))
		return
	}
	if n == 0 {
		return
	}
	if err := s.o.Notify.Notify(ctx, NoteCallbacksReady(s.o.PublicURL, n)); err != nil {
		s.o.Log.Warn("callbacks: notify", "err", redact.Error(err))
	}
}

// NoteCallbacksReady is the "callbacks ready" notification (C261, priority 3): a count and a link, never comment text, titles,
// lyrics or a query string.
func NoteCallbacksReady(publicURL string, n int) notify.Message {
	url := trimSlash(publicURL) + "/judge/callbacks"
	body := fmt.Sprintf("%d callbacks ready", n)
	if n == 1 {
		body = "1 callback ready"
	}
	return notify.Message{Title: "Callbacks ready", Priority: 3, Body: body, Click: url,
		Actions: []notify.Action{{Action: "view", Label: "Listen", URL: url}}}
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
