package generation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// maxPageBytes caps the serialized size of any list page (M1 C17); each package keeps its own constant.
const maxPageBytes = 90000

// actorFor builds the reference of a stored principal key: the kind is the key's prefix, the name the
// token name for an agent and the rest of the key otherwise.
func actorFor(ctx context.Context, q lineage.Querier, key string) Actor {
	kind, rest, _ := strings.Cut(key, ":")
	a := Actor{Key: key, Kind: kind, Name: rest}
	if kind == "agent" {
		var n string
		if q.QueryRowContext(ctx, `SELECT name FROM api_tokens WHERE id = ?`, rest).Scan(&n) == nil {
			a.Name = n
		}
	}
	if kind == "human" {
		a.Name = rest
	}
	return a
}

// TokenCaps is one agent token's limits.
type TokenCaps struct {
	TokenID, Name string
	Limits        spend.TokenLimits
}

// CapsView is the cap configuration; a human sees every token, an agent its own.
type CapsView struct {
	Caps      spend.Caps
	UpdatedBy Actor
	UpdatedAt time.Time
	Tokens    []TokenCaps
}

func (s *Service) capsView(ctx context.Context, q lineage.Querier, p auth.Principal) (CapsView, error) {
	var v CapsView
	var err error
	if v.Caps, err = s.o.Spend.LoadCaps(ctx, q); err != nil {
		return v, err
	}
	var by, at string
	if err := q.QueryRowContext(ctx, `SELECT updated_by, updated_at FROM spend_caps WHERE id = 1`).Scan(&by, &at); err != nil {
		return v, fmt.Errorf("generation: caps: %w", err)
	}
	v.UpdatedBy, v.UpdatedAt = actorFor(ctx, q, by), parseT(at)
	query := `SELECT id, name, daily_budget_micro_usd, auto_approve_takes, auto_approve_micro_usd FROM api_tokens WHERE revoked_at IS NULL`
	args := []any{}
	if p.Kind == auth.KindAgent {
		query += ` AND id = ?`
		args = append(args, p.ID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY created_at, id`, args...)
	if err != nil {
		return v, fmt.Errorf("generation: caps tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	v.Tokens = []TokenCaps{}
	for rows.Next() {
		var t TokenCaps
		var d, tk, am sql.NullInt64
		if err := rows.Scan(&t.TokenID, &t.Name, &d, &tk, &am); err != nil {
			return v, err
		}
		if d.Valid {
			x := money.MicroUSD(d.Int64)
			t.Limits.DailyMicro = &x
		}
		if am.Valid {
			x := money.MicroUSD(am.Int64)
			t.Limits.AutoApproveMicro = &x
		}
		if tk.Valid {
			x := int(tk.Int64)
			t.Limits.AutoApproveTakes = &x
		}
		v.Tokens = append(v.Tokens, t)
	}
	return v, rows.Err()
}

// MonthView is the current calendar month.
type MonthView struct {
	Start                                 time.Time
	Settled, BillAdjustments, Outstanding money.MicroUSD
	Cap, Left                             money.MicroUSD
}

// DayView is today's use by the caller.
type DayView struct {
	Principal                       Actor
	Settled, Outstanding, Cap, Left money.MicroUSD
}

// BudgetView is what the caller may still spend.
type BudgetView struct {
	Caps             CapsView
	Month            MonthView
	Day              DayView
	AutoApproveTakes int
	AutoApprove      money.MicroUSD
	Provider         provider.State
}

func (s *Service) billAdjustments(ctx context.Context, q lineage.Querier, from, to time.Time) (money.MicroUSD, error) {
	var v int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE kind = 'bill' AND window_start >= ? AND window_start < ?`,
		store.FormatTime(from), store.FormatTime(to)).Scan(&v); err != nil {
		return 0, fmt.Errorf("generation: bill adjustments: %w", err)
	}
	return money.MicroUSD(v), nil
}

// Budget reads the caps, this month and today, the caller's effective auto-approve limits and the provider
// state (C81 Budget). An agent sees its own token limits only.
func (s *Service) Budget(ctx context.Context, p auth.Principal) (BudgetView, error) {
	if err := s.ready(); err != nil {
		return BudgetView{}, err
	}
	q := s.o.Store.R
	var v BudgetView
	var err error
	if v.Caps, err = s.capsView(ctx, q, p); err != nil {
		return v, err
	}
	tok, err := s.o.Spend.TokenLimitsFor(ctx, q, p.Key())
	if err != nil {
		return v, err
	}
	u, err := s.o.Spend.Usage(ctx, q, p.Key(), s.o.Now())
	if err != nil {
		return v, err
	}
	m1 := u.MonthStart.AddDate(0, 1, 0)
	bills, err := s.billAdjustments(ctx, q, u.MonthStart, m1)
	if err != nil {
		return v, err
	}
	caps := v.Caps.Caps
	v.Month = MonthView{Start: u.MonthStart, Settled: u.MonthSettled - bills, BillAdjustments: bills, Outstanding: u.Outstanding, Cap: caps.MonthlyMicro,
		Left: max(caps.MonthlyMicro-u.MonthSettled-u.Outstanding, 0)}
	daily := spend.PrincipalDaily(p, tok, caps)
	v.Day = DayView{Principal: actorFor(ctx, q, p.Key()), Settled: u.PrincipalDaySettled, Outstanding: u.PrincipalOutstanding, Cap: daily,
		Left: max(daily-u.PrincipalDaySettled-u.PrincipalOutstanding, 0)}
	if p.Kind != auth.KindAgent {
		v.Day.Principal = Actor{Key: p.Key(), Kind: string(p.Kind), Name: p.Name}
	}
	v.AutoApproveTakes, v.AutoApprove = caps.AutoApproveTakes, caps.AutoApproveMicro
	if tok.AutoApproveTakes != nil {
		v.AutoApproveTakes = *tok.AutoApproveTakes
	}
	if tok.AutoApproveMicro != nil {
		v.AutoApprove = *tok.AutoApproveMicro
	}
	v.Provider, err = s.o.Gate.State(ctx)
	return v, err
}

// SpendDay is one day's settled spend.
type SpendDay struct {
	Date    string
	Settled money.MicroUSD
}

// SpendBatch is one batch of the month.
type SpendBatch struct {
	ID, State     string
	CreatedAt     time.Time
	CreatedBy     Actor
	Takes         int
	Expected, Max money.MicroUSD
	Settled       money.MicroUSD
	Reconciled    bool
}

// SpendView is a month's spend report.
type SpendView struct {
	Month                                 string
	Days                                  []SpendDay
	Batches                               []SpendBatch
	Settled, BillAdjustments, Outstanding money.MicroUSD
}

// SpendReport reports a month ("YYYY-MM"; empty means the current one): settled spend per day and per
// batch, the bill adjustments of the month and what is still outstanding.
func (s *Service) SpendReport(ctx context.Context, month string) (SpendView, error) {
	if err := s.ready(); err != nil {
		return SpendView{}, err
	}
	q := s.o.Store.R
	u, err := s.o.Spend.Usage(ctx, q, "", s.o.Now())
	if err != nil {
		return SpendView{}, err
	}
	loc := u.MonthStart.Location()
	m0 := u.MonthStart
	if month != "" {
		t, err := time.ParseInLocation("2006-01", month, loc)
		if err != nil {
			return SpendView{}, perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "month must be YYYY-MM", map[string]any{"field": "month"})
		}
		m0 = t
	}
	m1 := m0.AddDate(0, 1, 0)
	v := SpendView{Month: m0.Format("2006-01"), Days: []SpendDay{}, Batches: []SpendBatch{}, Outstanding: u.Outstanding}
	from, to := store.FormatTime(m0), store.FormatTime(m1)
	rows, err := q.QueryContext(ctx, `SELECT at, micro_usd FROM ledger WHERE kind = 'settle' AND at >= ? AND at < ? ORDER BY at, id`, from, to)
	if err != nil {
		return v, fmt.Errorf("generation: spend days: %w", err)
	}
	byDay := map[string]money.MicroUSD{}
	var order []string
	for rows.Next() {
		var at string
		var m int64
		if err := rows.Scan(&at, &m); err != nil {
			_ = rows.Close()
			return v, err
		}
		d := parseT(at).In(loc).Format("2006-01-02")
		if _, ok := byDay[d]; !ok {
			order = append(order, d)
		}
		byDay[d] += money.MicroUSD(m)
		v.Settled += money.MicroUSD(m)
	}
	if err := rows.Close(); err != nil {
		return v, err
	}
	for _, d := range order {
		v.Days = append(v.Days, SpendDay{Date: d, Settled: byDay[d]})
	}
	if v.BillAdjustments, err = s.billAdjustments(ctx, q, m0, m1); err != nil {
		return v, err
	}
	brows, err := q.QueryContext(ctx, `SELECT b.id, b.created_at, b.principal_id, b.n_takes, b.state, b.expected_micro_usd, b.max_micro_usd, b.reconciled_at IS NOT NULL,
		COALESCE((SELECT SUM(l.micro_usd) FROM ledger l WHERE l.batch_id = b.id AND l.kind = 'settle'), 0)
		FROM batches b WHERE b.created_at >= ? AND b.created_at < ? ORDER BY b.created_at, b.id`, from, to)
	if err != nil {
		return v, fmt.Errorf("generation: spend batches: %w", err)
	}
	var pending []SpendBatch
	for brows.Next() {
		var b SpendBatch
		var created, owner string
		var exp, mx, set int64
		if err := brows.Scan(&b.ID, &created, &owner, &b.Takes, &b.State, &exp, &mx, &b.Reconciled, &set); err != nil {
			_ = brows.Close()
			return v, err
		}
		b.CreatedAt, b.CreatedBy = parseT(created), Actor{Key: owner}
		b.Expected, b.Max, b.Settled = money.MicroUSD(exp), money.MicroUSD(mx), money.MicroUSD(set)
		pending = append(pending, b)
	}
	if err := brows.Close(); err != nil {
		return v, err
	}
	for _, b := range pending {
		b.CreatedBy = actorFor(ctx, q, b.CreatedBy.Key)
		v.Batches = append(v.Batches, b)
	}
	return v, nil
}

