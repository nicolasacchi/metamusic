package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// BULK-P6 (C398.3, C398.4): list_pods and the pods block of get_provider_status.

var (
	podDigest = "sha256:" + strings.Repeat("a1", 32)
	secretRef = "rp-pod-ref-" + strings.Repeat("z", 8)
)

var (
	editorU = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", Scopes: auth.ScopesFor(auth.RoleEditor), Via: "proxy"}
	listenU = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "carol", Name: "carol", Scopes: auth.ScopesFor(auth.RoleListener), Via: "proxy"}
)

// podRig serves the default backend and a pod backend (fake-pod) through the real provider admin, with two pods: one booting, one
// terminated and settled. keyPresent is what the key-file probe answers.
func podStatusRig(t *testing.T, info *api.PodsInfo) *jobsRig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := func() time.Time { return t0 }
	for _, b := range []string{provider.BackendFake, provider.BackendFakePod} {
		q := providertest.GoodQuote(b, t0)
		if b == provider.BackendFakePod {
			q.Endpoint = provider.EndpointConfig{Name: b, Type: provider.EndpointTypePod, WorkersMax: 1, GPUCount: 1, ImageDigest: podDigest}
		}
		raw, _ := json.Marshal(q)
		if _, err := st.W.Exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES(?,1,'system:default',?,?,?)`,
			b, store.FormatTime(t0), string(raw), store.FormatTime(t0)); err != nil {
			t.Fatal(err)
		}
	}
	def := provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, now)
	pod := provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeFake, Backend: provider.BackendFakePod, KeysLoaded: true, ColdStartS: 1200, MaxRuntime: 6 * time.Hour}, now)
	gs, err := provider.NewGateSet(provider.BackendFake, def, pod)
	if err != nil {
		t.Fatal(err)
	}
	sp := spend.New(spend.Options{Store: st, Now: now})
	admin := jobs.NewProviderAdmin(jobs.ProviderOptions{Store: st, Gate: def, KillSlackS: 15, TTL: 2 * time.Hour, PriceMarginPPM: 100000})
	admin.UseBackends(gs, nil, nil)
	admin.UseBound(jobs.BoundInputs{Spend: sp})
	mux := http.NewServeMux()
	api.New(mux, api.Deps{Store: st, Jobs: jobs.New(jobs.Options{Store: st, Spend: sp}), Provider: admin, Now: now, Pods: info})
	return &jobsRig{rig: &rig{mux: mux}, st: st, gate: def, admin: admin}
}

// seedPods writes the pod fixture: foreign keys are off so that the rows need no batch, estimate or job behind them.
func seedPods(t *testing.T, r *jobsRig) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := r.st.W.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`PRAGMA foreign_keys = OFF`)
	at := store.FormatTime(t0)
	later := store.FormatTime(t0.Add(30 * time.Minute))
	ins := `INSERT INTO pods(id, name, kind, batch_id, n, gpu_try, gpu_type, state, cloud, image_digest, price_pph_micro_usd, overhead_reserved_micro_usd, deadline_at, poll_exp, created_at, updated_at)
		VALUES(?, ?, ?, ?, 1, ?, 'gpu-a', 'intent', 'SECURE', ?, 400000, 163750, ?, 0, ?, ?)`
	exec(ins, "pod-live-0001", "mm-aaaaaaaaaaaa", "batch", "b-1", 1, podDigest, later, at, at)
	exec(ins, "pod-done-0002", "mm-bbbbbbbbbbbb", "batch", "b-1", 2, podDigest, later, store.FormatTime(t0.Add(-time.Hour)), at)
	exec(ins, "pod-other-0003", "mm-cccccccccccc", "canary", "b-2", 1, podDigest, later, store.FormatTime(t0.Add(-2*time.Hour)), at)
	exec(`UPDATE pods SET state = 'creating', provider_ref = ?, create_sent_at = ? WHERE id = 'pod-live-0001'`, secretRef, at)
	exec(`UPDATE pods SET state = 'booting', first_poll_at = ?, last_poll_at = ? WHERE id = 'pod-live-0001'`, at, later)
	exec(`UPDATE pods SET state = 'terminated', terminated_at = ?, lifetime_s = 1800, settled = 1, error_code = 'BOOT_TIMEOUT' WHERE id = 'pod-done-0002'`, later)
	exec(`UPDATE pods SET state = 'terminated', terminated_at = ?, lifetime_s = 60 WHERE id = 'pod-other-0003'`, later)
	exec(`INSERT INTO pod_events(pod_id, at, event, code) VALUES('pod-live-0001', ?, 'key_absent', 'KEY_ABSENT')`, at)
	exec(`INSERT INTO pod_events(pod_id, at, event, code) VALUES('pod-done-0002', ?, 'boot_timeout', 'BOOT_TIMEOUT')`, at)
}

func podIDs(m map[string]any) []string {
	var out []string
	for _, it := range m["items"].([]any) {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

func TestListPods_Shape(t *testing.T) {
	t.Parallel()
	r := podStatusRig(t, nil)
	if got := r.must(t, human, "GET", "/api/pods", "", 200)["items"]; len(got.([]any)) != 0 {
		t.Fatalf("empty table: %v", got)
	}
	seedPods(t, r)
	// live by default, newest first
	m := r.must(t, human, "GET", "/api/pods", "", 200)
	if got := strings.Join(podIDs(m), ","); got != "pod-live-0001" {
		t.Fatalf("live = %s", got)
	}
	p := m["items"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "name", "kind", "batch_id", "state", "gpu_try", "created_at", "booted_at", "first_poll_at", "last_poll_at", "deadline_at", "terminated_at", "lifetime_s", "error_code", "cost"} {
		if _, ok := p[k]; !ok {
			t.Errorf("key %s missing in %v", k, p)
		}
	}
	if len(p) != 15 {
		t.Errorf("keys = %d: %v", len(p), p)
	}
	if p["state"] != "booting" || p["name"] != "mm-aaaaaaaaaaaa" || p["kind"] != "batch" || p["batch_id"] != "b-1" || p["terminated_at"] != nil || p["lifetime_s"] != nil || p["last_poll_at"] == nil {
		t.Errorf("live pod = %v", p)
	}
	cost := p["cost"].(map[string]any)
	if cost["reserved_micro_usd"] != float64(163750) || cost["settled_micro_usd"] != nil || cost["settled_usd"] != nil {
		t.Errorf("cost = %v", cost)
	}
	// all, newest first
	m = r.must(t, human, "GET", "/api/pods?state=all", "", 200)
	if got := strings.Join(podIDs(m), ","); got != "pod-live-0001,pod-done-0002,pod-other-0003" {
		t.Errorf("all = %s", got)
	}
	done := m["items"].([]any)[1].(map[string]any)
	// settled: the lifetime (1800 s) at the frozen price (400000 per hour) = 200000
	if c := done["cost"].(map[string]any); c["settled_micro_usd"] != float64(200000) || c["reserved_micro_usd"] != float64(163750) || done["error_code"] != "BOOT_TIMEOUT" || done["lifetime_s"] != float64(1800) {
		t.Errorf("settled pod = %v", done)
	}
	if other := m["items"].([]any)[2].(map[string]any); other["kind"] != "canary" || other["cost"].(map[string]any)["settled_micro_usd"] != nil {
		t.Errorf("canary pod = %v", other)
	}
	// a batch filter and a limit
	if got := strings.Join(podIDs(r.must(t, human, "GET", "/api/pods?state=all&batch_id=b-1", "", 200)), ","); got != "pod-live-0001,pod-done-0002" {
		t.Errorf("batch filter = %s", got)
	}
	if got := podIDs(r.must(t, human, "GET", "/api/pods?state=all&limit=1", "", 200)); len(got) != 1 {
		t.Errorf("limit = %v", got)
	}
	if got := podIDs(r.must(t, human, "GET", "/api/pods?batch_id=nope", "", 200)); len(got) != 0 {
		t.Errorf("unknown batch = %v", got)
	}
	// validation
	r.fail(t, human, "GET", "/api/pods?state=dead", "", 422, "VALIDATION_FAILED")
	r.fail(t, human, "GET", "/api/pods?limit=0", "", 422, "VALIDATION_FAILED")
	r.fail(t, human, "GET", "/api/pods?batch_id=a%20b", "", 422, "VALIDATION_FAILED")
}

func TestListPods_OwnerOnly(t *testing.T) {
	t.Parallel()
	r := podStatusRig(t, nil)
	seedPods(t, r)
	for _, p := range []auth.Principal{editorU, listenU} {
		r.fail(t, p, "GET", "/api/pods", "", 403, "OWNER_ONLY")
		r.fail(t, p, "GET", "/api/provider", "", 403, "OWNER_ONLY")
	}
	// the Owner's agent and device (Owner-side) read it
	r.must(t, fixtureAgent, "GET", "/api/pods", "", 200)
	r.must(t, phone, "GET", "/api/pods", "", 200)
}

// TestListPods_NoSecretFields: neither list_pods nor the status ever carries the provider reference, a poll URL, a signature or a key,
// and no cell of the pods table that could is selected.
func TestListPods_NoSecretFields(t *testing.T) {
	t.Parallel()
	r := podStatusRig(t, &api.PodsInfo{ImageDigest: podDigest, KeyPresent: func() bool { return true }, WindowMax: 8 * time.Hour})
	seedPods(t, r)
	_, _ = r.st.W.Exec(`UPDATE pods SET last_reported = 'https://mm.example.com/worker/v1/pods/x?sig=abc' WHERE id = 'pod-live-0001'`)
	for _, path := range []string{"/api/pods?state=all", "/api/provider"} {
		body := strings.ToLower(r.callWith(human, "GET", path, "").Body.String())
		for _, bad := range []string{strings.ToLower(secretRef), "provider_ref", "sig=", "/worker/", "https://", "runpod_pod_key", "api_key", "bearer"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s leaks %q", path, bad)
			}
		}
	}
}

func TestProviderStatus_PodsBlock(t *testing.T) {
	t.Parallel()
	keyOn := true
	info := &api.PodsInfo{ImageDigest: podDigest, KeyPresent: func() bool { return keyOn }, WindowMax: 8 * time.Hour}
	r := podStatusRig(t, info)
	seedPods(t, r)
	status := func() (map[string]any, map[string]any) {
		m := r.must(t, human, "GET", "/api/provider", "", 200)
		var y, p map[string]any
		for _, b := range m["backends"].([]any) {
			bm := b.(map[string]any)
			switch bm["backend"] {
			case provider.BackendFake:
				y = bm
			case provider.BackendFakePod:
				p = bm
			}
		}
		return y, p
	}
	y, p := status()
	if _, has := y["pods"]; has {
		t.Errorf("the default backend carries a pods block: %v", y["pods"])
	}
	if _, has := r.must(t, human, "GET", "/api/provider", "", 200)["pods"]; has {
		t.Error("the top level carries a pods block")
	}
	pods, ok := p["pods"].(map[string]any)
	if !ok {
		t.Fatalf("no pods block: %v", p)
	}
	if len(pods) != 7 {
		t.Errorf("keys = %v", pods)
	}
	if pods["key_present"] != true || pods["probe"] != "missing" || pods["safety"] != "unverified" || pods["safety_digest_matches"] != false {
		t.Errorf("block = %v", pods)
	}
	live := pods["live"].([]any)
	if len(live) != 1 || live[0].(map[string]any)["id"] != "pod-live-0001" {
		t.Errorf("live = %v", live)
	}
	win := pods["window"].(map[string]any)
	if win["armed_at"] != store.FormatTime(t0) || win["auto_disarm_at"] != store.FormatTime(t0.Add(8*time.Hour)) {
		t.Errorf("window = %v", win)
	}
	if got := pods["alerts"].([]any); len(got) != 1 || got[0] != "KEY_ABSENT" {
		t.Errorf("alerts = %v (only the live pod's events count)", got)
	}
	// a probe, a canary of another image, then of this one
	add := func(q string, args ...any) {
		t.Helper()
		if _, err := r.st.W.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	add(`INSERT INTO pod_safety_log(kind, verdict, detail_json, created_by, created_at) VALUES('probe', 'too_broad', '{}', 'cli', ?)`, store.FormatTime(t0))
	add(`INSERT INTO pod_safety_log(kind, image_digest, verdict, detail_json, created_by, created_at) VALUES('canary', ?, 'verified', '{}', 'system:bulk', ?)`, "sha256:"+strings.Repeat("b", 64), store.FormatTime(t0))
	_, p = status()
	pods = p["pods"].(map[string]any)
	if pods["probe"] != "too_broad" || pods["safety"] != "unverified" || pods["safety_digest_matches"] != false {
		t.Errorf("after a canary of another image = %v", pods)
	}
	add(`INSERT INTO pod_safety_log(kind, image_digest, verdict, detail_json, created_by, created_at) VALUES('canary', ?, 'failed', '{}', 'system:bulk', ?)`, podDigest, store.FormatTime(t0))
	_, p = status()
	if pods = p["pods"].(map[string]any); pods["safety"] != "failed" || pods["safety_digest_matches"] != true {
		t.Errorf("failed = %v", pods)
	}
	add(`INSERT INTO pod_safety_log(kind, image_digest, verdict, detail_json, created_by, created_at) VALUES('canary', ?, 'verified', '{}', 'system:bulk', ?)`, podDigest, store.FormatTime(t0))
	add(`INSERT INTO pod_safety_log(kind, verdict, detail_json, created_by, created_at) VALUES('probe', 'ok', '{}', 'cli', ?)`, store.FormatTime(t0))
	keyOn = false
	_, p = status()
	if pods = p["pods"].(map[string]any); pods["safety"] != "verified" || pods["probe"] != "ok" || pods["key_present"] != false {
		t.Errorf("verified = %v", pods)
	}
	// disarmed: no window, and the state-changing answer carries the block too
	m := r.must(t, human, "POST", "/human/api/provider/state", `{"armed":false,"reason":"done","backend":"fake-pod"}`, 200)
	var seen bool
	for _, b := range m["backends"].([]any) {
		if bm := b.(map[string]any); bm["backend"] == provider.BackendFakePod {
			var blk map[string]any
			if blk, seen = bm["pods"].(map[string]any); seen && blk["window"] != nil {
				t.Errorf("window while disarmed = %v", blk["window"])
			}
		}
	}
	if !seen {
		t.Error("update_provider_state answers no pods block")
	}
}

// TestProviderStatus_NoPodsInfoNoBlock: without a pod backend (Deps.Pods nil) no entry has a pods block, and the status is the one it
// always was.
func TestProviderStatus_NoPodsInfoNoBlock(t *testing.T) {
	t.Parallel()
	r := podStatusRig(t, nil)
	m := r.must(t, human, "GET", "/api/provider", "", 200)
	for _, b := range m["backends"].([]any) {
		if _, has := b.(map[string]any)["pods"]; has {
			t.Errorf("pods block without a pod configuration: %v", b)
		}
	}
	y := newProviderRig(t, true).must(t, human, "GET", "/api/provider", "", 200)
	if strings.Contains(string(mustJSON(t, y)), `"pods"`) {
		t.Error("a process with no pod backend answers a pods key")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
