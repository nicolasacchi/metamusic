package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// Timing constants of the submit path (C75.3, C75.4).
const (
	submitTimeout      = 60 * time.Second
	uploadBudgetS      = 60
	windowSlack        = 15 * time.Minute // upload exp = submit + TTL + timeout_s + KillSlackS + 15 min (C74.1)
	preflightEvery     = 60 * time.Second
	maxClaimsPerStep   = 16
	noCapacityAfter    = 24 * time.Hour
	startingQueueSlack = 5 * time.Minute // row 13: still Queued at submitted_at + TTL + 5 min
)

// backoff is the resubmit delay after attempt k (rows 9 and 13): min(30 min, 1 min x 2^(k-1)).
func backoff(k int) time.Duration {
	if k < 1 {
		k = 1
	}
	if k > 6 {
		return 30 * time.Minute
	}
	return min(30*time.Minute, time.Minute<<(k-1))
}

// gateInfo is the claim gate read from local rows only.
type gateInfo struct {
	ok         bool
	reason     string
	quote      *provider.Quote
	workersMax int
	idleS      int
	price      money.MicroUSD // list price x margin; 0 without a usable quote
}

func (r *Runner) gateConfig() provider.GateConfig {
	mode := provider.ModeFake
	if r.o.Backend.Name() == provider.BackendRunPod {
		mode = provider.ModeRunPod
	}
	return provider.GateConfig{Mode: mode, KeysLoaded: true, ColdStartS: r.o.ColdStartS, MaxRuntime: r.o.MaxRuntime}
}

