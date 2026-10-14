package acceptance_test

import (
	"context"
	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// knownMother returns a lossless track of the archive whose recipe is known (a valid generation mother).
func knownMother(t *testing.T, r *servertest.Rig) string {
	t.Helper()
	var id string
	if err := r.Store.R.QueryRow(`SELECT t.id FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id
		WHERE rc.engine <> 'unknown' AND t.lossy = 0 ORDER BY t.id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func arr(v any) []any { a, _ := v.([]any); return a }

// TestAcceptance_M3_NewSeedVariantLinkedChildWithCost: a new-seed variant through the human path runs on the
// fake backend with the real ingest and lands as a linked child with its cost (roadmap M3).
func TestAcceptance_M3_NewSeedVariantLinkedChildWithCost(t *testing.T) {
	r := genRig(t, archivetest.SyntheticArchive(t, "acceptance"), servertest.Options{M2b: servertest.M2bOptions{Backfill: true}})
	h := r.Human(t)
	mother := knownMother(t, r)
	var motherRoot string
	var motherGen int
	if err := r.Store.R.QueryRow(`SELECT root_track_id, generation FROM tracks WHERE id = ?`, mother).Scan(&motherRoot, &motherGen); err != nil {
		t.Fatal(err)
	}
	est := obj(h.OK(t, "POST", "/api/tracks/"+mother+"/variants/estimate", map[string]any{"mode": "new_seed", "n_seeds": 1}))
	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "try another seed"}, "Idempotency-Key", "nsv-1")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	batch := obj(resp.JSON(t))
	job := obj(arr(batch["jobs"])[0])["id"].(string)
	pump(t, r, func() bool { s, _ := jobState(t, r, job); return terminalJob(s) })
	if s, code := jobState(t, r, job); s != "completed" {
		t.Fatalf("job %s (%s)", s, code)
	}
	j := obj(h.OK(t, "GET", "/api/jobs/"+job, nil))
	child, _ := j["track_id"].(string)
	if child == "" {
		t.Fatalf("job = %v", j)
	}
	var origin, parent, root, by string
	var gen int
	var env *string
	if err := r.Store.R.QueryRow(`SELECT origin, parent_track_id, root_track_id, generation, env_id, status_set_by FROM tracks WHERE id = ?`, child).
		Scan(&origin, &parent, &root, &gen, &env, &by); err != nil {
		t.Fatal(err)
	}
	if origin != "job" || parent != mother || root != motherRoot || gen != motherGen+1 || env == nil || *env == "" {
		t.Errorf("child: origin=%s parent=%s root=%s gen=%d env=%v", origin, parent, root, gen, env)
	}
	var op, rationale, mjob string
	var inferred int
	if err := r.Store.R.QueryRow(`SELECT operator, inferred, rationale, job_id FROM mutations WHERE child_track_id = ?`, child).Scan(&op, &inferred, &rationale, &mjob); err != nil {
		t.Fatal(err)
	}
	if op != "reroll_seed" || inferred != 0 || rationale != "try another seed" || mjob != job {
		t.Errorf("mutation: %s inferred=%d rationale=%q job=%s", op, inferred, rationale, mjob)
	}
	var fp int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM env_fingerprints WHERE env_id = ?`, *env).Scan(&fp); err != nil || fp != 1 {
		t.Errorf("env fingerprint rows = %d (%v)", fp, err)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM analyses WHERE track_id = ?`, child); n == 0 {
		t.Error("the child has no Ears Tier A rows")
	}
	tr := obj(h.OK(t, "GET", "/api/tracks/"+child, nil))
	gi := obj(tr["generation"])
	cost := obj(gi["cost"])
	if gi["operator"] != "reroll_seed" || gi["job_id"] != job || cost["basis"] != "provisional" || cost["display_micro_usd"].(float64) <= 0 {
		t.Errorf("generation = %v", gi)
	}
	var found bool
	for _, n := range arr(obj(h.OK(t, "GET", "/api/tracks/"+child+"/lineage", nil))["nodes"]) {
		if obj(n)["id"] == child && obj(n)["parent_id"] == mother && obj(n)["operator"] == "reroll_seed" {
			found = true
		}
	}
	if !found {
		t.Error("the lineage tree does not show the child under its mother")
	}
}

// draftItems estimates one from_draft item over every draft of the archive.
func draftIDs(t *testing.T, r *servertest.Rig) []string {
	t.Helper()
	rows, err := r.Store.R.Query(`SELECT id FROM recipe_drafts WHERE status = 'backlog' AND parent_track_id IS NOT NULL ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestAcceptance_M3_V2Batch: seven drafts with seven distinct mothers are generated from the UI with one
// estimate and one confirmation; the same estimate made by an agent waits for the operator (roadmap M3).
func TestAcceptance_M3_V2Batch(t *testing.T) {
	tpl := archivetest.GenerateArchive(t)
	est := func(c *servertest.Client, ids []string) map[string]any {
		return obj(c.OK(t, "POST", "/api/generations/estimate", map[string]any{"items": []any{map[string]any{"mode": "from_draft", "draft_ids": ids}}}))
	}

	t.Run("human", func(t *testing.T) {
		t.Parallel()
		r := genRig(t, tpl, servertest.Options{})
		h := r.Human(t)
		ids := draftIDs(t, r)
		if len(ids) != 7 {
			t.Fatalf("%d drafts, want 7", len(ids))
		}
		mothers := map[string]string{}
		for _, id := range ids {
			var m string
			if err := r.Store.R.QueryRow(`SELECT parent_track_id FROM recipe_drafts WHERE id = ?`, id).Scan(&m); err != nil {
				t.Fatal(err)
			}
			mothers[id] = m
		}
		e := est(h, ids)
		if e["takes"] != float64(7) {
			t.Fatalf("estimate = %v", e)
		}
		resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": e["confirmation_token"], "rationale": "v2 batch"}, "Idempotency-Key", "v2-human")
		if resp.Status != 201 {
			t.Fatalf("create: %d %s", resp.Status, resp.Body)
		}
		b := obj(resp.JSON(t))
		if b["state"] != "active" || b["via"] != "human_create" || obj(b["approval"])["via"] != "human_create" {
			t.Fatalf("batch = %v", b)
		}
		batch := b["id"].(string)
		pump(t, r, func() bool {
			return count(t, r, `SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state IN ('completed','failed','cancelled')`, batch) == 7
		})
		if n := count(t, r, `SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state = 'completed'`, batch); n != 7 {
			t.Fatalf("%d of 7 jobs completed", n)
		}
		reused := 0
		for _, id := range ids {
			var child, parent, req, status string
			if err := r.Store.R.QueryRow(`SELECT d.fulfilled_by_track_id, t.parent_track_id, rc.request_id, d.status
				FROM recipe_drafts d JOIN tracks t ON t.id = d.fulfilled_by_track_id JOIN recipes rc ON rc.id = t.recipe_id WHERE d.id = ?`, id).
				Scan(&child, &parent, &req, &status); err != nil {
				t.Fatalf("draft %s: %v", id, err)
			}
			// An imported draft owns a recipe row that the render reuses (id kept, D-M3-10); a draft created in
			// the app has none, so its take gets "<request id>-s<seed>".
			var want string
			var imported bool
			if err := r.Store.R.QueryRow(`SELECT request_id, source_recipe_id IS NOT NULL FROM recipe_drafts WHERE id = ?`, id).Scan(&want, &imported); err != nil {
				t.Fatal(err)
			}
			okReq := req == want
			if !imported {
				okReq = strings.HasPrefix(req, want+"-s")
			} else {
				reused++
			}
			if status != "fulfilled" || parent != mothers[id] || !okReq {
				t.Errorf("draft %s: status=%s parent=%s (want %s) request=%s (draft request %s, imported %v)", id, status, parent, mothers[id], req, want, imported)
			}
		}
		if reused != 1 {
			t.Errorf("%d drafts reused their recipe row, want the one imported draft", reused)
		}
		if n := count(t, r, `SELECT COUNT(*) FROM approvals WHERE batch_id = ?`, batch); n != 1 {
			t.Errorf("%d approval rows", n)
		}
	})

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		r := genRig(t, tpl, servertest.Options{})
		agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
		ids := draftIDs(t, r)
		e := est(agent, ids)
		if e["requires_human_approval"] != true {
			t.Fatalf("estimate = %v", e)
		}
		resp := agent.Do(t, "POST", "/api/generations", map[string]any{"confirmation_token": e["confirmation_token"]}, "Idempotency-Key", "v2-agent")
		if resp.Status != 201 {
			t.Fatalf("create: %d %s", resp.Status, resp.Body)
		}
		b := obj(resp.JSON(t))
		if b["state"] != "awaiting_confirmation" {
			t.Fatalf("batch = %v", b)
		}
		var reasons []string
		for _, x := range arr(obj(b["approval"])["reasons"]) {
			reasons = append(reasons, x.(string))
		}
		if !slices.Contains(reasons, "takes_over_limit") || !slices.Contains(reasons, "cost_over_limit") {
			t.Errorf("reasons = %v", reasons)
		}
		batch := b["id"].(string)
		// nothing is submitted while the batch waits
		r.Clock.Advance(time.Minute)
		if _, err := r.Runner.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := count(t, r, `SELECT COUNT(*) FROM job_attempts`); n != 0 {
			t.Errorf("%d attempts before approval", n)
		}
		if n := r.Fake.Counters().Submit; n != 0 {
			t.Errorf("%d submits before approval", n)
		}
		h := r.Human(t)
		h.OK(t, "POST", "/human/api/batches/"+batch+"/approve", map[string]any{"reason": "ok"})
		pump(t, r, func() bool {
			return count(t, r, `SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state = 'completed'`, batch) == 7
		})
	})
}

// TestAcceptance_M3_ComposeNewRoot: "new song" composes a root track with its cost (roadmap M3).
func TestAcceptance_M3_ComposeNewRoot(t *testing.T) {
	r := genRig(t, archivetest.SyntheticArchive(t, "acceptance"), servertest.Options{})
	h := r.Human(t)
	recipe := map[string]any{"id": "acc-compose-1", "style": "english, synth pop, 100 bpm",
		"lyrics": "[Verse]\nla la\nla\nla\nla\n[Chorus]\nla\nla\nla\nla", "cot": "full", "seed": 4242}
	est := obj(h.OK(t, "POST", "/api/generations/estimate", map[string]any{"items": []any{map[string]any{"mode": "compose_new", "recipe": recipe, "title": "Acceptance root"}}}))
	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "a new song"}, "Idempotency-Key", "compose-1")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	job := obj(arr(obj(resp.JSON(t))["jobs"])[0])["id"].(string)
	pump(t, r, func() bool { s, _ := jobState(t, r, job); return terminalJob(s) })
	if s, code := jobState(t, r, job); s != "completed" {
		t.Fatalf("job %s (%s)", s, code)
	}
	var track string
	if err := r.Store.R.QueryRow(`SELECT track_id FROM jobs WHERE id = ?`, job).Scan(&track); err != nil {
		t.Fatal(err)
	}
	var parent *string
	var root, origin, title string
	var gen int
	if err := r.Store.R.QueryRow(`SELECT parent_track_id, root_track_id, generation, origin, title FROM tracks WHERE id = ?`, track).Scan(&parent, &root, &gen, &origin, &title); err != nil {
		t.Fatal(err)
	}
	if parent != nil || root != track || gen != 0 || origin != "job" || title != "Acceptance root" {
		t.Errorf("root track: parent=%v root=%s gen=%d origin=%s title=%q", parent, root, gen, origin, title)
	}
	var mp *string
	var op string
	if err := r.Store.R.QueryRow(`SELECT parent_track_id, operator FROM mutations WHERE child_track_id = ?`, track).Scan(&mp, &op); err != nil {
		t.Fatal(err)
	}
	if mp != nil || op != "compose_new" {
		t.Errorf("mutation: parent=%v operator=%s", mp, op)
	}
	cost := obj(obj(obj(h.OK(t, "GET", "/api/tracks/"+track, nil))["generation"])["cost"])
	if cost["display_micro_usd"].(float64) <= 0 {
		t.Errorf("cost = %v", cost)
	}
}

