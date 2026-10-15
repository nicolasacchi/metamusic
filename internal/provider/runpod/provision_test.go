package runpod_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
)

var testDigest = "ghcr.io/example/worker@sha256:" + strings.Repeat("a", 64)

func desiredCfg() runpod.DesiredConfig {
	return runpod.DesiredConfig{ColdStartS: 180, MaxRuntime: 15 * time.Minute}
}

func testCatalog() []runpod.GPUType {
	return []runpod.GPUType{
		{ID: "gpu-a5000", Name: "RTX A5000", Pool: "AMPERE_24", ServerlessPrice: "0.69"},
		{ID: "gpu-l4", Name: "L4", Pool: "AMPERE_24", ServerlessPrice: "0.69"},
		{ID: "gpu-4090", Name: "RTX 4090", Pool: "ADA_24", ServerlessPrice: "1.10"},
	}
}

func desired(t *testing.T, c runpod.DesiredConfig, workers int) runpod.EndpointSpec {
	t.Helper()
	s, err := runpod.DesiredSpec(c, yue2.Weights, testDigest, workers, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ---- a small OpenAPI validator for the create body ----------------------------------------------------

func (d doc) validate(schema map[string]any, v any, path string) []string {
	schema = d.deref(schema)
	if schema == nil {
		return []string{path + ": no schema"}
	}
	if alts, ok := schema["oneOf"].([]any); ok {
		ok := 0
		for _, a := range alts {
			if len(d.validate(d.deref(a), v, path)) == 0 {
				ok++
			}
		}
		if ok != 1 {
			return []string{fmt.Sprintf("%s: matches %d of %d alternatives", path, ok, len(alts))}
		}
		return nil
	}
	var errs []string
	typ, _ := schema["type"].(string)
	switch x := v.(type) {
	case map[string]any:
		if typ != "object" {
			return []string{path + ": object where " + typ + " expected"}
		}
		props, _ := schema["properties"].(map[string]any)
		extra, hasExtra := schema["additionalProperties"].(map[string]any)
		for k, e := range x {
			switch p, known := props[k]; {
			case known:
				errs = append(errs, d.validate(p.(map[string]any), e, path+"."+k)...)
			case hasExtra:
				errs = append(errs, d.validate(extra, e, path+"."+k)...)
			default:
				errs = append(errs, path+"."+k+": not in the schema")
			}
		}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if _, present := x[r.(string)]; !present {
					errs = append(errs, path+": missing required "+r.(string))
				}
			}
		}
	case []any:
		if typ != "array" {
			return []string{path + ": array where " + typ + " expected"}
		}
		items, _ := schema["items"].(map[string]any)
		for i, e := range x {
			errs = append(errs, d.validate(items, e, fmt.Sprintf("%s[%d]", path, i))...)
		}
	case string:
		if typ != "string" {
			return []string{path + ": string where " + typ + " expected"}
		}
		if enum, ok := schema["enum"].([]any); ok && !slices.Contains(enum, any(x)) {
			errs = append(errs, path+": "+x+" not in the enum")
		}
	case json.Number:
		f, _ := x.Float64()
		if typ == "integer" && float64(int64(f)) != f {
			errs = append(errs, path+": not an integer")
		} else if typ != "integer" && typ != "number" {
			errs = append(errs, path+": number where "+typ+" expected")
		}
		if m, ok := schema["minimum"].(float64); ok && f < m {
			errs = append(errs, path+": below the minimum")
		}
	default:
		errs = append(errs, fmt.Sprintf("%s: unsupported value %T", path, v))
	}
	return errs
}

func bodyOf(t *testing.T, s runpod.EndpointSpec) map[string]any {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func leafPaths(prefix string, v any, out *[]string) {
	if m, ok := v.(map[string]any); ok && prefix != "env" {
		for k, e := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			leafPaths(p, e, out)
		}
		return
	}
	*out = append(*out, prefix)
}

func TestProvision_SpecFieldsInSnapshot(t *testing.T) {
	t.Parallel()
	d := loadSnapshot(t)
	var paths []string
	leafPaths("", bodyOf(t, desired(t, desiredCfg(), 1)), &paths)
	sort.Strings(paths)
	for _, p := range []string{"name", "type", "scaling.type", "scaling.queueDelay", "workers.min", "workers.max", "workers.idleTimeout",
		"gpu.count", "gpu.pools", "gpu.excludedTypes", "gpu.minCudaVersion", "flashboot", "timeout", "image", "env"} {
		if !slices.Contains(paths, p) {
			t.Errorf("the body lacks %s (got %v)", p, paths)
		}
	}
	for _, p := range paths {
		if !d.resolve("CreateEndpointRequest", p) {
			t.Errorf("%s is not in the snapshot's CreateEndpointRequest", p)
		}
	}
}

