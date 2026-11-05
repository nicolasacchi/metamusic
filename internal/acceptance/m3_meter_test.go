package acceptance_test

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestAcceptance_M3_MeterOverheadIsSettled: a finished batch settles the metered worker overhead (cold start and idle
// tail) into the ledger. Before the meter was wired in the app, finishBatch always settled zero.
func TestAcceptance_M3_MeterOverheadIsSettled(t *testing.T) {
	r := genRig(t, archivetest.SyntheticArchive(t, "acceptance"), servertest.Options{M2b: servertest.M2bOptions{Backfill: true}, Fake: &fake.Options{Speed: fake.Speed{Slow: 30 * time.Second}}})
	h := r.Human(t)
	mother := knownMother(t, r)
	est := obj(h.OK(t, "POST", "/api/tracks/"+mother+"/variants/estimate", map[string]any{"mode": "new_seed", "n_seeds": 1}))
	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "meter"}, "Idempotency-Key", "meter-1")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	b := obj(resp.JSON(t))
	job := obj(arr(b["jobs"])[0])["id"].(string)
	var batch string
	if err := r.Store.R.QueryRow(`SELECT batch_id FROM jobs WHERE id = ?`, job).Scan(&batch); err != nil {
		t.Fatal(err)
	}
	pump(t, r, func() bool {
		var s string
		_ = r.Store.R.QueryRow(`SELECT state FROM batches WHERE id = ?`, batch).Scan(&s)
		return s == "finished"
	})
	var overhead int64
	if err := r.Store.R.QueryRow(`SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE batch_id = ? AND kind = 'settle' AND reason = 'overhead'`, batch).Scan(&overhead); err != nil {
		t.Fatal(err)
	}
	if overhead <= 0 {
		t.Errorf("the batch settled %d micro-USD of worker overhead, want > 0", overhead)
	}
}
