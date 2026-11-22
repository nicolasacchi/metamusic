package jobs

import "github.com/nicolasacchi/metamusic/internal/store"

// Seams the pod lifecycle loop (internal/provider/runpod/pods) is wired through by the app (C391.3, C396.7): the loop imports neither the
// runner nor the reconciler, so the lease predicate and the pod backend's Trip reach it as injected functions.

// Fence is the lease predicate carrier of a write that starts now: the loop's fenced writes confirm the runner lease through it.
func (r *Runner) Fence() store.LeaseFence { return r.fence() }

// For returns the reconciler of the named backend among r and its peers, or nil when there is none (a single-backend process has only r).
func (r *Reconciler) For(backend string) *Reconciler {
	if r == nil {
		return nil
	}
	if r.o.Backend != nil && r.o.Backend.Name() == backend {
		return r
	}
	for _, p := range r.peers {
		if p.o.Backend != nil && p.o.Backend.Name() == backend {
			return p
		}
	}
	return nil
}
