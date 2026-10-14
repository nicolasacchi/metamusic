package upload

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestOpenAttempt_LeaseAndAllowlist(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	open := func(art map[string]int64, lease store.LeaseFence) error {
		tx, err := r.st.W.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return r.svc.OpenAttempt(t.Context(), tx, JobRef{JobID: jobID, BatchID: batchID}, attemptID, art, r.exp, lease)
	}
	if err := open(map[string]int64{"plan.json": 10}, store.LeaseFence{Owner: "intruder", Now: t0}); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("foreign owner: %v", err)
	}
	if err := open(map[string]int64{"plan.json": 10}, store.LeaseFence{Owner: "o1", Now: t0.Add(48 * time.Hour)}); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("expired lease: %v", err)
	}
	if err := open(map[string]int64{"latent.npy": 10}, store.LeaseFence{Owner: "o1", Now: t0}); err == nil {
		t.Error("a non-allowlisted artifact was accepted")
	}
	if err := open(map[string]int64{"plan.json": provider.Artifacts[4].MaxBytes + 1}, store.LeaseFence{Owner: "o1", Now: t0}); err == nil {
		t.Error("max_bytes above the allowlist limit was accepted")
	}
	if err := open(map[string]int64{"plan.json": 0}, store.LeaseFence{Owner: "o1", Now: t0}); err == nil {
		t.Error("zero max_bytes accepted")
	}
	if n := r.count(`SELECT COUNT(*) FROM uploads WHERE artifact='plan.json'`); n != 0 {
		t.Errorf("refused calls left %d rows", n)
	}
}

func TestMint_OnlyOpenUploads(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ups, start, err := r.svc.Mint(JobRef{JobID: jobID, BatchID: batchID}, attemptID, r.exp)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 3 || ups["audio.flac"].MaxBytes != 1000 || start == "" {
		t.Fatalf("%v %q", ups, start)
	}
	id, u := r.target("audio.flac")
	if w := r.put(u, []byte("x"), nil); w.Code != 201 {
		t.Fatalf("%d", w.Code)
	}
	ups, _, _ = r.svc.Mint(JobRef{JobID: jobID, BatchID: batchID}, attemptID, r.exp)
	if _, ok := ups["audio.flac"]; ok || len(ups) != 2 {
		t.Errorf("received upload minted again: %v", ups)
	}
	_ = id
	if _, _, err := r.svc.Mint(JobRef{JobID: jobID}, "01900000-0000-7000-8000-0000000000ff", r.exp); err == nil {
		t.Error("mint for an attempt without uploads succeeded")
	}
}

func TestConsumeCloseRemove(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id, u := r.target("audio.flac")
	r.put(u, []byte("x"), nil)
	rec, err := r.svc.Received(t.Context(), r.st.R, attemptID)
	if err != nil || len(rec) != 1 || rec[0].Path != "incoming/uploads/"+id || rec[0].Bytes != 1 {
		t.Fatalf("%v %v", rec, err)
	}
	tx, _ := r.st.W.Begin()
	if err := r.svc.Consume(t.Context(), tx, []string{id}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Consume(t.Context(), tx, []string{id}); err == nil {
		t.Error("consuming twice succeeded")
	}
	if err := r.svc.CloseAttempt(t.Context(), tx, attemptID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := r.str(`SELECT group_concat(state) FROM (SELECT state FROM uploads WHERE attempt_id = ? ORDER BY artifact)`, attemptID); got != "consumed,closed,closed" {
		t.Errorf("states %s", got)
	}
	if rec, _ := r.svc.Received(t.Context(), r.st.R, attemptID); len(rec) != 0 {
		t.Errorf("consumed upload still listed: %v", rec)
	}
	if err := r.svc.RemoveConsumedFiles(t.Context(), []string{id, "01900000-0000-7000-8000-0000000000ff"}); err != nil {
		t.Fatal(err)
	}
	r.noFiles(id)
}

func TestSweepFiles(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id, u := r.target("audio.flac")
	r.put(u, []byte("x"), nil)
	_, mu := r.target("mm-result.json")
	r.put(mu, []byte("{}"), nil)
	// a stale part file and a fresh one
	for _, n := range []string{"stale.part", "fresh.part"} {
		f, err := r.data.OpenFile("incoming/uploads/"+n, os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	old := t0.Add(-48 * time.Hour)
	if err := r.data.Chtimes("incoming/uploads/stale.part", old, old); err != nil {
		t.Fatal(err)
	}
	if err := r.data.Chtimes("incoming/uploads/fresh.part", t0.Add(10*time.Hour), t0.Add(10*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// the job is not terminal: only the stale part file goes
	if n, err := r.svc.SweepFiles(t.Context(), t0); err != nil || n != 1 {
		t.Fatalf("live job: removed %d, %v", n, err)
	}
	r.exec(`UPDATE jobs SET state='failing' WHERE id=?`, jobID)
	r.exec(`UPDATE jobs SET state='failed', error_code='WORKER_LOST', finished_at=?, lease_owner=NULL, lease_expires_at=NULL WHERE id=?`, store.FormatTime(t0), jobID)
	if n, err := r.svc.SweepFiles(t.Context(), t0.Add(23*time.Hour)); err != nil || n != 0 {
		t.Fatalf("before 24 h: removed %d, %v", n, err)
	}
	if n, err := r.svc.SweepFiles(t.Context(), t0.Add(25*time.Hour)); err != nil || n != 2 {
		t.Fatalf("after 24 h: removed %d, %v", n, err)
	}
	r.noFiles(id)
	if n, err := r.svc.SweepFiles(t.Context(), t0.Add(26*time.Hour)); err != nil || n != 0 {
		t.Fatalf("second sweep: removed %d, %v", n, err)
	}
	if _, err := r.data.Stat("incoming/uploads/fresh.part"); err != nil {
		t.Errorf("fresh part file removed: %v", err)
	}
}

func TestSweepFiles_NoDirectory(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if n, err := r.svc.SweepFiles(t.Context(), t0); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
}
