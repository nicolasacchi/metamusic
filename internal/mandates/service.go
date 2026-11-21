package mandates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engineadopt"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Canceller records a cancel intent on every non-terminal job of the batches inside tx (jobs.Service.CancelIntentsTx). It is
// injected so this package never imports jobs.
type Canceller func(ctx context.Context, tx *sql.Tx, batchIDs []string, by string) error

// RevokeBy is the decider recorded on the jobs a revoke cancels.
const RevokeBy = "system:mandate-revoke"

// Options builds a Service.
type Options struct {
	Store     *store.Store
	Spend     *spend.Service
	Adoption  *engineadopt.Service // nil: only yue2 counts as adopted
	Canceller Canceller            // nil: a revoke cancels no job (tests that have none)
	Now       func() time.Time     // nil => time.Now
	NewID     func() string        // nil => UUIDv7
	Wake      func()               // called after a revoke commits so the runner applies the cancel intents
}

// Service owns every SQL statement on mandates.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	return &Service{o: o}
}

// Querier is the read side (*sql.DB and *sql.Tx satisfy it).
type Querier = interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// CreateInput is the body of create_mandate.
type CreateInput struct {
	Holder     string
	Title      string
	Budget     money.MicroUSD
	ValidFrom  *time.Time // nil: now
	ValidUntil time.Time
	Scope      Scope
}

func notFound() *problem.Error { return problem.E(404, "NOT_FOUND", "no such mandate", nil) }

func ms(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

// Create stores a mandate (C369.1). Only an Owner who is a person may: the registry's HumanOnly and OwnerOnly flags refuse every
// other caller first, and this guard repeats it (an agent must never create or widen a mandate for itself). The one-active-per-holder
// check, the scope's cross-checks and the insert run in one transaction, so two creates cannot both pass.
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateInput) (Mandate, error) {
	if err := ownerPerson(p); err != nil {
		return Mandate{}, err
	}
	now := ms(s.o.Now())
	until := ms(in.ValidUntil)
	var from *time.Time
	if in.ValidFrom != nil {
		f := ms(*in.ValidFrom)
		from = &f
	}
	holderKey, vf, err := ValidateHeader(in.Holder, in.Title, in.Budget, from, until, now)
	if err != nil {
		return Mandate{}, err
	}
	scopeJSON, err := in.Scope.JSON()
	if err != nil {
		return Mandate{}, err
	}
	var m Mandate
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		if cur, err := s.active(ctx, tx, holderKey, now); err != nil {
			return err
		} else if cur != nil {
			return problem.E(409, "MANDATE_ACTIVE", "the holder already has a mandate that is scheduled or live", map[string]any{"mandate_id": cur.ID})
		}
		env, err := s.env(ctx, tx)
		if err != nil {
			return err
		}
		if err := ValidateScope(in.Scope, in.Budget, env); err != nil {
			return err
		}
		m = Mandate{ID: s.o.NewID(), HolderKey: holderKey, Title: in.Title, Budget: in.Budget, ValidFrom: vf, ValidUntil: until,
			Scope: in.Scope, State: StateApproved, CreatedBy: p.Key(), CreatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mandates(id, holder_key, title, budget_micro_usd, valid_from, valid_until, scope_json, state, created_by, created_at)
			VALUES(?,?,?,?,?,?,?,'approved',?,?)`, m.ID, m.HolderKey, m.Title, int64(m.Budget), store.FormatTime(m.ValidFrom), store.FormatTime(m.ValidUntil),
			scopeJSON, m.CreatedBy, store.FormatTime(m.CreatedAt)); err != nil {
			return fmt.Errorf("mandates: insert: %w", err)
		}
		return s.completeAudit(ctx, tx, 201, m.Budget, []string{m.ID})
	})
	return m, err
}

func ownerPerson(p auth.Principal) error {
	switch {
	case p.IsOwner():
		return nil
	case p.IsPerson() && !p.IsOwnerSide():
		return problem.OwnerOnly("mandates")
	}
	return problem.E(403, "HUMAN_ONLY", "only the Owner in the app creates a mandate", nil)
}

// env builds the validation environment through q: the global batch cap, adoption and the existing roots.
func (s *Service) env(ctx context.Context, q Querier) (Env, error) {
	env := Env{}
	if s.o.Spend != nil {
		caps, err := s.o.Spend.LoadCaps(ctx, q)
		if err != nil {
			return env, err
		}
		env.BatchCap = caps.BatchMicro
	}
	if s.o.Adoption != nil {
		env.Adopted = func(engine string) (bool, error) {
			st, err := s.o.Adoption.State(ctx, q, engine)
			return st.Adopted(), err
		}
	}
	env.RootsOK = func(ids []string) (string, error) {
		for _, id := range ids {
			var n int
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE id = ? AND root_track_id = id`, id).Scan(&n); err != nil {
				return "", err
			}
			if n == 0 {
				return id, nil
			}
		}
		return "", nil
	}
	return env, nil
}

