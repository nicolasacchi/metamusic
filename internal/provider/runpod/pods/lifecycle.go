package pods

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Trip reasons the lifecycle loop opens (D-BULK-9). internal/jobs names the same strings (TestPodTripReasons_MatchThePodsPackage).
const (
	TripOverrun       = "pod_overrun"
	TripDeadman       = "pod_deadman"
	TripUnknown       = "pod_unknown"
	TripPrice         = "pod_price"
	TripAccess        = "pod_access"
	TripExcess        = "pod_excess"
	TripCreateInvalid = "pod_create_invalid"
)

// Audit rows of the loop (registered with audit.AddSystemOps by the app).
const (
	OpPodCreate = "bulk:pod_create"
	OpPodSettle = "bulk:pod_settle"
	OpPodCanary = "bulk:pod_canary"
)

// Pod error codes the loop writes besides C390.1's list.
const (
	codeBudget       = "BUDGET_EXCEEDED"
	codeBalance      = "BALANCE"
	codeDeadline     = "POD_DEADLINE"
	codeRefused      = "CREATE_REFUSED"
	codeRateLimited  = "RATE_LIMITED"
	codeQueued       = "QUEUED_REFUSED"
	codeInvalid      = "CREATE_INVALID"
	codeAmbiguous    = "CREATE_AMBIGUOUS"
	codeNoKey        = "NO_KEY"
	codeNotNeeded    = "NOT_NEEDED"
	codeBootTimeout  = "BOOT_TIMEOUT"
	codePrice        = "PRICE_MISMATCH"
	codeNeverSeen    = "NEVER_SEEN"
	codeRestartLoop  = "RESTART_LOOP"
	codeWorkerLost   = "WORKER_LOST"
	codePodState     = "POD_STATE"
	codeWindowEnded  = "WINDOW_ENDED"
	codeWindowClosed = "WINDOW_CLOSED"
	maxTries         = 4 // pods.gpu_try BETWEEN 1 AND 4
	noCapacityWait   = 5 * time.Minute
	nameSweepEvery   = 60 * time.Second
	bootGraceS       = 60
	deleteAfterStopS = 60
	canaryDeadmanS   = 120
	canaryWindowS    = 420
	canaryVerdictS   = canaryDeadmanS + 60
	canaryDeleteS    = 60
	idleWindow       = 30 * time.Minute
	activeEvery      = 10 * time.Second
	quietEvery       = 60 * time.Second
	podCacheDir      = "/opt/mm/pod-cache/hub"
)

// AuditFunc records one audit row of a system action after its effect (the app builds it over audit.SystemEntry). nil = none.
type AuditFunc func(ctx context.Context, op string, details map[string]any, cost money.MicroUSD, failed bool)

// LifecycleOptions builds the lifecycle loop (C396.3). The package imports neither the runner nor the provider admin, so everything that
// crosses that line is injected by the app (C391.3): the lease (Holding, Fence), the gate (State), the trips (Trip) and the disarm.
type LifecycleOptions struct {
	Store *store.Store
	API   API
	// Queue is the hand-out queue shared with the poll handler and the backend.
	Queue *Queue
	// Signer mints the pod's poll link; PublicURL is the origin of the app as the pod sees it.
	Signer    *Signer
	PublicURL string
	Spend     *spend.Service
	Clock     clock.Clock
	Log       *slog.Logger
	NewID     func() string

	// Backend is runpod-pod (default) or fake-pod.
	Backend string
	Image   string
	// GPUTypes is MM_POD_GPU_TYPES in order: one type per create.
	GPUTypes []string
	DiskGB   int
	// Weights3B is the pinned revision of the 3B weights (MM_YUE2_3B_REVISION of the pod environment).
	Weights3B                                     string
	BootS, IdleS, SlackS, KillSlackS, MaxRecreate int
	MaxRuntime, Deadman, OrphanAfter, WindowMax   time.Duration
	// RequireCanary: an ordinary pod batch needs the latest canary row for the image digest to be `verified` (D-BULK-12). The real backend
	// sets it; the fake pod backend does not.
	RequireCanary bool
	// NoWindows turns the bulk-window rules off (the fake pod backend, armed by default, has no operator window): no window_ended or
	// window_idle disarm, and no stop at the window's end.
	NoWindows bool

	// KeyPresent reports whether the pod key file is in place now; nil = absent.
	KeyPresent func() bool
	// Holding reports whether the runner holds the lease with margin; Fence is the lease predicate carrier of a write that starts now.
	Holding func() bool
	Fence   func() store.LeaseFence
	// State is the gate's state of the backend (armed, enabled, tripped, when it was armed).
	State func(ctx context.Context) (provider.State, error)
	// Trip opens a trip of the backend (Reconciler.Trip); it acts on it too.
	Trip func(ctx context.Context, reason string, details map[string]any) error
	// Disarm is ProviderAdmin.SystemDisarm for the backend; reason is window_ended or window_idle.
	Disarm func(ctx context.Context, reason string) error
	Audit  AuditFunc
	// ThrowawayKey returns the random key a canary pod's poll link is signed with; nil = 32 bytes of crypto/rand.
	ThrowawayKey func() []byte
}

