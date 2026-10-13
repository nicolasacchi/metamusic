package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// ReconcilerOptions builds the reconciler (C76).
type ReconcilerOptions struct {
	Store   *store.Store
	Backend provider.Backend
	Spend   *spend.Service
	Uploads *upload.Service
	Notify  notify.Notifier
	Audit   *audit.Store
	Runner  *Runner // holds the lease; the reconciler writes only while it holds it
	Clock   clock.Clock
	NewID   func() string
	Owner   string
	Log     *slog.Logger

	MeterKey                 []byte
	PublicURL, ConsoleURL    string
	BillingLag, ResidualHold time.Duration
	ColdStartS, KillSlackS   int
	MaxRuntime               time.Duration
	PriceMarginPPM           int64
	GateCfg                  provider.GateConfig
}

// Reconciler adopts orphans, meters worker lifetimes, trips the provider and reconciles billing.
type Reconciler struct{ o ReconcilerOptions }

// NewReconciler builds the reconciler.
func NewReconciler(o ReconcilerOptions) *Reconciler {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	return &Reconciler{o: o}
}

// Run passes on a schedule until ctx ends. STUB(M3-B6).
func (r *Reconciler) Run(ctx context.Context) error { return ErrNotImplemented }

// Pass performs one reconciliation pass (tests drive it directly). STUB(M3-B6).
func (r *Reconciler) Pass(ctx context.Context) error { return ErrNotImplemented }
