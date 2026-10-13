package api_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func genRig(t *testing.T) *servertest.Rig {
	t.Helper()
	return servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, Backend: "fake"})
}

func m(v any) map[string]any { x, _ := v.(map[string]any); return x }
func arr(v any) []any        { x, _ := v.([]any); return x }

func TestGenerationOps_EstimateCreateApproveRoundTrip(t *testing.T) {
	t.Parallel()
	r := genRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	human := r.Human(t)

	est := m(agent.OK(t, "POST", "/api/generations/estimate", map[string]any{
		"items": []any{map[string]any{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 6}}}))
	cost := m(est["cost"])
	tok, _ := est["confirmation_token"].(string)
	if !strings.HasPrefix(tok, "mmc_") || len(tok) != 56 || est["takes"] != float64(6) || est["requires_human_approval"] != true || est["backend"] != "fake" {
		t.Fatalf("estimate = %v", est)
	}
	if got := cost["ceiling_display"].(string); !strings.HasPrefix(got, "$") || cost["ceiling_micro_usd"].(float64) < cost["max_micro_usd"].(float64) {
		t.Errorf("cost = %v", cost)
	}
	for _, k := range []string{"expected", "max", "retry_allowance", "ceiling", "attempt_overhead"} {
		if cost[k+"_micro_usd"] == nil || cost[k+"_usd"] == nil {
			t.Errorf("cost block lacks %s: %v", k, cost)
		}
	}
	items := arr(est["items"])
	takes := arr(m(items[0])["takes"])
	if len(items) != 1 || len(takes) != 6 || m(takes[0])["operator"] != "reroll_seed" || m(takes[0])["seed"] != float64(102) {
		t.Fatalf("items = %v", items)
	}
	if m(est["provider"])["generation_enabled"] != true || m(est["provider"])["armed"] != true || m(est["budget"])["fits"] != true {
		t.Errorf("provider/budget = %v / %v", est["provider"], est["budget"])
	}

	// create needs an Idempotency-Key
	body := map[string]any{"confirmation_token": tok, "rationale": "six rerolls"}
	if resp := agent.Do(t, "POST", "/api/generations", body); resp.Status != 428 {
		t.Errorf("missing key: %d %s", resp.Status, resp.Body)
	}
	resp := agent.Do(t, "POST", "/api/generations", body, "Idempotency-Key", "k-create-1")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	batch := m(resp.JSON(t))
	id := batch["id"].(string)
	if batch["state"] != "awaiting_confirmation" || batch["via"] != "api" || batch["rationale"] != "six rerolls" || batch["takes"] != float64(6) ||
		m(batch["approval"])["state"] != "pending" || len(arr(batch["jobs"])) != 6 || m(batch["counts"])["awaiting"] != float64(6) {
		t.Fatalf("batch = %v", batch)
	}
	if cb := m(batch["created_by"]); cb["kind"] != "agent" || cb["name"] != "agent-1" {
		t.Errorf("created_by = %v", cb)
	}
	// the retry with the same key replays the stored bytes
	again := agent.Do(t, "POST", "/api/generations", body, "Idempotency-Key", "k-create-1")
	if again.Status != 201 || string(again.Body) != string(resp.Body) || again.Header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay: %d replayed=%q identical=%v", again.Status, again.Header.Get("Idempotent-Replayed"), string(again.Body) == string(resp.Body))
	}
	// a fresh key with the consumed token: 409 naming the batch
	dup := agent.Do(t, "POST", "/api/generations", body, "Idempotency-Key", "k-create-2")
	pb := m(dup.JSON(t))
	if dup.Status != 409 || pb["code"] != "CONFIRMATION_INVALID" || m(pb["details"])["batch_id"] != id {
		t.Errorf("consumed token: %d %v", dup.Status, pb)
	}

	// the agent asks for approval: one push; the status shows pending
	ra := m(agent.OK(t, "POST", "/api/batches/"+id+"/approval-request", nil))
	if ra["state"] != "pending" || ra["notified"] != true {
		t.Errorf("request_approval = %v", ra)
	}
	st := m(agent.OK(t, "GET", "/api/batches/"+id+"/approval", nil))
	if st["state"] != "pending" || arr(st["reasons"])[0] != "takes_over_limit" || st["via"] != nil || st["decided_by"] != nil {
		t.Errorf("approval status = %v", st)
	}
	// an agent cannot approve; the operator can
	if r := agent.Do(t, "POST", "/human/api/batches/"+id+"/approve", map[string]any{}); r.Status != 403 {
		t.Errorf("agent approve: %d", r.Status)
	}
	ap := m(human.OK(t, "POST", "/human/api/batches/"+id+"/approve", map[string]any{"reason": "ok"}))
	if ap["state"] != "active" || m(ap["approval"])["state"] != "approved" || m(ap["approval"])["via"] != "web" || m(m(ap["approval"])["decided_by"])["kind"] != "human" {
		t.Errorf("approved = %v", ap)
	}
	// the agent sees the operator as a plain human reference
	st = m(agent.OK(t, "GET", "/api/batches/"+id+"/approval", nil))
	if m(st["decided_by"])["id"] != "human" {
		t.Errorf("decided_by seen by the agent = %v", st["decided_by"])
	}
	// the audit log holds the spend op, written in the create transaction: stored, with the batch among its created ids
	rows := arr(m(human.OK(t, "GET", "/api/audit?op=create_generation&status_min=200", nil))["items"])
	stored := 0
	for _, row := range rows {
		x := m(row)
		if x["status"] == float64(201) && x["idempotency"] == "stored" && len(arr(x["created_ids"])) == 7 && arr(x["created_ids"])[0] == id {
			stored++
		}
	}
	if stored != 1 {
		t.Errorf("%d stored create_generation audit rows for %s (the gate must not add a second one) among %v", stored, id, rows)
	}
}

