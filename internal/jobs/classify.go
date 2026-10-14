package jobs

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// NeverRanShapes are the provider-terminal shapes that the probe fixtures pin as "the job never ran"
// (C73.2 rules 4 and 5, key-capabilities.json never_ran_shapes). The zero value pins nothing: every such
// case is then settled at its time bound, which is conservative.
type NeverRanShapes struct {
	// TTLExpiryStates are the terminal states a TTL expiry reports (executionTime 0 or absent), at or
	// after submitted_at + TTL.
	TTLExpiryStates []provider.RemoteState
	// TTLExpiry404 pins that a TTL-expired job answers 404 on /status (rule 5's exception).
	TTLExpiry404 bool
	// CancelledWhileQueued pins Cancelled with executionTime 0 or absent as "never ran".
	CancelledWhileQueued bool
}

func (s NeverRanShapes) ttlState(st provider.RemoteState) bool {
	for _, x := range s.TTLExpiryStates {
		if x == st {
			return true
		}
	}
	return false
}

// Verdict is the result of classifying one observation of an attempt (C73.2).
type Verdict int

// Verdicts, in the order of the rules that produce them.
const (
	// VerdictPending: nothing terminal yet (rule 7); Queued and Running update observations.
	VerdictPending Verdict = iota
	// VerdictResultOK: a verified ok mm-result.json exists (rule 1); the take completes from it.
	VerdictResultOK
	// VerdictRan: executionTime > 0 (rule 2): settle `execution`.
	VerdictRan
	// VerdictRanUnknown: terminal without executionTime but with evidence of a run, or with an unpinned
	// shape (rules 3 and 4's fallback): settle `execution_unknown` at the time bound.
	VerdictRanUnknown
	// VerdictQueueExpiry: the pinned TTL-expiry shape (rule 4) or rule 5's 404 exception: never ran.
	VerdictQueueExpiry
	// VerdictCancelledQueued: the pinned cancelled-while-queued shape (rule 4): never ran, nothing to settle.
	VerdictCancelledQueued
	// VerdictLost: NotFound for an attempt with a ref (rule 5): the attempt becomes unknown.
	VerdictLost
	// VerdictRequeue: Running seen, then Queued again (rule 6).
	VerdictRequeue
)

func (v Verdict) String() string {
	return [...]string{"pending", "result_ok", "ran", "ran_unknown", "queue_expiry", "cancelled_queued", "lost", "requeue"}[v]
}

// ClassifyInput is everything Classify looks at.
type ClassifyInput struct {
	Status      provider.RemoteStatus
	Now         time.Time
	TTL         time.Duration
	SubmittedAt time.Time
	LastQueued  *time.Time // last poll that answered Queued
	// Evidence of a run besides the status: in_progress_seen, a start announcement, or an upload.
	InProgressSeen, Announced, HasUpload bool
	// WasRunning: the previous observation was Running (rule 6's "later Queued again").
	WasRunning bool
	// VerifiedOK: a verified mm-result.json with status ok exists for the attempt (rule 1).
	VerifiedOK bool
	Shapes     NeverRanShapes
}

// Classification is the verdict and the execution time it carries.
type Classification struct {
	Verdict     Verdict
	Terminal    bool  // the provider status is terminal
	ExecutionMS int64 // VerdictRan only
}

// terminal reports a provider-terminal state.
func terminal(s provider.RemoteState) bool {
	switch s {
	case provider.Completed, provider.Failed, provider.Cancelled, provider.TimedOut:
		return true
	}
	return false
}

// resultRetention is how long RunPod keeps a job record (C73.2 rule 5): a 404 younger than this cannot be
// a purged record.
const resultRetention = 30 * time.Minute

// Classify applies C73.2's rules 1 to 7 in order. Whether an attempt ran is decided from the provider
// record, never from which polls saw Running.
func Classify(in ClassifyInput) Classification {
	st := in.Status
	term := terminal(st.State)
	exec := int64(0)
	if st.ExecutionMS != nil {
		exec = *st.ExecutionMS
	}
	evidence := in.InProgressSeen || in.Announced || in.HasUpload || len(st.Output) > 0
	// Rule 1.
	if in.VerifiedOK {
		return Classification{Verdict: VerdictResultOK, Terminal: term, ExecutionMS: exec}
	}
	// Rule 5 (a missing record).
	if st.State == provider.NotFound {
		fresh := in.LastQueued != nil && in.Now.Sub(*in.LastQueued) < resultRetention
		if in.Shapes.TTLExpiry404 && fresh && !in.Now.Before(in.SubmittedAt.Add(in.TTL)) && !evidence {
			return Classification{Verdict: VerdictQueueExpiry}
		}
		return Classification{Verdict: VerdictLost}
	}
	if term {
		switch {
		case exec > 0: // rule 2
			return Classification{Verdict: VerdictRan, Terminal: true, ExecutionMS: exec}
		case evidence: // rule 3
			return Classification{Verdict: VerdictRanUnknown, Terminal: true}
		case in.Shapes.ttlState(st.State) && !in.Now.Before(in.SubmittedAt.Add(in.TTL)): // rule 4
			return Classification{Verdict: VerdictQueueExpiry, Terminal: true}
		case st.State == provider.Cancelled && in.Shapes.CancelledWhileQueued: // rule 4
			return Classification{Verdict: VerdictCancelledQueued, Terminal: true}
		default: // rule 4's fallback to rule 3
			return Classification{Verdict: VerdictRanUnknown, Terminal: true}
		}
	}
	// Rule 6.
	if st.State == provider.Queued && (in.WasRunning || in.InProgressSeen) {
		return Classification{Verdict: VerdictRequeue}
	}
	// Rule 7.
	return Classification{Verdict: VerdictPending}
}
