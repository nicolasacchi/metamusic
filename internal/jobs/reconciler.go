package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
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
	// Location is the process time zone: a billing window never crosses one of its month boundaries.
	// nil means time.Local.
	Location *time.Location
}

// Intervals of the reconciler (C76).
const (
	passEvery     = 60 * time.Second
	sweepActive   = 10 * time.Second
	activeTail    = 130 * time.Second // finished_at + idle_s (<= 60) + 60 s
	approvalBatch = 50
	stuckAfter    = 2 * time.Minute
	// An INITIALIZING worker costs nothing (RunPod bills from container start): the first pull of the 7.7 GB
	// image took 8-12 min and the cached-model download (7.3 GB, "initializing model files") 25+ min on a fresh
	// host (canary #2, 2026-10-15), so elapsed INITIALIZING time never trips; it only warns once per stretch.
	initWarnAfter = 30 * time.Minute
	// crash_loop needs evidence of a loop: restartTrip running -> initializing restarts within restartWindow, or
	// an UNHEALTHY worker for unhealthyGrace continuously.
	restartTrip     = 3
	restartWindow   = 30 * time.Minute
	unhealthyGrace  = 5 * time.Minute
	driftNoteEvery  = time.Hour
	skewLimit       = 30 * time.Second
	weeklyReminder  = 7 * 24 * time.Hour
	billingFetchGap = 15 * time.Minute
)

// Reconciler adopts orphans, meters worker lifetimes, trips the provider and reconciles billing.
type Reconciler struct {
	o ReconcilerOptions

	mu             sync.Mutex
	lastPassAt     time.Time
	lastSweepRn    time.Time // when Sweep was last called
	sweptAt        time.Time // when a sweep last succeeded
	skew           *time.Duration
	workers        []provider.Worker
	endpoint       *provider.EndpointConfig
	drift          []provider.Drift
	stuckSince     time.Time
	initSince      time.Time   // start of the current INITIALIZING-only stretch
	unhealthySince time.Time   // start of the current UNHEALTHY stretch
	prevRunning    int         // running workers at the previous sweep
	restarts       []time.Time // running -> initializing restarts inside restartWindow
	health         *provider.Health
	healthSince    time.Time // start of the current stretch with at least one RUNNING worker in /health
	crash          map[string]int
	actioned       map[string]bool
	notified       map[string]time.Time
	zeroChecked    map[string]bool
	holdFreed      int
	lastFetch      time.Time
	meterObs       map[string]*meterObs // per worker key: last listing observation and billable seconds so far
	billableSince  string               // cached settings meter.billable_since ("" until read)
}

// NewReconciler builds the reconciler.
func NewReconciler(o ReconcilerOptions) *Reconciler {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Location == nil {
		o.Location = time.Local
	}
	return &Reconciler{o: o, crash: map[string]int{}, actioned: map[string]bool{}, notified: map[string]time.Time{}, zeroChecked: map[string]bool{}, meterObs: map[string]*meterObs{}}
}

// Run drives the reconciler until ctx ends: a full pass at start and every 60 s, and a sweep every 10 s
// while a batch is active (and shortly after the last one finished). Nothing happens unless the runner
// holds the lease.
func (r *Reconciler) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if r.o.Runner != nil && r.o.Runner.Holding() {
			if err := r.Tick(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, store.ErrLeaseLost) {
				r.o.Log.Warn("reconciler", "err", redactErr(err))
			}
		}
		t := r.o.Clock.NewTimer(sweepActive)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C():
		}
	}
	return nil
}

// Tick does what is due now: a full pass every 60 s, otherwise a sweep every 10 s while generation is active.
func (r *Reconciler) Tick(ctx context.Context) error {
	now := r.o.Clock.Now()
	r.mu.Lock()
	lastPass, lastSweep := r.lastPassAt, r.lastSweepRn
	r.mu.Unlock()
	if lastPass.IsZero() || now.Sub(lastPass) >= passEvery || now.Before(lastPass) {
		return r.Pass(ctx)
	}
	if now.Sub(lastSweep) >= sweepActive {
		active, err := r.sweepActive(ctx, now)
		if err != nil || !active {
			return err
		}
		return r.sweepStep(ctx)
	}
	return nil
}

