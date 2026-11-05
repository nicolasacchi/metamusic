package runpod_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
)

func aceCfg() runpod.DesiredConfig {
	return runpod.DesiredConfig{ColdStartS: 300, MaxRuntime: 15 * time.Minute}
}

func aceSpec(t *testing.T, cfg runpod.DesiredConfig, tasks string, workers int) runpod.EndpointSpec {
	t.Helper()
	s, err := runpod.DesiredSpecFor(cfg, runpod.ACEStepProfile(cfg, tasks), testDigest, workers, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func indented(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// main's DesiredSpec body, captured from origin/main before E2-PR, is the golden of the yue2 endpoint.
func TestDesiredSpec_Yue2Unchanged(t *testing.T) {
	t.Parallel()
	cfg := desiredCfg()
	got := indented(t, desired(t, cfg, 0))
	want, err := os.ReadFile("testdata/desired_yue2.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the yue2 create body changed:\n%s", got)
	}
	viaProfile, err := runpod.DesiredSpecFor(cfg, runpod.Yue2Profile(cfg), testDigest, 0, testCatalog())
	if err != nil || indented(t, viaProfile) != got {
		t.Errorf("Yue2Profile differs from DesiredSpec: %v", err)
	}
	// the error paths of main's function are still its own
	bad := cfg
	bad.VAESource = "nowhere"
	if _, err := runpod.DesiredSpecFor(bad, runpod.Yue2Profile(bad), testDigest, 0, nil); err == nil {
		t.Error("a bad VAE source was accepted")
	}
}

func TestDesiredSpec_ACEStep(t *testing.T) {
	t.Parallel()
	got := indented(t, aceSpec(t, aceCfg(), "turbo", 0))
	golden(t, "desired_acestep.golden.json", got)
	s := aceSpec(t, aceCfg(), "turbo", 0)
	if s.Name != "mm-acestep" || s.Workers.Max != 0 || s.Scaling.QueueDelay != 360 || s.Env["ENGINE"] != "acestep15" {
		t.Errorf("spec %+v", s)
	}
	if _, has := s.Env["MM_ACESTEP_BASE_REVISION"]; has {
		t.Error("a turbo endpoint carries no base revision")
	}
	if _, has := s.Env["MM_YUE2_3B_REVISION"]; has {
		t.Error("yue2 variables leaked into the ACE-Step environment")
	}
	// the body is valid against the pinned REST v2 snapshot, like yue2's
	d := loadSnapshot(t)
	if errs := d.validate(d.at("components", "schemas", "CreateEndpointRequest").(map[string]any), bodyOf(t, s), "$"); len(errs) > 0 {
		t.Errorf("body invalid against the snapshot: %v", errs)
	}
}

func TestDesiredSpec_ACEStepEnvDriftChecked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" })
	c := provClient(t, e)
	prof := runpod.ACEStepProfile(aceCfg(), "turbo")
	opts := runpod.ProvisionOptions{Desired: aceCfg(), Profile: &prof, Image: testDigest}
	if res, err := runpod.Provision(bg(), c, opts); err != nil || res.Action != "create" {
		t.Fatalf("create: %+v %v", res, err)
	}
	if res, err := runpod.Provision(bg(), c, opts); err != nil || res.Action != "none" {
		t.Fatalf("idempotent: %+v %v", res, err)
	}
	e.srv.SetEndpoint(func(ep map[string]any) {
		ep["env"].(map[string]any)["MM_ACESTEP_MAIN_REVISION"] = strings.Repeat("0", 40)
	})
	res, err := runpod.Provision(bg(), c, opts)
	if err != nil || res.Action != "patch" || !res.Applied || strings.Join(res.Diff, ",") != "env" {
		t.Fatalf("a drifted revision must be repaired by an env patch: %+v %v", res, err)
	}
}

func TestDesiredSpec_TasksChangeEnv(t *testing.T) {
	t.Parallel()
	turbo := aceSpec(t, aceCfg(), "turbo", 0)
	both := aceSpec(t, aceCfg(), "base,turbo", 0)
	if both.Env["MM_ACESTEP_TASKS"] != "turbo,base" || both.Env["MM_ACESTEP_BASE_REVISION"] != acestep.Weights[acestep.WeightsRepoBase] {
		t.Errorf("env %v", both.Env)
	}
	if reflect.DeepEqual(turbo.Env, both.Env) {
		t.Error("the task list does not change the environment")
	}
	// the single cached model stays the main bundle: the base DiT is baked into the image
	p := runpod.ACEStepProfile(aceCfg(), "turbo,base")
	if len(p.ModelRefs) != 1 || p.ModelRefs[0].Repo() != acestep.WeightsRepoMain {
		t.Errorf("model refs %+v", p.ModelRefs)
	}
	if _, err := runpod.DesiredSpecFor(aceCfg(), runpod.ACEStepProfile(aceCfg(), "base"), testDigest, 0, nil); !errors.Is(err, runpod.ErrBadTasks) {
		t.Errorf("base alone: %v", err)
	}
}

func TestDesiredSpec_PoolsDriveMaxSeconds(t *testing.T) {
	t.Parallel()
	if s := aceSpec(t, aceCfg(), "turbo", 0); s.Env["MM_ACESTEP_MAX_SECONDS"] != "480" || s.Env["MM_ACESTEP_POOL_VRAM_GB"] != "24" {
		t.Errorf("24 GB pools: %v", s.Env)
	}
	f, err := acestep.ParseFeatures([]byte(`{"schema":"mm.engine-features/2","engine":"acestep15","verified_at":"2026-11-10","canary_commit":"abc",
 "tasks":["text2music"],"reference":false,"reference_reproducible":false,"seed_reproducible":false,"lrc":false,"lrc_fixed_s":60,
 "models_mode":"bundle_cached_base_baked","max_s_default":480,
 "pools":{"AMPERE_24":{"max_s":480},"ADA_48_PRO":{"max_s":600,"canary_commit":"abc"}},
 "repaint":{"min_s":3,"max_s":90},"extend":{"min_s":3,"max_s":90},"reference_max_s":30}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := aceCfg()
	cfg.Pools, cfg.Features = []string{"ADA_48_PRO"}, &f
	if s := aceSpec(t, cfg, "turbo", 0); s.Env["MM_ACESTEP_MAX_SECONDS"] != "600" || s.Env["MM_ACESTEP_POOL_VRAM_GB"] != "48" {
		t.Errorf("a 48 GB-only endpoint with a verified 600: %v", s.Env)
	}
	// the cap is the minimum over the pools, and the memory class the smallest
	cfg.Pools = []string{"ADA_48_PRO", "AMPERE_24"}
	if s := aceSpec(t, cfg, "turbo", 0); s.Env["MM_ACESTEP_MAX_SECONDS"] != "480" || s.Env["MM_ACESTEP_POOL_VRAM_GB"] != "24" {
		t.Errorf("mixed pools: %v", s.Env)
	}
	cfg.Pools = []string{"MYSTERY"}
	if _, err := runpod.DesiredSpecFor(cfg, runpod.ACEStepProfile(cfg, "turbo"), testDigest, 0, nil); err == nil {
		t.Error("a pool that names no memory class was accepted")
	}
	if g, ok := runpod.PoolVRAMGB("ADA_32_PRO"); !ok || g != 32 {
		t.Errorf("ADA_32_PRO = %d %v", g, ok)
	}
}

func TestConsoleSteps_OnePerModel(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := provClient(t, e)
	prof := runpod.ACEStepProfile(aceCfg(), "turbo,base")
	res, err := runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: aceCfg(), Profile: &prof, Image: testDigest, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ConsoleSteps) != 1 {
		t.Fatalf("steps %v", res.ConsoleSteps)
	}
	step := res.ConsoleSteps[0]
	for _, want := range []string{`"mm-acestep"`, "Model settings", acestep.WeightsRepoMain, "at revision " + acestep.Weights[acestep.WeightsRepoMain]} {
		if !strings.Contains(step, want) {
			t.Errorf("step lacks %q: %s", want, step)
		}
	}
	if strings.Contains(step, acestep.WeightsRepoBase) {
		t.Error("the baked base DiT must not be a console step")
	}
	for _, s := range res.ConsoleSteps {
		if strings.Contains(s, e.epRaw) {
			t.Error("a console step names the endpoint id")
		}
	}
}

func TestConsoleSteps_NoRunpodctlLine(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := provClient(t, e)
	prof := runpod.ACEStepProfile(aceCfg(), "turbo")
	res, err := runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: aceCfg(), Profile: &prof, Image: testDigest, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	all := strings.ToLower(strings.Join(res.ConsoleSteps, "\n") + strings.Join(res.Warnings, "\n"))
	if strings.Contains(all, "runpodctl") || strings.Contains(all, "model-reference") {
		t.Errorf("a console step names the CLI (its flag goes through GraphQL, D-E2-41): %s", all)
	}
}

// the binary never shells out to runpodctl (or anything else) and never speaks REST v1 or GraphQL for the model.
func TestProvision_NeverShellsOut(t *testing.T) {
	t.Parallel()
	for _, glob := range []string{"*.go", "../../../cmd/metamusic/cmd_runpod.go"} {
		files, _ := filepath.Glob(glob)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src := string(raw)
			if strings.Contains(src, `"os/exec"`) {
				t.Errorf("%s imports os/exec", f)
			}
			if strings.Contains(src, "rest.runpod.io") {
				t.Errorf("%s names the retiring REST v1 host", f)
			}
		}
	}
}

func TestModelRef_PinnedRevision(t *testing.T) {
	t.Parallel()
	for _, p := range []runpod.EngineProfile{runpod.Yue2Profile(desiredCfg()), runpod.ACEStepProfile(aceCfg(), "turbo,base")} {
		if len(p.ModelRefs) != 1 {
			t.Fatalf("%s: %d model refs (one cached model per endpoint)", p.Engine, len(p.ModelRefs))
		}
		m := p.ModelRefs[0]
		if len(m.Revision) != 40 || strings.Contains(m.Revision, "main") || !strings.HasPrefix(m.URL, "https://huggingface.co/") {
			t.Errorf("%s: model ref %+v is not pinned", p.Engine, m)
		}
		if p.Weights[m.Repo()] != m.Revision {
			t.Errorf("%s: ref %+v differs from the weights pin %v", p.Engine, m, p.Weights)
		}
	}
}

func TestProvision_ACEStepCreatedInactive(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" })
	c := provClient(t, e)
	prof := runpod.ACEStepProfile(aceCfg(), "turbo")
	res, err := runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: aceCfg(), Profile: &prof, Image: testDigest, MaxWorkers: 2})
	if err != nil || res.Action != "create" || !res.Applied || res.WorkersMax != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
	last, _ := e.srv.Last(runpodtest.OpCreateEndpoint)
	var body struct {
		Name    string            `json:"name"`
		Workers struct{ Max int } `json:"workers"`
	}
	if err := json.Unmarshal(last.Body, &body); err != nil || body.Name != "mm-acestep" || body.Workers.Max != 0 {
		t.Fatalf("create body %s", last.Body)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "inactive") {
		t.Errorf("warnings %v", res.Warnings)
	}
	// once it exists, --activate is an ordinary patch of workers
	res, err = runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: aceCfg(), Profile: &prof, Image: testDigest, MaxWorkers: 1})
	if err != nil || res.Action != "patch" || strings.Join(res.Diff, ",") != "workers" || res.WorkersMax != 1 {
		t.Fatalf("activate: %+v %v", res, err)
	}
	// yue2 is untouched by the rule: it can still be created active
	e2 := newEnv(t)
	e2.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" })
	res, err = runpod.Provision(bg(), provClient(t, e2), runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights, Image: testDigest, MaxWorkers: 2})
	if err != nil || res.WorkersMax != 2 || len(res.Warnings) != 0 {
		t.Fatalf("yue2 create: %+v %v", res, err)
	}
}

func TestProvision_ACEStepSpec(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" })
	prof := runpod.ACEStepProfile(aceCfg(), "turbo")
	res, err := runpod.Provision(bg(), provClient(t, e), runpod.ProvisionOptions{Desired: aceCfg(), Profile: &prof, Image: testDigest, DryRun: true})
	if err != nil || res.Action != "create" || res.Applied || e.srv.Calls(runpodtest.OpCreateEndpoint) != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
	var pretty strings.Builder
	var m any
	_ = json.Unmarshal(res.Body, &m)
	b, _ := json.MarshalIndent(m, "", "  ")
	pretty.Write(b)
	pretty.WriteString("\n")
	golden(t, "provision_acestep_dryrun.golden.json", pretty.String())
	// without an image a create is refused, like yue2's
	if _, err := runpod.Provision(bg(), provClient(t, e), runpod.ProvisionOptions{Desired: aceCfg(), Profile: &prof}); !errors.Is(err, runpod.ErrImageRequired) {
		t.Errorf("err %v", err)
	}
}

func TestBackend_EngineIdentity(t *testing.T) {
	t.Parallel()
	y := runpod.NewBackend(runpod.BackendOptions{ColdStartS: 180, MaxRuntime: 15 * time.Minute})
	if y.Name() != provider.BackendRunPod || y.Engine() != provider.EngineYuE2 || y.ColdStartS() != 180 {
		t.Errorf("yue2 backend: %s %s %d", y.Name(), y.Engine(), y.ColdStartS())
	}
	a := runpod.NewBackend(runpod.BackendOptions{Engine: provider.EngineACEStep, ColdStartS: 300, MaxRuntime: 20 * time.Minute})
	if a.Name() != provider.BackendRunPodACEStep || a.Engine() != provider.EngineACEStep || a.ColdStartS() != 300 || a.MaxRuntime() != 20*time.Minute {
		t.Errorf("acestep backend: %s %s %d %v", a.Name(), a.Engine(), a.ColdStartS(), a.MaxRuntime())
	}
}

func TestKeysFor(t *testing.T) {
	t.Parallel()
	k := config.RunPodKeys{Jobs: canary("jobs"), Read: canary("read"), Write: canary("write"), EndpointID: canary("yue"), ACEStepEndpointID: canary("ace")}
	if got := runpod.KeysFor(k, "yue2"); got != k {
		t.Error("yue2 keys changed")
	}
	a := runpod.KeysFor(k, "acestep15")
	if a.EndpointID != k.ACEStepEndpointID || a.Jobs != k.Jobs || a.Read != k.Read {
		t.Errorf("acestep keys %+v", a)
	}
	k.ACEStepJobsKey = canary("acejobs")
	if a = runpod.KeysFor(k, "acestep15"); a.Jobs != k.ACEStepJobsKey {
		t.Error("the own jobs key is not used")
	}
}

func TestProbe_TwoEndpoints(t *testing.T) {
	t.Parallel()
	probe := func(t *testing.T, aceID, aceKey string, known ...string) *runpod.KeysReport {
		t.Helper()
		e := newEnv(t, func(o *envOpts) { o.srv.KnownEndpoints = known })
		keys := e.keys
		keys.ACEStepEndpointID, keys.ACEStepJobsKey = aceID, aceKey
		rep, err := runpod.ProbeKeys(bg(), runpod.ProbeOptions{Client: e.cl, Keys: keys, Clock: e.clk, PollEvery: time.Millisecond, PollFor: 5 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		r := probe(t, "", "")
		if r.ACEStepEndpointReachable != nil || r.ACEStepJobsKeyScoped != nil {
			t.Error("a yue2-only probe records ACE-Step fields")
		}
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), "acestep") {
			t.Errorf("the yue2-only probe file changed: %s", raw)
		}
	})
	t.Run("unknown endpoint", func(t *testing.T) {
		t.Parallel()
		r := probe(t, canary("no-such-endpoint"), "", canary("endpoint"))
		if r.ACEStepEndpointReachable == nil || *r.ACEStepEndpointReachable || *r.ACEStepJobsKeyScoped {
			t.Errorf("reachable %v scoped %v", r.ACEStepEndpointReachable, r.ACEStepJobsKeyScoped)
		}
		if got := r.Keys["jobs"]["acestep_health"]; got.HTTP != 404 || got.Verdict != runpod.VerdictAllowed {
			t.Errorf("health %+v", got)
		}
	})
	t.Run("own jobs key", func(t *testing.T) {
		t.Parallel()
		r := probe(t, canary("no-such-endpoint"), canary("acejobs"), canary("endpoint"))
		if _, ok := r.Keys["jobs"]["acestep_health_own_key"]; !ok || r.ACEStepJobsKeyScoped == nil || *r.ACEStepJobsKeyScoped {
			t.Errorf("report %+v", r.Keys["jobs"])
		}
	})
}

func TestProbe_TwoEndpoints_Reachable(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	keys := e.keys
	keys.ACEStepEndpointID = e.epRaw
	rep, err := runpod.ProbeKeys(bg(), runpod.ProbeOptions{Client: e.cl, Keys: keys, Clock: e.clk, PollEvery: time.Millisecond, PollFor: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if rep.ACEStepEndpointReachable == nil || !*rep.ACEStepEndpointReachable || !*rep.ACEStepJobsKeyScoped {
		t.Errorf("reachable %v scoped %v", rep.ACEStepEndpointReachable, rep.ACEStepJobsKeyScoped)
	}
	raw, _ := json.MarshalIndent(runpod.BuildCapabilities(t0, nil, rep), "", "  ")
	if err := runpod.ValidateCapabilities(raw); err != nil {
		t.Errorf("the capabilities file with the ACE-Step fields does not validate: %v", err)
	}
	if err := runpod.CheckFileSafe(raw, e.epRaw); err != nil {
		t.Errorf("file not safe: %v", err)
	}
}

func TestProvision_VerifyModelsReadOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := provClient(t, e)
	prof := runpod.ACEStepProfile(aceCfg(), "turbo")
	opts := runpod.ProvisionOptions{Desired: aceCfg(), Profile: &prof}
	rep, err := runpod.VerifyModels(bg(), c, opts)
	if err != nil || rep.Found || rep.Endpoint != "mm-acestep" || len(rep.ConsoleSteps) != 1 || rep.MountVisibleInAPI {
		t.Fatalf("rep %+v err %v", rep, err)
	}
	e.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "mm-acestep" })
	if rep, err = runpod.VerifyModels(bg(), c, opts); err != nil || !rep.Found || rep.WorkersMax == nil {
		t.Fatalf("rep %+v err %v", rep, err)
	}
	if e.srv.Calls(runpodtest.OpPatchEndpoint)+e.srv.Calls(runpodtest.OpCreateEndpoint) != 0 {
		t.Error("verify-models wrote")
	}
}
