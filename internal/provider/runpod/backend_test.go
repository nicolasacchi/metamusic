package runpod_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
)

func (e *env) backend(mod ...func(*runpod.BackendOptions)) *runpod.Backend {
	o := runpod.BackendOptions{Keys: e.keys, Clock: e.clk, Version: "test", MgmtBase: e.srv.URL, JobsBase: e.srv.URL,
		MaxRuntime: 15 * time.Minute, ColdStartS: 180}
	for _, m := range mod {
		m(&o)
	}
	return runpod.NewBackend(o)
}

func ep(m map[string]any, k string) map[string]any { return m[k].(map[string]any) }

func uploads() map[string]provider.UploadTarget {
	m := map[string]provider.UploadTarget{}
	for _, a := range provider.Artifacts {
		if a.Required {
			m[a.Name] = provider.UploadTarget{URL: "https://mm.example.com/worker/v1/uploads/" + a.Name + "?exp=1&sig=2", MaxBytes: a.MaxBytes}
		}
	}
	return m
}

func take() provider.TakeSpec {
	return provider.TakeSpec{
		JobID: "01900000-0000-7000-8000-00000000a001", AttemptID: "01900000-0000-7000-8000-00000000a002", Engine: "yue2",
		Request:  json.RawMessage(`{"cot":"full","id":"syn-a-s1102","lyrics":"[Verse]\nla la","seed":1102,"style":"pop, 100 BPM"}`),
		TimeoutS: 186, TTL: 2 * time.Hour, UploadBudgetS: 60,
		Uploads:  uploads(),
		StartURL: "https://mm.example.com/worker/v1/attempts/a/start?exp=1&sig=2",
		Expect:   provider.WorkerExpect{Weights: map[string]string{"m-a-p/YuE2-3B": strings.Repeat("a", 40), "m-a-p/YuE2-Vae": strings.Repeat("b", 40)}},
		Deadline: t0.Add(10 * time.Minute),
	}
}

func TestBackend_QuoteMaxPoolPrice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	q, err := e.backend().Quote(bg())
	if err != nil {
		t.Fatal(err)
	}
	if q.ListPricePPH != 1_100_000 {
		t.Fatalf("ListPricePPH = %d", q.ListPricePPH)
	}
	if strings.Join(q.GPUClasses, ",") != "RTX 4090,RTX A5000" || strings.Join(q.Pools, ",") != "AMPERE_24,ADA_24" {
		t.Fatalf("classes %v pools %v", q.GPUClasses, q.Pools)
	}
	if q.Backend != provider.BackendRunPod || !q.ObservedAt.Equal(t0) {
		t.Fatalf("quote = %+v", q)
	}
	// an excluded type does not count, even when it is the dearest
	e.srv.SetCatalog([]map[string]any{
		{"id": "gpu-a5000", "name": "RTX A5000", "pool": "AMPERE_24", "price": map[string]any{"serverless": json.Number("0.69")}},
		{"id": "gpu-l4", "name": "L4", "pool": "AMPERE_24", "price": map[string]any{"serverless": json.Number("9.99")}},
		{"id": "gpu-4090", "name": "RTX 4090", "pool": "ADA_24", "price": map[string]any{"serverless": json.Number("1.10")}},
	})
	q, err = e.backend().Quote(bg())
	if err != nil || q.ListPricePPH != 1_100_000 {
		t.Fatalf("with an expensive excluded type: %d %v", q.ListPricePPH, err)
	}
	// the cached quote never holds the endpoint id
	b, _ := json.Marshal(q)
	if strings.Contains(string(b), e.epRaw) {
		t.Fatal("quote JSON contains the endpoint id")
	}
	if !strings.Contains(q.Endpoint.RequestURLs["run"], "<endpoint>") {
		t.Fatalf("requestUrls = %v", q.Endpoint.RequestURLs)
	}
	if q.Endpoint.ScalerValue != "240" || q.Endpoint.IdleTimeoutS == nil || *q.Endpoint.IdleTimeoutS != 20 ||
		q.Endpoint.TimeoutMS != 900000 || !strings.HasPrefix(q.Endpoint.ImageDigest, "sha256:") {
		t.Fatalf("endpoint = %+v", q.Endpoint)
	}
}