func TestGenerationOps_HumanCreateIsApproval(t *testing.T) {
	t.Parallel()
	r := genRig(t)
	human := r.Human(t)
	est := m(human.OK(t, "POST", "/api/tracks/"+lt.TrackLuna+"/variants/estimate", map[string]any{"mode": "new_seed", "n_seeds": 3}))
	if est["requires_human_approval"] != false {
		t.Errorf("a human estimate previews the SPA path: %v", est["requires_human_approval"])
	}
	tok := est["confirmation_token"].(string)
	resp := human.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": tok}, "Idempotency-Key", "h-1")
	if resp.Status != 201 {
		t.Fatalf("create_generation_human: %d %s", resp.Status, resp.Body)
	}
	b := m(resp.JSON(t))
	if b["state"] != "active" || b["via"] != "human_create" || m(b["approval"])["via"] != "human_create" || m(b["counts"])["queued"] != float64(3) {
		t.Errorf("batch = %v", b)
	}
	// create_variant confirms a variant estimate of the path's track only
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	e2 := m(agent.OK(t, "POST", "/api/tracks/"+lt.TrackLuna+"/variants/estimate", map[string]any{"mode": "style_edit", "patch": map[string]any{"style": "rock, 140 bpm"}}))
	wrong := agent.Do(t, "POST", "/api/tracks/"+lt.TrackCitta+"/variants", map[string]any{"confirmation_token": e2["confirmation_token"]}, "Idempotency-Key", "v-1")
	if wrong.Status != 409 || m(wrong.JSON(t))["code"] != "CONFIRMATION_INVALID" {
		t.Errorf("wrong track: %d %s", wrong.Status, wrong.Body)
	}
	ok := agent.Do(t, "POST", "/api/tracks/"+lt.TrackLuna+"/variants", map[string]any{"confirmation_token": e2["confirmation_token"]}, "Idempotency-Key", "v-2")
	if ok.Status != 201 || m(ok.JSON(t))["takes"] != float64(1) {
		t.Errorf("create_variant: %d %s", ok.Status, ok.Body)
	}
}