// Lifecycle is the loop that creates, watches, stops and settles pods (C396.3). It is the only writer of pod states (D-M3-3): the poll handler
// records observations and the runner owns the jobs. A Lifecycle built with the backend off does not exist (the app builds none), so
// production runs no code of this file.
type Lifecycle struct {
	o LifecycleOptions

	mu         sync.Mutex
	retryAfter map[string]time.Time // batch -> no create before (no capacity, rate limit)
	tripped    map[string]bool      // pod+reason already reported in this process
	keyNoted   map[string]bool      // pods whose key-absent event was written
	lastSweep  time.Time
	wake       chan struct{}
}

// NewLifecycle builds the loop. It makes no network call.
func NewLifecycle(o LifecycleOptions) *Lifecycle {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	if o.Backend == "" {
		o.Backend = provider.BackendRunPodPod
	}
	if o.Queue == nil {
		o.Queue = NewQueue()
	}
	return &Lifecycle{o: o, retryAfter: map[string]time.Time{}, tripped: map[string]bool{}, keyNoted: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// Nudge wakes the loop (the poll handler calls it on a first poll, a done or a restart report).
func (l *Lifecycle) Nudge() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *Lifecycle) now() time.Time { return l.o.Clock.Now() }

func (l *Lifecycle) holding() bool { return l.o.Holding != nil && l.o.Holding() }

func (l *Lifecycle) keyPresent() bool {
	return l.o.KeyPresent != nil && l.o.KeyPresent() && l.o.API != nil
}

// Run drives the loop until ctx ends: every 10 s while a pod row is not terminal, else every 60 s, and at once on a nudge or a queued take.
func (l *Lifecycle) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		changed := l.o.Queue.Changed() // taken before the pass, so a take queued during it is not missed
		live := false
		if l.holding() {
			if err := l.Tick(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, store.ErrLeaseLost) {
				l.o.Log.Warn("pod lifecycle", "err", redact.Error(err))
			}
			rows, err := AllLiveRows(ctx, l.o.Store.R)
			live = err == nil && len(rows) > 0
		}
		every := quietEvery
		if live {
			every = activeEvery
		}
		t := l.o.Clock.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C():
		case <-l.wake:
			t.Stop()
		case <-changed:
			t.Stop()
		}
	}
	return nil
}

// Tick does one pass. It does nothing unless the runner holds the lease. With no live pod, no armed backend and no key it only reads the
// database: no network call, no pod, no reservation.
func (l *Lifecycle) Tick(ctx context.Context) error {
	if !l.holding() {
		return nil
	}
	st, err := l.o.State(ctx)
	if err != nil {
		return fmt.Errorf("pods: gate state: %w", err)
	}
	var errs []error
	if l.keyPresent() {
		errs = append(errs, l.nameSweep(ctx))
	}
	rows, err := AllLiveRows(ctx, l.o.Store.R)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := l.reconcilePod(ctx, row, st); err != nil {
			if errors.Is(err, store.ErrLeaseLost) {
				return err
			}
			errs = append(errs, err)
		}
	}
	errs = append(errs, l.settle(ctx), l.createPod(ctx, st), l.windows(ctx, st))
	return errors.Join(errs...)
}

