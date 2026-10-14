// Package audit is the append-only audit log (design 4.4, C29).
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Idempotency outcomes recorded in audit rows.
const (
	IdemNone       = "none"
	IdemStored     = "stored"
	IdemReplayed   = "replayed"
	IdemConflict   = "conflict"
	IdemInProgress = "in_progress"
	IdemMissing    = "missing"
)

// Entry is one audit row to insert. It never carries request or response bodies.
type Entry struct {
	At          time.Time
	Principal   auth.Principal
	Transport   reqinfo.Transport
	Client      string
	Op          string
	Method      string
	ArgsSHA256  string
	Status      int
	ErrorCode   string
	Idempotency string
	CreatedIDs  []string
	Duration    time.Duration
	// CostMicroUSD is the money the action caused or reserved: the batch ceiling for spend operations,
	// the amount of D-M3-22 for runner:* and reconciler:* rows, else 0.
	CostMicroUSD money.MicroUSD
	// ID overrides the generated row id (tests); empty means the store's generator, or a UUIDv7 for InsertTx.
	ID string
}

// Row is a stored audit row as listed.
type Row struct {
	ID            string   `json:"id"`
	At            string   `json:"at"`
	PrincipalKind string   `json:"principal_kind"`
	PrincipalID   string   `json:"principal_id"`
	PrincipalName string   `json:"principal_name"`
	Transport     string   `json:"transport"`
	Client        string   `json:"client"`
	Op            string   `json:"op"`
	Method        string   `json:"method"`
	ArgsSHA256    string   `json:"args_sha256"`
	Status        int      `json:"status"`
	ErrorCode     string   `json:"error_code"`
	CostMicroUSD  int64    `json:"cost_micro_usd"`
	Idempotency   string   `json:"idempotency"`
	CreatedIDs    []string `json:"created_ids"`
	DurationMS    int64    `json:"duration_ms"`
}

// Filter narrows List. Zero fields match everything.
type Filter struct {
	PrincipalID  string // Principal.Key() or bare id as stored
	Op           string
	Transport    string
	Since, Until time.Time
	Cursor       string
	Limit        int // default 50, max 200

	// get_audit filters (C38); each list matches any of its values.
	PrincipalKinds []string
	Ops            []string
	ExcludeOps     []string
	StatusMin      int
}

// Page is one newest-first page; NextCursor is empty on the last page.
type Page struct {
	Items      []Row  `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Store reads and writes audit_log.
type Store struct {
	st    *store.Store
	newID func() string
}

// NewStore builds a Store; newID must yield time-ordered ids (UUIDv7).
func NewStore(st *store.Store, newID func() string) *Store { return &Store{st: st, newID: newID} }

// insertSQL is the one INSERT text shared by Store.Insert and InsertTx.
const insertSQL = `INSERT INTO audit_log
(id, at, principal_kind, principal_id, principal_name, transport, client, op, method, args_sha256,
 status, error_code, cost_micro_usd, idempotency, created_ids_json, duration_ms)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// execer is the write side shared by *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertRow(ctx context.Context, x execer, id string, e Entry) error {
	ids := e.CreatedIDs
	if ids == nil {
		ids = []string{}
	}
	idsJSON, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	idem := e.Idempotency
	if idem == "" {
		idem = IdemNone
	}
	var code any
	if e.ErrorCode != "" {
		code = e.ErrorCode
	}
	kind := string(e.Principal.Kind)
	if kind == "" {
		kind = string(auth.KindAnonymous)
	}
	_, err = x.ExecContext(ctx, insertSQL,
		id, e.At.UTC().Format(time.RFC3339), kind, e.Principal.Key(), e.Principal.Name, string(e.Transport), e.Client,
		e.Op, e.Method, e.ArgsSHA256, e.Status, code, int64(e.CostMicroUSD), idem, string(idsJSON), e.Duration.Milliseconds())
	return err
}

// Insert appends one row.
func (s *Store) Insert(ctx context.Context, e Entry) error {
	id := e.ID
	if id == "" {
		id = s.newID()
	}
	return insertRow(ctx, s.st.W, id, e)
}

// InsertTx appends the row inside the caller's transaction, so the audit row exists exactly when the
// effect it records committed (M3 C80, D-M3-21). It sets the gate's Audited mark; the gate then skips
// its own insert. The handler clears marks on every error path (idem.Marks.Reset).
func InsertTx(ctx context.Context, tx *sql.Tx, e Entry) error {
	id := e.ID
	if id == "" {
		id = uuid.NewV7().String()
	}
	if err := insertRow(ctx, tx, id, e); err != nil {
		return err
	}
	idem.MarksFrom(ctx).MarkAudited()
	return nil
}

type pendingKey struct{}

// WithPending stores the gate's precomputed entry (principal, transport, client, op, method, args sha,
// idempotency outcome so far) for a handler that audits in its own transaction.
func WithPending(ctx context.Context, e Entry) context.Context {
	return context.WithValue(ctx, pendingKey{}, e)
}

// PendingFrom returns the gate's precomputed entry. The handler completes it (status, idempotency
// "stored", created ids, measured duration, cost) and passes it to InsertTx.
func PendingFrom(ctx context.Context) (Entry, bool) {
	e, ok := ctx.Value(pendingKey{}).(Entry)
	return e, ok
}

