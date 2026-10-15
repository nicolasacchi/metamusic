package connectors

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// ErrNotFound is returned by Get and Set for an unknown grant id.
var ErrNotFound = errors.New("connectors: no such grant")

// Limits are a grant's spend limits; a nil field means "the global default" (never $0, C106).
type Limits struct {
	DailyBudgetMicro, AutoApproveMicro *money.MicroUSD
	AutoApproveTakes                   *int
}

// Grant is one connector grant: a (OAuth client, subject) pair and what it may do.
type Grant struct {
	ID, ClientID, Subject, Username, Label string
	Scopes                                 []auth.Scope
	Limits                                 Limits
	CreatedAt, UpdatedAt                   time.Time
	LastUsedAt, RevokedAt                  *time.Time
	UpdatedBy, RevokedBy                   string
}

// SetIn is the input of Set; at least one field must be non-nil.
type SetIn struct {
	Scopes  []auth.Scope
	Limits  *Limits
	Revoked *bool
}

// Store owns the connector_grants table.
type Store struct {
	st    *store.Store
	now   func() time.Time
	newID func() string
}

// NewStore builds the store; a nil now means time.Now and a nil newID a UUIDv7 generator.
func NewStore(st *store.Store, now func() time.Time, newID func() string) *Store {
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func() string { return uuid.NewV7().String() }
	}
	return &Store{st: st, now: now, newID: newID}
}

const grantCols = `id, client_id, subject, username, label, scopes, daily_budget_micro_usd, auto_approve_takes,
	auto_approve_micro_usd, created_at, last_used_at, updated_at, updated_by, revoked_at, revoked_by`

type rowScanner interface{ Scan(dest ...any) error }

func scanGrant(r rowScanner) (Grant, error) {
	var (
		g                          Grant
		scopes, created, updated   string
		daily, takes, autoMicro    sql.NullInt64
		lastUsed, revokedAt, revBy sql.NullString
	)
	if err := r.Scan(&g.ID, &g.ClientID, &g.Subject, &g.Username, &g.Label, &scopes, &daily, &takes, &autoMicro,
		&created, &lastUsed, &updated, &g.UpdatedBy, &revokedAt, &revBy); err != nil {
		return Grant{}, err
	}
	for _, s := range strings.Split(scopes, ",") {
		g.Scopes = append(g.Scopes, auth.Scope(s))
	}
	if daily.Valid {
		m := money.MicroUSD(daily.Int64)
		g.Limits.DailyBudgetMicro = &m
	}
	if autoMicro.Valid {
		m := money.MicroUSD(autoMicro.Int64)
		g.Limits.AutoApproveMicro = &m
	}
	if takes.Valid {
		n := int(takes.Int64)
		g.Limits.AutoApproveTakes = &n
	}
	var err error
	if g.CreatedAt, err = store.ParseTime(created); err != nil {
		return Grant{}, fmt.Errorf("connectors: created_at: %w", err)
	}
	if g.UpdatedAt, err = store.ParseTime(updated); err != nil {
		return Grant{}, fmt.Errorf("connectors: updated_at: %w", err)
	}
	for _, f := range []struct {
		src sql.NullString
		dst **time.Time
	}{{lastUsed, &g.LastUsedAt}, {revokedAt, &g.RevokedAt}} {
		if !f.src.Valid {
			continue
		}
		t, err := store.ParseTime(f.src.String)
		if err != nil {
			return Grant{}, fmt.Errorf("connectors: timestamp: %w", err)
		}
		*f.dst = &t
	}
	g.RevokedBy = revBy.String
	return g, nil
}