// sweepActive: a batch is running, or finished less than idle_s + 60 s ago (C76).
func (r *Reconciler) sweepActive(ctx context.Context, now time.Time) (bool, error) {
	var n int
	err := r.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches WHERE state = 'active' OR (state = 'finished' AND finished_at >= ?)`,
		store.FormatTime(now.Add(-activeTail))).Scan(&n)
	return n > 0, err
}

// Pass performs one reconciliation pass (tests drive it directly). It does nothing unless the runner
// holds the lease; every write carries the lease predicate (D-M3-30).
func (r *Reconciler) Pass(ctx context.Context) error {
	if r.o.Runner == nil || !r.o.Runner.Holding() {
		return nil
	}
	steps := []func(context.Context) error{r.adopt, r.expireApprovals, r.withdrawStuckApprovals, r.sweepUploads, r.refreshQuote, r.sweepStep, r.actOnTrips, r.reconcileBilling}
	var first error
	for _, step := range steps {
		if err := step(ctx); err != nil {
			if errors.Is(err, store.ErrLeaseLost) {
				return err
			}
			r.o.Log.Warn("reconciler step", "err", redactErr(err))
			if first == nil {
				first = err
			}
		}
	}
	r.mu.Lock()
	r.lastPassAt = r.o.Clock.Now()
	r.mu.Unlock()
	return first
}

func (r *Reconciler) lastSweepAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sweptAt
}

func (r *Reconciler) notify(ctx context.Context, m notify.Message) {
	if r.o.Notify == nil {
		return
	}
	if err := r.o.Notify.Notify(ctx, m); err != nil {
		r.o.Log.Warn("notify", "title", m.Title)
	}
}

// notifyOnce sends m unless the same key was sent within every (0 means once per process).
func (r *Reconciler) notifyOnce(ctx context.Context, key string, every time.Duration, m notify.Message) bool {
	now := r.o.Clock.Now()
	r.mu.Lock()
	last, ok := r.notified[key]
	send := !ok || (every > 0 && now.Sub(last) >= every)
	if send {
		r.notified[key] = now
	}
	r.mu.Unlock()
	if send {
		r.notify(ctx, m)
	}
	return send
}

// ---- approvals ----

// expireApprovals expires the pending approvals older than 24 h (C72.6): approval and batch expired, the
// outstanding released, reconciled_at set.
func (r *Reconciler) expireApprovals(ctx context.Context) error {
	ids, err := r.o.Spend.PendingExpired(ctx, r.o.Store.R, approvalBatch)
	if err != nil {
		return err
	}
	for _, id := range ids {
		err := r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
			f := r.o.Runner.fence()
			if err := r.o.Spend.ExpireApproval(ctx, tx, id, f); err != nil {
				return err
			}
			return r.auditTx(ctx, tx, f, "reconciler:approval_expired", map[string]any{"batch": id}, 0)
		})
		if errors.Is(err, spend.ErrInvalidTransition) {
			continue // decided meanwhile
		}
		if err != nil {
			return err
		}
		r.o.Runner.changed()
	}
	return nil
}

// withdrawStuckApprovals closes the batches that were cancelled while awaiting confirmation before cancel
// withdrew their approval itself: every job is cancelled but the batch is still awaiting_confirmation with a
// pending approval, which would keep the batch in the approvals hub. It is the same move cancel_batch now
// makes (approval and batch end 'rejected', approval via 'cancel', decided by the reconciler, the outstanding
// released) and writes one audit row per batch. Once a batch is closed it no longer matches, so the rule
// is idempotent and a no-op on a healthy database.
func (r *Reconciler) withdrawStuckApprovals(ctx context.Context) error {
	ids, err := r.o.Spend.StuckWithdrawn(ctx, r.o.Store.R, approvalBatch)
	if err != nil {
		return err
	}
	for _, id := range ids {
		err := r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
			f := r.o.Runner.fence()
			changed, err := r.o.Spend.WithdrawBatch(ctx, tx, id, spend.ExpirySystem, "cancelled before approval", &f)
			if err != nil || !changed {
				return err
			}
			return r.auditTx(ctx, tx, f, "reconciler:approval_withdrawn", map[string]any{"batch": id}, 0)
		})
		if err != nil {
			return err
		}
		r.o.Runner.changed()
	}
	return nil
}

// sweepUploads removes the received upload files of jobs that ended more than 24 h ago, and stale part files
// (C74.4); one audit row when something was removed.
func (r *Reconciler) sweepUploads(ctx context.Context) error {
	if r.o.Uploads == nil {
		return nil
	}
	n, err := r.o.Uploads.SweepFiles(ctx, r.o.Clock.Now())
	if n > 0 {
		r.auditAfter(ctx, "reconciler:upload_sweep", map[string]any{"files": n}, err != nil)
	}
	return err
}

// ---- quote ----

// refreshQuote reads the endpoint configuration and the catalog price and caches them in provider_state
// (C76). A failed refresh keeps the previous quote until it is QuoteMaxAge old; a refused key trips.
func (r *Reconciler) refreshQuote(ctx context.Context) error {
	q, err := r.o.Backend.Quote(ctx)
	if err != nil {
		if errors.Is(err, provider.ErrAccess) || errors.Is(err, provider.ErrEndpointNotFound) {
			return r.Trip(ctx, TripProviderAccess, map[string]any{"op": "quote"})
		}
		return err
	}
	now := r.o.Clock.Now()
	q.ObservedAt = now
	urls := make(map[string]string, len(q.Endpoint.RequestURLs))
	for k, v := range q.Endpoint.RequestURLs {
		urls[k] = redact.String(v)
	}
	q.Endpoint.RequestURLs = urls
	raw, err := json.Marshal(q)
	if err != nil {
		return err
	}
	return r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
		f := r.o.Runner.fence()
		res, err := tx.ExecContext(ctx, `UPDATE provider_state SET quote_json = ?, quote_at = ? WHERE backend = ?`+f.And(), //nolint:gosec // G202: fixed C69 predicate
			append([]any{string(raw), store.FormatTime(now), r.o.Backend.Name()}, f.Args()...)...)
		n, err := f.Fenced(res, err)
		if err != nil {
			return err
		}
		if n == 0 {
			return f.Confirm(ctx, tx, 0)
		}
		return nil
	})
}

// ---- sweep ----

const meterUpsertSQL = `INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last, image)
	SELECT ?, ?, ?, ?, ?, ?, ? WHERE `

const meterConflictSQL = ` ON CONFLICT(worker_key, started_at) DO UPDATE SET uptime_s = max(uptime_s, excluded.uptime_s),
	last_seen_at = excluded.last_seen_at, status_last = excluded.status_last, image = COALESCE(excluded.image, image)`

// sweepStep lists the workers and the endpoint (C76.3): it meters worker lifetimes, tags live attempts with
// the image of their worker, and applies the trip conditions, the drift check and the workers-at-zero check.
func (r *Reconciler) sweepStep(ctx context.Context) error {
	now := r.o.Clock.Now()
	rep, err := r.o.Backend.Sweep(ctx)
	r.mu.Lock()
	r.lastSweepRn = now
	r.mu.Unlock()
	if err != nil {
		if errors.Is(err, provider.ErrAccess) || errors.Is(err, provider.ErrEndpointNotFound) {
			return r.Trip(ctx, TripProviderAccess, map[string]any{"op": "sweep"})
		}
		return err
	}
	r.mu.Lock()
	r.sweptAt, r.skew, r.workers, r.endpoint, r.health = now, rep.Skew, rep.Workers, rep.Endpoint, rep.Health
	r.mu.Unlock()
	if err := r.ensureBillableSince(ctx, now); err != nil {
		return err
	}
	if err := r.recordWorkers(ctx, rep, now); err != nil {
		return err
	}
	if err := r.recordHealthLifetimes(ctx, rep, now); err != nil {
		return err
	}
	if err := r.checkTrips(ctx, rep, now); err != nil {
		return err
	}
	r.handleDrift(ctx, rep)
	if err := r.checkWorkersZero(ctx, rep, now); err != nil {
		return err
	}
	return r.actOnTrips(ctx) // a trip the runner opened (excess settle, refused top-up) is acted on within one sweep
}

func (r *Reconciler) workerKeyOf(id string) string {
	if len(r.o.MeterKey) == 0 {
		return ""
	}
	return workerKey(r.o.MeterKey, id)
}

// recordWorkers meters the billable seconds of the listed workers and tags live attempts with the digest of
// their worker's image (D-M3-26). See meterbillable.go for what is billable: the listing's uptimeSeconds is
// NOT billable time (it counts from the creation of the worker record, including image pull, model download
// and idle standby), so it only bounds the deltas that are accumulated.
func (r *Reconciler) recordWorkers(ctx context.Context, rep provider.SweepReport, now time.Time) error {
	ws := rep.Workers
	if len(ws) == 0 {
		r.mu.Lock()
		clear(r.meterObs) // nothing listed: every worker is a first observation when it comes back
		r.mu.Unlock()
		return nil
	}
	idleS := r.meterIdleS(ctx, rep.Endpoint)
	plan, err := r.planMeter(ctx, ws, now, idleS)
	if err != nil {
		return err
	}
	err = r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
		f := r.o.Runner.fence()
		ts := store.FormatTime(now)
		for _, p := range plan {
			if p.write {
				var image any
				if p.image != "" {
					image = p.image
				}
				args := []any{p.key, store.FormatTime(p.obs.started), ts, ts, p.obs.billable, p.status, image}
				if _, err := tx.ExecContext(ctx, meterUpsertSQL+f.Where()+meterConflictSQL, append(args, f.Args()...)...); err != nil { //nolint:gosec // G202: fixed C69 predicate
					return err
				}
			}
			if dg := ImageDigestFor(p.image, ""); strings.HasPrefix(dg, "sha256:") {
				if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET image_digest = ? WHERE worker_key = ? AND image_digest IS NULL AND state IN ('pending','submitted','unknown')`+f.And(), //nolint:gosec // G202: fixed C69 predicate
					append([]any{dg, p.key}, f.Args()...)...); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err // the in-memory state only advances once the totals are stored
	}
	r.commitMeter(plan)
	return nil
}

