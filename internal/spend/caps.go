package spend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Caps are the global spend limits (C72.3). Every amount is integer micro-USD.
type Caps struct {
	MonthlyMicro, BatchMicro, HumanDailyMicro, AgentDailyMicro money.MicroUSD
	AutoApproveTakes                                           int
	AutoApproveMicro                                           money.MicroUSD
}

// DefaultCaps are the migration defaults (D-M3-7): $10 a month, $5 a batch, $5 a day for the operator,
// $1 a day for agents, auto-approve up to 5 takes and $0.50.
var DefaultCaps = Caps{MonthlyMicro: 10_000_000, BatchMicro: 5_000_000, HumanDailyMicro: 5_000_000, AgentDailyMicro: 1_000_000,
	AutoApproveTakes: 5, AutoApproveMicro: 500_000}

// MaxCapMicro is the largest cap a caller may set ($1000).
const MaxCapMicro money.MicroUSD = 1_000_000_000

// TokenLimits are an agent token's own limits (api_tokens columns); nil means the global value.
type TokenLimits struct {
	DailyMicro, AutoApproveMicro *money.MicroUSD
	AutoApproveTakes             *int
}

// Cap names reported by CapError.
const (
	CapBatch        = "batch"
	CapMonthly      = "monthly"
	CapDaily        = "daily"
	CapBatchCeiling = "batch_ceiling"
)

// CapError is a refused reserve; the API maps it to 402 BUDGET_EXCEEDED.
type CapError struct {
	Cap                    string
	Limit, Used, Requested money.MicroUSD
}

// Error implements error.
func (e *CapError) Error() string {
	return fmt.Sprintf("spend: %s cap exceeded (limit %d, used %d, requested %d micro-USD)", e.Cap, e.Limit, e.Used, e.Requested)
}

// Details is the problem body's details object (C72.3).
func (e *CapError) Details() map[string]any {
	return map[string]any{
		"cap": e.Cap, "limit_micro_usd": int64(e.Limit), "used_micro_usd": int64(e.Used), "requested_micro_usd": int64(e.Requested),
		"limit_usd": e.Limit.USD(), "used_usd": e.Used.USD(), "requested_usd": e.Requested.USD(),
	}
}

// PrincipalDaily is the daily limit that applies to p: HumanDailyMicro for everyone but agents, the
// token's own limit or AgentDailyMicro for agents.
func PrincipalDaily(p auth.Principal, tok TokenLimits, c Caps) money.MicroUSD {
	if p.Kind != auth.KindAgent {
		return c.HumanDailyMicro
	}
	return agentDaily(tok, c)
}

func agentDaily(tok TokenLimits, c Caps) money.MicroUSD {
	if tok.DailyMicro != nil {
		return *tok.DailyMicro
	}
	return c.AgentDailyMicro
}

// CheckCaps checks a create (C72.3): ceiling against the batch cap, then the month, then the
// principal's day, each with newReserve added. First failure wins; exactly equal fits. nil means fits.
func CheckCaps(u Usage, c Caps, principalDaily, ceiling, newReserve money.MicroUSD) *CapError {
	if ceiling > c.BatchMicro {
		return &CapError{Cap: CapBatch, Limit: c.BatchMicro, Requested: ceiling}
	}
	return checkMonthDay(u, c, principalDaily, newReserve)
}

func checkMonthDay(u Usage, c Caps, principalDaily, newReserve money.MicroUSD) *CapError {
	if used := u.MonthSettled + u.Outstanding; used+newReserve > c.MonthlyMicro {
		return &CapError{Cap: CapMonthly, Limit: c.MonthlyMicro, Used: used, Requested: newReserve}
	}
	if used := u.PrincipalDaySettled + u.PrincipalOutstanding; used+newReserve > principalDaily {
		return &CapError{Cap: CapDaily, Limit: principalDaily, Used: used, Requested: newReserve}
	}
	return nil
}

// LoadCaps reads the singleton caps row through q.
func (s *Service) LoadCaps(ctx context.Context, q Querier) (Caps, error) {
	var c Caps
	var mo, ba, hd, ad, am int64
	err := q.QueryRowContext(ctx, `SELECT monthly_micro_usd, batch_micro_usd, human_daily_micro_usd, agent_daily_micro_usd,
		auto_approve_takes, auto_approve_micro_usd FROM spend_caps WHERE id = 1`).Scan(&mo, &ba, &hd, &ad, &c.AutoApproveTakes, &am)
	if err != nil {
		return c, fmt.Errorf("spend: load caps: %w", err)
	}
	c.MonthlyMicro, c.BatchMicro, c.HumanDailyMicro, c.AgentDailyMicro, c.AutoApproveMicro =
		money.MicroUSD(mo), money.MicroUSD(ba), money.MicroUSD(hd), money.MicroUSD(ad), money.MicroUSD(am)
	return c, nil
}