const mandateCols = `id, holder_key, title, budget_micro_usd, valid_from, valid_until, scope_json, state, created_by, created_at,
	revoked_by, revoked_at, revoke_reason`

func scan(row interface{ Scan(...any) error }) (Mandate, error) {
	var m Mandate
	var budget int64
	var vf, vu, scope, ca string
	var rb, ra, rr sql.NullString
	if err := row.Scan(&m.ID, &m.HolderKey, &m.Title, &budget, &vf, &vu, &scope, &m.State, &m.CreatedBy, &ca, &rb, &ra, &rr); err != nil {
		return Mandate{}, err
	}
	m.Budget = money.MicroUSD(budget)
	var err error
	if m.ValidFrom, err = store.ParseTime(vf); err != nil {
		return Mandate{}, err
	}
	if m.ValidUntil, err = store.ParseTime(vu); err != nil {
		return Mandate{}, err
	}
	if m.CreatedAt, err = store.ParseTime(ca); err != nil {
		return Mandate{}, err
	}
	if m.Scope, err = ParseScope(scope); err != nil {
		return Mandate{}, err
	}
	if rb.Valid {
		m.RevokedBy, m.RevokeReason = &rb.String, &rr.String
		t, err := store.ParseTime(ra.String)
		if err != nil {
			return Mandate{}, err
		}
		m.RevokedAt = &t
	}
	return m, nil
}

// Get reads one mandate; 404 NOT_FOUND when there is none.
func (s *Service) Get(ctx context.Context, id string) (Mandate, error) {
	return s.get(ctx, s.o.Store.R, id)
}

