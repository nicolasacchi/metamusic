package acceptance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// planSeedsBody is the estimate of a plan_seeds item over a composed recipe with two lyric sections.
func planSeedsBody(id, style string, n int) map[string]any {
	recipe := map[string]any{"id": id, "style": style, "cot": "full", "seed": 4300,
		"lyrics": "[Verse 1]\nla chiave del mare non è nella casa\n[Chorus]\nsono io che canto per te"}
	return map[string]any{"items": []any{map[string]any{"mode": "plan_seeds", "recipe": recipe, "desk": map[string]any{"n_seeds": n}}}}
}

// runPlanJob estimates and creates a plan_seeds batch as the Owner and pumps its single job to a terminal state.
func runPlanJob(t *testing.T, r *servertest.Rig, h *servertest.Client, id, style string, n int) (job, state, code string) {
	t.Helper()
	est := obj(h.OK(t, "POST", "/api/generations/estimate", planSeedsBody(id, style, n)))
	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "plan it"}, "Idempotency-Key", "desk-"+id)
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	job = obj(arr(obj(resp.JSON(t))["jobs"])[0])["id"].(string)
	pump(t, r, func() bool { s, _ := jobState(t, r, job); return terminalJob(s) })
	state, code = jobState(t, r, job)
	return job, state, code
}

// TestJobsReaders_PlanJobLinksPlans (C361.8, DESK-J1): on the fake backend a plan_seeds job runs to completion with no track; its
// plan bundles become Desk plans that list_plans?job_id= finds and get_plan reads, the job views show track_id null, and the run
// writes no track, recipe-of-a-take, mutation or master. A job whose worker fails every seed fails PLANS_FAILED and stores nothing.
func TestJobsReaders_PlanJobLinksPlans(t *testing.T) {
	r := genRig(t, archivetest.SyntheticArchive(t, "acceptance"), servertest.Options{M2b: servertest.M2bOptions{Backfill: true}})
	h := r.Human(t)
	tracks, mutations := count(t, r, `SELECT COUNT(*) FROM tracks`), count(t, r, `SELECT COUNT(*) FROM mutations`)

	job, state, code := runPlanJob(t, r, h, "desk-plan-ok", "italian, synth pop, 100 bpm", 2)
	if state != "completed" {
		t.Fatalf("job %s (%s): %s", state, code, jobEvents(t, r, job))
	}
	var kind, outcome string
	var track *string
	if err := r.Store.R.QueryRow(`SELECT kind, outcome, track_id FROM jobs WHERE id = ?`, job).Scan(&kind, &outcome, &track); err != nil {
		t.Fatal(err)
	}
	if kind != "plan" || outcome != "clean" || track != nil {
		t.Errorf("job kind %s outcome %s track %v", kind, outcome, track)
	}
	if count(t, r, `SELECT COUNT(*) FROM tracks`) != tracks || count(t, r, `SELECT COUNT(*) FROM mutations`) != mutations {
		t.Error("a plan job wrote a track or a mutation")
	}

	// the job views show track_id null
	jv := obj(h.OK(t, "GET", "/api/jobs/"+job, nil))
	if jv["state"] != "completed" || jv["track_id"] != nil || jv["kind"] != "plan" {
		t.Errorf("get_job = %v", jv)
	}

	// the plans: listed by job, readable, with their files on disk
	list := obj(h.OK(t, "GET", "/api/plans?job_id="+job, nil))
	plans := arr(list["items"])
	if len(plans) != 2 {
		t.Fatalf("list_plans?job_id = %v", list)
	}
	for i, p := range plans {
		pm := obj(p)
		if pm["source"] != "job" || pm["job_id"] != job || pm["seed_index"] != float64(i) || pm["cot"] != "full" || pm["track_id"] != nil {
			t.Errorf("plan %d = %v", i, pm)
		}
		pl := obj(h.OK(t, "GET", "/api/plans/"+pm["id"].(string), nil))
		if _, err := os.Stat(filepath.Join(r.DataDir, "plans", pm["id"].(string), "score.abc")); err != nil {
			t.Errorf("plan %d has no score file: %v", i, err)
		}
		if sc := obj(pl["score"]); sc["dialect"] != "yue2" || sc["aligned"] != true {
			t.Errorf("plan %d score = %v", i, sc)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM desk_plan_files`); n != 4 {
		t.Errorf("%d plan files, want score.abc and plan.json of two plans", n)
	}

	// every seed failing is PLANS_FAILED and stores nothing
	failed, state, code := runPlanJob(t, r, h, "desk-plan-fail", "italian, synth pop, 100 bpm, fake:plans_fail", 2)
	if state != "failed" {
		t.Fatalf("job %s (%s): %s", state, code, jobEvents(t, r, failed))
	}
	if n := count(t, r, `SELECT COUNT(*) FROM desk_plans WHERE job_id = ?`, failed); n != 0 {
		t.Errorf("%d plans of a failed job", n)
	}
}