// backendCalls is every request the provider saw so far: each fake method (Quote included) or each
// runpodtest request (GETs included).
func (p *provRig) backendCalls() int64 {
	if p.kind == "fake" {
		return p.Fake.Counters().Total()
	}
	return int64(len(p.rp.Requests()))
}

func noRows(t *testing.T, r *servertest.Rig, what string) {
	t.Helper()
	for _, tbl := range []string{"batches", "jobs", "job_attempts", "ledger", "approvals"} {
		if n := count(t, r, `SELECT COUNT(*) FROM `+tbl); n != 0 { //nolint:gosec // fixed table names
			t.Errorf("%s: %d %s rows", what, n, tbl)
		}
	}
}

// TestAcceptance_M3_OverBudgetZeroBackendCalls: a create over a cap is refused 402 BUDGET_EXCEEDED before any
// backend call; the token stays consumable once the cap is raised (roadmap M3).
func TestAcceptance_M3_OverBudgetZeroBackendCalls(t *testing.T) {
	caps := spend.Caps{MonthlyMicro: 300_000, BatchMicro: 300_000, HumanDailyMicro: 300_000, AgentDailyMicro: 300_000, AutoApproveTakes: 100, AutoApproveMicro: 300_000}
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	items := func(mother string) []any {
		return []any{map[string]any{"mode": "new_seed", "parent_track_id": mother, "n_seeds": 4}}
	}
	for _, who := range []string{"agent", "human"} {
		t.Run(who, func(t *testing.T) {
			t.Parallel()
			forEachBackend(t, func(t *testing.T, kind string) {
				p := newProvRig(t, kind, tpl, servertest.Options{Caps: &caps})
				mother := knownMother(t, p.Rig)
				h := p.Human(t)
				var c *servertest.Client
				path, hdr := "/api/generations", "ov-agent"
				if who == "agent" {
					c, _ = p.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
				} else {
					c, path, hdr = h, "/human/api/generations", "ov-human"
				}
				before := p.backendCalls()
				est := obj(c.OK(t, "POST", "/api/generations/estimate", map[string]any{"items": items(mother)}))
				if obj(est["budget"])["fits"] != false {
					t.Fatalf("budget = %v", est["budget"])
				}
				tok := est["confirmation_token"]
				resp := c.Do(t, "POST", path, map[string]any{"confirmation_token": tok}, "Idempotency-Key", hdr)
				if pb := obj(resp.JSON(t)); resp.Status != 402 || pb["code"] != "BUDGET_EXCEEDED" {
					t.Fatalf("create: %d %s", resp.Status, resp.Body)
				}
				if got := p.backendCalls(); got != before {
					t.Errorf("%d backend calls during the estimate and the refused create, want 0", got-before)
				}
				noRows(t, p.Rig, "after the refusal")
				// raising the cap makes the same token usable
				h.OK(t, "PUT", "/human/api/spend/caps", map[string]any{"monthly_micro_usd": 50_000_000, "batch_micro_usd": 50_000_000,
					"human_daily_micro_usd": 50_000_000, "agent_daily_micro_usd": 50_000_000})
				resp = c.Do(t, "POST", path, map[string]any{"confirmation_token": tok}, "Idempotency-Key", hdr+"-2")
				if resp.Status != 201 {
					t.Fatalf("create after raising the cap: %d %s", resp.Status, resp.Body)
				}
			})
		})
	}
}