func TestProvision_CreateBodyValid(t *testing.T) {
	t.Parallel()
	d := loadSnapshot(t)
	s := desired(t, desiredCfg(), 0)
	body := bodyOf(t, s)
	if errs := d.validate(d.at("components", "schemas", "CreateEndpointRequest").(map[string]any), body, "$"); len(errs) > 0 {
		t.Fatalf("body invalid against the snapshot: %v\n%v", errs, body)
	}
	if body["type"] != "QUEUE" || body["flashboot"] != "FLASHBOOT" || body["name"] != "mm-yue2" {
		t.Errorf("body %v", body)
	}
	if body["timeout"] != json.Number("900000") {
		t.Errorf("timeout = %v, want milliseconds", body["timeout"])
	}
	sc := body["scaling"].(map[string]any)
	if sc["type"] != "QUEUE_DELAY" || sc["queueDelay"] != json.Number("240") || sc["requestCount"] != nil {
		t.Errorf("scaling = %v", sc)
	}
	w := body["workers"].(map[string]any)
	if w["idleTimeout"] != json.Number("20") || w["min"] != json.Number("0") {
		t.Errorf("workers = %v", w)
	}
	g := body["gpu"].(map[string]any)
	if g["count"] != json.Number("1") || g["minCudaVersion"] != "12.8" {
		t.Errorf("gpu = %v", g)
	}
	if _, bad := g["allowedCudaVersions"]; bad {
		t.Error("allowedCudaVersions is mutually exclusive with minCudaVersion")
	}
	env := body["env"].(map[string]any)
	if env["MODE"] != "serverless" || env["HF_HUB_OFFLINE"] != "1" || env[yue2.Env3BRevision] != yue2.Weights[yue2.WeightsRepo3B] ||
		env[yue2.EnvVAERevision] != yue2.Weights[yue2.WeightsRepoVAE] || env["MM_YUE2_VAE_DIR"] != runpod.ImageVAEDir {
		t.Errorf("env = %v", env)
	}
	// the type is not patchable: UpdateEndpointRequest has no such property, the desired spec never patches it
	if _, ok := env["MM_WORKER_DEADLINE"]; ok {
		t.Error("MM_WORKER_DEADLINE belongs to --timeout-case only")
	}
}

func TestProvision_ExcludedTypeOnlyInsidePools(t *testing.T) {
	t.Parallel()
	both := desired(t, desiredCfg(), 0)
	if !slices.Equal(both.GPU.ExcludedTypes, []string{"gpu-l4"}) || !slices.Equal(both.GPU.Pools, []string{"AMPERE_24", "ADA_24"}) {
		t.Errorf("default gpu = %+v", both.GPU)
	}
	c := desiredCfg()
	c.Pools = []string{"ADA_24"}
	ada := desired(t, c, 0)
	if len(ada.GPU.ExcludedTypes) != 0 {
		t.Errorf("L4 is in AMPERE_24 only; excluding it with just ADA_24 listed would be a 400: %v", ada.GPU.ExcludedTypes)
	}
	raw, _ := json.Marshal(ada)
	if !strings.Contains(string(raw), `"excludedTypes":[]`) {
		t.Errorf("excludedTypes must be sent as an empty list: %s", raw)
	}
	c.Pools = []string{"bad pool"}
	if _, err := runpod.DesiredSpec(c, yue2.Weights, testDigest, 0, testCatalog()); err == nil {
		t.Error("invalid pool accepted")
	}
}

func TestProvision_DefaultInactive(t *testing.T) {
	t.Parallel()
	if s := desired(t, desiredCfg(), 0); s.Workers.Max != 0 {
		t.Errorf("workers.max = %d", s.Workers.Max)
	}
	if s := desired(t, desiredCfg(), 1); s.Workers.Max != 1 {
		t.Errorf("workers.max = %d", s.Workers.Max)
	}
	if _, err := runpod.DesiredSpec(desiredCfg(), yue2.Weights, testDigest, 3, testCatalog()); err == nil {
		t.Error("workers.max 3 accepted")
	}
	// end to end: provisioning without --activate leaves workers.max at 0
	e := newEnv(t)
	e.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" })
	c := provClient(t, e)
	res, err := runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights, Image: testDigest})
	if err != nil || res.Action != "create" || !res.Applied {
		t.Fatalf("res %+v err %v", res, err)
	}
	if got := runpodtest.Num(e.srv.Endpoint()["workers"].(map[string]any)["max"]); got != 0 {
		t.Errorf("created with workers.max = %d", got)
	}
}

