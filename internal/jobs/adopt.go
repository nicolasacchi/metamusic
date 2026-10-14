package jobs

import "context"

// adopt takes over the jobs of a dead runner at the start of a pass (C76.1) and records it: attempts that
// were `pending` when the process died are now `unknown` ghosts, and their jobs went back through the
// resubmit path behind an ambiguous top-up. `submitted` attempts are simply polled again by the runner;
// a 404 after downtime is `unknown`, never a queue expiry (classify).
func (r *Reconciler) adopt(ctx context.Context) error {
	got, err := r.o.Runner.adoptJobs(ctx)
	if err != nil {
		return err
	}
	if len(got) == 0 {
		return nil
	}
	ghosts := 0
	for _, a := range got {
		ghosts += a.Ghosts
	}
	r.auditAfter(ctx, "reconciler:adopt", map[string]any{"jobs": len(got)}, false)
	if ghosts > 0 {
		r.auditAfter(ctx, "reconciler:ghost", map[string]any{"attempts": ghosts}, false)
	}
	return nil
}
