package runpod_test

import (
	"errors"
	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
)

// Every client operation turns a 200 answer that is not the documented JSON into a Decode error, never a panic
// or a zero value: the error branches after each decode.
func TestClient_UndecodableAnswersAreDecodeErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) {
		o.client = func(co *runpod.ClientOptions) {
			a := adminKey(t, canary("admin"))
			co.Admin = &a
		}
	})
	calls := []struct {
		op  string
		run func() error
	}{
		{runpodtest.OpRun, func() error { _, err := e.cl.Run(bg(), []byte(`{}`), runpod.Policy{ExecutionTimeoutMS: 1}); return err }},
		{runpodtest.OpStatus, func() error { _, err := e.cl.Status(bg(), "j"); return err }},
		{runpodtest.OpHealth, func() error { _, err := e.cl.Health(bg()); return err }},
		{runpodtest.OpGetEndpoint, func() error { _, err := e.cl.GetEndpoint(bg(), runpod.KeyRead); return err }},
		{runpodtest.OpPatchEndpoint, func() error {
			_, err := e.cl.PatchEndpoint(bg(), runpod.KeyWrite, runpod.EndpointPatch{})
			return err
		}},
		{runpodtest.OpListEndpoints, func() error { _, err := e.cl.ListEndpoints(bg(), runpod.KeyRead); return err }},
		{runpodtest.OpCreateEndpoint, func() error {
			_, err := e.cl.CreateEndpoint(bg(), runpod.EndpointSpec{Name: "x", Type: "QUEUE"})
			return err
		}},
		{runpodtest.OpWorkers, func() error { _, err := e.cl.Workers(bg()); return err }},
		{runpodtest.OpCatalog, func() error { _, err := e.cl.CatalogGPUs(bg(), runpod.KeyRead); return err }},
		{runpodtest.OpBilling, func() error {
			_, err := e.cl.BillingServerless(bg(), runpod.BillingQuery{Start: t0, End: t0.Add(time.Hour)})
			return err
		}},
		{runpodtest.OpGraphQL, func() error { _, _, err := e.cl.Balance(bg()); return err }},
	}
	for _, c := range calls {
		e.srv.Fault(c.op, runpodtest.Fault{Status: 200, Body: `nope`})
		var re *runpod.Error
		if err := c.run(); !errors.As(err, &re) || re.Code != runpod.Decode {
			t.Errorf("%s: err = %v, want a Decode error", c.op, err)
		}
	}
}

// A live spec that lost the pinned facts is reported fact by fact, and an unparsable one is an error.
func TestCompareSchema_DegradedLiveSpec(t *testing.T) {
	t.Parallel()
	snap := runpod.Snapshot()
	if _, err := runpod.CompareSchema([]byte(`nope`), snap); err == nil {
		t.Error("an unparsable snapshot must be an error")
	}
	if _, err := runpod.CompareSchema(snap, []byte(`nope`)); err == nil {
		t.Error("an unparsable live spec must be an error")
	}
	r, err := runpod.CompareSchema(snap, []byte(`{}`))
	if err != nil || r.OK || !r.Drift {
		t.Fatalf("an empty live spec = %+v %v", r, err)
	}
	for _, want := range []string{"operation missing: ", "fact: gpu.minCudaVersion", "fact: scaling union has no queueDelay",
		"fact: scaling union has no requestCount", "fact: timeout", "fact: flashboot value OFF", "fact: CreateEndpointRequest required fields changed"} {
		if !hasPrefixed(r.Failures, want) {
			t.Errorf("no failure %q in %q", want, r.Failures)
		}
	}
	// the facts that read a description are checked against its words
	live := string(snap)
	for _, c := range []struct{ from, fact string }{
		{"allowedCudaVersions", "fact: gpu.minCudaVersion is no longer documented"},
		{"milliseconds", "fact: timeout is no longer documented in milliseconds"},
		{"PRIORITY_FLASHBOOT", "fact: flashboot value PRIORITY_FLASHBOOT"},
	} {
		if !strings.Contains(live, c.from) {
			t.Fatalf("snapshot has no %q", c.from)
		}
		r, err := runpod.CompareSchema(snap, []byte(strings.ReplaceAll(live, c.from, "renamed_word")))
		if err != nil || !hasPrefixed(r.Failures, c.fact) {
			t.Errorf("%s: failures %q err %v", c.from, r.Failures, err)
		}
	}
}