func TestProvision_TimeoutCaseEnv(t *testing.T) {
	t.Parallel()
	c := desiredCfg()
	c.TimeoutCase = true
	if got := desired(t, c, 1).Env["MM_WORKER_DEADLINE"]; got != "provider" {
		t.Errorf("MM_WORKER_DEADLINE = %q", got)
	}
	if _, ok := desired(t, desiredCfg(), 1).Env["MM_WORKER_DEADLINE"]; ok {
		t.Error("MM_WORKER_DEADLINE present without --timeout-case")
	}
	// the timeout it writes is the current MM_JOB_MAX_RUNTIME
	c.MaxRuntime = 30 * time.Second
	if got := desired(t, c, 1).Timeout; got != 30000 {
		t.Errorf("timeout = %d ms", got)
	}
}

func TestProvision_VAESource(t *testing.T) {
	t.Parallel()
	c := desiredCfg()
	c.VAESource = "volume:/runpod-volume/vae"
	if got := desired(t, c, 0).Env["MM_YUE2_VAE_DIR"]; got != "/runpod-volume/vae" {
		t.Errorf("dir = %q", got)
	}
	c.VAESource = "cached"
	if got := desired(t, c, 0).Env["MM_YUE2_VAE_DIR"]; got != runpod.CachedModelDir {
		t.Errorf("cached dir = %q", got)
	}
	c.VAESource = "image"
	if got := desired(t, c, 0).Env["MM_YUE2_VAE_DIR"]; got != "/opt/mm/hf-cache/hub" {
		t.Errorf("image dir = %q", got)
	}
	for _, bad := range []string{"volume:relative", "volume:/a/../b", "other", "volume:"} {
		c.VAESource = bad
		if _, err := runpod.DesiredSpec(c, yue2.Weights, testDigest, 0, nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func provClient(t *testing.T, e *env) *runpod.Client {
	t.Helper()
	k := adminKey(t, e.set.Admin)
	co := runpod.ClientOptions{MgmtBase: e.srv.URL, JobsBase: e.srv.URL, Admin: &k, Clock: e.clk, UserAgent: runpod.UserAgentFor("test")}
	co.Keys.EndpointID = ""
	return runpod.NewClient(co)
}

func TestProvision_IdempotentDiff(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" })
	c := provClient(t, e)
	opts := runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights, Image: testDigest}
	res, err := runpod.Provision(bg(), c, opts)
	if err != nil || res.Action != "create" {
		t.Fatalf("first run: %+v %v", res, err)
	}
	patches := e.srv.Calls(runpodtest.OpPatchEndpoint)
	res, err = runpod.Provision(bg(), c, opts)
	if err != nil || res.Action != "none" || len(res.Diff) != 0 {
		t.Fatalf("second run: %+v %v", res, err)
	}
	if e.srv.Calls(runpodtest.OpPatchEndpoint) != patches || e.srv.Calls(runpodtest.OpCreateEndpoint) != 1 {
		t.Errorf("patch calls %d create calls %d", e.srv.Calls(runpodtest.OpPatchEndpoint), e.srv.Calls(runpodtest.OpCreateEndpoint))
	}
	// one drifted field => exactly one PATCH carrying only that top-level field, complete
	e.srv.SetEndpoint(func(ep map[string]any) { ep["workers"].(map[string]any)["max"] = 2 })
	res, err = runpod.Provision(bg(), c, opts)
	if err != nil || res.Action != "patch" || !res.Applied || !slices.Equal(res.Diff, []string{"workers"}) {
		t.Fatalf("third run: %+v %v", res, err)
	}
	last, _ := e.srv.Last(runpodtest.OpPatchEndpoint)
	if last.Key != runpodtest.KindAdmin {
		t.Errorf("patch used key %q", last.Key)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(last.Body, &m); err != nil || len(m) != 1 {
		t.Fatalf("patch body %s", last.Body)
	}
	var w map[string]any
	if err := json.Unmarshal(m["workers"], &w); err != nil || w["min"] != float64(0) || w["max"] != float64(0) || w["idleTimeout"] != float64(20) {
		t.Errorf("workers object is not complete: %s", m["workers"])
	}
	// and it is idempotent again
	if res, err = runpod.Provision(bg(), c, opts); err != nil || res.Action != "none" {
		t.Fatalf("fourth run: %+v %v", res, err)
	}
	// without --image an existing endpoint's image is left alone
	e.srv.SetEndpoint(func(ep map[string]any) { ep["image"] = "ghcr.io/example/other@sha256:" + strings.Repeat("b", 64) })
	opts.Image = ""
	if res, err = runpod.Provision(bg(), c, opts); err != nil || res.Action != "none" {
		t.Fatalf("no-image run: %+v %v", res, err)
	}
}

func TestProvision_ActivateThenDeactivate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := provClient(t, e)
	opts := runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights, Image: testDigest, MaxWorkers: 1}
	// the server default endpoint has another image and no env: the first run patches several fields
	res, err := runpod.Provision(bg(), c, opts)
	if err != nil || res.Action != "patch" || !res.Applied {
		t.Fatalf("activate: %+v %v", res, err)
	}
	if got := runpodtest.Num(e.srv.Endpoint()["workers"].(map[string]any)["max"]); got != 1 {
		t.Errorf("workers.max = %d", got)
	}
	opts.MaxWorkers = 0 // --deactivate
	if res, err = runpod.Provision(bg(), c, opts); err != nil || !slices.Equal(res.Diff, []string{"workers"}) {
		t.Fatalf("deactivate: %+v %v", res, err)
	}
	if got := runpodtest.Num(e.srv.Endpoint()["workers"].(map[string]any)["max"]); got != 0 {
		t.Errorf("workers.max = %d", got)
	}
}

func TestProvision_RefusesTagImage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := provClient(t, e)
	for _, img := range []string{"ghcr.io/example/worker:latest", "ghcr.io/example/worker", "ghcr.io/example/worker@sha256:abc", "worker@sha256:" + strings.Repeat("A", 64)} {
		_, err := runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights, Image: img})
		if !errors.Is(err, runpod.ErrTagImage) {
			t.Errorf("%q: err = %v", img, err)
		}
	}
	if n := len(e.srv.Requests()); n != 0 {
		t.Errorf("%d requests reached the server for a refused image", n)
	}
	e.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" })
	if _, err := runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights}); !errors.Is(err, runpod.ErrImageRequired) {
		t.Errorf("create without an image: %v", err)
	}
	if e.srv.Calls(runpodtest.OpCreateEndpoint) != 0 {
		t.Error("an endpoint was created without an image")
	}
}

