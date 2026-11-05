package jobs

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// A batch whose attempts ended without any of them being tied to a worker lifetime must still become ready: the meter
// waits idle_s + 60 s from the last attempt end and then settles what it has, instead of holding the batch active forever.
func TestOverheadSeconds_ReadyWithoutAnyLifetime(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{jobs: 1})
	r.steps(2)
	r.exec(`UPDATE job_attempts SET state = 'done', terminal_at = ?, terminal_basis = 'time_bound' WHERE job_id = ?`, store.FormatTime(r.clk.Now()), b.Jobs[0])
	rec := r.recon()
	_, ready, err := rec.OverheadSeconds(t.Context(), r.st.R, b.Batch)
	if err != nil || ready {
		t.Fatalf("right after the end: ready=%v err=%v", ready, err)
	}
	r.advance(81 * time.Second) // batches.idle_s is 20
	secs, ready, err := rec.OverheadSeconds(t.Context(), r.st.R, b.Batch)
	if err != nil || !ready || secs != 0 {
		t.Fatalf("after idle+60: secs=%d ready=%v err=%v", secs, ready, err)
	}
}