func hasPrefixed(ss []string, p string) bool {
	for _, s := range ss {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func TestValidateCapabilities_RefusesMalformedFiles(t *testing.T) {
	t.Parallel()
	const fmtOK = `"format":"mm.runpod-probe/2"`
	rec := `"recommendation":{"fallback":"manual-max-0","vae":"image"}`
	cases := []struct{ name, doc, want string }{
		{"not json", `nope`, "key-capabilities.json"},
		{"unknown field", `{` + fmtOK + `,"probed":false,"zzz":1}`, "key-capabilities.json"},
		{"unknown format", `{"format":"other","probed":false}`, "unknown format"},
		{"unprobed carries keys", `{` + fmtOK + `,"probed":false,"keys":{}}`, "unprobed file carries"},
		{"probed lacks results", `{` + fmtOK + `,"probed":true}`, "lacks key results"},
		{"bad time", `{` + fmtOK + `,"probed":true,"probed_at":"yesterday","keys":{}}`, "not RFC 3339"},
		{"unknown kind", `{` + fmtOK + `,"probed":true,"probed_at":"2026-10-14T12:00:00Z","keys":{"root":{}},` + rec + `}`, "unknown key kind"},
		{"unknown verdict", `{` + fmtOK + `,"probed":true,"probed_at":"2026-10-14T12:00:00Z","keys":{"jobs":{"run":{"verdict":"maybe"}}},` + rec + `}`, "unknown verdict"},
		{"bad fallback", `{` + fmtOK + `,"probed":true,"probed_at":"2026-10-14T12:00:00Z","keys":{},"recommendation":{"fallback":"x","vae":"image"}}`, "recommendation.fallback"},
		{"bad vae", `{` + fmtOK + `,"probed":true,"probed_at":"2026-10-14T12:00:00Z","keys":{},"recommendation":{"fallback":"patch","vae":"x"}}`, "recommendation.vae"},
	}
	for _, c := range cases {
		if err := runpod.ValidateCapabilities([]byte(c.doc)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	ok := `{` + fmtOK + `,"probed":true,"probed_at":"2026-10-14T12:00:00Z","keys":{"jobs":{"run":{"verdict":"allowed"}}},` + rec + `}`
	if err := runpod.ValidateCapabilities([]byte(ok)); err != nil {
		t.Errorf("a well-formed probed file: %v", err)
	}
}

func TestDesiredSpec_RefusesIncompleteInput(t *testing.T) {
	t.Parallel()
	good := desiredCfg()
	zeroCold := good
	zeroCold.ColdStartS = 0
	cases := []struct {
		name    string
		cfg     runpod.DesiredConfig
		weights map[string]string
		image   string
		want    string
	}{
		{"no cold start", zeroCold, yue2.Weights, "", "must be positive"},
		{"image by tag", good, yue2.Weights, "ghcr.io/example/worker:latest", ""},
		{"weights not pinned", good, map[string]string{}, "", "not pinned"},
	}
	for _, c := range cases {
		_, err := runpod.DesiredSpec(c.cfg, c.weights, c.image, 1, testCatalog())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

// Provision names the step that failed, and changes nothing after a failed read.
func TestProvision_ReadAndWriteFailuresAreNamed(t *testing.T) {
	t.Parallel()
	opts := runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights, Image: testDigest}
	fail := runpodtest.Fault{Status: 401}
	cases := []struct {
		name  string
		setup func(*env, *runpod.ProvisionOptions)
		fault string
		want  string
	}{
		{"image by tag", func(_ *env, o *runpod.ProvisionOptions) { o.Image = "ghcr.io/example/worker:latest" }, "", "digest"},
		{"catalog", nil, runpodtest.OpCatalog, "reading the GPU catalog"},
		{"list", nil, runpodtest.OpListEndpoints, "listing endpoints"},
		{"create", func(e *env, _ *runpod.ProvisionOptions) {
			e.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" })
		}, runpodtest.OpCreateEndpoint, "creating the endpoint"},
		{"patch", func(e *env, _ *runpod.ProvisionOptions) {
			e.srv.SetEndpoint(func(ep map[string]any) { ep["workers"].(map[string]any)["max"] = 2 })
			runpod.Provision(bg(), provClient(t, e), runpod.ProvisionOptions{Desired: desiredCfg(), Weights: yue2.Weights, Image: testDigest, DryRun: true})
		}, runpodtest.OpPatchEndpoint, "patching the endpoint"},
	}
	for _, c := range cases {
		e := newEnv(t)
		o := opts
		if c.setup != nil {
			c.setup(e, &o)
		}
		if c.fault != "" {
			e.srv.Fault(c.fault, fail)
		}
		_, err := runpod.Provision(bg(), provClient(t, e), o)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// A canary against a server that is down, refuses, or answers nonsense never reports a pass.
func TestCanary_BrokenServerIsAnErrorNotAPass(t *testing.T) {
	t.Parallel()
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()
	serve := func(status int, body string) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	for name, base := range map[string]string{
		"unreachable":  downURL,
		"refused":      serve(500, `{"code":"BOOM","message":"nope"}`),
		"undecodable":  serve(200, `nope`),
		"empty refuse": serve(503, ``),
	} {
		o := runpod.CanaryOptions{BaseURL: base, Token: canary("mm-token"), Case: runpod.CaseMain, Day: t0, N: 1,
			WaitS: 1, SweepEvery: time.Millisecond, Clock: clocktest.NewManual(t0)}
		rep, err := runpod.RunCanary(bg(), o)
		if err == nil || rep != nil || !strings.Contains(err.Error(), "canary: ") {
			t.Errorf("%s: rep %+v err %v", name, rep, err)
		}
	}
}
