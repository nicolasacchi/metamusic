package pods

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// DB is the part of *sql.DB and *sql.Tx the store helpers use; a helper never begins a transaction, so the lifecycle lane can compose
// several (for example the ledger reserve and MarkOverheadReserved) in one.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Errors of the store helpers.
var (
	ErrNotFound     = errors.New("pods: no such pod")
	ErrStateChanged = errors.New("pods: the pod is not in the expected state")
)

// Pod kinds.
const (
	KindBatch  = "batch"
	KindCanary = "canary"
)

// Assignment statuses (pod_assignments.status).
const (
	AssignOK         = "ok"
	AssignError      = "error"
	AssignLost       = "lost"
	AssignNotHanded  = "not_handed"
	AssignCancelled  = "cancelled"
	codeWorkerReboot = "WORKER_RESTARTED"
)

// Row is one row of the pods table (only what the lifecycle and the poll handler read). ProviderRef is the RunPod pod id: it never leaves this
// package's callers through a response, a log line or an event.
type Row struct {
	ID, Name, Kind, BatchID, GPUType, State, ImageDigest string
	N, GPUTry, Restarts                                  int
	ProviderRef, LastReported, ErrorCode                 string
	PricePPH                                             money.MicroUSD
	OverheadReserved                                     money.MicroUSD
	OverheadReservedAt                                   *time.Time
	DeadlineAt                                           time.Time
	PollExp                                              int64
	CreateSentAt, RunningSeenAt, FirstPollAt             *time.Time
	LastPollAt, GoneSeenAt, TerminatedAt                 *time.Time
	LifetimeS                                            *int
	Settled                                              bool
}

// NewPod is what InsertIntent writes: the frozen columns of a pod (C394).
type NewPod struct {
	ID, Name, Kind, BatchID, GPUType, ImageDigest string
	N, GPUTry                                     int
	PricePPH, OverheadReserved                    money.MicroUSD
	DeadlineAt                                    time.Time
	PollExp                                       int64
	At                                            time.Time
}

// PollExpFor is the expiry of a pod's poll link: deadline_at + 600 s (C395.1).
func PollExpFor(deadline time.Time) int64 { return deadline.Unix() + 600 }

// InsertIntent writes a pod in state intent. The UNIQUE (batch_id, n, gpu_try) key makes one intent per pod identity.
func InsertIntent(ctx context.Context, db DB, p NewPod) error {
	at := store.FormatTime(p.At)
	_, err := db.ExecContext(ctx, `INSERT INTO pods(id,name,kind,batch_id,n,gpu_try,gpu_type,state,cloud,image_digest,price_pph_micro_usd,
		overhead_reserved_micro_usd,deadline_at,poll_exp,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'intent','SECURE',?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Kind, p.BatchID, p.N, p.GPUTry, p.GPUType, p.ImageDigest, int64(p.PricePPH), int64(p.OverheadReserved),
		store.FormatTime(p.DeadlineAt), p.PollExp, at, at)
	return err
}

// MarkOverheadReserved sets overhead_reserved_at once; it reports false when it was already set. The lifecycle lane calls it in the
// transaction that writes the ledger reserve and reserves only when it returns true, so a pod's overhead is reserved exactly once
// (ReservePodOverhead itself is not idempotent, P2's as-built note).
func MarkOverheadReserved(ctx context.Context, db DB, podID string, at time.Time) (bool, error) {
	res, err := db.ExecContext(ctx, `UPDATE pods SET overhead_reserved_at = ?, updated_at = ? WHERE id = ? AND overhead_reserved_at IS NULL`,
		store.FormatTime(at), store.FormatTime(at), podID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

const podCols = `id,name,kind,batch_id,gpu_type,state,image_digest,n,gpu_try,restarts,provider_ref,last_reported,error_code,
	price_pph_micro_usd,overhead_reserved_micro_usd,overhead_reserved_at,deadline_at,poll_exp,create_sent_at,running_seen_at,
	first_poll_at,last_poll_at,gone_seen_at,terminated_at,lifetime_s,settled`

type scanner interface{ Scan(dest ...any) error }

func scanPod(s scanner) (Row, error) {
	var p Row
	var ref, rep, code, ovAt, deadline, create, running, first, last, gone, term sql.NullString
	var life sql.NullInt64
	var price, ov int64
	var settled int
	err := s.Scan(&p.ID, &p.Name, &p.Kind, &p.BatchID, &p.GPUType, &p.State, &p.ImageDigest, &p.N, &p.GPUTry, &p.Restarts, &ref, &rep, &code,
		&price, &ov, &ovAt, &deadline, &p.PollExp, &create, &running, &first, &last, &gone, &term, &life, &settled)
	if err != nil {
		return Row{}, err
	}
	p.ProviderRef, p.LastReported, p.ErrorCode = ref.String, rep.String, code.String
	p.PricePPH, p.OverheadReserved, p.Settled = money.MicroUSD(price), money.MicroUSD(ov), settled == 1
	t, err := store.ParseTime(deadline.String)
	if err != nil {
		return Row{}, fmt.Errorf("pods: deadline_at: %w", err)
	}
	p.DeadlineAt = t
	for _, f := range []struct {
		src sql.NullString
		dst **time.Time
	}{{ovAt, &p.OverheadReservedAt}, {create, &p.CreateSentAt}, {running, &p.RunningSeenAt}, {first, &p.FirstPollAt}, {last, &p.LastPollAt}, {gone, &p.GoneSeenAt}, {term, &p.TerminatedAt}} {
		if !f.src.Valid {
			continue
		}
		v, err := store.ParseTime(f.src.String)
		if err != nil {
			return Row{}, fmt.Errorf("pods: bad timestamp: %w", err)
		}
		*f.dst = &v
	}
	if life.Valid {
		n := int(life.Int64)
		p.LifetimeS = &n
	}
	return p, nil
}

// GetRow reads one pod by row id (ErrNotFound when absent).
func GetRow(ctx context.Context, db DB, id string) (Row, error) {
	p, err := scanPod(db.QueryRowContext(ctx, `SELECT `+podCols+` FROM pods WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, ErrNotFound
	}
	return p, err
}