func TestProvision_DryRunRedacted(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := provClient(t, e)
	res, err := runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights, Image: testDigest, DryRun: true})
	if err != nil || res.Action != "patch" || res.Applied || len(res.Diff) == 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if e.srv.Calls(runpodtest.OpPatchEndpoint)+e.srv.Calls(runpodtest.OpCreateEndpoint) != 0 {
		t.Error("a dry run wrote")
	}
	out, _ := json.Marshal(res)
	for _, secret := range []string{e.epRaw, e.set.Admin, e.set.Read, e.set.Jobs} {
		if strings.Contains(string(out), secret) {
			t.Errorf("the dry-run output contains %q", secret[:10])
		}
	}
	// a create dry run
	e.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" })
	res, err = runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights, Image: testDigest, DryRun: true})
	if err != nil || res.Action != "create" || res.Applied || e.srv.Calls(runpodtest.OpCreateEndpoint) != 0 {
		t.Fatalf("create dry run: %+v %v", res, err)
	}
}

func TestProvision_ConsoleStepPrinted(t *testing.T) {
	t.Parallel()
	d := loadSnapshot(t)
	create := d.deref(d.at("components", "schemas", "CreateEndpointRequest"))
	for name := range create["properties"].(map[string]any) {
		if n := strings.ToLower(name); strings.Contains(n, "model") || strings.Contains(n, "cache") {
			t.Fatalf("the snapshot gained a cached-model field %q: provision can set it itself now", name)
		}
	}
	e := newEnv(t)
	c := provClient(t, e)
	res, err := runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights, Image: testDigest, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	// default (image): only the single cached 3B model; the VAE is baked into the image
	joined := strings.Join(res.ConsoleSteps, "\n")
	for _, want := range []string{yue2.WeightsRepo3B, yue2.Weights[yue2.WeightsRepo3B], "console"} {
		if !strings.Contains(joined, want) {
			t.Errorf("console steps lack %q:\n%s", want, joined)
		}
	}
	if len(res.ConsoleSteps) != 1 || strings.Contains(joined, yue2.WeightsRepoVAE) {
		t.Errorf("image VAE source must print one console step (the 3B model):\n%s", joined)
	}
	// cached: a second step for the VAE, with the one-cached-model caveat
	cfg := desiredCfg()
	cfg.VAESource = "cached"
	res, err = runpod.Provision(bg(), c, runpod.ProvisionOptions{Desired: cfg, Weights: yue2.Weights, Image: testDigest, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(res.ConsoleSteps, "\n")
	if len(res.ConsoleSteps) != 2 || !strings.Contains(joined, yue2.WeightsRepoVAE) || !strings.Contains(joined, "one cached model") {
		t.Errorf("cached VAE source steps:\n%s", joined)
	}
}
