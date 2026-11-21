package acceptance_test

import (
	"encoding/json"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// earsRender estimates, creates and runs one compose_new render and returns its job and track ids.
func earsRender(t *testing.T, r *servertest.Rig, h *servertest.Client, id, style string, tierB any) (job, track string, est map[string]any) {
	t.Helper()
	recipe := map[string]any{"id": id, "style": style, "cot": "melody", "seed": 4242,
		"lyrics": "[Verse 1]\nla chiave del mare non è nella casa\nsotto il cielo di sera che non ha una luna\n[Chorus]\nsono io che canto per te e con te\nnon so se è un sogno o una cosa che ho"}
	body := map[string]any{"items": []any{map[string]any{"mode": "compose_new", "recipe": recipe, "title": "Listening check " + id}}}
	if tierB != nil {
		body["options"] = map[string]any{"tier_b": tierB}
	}
	est = obj(h.OK(t, "POST", "/api/generations/estimate", body))
	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "listen to it"}, "Idempotency-Key", "ears-"+id)
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	job = obj(arr(obj(resp.JSON(t))["jobs"])[0])["id"].(string)
	pump(t, r, func() bool { s, _ := jobState(t, r, job); return terminalJob(s) })
	if s, code := jobState(t, r, job); s != "completed" {
		t.Fatalf("job %s (%s): %s", s, code, jobEvents(t, r, job))
	}
	if err := r.Store.R.QueryRow(`SELECT track_id FROM jobs WHERE id = ?`, job).Scan(&track); err != nil {
		t.Fatal(err)
	}
	return job, track, est
}

