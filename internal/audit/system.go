package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/canonjson"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

// SystemOps is the closed list of runner, reconciler and CLI actions that move money or touch the provider
// and are therefore audit rows (M3 D-M3-22). Job state transitions are not audit rows (they are job_events).
// The prefix names the actor: runner:*, reconciler:* and cli:*.
var SystemOps = []string{
	"runner:submit", "runner:topup", "runner:cancel", "runner:batch_finish", "runner:fail_remainder",
	"reconciler:adopt", "reconciler:ghost", "reconciler:settle_time_bound", "reconciler:approval_expired",
	"reconciler:trip", "reconciler:purge_queue", "reconciler:scale_to_zero", "reconciler:revert_drift",
	"reconciler:billing_window", "reconciler:late_adjust", "reconciler:hold_release", "reconciler:upload_sweep",
	"reconciler:meter_billable_since",
	"cli:runpod-arm", "cli:runpod-disarm", "cli:runpod-reset-trip", "cli:runpod-provision", "cli:restore-disarm",
}

// SystemMethod is the method column of every system row (M2a's column is NOT NULL).
const SystemMethod = "SYSTEM"

var systemOpSet = func() map[string]bool {
	m := make(map[string]bool, len(SystemOps))
	for _, op := range SystemOps {
		m[op] = true
	}
	return m
}()

// IsSystemOp reports whether op is one of SystemOps.
func IsSystemOp(op string) bool { return systemOpSet[op] }

// SystemEntry builds the audit row of a runner, reconciler or CLI action. details is encoded as JSON, then
// canonicalised, and only its sha256 is stored (args_sha256), so a row never carries URLs, keys or refs.
// Fill values for M2a's NOT NULL columns: method SYSTEM, status 200 (done) or 500 (failed), duration as
// measured, idempotency none, client empty. runner and reconciler rows use transport runner; cli rows use cli.
// cost is the amount of D-M3-22 (the attempt's overhead reserve, a top-up, the settled total, ...).
func SystemEntry(op string, at time.Time, details any, dur time.Duration, cost money.MicroUSD, failed bool) (Entry, error) {
	if !IsSystemOp(op) {
		return Entry{}, fmt.Errorf("audit: %q is not a system operation (D-M3-22)", op)
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return Entry{}, err
	}
	canon, err := canonjson.Canonical(raw)
	if err != nil {
		return Entry{}, err
	}
	sum := sha256.Sum256(canon)
	actor, _, _ := strings.Cut(op, ":")
	e := Entry{
		At: at, Op: op, Method: SystemMethod, ArgsSHA256: hex.EncodeToString(sum[:]), Status: 200,
		Idempotency: IdemNone, Duration: dur, CostMicroUSD: cost,
	}
	if failed {
		e.Status, e.ErrorCode = 500, "SYSTEM_ACTION_FAILED"
	}
	switch actor {
	case "cli":
		e.Principal, e.Transport = auth.CLI, reqinfo.CLI
	default:
		e.Principal, e.Transport = auth.System(actor), reqinfo.Runner
	}
	return e, nil
}