// LedgerQuery filters ListLedger.
type LedgerQuery struct {
	BatchID      string
	Kinds        []string
	Since, Until *time.Time
	Limit        int
	Cursor       string
}

// LedgerRow is one ledger entry.
type LedgerRow struct {
	ID             int64
	At             time.Time
	Kind, Reason   string
	Micro          money.MicroUSD
	BatchID, JobID *string
	Principal      Actor
}

// LedgerPage is one page of the ledger, newest first.
type LedgerPage struct {
	Items      []LedgerRow
	NextCursor string
}

// ListLedger lists the append-only ledger newest first with a keyset cursor on the row id.
func (s *Service) ListLedger(ctx context.Context, f LedgerQuery) (LedgerPage, error) {
	if err := s.ready(); err != nil {
		return LedgerPage{}, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	q := `SELECT id, at, kind, micro_usd, batch_id, job_id, reason, principal_id FROM ledger WHERE 1=1`
	var args []any
	if f.BatchID != "" {
		q += ` AND batch_id = ?`
		args = append(args, f.BatchID)
	}
	if len(f.Kinds) > 0 {
		q += ` AND kind IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.Kinds)), ",") + `)`
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if f.Since != nil {
		q += ` AND at >= ?`
		args = append(args, store.FormatTime(*f.Since))
	}
	if f.Until != nil {
		q += ` AND at < ?`
		args = append(args, store.FormatTime(*f.Until))
	}
	if f.Cursor != "" {
		id, err := strconv.ParseInt(f.Cursor, 10, 64)
		if err != nil || id <= 0 {
			return LedgerPage{}, perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "invalid cursor", map[string]any{"field": "cursor"})
		}
		q += ` AND id < ?`
		args = append(args, id)
	}
	rows, err := s.o.Store.R.QueryContext(ctx, q+` ORDER BY id DESC LIMIT ?`, append(args, limit+1)...) //nolint:gosec // G202: only fixed fragments and placeholders
	if err != nil {
		return LedgerPage{}, fmt.Errorf("generation: ledger: %w", err)
	}
	var all []LedgerRow
	for rows.Next() {
		var r LedgerRow
		var at, owner string
		var m int64
		var batch, job sql.NullString
		if err := rows.Scan(&r.ID, &at, &r.Kind, &m, &batch, &job, &r.Reason, &owner); err != nil {
			_ = rows.Close()
			return LedgerPage{}, err
		}
		r.At, r.Micro, r.BatchID, r.JobID, r.Principal = parseT(at), money.MicroUSD(m), nullPtr(batch), nullPtr(job), Actor{Key: owner}
		all = append(all, r)
	}
	if err := rows.Close(); err != nil {
		return LedgerPage{}, err
	}
	var page LedgerPage
	size := 0
	for i, r := range all {
		if i == limit {
			break
		}
		b, _ := json.Marshal(r)
		if len(page.Items) > 0 && size+len(b)+300 > maxPageBytes {
			break
		}
		size += len(b) + 300 // the wire form carries both amounts and the principal reference
		r.Principal = actorFor(ctx, s.o.Store.R, r.Principal.Key)
		page.Items = append(page.Items, r)
	}
	if len(page.Items) < len(all) {
		page.NextCursor = strconv.FormatInt(page.Items[len(page.Items)-1].ID, 10)
	}
	return page, nil
}

// SetCaps applies a set_spend_caps request (human-only) in one transaction with the cap-change rows and the
// in-transaction audit row, and returns the new configuration.
func (s *Service) SetCaps(ctx context.Context, p auth.Principal, patch spend.CapsPatch) (CapsView, error) {
	if err := s.ready(); err != nil {
		return CapsView{}, err
	}
	if patch.MonthlyMicro == nil && patch.BatchMicro == nil && patch.HumanDailyMicro == nil && patch.AgentDailyMicro == nil &&
		patch.AutoApproveTakes == nil && patch.AutoApproveMicro == nil && len(patch.Tokens) == 0 {
		return CapsView{}, perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "at least one field is required", map[string]any{"field": "body"})
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return CapsView{}, err
	}
	fail := func(err error) (CapsView, error) {
		_ = s.o.Spend.RollbackTx(tx)
		idem.MarksFrom(ctx).Reset()
		return CapsView{}, err
	}
	if _, _, err := s.o.Spend.SetCaps(ctx, tx, p, patch); err != nil {
		var ut *spend.UnknownTokenError
		switch {
		case errors.As(err, &ut):
			return fail(perr(http.StatusUnprocessableEntity, "UNKNOWN_TOKEN", "no such token", map[string]any{"token_id": ut.TokenID}))
		case errors.Is(err, spend.ErrCapRange):
			return fail(perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "a cap is outside 0..$1000 or auto-approve takes outside 0..100", map[string]any{"field": "body"}))
		}
		return fail(err)
	}
	view, err := s.capsView(ctx, tx, p)
	if err != nil {
		return fail(err)
	}
	if e, ok := audit.PendingFrom(ctx); ok {
		e.Status = http.StatusOK
		if e.Duration = s.o.Now().Sub(e.At); e.Duration < 0 {
			e.Duration = 0
		}
		if err := audit.InsertTx(ctx, tx, e); err != nil {
			return fail(fmt.Errorf("generation: audit: %w", err))
		}
	}
	if err := s.o.Spend.CommitTx(ctx, tx); err != nil {
		return fail(err)
	}
	return view, nil
}