// LiveRows lists the pods of a batch that are not terminated, oldest first.
func LiveRows(ctx context.Context, db DB, batchID string) ([]Row, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+podCols+` FROM pods WHERE batch_id = ? AND state <> 'terminated' ORDER BY created_at, n, gpu_try`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		p, err := scanPod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Move is the optional detail of a transition.
type Move struct {
	At        time.Time
	Event     string // the event name; "" = "state"
	Code      string // an error or reason code (also written to pods.error_code when set)
	Detail    any    // JSON-encoded into the event; never a URL, a provider id or a key
	LifetimeS *int   // with to = terminated
}

// Transition moves a pod from one state to another and appends the event, in the caller's transaction (pass a *sql.Tx; with a
// *sql.DB the two statements are not atomic). It fails with ErrIllegalTransition for a move that is not in Transitions and with
// ErrStateChanged when the pod is not in `from`. Entering terminated also writes terminated_at (a CHECK needs it).
func Transition(ctx context.Context, db DB, podID, from, to string, m Move) error {
	if err := checkMove(from, to); err != nil {
		return err
	}
	at := store.FormatTime(m.At)
	var term, life any
	if to == StateTerminated {
		term = at
		if m.LifetimeS != nil {
			life = *m.LifetimeS
		}
	}
	res, err := db.ExecContext(ctx, `UPDATE pods SET state = ?, updated_at = ?, error_code = COALESCE(?, error_code),
		terminated_at = COALESCE(?, terminated_at), lifetime_s = COALESCE(?, lifetime_s),
		terminate_sent_at = CASE WHEN ? = 'terminating' AND terminate_sent_at IS NULL THEN ? ELSE terminate_sent_at END
		WHERE id = ? AND state = ?`, to, at, nullStr(m.Code), term, life, to, at, podID, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrStateChanged
	}
	ev := m.Event
	if ev == "" {
		ev = "state"
	}
	return AppendEvent(ctx, db, podID, Event{At: m.At, From: from, To: to, Event: ev, Code: m.Code, Detail: m.Detail})
}

// Event is one row of pod_events (append-only).
type Event struct {
	At       time.Time
	From, To string
	Event    string
	Code     string
	Detail   any
}

// AppendEvent appends a pod event. The detail must be free of URLs, provider ids and keys (the caller's duty; the log is append-only).
func AppendEvent(ctx context.Context, db DB, podID string, e Event) error {
	var detail any
	if e.Detail != nil {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return fmt.Errorf("pods: event detail: %w", err)
		}
		detail = string(b)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO pod_events(pod_id,at,from_state,to_state,event,code,detail_json) VALUES(?,?,?,?,?,?,?)`,
		podID, store.FormatTime(e.At), nullStr(e.From), nullStr(e.To), e.Event, nullStr(e.Code), detail)
	return err
}

// QueueAssignment records that a take of a batch waits for a pod (the pod backend's Submit calls it).
func QueueAssignment(ctx context.Context, db DB, attemptID, batchID string, at time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO pod_assignments(attempt_id,batch_id,queued_at) VALUES(?,?,?)`, attemptID, batchID, store.FormatTime(at))
	return err
}