// checkTrips applies stuck_running, crash_loop and meter_exceeds_reserved (C76.3).
func (r *Reconciler) checkTrips(ctx context.Context, rep provider.SweepReport, now time.Time) error {
	if open, err := r.openTrips(ctx); err != nil || len(open) > 0 {
		return err // one open trip per backend
	}
	running := 0
	seen := map[string]bool{}
	var crashed bool
	for _, w := range rep.Workers {
		st := strings.ToUpper(w.Status)
		if st == "RUNNING" {
			running++
		}
		key := r.workerKeyOf(w.ID)
		if key == "" {
			key = "worker:" + w.ID
		}
		if st == "UNHEALTHY" && w.UptimeS != nil && *w.UptimeS > r.o.ColdStartS+60 {
			seen[key] = true
			r.mu.Lock()
			r.crash[key]++
			if r.crash[key] >= 2 {
				crashed = true
			}
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	for k := range r.crash {
		if !seen[k] {
			delete(r.crash, k)
		}
	}
	r.mu.Unlock()
	if crashed {
		return r.Trip(ctx, TripCrashLoop, map[string]any{"threshold_s": r.o.ColdStartS + 60})
	}
	if why := r.loopEvidence(ctx, rep, now); why != nil {
		return r.Trip(ctx, TripCrashLoop, why)
	}
	var evidence int
	if err := r.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_attempts a WHERE a.state IN ('pending','submitted','unknown')
		AND (a.in_progress_seen = 1 OR a.announced_at IS NOT NULL OR EXISTS (SELECT 1 FROM uploads u WHERE u.attempt_id = a.id AND u.state IN ('received','consumed')))`).Scan(&evidence); err != nil {
		return err
	}
	if h := rep.Health; h != nil { // /health counts workers the listing can omit (X3 incident)
		running = max(running, h.Running)
		evidence = max(evidence, h.InProgress)
	}
	// A RUNNING worker is also what RunPod calls one that is still creating and starting its container (create +
	// start + imports + SDK fitness checks: minutes on a cold host). One of our attempts that is submitted and
	// still queued remotely within ColdStartS + 60 s (the allowance behind the reservation's attempt_overhead
	// and the endpoint scaler) justifies exactly one such worker, so it is counted per attempt. The 2 minute
	// stuckAfter clock keeps running against the hard evidence only: when the window ends with the worker still
	// without a job, the trip is immediate; with no live attempt, or a queue wait past the window, the plain
	// 2 minute rule applies unchanged. Money stays bounded by this window, the scaler and attempt TTL, and the
	// meter (RUNNING seconds) still trips meter_exceeds_reserved on an overrun.
	window := time.Duration(r.o.ColdStartS+60) * time.Second
	booting, err := r.queuedInWindow(ctx, now, window)
	if err != nil {
		return err
	}
	r.mu.Lock()
	stuck := false
	if running > evidence {
		if r.stuckSince.IsZero() {
			r.stuckSince = now
		} else if now.Sub(r.stuckSince) >= stuckAfter && running > evidence+booting {
			stuck = true
		}
	} else {
		r.stuckSince = time.Time{}
	}
	r.mu.Unlock()
	if stuck {
		return r.Trip(ctx, TripStuckRunning, map[string]any{"running": running, "with_evidence": evidence, "cold_start_window_s": int(window / time.Second), "queued_in_window": booting})
	}
	over, detail, err := r.meterOver(ctx)
	if err != nil {
		return err
	}
	if over {
		return r.Trip(ctx, TripMeterExceeds, detail)
	}
	return nil
}

// queuedInWindow counts attempts that are pending/submitted without evidence of running, whose remote status is
// Queued or not yet observed, and that were submitted within window of now.
func (r *Reconciler) queuedInWindow(ctx context.Context, now time.Time, window time.Duration) (int, error) {
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT submitted_at FROM job_attempts a WHERE a.state IN ('pending','submitted')
		AND COALESCE(a.remote_status, '') IN ('', ?) AND a.in_progress_seen = 0 AND a.announced_at IS NULL`, string(provider.Queued))
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			return 0, err
		}
		t, err := store.ParseTime(at)
		if err != nil {
			return 0, err
		}
		if now.Sub(t) <= window {
			n++
		}
	}
	return n, rows.Err()
}