func TestGenerationOps_ProblemShapes(t *testing.T) {
	t.Parallel()
	r := genRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	problem := func(method, path string, body any, status int, code string) map[string]any {
		t.Helper()
		resp := agent.Do(t, method, path, body)
		p := m(resp.JSON(t))
		if resp.Status != status || p["code"] != code {
			t.Fatalf("%s %s: %d %v, want %d %s", method, path, resp.Status, p, status, code)
		}
		return m(p["details"])
	}
	d := problem("POST", "/api/tracks/"+lt.TrackRefFlac+"/variants/estimate", map[string]any{"mode": "new_seed"}, 422, "RECIPE_UNKNOWN")
	if d["track_id"] != lt.TrackRefFlac {
		t.Errorf("details = %v", d)
	}
	problem("POST", "/api/tracks/"+lt.TrackLossy+"/variants/estimate", map[string]any{"mode": "new_seed"}, 422, "RECIPE_UNKNOWN")
	problem("POST", "/api/tracks/no-such/variants/estimate", map[string]any{"mode": "new_seed"}, 422, "UNKNOWN_TRACK")
	d = problem("POST", "/api/generations/estimate", map[string]any{"items": []any{map[string]any{"mode": "lyric_edit", "parent_track_id": lt.TrackLuna}}}, 422, "INVALID_ITEM")
	if d["field"] != "patch" || d["rule"] != "patch_required" || d["item"] != float64(0) {
		t.Errorf("details = %v", d)
	}
	// abc: 5 is a schema error (the nullable string, not Huma's empty schema)
	resp := agent.Do(t, "POST", "/api/tracks/"+lt.TrackLuna+"/variants/estimate", map[string]any{"mode": "lyric_edit", "patch": map[string]any{"lyrics": "[Verse 1]\nnew", "abc": 5}})
	if resp.Status != 422 {
		t.Errorf("abc: 5 => %d", resp.Status)
	}
	// abc: null and cfg_scale: null pass through to the planner
	e := m(agent.OK(t, "POST", "/api/tracks/"+lt.TrackLuna+"/variants/estimate", map[string]any{"mode": "style_edit", "patch": map[string]any{"style": "jazz", "abc": nil, "cfg_scale": nil}}))
	if e["takes"] != float64(1) {
		t.Errorf("estimate = %v", e)
	}
	problem("POST", "/api/batches/"+lt.BatchAwaiting+"/approval-request", nil, 403, "NOT_BATCH_OWNER")
	problem("GET", "/api/batches/no-such/approval", nil, 404, "NOT_FOUND")
	d = problem("POST", "/api/generations/estimate", map[string]any{"items": []any{map[string]any{"mode": "compose_new", "recipe": map[string]any{
		"id": "x-1", "style": "pop", "lyrics": "[Verse 1]\nline", "cot": "none"}}}}, 422, "LINT_FAILED")
	if b, _ := json.Marshal(d); !strings.Contains(string(b), "bad_cot") {
		t.Errorf("details = %s", b)
	}
	// an unknown token
	resp = agent.Do(t, "POST", "/api/generations", map[string]any{"confirmation_token": "mmc_" + strings.Repeat("a", 52)}, "Idempotency-Key", "x-1")
	if resp.Status != 409 || m(resp.JSON(t))["code"] != "CONFIRMATION_INVALID" {
		t.Errorf("unknown token: %d %s", resp.Status, resp.Body)
	}
}

