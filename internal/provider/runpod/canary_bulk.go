package runpod

import (
	"context"
	"time"
)

// CaseFallback is the upload-fallback canary (BULK-R2, C399.9): one plain take on the serverless backend. While the job is generating,
// the case writes a 300 s drill row for its attempt (the primary upload route then answers 503 to that attempt only), so a worker
// with capability fallback puts its artifacts into the bucket instead. It passes iff the take completes, at least one of its uploads
// arrived through the fallback, every bucket object is deleted within ten minutes and no bucket credential or signature shows in the
// job's rows. It spends like case main (--max-usd, default $0.50) and needs capability fallback open and a configured bucket. The
// drill and the three reads of the host's own database go through FallbackHooks, because the canary is otherwise a REST client.
const CaseFallback CanaryCase = "fallback"

const (
	fallbackDrillSeconds = 300
	fallbackDeleteWithin = 10 * time.Minute
)

// FallbackHooks are the host-side half of the fallback case: a canary run on the app's own host gets them from its database. A run
// without hooks (a REST-only client) refuses the case before any spend.
type FallbackHooks interface {
	// Drill writes the drill row of the job's attempt for seconds seconds (audited, CLI-only).
	Drill(ctx context.Context, jobID string, seconds int) error
	// Receipts counts the received uploads of the job and how many of them arrived through the fallback.
	Receipts(ctx context.Context, jobID string) (viaFallback, total int, err error)
	// ObjectsPending counts the job's bucket objects that are not yet deleted.
	ObjectsPending(ctx context.Context, jobID string) (int, error)
	// Leaks reports whether a bucket credential, a signature or a presigned URL shows in the job's events or any audit row.
	Leaks(ctx context.Context, jobID string) (bool, error)
}

// fallbackState is the case's progress inside one run.
type fallbackState struct {
	drilled bool
	drillOK bool
	drillAt time.Time
	err     string
}

// fallbackRefusal says why the case cannot start (the empty string means it can): it is checked before anything is estimated.
func (c *canary) fallbackRefusal(prov cProvider) string {
	switch {
	case c.o.Fallback == nil:
		return "the fallback case needs the host's database for the drill: run it on the app host (the canary has no fallback hooks)"
	case prov.UploadFallback == nil || !prov.UploadFallback.Configured:
		return "the upload fallback is not configured on the server (no r2.env); run `metamusic upload fallback check` first"
	case !prov.hasCap("fallback"):
		return "capability fallback is not open for this backend's image (open it with `metamusic runpod contract open --caps fallback`)"
	}
	return ""
}

// maybeDrill writes the drill row the first time the job is seen generating. A failure is remembered and fails the case at the end:
// the take itself continues (it is paid for already).
func (c *canary) maybeDrill(ctx context.Context, spec CanarySpec, job cJob) {
	if spec.Case != CaseFallback || c.fb.drilled || job.State != "generating" {
		return
	}
	c.fb.drilled = true
	if err := c.o.Fallback.Drill(ctx, job.ID, fallbackDrillSeconds); err != nil {
		c.fb.err = "the drill row could not be written"
		c.logf("fallback: the drill row could not be written; the take continues without it")
		return
	}
	c.fb.drillOK, c.fb.drillAt = true, c.o.Clock.Now()
	c.logf("fallback: the primary upload route answers 503 to this attempt for %d s", fallbackDrillSeconds)
}

// checkFallback adds the case's checks after the main ones (a take that did not complete has nothing to check).
func (c *canary) checkFallback(ctx context.Context, job cJob, meas map[string]any) {
	c.check("drill_written", c.fb.drillOK, "%s", map[bool]string{true: "a 300 s drill row was written while generating", false: orDefault(c.fb.err, "the job was never seen generating")}[c.fb.drillOK])
	if job.State != "completed" {
		return
	}
	hk := c.o.Fallback
	via, total, err := hk.Receipts(ctx, job.ID)
	meas["uploads_total"], meas["uploads_via_fallback"] = total, via
	c.check("upload_via_fallback", err == nil && via >= 1, "%d of %d uploads arrived through the fallback", via, total)
	deadline := c.o.Clock.Now().Add(fallbackDeleteWithin)
	pending := -1
	for {
		if pending, err = hk.ObjectsPending(ctx, job.ID); err == nil && pending == 0 {
			break
		}
		if !c.o.Clock.Now().Before(deadline) || c.sleep(ctx, c.o.SweepEvery) != nil {
			break
		}
	}
	meas["objects_pending"] = pending
	c.check("objects_deleted", err == nil && pending == 0, "%d bucket objects left after %s", pending, fallbackDeleteWithin)
	leaks, err := hk.Leaks(ctx, job.ID)
	c.check("no_secret_in_events", err == nil && !leaks, "%s", map[bool]string{true: "a bucket credential or signature shows in a row", false: "none in the job's events or the audit rows"}[leaks])
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
