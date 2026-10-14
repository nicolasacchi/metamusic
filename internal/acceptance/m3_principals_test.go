package acceptance_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// m3Rig is the light rig of the principal tests: the fixture library with the M3 rows and the fake backend,
// runner idle.
func m3Rig(t *testing.T) *servertest.Rig {
	t.Helper()
	return servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, Backend: "fake"})
}

var m3HumanOps = []struct{ op, method, path string }{
	{"create_generation_human", "POST", "/human/api/generations"},
	{"approve_batch", "POST", "/human/api/batches/" + lt.BatchAwaiting + "/approve"},
	{"reject_batch", "POST", "/human/api/batches/" + lt.BatchAwaiting + "/reject"},
	{"set_spend_caps", "PUT", "/human/api/spend/caps"},
	{"update_provider_state", "POST", "/human/api/provider/state"},
}

// TestHumanOnly_M3Ops: the five human-only M3 operations answer 404 off /human/api/, refuse agents (403
// HUMAN_ONLY), devices and the MCP faces, and are not tools.
func TestHumanOnly_M3Ops(t *testing.T) {
	t.Parallel()
	r := m3Rig(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin)
	dev := r.Device(t)
	human := r.Human(t)
	body := map[string]any{"confirmation_token": "mmc_" + strings.Repeat("a", 52), "reason": "x", "monthly_micro_usd": 1_000_000, "armed": false}
	for _, c := range m3HumanOps {
		resp := agent.Do(t, c.method, c.path, body, "Idempotency-Key", "ho-"+c.op)
		if p := obj(resp.JSON(t)); resp.Status != 403 || p["code"] != "HUMAN_ONLY" {
			t.Errorf("%s as an agent: %d %s", c.op, resp.Status, resp.Body)
		}
		if resp := dev.Do(t, c.method, c.path, body, "Idempotency-Key", "ho-dev-"+c.op); resp.Status != 401 && resp.Status != 403 {
			t.Errorf("%s as a device: %d %s", c.op, resp.Status, resp.Body)
		}
		off := strings.Replace(c.path, "/human/api/", "/api/", 1)
		if c.op == "create_generation_human" {
			continue // /api/generations is the agent create_generation
		}
		if resp := human.Do(t, c.method, off, body, "Idempotency-Key", "ho-off-"+c.op); resp.Status != 404 && resp.Status != 405 {
			t.Errorf("%s off the human prefix (%s): %d %s", c.op, off, resp.Status, resp.Body)
		}
	}
	faces := map[string]*mcp.ClientSession{
		"mcp":   r.MCP(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin),
		"stdio": r.Stdio(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin),
	}
	for face, sess := range faces {
		tools, err := sess.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools.Tools {
			for _, c := range m3HumanOps {
				if tl.Name == c.op {
					t.Errorf("%s tools/list exposes %s", face, c.op)
				}
			}
		}
		for _, c := range m3HumanOps {
			res := rawTool(t, sess, c.op, map[string]any{"id": lt.BatchAwaiting, "reason": "x", "confirmation_token": "mmc_" + strings.Repeat("a", 52)}, true)
			if !res.IsError {
				t.Errorf("%s call_operation %s not refused", face, c.op)
			}
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM approvals WHERE batch_id = ? AND state <> 'pending'`, lt.BatchAwaiting); n != 0 {
		t.Error("the awaiting batch was decided by a non-human principal")
	}
}

// awaitingBatchOverStdio creates an agent batch over the stdio face (more takes than auto-approve allows).
func awaitingBatchOverStdio(t *testing.T, sess *mcp.ClientSession) map[string]any {
	t.Helper()
	est := obj(callTool(t, sess, "estimate_generation", map[string]any{"items": []map[string]any{{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 6}}}, false))
	return obj(callTool(t, sess, "create_generation", map[string]any{"confirmation_token": est["confirmation_token"]}, false))
}

// TestStdio_ApproveRefused: a stdio principal creates a batch that needs approval but can never give it.
func TestStdio_ApproveRefused(t *testing.T) {
	t.Parallel()
	r := m3Rig(t)
	sess := r.Stdio(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin)
	b := awaitingBatchOverStdio(t, sess)
	if b["state"] != "awaiting_confirmation" {
		t.Fatalf("batch = %v", b)
	}
	id := b["id"].(string)
	for _, op := range []string{"approve_batch", "reject_batch", "create_generation_human"} {
		res := rawTool(t, sess, op, map[string]any{"id": id, "reason": "mine", "confirmation_token": "mmc_" + strings.Repeat("b", 52)}, true)
		if !res.IsError {
			t.Errorf("%s over stdio was not refused", op)
		}
	}
	if got := count(t, r, `SELECT COUNT(*) FROM approvals WHERE batch_id = ? AND state = 'pending'`, id); got != 1 {
		t.Error("the batch is no longer pending")
	}
	if got := r.Fake.Counters().Submit; got != 0 {
		t.Errorf("%d submits for an unapproved batch", got)
	}
}

// TestStdio_DailyBudgetEnforced: the daily budget the operator set on a token binds the stdio face.
func TestStdio_DailyBudgetEnforced(t *testing.T) {
	t.Parallel()
	r := m3Rig(t)
	sess := r.Stdio(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)
	var tokenID string
	if err := r.Store.R.QueryRow(`SELECT id FROM api_tokens ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}
	r.Human(t).OK(t, "PUT", "/human/api/spend/caps", map[string]any{"tokens": []any{
		map[string]any{"token_id": tokenID, "daily_budget_micro_usd": 1_000, "auto_approve_takes": nil, "auto_approve_micro_usd": nil}}})
	est := obj(callTool(t, sess, "estimate_variant", map[string]any{"id": lt.TrackLuna, "mode": "new_seed", "n_seeds": 1}, false))
	before, batches := r.Fake.Counters().Total(), count(t, r, `SELECT COUNT(*) FROM batches`)
	res := rawTool(t, sess, "create_variant", map[string]any{"id": lt.TrackLuna, "confirmation_token": est["confirmation_token"]}, false)
	if !res.IsError || !strings.Contains(errText(res), "BUDGET_EXCEEDED") {
		t.Fatalf("create over the daily budget: error=%v %s", res.IsError, errText(res))
	}
	if r.Fake.Counters().Total() != before {
		t.Error("the refused create reached the backend")
	}
	if n := count(t, r, `SELECT COUNT(*) FROM batches`) - batches; n != 0 {
		t.Errorf("%d batches written for the refused create", n)
	}
}

// TestDevice_CannotGenerateOrApprove: a paired phone cannot start spending nor approve it; it may terminate.
func TestDevice_CannotGenerateOrApprove(t *testing.T) {
	t.Parallel()
	r := m3Rig(t)
	dev := r.Device(t)
	batches := count(t, r, `SELECT COUNT(*) FROM batches`)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/generations/estimate"},
		{"POST", "/api/tracks/" + lt.TrackLuna + "/variants/estimate"},
		{"POST", "/api/generations"},
		{"POST", "/api/tracks/" + lt.TrackLuna + "/variants"},
		{"POST", "/human/api/generations"},
		{"POST", "/human/api/batches/" + lt.BatchAwaiting + "/approve"},
		{"POST", "/human/api/batches/" + lt.BatchAwaiting + "/reject"},
		{"PUT", "/human/api/spend/caps"},
	} {
		resp := dev.Do(t, c.method, c.path, map[string]any{"mode": "new_seed", "confirmation_token": "mmc_" + strings.Repeat("a", 52), "items": []any{}, "monthly_micro_usd": 1}, "Idempotency-Key", "dev-"+c.path)
		if resp.Status != 401 && resp.Status != 403 {
			t.Errorf("device %s %s: %d %s", c.method, c.path, resp.Status, resp.Body)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM batches`); n != batches {
		t.Errorf("%d batches after the device's attempts", n)
	}
}

// TestMCP_CreateVariantReplay: a retried create_variant over MCP replays the stored 201 instead of failing on
// the consumed token: one batch, one job, no CONFIRMATION_INVALID.
func TestMCP_CreateVariantReplay(t *testing.T) {
	t.Parallel()
	r := m3Rig(t)
	sess := r.MCP(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)
	est := obj(callTool(t, sess, "estimate_variant", map[string]any{"id": lt.TrackLuna, "mode": "new_seed", "n_seeds": 1}, false))
	args := map[string]any{"id": lt.TrackLuna, "confirmation_token": est["confirmation_token"], "rationale": "replay"}
	before := count(t, r, `SELECT COUNT(*) FROM batches`)
	jobsBefore := count(t, r, `SELECT COUNT(*) FROM jobs`)
	first := rawTool(t, sess, "create_variant", args, false)
	second := rawTool(t, sess, "create_variant", args, false)
	if first.IsError || second.IsError {
		t.Fatalf("create_variant: first %v %s; second %v %s", first.IsError, errText(first), second.IsError, errText(second))
	}
	a, b := obj(decodeStructured(t, first)), obj(decodeStructured(t, second))
	if a["id"] == nil || a["id"] != b["id"] {
		t.Errorf("the retry made another batch: %v vs %v", a["id"], b["id"])
	}
	if n := count(t, r, `SELECT COUNT(*) FROM batches`) - before; n != 1 {
		t.Errorf("%d batches created", n)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM jobs`) - jobsBefore; n != 1 {
		t.Errorf("%d jobs created", n)
	}
}

// TestSpend_ConcurrentCreatesNeverExceedCap: sixteen creates race against a monthly cap that fits five; the
// cap is enforced inside the create transaction, so exactly five batches are made and eleven callers get 402.
func TestSpend_ConcurrentCreatesNeverExceedCap(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("heavy: sixteen racing creates")
	}
	// no M3 fixture rows: the month starts with an empty ledger
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, Backend: "fake"})
	human := r.Human(t)
	const callers = 16
	tokens := make([]string, callers)
	agents := make([]*servertest.Client, callers) // one principal per caller: an estimate replaces its principal's earlier one
	var maxMicro, overhead float64
	for i := range tokens {
		agents[i], _ = r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
		// distinct seeds: every caller renders its own recipe, as sixteen real users would
		items := []any{map[string]any{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "seeds": []int64{int64(7000 + i)}}}
		est := obj(agents[i].OK(t, "POST", "/api/generations/estimate", map[string]any{"items": items}))
		tokens[i] = est["confirmation_token"].(string)
		cost := obj(est["cost"])
		maxMicro, overhead = cost["max_micro_usd"].(float64), cost["attempt_overhead_micro_usd"].(float64)
	}
	take := maxMicro - overhead // the create reserves the take; the cap check adds the whole max on top of what is outstanding
	capMicro := int64(5*take + overhead + take/2)
	human.OK(t, "PUT", "/human/api/spend/caps", map[string]any{"monthly_micro_usd": capMicro, "batch_micro_usd": 50_000_000,
		"human_daily_micro_usd": 50_000_000, "agent_daily_micro_usd": 50_000_000, "auto_approve_takes": 100, "auto_approve_micro_usd": 50_000_000})
	before := count(t, r, `SELECT COUNT(*) FROM batches`)

	statuses := make([]int, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp := agents[i].Do(t, "POST", "/api/generations", map[string]any{"confirmation_token": tokens[i]}, "Idempotency-Key", fmt.Sprintf("conc-%d", i))
			statuses[i] = resp.Status
		}()
	}
	close(start)
	wg.Wait()
	ok, refused := 0, 0
	for _, s := range statuses {
		switch s {
		case 201:
			ok++
		case 402:
			refused++
		default:
			t.Errorf("unexpected status %d", s)
		}
	}
	if ok != 5 || refused != callers-5 {
		t.Errorf("%d created, %d refused; want 5 and %d", ok, refused, callers-5)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM batches`) - before; n != 5 {
		t.Errorf("%d batches in the database, want 5", n)
	}
}
