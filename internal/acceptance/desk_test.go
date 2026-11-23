package acceptance_test

// DESK-X0 (m5b-desk C364.2): the Composer's Desk end to end on the fake backend (no GPU spend, nothing real is called). The audition
// pipeline is desktest's double in the tests that do not need audio (the rig has no soundfont unless a test asks for one), and the
// real fluidsynth in the media tests below, which skip cleanly when it is absent unless MM_REQUIRE_FLUIDSYNTH=1 (CI sets it).

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// deskRig is a generation rig over the synthetic archive whose Desk auditions through desktest's double.
func deskRig(t *testing.T) *servertest.Rig {
	t.Helper()
	return genRig(t, archivetest.SyntheticArchive(t, "acceptance"),
		servertest.Options{M2b: servertest.M2bOptions{Backfill: true}, AppHooks: []app.BuildHook{deskFakeAudition(t)}})
}

// scoreTake is the id of an archive take that carries a score.abc of its own.
func scoreTake(t *testing.T, r *servertest.Rig) string {
	t.Helper()
	var id string
	if err := r.Store.R.QueryRow(`SELECT track_id FROM scores ORDER BY track_id LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("the synthetic archive has no take with a score: %v", err)
	}
	return id
}

// planABC reads a plan's score text over REST.
func planABC(t *testing.T, c *servertest.Client, id string) string {
	t.Helper()
	abc, _ := obj(c.OK(t, "GET", "/api/plans/"+id+"?include=abc", nil))["abc"].(string)
	if abc == "" {
		t.Fatalf("plan %s has no abc", id)
	}
	return abc
}

var deskIdem int

// deskPost is a POST that needs an Idempotency-Key, with a key that is new for every call.
func deskPost(t *testing.T, c *servertest.Client, path string, body any) *servertest.Response {
	t.Helper()
	deskIdem++
	return c.Do(t, "POST", path, body, "Idempotency-Key", fmt.Sprintf("desk-x0-%d", deskIdem))
}

// tempoEdit is an edit every score accepts.
func tempoEdit(bpm int) map[string]any {
	return map[string]any{"rationale": "acceptance", "ops": []map[string]any{{"op": "set_tempo", "bpm": bpm}}}
}

// assertIngested is the state a rendered take must be in once its ingest finished: an ordinary frozen master (mode 0444) with the
// loudness and spectrum analyses stored and finite. The NaN-loudness hotfix (a score render ends in exact digital silence) passed
// silently once because nothing asserted this.
func assertIngested(t *testing.T, r *servertest.Rig, track string) {
	t.Helper()
	var master string
	if err := r.Store.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, track).Scan(&master); err != nil {
		t.Fatalf("take %s has no tracks row: %v", track, err)
	}
	fi, err := os.Stat(r.DataDir + "/" + master)
	if err != nil {
		t.Fatalf("take %s: no master: %v", track, err)
	}
	if fi.Mode().Perm() != 0o444 {
		t.Errorf("take %s: master mode %v, want 0444", track, fi.Mode().Perm())
	}
	for _, an := range []string{"loudness", "spectrum"} {
		var m string
		if err := r.Store.R.QueryRow(`SELECT metrics_json FROM analyses WHERE track_id = ? AND analyzer = ?`, track, an).Scan(&m); err != nil {
			t.Errorf("take %s: no %s analysis: %v", track, an, err)
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(m), &doc); err != nil || len(doc) == 0 {
			t.Errorf("take %s: %s metrics %q: %v", track, an, m, err)
		}
		if strings.Contains(strings.ToLower(m), "nan") {
			t.Errorf("take %s: %s metrics carry NaN: %s", track, an, m)
		}
		// a NaN or Inf metric is not valid JSON: it is stored as null (or dropped), so a null is the NaN regression
		for k, v := range doc {
			if v == nil {
				t.Errorf("take %s: %s.%s is null (a NaN or Inf metric)", track, an, k)
			}
			if f, ok := v.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
				t.Errorf("take %s: %s.%s = %v", track, an, k, f)
			}
		}
		if an == "loudness" {
			for _, k := range []string{"integrated_lufs", "true_peak_dbtp"} {
				if _, ok := doc[k].(float64); !ok {
					t.Errorf("take %s: loudness.%s = %v, want a finite number", track, k, doc[k])
				}
			}
		}
	}
}

// spendRows counts what a spending path writes: estimates, batches, jobs, ledger rows, attempts.
func spendRows(t *testing.T, r *servertest.Rig) [5]int {
	t.Helper()
	var out [5]int
	for i, tb := range []string{"estimates", "batches", "jobs", "ledger", "job_attempts"} {
		out[i] = count(t, r, `SELECT COUNT(*) FROM `+tb)
	}
	return out
}

// TestAcceptance_DeskSpendFree (C364.2): a plan from a take, two edits and two auditions write no estimate, batch, job, ledger row or
// attempt, and no take.
func TestAcceptance_DeskSpendFree(t *testing.T) {
	r := deskRig(t)
	h := r.Human(t)
	tracks, spend := count(t, r, `SELECT COUNT(*) FROM tracks`), spendRows(t, r)

	plan := r.DeskPlanFromFixture(t, scoreTake(t, r))
	p0 := obj(h.OK(t, "GET", "/api/plans/"+plan, nil))
	if p0["source"] != "take" {
		t.Errorf("plan source = %v, want take", p0["source"])
	}
	e1 := obj(deskPost(t, h, "/api/plans/"+plan+"/edits", tempoEdit(88)).JSON(t))
	e2 := obj(deskPost(t, h, "/api/plans/"+e1["id"].(string)+"/edits", tempoEdit(92)).JSON(t))
	if e1["source"] != "edit" || e1["parent_plan_id"] != plan || e2["parent_plan_id"] != e1["id"] {
		t.Fatalf("edit chain: %v then %v", e1, e2)
	}
	if planABC(t, h, plan) == planABC(t, h, e2["id"].(string)) {
		t.Error("two tempo edits left the score unchanged")
	}
	for _, id := range []string{plan, e2["id"].(string)} {
		// 200 is allowed for the second: desktest's double renders the same PCM for any score, so the stored MP3 is shared
		if resp := deskPost(t, h, "/api/plans/"+id+"/auditions", map[string]any{"chords": "block"}); resp.Status != 201 && resp.Status != 200 {
			t.Fatalf("audition %s: %d %s", id, resp.Status, resp.Body)
		}
	}
	if resp := deskPost(t, h, "/api/plans/"+plan+"/auditions", map[string]any{"chords": "block"}); resp.Status != 200 {
		t.Errorf("the same audition again = %d, want the cached 200", resp.Status)
	}

	if got := spendRows(t, r); got != spend {
		t.Errorf("estimates/batches/jobs/ledger/attempts = %v, was %v: the spend-free half of the Desk wrote spend rows", got, spend)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM tracks`); n != tracks {
		t.Errorf("%d tracks, was %d: an audition or an edit made a take", n, tracks)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM desk_plans`); n != 3 {
		t.Errorf("%d plans, want the take's plan and its two edits", n)
	}
	// one or two rows: under desktest's double both plans render the same PCM, so their MP3 is one stored file; the repeat is a cache hit
	if n := count(t, r, `SELECT COUNT(*) FROM desk_auditions`); n < 1 || n > 2 {
		t.Errorf("%d auditions, want 1 or 2 (the repeat is a cache hit)", n)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM desk_auditions WHERE plan_id NOT IN (?, ?)`, plan, e2["id"].(string)); n != 0 {
		t.Errorf("%d auditions belong to a plan that was not auditioned", n)
	}
}

