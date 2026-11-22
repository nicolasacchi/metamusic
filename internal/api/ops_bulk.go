package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The pod bulk operations (BULK-P6, C398.3, C398.4, D-BULK-14): list_pods, and the pods block of get_provider_status. Both are Owner-only.

// PodsInfo is what the pod views know beyond the database. The build hook in internal/app/bulk.go sets Deps.Pods when the pod backend
// is built; without it get_provider_status has no pods block (there is no pod backend) and list_pods reads an empty table.
type PodsInfo struct {
	// ImageDigest is the configured image's digest ("sha256:<hex>"); the safety verdict is the latest canary row for it.
	ImageDigest string
	// KeyPresent says whether the pod key file is there now; it is read at every call and never cached.
	KeyPresent func() bool
	// WindowMax is MM_POD_WINDOW_MAX; zero for a backend that has no operator window (the simulated one).
	WindowMax time.Duration
}

const podSelect = `SELECT id, name, kind, batch_id, state, gpu_try, created_at, booted_at, first_poll_at, last_poll_at, deadline_at, terminated_at,
	lifetime_s, error_code, overhead_reserved_micro_usd, price_pph_micro_usd, settled FROM pods`

// listPods reads pod summaries, newest first. batch is "" for every batch; live drops terminated pods. The provider reference, the poll
// expiry and every other column that could carry a secret are not selected.
func listPods(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, batch string, live bool, limit int) ([]PodSummary, error) {
	query := podSelect + ` WHERE (? = '' OR batch_id = ?) AND (? = 0 OR state <> 'terminated') ORDER BY created_at DESC, id LIMIT ?`
	liveArg := 0
	if live {
		liveArg = 1
	}
	rows, err := q.QueryContext(ctx, query, batch, batch, liveArg, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PodSummary{}
	for rows.Next() {
		var p PodSummary
		var booted, first, last, term, errCode sql.NullString
		var life sql.NullInt64
		var reserved, price int64
		var settled int
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.BatchID, &p.State, &p.GPUTry, &p.CreatedAt, &booted, &first, &last, &p.DeadlineAt, &term,
			&life, &errCode, &reserved, &price, &settled); err != nil {
			return nil, err
		}
		p.BootedAt, p.FirstPollAt, p.LastPollAt, p.TerminatedAt = nullStrPtr(booted), nullStrPtr(first), nullStrPtr(last), nullStrPtr(term)
		p.ErrorCode = nullStrPtr(errCode)
		p.Cost = PodCost{ReservedMicroUSD: reserved, ReservedUSD: usd(money.MicroUSD(reserved))}
		if life.Valid {
			n := int(life.Int64)
			p.LifetimeS = &n
			if settled == 1 {
				m := money.ForSeconds(life.Int64, money.MicroUSD(price))
				v, s := int64(m), usd(m)
				p.Cost.SettledMicroUSD, p.Cost.SettledUSD = &v, &s
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func nullStrPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// podsBlock is the pods block of one pod backend's status: local facts only, no network.
func (d Deps) podsBlock(ctx context.Context, b BackendStatus) *PodsStatus {
	if d.Store == nil || d.Pods == nil {
		return nil
	}
	info := d.Pods
	out := &PodsStatus{Probe: "missing", Safety: generation.PodSafetyUnverified, Live: []PodSummary{}, Alerts: []string{}}
	if info.KeyPresent != nil {
		out.KeyPresent = info.KeyPresent()
	}
	var verdict string
	switch err := d.Store.R.QueryRowContext(ctx, `SELECT verdict FROM pod_safety_log WHERE kind = 'probe' ORDER BY id DESC LIMIT 1`).Scan(&verdict); {
	case err == nil:
		out.Probe = verdict
	case !errors.Is(err, sql.ErrNoRows):
		slog.Warn("pod status: probe unreadable")
	}
	if s, err := generation.PodSafety(ctx, d.Store.R, info.ImageDigest); err == nil {
		out.Safety = s
	} else {
		slog.Warn("pod status: safety unreadable")
	}
	var latest sql.NullString
	if err := d.Store.R.QueryRowContext(ctx, `SELECT image_digest FROM pod_safety_log WHERE kind = 'canary' ORDER BY id DESC LIMIT 1`).Scan(&latest); err == nil {
		out.SafetyDigestMatches = info.ImageDigest != "" && latest.String == info.ImageDigest
	}
	if live, err := listPods(ctx, d.Store.R, "", true, 20); err == nil {
		out.Live = live
	} else {
		slog.Warn("pod status: pods unreadable")
	}
	if b.Armed && info.WindowMax > 0 {
		if at, err := time.Parse(time.RFC3339Nano, b.ArmedChangedAt); err == nil {
			out.Window = &PodWindow{ArmedAt: b.ArmedChangedAt, AutoDisarmAt: tsOut(at.Add(info.WindowMax))}
		}
	}
	out.Alerts = d.podAlerts(ctx, b)
	return out
}

// podAlerts are the codes of the events of the pods that are still live (24 hours back), plus the reason of an open pod trip.
func (d Deps) podAlerts(ctx context.Context, b BackendStatus) []string {
	alerts := []string{}
	if b.Trip != nil && strings.HasPrefix(b.Trip.Reason, "pod_") {
		alerts = append(alerts, b.Trip.Reason)
	}
	rows, err := d.Store.R.QueryContext(ctx, `SELECT DISTINCT e.code FROM pod_events e JOIN pods p ON p.id = e.pod_id
		WHERE p.state <> 'terminated' AND e.code IS NOT NULL AND e.at >= ? ORDER BY e.code`, store.FormatTime(d.now().Add(-24*time.Hour)))
	if err != nil {
		slog.Warn("pod status: alerts unreadable")
		return alerts
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil && !slices.Contains(alerts, c) {
			alerts = append(alerts, c)
		}
	}
	return alerts
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// addPodBlocks fills the pods block of every pod backend of a provider status; every other entry is left as it was, so a process with no
// pod backend answers the status it always answered.
func (d Deps) addPodBlocks(ctx context.Context, s *ProviderStatus) {
	for i := range s.Backends {
		if provider.IsPodBackend(s.Backends[i].Backend) {
			s.Backends[i].Pods = d.podsBlock(ctx, s.Backends[i])
		}
	}
}

// registerBulk registers the pod operations.
func registerBulk(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_pods", Method: "GET", Path: "/api/pods",
		Summary:     "List the bulk pods",
		Description: "The pods of the pod backend, newest first: live ones by default, or all, optionally of one batch. Never the provider reference, the poll URL or a key.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/jobs", OwnerOnly: true},
		func(ctx context.Context, in *listPodsIn) (*listPodsOut, error) {
			if d.Store == nil {
				return nil, unavailable("pods")
			}
			items, err := listPods(ctx, d.Store.R, in.BatchID, in.State != "all", in.Limit)
			if err != nil {
				return nil, libErr(err)
			}
			return &listPodsOut{Body: PodList{Items: items}}, nil
		})
}