// workerCounts is the larger of the listing and /health per status (the listing can omit or mislabel workers).
func workerCounts(rep provider.SweepReport) (running, initializing, unhealthy int) {
	for _, w := range rep.Workers {
		switch strings.ToUpper(w.Status) {
		case "RUNNING":
			running++
		case "INITIALIZING":
			initializing++
		case "UNHEALTHY":
			unhealthy++
		}
	}
	if h := rep.Health; h != nil {
		running, initializing, unhealthy = max(running, h.Running), max(initializing, h.Initializing), max(unhealthy, h.Unhealthy)
	}
	return running, initializing, unhealthy
}

// loopEvidence returns trip details when the workers show a real crash loop: restartTrip running ->
// initializing restarts within restartWindow, or UNHEALTHY continuously for unhealthyGrace. A worker that is
// only INITIALIZING never trips on elapsed time (it is not billed); after initWarnAfter it raises one ntfy
// per stretch and reports initializing_since in the provider status.
func (r *Reconciler) loopEvidence(ctx context.Context, rep provider.SweepReport, now time.Time) map[string]any {
	running, initializing, unhealthy := workerCounts(rep)
	r.mu.Lock()
	if r.prevRunning > 0 && running == 0 && initializing > 0 {
		r.restarts = append(r.restarts, now)
	}
	r.prevRunning = running
	kept := r.restarts[:0]
	for _, t := range r.restarts {
		if now.Sub(t) < restartWindow {
			kept = append(kept, t)
		}
	}
	r.restarts = kept
	restarts := len(kept)
	if unhealthy > 0 {
		if r.unhealthySince.IsZero() {
			r.unhealthySince = now
		}
	} else {
		r.unhealthySince = time.Time{}
	}
	unhealthyFor := time.Duration(0)
	if !r.unhealthySince.IsZero() {
		unhealthyFor = now.Sub(r.unhealthySince)
	}
	if initializing > 0 && running == 0 {
		if r.initSince.IsZero() {
			r.initSince = now
		}
	} else {
		r.initSince = time.Time{}
	}
	initSince := r.initSince
	r.mu.Unlock()
	if restarts >= restartTrip {
		return map[string]any{"restarts": restarts, "window_s": int(restartWindow.Seconds())}
	}
	if unhealthyFor >= unhealthyGrace {
		return map[string]any{"unhealthy_s": int(unhealthyFor.Seconds())}
	}
	if !initSince.IsZero() && now.Sub(initSince) >= initWarnAfter {
		r.notifyOnce(ctx, "init-slow:"+initSince.Format(time.RFC3339Nano), 0, NoteInitializingSlow(r.o.PublicURL, int(now.Sub(initSince).Minutes())))
	}
	return nil
}

