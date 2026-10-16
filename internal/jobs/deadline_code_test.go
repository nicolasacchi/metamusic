package jobs

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// The worker's generation deadline ends the handler with {"error": "DEADLINE: ...", "mm": <output>}: RunPod
// reports FAILED with that error text, and mm-result.json carries error.code DEADLINE. Both must end the job
// DEADLINE, not WORKER_LOST (incident 2026-10-15).
func TestTerminalCode_WorkerDeadline(t *testing.T) {
	t.Parallel()
	const errOut = `{"contract":"mm-yue2/1","take_id":"t","attempt_id":"a","status":"error","error":{"code":"DEADLINE","message":"the generation deadline passed"},"artifacts":{},"audio_seconds":null,"truncated":null,"timings":{},"env":{}}`
	const otherOut = `{"contract":"mm-yue2/1","take_id":"t","attempt_id":"a","status":"error","error":{"code":"PIPELINE_ERROR","message":"boom"},"artifacts":{},"audio_seconds":null,"truncated":null,"timings":{},"env":{}}`
	for _, tc := range []struct {
		name string
		st   provider.RemoteStatus
		want string
	}{
		{"error text", provider.RemoteStatus{State: provider.Failed, ErrorText: "DEADLINE: the generation deadline passed"}, CodeDeadline},
		{"handler return shape", provider.RemoteStatus{State: provider.Failed, Output: []byte(`{"error":"DEADLINE: the generation deadline passed","mm":` + errOut + `}`)}, CodeDeadline},
		{"output", provider.RemoteStatus{State: provider.Failed, Output: []byte(errOut)}, CodeDeadline},
		{"other worker error", provider.RemoteStatus{State: provider.Failed, Output: []byte(otherOut)}, CodeWorkerError},
		{"other error text", provider.RemoteStatus{State: provider.Failed, ErrorText: "PIPELINE_ERROR: boom"}, CodeWorkerLost},
		{"no information", provider.RemoteStatus{State: provider.Failed}, CodeWorkerLost},
		{"provider timeout", provider.RemoteStatus{State: provider.TimedOut}, CodeDeadline},
	} {
		if got, _ := terminalCode(tc.st); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestRunner_WorkerDeadlineEndsDeadline(t *testing.T) {
	t.Parallel()
	t.Run("error text", func(t *testing.T) {
		t.Parallel()
		r, _, job, _ := generating(t)
		r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(119_936), ErrorText: "DEADLINE: the generation deadline passed"})
		r.advance(31 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeDeadline)
	})
	t.Run("mm-result.json already reported it", func(t *testing.T) {
		t.Parallel()
		r, _, job, _ := generating(t)
		att := r.att(job, 1).ID
		r.exec(`INSERT INTO job_events(job_id, attempt_id, at, event, code, detail_json) VALUES (?, ?, ?, 'status', 'WORKER_ERROR', '{"reason":"worker reported an error","worker_error":"DEADLINE"}')`, job, att, "2026-10-15T20:34:00.000Z")
		r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(119_936)})
		r.advance(31 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeDeadline)
	})
}
