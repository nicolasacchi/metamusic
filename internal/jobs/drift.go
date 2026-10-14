package jobs

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// DriftFields lists the endpoint fields checked against the desired configuration (C76.4,
// TestDrift_FieldList). The backend compares the nested ones against runpod.DesiredSpec; the reconciler
// itself re-checks LocalDriftFields from the swept configuration.
var DriftFields = []string{
	"type", "scaler.type", "scaler.queueDelay", "timeout", "gpu.count", "gpu.minCudaVersion", "gpu.allowedCudaVersions",
	"gpu.selection", "flashboot", "workers.min", "workers.max", "workers.idleTimeout", "image", "env", "requestUrls",
}

// LocalDriftFields is the subset LocalDrift evaluates without the backend's desired spec.
var LocalDriftFields = []string{"type", "scaler.type", "scaler.queueDelay", "timeout", "gpu.count", "workers.min", "workers.max", "workers.idleTimeout", "requestUrls"}

// MaxIdleTimeoutS is the largest idleTimeout the endpoint may have (C76.4).
const MaxIdleTimeoutS = 60

// LocalDrift compares the swept endpoint with what the gate configuration alone determines.
func LocalDrift(cfg provider.GateConfig, e provider.EndpointConfig) []provider.Drift {
	var out []provider.Drift
	add := func(field, want, got string) {
		if want != got {
			out = append(out, provider.Drift{Field: field, Want: want, Got: got})
		}
	}
	add("type", "QUEUE", e.Type)
	add("scaler.type", "QUEUE_DELAY", e.ScalerType)
	add("scaler.queueDelay", strconv.Itoa(cfg.ColdStartS+60), e.ScalerValue)
	add("timeout", strconv.FormatInt(cfg.MaxRuntime.Milliseconds(), 10), strconv.FormatInt(e.TimeoutMS, 10))
	add("gpu.count", "1", strconv.Itoa(e.GPUCount))
	add("workers.min", "0", strconv.Itoa(e.WorkersMin))
	if e.WorkersMax < 0 || e.WorkersMax > provider.MaxWorkers {
		out = append(out, provider.Drift{Field: "workers.max", Want: "0.." + strconv.Itoa(provider.MaxWorkers), Got: strconv.Itoa(e.WorkersMax)})
	}
	switch {
	case e.IdleTimeoutS == nil:
		out = append(out, provider.Drift{Field: "workers.idleTimeout", Want: "<=" + strconv.Itoa(MaxIdleTimeoutS), Got: "absent"})
	case *e.IdleTimeoutS > MaxIdleTimeoutS || *e.IdleTimeoutS < 1:
		out = append(out, provider.Drift{Field: "workers.idleTimeout", Want: "<=" + strconv.Itoa(MaxIdleTimeoutS), Got: strconv.Itoa(*e.IdleTimeoutS)})
	}
	for _, name := range slices.Sorted(mapKeys(e.RequestURLs)) {
		u, err := url.Parse(e.RequestURLs[name])
		if err != nil || !strings.EqualFold(u.Hostname(), "api.runpod.ai") {
			out = append(out, provider.Drift{Field: "requestUrls", Want: "api.runpod.ai", Got: "other host"})
			break
		}
	}
	return out
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func mergeDrift(a, b []provider.Drift) []provider.Drift {
	seen := map[string]bool{}
	var out []provider.Drift
	for _, d := range slices.Concat(a, b) {
		if !seen[d.Field] {
			seen[d.Field] = true
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(x, y provider.Drift) int { return strings.Compare(x.Field, y.Field) })
	return out
}

// handleDrift reverts what the keys allow and tells the operator either way (C76.4). A raised idle
// timeout or workers.max relative to a running batch's frozen quote is named in the message; the claim
// itself already ignores or tops up for it.
func (r *Reconciler) handleDrift(ctx context.Context, rep provider.SweepReport) {
	drift := mergeDrift(rep.Drift, nil)
	if rep.Endpoint != nil {
		drift = mergeDrift(rep.Drift, LocalDrift(r.o.GateCfg, *rep.Endpoint))
	}
	r.mu.Lock()
	r.drift = drift
	r.mu.Unlock()
	fields := make([]string, 0, len(drift)+2)
	for _, d := range drift {
		fields = append(fields, d.Field)
	}
	above := ""
	if rep.Endpoint != nil {
		if above = r.aboveFrozen(ctx, *rep.Endpoint); above != "" {
			fields = append(fields, above)
		}
	}
	if len(fields) == 0 {
		return
	}
	reverted := false
	if len(drift) > 0 && r.o.Backend.Capabilities().RevertDrift {
		err := r.o.Backend.RevertDrift(ctx, drift)
		reverted = err == nil
		r.auditAfter(ctx, "reconciler:revert_drift", map[string]any{"fields": fields}, err != nil)
	}
	r.notifyOnce(ctx, "drift:"+strings.Join(fields, ",")+strconv.FormatBool(reverted), 0, NoteConfigDrift(r.o.PublicURL, fields, reverted))
}

// aboveFrozen names an endpoint whose idle timeout or workers.max exceeds a live batch's frozen quote.
func (r *Reconciler) aboveFrozen(ctx context.Context, e provider.EndpointConfig) string {
	var idle, workers int
	if err := r.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(idle_s), 0), COALESCE(MAX(workers_max), 0) FROM batches WHERE state = 'active'`).Scan(&idle, &workers); err != nil {
		return ""
	}
	var parts []string
	if e.IdleTimeoutS != nil && idle > 0 && *e.IdleTimeoutS > idle {
		parts = append(parts, "idle timeout above a running batch")
	}
	if workers > 0 && e.WorkersMax > workers {
		parts = append(parts, "workers.max above a running batch")
	}
	return strings.Join(parts, ", ")
}