// List returns audit rows newest first with a keyset cursor on id.
func (s *Store) List(ctx context.Context, f Filter) (Page, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var where []string
	var args []any
	if f.PrincipalID != "" {
		where = append(where, "principal_id = ?")
		args = append(args, f.PrincipalID)
	}
	if f.Op != "" {
		where = append(where, "op = ?")
		args = append(args, f.Op)
	}
	in := func(col, neg string, vals []string) {
		if len(vals) == 0 {
			return
		}
		where = append(where, col+neg+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	in("principal_kind", "", f.PrincipalKinds)
	in("op", "", f.Ops)
	in("op", " NOT", f.ExcludeOps)
	if f.StatusMin > 0 {
		where = append(where, "status >= ?")
		args = append(args, f.StatusMin)
	}
	if f.Transport != "" {
		where = append(where, "transport = ?")
		args = append(args, f.Transport)
	}
	if !f.Since.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, f.Since.UTC().Format(time.RFC3339))
	}
	if !f.Until.IsZero() {
		where = append(where, "at < ?")
		args = append(args, f.Until.UTC().Format(time.RFC3339))
	}
	if f.Cursor != "" {
		where = append(where, "id < ?")
		args = append(args, f.Cursor)
	}
	q := `SELECT id, at, principal_kind, principal_id, principal_name, transport, client, op, method, args_sha256,
status, COALESCE(error_code,''), cost_micro_usd, idempotency, created_ids_json, duration_ms FROM audit_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ") //nolint:gosec // fragments are constants above; values are bound
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.st.R.QueryContext(ctx, q, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Items: []Row{}}
	for rows.Next() {
		var r Row
		var ids string
		if err := rows.Scan(&r.ID, &r.At, &r.PrincipalKind, &r.PrincipalID, &r.PrincipalName, &r.Transport, &r.Client,
			&r.Op, &r.Method, &r.ArgsSHA256, &r.Status, &r.ErrorCode, &r.CostMicroUSD, &r.Idempotency, &ids, &r.DurationMS); err != nil {
			return Page{}, err
		}
		if err := json.Unmarshal([]byte(ids), &r.CreatedIDs); err != nil {
			return Page{}, fmt.Errorf("audit: bad created_ids_json: %w", err)
		}
		page.Items = append(page.Items, r)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[limit-1].ID
	}
	return page, nil
}

type collectorKey struct{}

type collector struct {
	mu  sync.Mutex
	ids []string
}

// WithCollector returns a context whose NoteCreated calls are gathered.
func WithCollector(ctx context.Context) context.Context {
	return context.WithValue(ctx, collectorKey{}, &collector{})
}

// NoteCreated records ids of rows a handler created; a no-op without a collector.
func NoteCreated(ctx context.Context, ids ...string) {
	if c, ok := ctx.Value(collectorKey{}).(*collector); ok {
		c.mu.Lock()
		c.ids = append(c.ids, ids...)
		c.mu.Unlock()
	}
}

// Created returns the ids noted so far.
func Created(ctx context.Context) []string {
	c, ok := ctx.Value(collectorKey{}).(*collector)
	if !ok {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ids...)
}

var failures atomic.Int64

// NoteFailure counts an audit row that could not be written after every retry.
func NoteFailure() { failures.Add(1) }

// Failures reports audit rows lost since process start; non-zero means the audit trail has a gap.
func Failures() int64 { return failures.Load() }

// Inserter is the write side of the audit log; *Store implements it.
type Inserter interface {
	Insert(ctx context.Context, e Entry) error
}

// Retry defaults and cap, shared by every caller of InsertRetrying.
const (
	DefaultBudget  = 15 * time.Second
	DefaultBackoff = 25 * time.Millisecond
	maxBackoff     = 500 * time.Millisecond
)

// InsertRetrying writes the row, retrying with doubling backoff (first delay `backoff`, cap
// 500 ms) inside `budget`, detached from ctx cancellation. Constraint violations are permanent
// and not retried. A persistent failure is logged at error level and counted (NoteFailure) so
// the caller can still return the response of an effect that already committed.
func InsertRetrying(ctx context.Context, ins Inserter, e Entry, budget, backoff time.Duration) {
	if budget <= 0 {
		budget = DefaultBudget
	}
	if backoff <= 0 {
		backoff = DefaultBackoff
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	delay := backoff
	for {
		err := ins.Insert(actx, e)
		if err == nil {
			return
		}
		if permanent(err) {
			NoteFailure()
			slog.Error("audit insert failed", "op", e.Op, "err", redact.Error(err))
			return
		}
		t := time.NewTimer(delay)
		select {
		case <-actx.Done():
			t.Stop()
			NoteFailure()
			slog.Error("audit insert failed", "op", e.Op, "err", redact.Error(err))
			return
		case <-t.C:
		}
		delay = min(delay*2, maxBackoff)
	}
}

// permanent reports errors a retry cannot cure (a constraint violation such as a duplicate id).
func permanent(err error) bool {
	return strings.Contains(err.Error(), "constraint failed")
}