// readGate reads armed, tripped and the cached quote of the active backend through x.
func (r *Runner) readGate(ctx context.Context, x q) (gateInfo, error) {
	g := gateInfo{workersMax: provider.MaxWorkers}
	backend := r.o.Backend.Name()
	var armed int
	var quoteJSON, quoteAt sql.NullString
	err := x.QueryRowContext(ctx, `SELECT armed, quote_json, quote_at FROM provider_state WHERE backend = ?`, backend).Scan(&armed, &quoteJSON, &quoteAt)
	if errors.Is(err, sql.ErrNoRows) {
		g.reason = "not_armed"
		return g, nil
	}
	if err != nil {
		return g, fmt.Errorf("jobs: read provider state: %w", err)
	}
	var trips int
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_trips WHERE backend = ? AND reset_at IS NULL`, backend).Scan(&trips); err != nil {
		return g, fmt.Errorf("jobs: read trips: %w", err)
	}
	if quoteJSON.Valid {
		qt, qerr := decodeQuote(quoteJSON.String, quoteAt.String)
		if qerr == nil {
			g.quote = &qt
		}
	}
	now := r.o.Clock.Now()
	switch {
	case armed != 1:
		g.reason = "not_armed"
	case trips > 0:
		g.reason = "tripped"
	case backend == provider.BackendRunPod:
		if g.quote == nil {
			g.reason = provider.ReasonQuoteStale
		} else if why := provider.QuoteCheck(*g.quote, now, r.gateConfig()); why != "" {
			g.reason = why
		}
	default:
		// the fake backend needs no quote; a stale one is ignored rather than refused
		if g.quote != nil && now.Sub(g.quote.ObservedAt) > provider.QuoteMaxAge {
			g.quote = nil
		}
	}
	if g.reason != "" {
		return g, nil
	}
	g.ok = true
	if g.quote != nil {
		if g.quote.Endpoint.WorkersMax > 0 {
			g.workersMax = g.quote.Endpoint.WorkersMax
		}
		if g.quote.Endpoint.IdleTimeoutS != nil {
			g.idleS = *g.quote.Endpoint.IdleTimeoutS
		}
		if g.quote.ListPricePPH > 0 {
			g.price = money.WithMarginPPM(g.quote.ListPricePPH, r.o.PriceMarginPPM)
		}
	}
	return g, nil
}

// candidateSQL selects the next claimable job (C69 claim SQL). A queued job with an `unknown` attempt
// whose ambiguous top-up was not reserved waits (and fails once the ghost's window closed).
const candidateSQL = `SELECT j.id FROM jobs j JOIN batches b ON b.id = j.batch_id
	WHERE j.state = 'queued' AND j.backend = ? AND j.next_attempt_at <= ? AND j.cancel_requested_at IS NULL AND j.error_code IS NULL AND b.state = 'active'
	  AND (SELECT COUNT(*) FROM job_attempts a WHERE a.backend = ? AND a.state IN ('pending','submitted','unknown')) < min(?, b.workers_max)
	  AND NOT EXISTS (SELECT 1 FROM job_attempts g WHERE g.job_id = j.id AND g.state = 'unknown'
	        AND NOT EXISTS (SELECT 1 FROM ledger l WHERE l.attempt_id = g.id AND l.reason = 'topup_ambiguous'))
	ORDER BY b.created_at, j.take_index LIMIT 1`

// claimSQL is the C69 claim statement up to its lease predicate.
const claimSQL = `UPDATE jobs SET state='submitting', lease_owner=?, lease_expires_at=?, updated_at=?
	WHERE id = (` + candidateSQL + `) AND state = 'queued'`

// insertAttemptSQL is the attempt insert up to its lease predicate.
const insertAttemptSQL = `INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, created_at)
	SELECT ?, ?, ?, ?, 'pending', ?, ?, ?, ? WHERE `

// attemptArtifacts is the upload set of one attempt: the engine's required and optional artifacts.
func (r *Runner) attemptArtifacts(ctx context.Context, x q, j *jobRec) (arts map[string]int64, request string, err error) {
	eng, ok := r.o.Engines[j.Engine]
	if !ok {
		return nil, "", fmt.Errorf("jobs: no engine %q registered", j.Engine)
	}
	if err := x.QueryRowContext(ctx, `SELECT request_json FROM recipes WHERE id = ?`, j.RecipeID).Scan(&request); err != nil {
		return nil, "", fmt.Errorf("jobs: read recipe: %w", err)
	}
	required, optional := eng.Artifacts([]byte(request))
	arts = map[string]int64{}
	for _, n := range append(append([]string{}, required...), optional...) {
		limit, ok := provider.ArtifactMaxBytes(n)
		if !ok {
			return nil, "", fmt.Errorf("jobs: engine %q lists %q, which is not allowlisted", j.Engine, n)
		}
		arts[n] = limit
	}
	return arts, request, nil
}

// PreflightError names the failing check of the claim preflight ("disk" or "route").
type PreflightError struct {
	Check string
	Err   error
}

func (e *PreflightError) Error() string {
	return "jobs: preflight " + e.Check + ": " + redact.Error(e.Err)
}
func (e *PreflightError) Unwrap() error { return e.Err }

// phaseClaim claims and submits queued jobs while capacity allows.
func (r *Runner) phaseClaim(ctx context.Context, rep *StepReport) error {
	for range maxClaimsPerStep {
		ok, err := r.claimOne(ctx)
		if err != nil {
			if errors.Is(err, store.ErrLeaseShort) {
				return nil // the heartbeat renews first; no submit this tick
			}
			return err
		}
		if !ok {
			return nil
		}
		rep.Claimed++
	}
	return nil
}

// claim is what a committed claim transaction leaves for the submit step.
type claim struct {
	job     jobRec
	batch   batchRec
	attempt string
	exp     int64
	request string
	price   money.MicroUSD
}

// claimRefusal aborts the claim transaction; the job is then failed in a second transaction.
type claimRefusal struct {
	jobID, code string
	cap         *spend.CapError
	liveGhost   bool
}

func (c *claimRefusal) Error() string { return "jobs: claim refused: " + c.code }

func (r *Runner) claimOne(ctx context.Context) (bool, error) {
	backend := r.o.Backend.Name()
	g0, err := r.readGate(ctx, r.o.Store.R)
	if err != nil || !g0.ok {
		return false, err
	}
	now := r.o.Clock.Now()
	var candID string
	err = r.o.Store.R.QueryRowContext(ctx, candidateSQL, backend, store.FormatTime(now), backend, g0.workersMax).Scan(&candID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("jobs: find candidate: %w", err)
	}
	cand, err := loadJob(ctx, r.o.Store.R, candID)
	if err != nil {
		return false, err
	}
	arts, _, err := r.attemptArtifacts(ctx, r.o.Store.R, &cand)
	if err != nil {
		return false, err
	}
	var thisBytes int64
	for _, m := range arts {
		thisBytes += m
	}
	var open int64
	if err := r.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(u.max_bytes), 0) FROM uploads u JOIN job_attempts a ON a.id = u.attempt_id
		WHERE u.state = 'open' AND a.state IN ('pending','submitted','unknown')`).Scan(&open); err != nil {
		return false, err
	}
	if !r.preflightPasses(ctx, open+thisBytes, candID) {
		return false, nil // the failure was reported; queued jobs wait
	}
	var cl claim
	var refusal *claimRefusal
	err = r.tx(ctx, func(tx *sql.Tx) error {
		c, err := r.claimTx(ctx, tx, backend)
		if err != nil {
			return err
		}
		cl = *c
		return nil
	})
	if errors.As(err, &refusal) {
		return r.refuseClaim(ctx, refusal)
	}
	if err != nil {
		if errors.Is(err, errNoCandidate) {
			return false, nil
		}
		return false, err
	}
	if err := r.fault(FaultAfterClaimCommit); err != nil {
		return false, err
	}
	if err := r.submit(ctx, &cl); err != nil {
		return false, err
	}
	return true, nil
}

