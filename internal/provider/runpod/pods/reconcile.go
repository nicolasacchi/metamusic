package pods

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// reconcilePod advances one live pod by one step (C396.3). The writer of pod states is this loop alone; every transition is a fenced
// write, and every network call is made after the lease was confirmed with margin.
func (l *Lifecycle) reconcilePod(ctx context.Context, row Row, st provider.State) error {
	switch row.State {
	case StateIntent:
		switch {
		case !l.keyPresent():
			return l.terminateUnsent(ctx, row, StateIntent, codeNoKey, "no_key")
		case !st.Armed || !st.Enabled || st.Tripped || l.windowOver(st):
			// the window closed between the intent and the request: nothing is sent (the reserve stays in the pot and settles at the finish)
			return l.terminateUnsent(ctx, row, StateIntent, codeWindowClosed, "window_closed")
		}
		return l.postCreate(ctx, row)
	case StateCreating:
		// a request cannot be in flight inside a tick: this row was left by a crash between "creating" and the reply, so the create may
		// have happened. It is never replayed; the name decides.
		return l.orphan(ctx, row, codeAmbiguous, "create_interrupted")
	}
	if !l.keyPresent() {
		return l.keyAbsent(ctx, row)
	}
	if err := l.checkTrips(ctx, row); err != nil {
		return err
	}
	switch {
	case row.State == StateTerminating || row.State == StateOrphaned:
		return l.drain(ctx, row)
	case row.Kind == KindCanary:
		return l.watchCanary(ctx, row)
	}
	return l.watch(ctx, row, st)
}

// keyAbsent: the pod key was removed while a pod lives. Nothing can be called, so nothing is acted on; the operator is told once (the pod
// still ends by its own deadline and dead-man). Alerts only.
func (l *Lifecycle) keyAbsent(ctx context.Context, row Row) error {
	l.mu.Lock()
	noted := l.keyNoted[row.ID]
	l.keyNoted[row.ID] = true
	l.mu.Unlock()
	if noted {
		return nil
	}
	l.o.Log.Warn("pod key absent while a pod is live", "pod", row.Name, "state", row.State)
	return l.fenced(ctx, func(tx *sql.Tx) error {
		return AppendEvent(ctx, tx, row.ID, Event{At: l.now(), Event: "key_absent", Detail: map[string]any{"state": row.State}})
	})
}

// checkTrips applies the two time-based trips of C396.3 to a live pod: still listed at deadline + PodOverrunGraceS, and not confirmed gone by
// the dead-man plus a minute after its last sign of life.
func (l *Lifecycle) checkTrips(ctx context.Context, row Row) error {
	now := l.now()
	if !now.Before(row.DeadlineAt.Add(spend.PodOverrunGraceS * time.Second)) {
		if err := l.trip(ctx, row.ID, TripOverrun, map[string]any{"pod": row.Name, "state": row.State}); err != nil {
			return err
		}
	}
	if row.State != StateTerminating && row.State != StateOrphaned {
		return nil
	}
	base := row.CreateSentAt
	switch {
	case row.LastPollAt != nil:
		base = row.LastPollAt
	case row.RunningSeenAt != nil:
		base = row.RunningSeenAt
	}
	if base != nil && !now.Before(base.Add(l.o.Deadman+time.Minute)) {
		return l.trip(ctx, row.ID, TripDeadman, map[string]any{"pod": row.Name, "state": row.State})
	}
	return nil
}

// observe reads the pod at RunPod. gone: it does not exist (or is TERMINATED). A refused key trips pod_access.
func (l *Lifecycle) observe(ctx context.Context, row Row) (pod Pod, gone bool, err error) {
	if row.ProviderRef == "" {
		return Pod{}, false, nil
	}
	if err := l.networkOK(ctx); err != nil {
		return Pod{}, false, err
	}
	pod, err = l.o.API.GetPod(ctx, row.ProviderRef)
	switch {
	case errors.Is(err, ErrPodGone):
		return pod, true, nil
	case err != nil:
		l.accessErr(ctx, row.ID, err)
		return Pod{}, false, err
	}
	return pod, false, nil
}

