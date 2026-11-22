package pods

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The self-termination canary (C399.10, D-BULK-12). It is one pod of a one-take batch, created with a poll link nobody can verify and a dead-man
// of 120 s: every poll answers 403, so the agent never sees a 2xx and its Terminator must delete the pod. The loop watches the clock and writes
// the verdict; it never hands the take out (a canary pod gets no hand-out, no boot timeout and no orphan action).

type canaryVerdict struct {
	verdict string
	detail  map[string]any
}

var terminatorOKRE = regexp.MustCompile(`terminator: step ([123]) ok`)

// canaryVerified reports whether the latest canary row for the configured image digest says `verified` (D-BULK-12).
func (l *Lifecycle) canaryVerified(ctx context.Context) (bool, error) {
	var verdict string
	err := l.o.Store.R.QueryRowContext(ctx, `SELECT verdict FROM pod_safety_log WHERE kind = 'canary' AND image_digest = ? ORDER BY id DESC LIMIT 1`, l.digest()).Scan(&verdict)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return verdict == "verified", err
}

// watchCanary is one step of a booting canary pod: it records RUNNING and the pod's disappearance; a pod still listed 180 s after RUNNING fails
// the canary (and is deleted), one that never ran by the deadline is inconclusive (and is deleted a minute after it).
func (l *Lifecycle) watchCanary(ctx context.Context, row Row) error {
	now := l.now()
	pod, gone, err := l.observe(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrLeaseLost) {
			return err
		}
		l.o.Log.Warn("canary observe", "err", redact.Error(err))
		return nil
	}
	if gone {
		return l.vanished(ctx, row, "") // the verdict is the record; no error code
	}
	if pod.Status == StatusRunning && row.RunningSeenAt == nil {
		if err := l.fenced(ctx, func(tx *sql.Tx) error { return MarkRunningSeen(ctx, tx, row.ID, now) }); err != nil {
			return err
		}
		row.RunningSeenAt = &now
	}
	if row.RunningSeenAt != nil {
		if !now.Before(row.RunningSeenAt.Add(canaryVerdictS * time.Second)) {
			return l.endCanary(ctx, row, "canary_failed")
		}
		return nil
	}
	if !now.Before(row.DeadlineAt.Add(canaryDeleteS * time.Second)) {
		return l.endCanary(ctx, row, "canary_inconclusive")
	}
	return nil
}

// endCanary reads the log tail once, deletes the pod and moves it to terminating; drain confirms it gone and finish writes the verdict.
func (l *Lifecycle) endCanary(ctx context.Context, row Row, event string) error {
	if err := l.toTerminating(ctx, row, "", event, nil, false); err != nil {
		return err
	}
	row.State = StateTerminating
	l.readCanaryLog(ctx, row)
	err := l.deletePod(ctx, row)
	if err != nil {
		// the verdict records whether the delete worked
		return l.fenced(ctx, func(tx *sql.Tx) error {
			return AppendEvent(ctx, tx, row.ID, Event{At: l.now(), Event: "delete_failed"})
		})
	}
	return nil
}

// readCanaryLog reads the container log once and records which Terminator step succeeded (`terminator: step <n> ok`), if any.
func (l *Lifecycle) readCanaryLog(ctx context.Context, row Row) {
	if n, err := EventCount(ctx, l.o.Store.R, row.ID, "terminator_step"); err != nil || n > 0 || row.ProviderRef == "" {
		return
	}
	lines, err := l.o.API.PodLogsTail(ctx, row.ProviderRef, 200)
	if err != nil {
		return
	}
	step := ""
	for _, ln := range lines {
		if m := terminatorOKRE.FindStringSubmatch(ln); m != nil && step == "" {
			step = m[1]
		}
	}
	// an event is written even when no step succeeded: it is also the record that the log was read, once
	_ = l.fenced(ctx, func(tx *sql.Tx) error {
		return AppendEvent(ctx, tx, row.ID, Event{At: l.now(), Event: "terminator_step", Detail: map[string]any{"step": step}})
	})
}

// verdictFor decides the canary's verdict when the pod is confirmed gone (gone is when that was first seen): `verified` iff it vanished within
// 120 s + 60 s of RUNNING without our DELETE, `failed` if our DELETE was needed, `inconclusive` if it never reached RUNNING.
func (l *Lifecycle) verdictFor(ctx context.Context, row Row, gone time.Time) canaryVerdict {
	l.readCanaryLog(ctx, row)
	deleted, _ := EventCount(ctx, l.o.Store.R, row.ID, "delete_sent")
	failedDelete, _ := EventCount(ctx, l.o.Store.R, row.ID, "delete_failed")
	var step string
	_ = l.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(json_extract(detail_json, '$.step'), '') FROM pod_events WHERE pod_id = ? AND event = 'terminator_step' ORDER BY id DESC LIMIT 1`, row.ID).Scan(&step)
	d := map[string]any{"deleted_by_server": deleted > 0, "delete_failed": failedDelete > 0, "terminator_step": step}
	v := "inconclusive"
	if row.RunningSeenAt != nil {
		s := int(gone.Sub(*row.RunningSeenAt) / time.Second)
		d["running_to_gone_s"] = s
		v = "failed"
		if deleted == 0 && s <= canaryVerdictS {
			v = "verified"
		}
	}
	return canaryVerdict{verdict: v, detail: d}
}

// nameSweep lists the pods (every 60 s while the key is present): an mm-* pod with no live row is deleted and trips pod_unknown; a live row
// whose create outcome was unknown learns its pod id here.
func (l *Lifecycle) nameSweep(ctx context.Context) error {
	now := l.now()
	l.mu.Lock()
	due := now.Sub(l.lastSweep) >= nameSweepEvery
	if due {
		l.lastSweep = now
	}
	l.mu.Unlock()
	if !due {
		return nil
	}
	if err := l.networkOK(ctx); err != nil {
		if errors.Is(err, store.ErrLeaseShort) {
			return nil // the heartbeat renews first; the next pass sweeps
		}
		return err
	}
	listed, err := l.o.API.ListPods(ctx)
	if err != nil {
		l.accessErr(ctx, "sweep", err)
		return err
	}
	rows, err := AllLiveRows(ctx, l.o.Store.R)
	if err != nil {
		return err
	}
	byName := map[string]Row{}
	byRef := map[string]bool{}
	for _, r := range rows {
		byName[r.Name] = r
		if r.ProviderRef != "" {
			byRef[r.ProviderRef] = true
		}
	}
	var errs []error
	unknown := 0
	for _, p := range listed {
		if !strings.HasPrefix(p.Name, namePrefix) || p.Status == StatusTerminated {
			continue
		}
		if r, ok := byName[p.Name]; ok {
			if r.ProviderRef == "" {
				errs = append(errs, l.fenced(ctx, func(tx *sql.Tx) error { return SetProviderRef(ctx, tx, r.ID, p.ID, now) }))
			}
			continue
		}
		if byRef[p.ID] {
			continue
		}
		unknown++
		if err := l.o.API.DeletePod(ctx, p.ID); err != nil {
			l.accessErr(ctx, "sweep", err)
			errs = append(errs, errors.New(redact.Error(err)))
		}
	}
	if unknown > 0 {
		errs = append(errs, l.trip(ctx, "sweep-"+now.Format(time.RFC3339), TripUnknown, map[string]any{"unknown_pods": unknown}))
	}
	return errors.Join(errs...)
}