// TestAcceptance_Ears_RenderWithListeningCheck: on the fake backend, an estimate of a lyric render carries a Tier B budget; the
// render runs the simulated ears stage after the take, the artifact, rows, measured sections, Dossier tier B, flags and perception
// follow, and a take whose check fails still completes (EARS-G1, C339.6).
func TestAcceptance_Ears_RenderWithListeningCheck(t *testing.T) {
	r := genRig(t, archivetest.SyntheticArchive(t, "acceptance"), servertest.Options{M2b: servertest.M2bOptions{Backfill: true}})
	h := r.Human(t)

	// the estimate shows the check and prices it
	plainEst := obj(h.OK(t, "POST", "/api/generations/estimate", map[string]any{"options": map[string]any{"tier_b": false}, "items": []any{map[string]any{"mode": "compose_new",
		"recipe": map[string]any{"id": "ears-plain", "style": "italian, pop", "cot": "melody", "seed": 1, "lyrics": "[Verse 1]\nla chiave del mare non è nella casa"}}}}))
	samples := count(t, r, `SELECT COALESCE(SUM(samples), 0) FROM rtf_stats`)
	job, track, est := earsRender(t, r, h, "ears-ok", "italian, synth pop, 100 bpm", nil)
	take := obj(arr(obj(arr(est["items"])[0])["takes"])[0])
	if take["tier_b"] != true {
		t.Fatalf("estimate take = %v, want tier_b", take)
	}
	if obj(plainEst["cost"])["ceiling_micro_usd"].(float64) >= obj(est["cost"])["ceiling_micro_usd"].(float64) {
		t.Error("the listening check is not in the ceiling")
	}
	if got := obj(arr(obj(arr(plainEst["items"])[0])["takes"])[0])["tier_b"]; got != nil && got != false {
		t.Errorf("tier_b false estimate take has tier_b %v", got)
	}
	var needs, spec string
	var timeout int
	if err := r.Store.R.QueryRow(`SELECT needs_json, spec_json, timeout_s FROM jobs WHERE id = ?`, job).Scan(&needs, &spec, &timeout); err != nil {
		t.Fatal(err)
	}
	if needs != `["ears","v2"]` || timeout != int(take["timeout_s"].(float64)) {
		t.Errorf("job needs %s timeout %d (estimate %v)", needs, timeout, take["timeout_s"])
	}
	var sp struct {
		Ears struct {
			Lang   string   `json:"lang"`
			Budget int      `json:"budget_s"`
			Stages []string `json:"stages"`
		} `json:"ears"`
	}
	if err := json.Unmarshal([]byte(spec), &sp); err != nil || sp.Ears.Lang != "it" || sp.Ears.Budget < 60 || len(sp.Ears.Stages) != 2 {
		t.Errorf("spec = %s (%v)", spec, err)
	}

	// a render with needs [ears, v2] still teaches the render-phase RTF (amendment A1; the worker's e2e seconds exclude Tier B)
	if got := count(t, r, `SELECT COALESCE(SUM(samples), 0) FROM rtf_stats`); got != samples+1 {
		t.Errorf("rtf samples %d -> %d, want one more", samples, got)
	}

	// the artifact, the rows and the measured sections
	if n := count(t, r, `SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'analysis.json'`, track); n != 1 {
		t.Fatalf("analysis.json artifacts = %d", n)
	}
	for _, a := range []string{"tier_b_raw", "tier_b_diction"} {
		if n := count(t, r, `SELECT COUNT(*) FROM analyses WHERE track_id = ? AND analyzer = ?`, track, a); n != 1 {
			t.Errorf("%s rows = %d", a, n)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM sections WHERE track_id = ? AND source = 'measured'`, track); n == 0 {
		t.Error("no measured sections")
	}
	tb := obj(h.OK(t, "GET", "/api/tracks/"+track+"/tier-b", nil))
	if tb["state"] != "ok" || tb["status"] != "ok" {
		t.Errorf("tier-b = %v", tb)
	}
	an := obj(h.OK(t, "GET", "/api/tracks/"+track+"/analysis", nil))
	if an["tier"] != "B" {
		t.Errorf("analysis tier = %v, want B", an["tier"])
	}
	ctx := obj(h.OK(t, "GET", "/api/tracks/"+track+"/context", nil))
	if d := obj(obj(ctx["perception"])["diction"]); d == nil {
		t.Errorf("perception has no diction block: %v", ctx["perception"])
	}

	// a failing recogniser never fails the take: it completes and the check reads error
	_, bad, _ := earsRender(t, r, h, "ears-bad", "italian, synth pop, 100 bpm, fake:ears_error", nil)
	if tb := obj(h.OK(t, "GET", "/api/tracks/"+bad+"/tier-b", nil)); tb["state"] != "error" {
		t.Errorf("failed check: tier-b = %v", tb)
	}
	// a skipped stage never fails the take either
	_, skipped, _ := earsRender(t, r, h, "ears-skip", "italian, synth pop, 100 bpm, fake:ears_skip", nil)
	if n := count(t, r, `SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'analysis.json'`, skipped); n != 0 {
		t.Errorf("a skipped stage left %d analysis artifacts", n)
	}
	// tier_b false: a plain job, no needs, no artifact
	pj, plain, _ := earsRender(t, r, h, "ears-off", "italian, synth pop, 100 bpm", false)
	var pn string
	if err := r.Store.R.QueryRow(`SELECT needs_json FROM jobs WHERE id = ?`, pj).Scan(&pn); err != nil || pn != "[]" {
		t.Errorf("tier_b false job needs = %s (%v)", pn, err)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'analysis.json'`, plain); n != 0 {
		t.Errorf("a plain take has %d analysis artifacts", n)
	}
}

// jobEvents is the event trail of a job, for failure messages.
func jobEvents(t *testing.T, r *servertest.Rig, job string) string {
	t.Helper()
	rows, err := r.Store.R.Query(`SELECT event, COALESCE(detail_json, '') FROM job_events WHERE job_id = ? ORDER BY id`, job)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = rows.Close() }()
	var out string
	var needs, spec string
	if err := r.Store.R.QueryRow(`SELECT needs_json, spec_json FROM jobs WHERE id = ?`, job).Scan(&needs, &spec); err == nil {
		out = "needs " + needs + " spec " + spec + "; "
	}
	for rows.Next() {
		var n, d string
		if err := rows.Scan(&n, &d); err != nil {
			return err.Error()
		}
		out += n + " " + d + "; "
	}
	return out
}
