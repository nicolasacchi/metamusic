package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var aceImg = "ghcr.io/example/worker-acestep@sha256:" + strings.Repeat("b", 64)

// aceRig is a rig whose runpod.env also names the ACE-Step endpoint.
func aceRig(t *testing.T) *rpRig {
	t.Helper()
	r := newRPRig(t)
	p := filepath.Join(r.sec, "runpod.env")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(raw, []byte("RUNPOD_ACESTEP_ENDPOINT_ID="+rpCanary("ace-endpoint")+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rpRig) seedACEQuote(t *testing.T, armed int) {
	t.Helper()
	idle := 20
	q := provider.Quote{Backend: provider.BackendRunPodACEStep, ListPricePPH: 1_100_000, Endpoint: provider.EndpointConfig{
		Name: "mm-acestep", Type: "QUEUE", ScalerType: "QUEUE_DELAY", ScalerValue: "360", WorkersMax: 1, IdleTimeoutS: &idle,
		TimeoutMS: 900_000, GPUCount: 1}}
	raw, _ := json.Marshal(q)
	r.exec(t, `INSERT OR REPLACE INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES (?,?,?,?,?,?)`,
		provider.BackendRunPodACEStep, armed, "system:default", store.FormatTime(time.Now().Add(-time.Hour)), string(raw), store.FormatTime(time.Now().Add(-time.Minute)))
}

func (r *rpRig) armedOf(t *testing.T, backend string) int {
	t.Helper()
	var armed int
	if err := r.db(t).R.QueryRow(`SELECT armed FROM provider_state WHERE backend = ?`, backend).Scan(&armed); err != nil {
		t.Fatal(err)
	}
	return armed
}

func TestCLI_EngineFlag_Table(t *testing.T) {
	t.Parallel()
	r := aceRig(t)
	env := map[string]string{}
	for k, v := range r.env {
		env[k] = v
	}
	env["MM_TOKEN"] = rpCanary("mm-token")
	rows := []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"provision", "--engine", "bogus"}, 2, "--engine"},
		{[]string{"provision", "--engine", "acestep15", "--vae-source", "image"}, 2, "yue2 flag"},
		{[]string{"provision", "--engine", "yue2", "--tasks", "turbo"}, 2, "acestep15 flags"},
		{[]string{"provision", "--tasks", "turbo"}, 2, "acestep15 flags"},
		{[]string{"provision", "--init-fail"}, 2, "acestep15 flags"},
		{[]string{"provision", "--engine", "acestep15", "--init-fail"}, 2, "--activate"},
		{[]string{"provision", "--engine", "acestep15", "--tasks", "base"}, 2, "turbo"},
		{[]string{"provision", "--engine", "acestep15", "--verify-models", "--activate"}, 2, "read-only"},
		{[]string{"status", "--engine", "x"}, 2, "--engine"},
		{[]string{"arm", "--engine", "x", "--endpoint-name", "n", "--reason", "r"}, 2, "--engine"},
		{[]string{"disarm", "--engine", "x", "--reason", "r"}, 2, "--engine"},
		{[]string{"reset-trip", "--engine", "x", "--reason", "r"}, 2, "--engine"},
		{[]string{"canary", "--engine", "x", "--url", "http://x"}, 2, "--engine"},
		{[]string{"canary", "--engine", "acestep15", "--url", "http://127.0.0.1:1", "--case", "t2m"}, 2, "--max-usd is required"},
		{[]string{"canary", "--engine", "acestep15", "--max-usd", "1", "--case", "t2m"}, 2, "--url"},
		{[]string{"canary", "--engine", "acestep15", "--max-usd", "1", "--url", "http://127.0.0.1:1"}, 2, "--case is required"},
		{[]string{"canary", "--engine", "acestep15", "--max-usd", "1", "--url", "http://127.0.0.1:1", "--case", "main"}, 2, "unknown case"},
		{[]string{"canary", "--engine", "acestep15", "--max-usd", "1", "--url", "http://127.0.0.1:1", "--all"}, 2, "--dry-run"},
		{[]string{"canary", "--engine", "acestep15", "--max-usd", "1", "--url", "http://127.0.0.1:1", "--case", "kill-switch"}, 2, "--kill-switch-confirm"},
		{[]string{"canary", "--engine", "acestep15", "--max-usd", "1", "--url", "http://127.0.0.1:1", "--case", "init-fail"}, 2, "--init-fail-confirm"},
		{[]string{"canary", "--engine", "acestep15", "--max-usd", "1", "--url", "http://127.0.0.1:1", "--case", "t2m", "--max-s", "500"}, 2, "--max-s"},
		{[]string{"canary", "--url", "http://127.0.0.1:1", "--all"}, 2, "acestep15 flag"},
		{[]string{"canary", "--dry-run", "--engine", "acestep15"}, 2, "--all"},
		{[]string{"canary", "--dry-run", "--engine", "acestep15", "--all", "--case", "t2m"}, 2, "exclude"},
		{[]string{"canary", "--dry-run", "--engine", "acestep15", "--all", "--part", "c"}, 2, "--part"},
	}
	for _, c := range rows {
		code, out, errs := rp(t, env, c.args...)
		if code != c.code || !strings.Contains(errs, c.msg) {
			t.Errorf("%v => exit %d (%s%s), want %d containing %q", c.args, code, out, errs, c.code, c.msg)
		}
	}
}

