package jobs

import (
	"context"
	"slices"
	"time"

	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// The upload-fallback seams of the jobs package (bulk plan amendments A4 and A5, BULK-R2). Both are nil-safe: with no minter and no
// rescuer wired (production without r2.env, every test that does not ask) every code path is exactly what it was.

// FallbackMinter mints the presigned fallback URLs of one attempt, one per artifact, valid until exp, and the worker's switch time
// in seconds. It records every object it hands out before it returns the URLs; an error means no fallback for this attempt. The
// URLs are in memory only.
type FallbackMinter interface {
	MintFallback(ctx context.Context, attemptID string, exp time.Time) (urls map[string]string, afterS int, err error)
}

// Rescuer completes an attempt from the fallback bucket when the worker could not reach the app: it commits the attempt's uploads
// through the upload service and reports whether the result was received by this call. It is asked outside any transaction.
type Rescuer interface {
	Rescue(ctx context.Context, attemptID string) (found bool, err error)
}

// UseFallback wires the minter and the rescuer after the upload fallback is built; either may be nil. Call it before Run.
func (r *Runner) UseFallback(m FallbackMinter, rs Rescuer) { r.o.Fallback, r.o.Rescue = m, rs }

// rescueMinGap is how often one attempt is rescued from the transfer and poll paths (the periodic sweep of the fallback package
// covers the rest).
const rescueMinGap = 15 * time.Second

// rescue asks the Rescuer about an attempt and reports whether it received the result. A fault is logged without detail and is not an
// error of the step: the attempt keeps its own deadlines. force skips the per-attempt rate limit (a settlement is about to decide).
func (r *Runner) rescue(ctx context.Context, attemptID string, force bool) bool {
	if r.o.Rescue == nil {
		return false
	}
	now := r.o.Clock.Now()
	r.mu.Lock()
	if last, ok := r.lastRescue[attemptID]; !force && ok && now.Sub(last) < rescueMinGap {
		r.mu.Unlock()
		return false
	}
	r.lastRescue[attemptID] = now
	r.mu.Unlock()
	found, err := r.o.Rescue.Rescue(ctx, attemptID)
	if err != nil {
		r.o.Log.Warn("upload fallback: rescue failed", "err", redact.Error(err))
		return false
	}
	return found
}

// applyFallback adds the fallback URLs to the take spec of an attempt that goes out on mm.worker/2 with the capability open on its
// backend. The document is built and parsed with placeholder URLs first, so that nothing is minted (and no object recorded) for a
// document the worker would refuse; any failure leaves the spec as it was.
func (r *Runner) applyFallback(ctx context.Context, c *claim, spec *provider.TakeSpec) {
	if r.o.Fallback == nil || provider.WireFor(*spec, c.job.Needs, true) != provider.ContractV2 {
		return
	}
	open, err := r.openCapsFor(ctx, r.o.Store.R, c.job.Backend)
	if err != nil || !slices.Contains(open, jobspec.CapFallback) {
		return
	}
	trial := *spec
	trial.Wire, trial.FallbackAfterS = provider.ContractV2, 60
	trial.Uploads = make(map[string]provider.UploadTarget, len(spec.Uploads))
	for n, t := range spec.Uploads {
		t.Fallback = &provider.FallbackTarget{URL: "https://fallback.invalid/x"}
		trial.Uploads[n] = t
	}
	if raw, err := provider.BuildWorkerInputV2(trial); err != nil {
		return
	} else if _, err := provider.ParseWorkerInput(raw); err != nil {
		return
	}
	urls, after, err := r.o.Fallback.MintFallback(ctx, spec.AttemptID, spec.Deadline)
	if err != nil {
		r.o.Log.Warn("upload fallback: not minted; the attempt has no fallback", "err", redact.Error(err))
		return
	}
	if len(urls) == 0 || after < 60 || after > 600 {
		return
	}
	ups := make(map[string]provider.UploadTarget, len(spec.Uploads))
	for n, t := range spec.Uploads {
		if u, ok := urls[n]; ok {
			t.Fallback = &provider.FallbackTarget{URL: u}
		}
		ups[n] = t
	}
	spec.Uploads, spec.FallbackAfterS, spec.Wire = ups, after, provider.ContractV2
}

// onFallbackAttempt reports whether the attempt was given fallback objects, which means its document went out on mm.worker/2 even
// when the job itself needs nothing (WireFor, D-G1-3).
func onFallbackAttempt(ctx context.Context, x q, attemptID string) bool {
	var one int
	err := x.QueryRowContext(ctx, `SELECT 1 FROM fallback_objects WHERE attempt_id = ? LIMIT 1`, attemptID).Scan(&one)
	return err == nil
}

// outputWire is the contract the output of an attempt must carry: the job's own wire, except that a plain yue2 job that went out on
// /2 because the attempt had a fallback may answer /2.
func outputWire(base, out string, fallbackAttempt bool) string {
	if fallbackAttempt && base == provider.ContractV1 && out == provider.ContractV2 {
		return provider.ContractV2
	}
	return base
}
