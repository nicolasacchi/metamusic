package acceptance_test

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// moneyKey reports whether a JSON key names an amount (the Listener sweep's rule, C314.5).
func moneyKey(k string) bool {
	return strings.HasSuffix(k, "_micro_usd") || strings.HasSuffix(k, "_usd") || strings.HasPrefix(k, "display_")
}

// moneyKeysIn lists the paths of money keys anywhere in a decoded JSON value.
func moneyKeysIn(v any, path string, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if moneyKey(k) {
				*out = append(*out, path+"/"+k)
			}
			moneyKeysIn(c, path+"/"+k, out)
		}
	case []any:
		for _, c := range x {
			moneyKeysIn(c, path+"[]", out)
		}
	}
}

// TestEditorGeneration_E1 drives the whole Editor generation path over REST (D-U1-11, C310, C305): the request lands
// awaiting the Owner whatever the path, the Editor can neither approve nor decide, sees no cap or price, and sees the cost of
// its own batch only; the Owner approves it.
func TestEditorGeneration_E1(t *testing.T) {
	t.Parallel()
	r := m3Rig(t)
	bob := r.User(t, servertest.UserEditor, config.RoleEditor)
	owner := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)

	// the estimate: own cost in full, no price, pools, month or day pot, cap, armed or tripped
	body := map[string]any{"items": []map[string]any{{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 1}}}
	est := obj(bob.Do(t, "POST", "/api/generations/estimate", body).JSON(t))
	if est["requires_human_approval"] != true || !strings.Contains(strings.Join(anyStrings(est["approval_reasons"]), ","), "editor_batch") {
		t.Fatalf("estimate approval = %v %v", est["requires_human_approval"], est["approval_reasons"])
	}
	if _, ok := est["price"]; ok {
		t.Error("an Editor's estimate carries the price")
	}
	b := obj(est["budget"])
	for _, k := range []string{"month", "day", "batch_cap_micro_usd", "batch_cap_usd"} {
		if _, ok := b[k]; ok {
			t.Errorf("an Editor's estimate budget carries %s", k)
		}
	}
	for _, k := range []string{"armed", "tripped"} {
		if _, ok := obj(est["provider"])[k]; ok {
			t.Errorf("an Editor's estimate provider carries %s", k)
		}
	}
	if obj(est["cost"])["ceiling_micro_usd"] == nil || obj(est["cost"])["max_micro_usd"] == nil {
		t.Errorf("an Editor sees its own estimate in full: %v", est["cost"])
	}

	// the create: awaiting the Owner, via api, even asking for human_create
	resp := bob.Do(t, "POST", "/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "bob asks"}, "Idempotency-Key", "e1-bob-1")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	batch := obj(resp.JSON(t))
	id := batch["id"].(string)
	if batch["state"] != "awaiting_confirmation" || batch["via"] != "api" || obj(batch["approval"])["state"] != "pending" {
		t.Fatalf("batch = %v", batch)
	}
	if obj(batch["cost"])["ceiling_micro_usd"] == nil || obj(batch["created_by"])["id"] == nil && obj(batch["created_by"])["name"] == nil {
		t.Errorf("an Editor sees its own batch with cost and creator: %v %v", batch["cost"], batch["created_by"])
	}
	if got := count(t, r, `SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state <> 'awaiting_confirmation'`, id); got != 0 {
		t.Error("an Editor's job started before the Owner approved")
	}
	if r.Fake.Counters().Submit != 0 {
		t.Error("a submit happened for an unapproved Editor batch")
	}

	// no decision for an Editor, on any face
	for _, c := range []struct{ method, path string }{
		{"POST", "/human/api/batches/" + id + "/approve"}, {"POST", "/human/api/batches/" + id + "/reject"},
		{"POST", "/human/api/generations"}, {"PUT", "/human/api/spend/caps"}, {"POST", "/human/api/provider/state"},
	} {
		if resp := bob.Do(t, c.method, c.path, map[string]any{"reason": "mine", "armed": true, "monthly_micro_usd": 1, "confirmation_token": est["confirmation_token"]}, "Idempotency-Key", "e1-"+c.path); resp.Status != 403 {
			t.Errorf("editor %s %s: %d %s", c.method, c.path, resp.Status, resp.Body)
		}
	}
	if got := count(t, r, `SELECT COUNT(*) FROM approvals WHERE batch_id = ? AND state = 'pending'`, id); got != 1 {
		t.Error("the Editor batch is no longer pending")
	}

	// reads: own with cost; another's without cost or creator; no spend, ledger, provider or caps
	got := obj(bob.Do(t, "GET", "/api/batches/"+id, nil).JSON(t))
	if got["cost"] == nil {
		t.Errorf("own batch has no cost: %v", got)
	}
	other := obj(bob.Do(t, "GET", "/api/batches/"+lt.BatchAwaiting, nil).JSON(t))
	var leaks []string
	moneyKeysIn(other, "", &leaks)
	if _, ok := other["cost"]; ok || len(leaks) > 0 {
		t.Errorf("another principal's batch carries money: %v %v", other["cost"], leaks)
	}
	if _, ok := other["created_by"]; ok {
		t.Error("another principal's batch names its creator to an Editor")
	}
	list := obj(bob.Do(t, "GET", "/api/batches", nil).JSON(t))
	leaks = nil
	for _, it := range list["items"].([]any) {
		m := obj(it)
		if m["id"] == id {
			if m["cost"] == nil {
				t.Error("own batch in the list has no cost")
			}
			continue
		}
		moneyKeysIn(m, "", &leaks)
	}
	if len(leaks) > 0 {
		t.Errorf("batch list leaks money of others: %v", leaks)
	}
	jobs := obj(bob.Do(t, "GET", "/api/jobs", nil).JSON(t))
	leaks = nil
	for _, it := range jobs["items"].([]any) {
		m := obj(it)
		if m["batch_id"] == id {
			if m["cost"] == nil {
				t.Error("own job in the list has no cost")
			}
			continue
		}
		moneyKeysIn(m, "", &leaks)
	}
	if len(leaks) > 0 {
		t.Errorf("job list leaks money of others: %v", leaks)
	}
	for _, path := range []string{"/api/spend", "/api/ledger", "/api/provider", "/api/audit"} {
		if resp := bob.Do(t, "GET", path, nil); resp.Status != 403 {
			t.Errorf("editor GET %s: %d", path, resp.Status)
		}
	}
	bud := obj(bob.Do(t, "GET", "/api/budget", nil).JSON(t))
	leaks = nil
	moneyKeysIn(bud, "", &leaks)
	if len(leaks) > 0 || bud["approval_required"] != true || obj(bud["provider"])["generation_enabled"] == nil {
		t.Errorf("editor budget = %v (money keys %v)", bud, leaks)
	}
	for _, k := range []string{"caps", "month", "day", "auto_approve", "balance"} {
		if _, ok := bud[k]; ok {
			t.Errorf("editor budget carries %s", k)
		}
	}
	ownerBudget := obj(owner.Do(t, "GET", "/api/budget", nil).JSON(t))
	for _, k := range []string{"caps", "month", "day", "auto_approve", "balance", "provider"} {
		if _, ok := ownerBudget[k]; !ok {
			t.Errorf("the Owner's budget lost %s", k)
		}
	}
	if _, ok := ownerBudget["approval_required"]; ok {
		t.Error("the Owner's budget gained approval_required")
	}

	// the approval status: own only
	if resp := bob.Do(t, "GET", "/api/batches/"+id+"/approval", nil); resp.Status != 200 {
		t.Errorf("own approval status: %d", resp.Status)
	}
	if resp := bob.Do(t, "GET", "/api/batches/"+lt.BatchAwaiting+"/approval", nil); resp.Status != 404 {
		t.Errorf("another's approval status: %d", resp.Status)
	}

	// cancel: not another's
	if resp := bob.Do(t, "POST", "/api/batches/"+lt.BatchAwaiting+"/cancel", map[string]any{}); resp.Status != 403 {
		t.Errorf("editor cancels the agent's batch: %d %s", resp.Status, resp.Body)
	}
	_ = agent

	// the Owner approves and the batch starts
	if resp := owner.Do(t, "POST", "/human/api/batches/"+id+"/approve", map[string]any{"reason": "ok"}); resp.Status != 200 {
		t.Fatalf("owner approve: %d %s", resp.Status, resp.Body)
	}
	if got := count(t, r, `SELECT COUNT(*) FROM batches WHERE id = ? AND state = 'active'`, id); got != 1 {
		t.Error("the approved batch is not active")
	}
}

