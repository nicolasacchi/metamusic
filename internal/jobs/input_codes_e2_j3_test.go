package jobs

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// The worker's input and task errors end the job with a code of their own (E2-J3). They used to end WORKER_LOST whenever only the
// error text or the handler's wrapped return carried them; the generation deadline already worked that way (deadline_code_test.go).
// The mapping is engine-neutral, so the yue2 job tests below assert it too.
func TestTerminalCode_InputAndTaskCodes(t *testing.T) {
	t.Parallel()
	mm := func(code string) string {
		return `{"contract":"mm-worker-1","take_id":"t","attempt_id":"a","status":"error","error":{"code":"` + code + `","message":"m"},"artifacts":{},"audio_seconds":null,"truncated":null,"timings":{},"env":{}}`
	}
	for _, c := range []struct{ worker, want string }{
		{"INPUT_FAILED", CodeInputFailed}, {"INPUT_MISMATCH", CodeInputMismatch}, {"TASK_UNAVAILABLE", CodeTaskUnavailable},
	} {
		for _, tc := range []struct {
			name string
			st   provider.RemoteStatus
		}{
			{"error text", provider.RemoteStatus{State: provider.Failed, ErrorText: c.worker + ": src_audio could not be fetched"}},
			{"handler return shape", provider.RemoteStatus{State: provider.Failed, Output: []byte(`{"error":"` + c.worker + `: m","mm":` + mm(c.worker) + `}`)}},
			{"output", provider.RemoteStatus{State: provider.Failed, Output: []byte(mm(c.worker))}},
		} {
			if got, _ := terminalCode(tc.st); got != c.want {
				t.Errorf("%s %s: %s, want %s", c.worker, tc.name, got, c.want)
			}
		}
	}
	// a code without a name of its own keeps the old mapping
	if got, _ := terminalCode(provider.RemoteStatus{State: provider.Failed, ErrorText: "BAD_INPUT: x"}); got != CodeWorkerLost {
		t.Errorf("BAD_INPUT text: %s", got)
	}
	if got, _ := terminalCode(provider.RemoteStatus{State: provider.Failed, Output: []byte(mm("BAD_INPUT"))}); got != CodeWorkerError {
		t.Errorf("BAD_INPUT output: %s", got)
	}
	for _, code := range []string{CodeInputFailed, CodeInputMismatch, CodeTaskUnavailable} {
		if ErrorMessage(code) == "" {
			t.Errorf("no fixed message for %s", code)
		}
	}
}

// TestRunner_WorkerInputErrorsEndWithTheirCode drives a yue2 job to the end with each worker error, by the error text and by an
// mm-result.json that already reported it (the provider's terminal status then carries no code).
func TestRunner_WorkerInputErrorsEndWithTheirCode(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ worker, want string }{
		{"INPUT_FAILED", CodeInputFailed}, {"INPUT_MISMATCH", CodeInputMismatch}, {"TASK_UNAVAILABLE", CodeTaskUnavailable},
	} {
		t.Run(c.worker+" text", func(t *testing.T) {
			t.Parallel()
			r, _, job, _ := generating(t)
			r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000), ErrorText: c.worker + ": m"})
			r.advance(31 * time.Second)
			r.step()
			r.wantState(job, StateFailed)
			r.wantCode(job, c.want)
		})
		t.Run(c.worker+" reported by mm-result.json", func(t *testing.T) {
			t.Parallel()
			r, _, job, _ := generating(t)
			att := r.att(job, 1).ID
			r.exec(`INSERT INTO job_events(job_id, attempt_id, at, event, code, detail_json) VALUES (?, ?, ?, 'status', 'WORKER_ERROR', ?)`,
				job, att, "2026-10-15T20:34:00.000Z", `{"reason":"worker reported an error","worker_error":"`+c.worker+`"}`)
			r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000)})
			r.advance(31 * time.Second)
			r.step()
			r.wantState(job, StateFailed)
			r.wantCode(job, c.want)
		})
	}
}

// TestRunner_ACEInputMismatchEndsWithItsCode: an ACE-Step job whose worker answered INPUT_MISMATCH (B-INT saw it end WORKER_LOST).
func TestRunner_ACEInputMismatchEndsWithItsCode(t *testing.T) {
	b := newACERig(t, false, aceEngines())
	job := b.seedACE(b.aceRecipe(aceRequest(t, nil)))
	b.specOf(job)
	b.setStatus(b.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000), ErrorText: "INPUT_MISMATCH: src_audio does not match its digest"})
	for range 6 {
		b.step()
		if IsTerminal(b.state(job)) {
			break
		}
		b.advance(31e9)
	}
	b.wantState(job, StateFailed)
	b.wantCode(job, CodeInputMismatch)
}
