package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Event is one append-only job_events row (C75.7).
type Event struct {
	JobID, AttemptID string
	From, To         string
	Name, Code       string
	Detail           map[string]any
}

// Event names (C75.7).
const (
	EvCreated           = "created"
	EvClaim             = "claim"
	EvSubmit            = "submit"
	EvSubmitted         = "submitted"
	EvSubmitRetry       = "submit_retry"
	EvSubmitRefused     = "submit_refused"
	EvStatus            = "status"
	EvRunning           = "running"
	EvQueueExpired      = "queue_expired"
	EvResubmit          = "resubmit"
	EvUpload            = "upload"
	EvResultReceived    = "result_received"
	EvCompletedRemote   = "completed_remote"
	EvVerified          = "verified"
	EvIngested          = "ingested"
	EvEars              = "ears"
	EvCancelRequested   = "cancel_requested"
	EvCancelSent        = "cancel_sent"
	EvCancelConfirmed   = "cancel_confirmed"
	EvCancelLostRace    = "cancel_lost_race"
	EvDeadline          = "deadline"
	EvLost              = "lost"
	EvFailed            = "failed"
	EvAdopted           = "adopted"
	EvGhost             = "ghost"
	EvTopup             = "topup"
	EvSettled           = "settled"
	EvTrip              = "trip"
	EvStart             = "start"
	EvGhostStarted      = "ghost_started"
	EvStartConflict     = "start_conflict"
	EvProviderRequeue   = "provider_requeue"
	EvProviderCancelled = "provider_cancelled"
	EvUnknownStatus     = "unknown_status"
	EvStatusLost        = "status_lost"
	EvDuplicateAudio    = "duplicate_audio"
	EvNotSubmitted      = "not_submitted"
	EvBatchNoCapacity   = "batch_no_capacity"
	EvPreflightFailed   = "preflight_failed"
)

// common detail keys every event may carry besides its own.
var commonDetail = []string{"reason", "attempt", "n"}

// eventDetailKeys is the allowlist of detail_json keys per event (TestEvents_DetailAllowlist). No
// entry may ever name a URL, a provider ref, a signature, an expiry capability or a token.
var eventDetailKeys = map[string][]string{
	EvCreated:           {"takes"},
	EvClaim:             {"price_pph", "overhead_micro_usd", "window_ends_at", "topup_price_micro_usd"},
	EvSubmit:            {"timeout_s", "uploads"},
	EvSubmitted:         {},
	EvSubmitRetry:       {"outcome", "status", "retry_after_s", "next_attempt_at", "topup_micro_usd", "topup_refused", "text"},
	EvSubmitRefused:     {"outcome", "status", "text"},
	EvStatus:            {"status", "raw", "execution_ms", "delay_ms", "error_code", "text", "worker_error"},
	EvRunning:           {"status", "announced"},
	EvQueueExpired:      {"expiries", "next_attempt_at", "basis"},
	EvResubmit:          {"next_attempt_at", "queue_expiries"},
	EvUpload:            {"upload_id", "artifact", "bytes"},
	EvResultReceived:    {"artifact_count", "status"},
	EvCompletedRemote:   {"execution_ms"},
	EvVerified:          {"artifacts", "truncated_abc", "truncated_semantic"},
	EvIngested:          {"track_id", "outcome", "audio_s"},
	EvEars:              {"warnings"},
	EvCancelRequested:   {"by"},
	EvCancelSent:        {"count", "error"},
	EvCancelConfirmed:   {"basis"},
	EvCancelLostRace:    {"via"},
	EvDeadline:          {"deadline", "since"},
	EvLost:              {"status"},
	EvFailed:            {"ingest_code", "text", "missing", "mismatched", "worker_code"},
	EvAdopted:           {"attempts", "ghosts"},
	EvGhost:             {"topup_micro_usd", "topup_refused"},
	EvTopup:             {"topup_reason", "micro_usd", "refused", "cap"},
	EvSettled:           {"basis", "micro_usd", "excess_micro_usd", "execution_ms", "settle_reason"},
	EvTrip:              {"trip_reason"},
	EvStart:             {},
	EvGhostStarted:      {},
	EvStartConflict:     {},
	EvProviderRequeue:   {"topup_micro_usd", "topup_refused"},
	EvProviderCancelled: {"status"},
	EvUnknownStatus:     {"raw"},
	EvStatusLost:        {"error", "status"},
	EvDuplicateAudio:    {"track_id"},
	EvNotSubmitted:      {"queued_at"},
	EvBatchNoCapacity:   {"failed_jobs"},
	EvPreflightFailed:   {"check"},
}

// EventNames lists every event the runner may write.
func EventNames() []string {
	out := make([]string, 0, len(eventDetailKeys))
	for k := range eventDetailKeys {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// EventDetailKeys returns a copy of the detail-key allowlist of an event; ok is false for an unknown event.
func EventDetailKeys(name string) (keys []string, ok bool) {
	k, ok := eventDetailKeys[name]
	return slices.Concat(k, commonDetail), ok
}

const (
	maxEventDetail = 2 << 10 // bytes of detail_json
	maxEventText   = 500     // bytes of one provider text
)

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// cutText bounds provider text after redaction; it never splits a rune.
func cutText(s string) string {
	s = redact.String(s)
	if len(s) <= maxEventText {
		return s
	}
	s = s[:maxEventText]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// insertEvent appends one job_events row under the lease predicate (no row when the lease is gone). Unknown events and detail keys outside the allowlist are
// programming errors: they return an error instead of writing anything.
func insertEvent(ctx context.Context, ex execer, f store.LeaseFence, ev Event) error {
	allowed, ok := EventDetailKeys(ev.Name)
	if !ok {
		return fmt.Errorf("jobs: unknown event %q", ev.Name)
	}
	var detail any
	if len(ev.Detail) > 0 {
		clean := make(map[string]any, len(ev.Detail))
		for k, v := range ev.Detail {
			if !slices.Contains(allowed, k) {
				return fmt.Errorf("jobs: event %q does not allow detail key %q", ev.Name, k)
			}
			if s, isStr := v.(string); isStr {
				v = cutText(s)
			}
			clean[k] = v
		}
		raw, err := json.Marshal(clean)
		if err != nil {
			return err
		}
		if len(raw) > maxEventDetail {
			return fmt.Errorf("jobs: event %q detail exceeds %d bytes", ev.Name, maxEventDetail)
		}
		detail = string(raw)
	}
	args := []any{ev.JobID, nullable(ev.AttemptID), store.FormatTime(f.Now), nullable(ev.From), nullable(ev.To), ev.Name, nullable(ev.Code), detail}
	res, err := ex.ExecContext(ctx, `INSERT INTO job_events(job_id, attempt_id, at, from_state, to_state, event, code, detail_json)
		SELECT ?, ?, ?, ?, ?, ?, ?, ? WHERE `+f.Where(), append(args, f.Args()...)...) //nolint:gosec // G202: fixed C69 predicate, values are bound
	n, err := f.Fenced(res, err)
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrLeaseLost
	}
	return nil
}