var errNoCandidate = errors.New("jobs: no claimable job")

// claimTx is the claim transaction: gate, claim statement, price check, attempt and uploads, the
// attempt_overhead reserve, all under the lease predicate and before any network call (C75.3).
func (r *Runner) claimTx(ctx context.Context, tx *sql.Tx, backend string) (*claim, error) {
	f := r.fence()
	g, err := r.readGate(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !g.ok {
		return nil, errNoCandidate
	}
	exp := r.leaseExpiry()
	var id, batchID string
	claimQ := claimSQL + f.And() + ` RETURNING id, batch_id` //nolint:gosec // G202: fixed C69 SQL, values are bound
	row := tx.QueryRowContext(ctx, claimQ,
		append([]any{r.o.Owner, store.FormatTime(exp), store.FormatTime(f.Now), backend, store.FormatTime(f.Now), backend, g.workersMax}, f.Args()...)...)
	if err := row.Scan(&id, &batchID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if cerr := f.Confirm(ctx, tx, 0); cerr != nil {
				return nil, cerr
			}
			return nil, errNoCandidate
		}
		return nil, fmt.Errorf("jobs: claim: %w", err)
	}
	j, err := loadJob(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	b, err := loadBatch(ctx, tx, batchID)
	if err != nil {
		return nil, err
	}
	atts, err := queryAtts(ctx, tx, `job_id = ? ORDER BY n`, id)
	if err != nil {
		return nil, err
	}
	live := len(liveAttempts(atts)) // the claim itself is not an attempt yet
	arts, request, err := r.attemptArtifacts(ctx, tx, &j)
	if err != nil {
		return nil, err
	}

	// Price in force: the batch price, any higher price an earlier attempt of the job ran under, or the
	// cached list price x margin, whichever is highest (C75.3 step 1).
	held := b.Price
	for _, a := range atts {
		held = max(held, a.Price)
	}
	price := held
	var topup money.MicroUSD
	if g.price > held {
		price = g.price
		topup = money.ForSeconds(int64(j.TimeoutS+b.KillSlackS), price) - money.ForSeconds(int64(j.TimeoutS+b.KillSlackS), held)
	}
	if topup > 0 {
		ce, err := r.reserve(ctx, tx, f, &j, b, nil, "topup_price", topup)
		if err != nil {
			return nil, err
		}
		if ce != nil {
			return nil, &claimRefusal{jobID: id, code: CodePriceChanged, cap: ce, liveGhost: live > 0}
		}
	}

	attemptID := r.o.NewID()
	n := j.Attempts + 1
	expUnix := f.Now.Add(r.o.TTL + time.Duration(j.TimeoutS+r.o.KillSlackS)*time.Second + windowSlack).Unix()
	window := time.Unix(expUnix, 0).UTC()
	args := []any{attemptID, id, n, backend, store.FormatTime(f.Now), store.FormatTime(window), int64(price), store.FormatTime(f.Now)}
	insertQ := insertAttemptSQL + f.Where() //nolint:gosec // G202: fixed C69 predicate
	res, err := tx.ExecContext(ctx, insertQ, append(args, f.Args()...)...)
	if cnt, err := f.Fenced(res, err); err != nil {
		return nil, fmt.Errorf("jobs: insert attempt: %w", err)
	} else if cnt == 0 {
		return nil, store.ErrLeaseLost
	}
	res, err = tx.ExecContext(ctx, `UPDATE jobs SET attempts = attempts + 1, first_submitted_at = COALESCE(first_submitted_at, ?) WHERE id = ? AND lease_owner = ?`+f.And(), //nolint:gosec // G202: fixed C69 predicate
		append([]any{store.FormatTime(f.Now), id, r.o.Owner}, f.Args()...)...)
	if cnt, err := f.Fenced(res, err); err != nil {
		return nil, fmt.Errorf("jobs: count attempt: %w", err)
	} else if cnt == 0 {
		return nil, store.ErrLeaseLost
	}
	if err := r.o.Uploads.OpenAttempt(ctx, tx, upload.JobRef{JobID: id, BatchID: batchID}, attemptID, arts, expUnix, f); err != nil {
		return nil, err
	}
	idle := max(g.idleS, b.IdleS)
	if g.idleS > b.IdleS {
		r.warnIdleDrift(b, g.idleS)
	}
	overhead := money.ForSeconds(int64(r.o.ColdStartS+idle), price)
	ce, err := r.reserve(ctx, tx, f, &j, b, &attemptID, "attempt_overhead", overhead)
	if err != nil {
		return nil, err
	}
	if ce != nil {
		return nil, &claimRefusal{jobID: id, code: CodeBudgetExceeded, cap: ce, liveGhost: live > 0}
	}
	if n > 1 {
		if err := insertEvent(ctx, tx, f, Event{JobID: id, AttemptID: attemptID, Name: EvResubmit, Detail: map[string]any{"queue_expiries": j.QueueExpiries}}); err != nil {
			return nil, err
		}
	}
	if err := insertEvent(ctx, tx, f, Event{JobID: id, AttemptID: attemptID, From: StateQueued, To: StateSubmitting, Name: EvClaim,
		Detail: map[string]any{"price_pph": int64(price), "overhead_micro_usd": int64(overhead), "window_ends_at": store.FormatTime(window)}}); err != nil {
		return nil, err
	}
	if err := r.auditTx(ctx, tx, f, "runner:submit", map[string]any{"job": id, "attempt": attemptID, "batch": batchID}, overhead); err != nil {
		return nil, err
	}
	if err := f.Confirm(ctx, tx, store.LeaseMargin); err != nil {
		return nil, err
	}
	j.State = StateSubmitting
	return &claim{job: j, batch: b, attempt: attemptID, exp: expUnix, request: request, price: price}, nil
}

// reserve writes a top-up or overhead reserve through spend (caps and batch ceiling, C72.3) and its audit
// row. A refusal returns the CapError and writes nothing.
func (r *Runner) reserve(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, b batchRec, attemptID *string, reason string, amount money.MicroUSD) (*spend.CapError, error) {
	ce, err := r.o.Spend.ReserveTopUp(ctx, tx, spend.TopUp{BatchID: b.ID, JobID: j.ID, AttemptID: attemptID, Reason: reason, Amount: amount, Lease: f})
	if err != nil {
		return nil, err
	}
	if ce != nil {
		d := map[string]any{"topup_reason": reason, "micro_usd": int64(amount), "refused": true}
		if ce.Cap != "" {
			d["cap"] = string(ce.Cap)
		}
		ev := Event{JobID: j.ID, Name: EvTopup, Detail: d}
		if attemptID != nil {
			ev.AttemptID = *attemptID
		}
		if err := insertEvent(ctx, tx, f, ev); err != nil {
			return nil, err
		}
		return ce, nil
	}
	if reason != "attempt_overhead" {
		ev := Event{JobID: j.ID, Name: EvTopup, Detail: map[string]any{"topup_reason": reason, "micro_usd": int64(amount)}}
		if attemptID != nil {
			ev.AttemptID = *attemptID
		}
		if err := insertEvent(ctx, tx, f, ev); err != nil {
			return nil, err
		}
		if err := r.auditTx(ctx, tx, f, "runner:topup", map[string]any{"job": j.ID, "reason": reason}, amount); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// auditTx writes a runner:* audit row in the caller's transaction (D-M3-22); a nil audit store skips it.
func (r *Runner) auditTx(ctx context.Context, tx *sql.Tx, f store.LeaseFence, op string, details map[string]any, cost money.MicroUSD) error {
	if r.o.Audit == nil {
		return nil
	}
	e, err := audit.SystemEntry(op, f.Now, details, 0, cost, false)
	if err != nil {
		return err
	}
	return audit.InsertTx(ctx, tx, e)
}

// refuseClaim runs after a rolled-back claim: the job is failed (row 6), or deferred while a ghost of an
// earlier attempt can still run.
func (r *Runner) refuseClaim(ctx context.Context, c *claimRefusal) (bool, error) {
	err := r.tx(ctx, func(tx *sql.Tx) error {
		f := r.fence()
		j, err := loadJob(ctx, tx, c.jobID)
		if err != nil {
			return err
		}
		atts, err := queryAtts(ctx, tx, `job_id = ? ORDER BY n`, c.jobID)
		if err != nil {
			return err
		}
		if live := liveAttempts(atts); len(live) > 0 {
			// a ghost may still run: wait for its window, then fail
			until := live[0].WindowEndsAt
			for _, a := range live {
				if a.WindowEndsAt.After(until) {
					until = a.WindowEndsAt
				}
			}
			return skipStale(r.setJob(ctx, tx, f, &j, set("next_attempt_at", store.FormatTime(until.Add(time.Second)))))
		}
		return skipStale(r.failQueuedJob(ctx, tx, f, &j, c.code, Event{Name: EvFailed, Code: c.code}))
	})
	return false, err
}

// warnIdleDrift queues the drift ntfy once per batch when the live idle timeout exceeds the frozen quote
// (C76.4); the overhead reserve already uses the larger value.
func (r *Runner) warnIdleDrift(b batchRec, liveIdle int) {
	r.mu.Lock()
	done := r.idleWarned[b.ID]
	r.idleWarned[b.ID] = true
	r.mu.Unlock()
	if done {
		return
	}
	r.queueNote(notify.Message{Title: "Endpoint drift: idle timeout", Priority: 4,
		Body:  fmt.Sprintf("The endpoint idle timeout is %d s, above the %d s of a running batch; its reserves use the larger value.", liveIdle, b.IdleS),
		Click: r.o.PublicURL + "/jobs"})
}

// preflightPasses runs the D-M3-33 preflight at most every 60 s and reports a failure once per check.
func (r *Runner) preflightPasses(ctx context.Context, inFlightBytes int64, candidate string) bool {
	if r.o.Preflight == nil {
		return true
	}
	now := r.o.Clock.Now()
	r.mu.Lock()
	if !r.preflightAt.IsZero() && now.Sub(r.preflightAt) < preflightEvery && now.After(r.preflightAt) {
		err := r.preflightErr
		r.mu.Unlock()
		return err == nil
	}
	r.mu.Unlock()
	err := r.o.Preflight(ctx, inFlightBytes)
	r.mu.Lock()
	r.preflightAt, r.preflightErr = r.o.Clock.Now(), err
	r.mu.Unlock()
	if err != nil {
		r.preflightFailed(ctx, err, candidate)
	}
	return err == nil
}

func (r *Runner) preflightFailed(ctx context.Context, err error, candidate string) {
	check := "preflight"
	var pe *PreflightError
	if errors.As(err, &pe) {
		check = pe.Check
	}
	now := r.o.Clock.Now()
	r.mu.Lock()
	last := r.preflightMsg[check]
	send := last.IsZero() || now.Sub(last) >= time.Hour
	if send {
		r.preflightMsg[check] = now
	}
	r.mu.Unlock()
	if !send {
		return
	}
	_ = r.tx(ctx, func(tx *sql.Tx) error {
		return insertEvent(ctx, tx, r.fence(), Event{JobID: candidate, Name: EvPreflightFailed, Detail: map[string]any{"check": check}})
	})
	why := "disk low"
	if check == "route" {
		why = "upload route down"
	}
	_ = r.o.Notify.Notify(ctx, notify.Message{Title: "Generation paused: " + why, Priority: 4, Body: "Queued takes wait until the check passes.", Click: r.o.PublicURL + "/jobs"})
}

// ---- submit ----

// submit mints the capability URLs in memory, sends the attempt to the provider (never replayed) and
// records the outcome.
func (r *Runner) submit(ctx context.Context, c *claim) error {
	spec, err := r.takeSpec(c)
	if err != nil {
		// nothing was sent: the attempt is rejected like a client-side refusal
		return r.submitOutcome(ctx, c, provider.Ref{}, &submitFailure{kind: failRefused, code: CodeSubmitRejected, text: err.Error()})
	}
	sctx, cancel := context.WithTimeout(ctx, submitTimeout)
	ref, serr := r.o.Backend.Submit(sctx, spec)
	cancel()
	if err := r.fault(FaultAfterSubmitReturn); err != nil {
		return err
	}
	if serr != nil {
		return r.submitOutcome(ctx, c, provider.Ref{}, classifySubmit(serr))
	}
	if ref.ID == "" {
		return r.submitOutcome(ctx, c, ref, &submitFailure{kind: failAmbiguous, text: "backend returned no job reference"})
	}
	return r.submitOutcome(ctx, c, ref, nil)
}

func (r *Runner) takeSpec(c *claim) (provider.TakeSpec, error) {
	ups, startURL, err := r.o.Uploads.Mint(upload.JobRef{JobID: c.job.ID, BatchID: c.batch.ID}, c.attempt, c.exp)
	if err != nil {
		return provider.TakeSpec{}, err
	}
	return provider.TakeSpec{
		JobID: c.job.ID, AttemptID: c.attempt, Engine: c.job.Engine, Request: []byte(c.request),
		TimeoutS: c.job.TimeoutS, TTL: r.o.TTL, UploadBudgetS: uploadBudgetS, Uploads: ups, StartURL: startURL,
		Expect: r.o.Expect, Deadline: time.Unix(c.exp, 0).UTC(),
	}, nil
}

type failKind int

const (
	failRetry     failKind = iota // 429, paused, endpoint 404: not accepted; resubmit after a delay
	failRefused                   // 401/403/1010/400/422: not accepted; the job fails
	failAmbiguous                 // 5xx, network, timeout: a ghost may exist
)

// submitFailure is a classified Submit error.
type submitFailure struct {
	kind       failKind
	code       string // job error code for failRefused
	status     string // "RATE_LIMITED" | "ENDPOINT_PAUSED" | ...
	retryAfter time.Duration
	trip       bool
	text       string
}

// errShape reads the C77.4 shape {Code, Status, RetryAfter} from the first error in the chain that has
// it, without importing the backend that defines it (provider must not be imported by jobs for its error
// types, and jobs must not import runpod).
func errShape(err error) (code string, status int, retryAfter time.Duration, ok bool) {
	for e := err; e != nil; e = errors.Unwrap(e) {
		v := reflect.ValueOf(e)
		for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
			if v.IsNil() {
				break
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			continue
		}
		c := v.FieldByName("Code")
		if !c.IsValid() || c.Kind() != reflect.String {
			continue
		}
		if s := v.FieldByName("Status"); s.IsValid() && s.CanInt() {
			status = int(s.Int())
		}
		if ra := v.FieldByName("RetryAfter"); ra.IsValid() && ra.CanInt() {
			retryAfter = time.Duration(ra.Int())
		}
		return c.String(), status, retryAfter, true
	}
	return "", 0, 0, false
}

func classifySubmit(err error) *submitFailure {
	text := redact.Error(err)
	switch {
	case errors.Is(err, provider.ErrEndpointNotFound):
		return &submitFailure{kind: failRetry, status: "NOT_FOUND", trip: true, text: text}
	case errors.Is(err, provider.ErrAccess):
		return &submitFailure{kind: failRefused, code: CodeProviderAuth, trip: true, text: text}
	}
	code, _, ra, ok := errShape(err)
	if ok {
		switch code {
		case "EDGE_BLOCKED":
			return &submitFailure{kind: failRefused, code: CodeEdgeBlocked, trip: true, text: text}
		case "UNAUTHORIZED", "FORBIDDEN":
			return &submitFailure{kind: failRefused, code: CodeProviderAuth, trip: true, text: text}
		case "BAD_REQUEST":
			return &submitFailure{kind: failRefused, code: CodeSubmitRejected, text: text}
		case "RATE_LIMITED":
			return &submitFailure{kind: failRetry, status: code, retryAfter: ra, text: text}
		case "ENDPOINT_PAUSED":
			return &submitFailure{kind: failRetry, status: code, text: text}
		case "NOT_FOUND":
			return &submitFailure{kind: failRetry, status: code, trip: true, text: text}
		}
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) {
		return &submitFailure{kind: failAmbiguous, status: "NETWORK", text: text}
	}
	return &submitFailure{kind: failAmbiguous, status: "UPSTREAM", text: text}
}

// submitOutcome is the outcome transaction of C75.3 step 3 (rows 8, 9 and 10).
func (r *Runner) submitOutcome(ctx context.Context, c *claim, ref provider.Ref, fail *submitFailure) error {
	err := r.tx(ctx, func(tx *sql.Tx) error {
		f := r.fence()
		j, err := loadJob(ctx, tx, c.job.ID)
		if err != nil {
			return err
		}
		if j.State != StateSubmitting || j.LeaseOwner != r.o.Owner {
			return errStale
		}
		switch {
		case fail == nil:
			if err := r.setAttempt(ctx, tx, f, c.attempt, "state = 'submitted', ref = ?", ref.ID); err != nil {
				return err
			}
			return r.moveJob(ctx, tx, f, &j, StateStarting, Event{AttemptID: c.attempt, Name: EvSubmitted},
				set("state_deadline_at", store.FormatTime(f.Now.Add(r.o.TTL+startingQueueSlack))))
		case fail.kind == failRetry:
			if err := r.rejectAttempt(ctx, tx, f, &j, c.batch, c.attempt); err != nil {
				return err
			}
			if fail.trip {
				if err := r.trip(ctx, tx, f, c.batch.Backend, "provider_access", map[string]any{"op": "submit"}); err != nil {
					return err
				}
			}
			delay := backoff(j.Attempts)
			delay = max(delay, fail.retryAfter)
			next := f.Now.Add(delay)
			d := map[string]any{"outcome": fail.status, "next_attempt_at": store.FormatTime(next), "text": fail.text}
			if fail.retryAfter > 0 {
				d["retry_after_s"] = int64(fail.retryAfter / time.Second)
			}
			return r.moveJob(ctx, tx, f, &j, StateQueued, Event{AttemptID: c.attempt, Name: EvSubmitRetry, Detail: d}, set("next_attempt_at", store.FormatTime(next)))
		case fail.kind == failRefused:
			if err := r.rejectAttempt(ctx, tx, f, &j, c.batch, c.attempt); err != nil {
				return err
			}
			if fail.trip {
				if err := r.trip(ctx, tx, f, c.batch.Backend, "provider_access", map[string]any{"op": "submit"}); err != nil {
					return err
				}
			}
			atts, err := queryAtts(ctx, tx, `job_id = ?`, j.ID)
			if err != nil {
				return err
			}
			ev := Event{AttemptID: c.attempt, Name: EvSubmitRefused, Code: fail.code, Detail: map[string]any{"text": fail.text}}
			if live := liveAttempts(atts); len(live) > 0 {
				// a ghost of an earlier attempt may still run, and failed is reachable only when nothing can
				// spend: the refusal is remembered (error_code) and the job fails when the ghosts are over
				until := live[0].WindowEndsAt
				for _, a := range live {
					if a.WindowEndsAt.After(until) {
						until = a.WindowEndsAt
					}
				}
				return r.moveJob(ctx, tx, f, &j, StateQueued, ev, set("error_code", fail.code), set("next_attempt_at", store.FormatTime(until.Add(time.Second))))
			}
			if err := r.moveJob(ctx, tx, f, &j, StateFailed, ev, set("error_code", fail.code)); err != nil {
				return err
			}
			return r.releaseNeverRan(ctx, tx, f, &j, c.batch)
		default: // ambiguous: a ghost may exist
			if err := r.setAttempt(ctx, tx, f, c.attempt, "state = 'unknown'"); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: c.attempt, Name: EvGhost}); err != nil {
				return err
			}
			return r.requeueAfterSubmit(ctx, tx, f, &j, c.batch, &c.attempt, fail.status)
		}
	})
	if errors.Is(err, errStale) {
		return nil
	}
	return err
}

// requeueAfterSubmit puts a `submitting` job back in the queue (row 9) after an ambiguous submit or an
// adoption. Every `unknown` attempt without one gets its topup_ambiguous reserve first (D-M3-4); a refused
// top-up is recorded and the job later fails BUDGET_EXCEEDED once the ghost's window has closed.
func (r *Runner) requeueAfterSubmit(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, b batchRec, attemptID *string, why string) error {
	atts, err := queryAtts(ctx, tx, `job_id = ? AND state = 'unknown' ORDER BY n`, j.ID)
	if err != nil {
		return err
	}
	var topped money.MicroUSD
	refused := false
	for _, a := range atts {
		var have int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ledger WHERE attempt_id = ? AND reason = 'topup_ambiguous'`, a.ID).Scan(&have); err != nil {
			return err
		}
		if have > 0 {
			continue
		}
		amount := money.ForSeconds(int64(j.TimeoutS+b.KillSlackS), a.Price)
		id := a.ID
		ce, err := r.reserve(ctx, tx, f, j, b, &id, "topup_ambiguous", amount)
		if err != nil {
			return err
		}
		if ce != nil {
			refused = true
			continue
		}
		topped += amount
	}
	next := f.Now.Add(backoff(j.Attempts))
	d := map[string]any{"outcome": why, "next_attempt_at": store.FormatTime(next), "topup_micro_usd": int64(topped)}
	if refused {
		d["topup_refused"] = true
	}
	ev := Event{Name: EvSubmitRetry, Detail: d}
	if attemptID != nil {
		ev.AttemptID = *attemptID
	}
	if err := r.moveJob(ctx, tx, f, j, StateQueued, ev, set("next_attempt_at", store.FormatTime(next))); err != nil {
		return err
	}
	if topped > 0 {
		r.mu.Lock()
		r.reserved = true
		r.mu.Unlock()
	}
	return nil
}

// rejectAttempt marks a never-accepted attempt `rejected` (basis not_accepted), releases its overhead
// reserve and closes its uploads.
func (r *Runner) rejectAttempt(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, b batchRec, attemptID string) error {
	ts := store.FormatTime(f.Now)
	if err := r.setAttempt(ctx, tx, f, attemptID, "state = 'rejected', terminal_basis = 'not_accepted', terminal_at = ?, settled = 1", ts); err != nil {
		return err
	}
	var reserved int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE attempt_id = ? AND kind = 'reserve' AND reason = 'attempt_overhead'`, attemptID).Scan(&reserved); err != nil {
		return err
	}
	if reserved > 0 {
		if err := r.ledgerAppend(ctx, tx, f, spend.Entry{Kind: spend.KindRelease, MicroUSD: money.MicroUSD(reserved), BatchID: &b.ID, JobID: &j.ID,
			AttemptID: &attemptID, PrincipalID: b.PrincipalID, Reason: "not_accepted"}); err != nil {
			return err
		}
	}
	return r.o.Uploads.CloseAttempt(ctx, tx, attemptID)
}

// ledgerAppend appends ledger rows after confirming the lease in the same write transaction: the writer
// pool is a single immediate connection, so the lease state read here cannot change before commit.
func (r *Runner) ledgerAppend(ctx context.Context, tx *sql.Tx, f store.LeaseFence, es ...spend.Entry) error {
	if err := f.Confirm(ctx, tx, 0); err != nil {
		return err
	}
	return r.o.Spend.Ledger().Append(ctx, tx, es...)
}

// releaseNeverRan releases the remaining execution pot of a terminal job when no attempt can have run:
// every attempt rejected, or done with basis provider and execution_ms 0 (C72.5). A batch released as a
// whole (rejected, expired) has nothing left to release.
func (r *Runner) releaseNeverRan(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, b batchRec) error {
	if b.State == "rejected" || b.State == "expired" {
		return nil
	}
	atts, err := queryAtts(ctx, tx, `job_id = ? ORDER BY n`, j.ID)
	if err != nil {
		return err
	}
	for _, a := range atts {
		never := a.State == "rejected" || (a.State == "done" && a.TerminalBasis == "provider" && a.ExecutionMS != nil && *a.ExecutionMS == 0)
		if !never {
			return nil
		}
	}
	rem, err := r.o.Spend.Ledger().ExecutionRemaining(ctx, tx, j.ID)
	if err != nil || rem <= 0 {
		return err
	}
	return r.ledgerAppend(ctx, tx, f, spend.Entry{Kind: spend.KindRelease, MicroUSD: rem, BatchID: &b.ID, JobID: &j.ID, PrincipalID: b.PrincipalID, Reason: "never_ran"})
}

// failQueuedJob moves a queued job with no live attempt to failed (row 6) and releases its pot.
func (r *Runner) failQueuedJob(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, code string, ev Event) error {
	b, err := loadBatch(ctx, tx, j.BatchID)
	if err != nil {
		return err
	}
	if err := r.moveJob(ctx, tx, f, j, StateFailed, ev, set("error_code", code)); err != nil {
		return err
	}
	return r.releaseNeverRan(ctx, tx, f, j, b)
}

// decodeQuote reads the cached quote of provider_state; quote_at is authoritative for its age.
func decodeQuote(raw, at string) (provider.Quote, error) {
	var qt provider.Quote
	if err := json.Unmarshal([]byte(raw), &qt); err != nil {
		return qt, err
	}
	t, err := store.ParseTime(at)
	if err != nil {
		return qt, err
	}
	qt.ObservedAt = t
	return qt, nil
}
