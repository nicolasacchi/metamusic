package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The runner half of the worker capability gate (C322.5): what a worker's refusal of a /2 job does to the gate, and what a job that
// needs a closed capability for too long ends with. The claim filter itself is in claimSQL (submit.go).

// CodeWorkerOutdated is the error code of a job whose needs the worker image cannot meet: the image answered KIND_UNAVAILABLE, or the
// needs stayed closed for WorkerOutdatedAfter while the job waited in the queue.
const CodeWorkerOutdated = "WORKER_OUTDATED"

// WorkerOutdatedAfter is how long a queued job may wait for its needs to open before it fails WORKER_OUTDATED and releases its reservation.
const WorkerOutdatedAfter = time.Hour

// OpAutoClose is the audit row of an automatic gate change.
const OpAutoClose = "runner:worker_contract_close"

func init() { audit.AddSystemOps(OpAutoClose) }

// Worker error codes that mean the image cannot serve the job's wire (C322.5, C321.8).
const (
	workerContractVersion  = "CONTRACT_VERSION"
	workerBadInput         = "BAD_INPUT"
	workerKindUnavailable  = "KIND_UNAVAILABLE"
	autoCloseChangedBy     = "system:runner"
	autoCloseReasonContext = "worker answered "
)

// gateOutcome maps a worker error code of a /2 job to the job's error code and whether it changes the gate.
func gateOutcome(workerCode string) (jobCode string, ok bool) {
	switch workerCode {
	case workerContractVersion, workerBadInput:
		return CodeWorkerContract, true
	case workerKindUnavailable:
		return CodeWorkerOutdated, true
	}
	return "", false
}

// workerErrorRaw is the worker's own error code of a failed attempt: from the provider status, else from the error output the
// transfer handler already recorded for the attempt.
func workerErrorRaw(ctx context.Context, x q, o *obs) string {
	if c, _ := workerErrorCode(o.st); c != "" {
		return c
	}
	var wc sql.NullString
	if err := x.QueryRowContext(ctx, `SELECT json_extract(detail_json, '$.worker_error') FROM job_events WHERE attempt_id = ? AND event = 'status' AND code = ?
		AND json_extract(detail_json, '$.worker_error') IS NOT NULL ORDER BY id LIMIT 1`, o.a.ID, CodeWorkerError).Scan(&wc); err != nil {
		return ""
	}
	return wc.String
}

// gateCode is the job code of a failed /2 attempt whose worker refused the wire, after the gate has been updated in this transaction.
// Anything else (a /1 job, another worker error) returns "".
func (r *Runner) gateCode(ctx context.Context, tx *sql.Tx, f store.LeaseFence, o *obs) (string, error) {
	if contractOf(o.j.Engine, o.j.JobRow) != provider.ContractV2 {
		return "", nil
	}
	wc := workerErrorRaw(ctx, tx, o)
	code, ok := gateOutcome(wc)
	if !ok {
		return "", nil
	}
	if err := r.autoClose(ctx, tx, f, o, wc); err != nil {
		return "", err
	}
	return code, nil
}

