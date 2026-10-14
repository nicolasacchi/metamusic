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
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func init() {
	for _, id := range []string{"list_batches", "get_batch", "cancel_batch", "list_jobs", "get_job", "wait_for_job", "cancel_job", "get_provider_status", "update_provider_state"} {
		implementedOps[id] = true
	}
}

// implementedOps lists the operations whose stubs a lane has replaced (the stub test skips them).
var implementedOps = map[string]bool{}

var (
	fixtureAgent = auth.Principal{Kind: auth.KindAgent, ID: "fixture-token", Name: "fixture-agent", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate}, Via: "token"}
	otherAgent   = auth.Principal{Kind: auth.KindAgent, ID: "tok-9", Name: "other", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "token"}
	phone        = auth.Principal{Kind: auth.KindDevice, ID: "dev-1", Name: "phone", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "device"}
)

type jobsRig struct {
	*rig
	st    *store.Store
	gate  *provider.Gate
	admin *jobs.ProviderAdmin
}

// newJobsRig serves the M3 fixture (finished, awaiting and failed batches) through the real job and
// provider services.
func newJobsRig(t *testing.T, mode string, keys bool) *jobsRig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.FixtureM3(t, st, lt.FixedNow)
	now := func() time.Time { return lt.FixedNow }
	gate := provider.NewGate(st.R, provider.GateConfig{Mode: mode, KeysLoaded: keys, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, now)
	admin := jobs.NewProviderAdmin(jobs.ProviderOptions{Store: st, Gate: gate, KillSlackS: 15, TTL: 2 * time.Hour, PriceMarginPPM: 100000})
	mux := http.NewServeMux()
	api.New(mux, api.Deps{Store: st, Jobs: jobs.New(jobs.Options{Store: st}), Provider: admin, Now: now})
	return &jobsRig{rig: &rig{mux: mux}, st: st, gate: gate, admin: admin}
}