// TokenLimitsFor reads an agent token's limits; a principal key that is not an agent returns zero
// limits (all global).
func (s *Service) TokenLimitsFor(ctx context.Context, q Querier, principalKey string) (TokenLimits, error) {
	id, ok := strings.CutPrefix(principalKey, "agent:")
	if !ok {
		return TokenLimits{}, nil
	}
	return s.tokenLimits(ctx, q, id)
}

func (s *Service) tokenLimits(ctx context.Context, q Querier, tokenID string) (TokenLimits, error) {
	var d, am sql.NullInt64
	var tk sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT daily_budget_micro_usd, auto_approve_takes, auto_approve_micro_usd FROM api_tokens WHERE id = ?`, tokenID).
		Scan(&d, &tk, &am)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenLimits{}, nil
	}
	if err != nil {
		return TokenLimits{}, fmt.Errorf("spend: token limits: %w", err)
	}
	var t TokenLimits
	if d.Valid {
		v := money.MicroUSD(d.Int64)
		t.DailyMicro = &v
	}
	if am.Valid {
		v := money.MicroUSD(am.Int64)
		t.AutoApproveMicro = &v
	}
	if tk.Valid {
		v := int(tk.Int64)
		t.AutoApproveTakes = &v
	}
	return t, nil
}

// TokenPatch replaces one agent token's limits; a nil field clears the limit (back to the global).
type TokenPatch struct {
	TokenID                      string
	DailyMicro, AutoApproveMicro *money.MicroUSD
	AutoApproveTakes             *int
}

// CapsPatch is a set_spend_caps request: nil global fields stay as they are.
type CapsPatch struct {
	MonthlyMicro, BatchMicro, HumanDailyMicro, AgentDailyMicro, AutoApproveMicro *money.MicroUSD
	AutoApproveTakes                                                             *int
	Tokens                                                                       []TokenPatch
}

// ErrCapRange means an amount outside 0..MaxCapMicro or takes outside 0..100.
var ErrCapRange = errors.New("spend: cap out of range")

// UnknownTokenError names a token id that does not exist.
type UnknownTokenError struct{ TokenID string }

// Error implements error.
func (e *UnknownTokenError) Error() string { return "spend: unknown token " + e.TokenID }

// CapChange is one recorded change (a spend_cap_changes row).
type CapChange struct {
	Target        string // "global" | "token:<id>"
	Before, After json.RawMessage
}

type globalJSON struct {
	Monthly          int64 `json:"monthly_micro_usd"`
	Batch            int64 `json:"batch_micro_usd"`
	HumanDaily       int64 `json:"human_daily_micro_usd"`
	AgentDaily       int64 `json:"agent_daily_micro_usd"`
	AutoApproveTakes int   `json:"auto_approve_takes"`
	AutoApproveMicro int64 `json:"auto_approve_micro_usd"`
}

type tokenJSON struct {
	Daily            *int64 `json:"daily_budget_micro_usd"`
	AutoApproveTakes *int   `json:"auto_approve_takes"`
	AutoApproveMicro *int64 `json:"auto_approve_micro_usd"`
}

func (c Caps) json() globalJSON {
	return globalJSON{int64(c.MonthlyMicro), int64(c.BatchMicro), int64(c.HumanDailyMicro), int64(c.AgentDailyMicro), c.AutoApproveTakes, int64(c.AutoApproveMicro)}
}

func (t TokenLimits) json() tokenJSON {
	var j tokenJSON
	if t.DailyMicro != nil {
		v := int64(*t.DailyMicro)
		j.Daily = &v
	}
	if t.AutoApproveMicro != nil {
		v := int64(*t.AutoApproveMicro)
		j.AutoApproveMicro = &v
	}
	j.AutoApproveTakes = t.AutoApproveTakes
	return j
}

func okMicro(v *money.MicroUSD) bool { return v == nil || (*v >= 0 && *v <= MaxCapMicro) }
func okTakes(v *int) bool            { return v == nil || (*v >= 0 && *v <= 100) }

// SetCaps applies a patch in the caller's transaction and appends one spend_cap_changes row per
// changed target (global and each token) with before and after. It never checks usage: lowering a cap
// only affects new reserves (C72.3). The new global caps are returned.
func (s *Service) SetCaps(ctx context.Context, tx *sql.Tx, by auth.Principal, patch CapsPatch) (Caps, []CapChange, error) {
	for _, v := range []*money.MicroUSD{patch.MonthlyMicro, patch.BatchMicro, patch.HumanDailyMicro, patch.AgentDailyMicro, patch.AutoApproveMicro} {
		if !okMicro(v) {
			return Caps{}, nil, ErrCapRange
		}
	}
	if !okTakes(patch.AutoApproveTakes) {
		return Caps{}, nil, ErrCapRange
	}
	for _, t := range patch.Tokens {
		if !okMicro(t.DailyMicro) || !okMicro(t.AutoApproveMicro) || !okTakes(t.AutoApproveTakes) {
			return Caps{}, nil, ErrCapRange
		}
	}
	cur, err := s.LoadCaps(ctx, tx)
	if err != nil {
		return cur, nil, err
	}
	next := cur
	set := func(dst *money.MicroUSD, v *money.MicroUSD) {
		if v != nil {
			*dst = *v
		}
	}
	set(&next.MonthlyMicro, patch.MonthlyMicro)
	set(&next.BatchMicro, patch.BatchMicro)
	set(&next.HumanDailyMicro, patch.HumanDailyMicro)
	set(&next.AgentDailyMicro, patch.AgentDailyMicro)
	set(&next.AutoApproveMicro, patch.AutoApproveMicro)
	if patch.AutoApproveTakes != nil {
		next.AutoApproveTakes = *patch.AutoApproveTakes
	}
	now := store.FormatTime(s.now())
	var changes []CapChange
	record := func(target string, before, after any) error {
		b, _ := json.Marshal(before)
		a, _ := json.Marshal(after)
		if _, err := tx.ExecContext(ctx, `INSERT INTO spend_cap_changes(at, principal_id, target, before_json, after_json) VALUES (?,?,?,?,?)`,
			now, by.Key(), target, string(b), string(a)); err != nil {
			return fmt.Errorf("spend: record cap change: %w", err)
		}
		changes = append(changes, CapChange{Target: target, Before: b, After: a})
		return nil
	}
	if next != cur {
		if _, err := tx.ExecContext(ctx, `UPDATE spend_caps SET monthly_micro_usd=?, batch_micro_usd=?, human_daily_micro_usd=?,
			agent_daily_micro_usd=?, auto_approve_takes=?, auto_approve_micro_usd=?, updated_by=?, updated_at=? WHERE id = 1`,
			int64(next.MonthlyMicro), int64(next.BatchMicro), int64(next.HumanDailyMicro), int64(next.AgentDailyMicro),
			next.AutoApproveTakes, int64(next.AutoApproveMicro), by.Key(), now); err != nil {
			return cur, nil, fmt.Errorf("spend: update caps: %w", err)
		}
		if err := record("global", cur.json(), next.json()); err != nil {
			return cur, nil, err
		}
	}
	seen := map[string]bool{}
	for _, t := range patch.Tokens {
		if seen[t.TokenID] {
			continue
		}
		seen[t.TokenID] = true
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM api_tokens WHERE id = ?`, t.TokenID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return cur, nil, &UnknownTokenError{TokenID: t.TokenID}
		} else if err != nil {
			return cur, nil, err
		}
		before, err := s.tokenLimits(ctx, tx, t.TokenID)
		if err != nil {
			return cur, nil, err
		}
		after := TokenLimits{DailyMicro: t.DailyMicro, AutoApproveMicro: t.AutoApproveMicro, AutoApproveTakes: t.AutoApproveTakes}
		if sameToken(before, after) {
			continue
		}
		var d, am, tk any
		if t.DailyMicro != nil {
			d = int64(*t.DailyMicro)
		}
		if t.AutoApproveMicro != nil {
			am = int64(*t.AutoApproveMicro)
		}
		if t.AutoApproveTakes != nil {
			tk = *t.AutoApproveTakes
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET daily_budget_micro_usd=?, auto_approve_takes=?, auto_approve_micro_usd=? WHERE id=?`,
			d, tk, am, t.TokenID); err != nil {
			return cur, nil, fmt.Errorf("spend: update token limits: %w", err)
		}
		if err := record("token:"+t.TokenID, before.json(), after.json()); err != nil {
			return cur, nil, err
		}
	}
	return next, changes, nil
}

func sameToken(a, b TokenLimits) bool {
	eq := func(x, y *int64) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	aj, bj := a.json(), b.json()
	return eq(aj.Daily, bj.Daily) && eq(aj.AutoApproveMicro, bj.AutoApproveMicro) &&
		(aj.AutoApproveTakes == nil) == (bj.AutoApproveTakes == nil) && (aj.AutoApproveTakes == nil || *aj.AutoApproveTakes == *bj.AutoApproveTakes)
}