// fenced runs fn in one write transaction that first confirms the runner lease (every write of the loop is fenced, C396.3).
func (l *Lifecycle) fenced(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := l.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if l.o.Fence != nil {
		if err := l.o.Fence().Confirm(ctx, tx, 0); err != nil {
			return err
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// networkOK confirms at least store.LeaseMargin of lease is left before a network call that cannot be taken back (C396.3: "creating"
// needs 30 s of lease).
func (l *Lifecycle) networkOK(ctx context.Context) error {
	if l.o.Fence == nil {
		return nil
	}
	return l.o.Fence().Confirm(ctx, l.o.Store.R, store.LeaseMargin)
}

func (l *Lifecycle) audit(ctx context.Context, op string, details map[string]any, cost money.MicroUSD, failed bool) {
	if l.o.Audit != nil {
		l.o.Audit(ctx, op, details, cost, failed)
	}
}

// trip opens a trip once per pod and reason in this process (the trip itself is one open row per backend).
func (l *Lifecycle) trip(ctx context.Context, podID, reason string, details map[string]any) error {
	key := podID + "|" + reason
	l.mu.Lock()
	done := l.tripped[key]
	l.mu.Unlock()
	if done || l.o.Trip == nil {
		return nil
	}
	if err := l.o.Trip(ctx, reason, details); err != nil {
		return err
	}
	l.mu.Lock()
	l.tripped[key] = true
	l.mu.Unlock()
	return nil
}

// accessErr trips pod_access for a refused key (401 or 403 with the key present) and reports whether err was that.
func (l *Lifecycle) accessErr(ctx context.Context, podID string, err error) bool {
	if !errors.Is(err, provider.ErrAccess) || !l.keyPresent() {
		return false
	}
	if terr := l.trip(ctx, podID, TripAccess, map[string]any{"op": "pods"}); terr != nil {
		l.o.Log.Warn("pod trip", "reason", TripAccess, "err", redact.Error(terr))
	}
	return true
}

// ---- create ---------------------------------------------------------------------------------------------------------------

// overheadS is O = BootS + IdleS + SlackS + K + OverrunS of C393.1: what one pod may bill beyond its takes.
func (l *Lifecycle) overheadS() int {
	return l.o.BootS + l.o.IdleS + l.o.SlackS + l.o.KillSlackS + spend.PodOverrunGraceS + spend.PodPassS
}

type batchInfo struct {
	id        string
	price     money.MicroUSD
	totalS    int // Σ timeout_s of the batch's takes
	maxS      int
	canary    bool
	nTakes    int
	createdAt time.Time
}

// batchOf reads what a create needs of a batch.
func (l *Lifecycle) batchOf(ctx context.Context, id string) (batchInfo, error) {
	b := batchInfo{id: id}
	var price int64
	var created string
	var canary sql.NullInt64
	err := l.o.Store.R.QueryRowContext(ctx, `SELECT b.price_pph_micro_usd, b.n_takes, b.created_at, json_extract(e.quote_json, '$.pod.canary')
		FROM batches b JOIN estimates e ON e.id = b.estimate_id WHERE b.id = ?`, id).Scan(&price, &b.nTakes, &created, &canary)
	if err != nil {
		return b, err
	}
	b.price = money.MicroUSD(price)
	b.createdAt, _ = store.ParseTime(created)
	// A pod batch of one take is the self-termination canary: ordinary pod estimates need MM_POD_MIN_TAKES >= 10 takes, so only the canary
	// estimate (C399.10) can be that small. The quote may also say so (quoteDoc.Pod, BULK-P6).
	b.canary = (canary.Valid && canary.Int64 == 1) || b.nTakes == 1
	err = l.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(timeout_s), 0), COALESCE(MAX(timeout_s), 0) FROM jobs WHERE batch_id = ?`, id).Scan(&b.totalS, &b.maxS)
	return b, err
}

// createCandidate is the oldest active pod batch of this backend with an unhanded take in memory and no live pod.
func (l *Lifecycle) createCandidate(ctx context.Context) (string, error) {
	rows, err := l.o.Store.R.QueryContext(ctx, `SELECT DISTINCT b.id FROM batches b JOIN pod_assignments a ON a.batch_id = b.id
		WHERE b.backend = ? AND b.state = 'active' AND a.pod_id IS NULL AND a.done_at IS NULL ORDER BY b.created_at, b.id`, l.o.Backend)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	for _, id := range ids {
		if l.o.Queue.Len(id) > 0 {
			return id, nil
		}
	}
	return "", nil
}

// createPod creates at most one pod, and only inside an operator-approved window: the backend is armed, enabled and untripped, the key is
// in place, the armed window (MM_POD_WINDOW_MAX) is not over, and no other pod is live (the global bound counts one worker).
func (l *Lifecycle) createPod(ctx context.Context, st provider.State) error {
	if !st.Armed || !st.Enabled || st.Tripped || !l.keyPresent() || l.windowOver(st) {
		return nil
	}
	live, err := AllLiveRows(ctx, l.o.Store.R)
	if err != nil || len(live) > 0 {
		return err
	}
	batch, err := l.createCandidate(ctx)
	if err != nil || batch == "" {
		return err
	}
	b, err := l.batchOf(ctx, batch)
	if err != nil {
		return err
	}
	now := l.now()
	l.mu.Lock()
	wait := l.retryAfter[batch]
	l.mu.Unlock()
	if now.Before(wait) {
		return nil
	}
	if !b.canary && l.o.RequireCanary {
		ok, err := l.canaryVerified(ctx)
		if err != nil || !ok {
			return err
		}
	}
	rows, err := PodsOfBatch(ctx, l.o.Store.R, batch)
	if err != nil {
		return err
	}
	plan, err := l.planCreate(b, rows, now)
	if err != nil || plan == nil {
		return err
	}
	if plan.endSupply {
		return l.endSupply(ctx, batch, rows, codeDeadline, plan.reason)
	}
	return l.startPod(ctx, b, *plan)
}

type createPlan struct {
	n, try    int
	gpuIdx    int
	deadline  time.Time
	endSupply bool
	reason    string
}

// planCreate decides which pod identity to create next (nil: nothing to do now) from the rows of the batch (C396.3).
func (l *Lifecycle) planCreate(b batchInfo, rows []Row, now time.Time) (*createPlan, error) {
	p := &createPlan{n: 1, try: 1}
	var first *Row
	refused := 0
	for i := range rows {
		r := &rows[i]
		switch r.ErrorCode {
		case codeBudget, codeBalance, codeDeadline, codeInvalid:
			return nil, nil // the supply of this batch ended
		}
		if first == nil || r.DeadlineAt.Before(first.DeadlineAt) {
			first = r
		}
	}
	if b.canary && len(rows) > 0 {
		return nil, nil // one canary pod per batch, whatever became of it
	}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		p.n, p.try = last.N, last.GPUTry+1
		for _, r := range rows {
			if r.N == last.N && r.ErrorCode == codeRefused {
				refused++
			}
		}
		if last.ErrorCode == codeRefused || last.ErrorCode == codeRateLimited || last.ErrorCode == codeQueued {
			// the same pod identity, the next type
			if p.try > maxTries {
				p.endSupply, p.reason = true, "no_capacity"
				return p, nil
			}
		} else {
			p.n, p.try, refused = last.N+1, 1, 0
			if p.n > 1+l.o.MaxRecreate {
				return nil, nil
			}
		}
	}
	if len(l.o.GPUTypes) > 0 {
		p.gpuIdx = refused % len(l.o.GPUTypes)
	}
	switch {
	case first != nil:
		p.deadline = first.DeadlineAt // absolute, from the first create
	case b.canary:
		p.deadline = now.Add(time.Duration(l.o.BootS+canaryWindowS) * time.Second)
	default:
		span := l.o.BootS + b.totalS + b.maxS + l.o.IdleS + l.o.SlackS
		if limit := int(l.o.MaxRuntime / time.Second); limit > 0 {
			span = min(span, limit)
		}
		p.deadline = now.Add(time.Duration(span) * time.Second)
	}
	if !b.canary && now.Add(time.Duration(l.o.BootS+b.maxS+l.o.KillSlackS)*time.Second).After(p.deadline) {
		p.endSupply, p.reason = true, "no_time"
	}
	// a full round of refusals waits five minutes before the next one
	if p.try > 1 && len(l.o.GPUTypes) > 0 && refused > 0 && refused%len(l.o.GPUTypes) == 0 {
		last := rows[len(rows)-1]
		if last.TerminatedAt != nil && now.Before(last.TerminatedAt.Add(noCapacityWait)) && !p.endSupply {
			return nil, nil
		}
	}
	return p, nil
}

// endSupply ends the pod supply of a batch: the last pod row (or a refused-overhead intent) carries the code, so Submit refuses later
// takes, and every take still waiting in memory is dropped and closed `cancelled` so the runner requeues it into that refusal.
func (l *Lifecycle) endSupply(ctx context.Context, batch string, rows []Row, code, reason string) error {
	now := l.now()
	ids, err := UnhandedAssignments(ctx, l.o.Store.R, batch)
	if err != nil {
		return err
	}
	return l.fenced(ctx, func(tx *sql.Tx) error {
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			if err := SetErrorCode(ctx, tx, last.ID, code, now); err != nil {
				return err
			}
			if err := AppendEvent(ctx, tx, last.ID, Event{At: now, Event: "supply_ended", Code: code, Detail: map[string]any{"reason": reason}}); err != nil {
				return err
			}
		}
		for _, id := range ids {
			l.o.Queue.Drop(id)
			if _, err := CloseAssignment(ctx, tx, id, AssignCancelled, code, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// podName is mm-<12 hex> from the random end of the row id (the canary: mm-c<11 hex>).
func podName(id string, canary bool) string {
	hex := strings.ReplaceAll(id, "-", "")
	if canary {
		return "mm-c" + hex[len(hex)-11:]
	}
	return namePrefix + hex[len(hex)-12:]
}

// startPod writes the intent and its reserve in one transaction, then the creating state, then POSTs (C396.3).
func (l *Lifecycle) startPod(ctx context.Context, b batchInfo, p createPlan) error {
	now := l.now()
	id := l.o.NewID()
	kind := KindBatch
	if b.canary {
		kind = KindCanary
	}
	if len(l.o.GPUTypes) == 0 {
		return ErrNoGPUTypes
	}
	gpu := l.o.GPUTypes[p.gpuIdx]
	digest := l.digest()
	np := NewPod{ID: id, Name: podName(id, b.canary), Kind: kind, BatchID: b.id, GPUType: gpu, ImageDigest: digest, N: p.n, GPUTry: p.try,
		PricePPH: b.price, DeadlineAt: p.deadline, PollExp: PollExpFor(p.deadline), At: now}
	var refusal *spend.CapError
	if p.try == 1 { // a pod's overhead is reserved once, at its first try
		np.OverheadReserved = money.ForSeconds(int64(l.overheadS()), b.price)
	}
	err := l.fenced(ctx, func(tx *sql.Tx) error {
		if err := InsertIntent(ctx, tx, np); err != nil {
			return err
		}
		if np.OverheadReserved == 0 {
			return nil
		}
		fresh, err := MarkOverheadReserved(ctx, tx, id, now)
		if err != nil || !fresh {
			return err
		}
		ce, err := l.o.Spend.ReservePodOverhead(ctx, tx, spend.PodOverhead{BatchID: b.id, PodID: id, Amount: np.OverheadReserved, Lease: l.o.Fence()})
		if err != nil {
			return err
		}
		if ce != nil {
			refusal = ce
			return errRefusedOverhead
		}
		return nil
	})
	if errors.Is(err, errRefusedOverhead) {
		return l.budgetRefused(ctx, np, refusal)
	}
	if err != nil {
		return err
	}
	l.audit(ctx, OpPodCreate, map[string]any{"batch": b.id, "n": p.n, "try": p.try, "kind": kind, "overhead_micro_usd": int64(np.OverheadReserved)}, np.OverheadReserved, false)
	row, err := GetRow(ctx, l.o.Store.R, id)
	if err != nil {
		return err
	}
	return l.postCreate(ctx, row)
}

var errRefusedOverhead = errors.New("pods: overhead reserve refused")

// budgetRefused records the refused overhead reserve: the intent ends `terminated` BUDGET_EXCEEDED (later claims are refused) and the
// takes waiting in memory are closed.
func (l *Lifecycle) budgetRefused(ctx context.Context, np NewPod, ce *spend.CapError) error {
	now := l.now()
	np.OverheadReserved = 0
	zero := 0
	err := l.fenced(ctx, func(tx *sql.Tx) error {
		if err := InsertIntent(ctx, tx, np); err != nil {
			return err
		}
		return Transition(ctx, tx, np.ID, StateIntent, StateTerminated, Move{At: now, Event: "overhead_refused", Code: codeBudget, LifetimeS: &zero,
			Detail: map[string]any{"cap": ce.Cap}})
	})
	if err != nil {
		return err
	}
	rows, err := PodsOfBatch(ctx, l.o.Store.R, np.BatchID)
	if err != nil {
		return err
	}
	return l.endSupply(ctx, np.BatchID, rows, codeBudget, "overhead_refused")
}

func (l *Lifecycle) digest() string {
	d := ImageDigest(l.o.Image)
	if d == "" && l.o.Backend == provider.BackendFakePod {
		d = fakeDigest
	}
	return d
}

// env is the pod's environment (C392.2). The poll link is the credential of the pod: it is built here and goes nowhere but the create body.
func (l *Lifecycle) env(row Row) map[string]string {
	signer, deadman := l.o.Signer, int(l.o.Deadman/time.Second)
	if row.Kind == KindCanary {
		key := make([]byte, MinKeyLen)
		if l.o.ThrowawayKey != nil {
			key = l.o.ThrowawayKey()
		} else {
			_, _ = rand.Read(key)
		}
		signer, deadman = NewSigner(key), canaryDeadmanS // a link nobody can verify: every poll answers 403 and the dead-man must fire
	}
	return map[string]string{
		"MODE":                "agent",
		"MM_POLL_URL":         signer.URL(l.o.PublicURL, row.ID, row.PollExp),
		"MM_DEADLINE_UNIX":    strconv.FormatInt(row.DeadlineAt.Unix(), 10),
		"MM_DEADMAN_S":        strconv.Itoa(deadman),
		"MM_YUE2_3B_REVISION": l.o.Weights3B,
		"MM_YUE2_3B_DIR":      podCacheDir,
	}
}

// postCreate moves an intent to creating and sends the create request, never replaying it: an unknown outcome makes the pod `orphaned`.
func (l *Lifecycle) postCreate(ctx context.Context, row Row) error {
	if err := l.networkOK(ctx); err != nil {
		if errors.Is(err, store.ErrLeaseShort) {
			return nil // the heartbeat renews first; the intent resumes on a later tick
		}
		return err
	}
	if need, err := l.stillNeeded(ctx, row); err != nil || !need {
		if err != nil {
			return err
		}
		return l.terminateUnsent(ctx, row, StateIntent, codeNotNeeded, "not_needed")
	}
	now := l.now()
	if err := l.fenced(ctx, func(tx *sql.Tx) error {
		if err := SetCreateSent(ctx, tx, row.ID, now); err != nil {
			return err
		}
		return Transition(ctx, tx, row.ID, StateIntent, StateCreating, Move{At: now, Event: "create_sent"})
	}); err != nil {
		return err
	}
	row.State = StateCreating
	pod, err := l.o.API.CreatePod(ctx, CreateBody{Name: row.Name, Image: l.o.Image, GPUTypeID: row.GPUType, DiskGB: l.o.DiskGB, Env: l.env(row)})
	return l.createOutcome(ctx, row, pod, err)
}

// stillNeeded: the batch is active and has an unhanded take waiting (a canary needs only the batch).
func (l *Lifecycle) stillNeeded(ctx context.Context, row Row) (bool, error) {
	var active int
	if err := l.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches WHERE id = ? AND state = 'active'`, row.BatchID).Scan(&active); err != nil || active == 0 {
		return false, err
	}
	ids, err := UnhandedAssignments(ctx, l.o.Store.R, row.BatchID)
	return err == nil && len(ids) > 0, err
}

// terminateUnsent ends a pod whose create request never went out.
func (l *Lifecycle) terminateUnsent(ctx context.Context, row Row, from, code, event string) error {
	zero := 0
	return l.fenced(ctx, func(tx *sql.Tx) error {
		return Transition(ctx, tx, row.ID, from, StateTerminated, Move{At: l.now(), Event: event, Code: code, LifetimeS: &zero})
	})
}

// createOutcome handles the reply of a create (C392.3).
func (l *Lifecycle) createOutcome(ctx context.Context, row Row, pod Pod, err error) error {
	now := l.now()
	var rl *ErrRateLimited
	var rf *ErrRefused
	switch {
	case err == nil:
		return l.fenced(ctx, func(tx *sql.Tx) error {
			if err := SetProviderRef(ctx, tx, row.ID, pod.ID, now); err != nil {
				return err
			}
			return Transition(ctx, tx, row.ID, StateCreating, StateBooting, Move{At: now, Event: "created"})
		})
	case errors.Is(err, ErrBalance):
		return l.endCreate(ctx, row, codeBalance, "balance", true)
	case errors.Is(err, ErrInvalid):
		if terr := l.trip(ctx, row.ID, TripCreateInvalid, map[string]any{"op": "create"}); terr != nil {
			l.o.Log.Warn("pod trip", "reason", TripCreateInvalid, "err", redact.Error(terr))
		}
		return l.endCreate(ctx, row, codeInvalid, "create_invalid", true)
	case errors.Is(err, ErrQueued):
		return l.endCreate(ctx, row, codeQueued, "queued_refused", false)
	case errors.Is(err, ErrNoKey):
		return l.endCreate(ctx, row, codeNoKey, "no_key", false)
	case errors.As(err, &rl):
		l.mu.Lock()
		l.retryAfter[row.BatchID] = now.Add(max(rl.RetryAfter, 30*time.Second))
		l.mu.Unlock()
		return l.endCreate(ctx, row, codeRateLimited, "rate_limited", false)
	case errors.As(err, &rf):
		if err := l.endCreate(ctx, row, codeRefused, "create_refused", false); err != nil {
			return err
		}
		return l.noCapacity(ctx, row)
	case errors.Is(err, provider.ErrAccess):
		l.accessErr(ctx, row.ID, err)
		return l.endCreate(ctx, row, "ACCESS_REFUSED", "access_refused", false)
	}
	// an ambiguous outcome (5xx, timeout, reset) or any other failure: the pod may exist. Never replayed; the name decides.
	return l.orphan(ctx, row, codeAmbiguous, "create_ambiguous")
}

// endCreate ends a creating pod whose request was answered without creating one. ended: the batch's pod supply is over.
func (l *Lifecycle) endCreate(ctx context.Context, row Row, code, event string, ended bool) error {
	zero := 0
	err := l.fenced(ctx, func(tx *sql.Tx) error {
		return Transition(ctx, tx, row.ID, StateCreating, StateTerminated, Move{At: l.now(), Event: event, Code: code, LifetimeS: &zero})
	})
	if err != nil || !ended {
		return err
	}
	rows, err := PodsOfBatch(ctx, l.o.Store.R, row.BatchID)
	if err != nil {
		return err
	}
	return l.endSupply(ctx, row.BatchID, rows, code, event)
}

// noCapacity notes a full round of refused types: the takes stay queued and the next round waits five minutes (C396.3).
func (l *Lifecycle) noCapacity(ctx context.Context, row Row) error {
	rows, err := PodsOfBatch(ctx, l.o.Store.R, row.BatchID)
	if err != nil {
		return err
	}
	refused := 0
	for _, r := range rows {
		if r.N == row.N && r.ErrorCode == codeRefused {
			refused++
		}
	}
	if len(l.o.GPUTypes) == 0 || refused%len(l.o.GPUTypes) != 0 {
		return nil
	}
	now := l.now()
	l.mu.Lock()
	l.retryAfter[row.BatchID] = now.Add(noCapacityWait)
	l.mu.Unlock()
	if err := l.fenced(ctx, func(tx *sql.Tx) error {
		return AppendEvent(ctx, tx, row.ID, Event{At: now, Event: "no_capacity", Code: "NO_CAPACITY", Detail: map[string]any{"types": len(l.o.GPUTypes), "retry_after_s": int(noCapacityWait / time.Second)}})
	}); err != nil {
		return err
	}
	if row.GPUTry >= maxTries {
		return l.endSupply(ctx, row.BatchID, rows, codeDeadline, "no_capacity")
	}
	return nil
}