// recordHealthLifetimes meters the RUNNING workers /health counts but the listing does not give a startedAt and
// an uptime for: one synthetic lifetime per such worker, from the first sweep that saw a running worker until
// the running counter returns to zero. Only running time is billable (RunPod bills container start to stop;
// an image pull while INITIALIZING is not billed), so initializing, unhealthy, throttled and idle workers are
// never metered here (X3 canary #2: a 9 minute image pull tripped meter_exceeds_reserved). Listed workers are
// metered by recordWorkers and never counted twice.
func (r *Reconciler) recordHealthLifetimes(ctx context.Context, rep provider.SweepReport, now time.Time) error {
	h := rep.Health
	if h == nil || len(r.o.MeterKey) == 0 {
		return nil
	}
	r.mu.Lock()
	if h.Running == 0 {
		r.healthSince = time.Time{}
		r.mu.Unlock()
		return nil
	}
	if r.healthSince.IsZero() {
		r.healthSince = now
	}
	since := r.healthSince
	r.mu.Unlock()
	listed := 0
	for _, w := range rep.Workers {
		if w.UptimeS != nil && w.StartedAt != nil && strings.EqualFold(w.Status, "RUNNING") {
			listed++
		}
	}
	var statuses []string
	for range max(h.Running-listed, 0) {
		statuses = append(statuses, "RUNNING")
	}
	if len(statuses) == 0 {
		return nil
	}
	uptime := max(int(now.Sub(since).Seconds()), 0)
	return r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
		f := r.o.Runner.fence()
		ts := store.FormatTime(now)
		for i, st := range statuses {
			key := r.workerKeyOf(fmt.Sprintf("health:%d:%d", since.UnixNano(), i))
			args := []any{key, store.FormatTime(since), ts, ts, uptime, st, nil}
			if _, err := tx.ExecContext(ctx, meterUpsertSQL+f.Where()+meterConflictSQL, append(args, f.Args()...)...); err != nil { //nolint:gosec // G202: fixed C69 predicate
				return err
			}
		}
		return nil
	})
}

