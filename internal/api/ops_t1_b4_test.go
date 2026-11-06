package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

type t1World struct {
	r  *servertest.Rig
	fx lt.T1Info
}

func newT1World(t *testing.T) t1World {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true, RealFeatures: true})
	fx := lt.ApplyT1(t, r.Store, r.Clock.Now())
	r.Settle(t)
	return t1World{r: r, fx: fx}
}

func glacier(src string, factor float64) map[string]any {
	return map[string]any{"kind": "glacier", "glacier": map[string]any{"source_id": src, "factor": factor, "length_s": 10}}
}

func t1JSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return m
}

func count(t *testing.T, r *servertest.Rig, q string) (n int) {
	t.Helper()
	if err := r.Store.R.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEstimate_CPUAndZeroCost(t *testing.T) {
	t.Parallel()
	w := newT1World(t)
	agent, _ := w.r.Agent(t, rw...)
	resp := agent.Do(t, "POST", "/api/transforms/estimate", glacier(w.fx.Tx["A"], 20))
	if resp.Status != 200 {
		t.Fatalf("estimate: %d %s", resp.Status, resp.Body)
	}
	m := t1JSON(t, resp.Body)
	cost := m["cost"].(map[string]any)
	cpu := m["cpu"].(map[string]any)
	if cost["micro_usd"].(float64) != 0 || cost["usd"] != "0.000000" || cpu["model"] != "cpu/1" || cpu["expected_ms"].(float64) <= 0 ||
		cpu["max_ms"].(float64) < cpu["expected_ms"].(float64) || m["kind"] != "glacier" || m["spec_hash"] == "" {
		t.Errorf("estimate = %s", resp.Body)
	}
	if out := m["output"].(map[string]any); out["bits"].(float64) != 24 || out["channels"].(float64) != 2 {
		t.Errorf("output = %v", out)
	}
	if q := m["quota"].(map[string]any); q["principal_kind"] != "agent" {
		t.Errorf("quota = %v", q)
	}
}

func TestEstimate_WritesNothing(t *testing.T) {
	t.Parallel()
	w := newT1World(t)
	agent, _ := w.r.Agent(t, rw...)
	before := [3]int{count(t, w.r, `SELECT COUNT(*) FROM transforms`), count(t, w.r, `SELECT COUNT(*) FROM tracks`), count(t, w.r, `SELECT COUNT(*) FROM recipes`)}
	for range 2 {
		if r := agent.Do(t, "POST", "/api/transforms/estimate", glacier(w.fx.Tx["A"], 20)); r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
	}
	after := [3]int{count(t, w.r, `SELECT COUNT(*) FROM transforms`), count(t, w.r, `SELECT COUNT(*) FROM tracks`), count(t, w.r, `SELECT COUNT(*) FROM recipes`)}
	if before != after {
		t.Errorf("rows before %v after %v: estimate wrote", before, after)
	}
}

func TestCreate_Status202And200(t *testing.T) {
	t.Parallel()
	w := newT1World(t)
	agent, _ := w.r.Agent(t, rw...)
	r1 := agent.Do(t, "POST", "/api/transforms", glacier(w.fx.Tx["A"], 20), "Idempotency-Key", "k-1")
	if r1.Status != 202 {
		t.Fatalf("create: %d %s", r1.Status, r1.Body)
	}
	v := t1JSON(t, r1.Body)
	if v["state"] != "queued" || v["reused"] != false || v["id"] != v["take_id"] || v["cost"].(map[string]any)["usd"] != "0.000000" {
		t.Errorf("view = %s", r1.Body)
	}
	// the same key replays the stored answer; the same spec under a new key is the live one, reused
	if r := agent.Do(t, "POST", "/api/transforms", glacier(w.fx.Tx["A"], 20), "Idempotency-Key", "k-1"); r.Status != 202 || t1JSON(t, r.Body)["id"] != v["id"] {
		t.Errorf("replay: %d %s", r.Status, r.Body)
	}
	r3 := agent.Do(t, "POST", "/api/transforms", glacier(w.fx.Tx["A"], 20), "Idempotency-Key", "k-2")
	if r3.Status != 202 || t1JSON(t, r3.Body)["reused"] != true || t1JSON(t, r3.Body)["id"] != v["id"] {
		t.Errorf("reuse: %d %s", r3.Status, r3.Body)
	}
	if n := count(t, w.r, `SELECT COUNT(*) FROM transforms`); n != 1 {
		t.Errorf("%d transform rows, want 1", n)
	}
}

func TestAudit_T1(t *testing.T) {
	t.Parallel()
	w := newT1World(t)
	agent, _ := w.r.Agent(t, rw...)
	if r := agent.Do(t, "POST", "/api/transforms", glacier(w.fx.Tx["A"], 20), "Idempotency-Key", "a-1"); r.Status != 202 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var cost, status int
	if err := w.r.Store.R.QueryRow(`SELECT cost_micro_usd, status FROM audit_log WHERE op = 'create_transform'`).Scan(&cost, &status); err != nil {
		t.Fatalf("no create_transform audit row: %v", err)
	}
	if cost != 0 || status != 202 {
		t.Errorf("audit cost %d status %d", cost, status)
	}
}

func TestReads_T1(t *testing.T) {
	t.Parallel()
	w := newT1World(t)
	agent, _ := w.r.Agent(t, rw...)
	id := t1JSON(t, agent.Do(t, "POST", "/api/transforms", glacier(w.fx.Tx["A"], 20), "Idempotency-Key", "r-1").Body)["id"].(string)

	if g := agent.Do(t, "GET", "/api/transforms/"+id, nil); g.Status != 200 || t1JSON(t, g.Body)["state"] != "queued" {
		t.Errorf("get: %d %s", g.Status, g.Body)
	}
	l := agent.Do(t, "GET", "/api/transforms?kind=glacier&track_id="+w.fx.Tx["A"], nil)
	items, _ := t1JSON(t, l.Body)["items"].([]any)
	if l.Status != 200 || len(items) != 1 {
		t.Errorf("list: %d %s", l.Status, l.Body)
	}
	if l := agent.Do(t, "GET", "/api/transforms?state=done", nil); l.Status != 200 || len(t1JSON(t, l.Body)["items"].([]any)) != 0 {
		t.Errorf("list done: %d %s", l.Status, l.Body)
	}
	wt := agent.Do(t, "GET", "/api/transforms/"+id+"/wait?timeout_s=0&since_state=running", nil)
	m := t1JSON(t, wt.Body)
	if wt.Status != 200 || m["changed"] != true || m["transform"].(map[string]any)["id"] != id {
		t.Errorf("wait: %d %s", wt.Status, wt.Body)
	}
	wt = agent.Do(t, "GET", "/api/transforms/"+id+"/wait?timeout_s=0", nil)
	if m := t1JSON(t, wt.Body); m["timed_out"] != true || m["changed"] != false {
		t.Errorf("wait without change: %s", wt.Body)
	}
	c := agent.Do(t, "GET", "/api/transforms/capabilities", nil)
	cm := t1JSON(t, c.Body)
	if c.Status != 200 || cm["enabled"] != true || len(cm["kinds"].([]any)) < 5 {
		t.Errorf("capabilities: %d %.300s", c.Status, c.Body)
	}
	for _, k := range cm["kinds"].([]any) {
		if k.(map[string]any)["kind"] == "score_render" {
			t.Error("score_render listed without T1b")
		}
	}
}

func TestCancel_T1(t *testing.T) {
	t.Parallel()
	w := newT1World(t)
	agent, _ := w.r.Agent(t, rw...)
	other, _ := w.r.Agent(t, rw...)
	human := w.r.Human(t)
	id := t1JSON(t, agent.Do(t, "POST", "/api/transforms", glacier(w.fx.Tx["A"], 20), "Idempotency-Key", "c-1").Body)["id"].(string)

	// an agent that is not the owner is refused; the owner names no one else
	r := other.Do(t, "POST", "/api/transforms/"+id+"/cancel", nil)
	if p := t1JSON(t, r.Body); r.Status != 403 || p["code"] != "FORBIDDEN" {
		t.Errorf("foreign cancel: %d %s", r.Status, r.Body)
	}
	r = agent.Do(t, "POST", "/api/transforms/"+id+"/cancel", nil)
	m := t1JSON(t, r.Body)
	if r.Status != 200 || m["state"] != "cancelled" || m["error"].(map[string]any)["code"] != "CANCELLED_BY_USER" || m["cpu"].(map[string]any)["used_ms"].(float64) != 0 {
		t.Errorf("cancel: %d %s", r.Status, r.Body)
	}
	// terminal: a no-op returning the view
	if r := human.Do(t, "POST", "/api/transforms/"+id+"/cancel", nil); r.Status != 200 {
		t.Errorf("cancel again: %d %s", r.Status, r.Body)
	}
	if r := agent.Do(t, "POST", "/api/transforms/nope/cancel", nil); r.Status != 404 {
		t.Errorf("unknown: %d %s", r.Status, r.Body)
	}
}

func TestErrors_T1(t *testing.T) {
	t.Parallel()
	w := newT1World(t)
	agent, _ := w.r.Agent(t, rw...)
	post := func(body any) (int, map[string]any, *servertest.Response) {
		r := agent.Do(t, "POST", "/api/transforms/estimate", body)
		return r.Status, t1JSON(t, r.Body), r
	}
	details := func(m map[string]any) map[string]any { d, _ := m["details"].(map[string]any); return d }

	// kind and member disagree
	st, m, _ := post(map[string]any{"kind": "glacier", "widen": map[string]any{"source_id": w.fx.Tx["A"]}})
	if st != 422 || m["code"] != "INVALID_TRANSFORM" || details(m)["rule"] != "kind_params_mismatch" {
		t.Errorf("mismatch: %d %v", st, m)
	}
	// a range the schema does not bound
	st, m, _ = post(map[string]any{"kind": "glacier", "glacier": map[string]any{"source_id": w.fx.Tx["A"], "length_s": 100000}})
	if st != 422 || (m["code"] != "TRANSFORM_TOO_LARGE" && m["code"] != "INVALID_TRANSFORM") {
		t.Errorf("too long: %d %v", st, m)
	}
	// unknown input
	if st, m, _ = post(glacier("01900000-0000-7000-8000-ffffffffffff", 20)); st != 404 || m["code"] != "NOT_FOUND" {
		t.Errorf("unknown input: %d %v", st, m)
	}
	// a reference import has no recipe
	if st, m, _ = post(glacier(lt.TrackRefFlac, 20)); st != 422 || m["code"] != "RECIPE_UNKNOWN" {
		t.Errorf("reference: %d %v", st, m)
	}
	// a morph of a Take with itself
	st, m, _ = post(map[string]any{"kind": "morph", "morph": map[string]any{"track_a_id": w.fx.Tx["A"], "track_b_id": w.fx.Tx["A"]}})
	if st != 422 || m["code"] != "INVALID_TRANSFORM" || details(m)["rule"] != "same_input" {
		t.Errorf("same input: %d %v", st, m)
	}
	// score_render is T1b: not enabled here
	st, m, _ = post(map[string]any{"kind": "score_render", "score_render": map[string]any{"score": map[string]any{"abc": "X:1\nK:C\nCDEF|"}}})
	if st != 503 || m["code"] != "UNAVAILABLE" {
		t.Errorf("score_render: %d %v", st, m)
	}
}

func TestQueueFull_T1(t *testing.T) {
	t.Parallel()
	w := newT1World(t)
	agent, _ := w.r.Agent(t, rw...)
	var last *servertest.Response
	for i := range 8 {
		last = agent.Do(t, "POST", "/api/transforms", glacier(w.fx.Tx["A"], float64(10+i)), "Idempotency-Key", fmt.Sprintf("q-%d", i))
		if last.Status == 429 {
			break
		}
		if last.Status != 202 {
			t.Fatalf("create %d: %d %s", i, last.Status, last.Body)
		}
	}
	m := t1JSON(t, last.Body)
	d, _ := m["details"].(map[string]any)
	if last.Status != 429 || m["code"] != "TRANSFORM_QUEUE_FULL" || d["max"] == nil || last.Header.Get("Retry-After") == "" {
		t.Errorf("queue full: %d %s retry-after %q", last.Status, last.Body, last.Header.Get("Retry-After"))
	}
}

func TestAuthz_T1(t *testing.T) {
	t.Parallel()
	w := newT1World(t)
	ro, _ := w.r.Agent(t, auth.ScopeRead)
	for _, op := range t1Ops {
		path := strings.NewReplacer("{id}", "x").Replace(op.path)
		r := ro.Do(t, op.method, path, bodyOr(op))
		if op.scope == auth.ScopeWrite && r.Status != 403 {
			t.Errorf("%s with read scope: %d", op.id, r.Status)
		}
		if op.scope == auth.ScopeRead && r.Status == 403 {
			t.Errorf("%s with read scope: 403", op.id)
		}
	}
	// no credentials at all
	for _, op := range t1Ops {
		req, _ := http.NewRequest(op.method, w.r.URL+strings.NewReplacer("{id}", "x").Replace(op.path), strings.NewReader(op.reqBody))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s without credentials: %d", op.id, resp.StatusCode)
		}
	}
}

func bodyOr(op t1Op) any {
	if op.reqBody == "" {
		return nil
	}
	return op.reqBody
}

// TestLeaks_T1: no response carries a path of the host.
func TestLeaks_T1(t *testing.T) {
	t.Parallel()
	w := newT1World(t)
	agent, _ := w.r.Agent(t, rw...)
	var all []string
	collect := func(r *servertest.Response) { all = append(all, string(r.Body)) }
	collect(agent.Do(t, "POST", "/api/transforms/estimate", glacier(w.fx.Tx["A"], 20)))
	collect(agent.Do(t, "POST", "/api/transforms", glacier(w.fx.Tx["A"], 20), "Idempotency-Key", "l-1"))
	collect(agent.Do(t, "GET", "/api/transforms", nil))
	collect(agent.Do(t, "GET", "/api/transforms/capabilities", nil))
	for _, b := range all {
		if strings.Contains(b, w.r.DataDir) || strings.Contains(b, "/incoming/") {
			t.Errorf("response leaks a path: %.200s", b)
		}
	}
}