// autoClose appends the log row of C322.5 when the attempt ran on the image the gate is bound to. CONTRACT_VERSION and BAD_INPUT close
// every capability; KIND_UNAVAILABLE removes the capabilities the job needed beyond v2 (and any the worker's own caps list lacks).
func (r *Runner) autoClose(ctx context.Context, tx *sql.Tx, f store.LeaseFence, o *obs, workerCode string) error {
	backend := o.a.Backend
	row, have, err := provider.LatestContract(ctx, tx, backend)
	if err != nil {
		return err
	}
	var open []string // what is open now; empty for a real backend with no row, so there is nothing to close
	var digest string
	switch {
	case have:
		open, digest = row.Caps, row.ImageDigest
		// An attempt that ran on another image than the one the gate was opened for says nothing about it. An attempt whose image is
		// not known (the worker record had not arrived) is treated as the gate's image: closing wrongly costs the operator one reopen,
		// not closing wrongly costs a paid failure per job.
		if d := o.a.ImageDigest; d != "" && d != digest {
			return nil
		}
	case provider.IsFakeBackend(backend):
		open, digest = slices.Clone(provider.Caps), o.a.ImageDigest
	}
	if digest == "" {
		digest = "sha256:" + strings.Repeat("0", 64)
	}
	next := []string{}
	if workerCode == workerKindUnavailable {
		missing := slices.DeleteFunc(slices.Clone(o.j.Needs), func(c string) bool { return c == jobspec.CapV2 })
		if out, derr := provider.DecodeOutput(o.st.Output); derr == nil && len(out.Caps) > 0 {
			for _, c := range open {
				if !slices.Contains(out.Caps, c) && !slices.Contains(missing, c) {
					missing = append(missing, c)
				}
			}
		}
		for _, c := range open {
			if !slices.Contains(missing, c) {
				next = append(next, c)
			}
		}
	}
	if len(open) == 0 || slices.Equal(next, open) {
		return nil
	}
	closed := provider.MissingCaps(open, next)
	if err := provider.AppendContract(ctx, tx, provider.WorkerContractRow{Backend: backend, ImageDigest: digest, Caps: next, Via: provider.ContractViaAuto,
		AttemptID: o.a.ID, ChangedBy: autoCloseChangedBy, ChangedAt: f.Now, Reason: autoCloseReasonContext + workerCode}); err != nil {
		return err
	}
	if err := r.auditTx(ctx, tx, f, OpAutoClose, map[string]any{"backend": backend, "attempt": o.a.ID, "worker_code": workerCode, "closed": closed}, 0); err != nil {
		return err
	}
	r.queueNote(NoteWorkerContractClosed(r.o.PublicURL, backend, closed))
	return nil
}

// NoteWorkerContractClosed is "worker capabilities closed" (priority 5): the runner closed capabilities of a backend after its worker
// refused a job.
func NoteWorkerContractClosed(publicURL, backend string, closed []string) notify.Message {
	return notify.Message{Title: "Worker capabilities closed: " + backend, Priority: 5, Tags: []string{"rotating_light"},
		Body:  "The worker refused a job; closed: " + strings.Join(closed, ", ") + ". Roll out a matching image and reopen with metamusic runpod contract open.",
		Click: page(publicURL, "/jobs")}
}

// openCapsFor are the capabilities open for a backend now, read from its cached quote and the log (phaseFailQueued).
func (r *Runner) openCapsFor(ctx context.Context, x q, backend string) ([]string, error) {
	var quoteJSON, quoteAt sql.NullString
	err := x.QueryRowContext(ctx, `SELECT quote_json, quote_at FROM provider_state WHERE backend = ?`, backend).Scan(&quoteJSON, &quoteAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("jobs: read provider state: %w", err)
	}
	var qt *provider.Quote
	if quoteJSON.Valid {
		if d, derr := decodeQuote(quoteJSON.String, quoteAt.String); derr == nil {
			qt = &d
		}
	}
	return provider.WorkerCaps(ctx, x, backend, qt, r.o.Clock.Now())
}

// outdatedQueued reports whether a queued job's needs are closed and have been for WorkerOutdatedAfter: it waited that long, and the
// gate itself last changed that long ago (a capability just closed gives the operator the hour). Jobs with no needs never match.
func (r *Runner) outdatedQueued(ctx context.Context, x q, j *jobRec, now time.Time) (bool, error) {
	if len(j.Needs) == 0 || j.QueuedAt == nil || now.Before(j.QueuedAt.Add(WorkerOutdatedAfter)) {
		return false, nil
	}
	open, err := r.openCapsFor(ctx, x, j.Backend)
	if err != nil {
		return false, err
	}
	if len(provider.MissingCaps(j.Needs, open)) == 0 {
		return false, nil
	}
	if row, ok, err := provider.LatestContract(ctx, x, j.Backend); err != nil {
		return false, err
	} else if ok && now.Before(row.ChangedAt.Add(WorkerOutdatedAfter)) {
		return false, nil
	}
	return true, nil
}

// workerOutdatedDetail is the event detail of a job that failed because its needs stayed closed: the capabilities that were not open.
func workerOutdatedDetail(j *jobRec, open []string) map[string]any {
	return map[string]any{"missing": provider.MissingCaps(j.Needs, open)}
}
