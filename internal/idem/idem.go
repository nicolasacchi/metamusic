// Package idem stores idempotency keys and their responses (C29, D-M2a-3, D-M2a-21).
package idem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/nicolasacchi/metamusic/internal/canonjson"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Outcome is what Begin decided.
type Outcome int

const (
	Proceed Outcome = iota
	Replay
	Conflict
	InProgress
)

// Begun is Begin's result; Status, ContentType and Body are set for Replay.
type Begun struct {
	Outcome     Outcome
	Status      int
	ContentType string
	Body        []byte
	// Gen is the lease generation of a Proceed: the created_at Begin's upsert wrote (second-granular
	// RFC 3339). A reclaim needs lease_until <= now, i.e. at least Lease (2 min) later, so two
	// generations of one key never share a value; CompleteTx is fenced by it (M3 C80).
	Gen string
}

// TTLs and the pending-row lease.
const (
	ExplicitTTL = 24 * time.Hour
	DerivedTTL  = 10 * time.Minute
	Lease       = 2 * time.Minute
)

// Store is the idempotency table.
type Store struct {
	st  *store.Store
	now func() time.Time
}

// NewStore builds a Store; a nil now means time.Now.
func NewStore(st *store.Store, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{st: st, now: now}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Begin claims (principalKey, key) for a request, or reports why it cannot.
func (s *Store) Begin(ctx context.Context, principalKey, key, op, reqSHA string, derived bool) (Begun, error) {
	now := s.now()
	ttl := ExplicitTTL
	d := 0
	if derived {
		ttl, d = DerivedTTL, 1
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Begun{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var created string
	err = tx.QueryRowContext(ctx, `
INSERT INTO idempotency(principal_key,key,op,request_sha256,derived,state,created_at,lease_until,expires_at)
VALUES(?,?,?,?,?,'pending',?,?,?)
ON CONFLICT(principal_key,key) DO UPDATE SET
  op=excluded.op, request_sha256=excluded.request_sha256, derived=excluded.derived, state='pending',
  status=NULL, content_type=NULL, response=NULL, created_at=excluded.created_at,
  lease_until=excluded.lease_until, expires_at=excluded.expires_at
WHERE idempotency.expires_at <= excluded.created_at
   OR (idempotency.state = 'pending' AND idempotency.lease_until <= excluded.created_at
       AND idempotency.request_sha256 = excluded.request_sha256)
RETURNING created_at`,
		principalKey, key, op, reqSHA, d, ts(now), ts(now.Add(Lease)), ts(now.Add(ttl))).Scan(&created)
	switch {
	case err == nil:
		if err := tx.Commit(); err != nil {
			return Begun{}, err
		}
		return Begun{Outcome: Proceed, Gen: created}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Begun{}, err
	}

	var (
		sha, state string
		status     sql.NullInt64
		ct         sql.NullString
		body       []byte
	)
	err = tx.QueryRowContext(ctx, `SELECT request_sha256, state, status, content_type, response
FROM idempotency WHERE principal_key = ? AND key = ?`, principalKey, key).Scan(&sha, &state, &status, &ct, &body)
	if err != nil {
		return Begun{}, err
	}
	if err := tx.Commit(); err != nil {
		return Begun{}, err
	}
	switch {
	case sha != reqSHA:
		return Begun{Outcome: Conflict}, nil
	case state == "done":
		return Begun{Outcome: Replay, Status: int(status.Int64), ContentType: ct.String, Body: body}, nil
	}
	return Begun{Outcome: InProgress}, nil
}

// Extend pushes a pending row's lease out to now + Lease.
func (s *Store) Extend(ctx context.Context, principalKey, key string) error {
	_, err := s.st.W.ExecContext(ctx, `UPDATE idempotency SET lease_until = ?
WHERE principal_key = ? AND key = ? AND state = 'pending'`, ts(s.now().Add(Lease)), principalKey, key)
	return err
}

// Complete stores the response of a pending row.
func (s *Store) Complete(ctx context.Context, principalKey, key string, status int, contentType string, body []byte) error {
	if body == nil {
		body = []byte{}
	}
	_, err := s.st.W.ExecContext(ctx, `UPDATE idempotency SET state = 'done', status = ?, content_type = ?, response = ?
WHERE principal_key = ? AND key = ? AND state = 'pending'`, status, contentType, body, principalKey, key)
	return err
}

// Release deletes a pending row so the key can be retried.
func (s *Store) Release(ctx context.Context, principalKey, key string) error {
	_, err := s.st.W.ExecContext(ctx, `DELETE FROM idempotency WHERE principal_key = ? AND key = ? AND state = 'pending'`,
		principalKey, key)
	return err
}

// Prune deletes rows that expired more than an hour ago.
func (s *Store) Prune(ctx context.Context) (int, error) {
	res, err := s.st.W.ExecContext(ctx, `DELETE FROM idempotency WHERE expires_at < ?`, ts(s.now().Add(-time.Hour)))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RequestSHA hashes what identifies a request: operation, method, escaped
// path, the query (parameter order irrelevant) and the body (canonical JSON
// when it parses, the raw bytes otherwise).
func RequestSHA(op, method, escapedPath string, query url.Values, body []byte) (string, error) {
	if c, err := canonjson.Canonical(body); err == nil {
		body = c
	}
	h := sha256.New()
	for _, part := range [][]byte{[]byte(op), []byte(method), []byte(escapedPath), []byte(query.Encode()), body} {
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(len(part))) //nolint:gosec // len is never negative
		h.Write(n[:])
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DerivedKey builds the key the MCP adapter uses when a caller supplies none.
func DerivedKey(principalKey, op string, args json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	c, err := canonjson.Canonical(args)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(principalKey))
	h.Write([]byte{0})
	h.Write([]byte(op))
	h.Write([]byte{0})
	h.Write(c)
	return "mcp:" + hex.EncodeToString(h.Sum(nil)), nil
}

type derivedKeyCtx struct{}

// WithDerived marks the request as carrying a derived key.
func WithDerived(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, derivedKeyCtx{}, key)
}

// DerivedFrom returns the derived key marker, if any.
func DerivedFrom(ctx context.Context) (string, bool) {
	k, ok := ctx.Value(derivedKeyCtx{}).(string)
	return k, ok && k != ""
}

// Redact sets the named top-level fields of a JSON object body to null.
// Bodies that are not objects, or do not contain the fields, are unchanged.
func Redact(body []byte, fields []string) []byte {
	if len(fields) == 0 {
		return body
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	hit := false
	for _, f := range fields {
		if _, ok := m[f]; ok {
			m[f] = json.RawMessage("null")
			hit = true
		}
	}
	if !hit {
		return body
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return body
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// ErrLeaseLost reports that CompleteTx found the pending row reclaimed by another request, or already completed.
// The handler rolls back and answers 409 IDEMPOTENCY_IN_PROGRESS. (Not store.ErrLeaseLost, which is the
// runner lease.)
var ErrLeaseLost = errors.New("idem: idempotency lease lost")

// CompleteTx stores the response of the pending row generation gen inside the caller's transaction, so
// the response is replayable exactly when the effect it describes committed (M3 C80, D-M3-21). Zero rows
// updated means the key was reclaimed (a new created_at) or completed: ErrLeaseLost. On success it sets
// the gate's Completed mark; the handler clears marks on any error path (Marks.Reset).
func CompleteTx(ctx context.Context, tx *sql.Tx, principalKey, key, gen string, status int, contentType string, body []byte) error {
	if body == nil {
		body = []byte{}
	}
	res, err := tx.ExecContext(ctx, `UPDATE idempotency SET state = 'done', status = ?, content_type = ?, response = ?
WHERE principal_key = ? AND key = ? AND state = 'pending' AND created_at = ?`,
		status, contentType, body, principalKey, key, gen)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	MarksFrom(ctx).MarkCompleted()
	return nil
}

type reqKeyCtx struct{}

type reqKey struct{ principalKey, key, gen string }

// WithKey records the idempotency identity the gate began for this request; create operations read it
// with KeyFrom to call CompleteTx.
func WithKey(ctx context.Context, principalKey, key, gen string) context.Context {
	return context.WithValue(ctx, reqKeyCtx{}, reqKey{principalKey, key, gen})
}

// KeyFrom returns the principal key, idempotency key and lease generation set by the gate for a create
// operation; ok is false outside a gated create (for example a direct service test).
func KeyFrom(ctx context.Context) (principalKey, key, gen string, ok bool) {
	k, ok := ctx.Value(reqKeyCtx{}).(reqKey)
	return k.principalKey, k.key, k.gen, ok
}

// Marks are mutable flags the gate installs before calling a handler. A context.WithValue made inside the
// handler could never reach the gate, so CompleteTx and audit.InsertTx set fields of the struct the gate
// already holds. They are set only after the statement succeeded; because the handler's transaction may
// still roll back, the handler clears them on every error path (Reset).
type Marks struct {
	completed atomic.Bool
	audited   atomic.Bool
}

type marksCtx struct{}

// WithMarks installs a fresh Marks; the gate calls it before invoking the handler.
func WithMarks(ctx context.Context) (context.Context, *Marks) {
	m := &Marks{}
	return context.WithValue(ctx, marksCtx{}, m), m
}

// MarksFrom returns the installed Marks, or nil (every method is nil-safe).
func MarksFrom(ctx context.Context) *Marks {
	m, _ := ctx.Value(marksCtx{}).(*Marks)
	return m
}

// Completed reports whether CompleteTx stored the response.
func (m *Marks) Completed() bool { return m != nil && m.completed.Load() }

// Audited reports whether audit.InsertTx wrote the audit row.
func (m *Marks) Audited() bool { return m != nil && m.audited.Load() }

// MarkCompleted is set by CompleteTx.
func (m *Marks) MarkCompleted() {
	if m != nil {
		m.completed.Store(true)
	}
}

// MarkAudited is set by audit.InsertTx.
func (m *Marks) MarkAudited() {
	if m != nil {
		m.audited.Store(true)
	}
}

// Reset clears both marks (deferred rollback of a spend operation).
func (m *Marks) Reset() {
	if m != nil {
		m.completed.Store(false)
		m.audited.Store(false)
	}
}