// Get returns one grant (ErrNotFound for an unknown id).
func (s *Store) Get(ctx context.Context, id string) (Grant, error) {
	g, err := scanGrant(s.st.R.QueryRowContext(ctx, `SELECT `+grantCols+` FROM connector_grants WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrNotFound
	}
	if err != nil {
		return Grant{}, fmt.Errorf("connectors: get: %w", err)
	}
	return g, nil
}

// Ensure returns the grant of (clientID, subject), creating it with scopes read,write on the first call (C103
// upsert). The SELECT comes first, so newID is called only on a miss; a lost race returns the other writer's
// row with created=false. It is the one insert path (verifier, seed-synthetic, fixtures, parity rig).
func (s *Store) Ensure(ctx context.Context, clientID, subject, username, label string) (g Grant, created bool, err error) {
	if clientID == "" || subject == "" || username == "" || label == "" {
		return Grant{}, false, errors.New("connectors: client, subject, username and label are required")
	}
	const byPair = `SELECT ` + grantCols + ` FROM connector_grants WHERE client_id = ? AND subject = ?`
	g, err = scanGrant(s.st.W.QueryRowContext(ctx, byPair, clientID, subject))
	if err == nil {
		return g, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Grant{}, false, fmt.Errorf("connectors: ensure: %w", err)
	}
	at := store.FormatTime(s.now())
	var id string
	err = s.st.W.QueryRowContext(ctx, `INSERT INTO connector_grants(id, client_id, subject, username, label, scopes, created_at, updated_at, updated_by)
		VALUES(?,?,?,?,?,'read,write',?,?,'system:oauth') ON CONFLICT(client_id, subject) DO NOTHING RETURNING id`,
		s.newID(), clientID, subject, username, label, at, at).Scan(&id)
	switch {
	case err == nil:
		created = true
	case errors.Is(err, sql.ErrNoRows): // lost the race
	default:
		return Grant{}, false, fmt.Errorf("connectors: ensure: %w", err)
	}
	g, err = scanGrant(s.st.W.QueryRowContext(ctx, byPair, clientID, subject))
	if err != nil {
		return Grant{}, false, fmt.Errorf("connectors: ensure: %w", err)
	}
	return g, created, nil
}

// List returns the grants, newest last_used_at first (never-used grants after the used ones), then created_at, id.
func (s *Store) List(ctx context.Context) ([]Grant, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT `+grantCols+` FROM connector_grants
		ORDER BY last_used_at IS NULL, last_used_at DESC, created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("connectors: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Grant{}
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("connectors: list: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("connectors: list: %w", err)
	}
	return out, nil
}

// Touch records a use of the grant, at most once a minute (C103); the caller bounds ctx.
func (s *Store) Touch(ctx context.Context, id string) error {
	now := s.now()
	_, err := s.st.W.ExecContext(ctx, `UPDATE connector_grants SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		store.FormatTime(now), id, store.FormatTime(now.Add(-TouchInterval)))
	if err != nil {
		return fmt.Errorf("connectors: touch: %w", err)
	}
	return nil
}

// TouchInterval is the minimum gap between two last_used_at writes of one grant.
const TouchInterval = time.Minute

// Set validation failures.
var (
	ErrInvalidScopes = errors.New("connectors: scopes must be a unique subset of read, write, generate that contains read")
	ErrInvalidLimits = errors.New("connectors: limit out of range")
	ErrEmptyChange   = errors.New("connectors: nothing to change")
)

const maxLimitMicro = 1_000_000_000

func limitsOK(l Limits) bool {
	okMicro := func(v *money.MicroUSD) bool { return v == nil || (*v >= 0 && *v <= maxLimitMicro) }
	return okMicro(l.DailyBudgetMicro) && okMicro(l.AutoApproveMicro) && (l.AutoApproveTakes == nil || (*l.AutoApproveTakes >= 0 && *l.AutoApproveTakes <= 100))
}

// canonicalScopes validates scopes (read required, no admin, unique) and returns the stored form, which the
// table's CHECK fixes to the order read, write, generate.
func canonicalScopes(in []auth.Scope) (string, error) {
	var read, write, gen bool
	seen := 0
	for _, sc := range in {
		switch sc {
		case auth.ScopeRead:
			read = true
		case auth.ScopeWrite:
			write = true
		case auth.ScopeGenerate:
			gen = true
		default:
			return "", ErrInvalidScopes
		}
		seen++
	}
	if !read || seen != btoi(read)+btoi(write)+btoi(gen) {
		return "", ErrInvalidScopes
	}
	out := "read"
	if write {
		out += ",write"
	}
	if gen {
		out += ",generate"
	}
	return out, nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

type limitsJSON struct {
	Daily            *int64 `json:"daily_budget_micro_usd"`
	AutoApproveTakes *int   `json:"auto_approve_takes"`
	AutoApproveMicro *int64 `json:"auto_approve_micro_usd"`
}

func (l Limits) json() limitsJSON {
	j := limitsJSON{AutoApproveTakes: l.AutoApproveTakes}
	if l.DailyBudgetMicro != nil {
		v := int64(*l.DailyBudgetMicro)
		j.Daily = &v
	}
	if l.AutoApproveMicro != nil {
		v := int64(*l.AutoApproveMicro)
		j.AutoApproveMicro = &v
	}
	return j
}

func nullable[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// Set changes scopes, limits and the revoked flag of a grant in one transaction (C103 grant-set SQL); by must hold
// scope admin (the gate enforces it). A limits change appends one spend_cap_changes row (target connector:<id>)
// with before and after in the same transaction. Revoking an already revoked grant keeps its first revoker.
func (s *Store) Set(ctx context.Context, by auth.Principal, id string, in SetIn) (Grant, error) {
	if in.Scopes == nil && in.Limits == nil && in.Revoked == nil {
		return Grant{}, ErrEmptyChange
	}
	scopes := ""
	if in.Scopes != nil {
		var err error
		if scopes, err = canonicalScopes(in.Scopes); err != nil {
			return Grant{}, err
		}
	}
	if in.Limits != nil && !limitsOK(*in.Limits) {
		return Grant{}, ErrInvalidLimits
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Grant{}, fmt.Errorf("connectors: set: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := scanGrant(tx.QueryRowContext(ctx, `SELECT `+grantCols+` FROM connector_grants WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrNotFound
	}
	if err != nil {
		return Grant{}, fmt.Errorf("connectors: set: %w", err)
	}
	next := cur
	if in.Scopes == nil {
		scopes = joinScopes(cur.Scopes)
	}
	at := store.FormatTime(s.now())
	var revokedAt, revokedBy any
	if cur.RevokedAt != nil {
		revokedAt, revokedBy = store.FormatTime(*cur.RevokedAt), cur.RevokedBy
	}
	if in.Revoked != nil {
		switch {
		case *in.Revoked && cur.RevokedAt == nil:
			revokedAt, revokedBy = at, by.Key()
		case !*in.Revoked:
			revokedAt, revokedBy = nil, nil
		}
	}
	if in.Limits != nil {
		next.Limits = *in.Limits
	}
	if _, err := tx.ExecContext(ctx, `UPDATE connector_grants SET scopes=?, daily_budget_micro_usd=?, auto_approve_takes=?,
		auto_approve_micro_usd=?, revoked_at=?, revoked_by=?, updated_at=?, updated_by=? WHERE id=?`,
		scopes, nullable(next.Limits.DailyBudgetMicro), nullable(next.Limits.AutoApproveTakes), nullable(next.Limits.AutoApproveMicro),
		revokedAt, revokedBy, at, by.Key(), id); err != nil {
		return Grant{}, fmt.Errorf("connectors: set: %w", err)
	}
	if in.Limits != nil && !sameLimits(cur.Limits, next.Limits) {
		before, _ := json.Marshal(cur.Limits.json())
		after, _ := json.Marshal(next.Limits.json())
		if _, err := tx.ExecContext(ctx, `INSERT INTO spend_cap_changes(at, principal_id, target, before_json, after_json) VALUES (?,?,?,?,?)`,
			at, by.Key(), "connector:"+id, string(before), string(after)); err != nil {
			return Grant{}, fmt.Errorf("connectors: record cap change: %w", err)
		}
	}
	out, err := scanGrant(tx.QueryRowContext(ctx, `SELECT `+grantCols+` FROM connector_grants WHERE id = ?`, id))
	if err != nil {
		return Grant{}, fmt.Errorf("connectors: set: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Grant{}, fmt.Errorf("connectors: set: %w", err)
	}
	return out, nil
}

func joinScopes(in []auth.Scope) string {
	parts := make([]string, len(in))
	for i, sc := range in {
		parts[i] = string(sc)
	}
	return strings.Join(parts, ",")
}

func sameLimits(a, b Limits) bool {
	x, y := a.json(), b.json()
	eqI := func(p, q *int64) bool { return (p == nil) == (q == nil) && (p == nil || *p == *q) }
	return eqI(x.Daily, y.Daily) && eqI(x.AutoApproveMicro, y.AutoApproveMicro) &&
		(x.AutoApproveTakes == nil) == (y.AutoApproveTakes == nil) && (x.AutoApproveTakes == nil || *x.AutoApproveTakes == *y.AutoApproveTakes)
}
