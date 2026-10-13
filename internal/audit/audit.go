// Package audit is the append-only audit log (design 4.4, C29).
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
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

// Insert appends one row.
func (s *Store) Insert(ctx context.Context, e Entry) error {
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
	pid := e.Principal.Key()
	_, err = s.st.W.ExecContext(ctx, `INSERT INTO audit_log
(id, at, principal_kind, principal_id, principal_name, transport, client, op, method, args_sha256,
 status, error_code, cost_micro_usd, idempotency, created_ids_json, duration_ms)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,0,?,?,?)`,
		s.newID(), e.At.UTC().Format(time.RFC3339), kind, pid, e.Principal.Name, string(e.Transport), e.Client,
		e.Op, e.Method, e.ArgsSHA256, e.Status, code, idem, string(idsJSON), e.Duration.Milliseconds())
	return err
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