// watch is one step of a booting, ready or busy batch pod.
func (l *Lifecycle) watch(ctx context.Context, row Row, st provider.State) error {
	now := l.now()
	pod, gone, err := l.observe(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrLeaseLost) {
			return err
		}
		l.o.Log.Warn("pod observe", "pod", row.Name, "err", redact.Error(err))
		return nil // a failed read changes nothing; the orphan rule uses the poll clock, not this call
	}
	if gone {
		code := codeNeverSeen
		if row.RunningSeenAt != nil {
			code = codePodState
		}
		return l.vanished(ctx, row, code)
	}
	if pod.Status == StatusRunning && row.RunningSeenAt == nil {
		if err := l.fenced(ctx, func(tx *sql.Tx) error { return MarkRunningSeen(ctx, tx, row.ID, now) }); err != nil {
			return err
		}
	}
	if pod.Status == StatusExited || pod.Status == StatusError {
		return l.toTerminating(ctx, row, codePodState, "pod_"+strings.ToLower(pod.Status), nil, true)
	}
	if row.State == StateBooting && pod.Priced && pod.CostPPH > row.PricePPH {
		// the pod bills more than the price the batch was reserved at: delete it at once
		if err := l.fenced(ctx, func(tx *sql.Tx) error {
			return SetReportedPrice(ctx, tx, row.ID, int64(pod.CostPPH), now)
		}); err != nil {
			return err
		}
		if err := l.toTerminating(ctx, row, codePrice, "price_mismatch", map[string]any{"reported_micro_usd": int64(pod.CostPPH), "frozen_micro_usd": int64(row.PricePPH)}, true); err != nil {
			return err
		}
		return l.trip(ctx, row.ID, TripPrice, map[string]any{"pod": row.Name})
	}
	if row.State != StateBooting && row.LastReported == "booting" {
		return l.restart(ctx, row)
	}
	if row.FirstPollAt != nil && row.LastPollAt != nil && now.Sub(*row.LastPollAt) >= l.o.OrphanAfter {
		return l.orphan(ctx, row, codeWorkerLost, "orphaned")
	}
	if reason := l.stopReason(ctx, row, st); reason != "" {
		return l.stop(ctx, row, reason)
	}
	switch row.State {
	case StateBooting:
		return l.boot(ctx, row)
	case StateReady, StateBusy:
		return l.syncBusy(ctx, row)
	}
	return nil
}

// boot promotes a booting pod that reported its weights loaded, or ends one that did not in time.
func (l *Lifecycle) boot(ctx context.Context, row Row) error {
	now := l.now()
	ready, err := BootWeightsReady(ctx, l.o.Store.R, row.ID)
	if err != nil {
		return err
	}
	if ready && row.LastReported != "booting" { // after a restart boot_json is the old container's: wait for the new report
		return l.fenced(ctx, func(tx *sql.Tx) error {
			return Transition(ctx, tx, row.ID, StateBooting, StateReady, Move{At: now, Event: "ready"})
		})
	}
	if row.CreateSentAt != nil && !now.Before(row.CreateSentAt.Add(time.Duration(l.o.BootS+bootGraceS)*time.Second)) {
		return l.toTerminating(ctx, row, codeBootTimeout, "boot_timeout", nil, true)
	}
	return nil
}

// syncBusy keeps ready and busy in step with the pod's open assignment.
func (l *Lifecycle) syncBusy(ctx context.Context, row Row) error {
	var open int
	if err := l.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM pod_assignments WHERE pod_id = ? AND handed_at IS NOT NULL AND done_at IS NULL`, row.ID).Scan(&open); err != nil {
		return err
	}
	to := ""
	switch {
	case row.State == StateReady && open > 0:
		to = StateBusy
	case row.State == StateBusy && open == 0:
		to = StateReady
	}
	if to == "" {
		return nil
	}
	return l.fenced(ctx, func(tx *sql.Tx) error {
		return Transition(ctx, tx, row.ID, row.State, to, Move{At: l.now()})
	})
}

// restart handles a `booting` report from a pod that was ready or busy: the container restarted. The first restart sends the pod back to
// booting; a second ends it (RESTART_LOOP). The poll handler already recorded the handed take lost.
func (l *Lifecycle) restart(ctx context.Context, row Row) error {
	now := l.now()
	n := row.Restarts + 1
	if err := l.fenced(ctx, func(tx *sql.Tx) error { return SetRestarts(ctx, tx, row.ID, n, now) }); err != nil {
		return err
	}
	if n >= 2 {
		return l.toTerminating(ctx, row, codeRestartLoop, "restart_loop", map[string]any{"restarts": n}, true)
	}
	return l.fenced(ctx, func(tx *sql.Tx) error {
		return Transition(ctx, tx, row.ID, row.State, StateBooting, Move{At: now, Event: "restarted", Code: "WORKER_RESTARTED", Detail: map[string]any{"restarts": n}})
	})
}

// stopReason says why a pod must stop now ("" = it must not): the armed window is over, the batch's deadline is near, the batch has no unfinished
// job, or it is ready, has sat idle for IdleS and no job of the batch is live or soon claimable (C396.3).
func (l *Lifecycle) stopReason(ctx context.Context, row Row, st provider.State) string {
	now := l.now()
	switch {
	case l.windowOver(st):
		return "window_ended"
	case !now.Before(row.DeadlineAt.Add(-30 * time.Second)):
		return "deadline"
	}
	var unfinished, live, soon int
	err := l.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(state NOT IN ('completed','failed','cancelled','expired')), 0),
		COALESCE(SUM(state NOT IN ('completed','failed','cancelled','expired','awaiting_confirmation','queued')), 0),
		COALESCE(SUM(state = 'queued' AND next_attempt_at <= ?), 0) FROM jobs WHERE batch_id = ?`,
		store.FormatTime(now.Add(time.Duration(l.o.IdleS)*time.Second)), row.BatchID).Scan(&unfinished, &live, &soon)
	if err != nil {
		l.o.Log.Warn("pod stop rule", "err", redact.Error(err))
		return ""
	}
	if unfinished == 0 {
		return "batch_done"
	}
	if row.State != StateReady || live > 0 || soon > 0 || l.o.Queue.Len(row.BatchID) > 0 {
		return ""
	}
	if since, err := l.idleSince(ctx, row); err == nil && !now.Before(since.Add(time.Duration(l.o.IdleS)*time.Second)) {
		return "idle"
	}
	return ""
}

