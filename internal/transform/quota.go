package transform

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The daily CPU quota (D-T1-10, C201.3): a rolling 24 h window per principal KIND, one shared pool. All agent tokens together
// draw from the agent cap and humans from their own, so one rogue agent can exhaust the agent pool and starve other agents but
// never a human. A live row (queued or running) reserves its max_cpu_ms; every terminal row records cpu_ms (0 when it never
// ran), so a finished row releases its reservation by the CHECK constraint alone and a create-then-cancel loop costs nothing.

// Querier is the read side shared by *sql.DB and *sql.Tx, so create can read the quota inside its transaction.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Quota is one kind's position in the window.
type Quota struct {
	Kind       string
	CapMS      int64
	UsedMS     int64 // cpu_ms of rows finished inside the window
	ReservedMS int64 // max_cpu_ms of live rows
	ResetsAt   time.Time
}

// RemainingMS is what a new transform may still reserve (never negative).
func (q Quota) RemainingMS() int64 { return max(q.CapMS-q.UsedMS-q.ReservedMS, 0) }

// QuotaKind maps a principal to its quota pool: agents draw from the agent cap, every human-operated principal (human, device)
// from the human one.
func QuotaKind(p auth.Principal) string {
	if p.Kind == auth.KindAgent {
		return "agent"
	}
	return "human"
}

func (s *Service) capMS(kind string) int64 {
	if kind == "agent" {
		return int64(s.o.Config.TransformAgentDailyCPUS) * 1000
	}
	return int64(s.o.Config.TransformHumanDailyCPUS) * 1000
}

// DailyCPUUsedMS is SUM(COALESCE(cpu_ms, max_cpu_ms)) over the rows of kind that are live or finished inside the window, split
// into the finished and the reserved part.
func (s *Service) DailyCPUUsedMS(ctx context.Context, q Querier, kind string, now time.Time) (used, reserved int64, err error) {
	since := store.FormatTime(now.Add(-QuotaWindow))
	err = q.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN state IN ('queued','running') THEN 0 ELSE COALESCE(cpu_ms, 0) END), 0),
		COALESCE(SUM(CASE WHEN state IN ('queued','running') THEN max_cpu_ms ELSE 0 END), 0)
		FROM transforms WHERE principal_kind = ? AND (state IN ('queued','running') OR finished_at >= ?)`, kind, since).Scan(&used, &reserved)
	if err != nil {
		return 0, 0, fmt.Errorf("transform: quota: %w", err)
	}
	return used, reserved, nil
}

// QuotaOf reads the position of kind at now.
func (s *Service) QuotaOf(ctx context.Context, q Querier, kind string, now time.Time) (Quota, error) {
	used, reserved, err := s.DailyCPUUsedMS(ctx, q, kind, now)
	if err != nil {
		return Quota{}, err
	}
	out := Quota{Kind: kind, CapMS: s.capMS(kind), UsedMS: used, ReservedMS: reserved, ResetsAt: now.Add(QuotaWindow)}
	// The window is rolling: the pool starts to recover when the oldest counted finished row leaves it.
	var oldest sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT MIN(finished_at) FROM transforms WHERE principal_kind = ? AND finished_at >= ? AND cpu_ms > 0`,
		kind, store.FormatTime(now.Add(-QuotaWindow))).Scan(&oldest); err == nil && oldest.Valid {
		if t, perr := store.ParseTime(oldest.String); perr == nil {
			out.ResetsAt = t.Add(QuotaWindow)
		}
	}
	return out, nil
}

// CheckQuota answers 429 TRANSFORM_QUOTA_EXCEEDED when reserving needMS more would pass kind's cap. It is create's check (8);
// pass the transaction as q to read the same snapshot the insert will see.
func (s *Service) CheckQuota(ctx context.Context, q Querier, kind string, needMS int64) error {
	qt, err := s.QuotaOf(ctx, q, kind, s.o.Now())
	if err != nil {
		return err
	}
	if needMS > qt.RemainingMS() {
		return problem.E(429, ErrQuotaExceeded, "the daily CPU quota of this kind of principal is used up", map[string]any{
			"used_ms": qt.UsedMS, "reserved_ms": qt.ReservedMS, "cap_ms": qt.CapMS, "resets_at": store.FormatTime(qt.ResetsAt),
		})
	}
	return nil
}