func TestBackend_QuoteCatalogCachedForAnHour(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.backend()
	for i := 0; i < 3; i++ {
		if _, err := b.Quote(bg()); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.srv.Calls(runpodtest.OpCatalog); n != 1 {
		t.Fatalf("catalog fetched %d times", n)
	}
	e.clk.Advance(61 * time.Minute)
	if _, err := b.Quote(bg()); err != nil {
		t.Fatal(err)
	}
	if n := e.srv.Calls(runpodtest.OpCatalog); n != 2 {
		t.Fatalf("catalog fetched %d times after an hour", n)
	}
}

func TestBackend_QuoteFailsClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(e *env)
		later  time.Duration
		want   string
	}{
		{name: "baseline", want: ""},
		{name: "idleTimeout missing", mutate: func(e *env) { e.srv.SetEndpoint(func(m map[string]any) { delete(ep(m, "workers"), "idleTimeout") }) }, want: "endpoint_config"},
		{name: "scaler request count", mutate: func(e *env) {
			e.srv.SetEndpoint(func(m map[string]any) { m["scaling"] = map[string]any{"type": "REQUEST_COUNT", "requestCount": 4} })
		}, want: "endpoint_config"},
		{name: "queueDelay differs", mutate: func(e *env) {
			e.srv.SetEndpoint(func(m map[string]any) { ep(m, "scaling")["queueDelay"] = 30 })
		}, want: "endpoint_config"},
		{name: "workers.max 3", mutate: func(e *env) { e.srv.SetEndpoint(func(m map[string]any) { ep(m, "workers")["max"] = 3 }) }, want: "endpoint_config"},
		{name: "gpu.count 2", mutate: func(e *env) { e.srv.SetEndpoint(func(m map[string]any) { ep(m, "gpu")["count"] = 2 }) }, want: "endpoint_config"},
		{name: "wrong timeout", mutate: func(e *env) { e.srv.SetEndpoint(func(m map[string]any) { m["timeout"] = 600000 }) }, want: "endpoint_config"},
		{name: "foreign requestUrls host", mutate: func(e *env) {
			e.srv.SetEndpoint(func(m map[string]any) { ep(m, "requestUrls")["run"] = "https://evil.example.net/v2/x/run" })
		}, want: "endpoint_config"},
		{name: "inactive", mutate: func(e *env) { e.srv.SetEndpoint(func(m map[string]any) { ep(m, "workers")["max"] = 0 }) }, want: "endpoint_inactive"},
		{name: "price missing", mutate: func(e *env) {
			e.srv.SetCatalog([]map[string]any{{"id": "gpu-a5000", "name": "A", "pool": "AMPERE_24", "price": map[string]any{"secure": json.Number("0.3")}},
				{"id": "gpu-4090", "name": "B", "pool": "ADA_24", "price": map[string]any{"serverless": json.Number("1.1")}}})
		}, want: "catalog_unreadable"},
		{name: "price zero", mutate: func(e *env) {
			e.srv.SetCatalog([]map[string]any{{"id": "gpu-a5000", "name": "A", "pool": "AMPERE_24", "price": map[string]any{"serverless": json.Number("0")}}})
		}, want: "catalog_unreadable"},
		{name: "no allowed type", mutate: func(e *env) { e.srv.SetCatalog(nil) }, want: "catalog_unreadable"},
		{name: "catalog forbidden", mutate: func(e *env) {
			a := runpodtest.ReadOnly()
			delete(a, runpodtest.OpCatalog)
			e.srv.SetAllow(runpodtest.KindRead, a)
		}, want: "catalog_unreadable"},
		{name: "stale", later: 61 * time.Minute, want: "quote_stale"},
		{name: "workers unreadable", mutate: func(e *env) {
			a := runpodtest.ReadOnly()
			delete(a, runpodtest.OpWorkers)
			e.srv.SetAllow(runpodtest.KindRead, a)
		}, want: "sweep_unreadable"},
		{name: "endpoint unreadable", mutate: func(e *env) {
			a := runpodtest.ReadOnly()
			delete(a, runpodtest.OpGetEndpoint)
			e.srv.SetAllow(runpodtest.KindRead, a)
		}, want: "sweep_unreadable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			if c.mutate != nil {
				c.mutate(e)
			}
			b := e.backend()
			q, err := b.Quote(bg())
			if err != nil {
				t.Fatal(err)
			}
			if got := b.QuoteCheck(q, t0.Add(c.later)); got != c.want {
				t.Fatalf("reason = %q, want %q", got, c.want)
			}
		})
	}
}