// idleSince is when the pod last had work: the later of its last move to ready and the end of its last assignment.
func (l *Lifecycle) idleSince(ctx context.Context, row Row) (time.Time, error) {
	var ev, done sql.NullString
	err := l.o.Store.R.QueryRowContext(ctx, `SELECT (SELECT MAX(at) FROM pod_events WHERE pod_id = ?1 AND to_state = 'ready'),
		(SELECT MAX(done_at) FROM pod_assignments WHERE pod_id = ?1)`, row.ID).Scan(&ev, &done)
	if err != nil {
		return time.Time{}, err
	}
	since := row.CreateSentAt
	best := time.Time{}
	if since != nil {
		best = *since
	}
	for _, s := range []sql.NullString{ev, done} {
		if !s.Valid {
			continue
		}
		if t, err := store.ParseTime(s.String); err == nil && t.After(best) {
			best = t
		}
	}
	return best, nil
}

// stop answers `stop` on the pod's next poll (state terminating) and deletes it after 60 s if it is still there (drain).
func (l *Lifecycle) stop(ctx context.Context, row Row, reason string) error {
	code := ""
	if reason == "deadline" || reason == "window_ended" {
		code = codeDeadline
		if reason == "window_ended" {
			code = codeWindowEnded
		}
	}
	return l.toTerminating(ctx, row, code, "stop", map[string]any{"reason": reason}, false)
}

// toTerminating moves a pod to terminating, records its handed takes lost, and with now set deletes it at once.
func (l *Lifecycle) toTerminating(ctx context.Context, row Row, code, event string, detail map[string]any, now bool) error {
	at := l.now()
	var lost []string
	err := l.fenced(ctx, func(tx *sql.Tx) error {
		var err error
		if lost, err = MarkLost(ctx, tx, row.ID, codeWorkerLost, at); err != nil {
			return err
		}
		return Transition(ctx, tx, row.ID, row.State, StateTerminating, Move{At: at, Event: event, Code: code, Detail: detail})
	})
	if err != nil {
		return err
	}
	l.clearCancels(lost)
	if !now {
		return nil
	}
	row.State = StateTerminating
	return l.deletePod(ctx, row)
}

// orphan gives a pod up: its poll clock stopped (or its create outcome is unknown), so it is deleted and its handed takes are lost.
func (l *Lifecycle) orphan(ctx context.Context, row Row, code, event string) error {
	at := l.now()
	var lost []string
	err := l.fenced(ctx, func(tx *sql.Tx) error {
		var err error
		if lost, err = MarkLost(ctx, tx, row.ID, codeWorkerLost, at); err != nil {
			return err
		}
		return Transition(ctx, tx, row.ID, row.State, StateOrphaned, Move{At: at, Event: event, Code: code})
	})
	if err != nil {
		return err
	}
	l.clearCancels(lost)
	row.State = StateOrphaned
	if !l.keyPresent() {
		return nil
	}
	return l.deletePod(ctx, row)
}

func (l *Lifecycle) clearCancels(ids []string) {
	for _, id := range ids {
		l.o.Queue.ClearCancel(id)
	}
}

// deletePod sends DELETE once per call (a 404 is success) and records `delete_sent` the first time.
func (l *Lifecycle) deletePod(ctx context.Context, row Row) error {
	if row.ProviderRef == "" {
		return nil // an unknown id: the name sweep finds the pod
	}
	if err := l.networkOK(ctx); err != nil {
		return err
	}
	if err := l.o.API.DeletePod(ctx, row.ProviderRef); err != nil {
		l.accessErr(ctx, row.ID, err)
		return err
	}
	if n, err := EventCount(ctx, l.o.Store.R, row.ID, "delete_sent"); err != nil || n > 0 {
		return err
	}
	return l.fenced(ctx, func(tx *sql.Tx) error {
		return AppendEvent(ctx, tx, row.ID, Event{At: l.now(), Event: "delete_sent"})
	})
}