// renderPlans estimates and creates a render_plan batch for the plans as c, answering the create response.
func renderPlans(t *testing.T, c *servertest.Client, path, key string, plans ...string) (est, created map[string]any, status int) {
	t.Helper()
	est = obj(c.OK(t, "POST", "/api/generations/estimate", map[string]any{"items": []any{map[string]any{"mode": "render_plan", "desk": map[string]any{"plan_ids": plans}}}}))
	resp := c.Do(t, "POST", path, map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "render the plan"}, "Idempotency-Key", key)
	return est, obj(resp.JSON(t)), resp.Status
}

// TestAcceptance_DeskLoop (C364.2): plan_seeds of 4 on the fake backend, four plans, an audition, an edit, an audition of the edit,
// then render_plan: a take with operator render_plan, params.plan_id and a recipe whose score is the edited plan's. The take's
// ingest state is asserted.
func TestAcceptance_DeskLoop(t *testing.T) {
	r := deskRig(t)
	h := r.Human(t)

	job, state, code := runPlanJob(t, r, h, "desk-loop", "italian, synth pop, 100 bpm", 4)
	if state != "completed" {
		t.Fatalf("plan job %s (%s): %s", state, code, jobEvents(t, r, job))
	}
	plans := arr(obj(h.OK(t, "GET", "/api/plans?job_id="+job, nil))["items"])
	if len(plans) != 4 {
		t.Fatalf("%d plans from a plan job of 4 seeds", len(plans))
	}
	first := obj(plans[0])["id"].(string)

	if resp := deskPost(t, h, "/api/plans/"+first+"/auditions", map[string]any{}); resp.Status != 201 {
		t.Fatalf("audition: %d %s", resp.Status, resp.Body)
	}
	edit := obj(deskPost(t, h, "/api/plans/"+first+"/edits", tempoEdit(84)).JSON(t))
	child := edit["id"].(string)
	if edit["parent_plan_id"] != first || edit["source"] != "edit" {
		t.Fatalf("edit = %v", edit)
	}
	// 200 is allowed: desktest's double renders the same PCM for any score, so the stored MP3 of the first audition is reused
	if resp := deskPost(t, h, "/api/plans/"+child+"/auditions", map[string]any{}); resp.Status != 201 && resp.Status != 200 {
		t.Fatalf("audition of the edit: %d %s", resp.Status, resp.Body)
	}
	want := planABC(t, h, child)

	before := count(t, r, `SELECT COUNT(*) FROM tracks`)
	est, created, st := renderPlans(t, h, "/human/api/generations", "desk-loop-render", child)
	if st != 201 {
		t.Fatalf("create render_plan: %d %v (estimate %v)", st, created, est)
	}
	jobID := obj(arr(created["jobs"])[0])["id"].(string)
	pump(t, r, func() bool { s, _ := jobState(t, r, jobID); return terminalJob(s) })
	if s, c := jobState(t, r, jobID); s != "completed" {
		t.Fatalf("render job %s (%s): %s", s, c, jobEvents(t, r, jobID))
	}

	var op, mut string
	var track *string
	if err := r.Store.R.QueryRow(`SELECT operator, mutation_json, track_id FROM jobs WHERE id = ?`, jobID).Scan(&op, &mut, &track); err != nil {
		t.Fatal(err)
	}
	var m struct {
		Params struct {
			PlanID string `json:"plan_id"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(mut), &m); err != nil {
		t.Fatal(err)
	}
	if op != "render_plan" || m.Params.PlanID != child || track == nil {
		t.Fatalf("job operator %q plan_id %q track %v", op, m.Params.PlanID, track)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM tracks`); n != before+1 {
		t.Errorf("%d tracks, want one new take", n)
	}
	var reqJSON string
	if err := r.Store.R.QueryRow(`SELECT c.request_json FROM tracks t JOIN recipes c ON c.id = t.recipe_id WHERE t.id = ?`, *track).Scan(&reqJSON); err != nil {
		t.Fatal(err)
	}
	var req struct {
		ABC string `json:"abc"`
	}
	if err := json.Unmarshal([]byte(reqJSON), &req); err != nil {
		t.Fatal(err)
	}
	if req.ABC != want {
		t.Errorf("the take's recipe carries a score that is not the edited plan's:\n%s\nwant\n%s", req.ABC, want)
	}
	assertIngested(t, r, *track)

	// the plan lists its render
	rendered := obj(h.OK(t, "GET", "/api/plans/"+child+"?include=renders", nil))
	if rs := arr(rendered["renders"]); len(rs) != 1 {
		t.Errorf("renders of the plan = %v", rendered["renders"])
	}
}

// TestAcceptance_DeskEditorFlow (C364.2): an Editor plans from a take, edits and auditions; the render batch it creates waits for the
// Owner, who approves it; the Editor can neither approve nor give a verdict, and a Listener reaches no plan.
func TestAcceptance_DeskEditorFlow(t *testing.T) {
	r := deskRig(t)
	owner := r.Human(t)
	bob := r.User(t, servertest.UserEditor, config.RoleEditor)
	carol := r.User(t, servertest.UserListener, config.RoleListener)

	plan := obj(deskPost(t, bob, "/api/tracks/"+scoreTake(t, r)+"/plan", nil).JSON(t))["id"].(string)
	child := obj(deskPost(t, bob, "/api/plans/"+plan+"/edits", tempoEdit(90)).JSON(t))["id"].(string)
	if resp := deskPost(t, bob, "/api/plans/"+child+"/auditions", map[string]any{}); resp.Status != 201 {
		t.Fatalf("editor audition: %d %s", resp.Status, resp.Body)
	}
	if resp := bob.Do(t, "PUT", "/api/plans/"+child+"/verdict", map[string]any{"verdict": "liked"}); resp.Status != 403 {
		t.Errorf("an Editor's verdict: %d %s, want 403", resp.Status, resp.Body)
	}
	if resp := carol.Do(t, "GET", "/api/plans", nil); resp.Status != 403 {
		t.Errorf("a Listener's list_plans: %d, want 403", resp.Status)
	}
	if resp := carol.Do(t, "GET", "/api/plans/"+child, nil); resp.Status != 403 {
		t.Errorf("a Listener's get_plan: %d, want 403", resp.Status)
	}

	est, created, st := renderPlans(t, bob, "/api/generations", "desk-editor-render", child)
	if est["requires_human_approval"] != true {
		t.Errorf("an Editor's render does not require approval: %v", est["approval_reasons"])
	}
	if !slices.Contains(anyStrings(est["approval_reasons"]), "editor_batch") {
		t.Errorf("an Editor's approval reasons = %v, want editor_batch", est["approval_reasons"])
	}
	if st != 201 && st != 200 {
		t.Fatalf("editor create: %d %v", st, created)
	}
	batch := created["id"].(string)
	if created["state"] != "awaiting_confirmation" {
		t.Errorf("batch state %v, want awaiting_confirmation", created["state"])
	}
	for range 5 { // the runner must not touch it
		r.Clock.Advance(10 * time.Second)
		if _, err := r.Runner.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := r.Reconciler.Pass(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state <> 'awaiting_confirmation'`, batch); n != 0 {
		t.Errorf("%d jobs of an unapproved Editor batch moved", n)
	}
	if resp := bob.Do(t, "POST", "/human/api/batches/"+batch+"/approve", map[string]any{"reason": "self"}); resp.Status != 403 {
		t.Errorf("an Editor's approve: %d, want 403", resp.Status)
	}
	if resp := owner.Do(t, "POST", "/human/api/batches/"+batch+"/approve", map[string]any{"reason": "ok"}); resp.Status != 200 {
		t.Fatalf("owner approve: %d %s", resp.Status, resp.Body)
	}
	job := count(t, r, `SELECT COUNT(*) FROM jobs WHERE batch_id = ?`, batch)
	if job != 1 {
		t.Fatalf("%d jobs in the batch", job)
	}
	var jobID string
	if err := r.Store.R.QueryRow(`SELECT id FROM jobs WHERE batch_id = ?`, batch).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	pump(t, r, func() bool { s, _ := jobState(t, r, jobID); return terminalJob(s) })
	var track *string
	if s, c := jobState(t, r, jobID); s != "completed" {
		t.Fatalf("approved render %s (%s)", s, c)
	}
	if err := r.Store.R.QueryRow(`SELECT track_id FROM jobs WHERE id = ?`, jobID).Scan(&track); err != nil || track == nil {
		t.Fatalf("no take after the approval: %v", err)
	}
	assertIngested(t, r, *track)

	// an agent's render of a Desk plan never auto-runs either: it waits for the Owner (desk_render)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)
	aest, acreated, ast := renderPlans(t, agent, "/api/generations", "desk-agent-render", child)
	if !slices.Contains(anyStrings(aest["approval_reasons"]), "desk_render") {
		t.Errorf("an agent's approval reasons = %v, want desk_render", aest["approval_reasons"])
	}
	if (ast != 201 && ast != 200) || acreated["state"] != "awaiting_confirmation" {
		t.Errorf("agent render: %d state %v, want awaiting_confirmation", ast, acreated["state"])
	}
}

// TestAcceptance_DeskAuditionReal (media tier): the real fluidsynth, the generated soundfont, the real child and ffmpeg make an
// audition of a plan from a take; the MP3 is served by its signed url.
func TestAcceptance_DeskAuditionReal(t *testing.T) {
	t1BuildChild(t)
	o := servertest.Options{M2b: servertest.M2bOptions{Backfill: true}}
	o.WithSoundfont(t)
	o.T1.Child = &transform.ExecChild{Self: t1ChildBin()}
	r := genRig(t, archivetest.SyntheticArchive(t, "acceptance"), o)
	h := r.Human(t)
	plan := r.DeskPlanFromFixture(t, scoreTake(t, r))
	tracksBefore := count(t, r, `SELECT COUNT(*) FROM tracks`)
	resp := deskPost(t, h, "/api/plans/"+plan+"/auditions", map[string]any{"soundfont": "gm", "tail_s": 1.0})
	if resp.Status != 201 {
		t.Fatalf("audition: %d %s", resp.Status, resp.Body)
	}
	au := obj(resp.JSON(t))
	if au["soundfont"] != "gm" || au["seconds"].(float64) <= 0 {
		t.Errorf("audition = %v", au)
	}
	got := h.Do(t, "GET", au["url"].(string), nil)
	if got.Status != 200 || len(got.Body) < 1000 {
		t.Fatalf("the audition MP3: %d, %d bytes", got.Status, len(got.Body))
	}
	if b := got.Body; !(string(b[:3]) == "ID3" || (b[0] == 0xff && b[1]&0xe0 == 0xe0)) {
		t.Errorf("the audition is not an MP3: % x", b[:4])
	}
	// an audition is a Desk file, never a take: the library has exactly the tracks it had before
	if n := count(t, r, `SELECT COUNT(*) FROM tracks`); n != tracksBefore {
		t.Errorf("an audition became a take: %d tracks, %d before", n, tracksBefore)
	}
}

// scoreTune is a short standard-ABC tune: four bars, then the default 3 s of exact digital silence the renderer appends (the shape
// that once failed ingest with NaN momentary loudness).
const scoreTune = "X:1\nT:Alice's scale\nM:4/4\nL:1/4\nQ:1/4=120\nK:C\n\"C\"CDEF|\"G\"GABc|\"C\"cBAG|\"G\"FEDC|\n"

// TestAcceptance_ScoreRenderTake (T1b end to end, C364.2): create_transform kind score_render over the real surface makes an ordinary
// rootless Take through fluidsynth, ffmpeg, the render child and the real ingest pipeline, in its default tail and with chords; the
// ingest state of each is asserted.
func TestAcceptance_ScoreRenderTake(t *testing.T) {
	tr := newT1RigWith(t, func(o *servertest.Options) { o.WithSoundfont(t) })
	for name, extra := range map[string]map[string]any{"default tail": {}, "chords block": {"chords": "block", "tail_s": 1.0}} {
		p := map[string]any{"score": map[string]any{"abc": scoreTune}, "dialect": "standard", "soundfont": "gm"}
		for k, v := range extra {
			p[k] = v
		}
		got := tr.RunDone(map[string]any{"kind": "score_render", "score_render": p})
		id := got["id"].(string)
		var engine string
		var parent *string
		if err := tr.Store.R.QueryRow(`SELECT c.engine, t.parent_track_id FROM tracks t JOIN recipes c ON c.id = t.recipe_id WHERE t.id = ?`, id).Scan(&engine, &parent); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if engine != "dsp" || parent != nil {
			t.Errorf("%s: engine %q parent %v, want a rootless dsp take", name, engine, parent)
		}
		if state, pcm, code := tr.Row(id); state != "done" || pcm == "" || code != "" {
			t.Errorf("%s: transform %s pcm %q code %q", name, state, pcm, code)
		}
		assertIngested(t, tr.Rig, id)
	}
}