// errText joins the text content of an MCP error result.
func errText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestAcceptance_M3_ReferenceMotherRefused: reference tracks (stub recipe, lossy MP3) are never mothers: the
// whole estimate is refused 422 RECIPE_UNKNOWN through REST, MCP and stdio, with no backend call and no row
// of any generation table (M2c).
func TestAcceptance_M3_ReferenceMotherRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	tpl := archivetest.SyntheticArchive(t, "reference")
	r := servertest.New(t, servertest.Options{Archive: &tpl, Backend: "fake", M2b: servertest.M2bOptions{Media: true}})
	refs := refTracks(t, r)
	var flacRef, lossyRef refTrack
	for _, x := range refs {
		switch {
		case x.Lossy && lossyRef.ID == "":
			lossyRef = x
		case !x.Lossy && flacRef.ID == "":
			flacRef = x
		}
	}
	if flacRef.ID == "" || lossyRef.ID == "" {
		t.Fatalf("the reference archive lacks a reference FLAC or a lossy reference: %+v", refs)
	}
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	human := r.Human(t)
	mcpSess := r.MCP(t, auth.ScopeRead, auth.ScopeGenerate)
	stdio := r.Stdio(t, auth.ScopeRead, auth.ScopeGenerate)

	// from_template over five mothers, one of them a reference FLAC
	rows, err := r.Store.R.Query(`SELECT t.id FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id
		WHERE rc.engine <> 'unknown' AND t.lossy = 0 ORDER BY t.id LIMIT 4`)
	if err != nil {
		t.Fatal(err)
	}
	mothers := []string{flacRef.ID}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		mothers = append(mothers, id)
	}
	_ = rows.Close()
	if len(mothers) != 5 {
		t.Fatalf("%d mothers, want 5", len(mothers))
	}
	template := map[string]any{"items": []any{map[string]any{"mode": "from_template", "template_mode": "new_seed", "parent_track_ids": mothers}}}

	type call struct {
		name string
		args map[string]any
		op   string
		id   string
	}
	calls := []call{
		{"flac variant", map[string]any{"id": flacRef.ID, "mode": "new_seed"}, "estimate_variant", flacRef.ID},
		{"lossy variant", map[string]any{"id": lossyRef.ID, "mode": "new_seed"}, "estimate_variant", lossyRef.ID},
		{"template with a reference", template, "estimate_generation", ""},
	}
	for _, c := range calls {
		path, body := "/api/generations/estimate", c.args
		if c.op == "estimate_variant" {
			path, body = "/api/tracks/"+c.id+"/variants/estimate", map[string]any{"mode": "new_seed"}
		}
		for who, cl := range map[string]*servertest.Client{"agent": agent, "human": human} {
			resp := cl.Do(t, "POST", path, body)
			if p := obj(resp.JSON(t)); resp.Status != 422 || p["code"] != "RECIPE_UNKNOWN" {
				t.Errorf("%s %s over REST: %d %s", c.name, who, resp.Status, resp.Body)
			}
		}
		for face, sess := range map[string]*mcp.ClientSession{"mcp": mcpSess, "stdio": stdio} {
			res := rawTool(t, sess, c.op, c.args, false)
			if !res.IsError || !strings.Contains(errText(res), "RECIPE_UNKNOWN") {
				t.Errorf("%s over %s: error=%v %s", c.name, face, res.IsError, errText(res))
			}
		}
	}
	if got := r.Fake.Counters().Total(); got != 0 {
		t.Errorf("backend calls = %d, want 0", got)
	}
	noRows(t, r, "after the refusals")
	if n := count(t, r, `SELECT COUNT(*) FROM estimates`); n != 0 {
		t.Errorf("%d estimate rows", n)
	}
	// the lineage page offers no action: the nodes say so
	for _, x := range []refTrack{flacRef, lossyRef} {
		for _, n := range arr(obj(human.OK(t, "GET", "/api/tracks/"+x.ID+"/lineage", nil))["nodes"]) {
			if nn := obj(n); nn["id"] == x.ID && (nn["recipe_known"] != false || nn["seed"] != nil) {
				t.Errorf("lineage node of %s = %v", x.Title, nn)
			}
		}
	}
}