// drain drives a terminating or orphaned pod to `terminated`: it finds the pod when its id is unknown, deletes it when due, and confirms it
// is gone (GET 404 or TERMINATED).
func (l *Lifecycle) drain(ctx context.Context, row Row) error {
	now := l.now()
	if row.ProviderRef == "" {
		ref, err := l.findByName(ctx, row.Name)
		if err != nil {
			return err
		}
		if ref == "" {
			// not listed: if the create ever succeeded the pod is gone already. Wait out the listing's lag before saying so.
			if row.CreateSentAt == nil || !now.Before(row.CreateSentAt.Add(2*time.Minute)) {
				return l.finish(ctx, row)
			}
			return nil
		}
		if err := l.fenced(ctx, func(tx *sql.Tx) error { return SetProviderRef(ctx, tx, row.ID, ref, now) }); err != nil {
			return err
		}
		row.ProviderRef = ref
	}
	if row.State == StateOrphaned || l.deleteDue(ctx, row) {
		if err := l.deletePod(ctx, row); err != nil {
			return err
		}
	}
	_, gone, err := l.observe(ctx, row)
	if err != nil || !gone {
		return err
	}
	return l.finish(ctx, row)
}

// deleteDue: a terminating pod is given 60 s to obey the stop, unless a DELETE already went out.
func (l *Lifecycle) deleteDue(ctx context.Context, row Row) bool {
	if n, err := EventCount(ctx, l.o.Store.R, row.ID, "delete_sent"); err != nil || n > 0 {
		return true
	}
	return row.TerminateSentAt == nil || !l.now().Before(row.TerminateSentAt.Add(deleteAfterStopS*time.Second))
}

// findByName looks a pod up in the listing by its name.
func (l *Lifecycle) findByName(ctx context.Context, name string) (string, error) {
	if err := l.networkOK(ctx); err != nil {
		return "", err
	}
	ps, err := l.o.API.ListPods(ctx)
	if err != nil {
		l.accessErr(ctx, name, err)
		return "", err
	}
	for _, p := range ps {
		if p.Name == name && p.Status != StatusTerminated {
			return p.ID, nil
		}
	}
	return "", nil
}

// vanished handles a pod found gone while it was still believed to run: it ends through terminating, with its takes lost.
func (l *Lifecycle) vanished(ctx context.Context, row Row, code string) error {
	if err := l.toTerminating(ctx, row, code, "gone_unexpectedly", nil, false); err != nil {
		return err
	}
	row.State = StateTerminating
	return l.finish(ctx, row)
}

// finish records that the pod is gone: gone_seen_at, the lifetime (create_sent_at to the first sighting of its absence, rounded up to the
// minute, D-BULK-7) and `terminated`. A canary's verdict is written in the same transaction.
func (l *Lifecycle) finish(ctx context.Context, row Row) error {
	now := l.now()
	gone := now
	if row.GoneSeenAt != nil {
		gone = *row.GoneSeenAt
	}
	life := 0
	if row.CreateSentAt != nil {
		life = ceilMinute(gone.Sub(*row.CreateSentAt))
	}
	var v *canaryVerdict
	if row.Kind == KindCanary {
		x := l.verdictFor(ctx, row, gone)
		v = &x
	}
	var lost []string
	err := l.fenced(ctx, func(tx *sql.Tx) error {
		if err := MarkGoneSeen(ctx, tx, row.ID, gone); err != nil {
			return err
		}
		var err error
		if lost, err = MarkLost(ctx, tx, row.ID, codeWorkerLost, now); err != nil {
			return err
		}
		if v != nil {
			if err := AppendSafety(ctx, tx, SafetyEntry{Kind: "canary", ImageDigest: row.ImageDigest, Verdict: v.verdict, CreatedBy: "system:bulk", At: now, Detail: v.detail}); err != nil {
				return err
			}
		}
		return Transition(ctx, tx, row.ID, row.State, StateTerminated, Move{At: now, Event: "gone", LifetimeS: &life})
	})
	if err != nil {
		return err
	}
	l.clearCancels(lost)
	if v == nil {
		return nil
	}
	l.audit(ctx, OpPodCanary, map[string]any{"verdict": v.verdict}, 0, v.verdict == "failed")
	rows, err := PodsOfBatch(ctx, l.o.Store.R, row.BatchID)
	if err != nil {
		return err
	}
	return l.endSupply(ctx, row.BatchID, rows, codeDeadline, "canary_done") // the canary's take is never handed out: it ends never-ran
}

// ceilMinute rounds a duration up to whole minutes and returns seconds.
func ceilMinute(d time.Duration) int {
	s := int(d / time.Second)
	if d%time.Second > 0 {
		s++
	}
	if s <= 0 {
		return 0
	}
	return (s + 59) / 60 * 60
}
