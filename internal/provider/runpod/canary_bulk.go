package runpod

import (
	"context"
	"net/http"
	"net/url"
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

// CasePodSelfterm is the self-termination canary of the pod backend (BULK-P7, C399.10 client side): an estimate with options
// {backend: "runpod-pod", pod_canary: "self_terminate"} and one synthetic take. The server creates one canary pod whose poll link
// every poll refuses, so the agent's dead-man must fire. The batch always waits for the Owner's approval on /human: the case prints
// that and waits. It passes iff the canary pod is gone, no pod is left live, the latest safety verdict for the configured image
// digest is `verified` and the batch settled within its reserve. It needs the pod backend armed, the pod key present, a probe row and
// capability agent open; it spends only what the Owner approves (--max-usd is required: the estimate ceiling is checked against it).
const CasePodSelfterm CanaryCase = "pod-selfterm"

const (
	defaultPodApprovalWait = 30 * time.Minute
	defaultPodWait         = 90 * time.Minute
	podVerdictGrace        = 2 * time.Minute
)

func podSelftermSpec(spec CanarySpec) CanarySpec {
	spec.Options = map[string]any{"backend": "runpod-pod", "pod_canary": "self_terminate"}
	spec.NeedCaps = []string{"agent"}
	spec.driver = podSelftermDriver
	return spec
}

// podRefusal says why the pod case cannot start (empty: it can). The generic gates (enabled, armed, trip, capabilities) have run on
// the pod backend's own entry; this adds the pod facts.
func podRefusal(p cProvider) string {
	switch {
	case p.Pods == nil:
		return "the server shows no pods block for the runpod-pod backend (MM_POD_BACKEND=1 is needed)"
	case !p.Pods.KeyPresent:
		return "the pod key is absent on the server (place runpod-pod.env in the secrets directory)"
	case p.Pods.Probe != "ok" && p.Pods.Probe != "too_broad":
		return "the pod key has no usable probe row (run `metamusic runpod probe --pods`)"
	}
	return ""
}

// podView is the part of a pod summary (list_pods) the case reads.
type podView struct {
	Kind      string  `json:"kind"`
	State     string  `json:"state"`
	LifetimeS *int    `json:"lifetime_s"`
	ErrorCode *string `json:"error_code"`
}

func (c *canary) listBatchPods(ctx context.Context) ([]podView, error) {
	var out struct {
		Items []podView `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/pods?state=all&batch_id="+url.QueryEscape(c.rep.BatchID), nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *canary) batchState(ctx context.Context) (string, error) {
	var b cBatch
	err := c.do(ctx, http.MethodGet, "/api/batches/"+url.PathEscape(c.rep.BatchID), nil, nil, &b)
	return b.State, err
}

// cancelBatch cancels with a fresh context so that an interrupted run still stops the batch.
func (c *canary) cancelBatch() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return c.do(ctx, http.MethodPost, "/api/batches/"+url.PathEscape(c.rep.BatchID)+"/cancel", nil, map[string]any{"reason": "runpod canary"}, nil)
}

// podSelftermDriver replaces the job wait loop: approval, the pod's life, the verdict.
func podSelftermDriver(ctx context.Context, c *canary, spec CanarySpec, prov cProvider) *CanaryReport {
	approvalWait, podWait := c.o.PodApprovalWait, c.o.PodWait
	if approvalWait <= 0 {
		approvalWait = defaultPodApprovalWait
	}
	if podWait <= 0 {
		podWait = defaultPodWait
	}
	before := ""
	if prov.Pods != nil {
		before = prov.Pods.Safety
	}
	meas := map[string]any{"safety_before": before}
	c.rep.Measured = meas
	c.logf("the batch awaits the Owner's approval on /human (it spends the pod's lifetime, at most the estimate ceiling); waiting up to %s", approvalWait)
	// 1. approval
	approved := false
	deadline := c.o.Clock.Now().Add(approvalWait)
	for {
		if err := ctx.Err(); err != nil {
			_ = c.cancelBatch()
			return c.finish("aborted", ExitAborted, "interrupted; the batch was cancelled")
		}
		st, err := c.batchState(ctx)
		if err != nil {
			_ = c.cancelBatch()
			return c.finish("fail", ExitFail, "reading the batch failed (%v); the batch was cancelled", err)
		}
		if st == "rejected" || st == "expired" {
			return c.finish("aborted", ExitAborted, "the batch was %s without running: nothing was spent", st)
		}
		if st != "awaiting_confirmation" {
			approved = true
			break
		}
		if !c.o.Clock.Now().Before(deadline) {
			break
		}
		if c.sleep(ctx, c.o.SweepEvery) != nil {
			continue
		}
	}
	if !approved {
		_ = c.cancelBatch()
		return c.finish("aborted", ExitAborted, "the Owner did not approve the batch within %s: it was cancelled and nothing was spent (no approval, no pod)", approvalWait)
	}
	c.logf("approved; the canary pod boots, loses its poll link and must delete itself")
	// 2. the pod's life
	var pod *podView
	deadline = c.o.Clock.Now().Add(podWait)
	for {
		if err := ctx.Err(); err != nil {
			_ = c.cancelBatch()
			return c.finish("aborted", ExitAborted, "interrupted; the batch was cancelled")
		}
		items, err := c.listBatchPods(ctx)
		if err != nil {
			_ = c.cancelBatch()
			return c.finish("fail", ExitFail, "listing the pods failed (%v); the batch was cancelled", err)
		}
		pod = nil
		for i := range items {
			if items[i].Kind == "canary" {
				pod = &items[i]
			}
		}
		if pod != nil && pod.State == "terminated" {
			break
		}
		if !c.o.Clock.Now().Before(deadline) {
			_ = c.cancelBatch() // the server then stops and deletes every live pod of the batch
			break
		}
		if c.sleep(ctx, c.o.SweepEvery) != nil {
			continue
		}
	}
	c.check("pod_created", pod != nil, "%s", map[bool]string{true: "a canary pod was created for the batch", false: "no canary pod was listed for the batch"}[pod != nil])
	gone := pod != nil && pod.State == "terminated"
	c.check("pod_terminated", gone, "%s", map[bool]string{true: "the canary pod is gone", false: "the canary pod was still listed when the wait ended; the batch was cancelled"}[gone])
	if pod != nil {
		meas["pod_state"] = pod.State
		if pod.LifetimeS != nil {
			meas["pod_lifetime_s"] = *pod.LifetimeS
		}
		if pod.ErrorCode != nil {
			meas["pod_error_code"] = *pod.ErrorCode
		}
	}
	// 3. the verdict: the loop writes it when the pod is confirmed gone; wait a little for it to show
	after, matches, live := c.readSafety(ctx, before, gone)
	meas["safety_after"], meas["live_pods_after"] = after, live
	c.check("verdict_verified", after == "verified" && matches, "latest safety verdict %q for the configured image digest (digest matches: %v; before the run: %q)", after, matches, before)
	c.check("no_live_pods", live == 0, "%d pods still live", live)
	var job struct {
		State     string  `json:"state"`
		ErrorCode *string `json:"error_code"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/jobs/"+url.PathEscape(c.rep.JobID), nil, nil, &job); err == nil {
		meas["take_final_state"] = job.State // the canary take is never handed out; it ends never-ran
		c.rep.FinalState = job.State
		if job.ErrorCode != nil {
			c.rep.ErrorCode = *job.ErrorCode
		}
	}
	c.checkSettled(ctx, meas)
	for _, ck := range c.rep.Checks {
		if !ck.OK {
			return c.finish("fail", ExitFail, "check failed: %s (%s)", ck.Name, ck.Note)
		}
	}
	return c.finish("pass", ExitPass, "")
}

// readSafety reads the pod backend's safety verdict and live pod count. When the pod is gone it waits up to podVerdictGrace for the
// verdict to change from `before` (the loop writes it a step after the pod is confirmed gone).
func (c *canary) readSafety(ctx context.Context, before string, gone bool) (verdict string, digestMatches bool, live int) {
	deadline := c.o.Clock.Now().Add(podVerdictGrace)
	for {
		p, err := c.readProvider(ctx)
		if err == nil && p.Pods != nil {
			verdict, digestMatches, live = p.Pods.Safety, p.Pods.SafetyDigestMatches, len(p.Pods.Live)
			if !gone || verdict != before {
				return
			}
		}
		if !c.o.Clock.Now().Before(deadline) || c.sleep(ctx, c.o.SweepEvery) != nil {
			return
		}
	}
}