func (r *jobsRig) do(p auth.Principal, method, path, body string) (int, map[string]any) {
	rec := r.callWith(p, method, path, body)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func (r *jobsRig) must(t *testing.T, p auth.Principal, method, path, body string, status int) map[string]any {
	t.Helper()
	code, m := r.do(p, method, path, body)
	if code != status {
		t.Fatalf("%s %s: %d %v, want %d", method, path, code, m, status)
	}
	return m
}

func (r *jobsRig) fail(t *testing.T, p auth.Principal, method, path, body string, status int, code string) map[string]any {
	t.Helper()
	got, m := r.do(p, method, path, body)
	if got != status || m["code"] != code {
		t.Fatalf("%s %s: %d %v, want %d %s", method, path, got, m, status, code)
	}
	return m
}

func ids(items any) []string {
	var out []string
	for _, it := range items.([]any) {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

func TestOps_JobsAndBatches(t *testing.T) {
	t.Parallel()
	r := newJobsRig(t, provider.ModeFake, true)

	// list_batches: newest first, filters, paging
	m := r.must(t, human, "GET", "/api/batches", "", 200)
	if got := ids(m["items"]); strings.Join(got, ",") != strings.Join([]string{lt.BatchAwaiting, lt.BatchFailed, lt.BatchDone}, ",") {
		t.Errorf("batches %v", got)
	}
	if m["next_cursor"] != nil {
		t.Errorf("next_cursor %v", m["next_cursor"])
	}
	m = r.must(t, human, "GET", "/api/batches?principal_kind=agent", "", 200)
	if got := ids(m["items"]); len(got) != 1 || got[0] != lt.BatchAwaiting {
		t.Errorf("agent batches %v", got)
	}
	m = r.must(t, human, "GET", "/api/batches?state=finished&state=rejected", "", 200)
	if got := ids(m["items"]); len(got) != 2 {
		t.Errorf("finished batches %v", got)
	}
	m = r.must(t, human, "GET", "/api/batches?limit=1", "", 200)
	if m["next_cursor"] == nil {
		t.Fatal("no cursor")
	}
	m2 := r.must(t, human, "GET", "/api/batches?limit=5&cursor="+m["next_cursor"].(string), "", 200)
	if got := ids(m2["items"]); len(got) != 2 || got[0] != lt.BatchFailed {
		t.Errorf("second page %v", got)
	}
	r.fail(t, human, "GET", "/api/batches?cursor=bogus", "", 422, "VALIDATION_FAILED")
	r.fail(t, human, "GET", "/api/batches?since=yesterday", "", 422, "VALIDATION_FAILED")

	// get_batch: the finished, reconciled one
	b := r.must(t, human, "GET", "/api/batches/"+lt.BatchDone, "", 200)
	cost := b["cost"].(map[string]any)
	if b["state"] != "finished" || cost["reconciled"] != true || cost["settled_micro_usd"] != float64(18216+12000) || cost["outstanding_micro_usd"] != float64(0) || cost["settled_usd"] != "0.030216" {
		t.Errorf("batch %v", b)
	}
	counts := b["counts"].(map[string]any)
	if counts["completed"] != float64(1) || counts["truncated"] != float64(0) {
		t.Errorf("counts %v", counts)
	}
	if ap := b["approval"].(map[string]any); ap["state"] != "approved" || ap["via"] != "human_create" {
		t.Errorf("approval %v", ap)
	}
	jl := b["jobs"].([]any)
	if len(jl) != 1 {
		t.Fatalf("jobs %v", jl)
	}
	jc := jl[0].(map[string]any)["cost"].(map[string]any)
	if jc["basis"] != "reconciled" || jc["display_micro_usd"] != float64(18216+12000) {
		t.Errorf("job cost %v", jc)
	}
	// principal references: the operator is masked for an agent
	if cb := b["created_by"].(map[string]any); cb["kind"] != "human" || cb["id"] == "" {
		t.Errorf("created_by %v", cb)
	}
	ba := r.must(t, fixtureAgent, "GET", "/api/batches/"+lt.BatchDone, "", 200)
	if cb := ba["created_by"].(map[string]any); cb["id"] != "human" || cb["name"] != "human" {
		t.Errorf("an agent sees the operator as %v", cb)
	}
	r.fail(t, human, "GET", "/api/batches/nope", "", 404, "NOT_FOUND")

	// list_jobs
	m = r.must(t, human, "GET", "/api/jobs", "", 200)
	if got := ids(m["items"]); len(got) != 4 {
		t.Errorf("jobs %v", got)
	}
	m = r.must(t, human, "GET", "/api/jobs?state=awaiting_confirmation", "", 200)
	if got := ids(m["items"]); len(got) != 2 {
		t.Errorf("awaiting %v", got)
	}
	m = r.must(t, human, "GET", "/api/jobs?track_id="+lt.TrackLuna, "", 200)
	if got := ids(m["items"]); len(got) != 1 || got[0] != lt.JobDone {
		t.Errorf("track filter (the mother) %v", got)
	}
	m = r.must(t, human, "GET", "/api/jobs?track_id="+lt.TrackJobChild, "", 200)
	if got := ids(m["items"]); len(got) != 1 || got[0] != lt.JobDone {
		t.Errorf("track filter (the child) %v", got)
	}
	m = r.must(t, human, "GET", "/api/jobs?batch_id="+lt.BatchAwaiting+"&limit=1", "", 200)
	if len(ids(m["items"])) != 1 || m["next_cursor"] == nil {
		t.Errorf("paging %v", m)
	}

	// get_job: environment and cost, no ref, URL or endpoint id
	j := r.must(t, fixtureAgent, "GET", "/api/jobs/"+lt.JobDone, "", 200)
	if j["state"] != "completed" || j["outcome"] != "clean" || j["track_id"] != lt.TrackJobChild || j["mode"] != "new_seed" || j["engine"] != "yue2" {
		t.Errorf("job %v", j)
	}
	env := j["env"].(map[string]any)
	if env["env_id"] != lt.EnvFixture || env["gpu_class"] != "RTX 4090" || env["detail"].(map[string]any)["gpu_name"] != "Fixture GPU" {
		t.Errorf("env %v", env)
	}
	tm := j["timings"].(map[string]any)
	if tm["execution_ms"] != float64(95040) {
		t.Errorf("timings %v", tm)
	}
	if j["audio_s"] != float64(216) || j["truncated"] != nil {
		t.Errorf("audio %v %v", j["audio_s"], j["truncated"])
	}
	raw, _ := json.Marshal(j)
	for _, bad := range []string{"fixture-ref-0001", "https://", "\"ref\"", "worker_key", "sig="} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("get_job leaks %q", bad)
		}
	}
	r.fail(t, human, "GET", "/api/jobs/nope", "", 404, "NOT_FOUND")
}

func TestOps_WaitForJob(t *testing.T) {
	t.Parallel()
	r := newJobsRig(t, provider.ModeFake, true)
	m := r.must(t, fixtureAgent, "GET", "/api/jobs/"+lt.JobDone+"/wait?timeout_s=0", "", 200)
	if m["changed"] != true || m["timed_out"] != false || m["job"].(map[string]any)["state"] != "completed" {
		t.Errorf("terminal job: %v", m)
	}
	m = r.must(t, fixtureAgent, "GET", "/api/jobs/"+lt.JobAwaiting1+"/wait?timeout_s=0", "", 200)
	if m["changed"] != false || m["timed_out"] != true {
		t.Errorf("unchanged job: %v", m)
	}
	m = r.must(t, fixtureAgent, "GET", "/api/jobs/"+lt.JobAwaiting1+"/wait?timeout_s=0&since_state=queued", "", 200)
	if m["changed"] != true || m["timed_out"] != false {
		t.Errorf("state differs from since_state: %v", m)
	}
	r.fail(t, fixtureAgent, "GET", "/api/jobs/nope/wait?timeout_s=0", "", 404, "NOT_FOUND")
	r.fail(t, fixtureAgent, "GET", "/api/jobs/"+lt.JobDone+"/wait?timeout_s=56", "", 422, "VALIDATION_FAILED")
}

func TestCancel_AgentNotOwner403(t *testing.T) {
	t.Parallel()
	r := newJobsRig(t, provider.ModeFake, true)
	r.fail(t, otherAgent, "POST", "/api/jobs/"+lt.JobAwaiting1+"/cancel", `{}`, 403, "NOT_BATCH_OWNER")
	r.fail(t, otherAgent, "POST", "/api/batches/"+lt.BatchAwaiting+"/cancel", `{}`, 403, "NOT_BATCH_OWNER")
	if n := cancelIntents(t, r.st); n != 0 {
		t.Errorf("%d intents written by a refused cancel", n)
	}
	j := r.must(t, fixtureAgent, "POST", "/api/jobs/"+lt.JobAwaiting1+"/cancel", `{"reason":"changed my mind"}`, 200)
	if j["cancel_requested_at"] == nil {
		t.Errorf("job %v", j)
	}
	if n := cancelIntents(t, r.st); n != 1 {
		t.Errorf("intents %d", n)
	}
	r.fail(t, human, "POST", "/api/jobs/nope/cancel", `{}`, 404, "NOT_FOUND")
	r.fail(t, human, "POST", "/api/batches/nope/cancel", `{}`, 404, "NOT_FOUND")
	r.fail(t, human, "POST", "/api/jobs/"+lt.JobAwaiting1+"/cancel", `{"reason":""}`, 422, "VALIDATION_FAILED")
}

func cancelIntents(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.R.QueryRow(`SELECT COUNT(*) FROM jobs WHERE cancel_requested_at IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCancel_DeviceAllowed(t *testing.T) {
	t.Parallel()
	r := newJobsRig(t, provider.ModeFake, true)
	b := r.must(t, phone, "POST", "/api/batches/"+lt.BatchAwaiting+"/cancel", `{}`, 200)
	for _, j := range b["jobs"].([]any) {
		if j.(map[string]any)["state"] != "awaiting_confirmation" {
			t.Errorf("the intent does not change state: %v", j)
		}
	}
	if n := cancelIntents(t, r.st); n != 2 {
		t.Errorf("intents %d, want 2", n)
	}
}

func TestCancel_TerminalIdempotent(t *testing.T) {
	t.Parallel()
	r := newJobsRig(t, provider.ModeFake, true)
	for i := range 2 {
		j := r.must(t, human, "POST", "/api/jobs/"+lt.JobDone+"/cancel", `{}`, 200)
		if j["state"] != "completed" || j["cancel_requested_at"] != nil {
			t.Fatalf("call %d: %v", i, j)
		}
		b := r.must(t, human, "POST", "/api/batches/"+lt.BatchDone+"/cancel", `{}`, 200)
		if b["state"] != "finished" {
			t.Fatalf("batch call %d: %v", i, b)
		}
	}
	if n := cancelIntents(t, r.st); n != 0 {
		t.Errorf("intents on terminal jobs: %d", n)
	}
}

func TestUpdateProviderState_API(t *testing.T) {
	t.Parallel()
	r := newJobsRig(t, provider.ModeFake, true)
	st := r.must(t, human, "GET", "/api/provider", "", 200)
	if st["backend"] != "fake" || st["armed"] != true || st["generation_enabled"] != true || st["trip"] != nil {
		t.Errorf("status %v", st)
	}
	st = r.must(t, human, "POST", "/human/api/provider/state", `{"armed":false,"reason":"maintenance"}`, 200)
	if st["armed"] != false {
		t.Errorf("disarm: %v", st)
	}
	if by := st["armed_changed_by"].(map[string]any); by["kind"] != "human" {
		t.Errorf("armed_changed_by %v", by)
	}
	r.fail(t, human, "POST", "/human/api/provider/state", `{"armed":true}`, 422, "VALIDATION_FAILED")
	r.fail(t, human, "POST", "/human/api/provider/state", `{"reason":"nothing to do"}`, 422, "VALIDATION_FAILED")
	r.fail(t, human, "POST", "/human/api/provider/state", `{"armed":true,"reason":""}`, 422, "VALIDATION_FAILED")
	st = r.must(t, human, "POST", "/human/api/provider/state", `{"armed":true,"reason":"back","reset_trip":true}`, 200)
	if st["armed"] != true {
		t.Errorf("arm: %v", st)
	}
	// the system default stays visible as a system principal for everyone
	r.must(t, human, "POST", "/human/api/provider/state", `{"armed":false,"reason":"x"}`, 200)
	if _, err := r.st.W.Exec(`UPDATE provider_state SET changed_by = 'system:default'`); err != nil {
		t.Fatal(err)
	}
	if by := r.must(t, fixtureAgent, "GET", "/api/provider", "", 200)["armed_changed_by"].(map[string]any); by["kind"] != "system" || by["name"] != "default" {
		t.Errorf("system principal %v", by)
	}
}

func TestUpdateProviderState_DisabledRunPodAndRestore(t *testing.T) {
	t.Parallel()
	r := newJobsRig(t, provider.ModeRunPod, false)
	if _, err := r.st.W.Exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES(?, 0, 'system:default', ?)`, provider.BackendRunPod, store.FormatTime(lt.FixedNow)); err != nil {
		t.Fatal(err)
	}
	st := r.must(t, human, "GET", "/api/provider", "", 200)
	if st["backend"] != "runpod-serverless" || st["generation_enabled"] != false || st["disabled_reason"] != "keys_absent" || st["armed"] != false {
		t.Errorf("status %v", st)
	}
	p := r.fail(t, human, "POST", "/human/api/provider/state", `{"armed":true,"reason":"go"}`, 409, "GENERATION_DISABLED")
	if p["details"].(map[string]any)["reason"] != "keys_absent" {
		t.Errorf("details %v", p["details"])
	}
	// after a restore the arm waits for a reconciliation
	r2 := newJobsRig(t, provider.ModeFake, true)
	if _, err := r2.st.W.Exec(`UPDATE provider_state SET armed = 0, reason = 'restored', restored_at = ?`, store.FormatTime(lt.FixedNow)); err != nil {
		t.Fatal(err)
	}
	if got := r2.must(t, human, "GET", "/api/provider", "", 200); got["restored_unreconciled"] != true {
		t.Errorf("status after restore %v", got["restored_unreconciled"])
	}
	r2.fail(t, human, "POST", "/human/api/provider/state", `{"armed":true,"reason":"go"}`, 409, "RECONCILE_REQUIRED")
}

func TestGetProviderStatus_NoSecrets(t *testing.T) {
	t.Parallel()
	r := newJobsRig(t, provider.ModeFake, true)
	canary := "canaryEndpoint0123"
	q, _ := json.Marshal(map[string]any{"Backend": "fake", "ListPricePPH": 690000, "Pools": []string{"AMPERE_24"},
		"Endpoint": map[string]any{"Name": "mm-yue2", "Type": "QUEUE", "WorkersMax": 2, "RequestURLs": map[string]string{"run": "https://api.runpod.ai/v2/" + canary + "/run"},
			"ImageDigest": "sha256:" + strings.Repeat("d", 64), "Pools": []string{"AMPERE_24"}}})
	if _, err := r.st.W.Exec(`UPDATE provider_state SET quote_json = ?, quote_at = ?`, string(q), store.FormatTime(lt.FixedNow)); err != nil {
		t.Fatal(err)
	}
	m := r.must(t, fixtureAgent, "GET", "/api/provider", "", 200)
	raw, _ := json.Marshal(m)
	for _, bad := range []string{canary, "api.runpod.ai", "https://", "worker", "Bearer", "rpa_"} {
		if strings.Contains(string(raw), bad) && bad != "worker" {
			t.Errorf("status leaks %q", bad)
		}
	}
	ep := m["endpoint"].(map[string]any)
	if ep["name"] != "mm-yue2" || ep["image_digest_short"] != strings.Repeat("d", 12) {
		t.Errorf("endpoint %v", ep)
	}
	if price := m["price"].(map[string]any); price["per_hour_micro_usd"] != float64(690000) || price["per_hour_usd"] != "0.69" {
		t.Errorf("price %v", price)
	}
	if m["capabilities"].(map[string]any)["billing"] != "none" {
		t.Errorf("capabilities %v", m["capabilities"])
	}
	if rtf := m["rtf"].([]any); len(rtf) != 3 {
		t.Errorf("rtf %v", rtf)
	}
	if bal := m["balance"].(map[string]any); bal["source"] != "unknown" {
		t.Errorf("balance %v", bal)
	}
}