func TestCLI_ProvisionACEStep(t *testing.T) {
	t.Parallel()
	r := aceRig(t)
	workersMax := func() int { return int(r.srv.Endpoint()["workers"].(map[string]any)["max"].(float64)) }
	r.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" }) // so provision creates
	// a dry run prints the console step for the one cached model, no VAE and no runpodctl
	code, out, errs := rp(t, r.env, "provision", "--engine", "acestep15", "--image", aceImg, "--dry-run")
	if code != 0 || !strings.Contains(out, `"mm-acestep"`) || !strings.Contains(out, acestep.WeightsRepoMain) || strings.Contains(out, "runpodctl") ||
		strings.Contains(out, "VAE") {
		t.Fatalf("dry run: %d %s %s", code, out, errs)
	}
	if r.srv.Calls("create_endpoint") != 0 {
		t.Fatal("a dry run created the endpoint")
	}
	// --activate on the first run still creates it inactive; the hint names the variable and never the id
	code, out, errs = rp(t, r.env, "provision", "--engine", "acestep15", "--image", aceImg, "--activate")
	if code != 0 || workersMax() != 0 {
		t.Fatalf("create: %d %s %s max=%d", code, out, errs, workersMax())
	}
	if !strings.Contains(out, "RUNPOD_ACESTEP_ENDPOINT_ID") || strings.Contains(out, r.ep) || !strings.Contains(out, "inactive") {
		t.Errorf("hint:\n%s", out)
	}
	if n, _, _, _ := r.auditOps(t, "cli:runpod-provision"); n != 1 {
		t.Errorf("audit rows %d", n)
	}
	// activating is a later run
	if code, out, errs = rp(t, r.env, "provision", "--engine", "acestep15", "--image", aceImg, "--activate", "--max", "1"); code != 0 || workersMax() != 1 {
		t.Fatalf("activate: %d %s %s max=%d", code, out, errs, workersMax())
	}
	// init-fail writes the wrong revision; a plain run restores it
	revision := func() string { return r.srv.Endpoint()["env"].(map[string]any)["MM_ACESTEP_MAIN_REVISION"].(string) }
	if code, out, errs = rp(t, r.env, "provision", "--engine", "acestep15", "--image", aceImg, "--activate", "--max", "1", "--init-fail"); code != 0 ||
		revision() != strings.Repeat("0", 40) || !strings.Contains(out, "on purpose") {
		t.Fatalf("init-fail: %d %s %s rev=%s", code, out, errs, revision())
	}
	if code, out, errs = rp(t, r.env, "provision", "--engine", "acestep15", "--image", aceImg, "--deactivate"); code != 0 ||
		revision() != acestep.Weights[acestep.WeightsRepoMain] || workersMax() != 0 {
		t.Fatalf("restore: %d %s %s rev=%s", code, out, errs, revision())
	}
	// verify-models is read-only
	creates := r.srv.Calls("create_endpoint") + r.srv.Calls("patch_endpoint")
	code, out, errs = rp(t, r.env, "provision", "--engine", "acestep15", "--verify-models")
	if code != 0 || !strings.Contains(out, `"found": true`) || r.srv.Calls("create_endpoint")+r.srv.Calls("patch_endpoint") != creates {
		t.Fatalf("verify-models: %d %s %s", code, out, errs)
	}
	for _, o := range []string{out, errs} {
		if strings.Contains(o, r.keys.Admin) || strings.Contains(o, r.ep) {
			t.Error("a secret was printed")
		}
	}
}