func TestGenerationOps_BudgetAndLedger(t *testing.T) {
	t.Parallel()
	r := genRig(t)
	agent, agentID := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	human := r.Human(t)
	b := m(agent.OK(t, "GET", "/api/budget", nil))
	if m(b["month"])["cap_micro_usd"] != float64(10_000_000) || m(b["day"])["cap_micro_usd"] != float64(1_000_000) || m(b["balance"])["source"] != "unknown" ||
		m(b["auto_approve"])["takes"] != float64(5) || m(b["provider"])["generation_enabled"] != true {
		t.Errorf("budget = %v", b)
	}
	if toks := arr(m(b["caps"])["tokens"]); len(toks) != 1 || m(toks[0])["token_id"] != agentID {
		t.Errorf("an agent sees its own token only: %v", toks)
	}
	// the operator sets caps: one history row, an agent cannot
	if resp := agent.Do(t, "PUT", "/human/api/spend/caps", map[string]any{"monthly_micro_usd": 5_000_000}); resp.Status != 403 {
		t.Errorf("agent set caps: %d", resp.Status)
	}
	caps := m(human.OK(t, "PUT", "/human/api/spend/caps", map[string]any{"monthly_micro_usd": 5_000_000, "tokens": []any{
		map[string]any{"token_id": agentID, "daily_budget_micro_usd": 400_000, "auto_approve_takes": nil, "auto_approve_micro_usd": nil}}}))
	if caps["monthly_micro_usd"] != float64(5_000_000) || caps["monthly_usd"] != "5" {
		t.Errorf("caps = %v", caps)
	}
	b = m(agent.OK(t, "GET", "/api/budget", nil))
	if m(b["day"])["cap_micro_usd"] != float64(400_000) || m(b["month"])["cap_micro_usd"] != float64(5_000_000) {
		t.Errorf("budget after caps = %v", b)
	}
	if resp := human.Do(t, "PUT", "/human/api/spend/caps", map[string]any{"tokens": []any{map[string]any{"token_id": "ghost", "daily_budget_micro_usd": nil, "auto_approve_takes": nil, "auto_approve_micro_usd": nil}}}); resp.Status != 422 || m(resp.JSON(t))["code"] != "UNKNOWN_TOKEN" {
		t.Errorf("unknown token: %d %s", resp.Status, resp.Body)
	}
	if resp := human.Do(t, "PUT", "/human/api/spend/caps", map[string]any{}); resp.Status != 422 {
		t.Errorf("empty caps: %d", resp.Status)
	}
	if resp := human.Do(t, "PUT", "/human/api/spend/caps", map[string]any{"monthly_micro_usd": 1_000_000_001}); resp.Status != 422 {
		t.Errorf("over $1000: %d", resp.Status)
	}
	sp := m(agent.OK(t, "GET", "/api/spend?month=2026-10", nil))
	if sp["month"] != "2026-10" || m(sp["totals"])["settled_micro_usd"] == nil || len(arr(sp["days"])) == 0 || len(arr(sp["batches"])) == 0 {
		t.Errorf("spend = %v", sp)
	}
	led := m(agent.OK(t, "GET", "/api/ledger?limit=2&kind=reserve", nil))
	if len(arr(led["items"])) != 2 || led["next_cursor"] == nil {
		t.Fatalf("ledger = %v", led)
	}
	e := m(arr(led["items"])[0])
	if e["kind"] != "reserve" || e["usd"] == nil || m(e["principal"])["kind"] == nil {
		t.Errorf("entry = %v", e)
	}
	next := m(agent.OK(t, "GET", "/api/ledger?limit=2&kind=reserve&cursor="+led["next_cursor"].(string), nil))
	if len(arr(next["items"])) == 0 || m(arr(next["items"])[0])["id"].(float64) >= e["id"].(float64) {
		t.Errorf("next page = %v", next)
	}
	if resp := agent.Do(t, "GET", "/api/ledger?since=yesterday", nil); resp.Status != 422 {
		t.Errorf("bad since: %d", resp.Status)
	}
}

func TestGenerationOps_Lineage(t *testing.T) {
	t.Parallel()
	r := genRig(t)
	agent, _ := r.Agent(t)
	l := m(agent.OK(t, "GET", "/api/tracks/"+lt.TrackJobChild+"/lineage", nil))
	nodes := arr(l["nodes"])
	if l["root_id"] != lt.TrackLuna || l["total"] != float64(2) || l["truncated"] != false || len(nodes) != 2 {
		t.Fatalf("lineage = %v", l)
	}
	child := m(nodes[1])
	if child["operator"] != "reroll_seed" || child["parent_id"] != lt.TrackLuna || child["generation"] != float64(1) || child["recipe_known"] != true || child["origin"] != "job" ||
		child["job_id"] != lt.JobDone || m(child["cost"])["basis"] != "reconciled" || m(child["cost"])["display_micro_usd"] != float64(30216) {
		t.Errorf("child = %v", child)
	}
	if root := m(nodes[0]); root["operator"] != nil || root["parent_id"] != nil || root["cost"] != nil || root["children_count"] != float64(1) {
		t.Errorf("root = %v", root)
	}
	// reference nodes: no seed, recipe_known=false
	ref := m(agent.OK(t, "GET", "/api/tracks/"+lt.TrackRefFlac+"/lineage", nil))
	rn := m(arr(ref["nodes"])[0])
	if rn["seed"] != nil || rn["recipe_known"] != false || !strings.HasPrefix(rn["request_id"].(string), "ref_") {
		t.Errorf("reference node = %v", rn)
	}
	if lim := m(agent.OK(t, "GET", "/api/tracks/"+lt.TrackLuna+"/lineage?limit=1", nil)); lim["truncated"] != true || len(arr(lim["nodes"])) != 1 || lim["total"] != float64(2) {
		t.Errorf("limit = %v", lim)
	}
	if resp := agent.Do(t, "GET", "/api/tracks/no-such/lineage", nil); resp.Status != 404 {
		t.Errorf("unknown track: %d", resp.Status)
	}
}