// checkWorkersZero notifies once per finished batch when a worker is still up at finished_at + idle_s + 30 s
// while no attempt is live (design layer 7).
func (r *Reconciler) checkWorkersZero(ctx context.Context, rep provider.SweepReport, now time.Time) error {
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT id, idle_s, finished_at FROM batches WHERE state = 'finished' AND finished_at >= ?`, store.FormatTime(now.Add(-time.Hour)))
	if err != nil {
		return err
	}
	type fb struct {
		id   string
		idle int
		at   time.Time
	}
	var fbs []fb
	for rows.Next() {
		var b fb
		var at string
		if err := rows.Scan(&b.id, &b.idle, &at); err != nil {
			rows.Close()
			return err
		}
		b.at = mustT(at)
		fbs = append(fbs, b)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	live := 0
	for _, w := range rep.Workers {
		if workerLive(w.Status) {
			live++
		}
	}
	if rep.Health != nil {
		live = max(live, rep.Health.Live())
	}
	for _, b := range fbs {
		r.mu.Lock()
		done := r.zeroChecked[b.id]
		r.mu.Unlock()
		if done || now.Before(b.at.Add(time.Duration(b.idle+30)*time.Second)) {
			continue
		}
		var attempts int
		if err := r.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_attempts WHERE state IN ('pending','submitted','unknown')`).Scan(&attempts); err != nil {
			return err
		}
		r.mu.Lock()
		r.zeroChecked[b.id] = true
		r.mu.Unlock()
		if live > 0 && attempts == 0 {
			r.notify(ctx, NoteWorkersNotZero(r.o.PublicURL))
		}
	}
	return nil
}

