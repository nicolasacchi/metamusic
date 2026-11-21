package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestBenchOps_RoleMatrix: the Critic Bench is the Owner's (D-M6N-8, approved 2026-11-21): the Owner and delegates read it, Editors
// and Listeners are refused, the Meta is the C365 row.
func TestBenchOps_RoleMatrix(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true})
	r.Settle(t)
	var found bool
	for _, op := range api.Operations(r.API) {
		if op.ID != "get_critic_bench" {
			continue
		}
		found = true
		m := op.Meta
		if m.Scope != auth.ScopeRead || m.Cost != "free" || m.MCP != "generic" || m.UI != "/critics" || !m.OwnerOnly || m.HumanOnly {
			t.Errorf("meta %+v", m)
		}
		if op.Method != "GET" || op.Path != "/api/critic-bench" {
			t.Errorf("%s %s", op.Method, op.Path)
		}
	}
	if !found {
		t.Fatal("get_critic_bench is not registered")
	}
	owner := r.Human(t)
	editor := r.User(t, "bob", config.RoleEditor)
	listener := r.User(t, "carol", config.RoleListener)
	read, _ := r.Agent(t, auth.ScopeRead)
	for name, c := range map[string]*servertest.Client{"owner": owner, "agent read": read} {
		if resp := c.Do(t, "GET", "/api/critic-bench", nil); resp.Status != 200 {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
	}
	for name, c := range map[string]*servertest.Client{"editor": editor, "listener": listener} {
		resp := c.Do(t, "GET", "/api/critic-bench", nil)
		if resp.Status != 403 {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
	}
}

// TestBench_REST: the body shape, the query, the 404 and that the GET writes nothing.
func TestBench_REST(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true})
	r.Settle(t)
	owner := r.Human(t)
	for i, tk := range []string{lt.TrackLuna, lt.TrackCitta} {
		if _, err := r.Store.W.Exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'energy', 'mm-energy/1 x', 'A', ?, ?)`,
			tk, fmt.Sprintf(`{"energy":0.%d}`, i+1), "2026-11-21T10:00:00.000Z"); err != nil {
			t.Fatal(err)
		}
	}
	count := func() (n int) {
		if err := r.Store.R.QueryRow(`SELECT (SELECT count(*) FROM analyses) + (SELECT count(*) FROM ratings) + (SELECT count(*) FROM duel_verdicts) + (SELECT count(*) FROM audit_log)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	resp := owner.Do(t, "GET", "/api/critic-bench", nil)
	if resp.Status != 200 {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
	b := obj(t, resp.JSON(t))
	rule := obj(t, b["rule"])
	if rule["min_n"] != float64(30) || rule["trusted_lower"] != 0.55 || rule["contrary_upper"] != 0.45 {
		t.Errorf("rule %v", rule)
	}
	if !strings.Contains(fmt.Sprint(b["note"]), "nothing is ever discarded automatically") {
		t.Errorf("note %v", b["note"])
	}
	critics, _ := b["critics"].([]any)
	var energy map[string]any
	for _, c := range critics {
		if obj(t, c)["id"] == "tier_a.energy" {
			energy = obj(t, c)
		}
	}
	if energy == nil || energy["tier"] != "A" || energy["kind"] != "metric" || energy["direction"] != float64(1) {
		t.Fatalf("critics %v", critics)
	}
	if resp = owner.Do(t, "GET", "/api/critic-bench?include_unknown=false", nil); resp.Status != 200 || strings.Contains(string(resp.Body), "tier_a.energy") {
		t.Errorf("include_unknown=false: %d %s", resp.Status, resp.Body)
	}
	if resp = owner.Do(t, "GET", "/api/critic-bench?critic=tier_a.energy", nil); resp.Status != 200 || !strings.Contains(string(resp.Body), `"id":"tier_a.energy"`) {
		t.Errorf("one critic: %d %s", resp.Status, resp.Body)
	}
	if resp = owner.Do(t, "GET", "/api/critic-bench?critic=nope", nil); resp.Status != 404 || obj(t, resp.JSON(t))["code"] != "NOT_FOUND" {
		t.Errorf("unknown critic: %d %s", resp.Status, resp.Body)
	}
	if count() != before {
		t.Error("the GET wrote")
	}
}
