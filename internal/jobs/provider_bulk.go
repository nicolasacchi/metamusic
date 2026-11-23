package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Bulk-window arming and disarming of the pod backend (C396.6). Kept apart from provider.go so the serial edit there is two lines.

// SystemActor is the changed_by of a backend the system disarms itself (a window that ended, a window that went idle).
const SystemActor = "system:bulk"

// OpBulkDisarm is the audit row of a system disarm; the app registers it with audit.AddSystemOps.
const OpBulkDisarm = "bulk:disarm"

// SystemAuditFunc records one audit row of a system action after its effect; the app builds it over audit.SystemEntry. nil = none.
type SystemAuditFunc func(ctx context.Context, op string, details map[string]any, cost money.MicroUSD, failed bool)

// UseSystemAudit wires the audit sink of SystemDisarm.
func (a *ProviderAdmin) UseSystemAudit(f SystemAuditFunc) {
	a.mu.Lock()
	a.sysAudit = f
	a.mu.Unlock()
}

// ErrBadSystemReason refuses a system disarm without a usable reason.
var ErrBadSystemReason = errors.New("jobs: a system disarm needs a reason of 1 to 500 characters")

// SystemDisarm disarms backend as changed_by `system:bulk` with the given reason (window_ended, window_idle), in one statement that
// only ever moves armed 1 -> 0 (the safe direction, so it needs no lease: a second process doing it is harmless). It reports whether
// the backend was armed. An unknown backend is provider.ErrNoBackend.
func (a *ProviderAdmin) SystemDisarm(ctx context.Context, backend, reason string) (bool, error) {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > 500 {
		return false, ErrBadSystemReason
	}
	if a.o.Store == nil {
		return false, errors.ErrUnsupported
	}
	g, _, _, err := a.resolve(backend)
	if err != nil {
		return false, err
	}
	name := g.Config().BackendName()
	if name == "" {
		return false, provider.ErrNoBackend
	}
	now := store.FormatTime(a.o.Clock.Now())
	res, err := a.o.Store.W.ExecContext(ctx, `UPDATE provider_state SET armed = 0, changed_by = ?, changed_at = ?, reason = ? WHERE backend = ? AND armed = 1`,
		SystemActor, now, reason, name)
	if err != nil {
		return false, fmt.Errorf("jobs: system disarm: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	a.mu.RLock()
	f := a.sysAudit
	a.mu.RUnlock()
	if f != nil {
		f(ctx, OpBulkDisarm, map[string]any{"backend": name, "reason": reason}, 0, false)
	}
	return true, nil
}

// ReasonPodProbeMissing is the disabled reason of arming runpod-pod without a usable probe row (C390.1).
const ReasonPodProbeMissing = "pod_probe_missing"

// CheckArmPod is the pod rule of CheckArm (C396.6): arming runpod-pod needs the latest probe row of pod_safety_log to be `ok`, or
// `too_broad` with the operator's explicit acknowledgment (`--accept-broad-pod-key`). Any other backend, including `fake-pod`,
// passes. The refusal is a GenerationDisabledError with reason pod_probe_missing.
func CheckArmPod(ctx context.Context, x Querier, backend string, acceptBroad bool) error {
	if backend != provider.BackendRunPodPod {
		return nil
	}
	var verdict string
	err := x.QueryRowContext(ctx, `SELECT verdict FROM pod_safety_log WHERE kind = 'probe' ORDER BY id DESC LIMIT 1`).Scan(&verdict)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return &GenerationDisabledError{Reason: ReasonPodProbeMissing}
	case err != nil:
		return err
	case verdict == "ok", verdict == "too_broad" && acceptBroad:
		return nil
	}
	return &GenerationDisabledError{Reason: ReasonPodProbeMissing}
}