// Assignment is one row of pod_assignments.
type Assignment struct {
	AttemptID, BatchID, PodID, Status, ErrorCode string
	QueuedAt                                     time.Time
	HandedAt, DoneAt                             *time.Time
	ExecutionMS, AgentExecutionMS                *int64
}

// GetAssignment reads one assignment (ErrNotFound when absent).
func GetAssignment(ctx context.Context, db DB, attemptID string) (Assignment, error) {
	var a Assignment
	var pod, status, code, queued, handed, done sql.NullString
	var exec, agent sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT attempt_id,batch_id,pod_id,queued_at,handed_at,done_at,status,execution_ms,agent_execution_ms,error_code
		FROM pod_assignments WHERE attempt_id = ?`, attemptID).Scan(&a.AttemptID, &a.BatchID, &pod, &queued, &handed, &done, &status, &exec, &agent, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return Assignment{}, ErrNotFound
	}
	if err != nil {
		return Assignment{}, err
	}
	a.PodID, a.Status, a.ErrorCode = pod.String, status.String, code.String
	for _, f := range []struct {
		src sql.NullString
		dst **time.Time
	}{{handed, &a.HandedAt}, {done, &a.DoneAt}} {
		if f.src.Valid {
			t, err := store.ParseTime(f.src.String)
			if err != nil {
				return Assignment{}, err
			}
			*f.dst = &t
		}
	}
	if t, err := store.ParseTime(queued.String); err == nil {
		a.QueuedAt = t
	}
	if exec.Valid {
		a.ExecutionMS = &exec.Int64
	}
	if agent.Valid {
		a.AgentExecutionMS = &agent.Int64
	}
	return a, nil
}

// CloseAssignment records the terminal status of an assignment that never ran or was cancelled (status not_handed or cancelled, with
// no pod) once; it reports false when the assignment is already done or handed.
func CloseAssignment(ctx context.Context, db DB, attemptID, status, code string, at time.Time) (bool, error) {
	if status != AssignNotHanded && status != AssignCancelled {
		return false, fmt.Errorf("pods: CloseAssignment status %q", status)
	}
	res, err := db.ExecContext(ctx, `UPDATE pod_assignments SET done_at = ?, status = ?, error_code = ? WHERE attempt_id = ? AND done_at IS NULL AND pod_id IS NULL`,
		store.FormatTime(at), status, nullStr(code), attemptID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// MarkLost records every handed, not-done assignment of a pod as lost with the given code (a restart, an orphan) and returns their attempt ids.
func MarkLost(ctx context.Context, db DB, podID, code string, at time.Time) ([]string, error) {
	rows, err := db.QueryContext(ctx, `UPDATE pod_assignments SET done_at = ?, status = 'lost', error_code = ?
		WHERE pod_id = ? AND handed_at IS NOT NULL AND done_at IS NULL RETURNING attempt_id`, store.FormatTime(at), code, podID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SafetyEntry is one row of pod_safety_log (append-only).
type SafetyEntry struct {
	Kind, ImageDigest, Verdict, CreatedBy string
	Detail                                any
	At                                    time.Time
}

// AppendSafety appends a probe or canary verdict. The database checks the kind/verdict pairing and that a canary names its image digest.
func AppendSafety(ctx context.Context, db DB, e SafetyEntry) error {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO pod_safety_log(kind,image_digest,verdict,detail_json,created_by,created_at) VALUES(?,?,?,?,?,?)`,
		e.Kind, nullStr(e.ImageDigest), e.Verdict, string(b), e.CreatedBy, store.FormatTime(e.At))
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
