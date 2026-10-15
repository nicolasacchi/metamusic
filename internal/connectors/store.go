package connectors

import (
	"context"
	"database/sql"
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

// ErrNotImplemented marks a stub of a later task (the message names it).
var ErrNotImplemented = errors.New("connectors: not implemented")

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

// List returns the grants, newest last_used_at first, then created_at.
// STUB(M4a-C2): the list, the limits write and the verifier land in the connectors lane.
func (s *Store) List(context.Context) ([]Grant, error) {
	return nil, fmt.Errorf("%w: STUB(M4a-C2) List", ErrNotImplemented)
}

// Set changes scopes, limits and the revoked flag of a grant; by must hold scope admin (the gate enforces it).
// STUB(M4a-C2): C103's grant-set SQL with its spend_cap_changes row.
func (s *Store) Set(context.Context, auth.Principal, string, SetIn) (Grant, error) {
	return Grant{}, fmt.Errorf("%w: STUB(M4a-C2) Set", ErrNotImplemented)
}