// TestAcceptance_M3_AgentFlow: an agent with a read+write+generate PAT rates a take 4 stars, reads its context,
// estimates and creates a variant, waits for the job, comments on the child and finds every step in the audit
// log, in order, with the created ids and the create's cost (roadmap M3).
func TestAcceptance_M3_AgentFlow(t *testing.T) {
	r := genRig(t, archivetest.SyntheticArchive(t, "acceptance"), servertest.Options{})
	tok, agentID, err := r.Services.Tokens.Create(context.Background(), "flow-agent", []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate})
	if err != nil {
		t.Fatal(err)
	}
	sess := mcpWithToken(t, r, tok)
	mother := knownMother(t, r)
	step := func() { r.Clock.Advance(time.Second) } // distinct audit timestamps, so the order is observable

	callTool(t, sess, "rate_track", map[string]any{"id": mother, "stars": 4, "verdict": "worth a variant"}, false)
	step()
	callTool(t, sess, "get_track_context", map[string]any{"id": mother}, false)
	step()
	est := obj(callTool(t, sess, "estimate_variant", map[string]any{"id": mother, "mode": "new_seed", "n_seeds": 1}, false))
	step()
	batch := obj(callTool(t, sess, "create_variant", map[string]any{"id": mother, "confirmation_token": est["confirmation_token"], "rationale": "agent flow"}, false))
	job := obj(arr(batch["jobs"])[0])["id"].(string)
	step()

	pump(t, r, func() bool {
		s, _ := jobState(t, r, job)
		return terminalJob(s)
	})
	// wait_for_job answers at once for a finished job (the long poll itself is covered by the jobs suite)
	got := obj(obj(callTool(t, sess, "wait_for_job", map[string]any{"id": job, "timeout_s": 5}, true))["job"])
	if got["state"] != "completed" {
		t.Fatalf("wait_for_job = %v", got)
	}
	child, _ := got["track_id"].(string)
	step()
	comment := obj(callTool(t, sess, "add_comment", map[string]any{"id": child, "body": "sounds right", "kind": "note"}, false))
	step()

	var rows []map[string]any
	page := obj(callTool(t, sess, "get_audit", map[string]any{"principal_id": "agent:" + agentID, "limit": 200}, true))
	for _, it := range arr(page["items"]) {
		rows = append(rows, obj(it))
	}
	// ordered by timestamp: the rig's fixed clock moved one second per step (the in-transaction audit row of a
	// spend op takes its id from another generator, so the id order is not the call order)
	slices.SortStableFunc(rows, func(a, b map[string]any) int { return strings.Compare(a["at"].(string), b["at"].(string)) })
	var ops []string
	byOp := map[string]map[string]any{}
	for _, row := range rows {
		op := row["op"].(string)
		ops = append(ops, op)
		byOp[op] = row
	}
	want := []string{"rate_track", "get_track_context", "estimate_variant", "create_variant", "wait_for_job", "add_comment"}
	if len(ops) > len(want) && ops[len(ops)-1] == "get_audit" {
		ops = ops[:len(ops)-1]
	}
	if d := cmp.Diff(want, ops); d != "" {
		t.Errorf("audit order (-want +got):\n%s", d)
	}
	cv := byOp["create_variant"]
	ids := arr(cv["created_ids"])
	if len(ids) == 0 || ids[0] != batch["id"] {
		t.Errorf("create_variant audit row = %v (batch %v)", cv, batch["id"])
	}
	var cost int64
	if err := r.Store.R.QueryRow(`SELECT cost_micro_usd FROM audit_log WHERE id = ?`, cv["id"]).Scan(&cost); err != nil || cost <= 0 {
		t.Errorf("create_variant audit cost = %d (%v), want the batch ceiling", cost, err)
	}
	if ac := arr(byOp["add_comment"]["created_ids"]); len(ac) != 1 || ac[0] != comment["id"] {
		t.Errorf("add_comment created ids = %v, want %v", ac, comment["id"])
	}
}