// TestEditorBatchWithdraw_E1: an Editor withdraws its own awaiting batch and the reservation is released.
func TestEditorBatchWithdraw_E1(t *testing.T) {
	t.Parallel()
	r := m3Rig(t)
	bob := r.User(t, servertest.UserEditor, config.RoleEditor)
	est := obj(bob.Do(t, "POST", "/api/generations/estimate", map[string]any{"items": []map[string]any{{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 1}}}).JSON(t))
	resp := bob.Do(t, "POST", "/api/generations", map[string]any{"confirmation_token": est["confirmation_token"]}, "Idempotency-Key", "e1-bob-2")
	id := obj(resp.JSON(t))["id"].(string)
	if got := count(t, r, `SELECT COALESCE(SUM(micro_usd),0) FROM ledger WHERE batch_id = ?`, id); got <= 0 {
		t.Fatalf("no reservation held: %d", got)
	}
	if resp := bob.Do(t, "POST", "/api/batches/"+id+"/cancel", map[string]any{}); resp.Status != 200 {
		t.Fatalf("withdraw: %d %s", resp.Status, resp.Body)
	}
	if got := count(t, r, `SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'release'`, id); got != 1 {
		t.Errorf("release rows = %d, want the reservation released", got)
	}
	if got := count(t, r, `SELECT COUNT(*) FROM batches WHERE id = ? AND state = 'rejected'`, id); got != 1 {
		t.Error("the withdrawn batch is not closed")
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
