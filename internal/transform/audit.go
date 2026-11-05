package transform

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/canonjson"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

// The runner's terminal transitions write system audit rows `transform_done`, `transform_failed` and `transform_cancelled` with
// cpu_ms (C212.7). They are not in audit.SystemOps (that closed list is the money ledger of M3, pinned by its own test); like
// those rows they carry no body, only the sha256 of the canonical details, and cost 0. The row is written in the same
// transaction as the state change, so it exists exactly when the transition committed.

// Audit ops of the runner.
const (
	AuditDone      = "transform_done"
	AuditFailed    = "transform_failed"
	AuditCancelled = "transform_cancelled"
)

// auditDetails is hashed, never stored: ids, the kind, the CPU used and the stable code.
type auditDetails struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	CPUMS int64  `json:"cpu_ms"`
	Code  string `json:"code,omitempty"`
}

// systemEntry builds the audit row of a terminal transition.
func systemEntry(op string, at time.Time, d auditDetails, dur time.Duration) (audit.Entry, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return audit.Entry{}, err
	}
	canon, err := canonjson.Canonical(raw)
	if err != nil {
		return audit.Entry{}, err
	}
	sum := sha256.Sum256(canon)
	e := audit.Entry{
		At: at, Principal: auth.System("transform"), Transport: reqinfo.Runner, Op: op, Method: audit.SystemMethod,
		ArgsSHA256: hex.EncodeToString(sum[:]), Status: 200, Idempotency: audit.IdemNone, Duration: dur, CostMicroUSD: 0,
	}
	if op == AuditDone {
		e.CreatedIDs = []string{d.ID}
	}
	if op == AuditFailed {
		e.Status, e.ErrorCode = 500, d.Code
	}
	return e, nil
}

// auditTerminal writes the row inside tx. It deliberately uses a context without the request's idempotency marks: this is the
// runner's own row, and marking the request audited would make the gate skip the operation's row (cancel_transform).
func auditTerminal(tx *sql.Tx, op string, at time.Time, d auditDetails, dur time.Duration) error {
	e, err := systemEntry(op, at, d, dur)
	if err != nil {
		return err
	}
	return audit.InsertTx(context.Background(), tx, e)
}
