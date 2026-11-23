package acceptance_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// The pod acceptance rig (BULK-S1, C399.13): a rig over the fake backend and the fake pod backend `fake-pod`, whose simulated agents
// poll the rig's own poll route and upload through its own upload handlers, with the production ingest bridge over real ffmpeg. The
// runner is idle: tests drive rounds (Rig.PodRound) and advance the rig's clock, so no wall-clock time passes.

func podRig(t *testing.T, po servertest.PodsOptions, mods ...func(*servertest.Options)) *servertest.Rig {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	testtools.Require(t, "ffmpeg")
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	o := servertest.Options{Archive: &tpl, Backend: "fake", Pods: &po}
	o.M2b.Media = true
	for _, m := range mods {
		m(&o)
	}
	r := servertest.New(t, o)
	// the first runner step takes the lease; the first reconciler pass reads the quotes the estimates need
	ctx := context.Background()
	if _, err := r.Runner.Step(ctx); err != nil {
		t.Fatalf("runner step: %v", err)
	}
	if err := r.Reconciler.Pass(ctx); err != nil {
		t.Fatalf("reconciler pass: %v", err)
	}
	return r
}

// podBatch estimates n takes of a mother track on fake-pod (the human path) and creates the batch; style adds markers to every take.
func podBatch(t *testing.T, r *servertest.Rig, n int, style, key string) (batch string, jobs []string) {
	t.Helper()
	h := r.Human(t)
	mother := yue2Mother(t, r)
	var items []any
	for left := n; left > 0; left -= 10 {
		it := map[string]any{"mode": "new_seed", "parent_track_id": mother, "n_seeds": min(left, 10)}
		if style != "" {
			it = map[string]any{"mode": "style_edit", "parent_track_id": mother, "n_seeds": min(left, 10), "patch": map[string]any{"style": motherStyle(t, r, mother) + " " + style}}
		}
		items = append(items, it)
	}
	body := map[string]any{"items": items, "options": map[string]any{"backend": provider.BackendFakePod, "tier_b": false}}
	est := obj(h.OK(t, "POST", "/api/generations/estimate", body))
	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "pod acceptance"}, "Idempotency-Key", key)
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	b := obj(resp.JSON(t))
	for _, j := range arr(b["jobs"]) {
		jobs = append(jobs, obj(j)["id"].(string))
	}
	return b["id"].(string), jobs
}

// podPump runs rounds, advancing the clock by step before each, until done holds or max rounds passed.
func podPump(t *testing.T, r *servertest.Rig, step time.Duration, rounds int, done func() bool) {
	t.Helper()
	for range rounds {
		if done() {
			return
		}
		r.Clock.Advance(step)
		r.PodRound(t)
	}
	if !done() {
		t.Fatalf("podPump: the condition never held\n%s\n%s\n%s\ntrips: %s", rowsDump(t, r, `SELECT id, state, error_code, last_reported, lifetime_s, settled FROM pods`),
			rowsDump(t, r, `SELECT state, COUNT(*) FROM jobs GROUP BY 1`), rowsDump(t, r, `SELECT id, state FROM batches`), rowsDump(t, r, `SELECT reason, reset_at FROM provider_trips`))
	}
}

// ledgerSum is the sum of ledger rows of a batch by kind and reason ("" = any reason).
func ledgerSum(t *testing.T, r *servertest.Rig, batch, kind, reason string) int64 {
	t.Helper()
	var v int64
	if err := r.Store.R.QueryRow(`SELECT COALESCE(SUM(micro_usd),0) FROM ledger WHERE batch_id = ? AND kind = ? AND (? = '' OR reason = ?)`, batch, kind, reason, reason).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func scalar(t *testing.T, r *servertest.Rig, q string, args ...any) string {
	t.Helper()
	var v any
	if err := r.Store.R.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(v)
}

// podSettled holds when the batch is finished, its pods are gone at the simulated RunPod and every pod row is terminated and settled.
func podSettled(t *testing.T, r *servertest.Rig, batch string) func() bool {
	return func() bool {
		return r.Pods.Live() == 0 && scalar(t, r, `SELECT state FROM batches WHERE id = ?`, batch) == "finished" &&
			scalar(t, r, `SELECT COUNT(*) FROM pods WHERE batch_id = ? AND (state <> 'terminated' OR settled = 0)`, batch) == "0"
	}
}
