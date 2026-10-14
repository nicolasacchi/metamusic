package jobs

// Job states (C75.1).
const (
	StateAwaiting     = "awaiting_confirmation"
	StateQueued       = "queued"
	StateSubmitting   = "submitting"
	StateStarting     = "starting"
	StateGenerating   = "generating"
	StateTransferring = "transferring"
	StateIngesting    = "ingesting"
	StateCompleted    = "completed"
	StateCancelling   = "cancelling"
	StateCancelled    = "cancelled"
	StateFailing      = "failing"
	StateFailed       = "failed"
	StateExpired      = "expired"
)

// AllStates lists every job state.
var AllStates = []string{
	StateAwaiting, StateQueued, StateSubmitting, StateStarting, StateGenerating, StateTransferring,
	StateIngesting, StateCompleted, StateCancelling, StateCancelled, StateFailing, StateFailed, StateExpired,
}

// IsTerminal reports whether no transition leaves state.
func IsTerminal(state string) bool {
	switch state {
	case StateCompleted, StateFailed, StateCancelled, StateExpired:
		return true
	}
	return false
}

// IsLeased reports whether jobs in state are held by a runner job lease (C69): every state between
// submitting and failing. awaiting_confirmation, queued and the terminal states never hold one.
func IsLeased(state string) bool {
	switch state {
	case StateSubmitting, StateStarting, StateGenerating, StateTransferring, StateIngesting, StateCancelling, StateFailing:
		return true
	}
	return false
}

// Error codes of a job (jobs.error_code; the API values of C81).
const (
	CodeCancelledByUser  = "CANCELLED_BY_USER"
	CodeRejected         = "REJECTED"
	CodeApprovalExpired  = "APPROVAL_EXPIRED"
	CodeNoCapacity       = "NO_CAPACITY"
	CodeBudgetExceeded   = "BUDGET_EXCEEDED"
	CodePriceChanged     = "PRICE_CHANGED"
	CodeNotSubmitted     = "NOT_SUBMITTED"
	CodeProviderAuth     = "PROVIDER_AUTH"
	CodeEdgeBlocked      = "EDGE_BLOCKED"
	CodeSubmitRejected   = "SUBMIT_REJECTED"
	CodeWorkerError      = "WORKER_ERROR"
	CodeWorkerLost       = "WORKER_LOST"
	CodeDeadline         = "DEADLINE"
	CodeProviderCanceled = "PROVIDER_CANCELLED"
	CodeProviderTripped  = "PROVIDER_TRIPPED"
	CodeArtifactMissing  = "ARTIFACT_MISSING"
	CodeArtifactMismatch = "ARTIFACT_MISMATCH"
	CodeWorkerContract   = "WORKER_CONTRACT"
	CodeIngestFailed     = "INGEST_FAILED"
	CodeDuplicateAudio   = "DUPLICATE_AUDIO"
)

// Fixed limits of the runner (C68, C75).
const (
	// MaxQueueExpiryResubmits is the most queue expiries a job survives (D-M3-29): the 7th fails it.
	MaxQueueExpiryResubmits = 6
	// SkewLimit is the clock skew above which billing releases pause (C76.5).
	SkewLimit = 30
)