func TestCLI_ACEStepBackendIsSeparate(t *testing.T) {
	t.Parallel()
	r := aceRig(t)
	r.seedQuote(t, 0, nil)
	r.seedACEQuote(t, 0)
	// the typed name must be the ACE-Step endpoint's
	if code, _, errs := rp(t, r.env, "arm", "--engine", "acestep15", "--endpoint-name", "mm-yue2", "--reason", "go"); code != 3 || !strings.Contains(errs, "does not match") {
		t.Errorf("yue2 name for the ACE backend: %d %s", code, errs)
	}
	if code, out, errs := rp(t, r.env, "arm", "--engine", "acestep15", "--endpoint-name", "mm-acestep", "--reason", "go"); code != 0 || !strings.Contains(out, "armed") {
		t.Fatalf("arm: %d %s %s", code, out, errs)
	}
	if r.armedOf(t, provider.BackendRunPodACEStep) != 1 || r.armedOf(t, provider.BackendRunPod) != 0 {
		t.Error("arming one backend touched the other")
	}
	// status names the backend and reads its row
	code, out, errs := rp(t, r.env, "status", "--engine", "acestep15", "--json")
	if code != 0 {
		t.Fatalf("status: %d %s %s", code, out, errs)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil || v["backend"] != provider.BackendRunPodACEStep || v["armed"] != true {
		t.Errorf("status %s", out)
	}
	// a trip on the ACE backend is reset by its own command only
	r.exec(t, `INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES ('t-ace', ?, ?, 'crash_loop', '{}', '[]')`,
		provider.BackendRunPodACEStep, store.FormatTime(rpNow))
	if code, out, _ := rp(t, r.env, "reset-trip", "--reason", "looked"); code != 0 || !strings.Contains(out, "no open trip") {
		t.Errorf("the yue2 reset touched the ACE trip: %d %s", code, out)
	}
	if code, out, errs := rp(t, r.env, "reset-trip", "--engine", "acestep15", "--reason", "restored the revision"); code != 0 || !strings.Contains(out, "trip reset") {
		t.Fatalf("reset: %d %s %s", code, out, errs)
	}
	if code, out, errs := rp(t, r.env, "disarm", "--engine", "acestep15", "--reason", "done"); code != 0 || r.armedOf(t, provider.BackendRunPodACEStep) != 0 {
		t.Fatalf("disarm: %d %s %s", code, out, errs)
	}
	// without the endpoint id in runpod.env the ACE-Step commands refuse and the yue2 ones still work
	if err := os.WriteFile(filepath.Join(r.sec, "runpod.env"), []byte("RUNPOD_JOBS_KEY="+r.keys.Jobs+"\nRUNPOD_READ_KEY="+r.keys.Read+"\nRUNPOD_ENDPOINT_ID="+r.ep+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := rp(t, r.env, "arm", "--engine", "acestep15", "--endpoint-name", "mm-acestep", "--reason", "go"); code != 3 || !strings.Contains(errs, "keys_absent") {
		t.Errorf("no ACE id: %d %s", code, errs)
	}
	if code, out, _ := rp(t, r.env, "status", "--engine", "acestep15"); code != 0 || !strings.Contains(out, "RUNPOD_ACESTEP_ENDPOINT_ID is not set") {
		t.Errorf("status without the id: %d %s", code, out)
	}
}

func TestCLI_ProbeCoversTwoEndpoints(t *testing.T) {
	t.Parallel()
	r := aceRig(t)
	out := filepath.Join(t.TempDir(), "caps.json")
	code, stdout, errs := rp(t, r.env, "probe", "--keys", "--out", out)
	if code != 0 {
		t.Fatalf("probe: %d %s %s", code, stdout, errs)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["acestep_endpoint_reachable"]; !ok {
		t.Errorf("the probe file lacks the ACE-Step fields:\n%s", stdout)
	}
	for _, secret := range []string{rpCanary("ace-endpoint"), r.ep, r.keys.Jobs} {
		if strings.Contains(stdout, secret) {
			t.Errorf("the probe output contains a secret")
		}
	}
}

func TestCLI_CanaryDryRun_NoSpendNoServer(t *testing.T) {
	t.Parallel()
	r := newRPRigNoDB(t)
	env := map[string]string{}
	for k, v := range r.env {
		env[k] = v
	}
	delete(env, "MM_TOKEN")
	for _, part := range []string{"a"} {
		code, out, errs := rp(t, env, "canary", "--engine", "acestep15", "--all", "--dry-run", "--part", part)
		if code != 0 {
			t.Fatalf("part %s: %d %s %s", part, code, out, errs)
		}
		want, err := os.ReadFile("../../internal/provider/runpod/testdata/canary_worst_case_" + part + ".golden")
		if err != nil {
			t.Fatal(err)
		}
		if out != string(want) {
			t.Errorf("part %s differs from the library golden:\n%s", part, out)
		}
	}
	if r.srv.Calls("run") != 0 || len(r.srv.Requests()) != 0 {
		t.Errorf("the dry run called the provider: %d requests", len(r.srv.Requests()))
	}
	// one case, a dearer price
	code, out, _ := rp(t, env, "canary", "--engine", "acestep15", "--dry-run", "--case", "t2m", "--price-usd-hour", "1.38")
	if code != 0 || !strings.Contains(out, "case t2m") || strings.Contains(out, "ILLUSTRATIVE") || strings.Contains(out, "case repaint") || strings.Contains(out, "init-fail window") {
		t.Errorf("single case:\n%s", out)
	}
}