// interruptStates are the job states in which a provider job exists and has not finished.
func inFlight(state string) bool {
	switch state {
	case "starting", "generating":
		return true
	}
	return false
}

// forEachBackend runs a case against the fake backend and against the serverless backend over runpodtest.
func forEachBackend(t *testing.T, fn func(t *testing.T, kind string)) {
	for _, kind := range []string{"fake", "runpod"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fn(t, kind)
		})
	}
}

// TestAcceptance_Interrupt_UserTerminate: terminating a running job cancels it with CANCELLED_BY_USER and
// leaves no GPU worker running (D20).
func TestAcceptance_Interrupt_UserTerminate(t *testing.T) {
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	forEachBackend(t, func(t *testing.T, kind string) {
		p := newProvRig(t, kind, tpl, servertest.Options{})
		mother := knownMother(t, p.Rig)
		markers := "fake:slow=300"
		if kind == "runpod" {
			markers = ""
			p.rp.NextJob(runpodtest.Behavior{Hold: true})
		}
		_, job := startVariant(t, p.Rig, mother, markers, "int-term")
		pump(t, p.Rig, func() bool { s, _ := jobState(t, p.Rig, job); return inFlight(s) })
		h := p.Human(t)
		h.OK(t, "POST", "/api/jobs/"+job+"/cancel", map[string]any{"reason": "changed my mind"})
		pump(t, p.Rig, func() bool { s, _ := jobState(t, p.Rig, job); return terminalJob(s) })
		if s, code := jobState(t, p.Rig, job); s != "cancelled" || code != "CANCELLED_BY_USER" {
			t.Errorf("job = %s %s", s, code)
		}
		p.Clock.Advance(10 * time.Minute) // past the idle timeout
		pump(t, p.Rig, func() bool { return true })
		if n := p.workers(t); n != 0 {
			t.Errorf("%d workers still running", n)
		}
		if p.kind == "runpod" && p.rp.Calls(runpodtest.OpCancel) == 0 {
			t.Error("the provider job was never cancelled")
		}
		if n := count(t, p.Rig, `SELECT COUNT(*) FROM tracks WHERE origin = 'job'`); n != 0 {
			t.Errorf("%d takes ingested from a cancelled job", n)
		}
	})
}