// SweepSnapshot is what the last sweep showed, for get_provider_status.
type SweepSnapshot struct {
	ObservedAt        time.Time
	WorkerCounts      map[string]int // by lower-case status
	Endpoint          *provider.EndpointConfig
	Drift             []provider.Drift
	Skew              *time.Duration
	LastReconcileAt   time.Time
	InitializingSince time.Time // zero unless a worker has been INITIALIZING (and none running) since then
}

// Snapshot returns the last sweep (zero ObservedAt before the first one).
func (r *Reconciler) Snapshot() SweepSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := SweepSnapshot{ObservedAt: r.sweptAt, Endpoint: r.endpoint, Drift: append([]provider.Drift(nil), r.drift...), Skew: r.skew, LastReconcileAt: r.lastPassAt, InitializingSince: r.initSince,
		WorkerCounts: map[string]int{}}
	for _, w := range r.workers {
		s.WorkerCounts[strings.ToLower(w.Status)]++
	}
	if h := r.health; h != nil { // /health counts workers the listing may omit: never report fewer
		for k, n := range map[string]int{"running": h.Running, "idle": h.Idle + h.Ready, "initializing": h.Initializing,
			"throttled": h.Throttled, "unhealthy": h.Unhealthy} {
			if n > s.WorkerCounts[k] {
				s.WorkerCounts[k] = n
			}
		}
	}
	return s
}

// auditTx writes a reconciler:* row in the caller's transaction (D-M3-22); a nil audit store skips it.
func (r *Reconciler) auditTx(ctx context.Context, tx *sql.Tx, f store.LeaseFence, op string, details map[string]any, cost money.MicroUSD) error {
	if r.o.Audit == nil {
		return nil
	}
	e, err := audit.SystemEntry(op, f.Now, details, 0, cost, false)
	if err != nil {
		return err
	}
	return audit.InsertTx(ctx, tx, e)
}
