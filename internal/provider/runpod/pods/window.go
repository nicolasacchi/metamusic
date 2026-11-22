package pods

import (
	"context"
	"database/sql"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Bulk windows (D-BULK-11, C396.6): the operator arms the backend; the loop disarms it again after MM_POD_WINDOW_MAX from arming
// (`window_ended`), or after 30 minutes with no pod batch active or awaiting confirmation and no live pod (`window_idle`). Nothing here arms.

// windowOver reports whether the armed window has run its maximum.
func (l *Lifecycle) windowOver(st provider.State) bool {
	return !l.o.NoWindows && st.Armed && l.o.WindowMax > 0 && !st.ArmedChangedAt.IsZero() && !l.now().Before(st.ArmedChangedAt.Add(l.o.WindowMax))
}

// windows disarms the backend when its window ended or went idle. With the backend disarmed it does nothing.
func (l *Lifecycle) windows(ctx context.Context, st provider.State) error {
	if !st.Armed || l.o.Disarm == nil || l.o.NoWindows {
		return nil
	}
	reason := ""
	if l.windowOver(st) {
		// the window end is derived from the arming time, which a disarm replaces: disarm only once no pod lives, so a stop that failed
		// is still retried against the window (a live pod would otherwise outlive the approved maximum)
		live, err := AllLiveRows(ctx, l.o.Store.R)
		if err != nil || len(live) > 0 {
			return err
		}
		reason = "window_ended"
	} else {
		busy, last, err := l.activity(ctx)
		if err != nil {
			return err
		}
		if busy {
			return nil
		}
		if last.Before(st.ArmedChangedAt) {
			last = st.ArmedChangedAt
		}
		if !l.now().Before(last.Add(idleWindow)) {
			reason = "window_idle"
		}
	}
	if reason == "" {
		return nil
	}
	return l.o.Disarm(ctx, reason)
}

// activity reports whether a pod batch is active or awaiting confirmation or a pod is live, and when the backend last had any (the latest
// finished pod batch or terminated pod).
func (l *Lifecycle) activity(ctx context.Context) (busy bool, last time.Time, err error) {
	var n int
	if err = l.o.Store.R.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM batches WHERE backend = ?1 AND state IN ('awaiting_confirmation','active'))
		+ (SELECT COUNT(*) FROM pods WHERE state <> 'terminated')`, l.o.Backend).Scan(&n); err != nil || n > 0 {
		return n > 0, time.Time{}, err
	}
	var at sql.NullString
	if err = l.o.Store.R.QueryRowContext(ctx, `SELECT MAX(x) FROM (SELECT MAX(finished_at) x FROM batches WHERE backend = ?1 UNION ALL SELECT MAX(terminated_at) FROM pods)`,
		l.o.Backend).Scan(&at); err != nil {
		return false, time.Time{}, err
	}
	if at.Valid {
		last, err = store.ParseTime(at.String)
	}
	return false, last, err
}