// TestAcceptance_Interrupt_WorkerKilled: a worker killed under a running job fails it WORKER_LOST and leaves
// no worker running.
func TestAcceptance_Interrupt_WorkerKilled(t *testing.T) {
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	forEachBackend(t, func(t *testing.T, kind string) {
		p := newProvRig(t, kind, tpl, servertest.Options{})
		mother := knownMother(t, p.Rig)
		markers := "fake:fail=worker_lost"
		if kind == "runpod" {
			markers = ""
			p.rp.NextJob(runpodtest.Behavior{WorkerDeath: true})
		}
		_, job := startVariant(t, p.Rig, mother, markers, "int-kill")
		pump(t, p.Rig, func() bool { s, _ := jobState(t, p.Rig, job); return terminalJob(s) })
		if s, code := jobState(t, p.Rig, job); s != "failed" || code != "WORKER_LOST" {
			t.Errorf("job = %s %s", s, code)
		}
		p.Clock.Advance(10 * time.Minute)
		pump(t, p.Rig, func() bool { return true })
		if n := p.workers(t); n != 0 {
			t.Errorf("%d workers still running", n)
		}
	})
}

// TestAcceptance_Interrupt_ServerRestart: a server restart while a job runs loses nothing: the new process
// adopts the job through its persisted provider reference, completes it, and the provider saw one submit.
func TestAcceptance_Interrupt_ServerRestart(t *testing.T) {
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	forEachBackend(t, func(t *testing.T, kind string) {
		p := newProvRig(t, kind, tpl, servertest.Options{})
		mother := knownMother(t, p.Rig)
		markers := "fake:slow=60"
		if kind == "runpod" {
			markers = ""
		}
		_, job := startVariant(t, p.Rig, mother, markers, "int-restart")
		pump(t, p.Rig, func() bool { s, _ := jobState(t, p.Rig, job); return inFlight(s) })
		if n := p.submits(); n != 1 {
			t.Fatalf("%d submits before the restart", n)
		}
		// the first process dies (its runner never steps again); its lease lapses
		p.Clock.Advance(5 * time.Minute)
		second := p.NewProcess(t, 2)
		pumpWith(t, p.Clock, second.Runner, second.Reconciler, func() bool { s, _ := jobState(t, p.Rig, job); return terminalJob(s) })
		if s, code := jobState(t, p.Rig, job); s != "completed" {
			t.Fatalf("job = %s %s", s, code)
		}
		if n := p.submits(); n != 1 {
			t.Errorf("%d submits in all, want exactly one", n)
		}
		if n := count(t, p.Rig, `SELECT COUNT(*) FROM tracks WHERE origin = 'job'`); n != 1 {
			t.Errorf("%d takes ingested, want 1", n)
		}
	})
}
