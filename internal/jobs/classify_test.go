package jobs

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

func TestClassify_AttemptTable(t *testing.T) {
	t.Parallel()
	now := t0.Add(3 * time.Hour)
	sub := t0
	ttl := 2 * time.Hour
	recent := now.Add(-5 * time.Minute)
	old := now.Add(-2 * time.Hour)
	pinned := NeverRanShapes{TTLExpiryStates: []provider.RemoteState{provider.TimedOut}, CancelledWhileQueued: true, TTLExpiry404: true}
	st := func(s provider.RemoteState, exec *int64, out string) provider.RemoteStatus {
		return provider.RemoteStatus{State: s, Raw: string(s), ExecutionMS: exec, Output: []byte(out)}
	}
	cases := []struct {
		name string
		in   ClassifyInput
		want Verdict
	}{
		{"rule1 verified result wins over a failed status", ClassifyInput{Status: st(provider.Failed, nil, ""), VerifiedOK: true}, VerdictResultOK},
		{"rule1 verified result, still running", ClassifyInput{Status: st(provider.Running, nil, ""), VerifiedOK: true}, VerdictResultOK},
		{"rule2 execution time on completed", ClassifyInput{Status: st(provider.Completed, ms(70000), ""), Shapes: pinned}, VerdictRan},
		{"rule2 execution time on a timeout kill", ClassifyInput{Status: st(provider.TimedOut, ms(200000), "")}, VerdictRan},
		{"rule2 beats a pinned never-ran shape", ClassifyInput{Status: st(provider.TimedOut, ms(5), ""), Shapes: pinned, Now: now, SubmittedAt: sub, TTL: ttl}, VerdictRan},
		{"rule3 completed without execution time, with an upload", ClassifyInput{Status: st(provider.Completed, nil, ""), HasUpload: true}, VerdictRanUnknown},
		{"rule3 failed after running seen", ClassifyInput{Status: st(provider.Failed, ms(0), ""), InProgressSeen: true}, VerdictRanUnknown},
		{"rule3 timed out after the start announcement", ClassifyInput{Status: st(provider.TimedOut, nil, ""), Announced: true, Shapes: pinned, Now: now, SubmittedAt: sub, TTL: ttl}, VerdictRanUnknown},
		{"rule3 non-empty output counts as evidence", ClassifyInput{Status: st(provider.Failed, nil, `{"x":1}`)}, VerdictRanUnknown},
		{"rule4 TTL expiry shape", ClassifyInput{Status: st(provider.TimedOut, ms(0), ""), Shapes: pinned, Now: now, SubmittedAt: sub, TTL: ttl}, VerdictQueueExpiry},
		{"rule4 TTL expiry shape before the TTL passed is time-bound", ClassifyInput{Status: st(provider.TimedOut, nil, ""), Shapes: pinned, Now: sub.Add(time.Hour), SubmittedAt: sub, TTL: ttl}, VerdictRanUnknown},
		{"rule4 unpinned TTL shape falls back to the time bound", ClassifyInput{Status: st(provider.TimedOut, nil, ""), Now: now, SubmittedAt: sub, TTL: ttl}, VerdictRanUnknown},
		{"rule4 cancelled while queued", ClassifyInput{Status: st(provider.Cancelled, nil, ""), Shapes: pinned}, VerdictCancelledQueued},
		{"rule4 unpinned cancelled shape is time-bound", ClassifyInput{Status: st(provider.Cancelled, nil, "")}, VerdictRanUnknown},
		{"rule4 other terminal record without execution time", ClassifyInput{Status: st(provider.Failed, nil, ""), Shapes: pinned}, VerdictRanUnknown},
		{"rule5 not found is lost, never an expiry", ClassifyInput{Status: st(provider.NotFound, nil, ""), Now: now, SubmittedAt: sub, TTL: ttl, LastQueued: &recent}, VerdictLost},
		{"rule5 not found with every condition and the 404 shape pinned", ClassifyInput{Status: st(provider.NotFound, nil, ""), Shapes: pinned, Now: now, SubmittedAt: sub, TTL: ttl, LastQueued: &recent}, VerdictQueueExpiry},
		{"rule5 exception needs the queued sighting inside retention", ClassifyInput{Status: st(provider.NotFound, nil, ""), Shapes: pinned, Now: now, SubmittedAt: sub, TTL: ttl, LastQueued: &old}, VerdictLost},
		{"rule5 exception needs the TTL to have passed", ClassifyInput{Status: st(provider.NotFound, nil, ""), Shapes: pinned, Now: sub.Add(time.Hour), SubmittedAt: sub, TTL: ttl, LastQueued: &recent}, VerdictLost},
		{"rule5 exception needs no evidence of a run", ClassifyInput{Status: st(provider.NotFound, nil, ""), Shapes: pinned, Now: now, SubmittedAt: sub, TTL: ttl, LastQueued: &recent, InProgressSeen: true}, VerdictLost},
		{"rule5 exception needs a queued sighting", ClassifyInput{Status: st(provider.NotFound, nil, ""), Shapes: pinned, Now: now, SubmittedAt: sub, TTL: ttl}, VerdictLost},
		{"rule6 requeue", ClassifyInput{Status: st(provider.Queued, nil, ""), WasRunning: true}, VerdictRequeue},
		{"rule6 requeue after running seen", ClassifyInput{Status: st(provider.Queued, nil, ""), InProgressSeen: true}, VerdictRequeue},
		{"rule7 queued", ClassifyInput{Status: st(provider.Queued, nil, "")}, VerdictPending},
		{"rule7 running", ClassifyInput{Status: st(provider.Running, nil, "")}, VerdictPending},
		{"rule7 unknown status is not terminal", ClassifyInput{Status: provider.RemoteStatus{State: provider.Unknown, Raw: "WEIRD"}}, VerdictPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := Classify(c.in)
			if got.Verdict != c.want {
				t.Errorf("verdict %v, want %v", got.Verdict, c.want)
			}
			if got.Verdict == VerdictRan && got.ExecutionMS != *c.in.Status.ExecutionMS {
				t.Errorf("execution %d", got.ExecutionMS)
			}
			if wantTerm := terminal(c.in.Status.State); got.Verdict != VerdictLost && got.Verdict != VerdictQueueExpiry && got.Verdict != VerdictRequeue && got.Terminal != wantTerm {
				t.Errorf("terminal %v, want %v", got.Terminal, wantTerm)
			}
		})
	}
	if s := VerdictRequeue.String(); s != "requeue" {
		t.Errorf("String %q", s)
	}
}
