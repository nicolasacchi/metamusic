package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Stats counts what a sync function wrote, for the Reconciler report.
type Stats struct{ Created, MembersAdded int }

// Add accumulates o into s.
func (s *Stats) Add(o Stats) { s.Created += o.Created; s.MembersAdded += o.MembersAdded }

// EnsureTx gets or creates the auto session of k.SourceKey (C92). Every stored timestamp is FormatTime(ParseInstant(source))
// (D-L1-34: pre-M3 sources are 20 characters wide, new columns 24). An existing session keeps its name, kind and
// revision; only started_at and ended_at of an auto session are refreshed, and only when they changed.
func EnsureTx(ctx context.Context, tx *sql.Tx, env Env, k Key) (id string, created bool, err error) {
	if k.SourceKey == "" {
		return "", false, problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "a session needs a source key", map[string]any{"field": "source_key"})
	}
	started, err := store.NormaliseInstant(k.StartedAt)
	if err != nil {
		return "", false, err
	}
	var ended *string
	if k.EndedAt != nil {
		e, err := store.NormaliseInstant(*k.EndedAt)
		if err != nil {
			return "", false, err
		}
		if e < started { // never write an ended_at before the start (the column CHECK)
			e = started
		}
		ended = &e
	}
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM sessions WHERE source_key = ?`, k.SourceKey).Scan(&id); {
	case err == nil:
		if ended != nil && *ended < started {
			ended = &started
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET started_at = ?, ended_at = ? WHERE id = ? AND (started_at <> ? OR ended_at IS NOT ?)`,
			started, ended, id, started, ended); err != nil {
			return "", false, fmt.Errorf("sessions: refresh: %w", err)
		}
		return id, false, nil
	case err != sql.ErrNoRows:
		return "", false, fmt.Errorf("sessions: ensure: %w", err)
	}
	id = env.NewID()
	ts := store.FormatTime(env.Now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(id, kind, source_key, name, started_at, ended_at, created_by, created_at, updated_by, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, id, k.Kind, k.SourceKey, k.Name, started, ended, env.Actor, ts, env.Actor, ts); err != nil {
		return "", false, fmt.Errorf("sessions: ensure: insert: %w", err)
	}
	return id, true, nil
}

// AddMembersTx adds members to a session with INSERT OR IGNORE and returns how many were new.
func AddMembersTx(ctx context.Context, tx *sql.Tx, env Env, sessionID, memberKind string, ids []string) (added int, err error) {
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_members(session_id, member_kind, member_id, added_at) VALUES (?,?,?,?)`,
			sessionID, memberKind, id, store.FormatTime(env.Now))
		if err != nil {
			return added, fmt.Errorf("sessions: add member: %w", err)
		}
		n, _ := res.RowsAffected()
		added += int(n)
	}
	return added, nil
}

// addMembersSQL runs an INSERT OR IGNORE ... SELECT that names the session id and the add time as its first two
// arguments, and returns the number of new rows.
func addMembersSQL(ctx context.Context, tx *sql.Tx, env Env, query string, sessionID string, args ...any) (int, error) {
	res, err := tx.ExecContext(ctx, query, append([]any{sessionID, store.FormatTime(env.Now)}, args...)...)
	if err != nil {
		return 0, fmt.Errorf("sessions: add members: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