func TestBackend_QuoteErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.Fault(runpodtest.OpGetEndpoint, runpodtest.Fault{Status: 403, Body: "error code: 1010"})
	if _, err := e.backend().Quote(bg()); !errors.Is(err, provider.ErrAccess) {
		t.Fatalf("1010 => %v", err)
	}
	e.srv.Fault(runpodtest.OpGetEndpoint, runpodtest.Fault{Status: 404})
	if _, err := e.backend().Quote(bg()); !errors.Is(err, provider.ErrEndpointNotFound) {
		t.Fatalf("404 => %v", err)
	}
}

func TestBackend_SubmitBody(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ref, err := e.backend().Submit(bg(), take())
	if err != nil || ref.Backend != provider.BackendRunPod || ref.ID == "" {
		t.Fatalf("ref = %+v %v", ref, err)
	}
	r, _ := e.srv.Last(runpodtest.OpRun)
	var body struct {
		Input  json.RawMessage `json:"input"`
		Policy struct {
			ExecutionTimeout int64 `json:"executionTimeout"`
			TTL              int64 `json:"ttl"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Policy.ExecutionTimeout != 186_000 || body.Policy.TTL != 7_200_000 {
		t.Fatalf("policy = %+v", body.Policy)
	}
	in, err := provider.ParseWorkerInput(body.Input)
	if err != nil {
		t.Fatal(err)
	}
	if in.TimeoutS != 186 || in.UploadBudgetS != 60 || in.Start.URL == "" || in.DeadlineUnix != t0.Add(10*time.Minute).Unix() ||
		in.Engine != "yue2" || len(in.Uploads) != len(uploads()) || in.Expect.Weights["m-a-p/YuE2-3B"] == "" {
		t.Fatalf("input = %+v", in)
	}
	bad := take()
	bad.TimeoutS = 0
	if _, err := e.backend().Submit(bg(), bad); err == nil {
		t.Fatal("zero timeout accepted")
	}
	bad = take()
	bad.StartURL = "not a url"
	if _, err := e.backend().Submit(bg(), bad); err == nil {
		t.Fatal("invalid start url accepted")
	}
	if e.srv.Calls(runpodtest.OpRun) != 1 {
		t.Fatalf("/run calls = %d", e.srv.Calls(runpodtest.OpRun))
	}
}

func TestBackend_SubmitBodyWithinEstimate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tk := take()
	abc := strings.Repeat("A", 1<<20)
	tk.Request, _ = json.Marshal(map[string]any{"cot": "full", "id": "syn-a-s1", "lyrics": "[Verse]\nla", "seed": 1, "style": "pop", "abc": abc})
	if _, err := e.backend().Submit(bg(), tk); err != nil {
		t.Fatal(err)
	}
	r, _ := e.srv.Last(runpodtest.OpRun)
	if len(r.Body) >= provider.MaxRunBody {
		t.Fatalf("body %d >= %d", len(r.Body), provider.MaxRunBody)
	}
	tk.Request, _ = json.Marshal(map[string]any{"cot": "full", "id": "x", "lyrics": "l", "seed": 1, "style": "s", "abc": strings.Repeat("A", provider.MaxRunBody)})
	n := e.srv.Calls(runpodtest.OpRun)
	if _, err := e.backend().Submit(bg(), tk); !errors.Is(err, runpod.ErrRequestTooLarge) {
		t.Fatalf("oversized submit = %v", err)
	}
	if e.srv.Calls(runpodtest.OpRun) != n {
		t.Fatal("oversized body reached the server")
	}
}

func TestBackend_StatusMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw   string
		state provider.RemoteState
	}{
		{"IN_QUEUE", provider.Queued}, {"IN_PROGRESS", provider.Running}, {"COMPLETED", provider.Completed}, {"FAILED", provider.Failed},
		{"CANCELLED", provider.Cancelled}, {"TIMED_OUT", provider.TimedOut}, {"RETRYING", provider.Unknown},
	}
	e := newEnv(t)
	b := e.backend()
	for _, c := range cases {
		e.srv.Fault(runpodtest.OpStatus, runpodtest.Fault{Status: 200, Body: fmt.Sprintf(
			`{"id":"j","status":%q,"delayTime":800,"executionTime":1500.7,"workerId":"w1","output":{"a":1},"error":"x https://h.example/p?sig=1"}`, c.raw)})
		rs, err := b.Poll(bg(), provider.Ref{Backend: provider.BackendRunPod, ID: "j"})
		if err != nil || rs.State != c.state || rs.Raw != c.raw {
			t.Fatalf("%s => %+v %v", c.raw, rs, err)
		}
		if rs.ExecutionMS == nil || *rs.ExecutionMS != 1500 || rs.DelayMS == nil || *rs.DelayMS != 800 || rs.WorkerID != "w1" || string(rs.Output) != `{"a":1}` {
			t.Fatalf("%s record = %+v", c.raw, rs)
		}
		if strings.Contains(rs.ErrorText, "sig=1") {
			t.Fatalf("ErrorText = %q", rs.ErrorText)
		}
	}
	// absent executionTime stays nil
	e.srv.Fault(runpodtest.OpStatus, runpodtest.Fault{Status: 200, Body: `{"id":"j","status":"COMPLETED"}`})
	rs, _ := b.Poll(bg(), provider.Ref{ID: "j"})
	if rs.ExecutionMS != nil || rs.DelayMS != nil {
		t.Fatalf("absent fields = %+v", rs)
	}
	// oversized output is dropped
	e.srv.Fault(runpodtest.OpStatus, runpodtest.Fault{Status: 200, Body: `{"id":"j","status":"COMPLETED","output":{"x":"` + strings.Repeat("a", 70<<10) + `"}}`})
	rs, _ = b.Poll(bg(), provider.Ref{ID: "j"})
	if rs.Output != nil || rs.ErrorText == "" {
		t.Fatalf("oversized output kept: %d bytes", len(rs.Output))
	}
	// 404 + health 2xx => the job is gone
	rs, err := b.Poll(bg(), provider.Ref{ID: "no-such-job"})
	if err != nil || rs.State != provider.NotFound {
		t.Fatalf("404 => %+v %v", rs, err)
	}
	// 404 + health 404 => the endpoint is gone
	e.srv.Fault(runpodtest.OpHealth, runpodtest.Fault{Status: 404})
	if _, err := b.Poll(bg(), provider.Ref{ID: "no-such-job"}); !errors.Is(err, provider.ErrEndpointNotFound) {
		t.Fatalf("endpoint 404 => %v", err)
	}
	// 404 + health failing otherwise => a plain error, never NotFound
	e.srv.Fault(runpodtest.OpHealth, runpodtest.Fault{Status: 401})
	if rs, err := b.Poll(bg(), provider.Ref{ID: "no-such-job"}); !errors.Is(err, provider.ErrAccess) || rs.State == provider.NotFound {
		t.Fatalf("health 401 => %+v %v", rs, err)
	}
}

func TestBackend_CancelPostOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.backend()
	ref, _ := b.Submit(bg(), take())
	if err := b.Cancel(bg(), ref); err != nil {
		t.Fatal(err)
	}
	if n := e.srv.Calls(runpodtest.OpCancel); n != 1 || e.srv.Calls(runpodtest.OpStatus) != 0 {
		t.Fatalf("cancel calls %d, status calls %d", n, e.srv.Calls(runpodtest.OpStatus))
	}
	if e.srv.JobState(ref.ID) != "CANCELLED" {
		t.Fatalf("state = %s", e.srv.JobState(ref.ID))
	}
	// unknown job: already gone
	if err := b.Cancel(bg(), provider.Ref{ID: "nope"}); err != nil {
		t.Fatalf("cancel of an unknown job = %v", err)
	}
	e.srv.Fault(runpodtest.OpCancel, runpodtest.Fault{Status: 503, Times: -1})
	before := e.srv.Calls(runpodtest.OpCancel)
	if err := b.Cancel(bg(), ref); err == nil {
		t.Fatal("503 swallowed")
	}
	if e.srv.Calls(runpodtest.OpCancel) != before+1 {
		t.Fatal("cancel retried")
	}
}

func TestBackend_PatchCompleteNestedObjects(t *testing.T) {
	t.Parallel()
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprintf("replaces=%v", replace), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, func(o *envOpts) { o.srv.PatchReplacesNested = replace })
			b := e.backend()
			if err := b.ScaleToZero(bg()); err != nil {
				t.Fatal(err)
			}
			r, _ := e.srv.Last(runpodtest.OpPatchEndpoint)
			var sent map[string]map[string]any
			if err := json.Unmarshal(r.Body, &sent); err != nil {
				t.Fatal(err)
			}
			for _, need := range [][2]string{{"workers", "min"}, {"workers", "max"}, {"workers", "idleTimeout"}, {"gpu", "pools"}, {"gpu", "excludedTypes"},
				{"gpu", "count"}, {"gpu", "minCudaVersion"}, {"scaling", "type"}, {"scaling", "queueDelay"}} {
				if _, ok := sent[need[0]][need[1]]; !ok {
					t.Errorf("PATCH lacks %s.%s: %s", need[0], need[1], r.Body)
				}
			}
			if _, ok := sent["gpu"]["allowedCudaVersions"]; ok {
				t.Error("allowedCudaVersions sent")
			}
			got := e.srv.Endpoint()
			if runpodtest.Num(ep(got, "workers")["max"]) != 0 || runpodtest.Num(ep(got, "workers")["idleTimeout"]) != 20 ||
				runpodtest.Num(ep(got, "scaling")["queueDelay"]) != 240 || len(ep(got, "gpu")["excludedTypes"].([]any)) != 1 || ep(got, "gpu")["minCudaVersion"] != "12.8" {
				t.Fatalf("endpoint after ScaleToZero = %v", got)
			}
			if r.Key != runpodtest.KindWrite {
				t.Fatalf("PATCH key %s", r.Key)
			}
			// drift: revert to the wanted values
			e.srv.SetEndpoint(func(m map[string]any) { ep(m, "workers")["max"] = 2; ep(m, "workers")["idleTimeout"] = 120 })
			rep, err := b.Sweep(bg())
			if err != nil || len(rep.Drift) != 1 || rep.Drift[0].Field != "workers.idleTimeout" {
				t.Fatalf("sweep = %+v %v", rep, err)
			}
			if err := b.RevertDrift(bg(), rep.Drift); err != nil {
				t.Fatal(err)
			}
			got = e.srv.Endpoint()
			if runpodtest.Num(ep(got, "workers")["idleTimeout"]) != 20 || runpodtest.Num(ep(got, "workers")["max"]) != 2 || len(ep(got, "gpu")["pools"].([]any)) != 2 {
				t.Fatalf("endpoint after RevertDrift = %v", got)
			}
		})
	}
}

func TestBackend_PatchVerifiedByGET(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// the server acknowledges the PATCH but does not apply it
	e.srv.Fault(runpodtest.OpPatchEndpoint, runpodtest.Fault{Status: 200, Body: `{"id":"x"}`})
	err := e.backend().ScaleToZero(bg())
	if !errors.Is(err, runpod.ErrPatchMismatch) || !strings.Contains(err.Error(), "workers.max") {
		t.Fatalf("err = %v", err)
	}
	if e.srv.Calls(runpodtest.OpGetEndpoint) != 2 {
		t.Fatalf("GETs = %d", e.srv.Calls(runpodtest.OpGetEndpoint))
	}
	// unsupported drift fields are named
	err = e.backend().RevertDrift(bg(), []provider.Drift{{Field: "requestUrls", Want: "api.runpod.ai"}})
	if err == nil || !strings.Contains(err.Error(), "requestUrls") {
		t.Fatalf("err = %v", err)
	}
	if err := e.backend().RevertDrift(bg(), []provider.Drift{{Field: "workers.max", Want: "x"}}); err == nil {
		t.Fatal("non-numeric want accepted")
	}
}

func TestBackend_PatchPermissions(t *testing.T) {
	t.Parallel()
	forbidden := newEnv(t, func(o *envOpts) { o.srv.PatchForbidden = true })
	if err := forbidden.backend().ScaleToZero(bg()); !errors.Is(err, provider.ErrNotPermitted) {
		t.Fatalf("403 on PATCH = %v", err)
	}
	nokey := newEnv(t, func(o *envOpts) { o.noWrite = true })
	b := nokey.backend()
	if err := b.ScaleToZero(bg()); !errors.Is(err, provider.ErrNotPermitted) {
		t.Fatalf("no write key = %v", err)
	}
	if err := b.RevertDrift(bg(), nil); !errors.Is(err, provider.ErrNotPermitted) {
		t.Fatalf("no write key revert = %v", err)
	}
	if nokey.srv.Calls(runpodtest.OpPatchEndpoint) != 0 {
		t.Fatal("PATCH sent without a write key")
	}
}

func TestBackend_EndpointTimeoutEqualsMaxRuntime(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.SetEndpoint(func(m map[string]any) { m["timeout"] = 600000 })
	b := e.backend()
	rep, err := b.Sweep(bg())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range rep.Drift {
		if d.Field == "timeout" && d.Want == "900000" && d.Got == "600000" {
			found = true
		}
	}
	if !found {
		t.Fatalf("drift = %+v", rep.Drift)
	}
	q, _ := b.Quote(bg())
	if r := b.QuoteCheck(q, t0); r != "endpoint_config" {
		t.Fatalf("reason = %q", r)
	}
	// a different configured runtime flips both
	e2 := newEnv(t)
	b2 := e2.backend(func(o *runpod.BackendOptions) { o.MaxRuntime = 30 * time.Minute })
	q2, _ := b2.Quote(bg())
	if r := b2.QuoteCheck(q2, t0); r != "endpoint_config" {
		t.Fatalf("30m runtime vs 15m endpoint = %q", r)
	}
}

func TestBackend_SweepReport(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) { o.srv.SkewOffset = 3 * time.Second })
	e.srv.SetWorkers([]map[string]any{{"id": "w1", "status": "RUNNING", "isStale": true, "image": "img", "startedAt": "2026-10-14T11:59:00Z", "uptimeSeconds": 60},
		{"id": "w2", "status": "INITIALIZING", "startedAt": nil, "uptimeSeconds": nil}})
	d := &runpod.EndpointSpec{Image: "ghcr.io/example/worker@sha256:" + strings.Repeat("1", 64),
		GPU: runpod.GPUSpec{Pools: []string{"AMPERE_24"}, ExcludedTypes: []string{"gpu-l4"}}, Env: map[string]string{"MM_YUE2_3B_REVISION": "abc"}}
	e.srv.SetEndpoint(func(m map[string]any) { m["env"] = map[string]any{"MM_WORKER_DEADLINE": "1"} })
	rep, err := e.backend(func(o *runpod.BackendOptions) { o.Desired = d }).Sweep(bg())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Workers) != 2 || !rep.Workers[0].IsStale || rep.Workers[0].UptimeS == nil || rep.Workers[1].UptimeS != nil || rep.Workers[1].StartedAt != nil {
		t.Fatalf("workers = %+v", rep.Workers)
	}
	if rep.Skew == nil || *rep.Skew != 3*time.Second || rep.Endpoint == nil || rep.ObservedAt != t0 {
		t.Fatalf("report = %+v", rep)
	}
	fields := map[string]bool{}
	for _, x := range rep.Drift {
		fields[x.Field] = true
	}
	for _, want := range []string{"image", "gpu.selection", "env.MM_YUE2_3B_REVISION", "env.MM_WORKER_DEADLINE"} {
		if !fields[want] {
			t.Errorf("drift lacks %s: %+v", want, rep.Drift)
		}
	}
}

func TestBackend_DriftFieldList(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.SetEndpoint(func(m map[string]any) {
		m["type"] = "LOAD_BALANCER"
		m["scaling"] = map[string]any{"type": "REQUEST_COUNT", "requestCount": 2}
		m["timeout"] = 1000
		m["flashboot"] = "OFF"
		ep(m, "workers")["min"] = 1
		ep(m, "workers")["max"] = 5
		delete(ep(m, "workers"), "idleTimeout")
		ep(m, "gpu")["count"] = 2
		ep(m, "gpu")["minCudaVersion"] = "11.0"
		ep(m, "requestUrls")["run"] = "https://elsewhere.example/v2/x/run"
	})
	rep, err := e.backend().Sweep(bg())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range rep.Drift {
		got[d.Field] = true
	}
	for _, want := range []string{"type", "scaling.type", "timeout", "flashboot", "workers.min", "workers.max", "workers.idleTimeout", "gpu.count", "gpu.minCudaVersion", "requestUrls"} {
		if !got[want] {
			t.Errorf("missing drift field %s (%v)", want, rep.Drift)
		}
	}
	// a long idleTimeout and a wrong queueDelay
	e2 := newEnv(t)
	e2.srv.SetEndpoint(func(m map[string]any) { ep(m, "workers")["idleTimeout"] = 300; ep(m, "scaling")["queueDelay"] = 30 })
	rep, _ = e2.backend().Sweep(bg())
	if len(rep.Drift) != 2 {
		t.Fatalf("drift = %+v", rep.Drift)
	}
	clean, _ := newEnv(t).backend().Sweep(bg())
	if len(clean.Drift) != 0 {
		t.Fatalf("clean endpoint drifts: %+v", clean.Drift)
	}
}

func TestBackend_BillingExact(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.SetBilling([]map[string]any{
		{"startTime": "2026-10-14T10:00:00Z", "endTime": "2026-10-14T11:00:00Z", "serverlessId": "x", "totalAmount": json.Number("0.123456789"),
			"gpuAmount": json.Number("0.1"), "cpuAmount": json.Number("0.02"), "diskAmount": json.Number("0.003456789"), "feeAmount": json.Number("0")},
		{"startTime": "2026-10-14T11:00:00Z", "endTime": "2026-10-14T12:00:00Z", "serverlessId": "x", "totalAmount": json.Number("1.5e-3"),
			"gpuAmount": json.Number("1.5e-3"), "cpuAmount": json.Number("0"), "diskAmount": json.Number("0"), "feeAmount": json.Number("0")},
	})
	bs, err := e.backend().Billing(bg(), time.Date(2026, 10, 14, 10, 20, 0, 0, time.UTC), time.Date(2026, 10, 14, 12, 10, 0, 0, time.UTC))
	if err != nil || len(bs) != 2 {
		t.Fatalf("buckets = %+v %v", bs, err)
	}
	if bs[0].Total != 123457 || bs[0].Compute != 120000 || bs[0].Disk != 3457 || bs[1].Total != 1500 || bs[1].Compute != 1500 {
		t.Fatalf("amounts = %+v", bs)
	}
	if !bs[0].Start.Equal(time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)) || !bs[0].End.Equal(time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("bounds = %v %v", bs[0].Start, bs[0].End)
	}
	r, _ := e.srv.Last(runpodtest.OpBilling)
	for _, want := range []string{"bucketSize=hour", "startTime=2026-10-14T10%3A00%3A00Z", "endTime=2026-10-14T13%3A00%3A00Z"} {
		if !strings.Contains(r.Query, want) {
			t.Errorf("query %q lacks %q", r.Query, want)
		}
	}
	// an exactly aligned end is not widened
	_, _ = e.backend().Billing(bg(), t0.Add(-2*time.Hour), t0)
	r, _ = e.srv.Last(runpodtest.OpBilling)
	if !strings.Contains(r.Query, "endTime=2026-10-14T12%3A00%3A00Z") {
		t.Errorf("aligned end widened: %s", r.Query)
	}
	// unreadable billing
	e.srv.SetAllow(runpodtest.KindRead, runpodtest.Allow{runpodtest.OpGetEndpoint: true})
	if _, err := e.backend().Billing(bg(), t0.Add(-time.Hour), t0); !errors.Is(err, provider.ErrNotPermitted) {
		t.Fatalf("403 billing = %v", err)
	}
	// a garbage amount is a decode error
	e2 := newEnv(t)
	e2.srv.Fault(runpodtest.OpBilling, runpodtest.Fault{Status: 200, Body: `[{"startTime":"2026-10-14T10:00:00Z","endTime":"2026-10-14T11:00:00Z","totalAmount":-1,"gpuAmount":0,"cpuAmount":0,"diskAmount":0}]`})
	if _, err := e2.backend().Billing(bg(), t0.Add(-3*time.Hour), t0); err == nil {
		t.Fatal("negative amount accepted")
	}
}

func TestBackend_CapabilitiesFromProbeFile(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	cons := provider.Capabilities{Billing: "none"}
	if got := e.backend().Capabilities(); got != cons {
		t.Fatalf("absent file => %+v", got)
	}
	unprobed := []byte(`{"format":"mm.runpod-probe/2","probed":false}`)
	if got := e.backend(func(o *runpod.BackendOptions) { o.ProbeJSON = unprobed }).Capabilities(); got != cons {
		t.Fatalf("unprobed => %+v", got)
	}
	if got := e.backend(func(o *runpod.BackendOptions) { o.ProbeJSON = []byte("garbage") }).Capabilities(); got != cons {
		t.Fatalf("garbage => %+v", got)
	}
	probed := []byte(`{"format":"mm.runpod-probe/2","probed":true,"billing_readable":true,"recommendation":{"write_key_scoped":true}}`)
	full := provider.Capabilities{ScaleToZero: true, RevertDrift: true, Billing: "hour"}
	if got := e.backend(func(o *runpod.BackendOptions) { o.ProbeJSON = probed }).Capabilities(); got != full {
		t.Fatalf("probed => %+v", got)
	}
	nokey := newEnv(t, func(o *envOpts) { o.noWrite = true })
	if got := nokey.backend(func(o *runpod.BackendOptions) { o.ProbeJSON = probed }).Capabilities(); got.ScaleToZero || got.Billing != "hour" {
		t.Fatalf("no write key => %+v", got)
	}
	notScoped := []byte(`{"probed":true,"billing_readable":false,"recommendation":{"write_key_scoped":false}}`)
	if got := e.backend(func(o *runpod.BackendOptions) { o.ProbeJSON = notScoped }).Capabilities(); got != cons {
		t.Fatalf("not scoped => %+v", got)
	}
}

func TestBackend_PurgeAndName(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.backend()
	if b.Name() != provider.BackendRunPod {
		t.Fatal(b.Name())
	}
	if b.Client() == nil {
		t.Fatal("no client")
	}
	if err := b.PurgeQueue(bg()); err != nil || e.srv.Calls(runpodtest.OpPurge) != 1 {
		t.Fatalf("purge = %v", err)
	}
}

func TestBackend_EndpointIDNeverLogged(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.backend()
	echo := `{"title":"x","detail":"endpoint ` + e.epRaw + ` at https://api.runpod.ai/v2/` + e.epRaw + `/run failed","errors":["` + e.epRaw + `"]}`
	for _, op := range []string{runpodtest.OpRun, runpodtest.OpStatus, runpodtest.OpCancel, runpodtest.OpPurge, runpodtest.OpGetEndpoint,
		runpodtest.OpWorkers, runpodtest.OpCatalog, runpodtest.OpBilling, runpodtest.OpPatchEndpoint, runpodtest.OpHealth} {
		e.srv.Fault(op, runpodtest.Fault{Status: 400, Body: echo, Times: 3})
	}
	var out []string
	add := func(v any, err error) {
		out = append(out, fmt.Sprintf("%v|%+v|%#v|%v", v, v, v, err))
	}
	v1, err := b.Submit(bg(), take())
	add(v1, err)
	v2, err := b.Poll(bg(), provider.Ref{ID: "j"})
	add(v2, err)
	add(nil, b.Cancel(bg(), provider.Ref{ID: "j"}))
	add(nil, b.PurgeQueue(bg()))
	v3, err := b.Quote(bg())
	add(v3, err)
	v4, err := b.Sweep(bg())
	add(v4, err)
	add(nil, b.ScaleToZero(bg()))
	v5, err := b.Billing(bg(), t0.Add(-time.Hour), t0)
	add(v5, err)
	// and the successful paths
	e2 := newEnv(t)
	q, _ := e2.backend().Quote(bg())
	rep, _ := e2.backend().Sweep(bg())
	add(q, nil)
	add(rep, nil)
	for _, s := range out {
		if strings.Contains(s, e.epRaw) || strings.Contains(s, e2.epRaw) {
			t.Fatalf("endpoint id leaked: %s", s)
		}
	}
	if len(out) < 9 {
		t.Fatal("battery incomplete")
	}
}

func TestRunPod_NoFloatMoney(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		ast.Inspect(af, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && (id.Name == "float32" || id.Name == "float64") {
				t.Errorf("%s: %s used", fset.Position(id.Pos()), id.Name)
			}
			if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.FLOAT {
				t.Errorf("%s: float literal %s", fset.Position(bl.Pos()), bl.Value)
			}
			return true
		})
	}
	if checked < 7 {
		t.Fatalf("only %d files scanned", checked)
	}
}