func (s *Service) get(ctx context.Context, q Querier, id string) (Mandate, error) {
	m, err := scan(q.QueryRowContext(ctx, `SELECT `+mandateCols+` FROM mandates WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Mandate{}, notFound()
	}
	return m, err
}

// Usage is the usage of one mandate (C367) read through the service's reader.
func (s *Service) Usage(ctx context.Context, id string) (spend.MandateUsage, error) {
	if s.o.Spend == nil {
		return spend.MandateUsage{}, errors.ErrUnsupported
	}
	return s.o.Spend.MandateUsage(ctx, s.o.Store.R, id)
}

// Location is the zone "per day" is counted in: the spend Location, so the key of coverage and the key of usage agree.
func (s *Service) Location() *time.Location {
	if s.o.Spend == nil {
		return time.Local
	}
	return s.o.Spend.Location()
}

// Filter narrows List.
type Filter struct {
	State           string   // stored state: approved | revoked
	EffectiveStates []string // scheduled | live | ended | revoked (explode)
	Holder          string   // wire name
	Limit           int      // 1..100, default 50
	Cursor          string
}

// Page is one page of List, newest first.
type Page struct {
	Items      []Mandate
	NextCursor string
}

// List pages mandates newest first (ids are UUIDv7). GET never writes.
func (s *Service) List(ctx context.Context, f Filter) (Page, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		return Page{}, problem.E(422, "VALIDATION_FAILED", "limit: at most 100", map[string]any{"field": "limit"})
	}
	var where []string
	var args []any
	if f.State != "" {
		if f.State != StateApproved && f.State != StateRevoked {
			return Page{}, problem.E(422, "VALIDATION_FAILED", "state: approved or revoked", map[string]any{"field": "state"})
		}
		where, args = append(where, "state = ?"), append(args, f.State)
	}
	if f.Holder != "" {
		key, ok := Holders[f.Holder]
		if !ok {
			return Page{}, problem.E(422, "VALIDATION_FAILED", "holder: unknown holder", map[string]any{"field": "holder"})
		}
		where, args = append(where, "holder_key = ?"), append(args, key)
	}
	if len(f.EffectiveStates) > 0 {
		now := store.FormatTime(s.o.Now())
		var alts []string
		for _, es := range f.EffectiveStates {
			switch es {
			case EffRevoked:
				alts = append(alts, "state = 'revoked'")
			case EffScheduled:
				alts, args = append(alts, "(state = 'approved' AND valid_from > ?)"), append(args, now)
			case EffLive:
				alts, args = append(alts, "(state = 'approved' AND valid_from <= ? AND valid_until > ?)"), append(args, now, now)
			case EffEnded:
				alts, args = append(alts, "(state = 'approved' AND valid_until <= ?)"), append(args, now)
			default:
				return Page{}, problem.E(422, "VALIDATION_FAILED", "effective_state: scheduled, live, ended or revoked", map[string]any{"field": "effective_state"})
			}
		}
		where = append(where, "("+strings.Join(alts, " OR ")+")")
	}
	if f.Cursor != "" {
		where, args = append(where, "id < ?"), append(args, f.Cursor)
	}
	q := `SELECT ` + mandateCols + ` FROM mandates`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ") //nolint:gosec // G202: fixed fragments built above, every value is bound
	}
	q += ` ORDER BY id DESC LIMIT ?`
	rows, err := s.o.Store.R.QueryContext(ctx, q, append(args, limit+1)...) //nolint:gosec // G202: fixed fragments, every value is bound
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	var pg Page
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return Page{}, err
		}
		pg.Items = append(pg.Items, m)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(pg.Items) > limit {
		pg.Items = pg.Items[:limit]
		pg.NextCursor = pg.Items[limit-1].ID
	}
	return pg, nil
}

// active is the holder's mandate that is scheduled or live at now (the one-active rule, C366.4), or nil.
func (s *Service) active(ctx context.Context, q Querier, holderKey string, now time.Time) (*Mandate, error) {
	m, err := scan(q.QueryRowContext(ctx, `SELECT `+mandateCols+` FROM mandates WHERE holder_key = ? AND state = 'approved' AND valid_until > ?
		ORDER BY valid_from, id LIMIT 1`, holderKey, store.FormatTime(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Active is the holder's scheduled or live mandate, nil when there is none (the night shift tells "scheduled" from "none").
func (s *Service) Active(ctx context.Context, q Querier, holderKey string) (*Mandate, error) {
	return s.active(ctx, q, holderKey, s.o.Now())
}

// Live is the holder's live mandate with its usage, nil when none is live (a scheduled one is not).
func (s *Service) Live(ctx context.Context, q Querier, holderKey string) (*Mandate, spend.MandateUsage, error) {
	now := s.o.Now()
	m, err := s.active(ctx, q, holderKey, now)
	if err != nil || m == nil || m.EffectiveState(now) != EffLive {
		return nil, spend.MandateUsage{}, err
	}
	if s.o.Spend == nil {
		return nil, spend.MandateUsage{}, errors.ErrUnsupported
	}
	u, err := s.o.Spend.MandateUsage(ctx, q, m.ID)
	if err != nil {
		return nil, spend.MandateUsage{}, err
	}
	return m, u, nil
}

// Revoke ends a mandate (C369.4): state revoked, and a cancel intent on every non-terminal job of its batches, in one transaction,
// then the runner is woken. It only reduces spend, so every Owner-side principal may (the Owner, the Owner's devices, agents); an
// Editor or Listener may not. A revoked or ended mandate is a 409 INVALID_TRANSITION.
func (s *Service) Revoke(ctx context.Context, p auth.Principal, id, reason string) (Mandate, error) {
	if !p.IsOwnerSide() {
		return Mandate{}, problem.OwnerOnly("mandates")
	}
	if n := len([]rune(reason)); n < 1 || n > MaxReason {
		return Mandate{}, problem.E(422, "VALIDATION_FAILED", "reason: 1 to 500 characters", map[string]any{"field": "reason"})
	}
	var m Mandate
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		cur, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		now := ms(s.o.Now())
		if eff := cur.EffectiveState(now); eff != EffLive && eff != EffScheduled {
			return problem.E(409, "INVALID_TRANSITION", "the mandate is already "+eff, map[string]any{"state": eff})
		}
		res, err := tx.ExecContext(ctx, `UPDATE mandates SET state = 'revoked', revoked_by = ?, revoked_at = ?, revoke_reason = ? WHERE id = ? AND state = 'approved'`,
			p.Key(), store.FormatTime(now), reason, id)
		if err != nil {
			return fmt.Errorf("mandates: revoke: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return problem.E(409, "INVALID_TRANSITION", "the mandate is already revoked", nil)
		}
		if s.o.Canceller != nil {
			ids, err := batchIDs(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := s.o.Canceller(ctx, tx, ids, RevokeBy); err != nil {
				return fmt.Errorf("mandates: cancel: %w", err)
			}
		}
		if m, err = s.get(ctx, tx, id); err != nil {
			return err
		}
		return s.completeAudit(ctx, tx, 200, 0, nil)
	})
	if err != nil {
		return Mandate{}, err
	}
	if s.o.Wake != nil {
		s.o.Wake()
	}
	return m, nil
}

func batchIDs(ctx context.Context, q Querier, mandateID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM batches WHERE mandate_id = ? ORDER BY created_at, id`, mandateID)
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

// completeAudit writes the gate's precomputed audit row inside tx, so the row commits with the change (none when called outside the gate).
func (s *Service) completeAudit(ctx context.Context, tx *sql.Tx, status int, cost money.MicroUSD, created []string) error {
	e, ok := audit.PendingFrom(ctx)
	if !ok {
		return nil
	}
	e.Status = status
	e.Duration = max(0, s.o.Now().Sub(e.At))
	e.CostMicroUSD = cost
	e.CreatedIDs = created
	return audit.InsertTx(ctx, tx, e)
}

func (s *Service) writeTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		idem.MarksFrom(ctx).Reset() // the audit row rolled back with the rest: the gate writes its own
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
