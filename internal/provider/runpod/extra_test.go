package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
)

func TestBackend_RevertDriftFields(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) { o.srv.PatchReplacesNested = true })
	e.srv.SetEndpoint(func(m map[string]any) {
		w := ep(m, "workers")
		w["min"], w["max"], w["idleTimeout"] = 1, 2, 120
		ep(m, "gpu")["count"] = 2
		ep(m, "gpu")["minCudaVersion"] = "11.0"
		ep(m, "scaling")["queueDelay"] = 99
		m["timeout"] = 5000
		m["flashboot"] = "OFF"
	})
	img := "ghcr.io/example/worker@sha256:" + strings.Repeat("2", 64)
	err := e.backend().RevertDrift(bg(), []provider.Drift{
		{Field: "workers.min", Want: "0"}, {Field: "workers.max", Want: "1"}, {Field: "workers.idleTimeout", Want: "20"}, {Field: "gpu.count", Want: "1"},
		{Field: "gpu.minCudaVersion", Want: "12.8"}, {Field: "scaling.queueDelay", Want: "240"}, {Field: "scaling.type", Want: "QUEUE_DELAY"},
		{Field: "timeout", Want: "900000"}, {Field: "flashboot", Want: "FLASHBOOT"}, {Field: "image", Want: img},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := e.srv.Endpoint()
	w, g := ep(got, "workers"), ep(got, "gpu")
	if runpodtest.Num(w["min"]) != 0 || runpodtest.Num(w["max"]) != 1 || runpodtest.Num(w["idleTimeout"]) != 20 || runpodtest.Num(g["count"]) != 1 ||
		g["minCudaVersion"] != "12.8" || runpodtest.Num(ep(got, "scaling")["queueDelay"]) != 240 || runpodtest.Num(got["timeout"]) != 900000 ||
		got["flashboot"] != "FLASHBOOT" || got["image"] != img {
		t.Fatalf("endpoint = %v", got)
	}
	for _, bad := range []provider.Drift{{Field: "scaling.type", Want: "REQUEST_COUNT"}, {Field: "timeout", Want: "x"}, {Field: "gpu.count", Want: "01"}} {
		if err := e.backend().RevertDrift(bg(), []provider.Drift{bad}); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestBackend_PatchMismatchFields(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.Fault(runpodtest.OpPatchEndpoint, runpodtest.Fault{Status: 200, Body: `{}`})
	err := e.backend().RevertDrift(bg(), []provider.Drift{
		{Field: "workers.min", Want: "1"}, {Field: "workers.max", Want: "2"}, {Field: "workers.idleTimeout", Want: "30"}, {Field: "gpu.count", Want: "2"},
		{Field: "gpu.minCudaVersion", Want: "11.0"}, {Field: "scaling.queueDelay", Want: "99"}, {Field: "timeout", Want: "5000"}, {Field: "flashboot", Want: "OFF"},
	})
	if !errors.Is(err, runpod.ErrPatchMismatch) {
		t.Fatalf("err = %v", err)
	}
	for _, f := range []string{"workers.min", "workers.max", "workers.idleTimeout", "gpu.count", "gpu.minCudaVersion", "scaling.queueDelay", "timeout", "flashboot"} {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("mismatch lacks %s: %v", f, err)
		}
	}
}

func TestBackend_PatchErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.Fault(runpodtest.OpPatchEndpoint, runpodtest.Fault{Status: 404})
	if err := e.backend().ScaleToZero(bg()); !errors.Is(err, provider.ErrEndpointNotFound) {
		t.Fatalf("404 PATCH = %v", err)
	}
	e.srv.Fault(runpodtest.OpPatchEndpoint, runpodtest.Fault{Status: 401})
	if err := e.backend().ScaleToZero(bg()); !errors.Is(err, provider.ErrAccess) {
		t.Fatalf("401 PATCH = %v", err)
	}
	// GET before the PATCH fails
	e.srv.Fault(runpodtest.OpGetEndpoint, runpodtest.Fault{Status: 404})
	if err := e.backend().ScaleToZero(bg()); !errors.Is(err, provider.ErrEndpointNotFound) {
		t.Fatalf("404 GET = %v", err)
	}
	e.srv.Fault(runpodtest.OpGetEndpoint, runpodtest.Fault{Status: 404})
	if err := e.backend().RevertDrift(bg(), nil); !errors.Is(err, provider.ErrEndpointNotFound) {
		t.Fatalf("404 GET (revert) = %v", err)
	}
	// GET after the PATCH fails
	e2 := newEnv(t)
	e2.srv.Fault(runpodtest.OpGetEndpoint, runpodtest.Fault{Status: 200, Body: `{"id":"x"}`}) // consumed by the first GET (empty endpoint)
	e2.srv.Fault(runpodtest.OpGetEndpoint, runpodtest.Fault{Status: 404})
	if err := e2.backend().ScaleToZero(bg()); !errors.Is(err, provider.ErrEndpointNotFound) {
		t.Fatalf("404 verifying GET = %v", err)
	}
}

func TestBackend_SweepAndBillingErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.backend()
	e.srv.Fault(runpodtest.OpWorkers, runpodtest.Fault{Status: 404})
	if _, err := b.Sweep(bg()); !errors.Is(err, provider.ErrEndpointNotFound) {
		t.Fatal(err)
	}
	e.srv.Fault(runpodtest.OpGetEndpoint, runpodtest.Fault{Status: 401})
	if _, err := b.Sweep(bg()); !errors.Is(err, provider.ErrAccess) {
		t.Fatal(err)
	}
	e.srv.Fault(runpodtest.OpBilling, runpodtest.Fault{Status: 404})
	if _, err := b.Billing(bg(), t0.Add(-time.Hour), t0); !errors.Is(err, provider.ErrEndpointNotFound) {
		t.Fatal(err)
	}
	e.srv.Fault(runpodtest.OpWorkers, runpodtest.Fault{Status: 401})
	if _, err := b.Quote(bg()); !errors.Is(err, provider.ErrAccess) {
		t.Fatalf("quote workers 401 = %v", err)
	}
	e.srv.Fault(runpodtest.OpCatalog, runpodtest.Fault{Status: 401})
	if _, err := b.Quote(bg()); !errors.Is(err, provider.ErrAccess) {
		t.Fatalf("quote catalog 401 = %v", err)
	}
	noRead := newEnv(t)
	noRead.keys.Read = ""
	if _, err := noRead.backend().Billing(bg(), t0.Add(-time.Hour), t0); !errors.Is(err, provider.ErrNotPermitted) {
		t.Fatal(err)
	}
	// billing with a garbage amount component
	e.srv.Fault(runpodtest.OpBilling, runpodtest.Fault{Status: 200, Body: `[{"startTime":"2026-10-14T10:00:00Z","endTime":"2026-10-14T11:00:00Z","totalAmount":0.1,"gpuAmount":-1,"cpuAmount":0,"diskAmount":0}]`})
	if _, err := b.Billing(bg(), t0.Add(-3*time.Hour), t0); err == nil {
		t.Fatal("negative gpu amount accepted")
	}
	e.srv.Fault(runpodtest.OpBilling, runpodtest.Fault{Status: 200, Body: `[{"startTime":"2026-10-14T10:00:00Z","endTime":"2026-10-14T11:00:00Z","totalAmount":"x"}]`})
	if _, err := b.Billing(bg(), t0.Add(-3*time.Hour), t0); err == nil {
		t.Fatal("garbage total accepted")
	}
}

func TestBackend_RequestCountScalerLiteral(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.SetEndpoint(func(m map[string]any) {
		m["scaling"] = map[string]any{"type": "REQUEST_COUNT", "requestCount": json.Number("4")}
		ep(m, "workers")["idleTimeout"] = json.Number("20.0")
	})
	q, err := e.backend().Quote(bg())
	if err != nil || q.Endpoint.ScalerType != "REQUEST_COUNT" || q.Endpoint.ScalerValue != "4" || *q.Endpoint.IdleTimeoutS != 20 {
		t.Fatalf("%+v %v", q.Endpoint, err)
	}
	e.srv.SetEndpoint(func(m map[string]any) {
		m["scaling"] = map[string]any{"type": "QUEUE_DELAY", "queueDelay": json.Number("240.0")}
	})
	q, _ = e.backend().Quote(bg())
	if q.Endpoint.ScalerValue != "240" {
		t.Fatalf("ScalerValue = %q", q.Endpoint.ScalerValue)
	}
	// drift: queue delay missing on a queue-delay scaler
	e.srv.SetEndpoint(func(m map[string]any) { m["scaling"] = map[string]any{"type": "QUEUE_DELAY"} })
	rep, _ := e.backend().Sweep(bg())
	found := false
	for _, d := range rep.Drift {
		found = found || d.Field == "scaling.queueDelay"
	}
	if !found {
		t.Fatalf("drift = %+v", rep.Drift)
	}
}

func TestClient_ClassifyDetails(t *testing.T) {
	t.Parallel()
	body := `{"title":"T","detail":"d","errors":[{"message":"m1"},{"field":"f","detail":"d2"},{"x":1},[1,2],{"detail":"only"}]}`
	e := classify(400, nil, body, nil)
	want := []string{"m1", "f: d2", `{"x":1}`, "[1,2]", "only"}
	if strings.Join(e.Errors, "|") != strings.Join(want, "|") {
		t.Fatalf("errors = %q", e.Errors)
	}
	// multi-byte text is cut on a rune boundary
	long := strings.Repeat("é", 400)
	e = classify(400, nil, `{"detail":"`+long+`"}`, nil)
	if len(e.Detail) > 500 || !strings.HasSuffix(e.Detail, "é") {
		t.Fatalf("detail %d bytes", len(e.Detail))
	}
	// plain-text bodies are kept (redacted)
	e = classify(502, nil, "Bad gateway", nil)
	if e.Detail != "Bad gateway" || e.Code != runpod.Upstream {
		t.Fatalf("%+v", e)
	}
	// unexpected 3xx is not a success
	if e := classify(302, nil, "", nil); e == nil || e.Code != runpod.BadRequest {
		t.Fatalf("302 => %v", e)
	}
	// a non-URL transport error
	if e := classify(0, nil, "", errors.New("boom")); e.Code != runpod.Network || e.Timeout {
		t.Fatalf("%+v", e)
	}
	if e := classify(0, nil, "", context.DeadlineExceeded); !e.Timeout || !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("%+v", e)
	}
}

func TestClient_DefaultsAndOptions(t *testing.T) {
	t.Parallel()
	c := runpod.NewClient(runpod.ClientOptions{})
	if c.Redactor().String("Bearer abcdefgh") == "Bearer abcdefgh" {
		t.Fatal("redactor inactive")
	}
	if _, ok := c.Skew(); ok {
		t.Fatal("skew")
	}
	// no keys at all: every call is refused locally
	if _, err := c.Health(bg()); !errors.Is(err, provider.ErrNotPermitted) {
		t.Fatal(err)
	}
	if runpod.UserAgentFor("1.2.3") != "metamusic/1.2.3 (+https://github.com/nicolasacchi/metamusic)" {
		t.Fatal(runpod.UserAgentFor("1.2.3"))
	}
	// the billing and other timeouts can be overridden
	e := newEnv(t, func(o *envOpts) {
		o.client = func(co *runpod.ClientOptions) {
			co.Timeouts = runpod.Timeouts{Billing: 20 * time.Millisecond, Other: 20 * time.Millisecond}
		}
	})
	e.srv.Fault(runpodtest.OpBilling, runpodtest.Fault{Status: 200, Body: `[]`, Sleep: 300 * time.Millisecond, Times: 4})
	drive(t, e.clk, func() {
		if _, err := e.cl.BillingServerless(bg(), runpod.BillingQuery{Start: t0, End: t0.Add(time.Hour)}); err == nil {
			t.Fatal("billing did not time out")
		}
	})
	e.srv.Fault(runpodtest.OpHealth, runpodtest.Fault{Status: 200, Body: `{}`, Sleep: 300 * time.Millisecond, Times: 4})
	drive(t, e.clk, func() {
		if _, err := e.cl.Health(bg()); err == nil {
			t.Fatal("health did not time out")
		}
	})
}

func TestClient_ContextCancellation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx, cancel := context.WithCancel(bg())
	cancel()
	if _, err := e.cl.Run(ctx, in, runpod.Policy{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx => %v", err)
	}
	// cancelled while waiting for the rate-limit window
	e.srv.Fault(runpodtest.OpHealth, runpodtest.Fault{Status: 200, Body: `{}`, Header: http.Header{"Ratelimit": {`"minute";r=0;t=30`}}})
	if _, err := e.cl.Health(bg()); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(bg())
	done := make(chan error, 1)
	go func() { _, err := e.cl.Health(ctx2); done <- err }()
	waitPending(t, e, 1)
	cancel2()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// cancelled during a GET backoff
	e3 := newEnv(t)
	e3.srv.Fault(runpodtest.OpWorkers, runpodtest.Fault{Status: 503, Times: -1})
	ctx3, cancel3 := context.WithCancel(bg())
	done3 := make(chan error, 1)
	go func() { _, err := e3.cl.Workers(ctx3); done3 <- err }()
	waitPending(t, e3, 1)
	cancel3()
	if err := <-done3; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestClient_DecodeShapes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.Fault(runpodtest.OpWorkers, runpodtest.Fault{Status: 200, Body: `{"workers":[{"id":"w"}]}`})
	if ws, err := e.cl.Workers(bg()); err != nil || len(ws) != 1 {
		t.Fatalf("wrapped list = %v %v", ws, err)
	}
	e.srv.Fault(runpodtest.OpWorkers, runpodtest.Fault{Status: 200, Body: `{"nothing":[]}`})
	var re *runpod.Error
	if _, err := e.cl.Workers(bg()); !errors.As(err, &re) || re.Code != runpod.Decode {
		t.Fatal(err)
	}
	e.srv.Fault(runpodtest.OpWorkers, runpodtest.Fault{Status: 200, Body: `nope`})
	if _, err := e.cl.Workers(bg()); !errors.As(err, &re) || re.Code != runpod.Decode {
		t.Fatal(err)
	}
	// numbers: floats and exponents in execution times
	for body, want := range map[string]int64{`{"id":"j","status":"COMPLETED","executionTime":12.9}`: 12, `{"id":"j","status":"COMPLETED","executionTime":-3}`: -3} {
		e.srv.Fault(runpodtest.OpStatus, runpodtest.Fault{Status: 200, Body: body})
		js, err := e.cl.Status(bg(), "j")
		if err != nil || js.ExecutionMS == nil || *js.ExecutionMS != want {
			t.Fatalf("%s => %+v %v", body, js, err)
		}
	}
	e.srv.Fault(runpodtest.OpStatus, runpodtest.Fault{Status: 200, Body: `{"id":"j","status":"X","executionTime":1.5e3,"output":null,"error":{"code":1}}`})
	js, err := e.cl.Status(bg(), "j")
	if err != nil || js.ExecutionMS != nil || len(js.Output) != 0 || js.Error != `{"code":1}` {
		t.Fatalf("%+v %v", js, err)
	}
	// health counters
	e.srv.Fault(runpodtest.OpHealth, runpodtest.Fault{Status: 200, Body: `{"jobs":{"inQueue":2,"x":"bad"},"workers":{"idle":1}}`})
	h, err := e.cl.Health(bg())
	if err != nil || h.Jobs["inQueue"] != 2 || h.Workers["idle"] != 1 {
		t.Fatalf("%+v %v", h, err)
	}
	// balance decode failures
	e.srv.Fault(runpodtest.OpGraphQL, runpodtest.Fault{Status: 200, Body: `nope`})
	if _, _, err := e.cl.Balance(bg()); err == nil {
		t.Fatal("garbage balance accepted")
	}
	e.srv.Fault(runpodtest.OpGraphQL, runpodtest.Fault{Status: 200, Body: `{"data":{"myself":{"clientBalance":"abc"}}}`})
	if _, ok, err := e.cl.Balance(bg()); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	// endpoint documents with garbage
	e.srv.Fault(runpodtest.OpGetEndpoint, runpodtest.Fault{Status: 200, Body: `nope`})
	if _, err := e.cl.GetEndpoint(bg(), runpod.KeyRead); err == nil {
		t.Fatal("garbage endpoint")
	}
	e.srv.Fault(runpodtest.OpPatchEndpoint, runpodtest.Fault{Status: 200, Body: `nope`})
	if _, err := e.cl.PatchEndpoint(bg(), runpod.KeyWrite, runpod.EndpointPatch{}); err == nil {
		t.Fatal("garbage patch response")
	}
	e.srv.Fault(runpodtest.OpListEndpoints, runpodtest.Fault{Status: 200, Body: `nope`})
	if _, err := e.cl.ListEndpoints(bg(), runpod.KeyRead); err == nil {
		t.Fatal("garbage list")
	}
	e.srv.Fault(runpodtest.OpCatalog, runpodtest.Fault{Status: 200, Body: `nope`})
	if _, err := e.cl.CatalogGPUs(bg(), runpod.KeyRead); err == nil {
		t.Fatal("garbage catalog")
	}
	e.srv.Fault(runpodtest.OpBilling, runpodtest.Fault{Status: 200, Body: `nope`})
	if _, err := e.cl.BillingServerless(bg(), runpod.BillingQuery{}); err == nil {
		t.Fatal("garbage billing")
	}
}

type recorder struct {
	mu  sync.Mutex
	got []runpod.Exchange
}

func (r *recorder) Record(x runpod.Exchange) { r.mu.Lock(); r.got = append(r.got, x); r.mu.Unlock() }

func TestClient_RecorderSeesExchanges(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	e := newEnv(t, func(o *envOpts) { o.client = func(co *runpod.ClientOptions) { co.Recorder = rec } })
	if _, err := e.cl.Run(bg(), in, runpod.Policy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cl.Status(bg(), "j"); err == nil {
		t.Fatal("expected 200 or 404") // an unknown job answers 404
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.got) != 2 || rec.got[0].Op != "run" || rec.got[0].Status != 200 || rec.got[0].PathTemplate != "/v2/<endpoint>/run" ||
		rec.got[1].Status != 404 || rec.got[1].PathTemplate != "/v2/<endpoint>/status/{id}" {
		t.Fatalf("exchanges = %+v", rec.got)
	}
	for _, x := range rec.got {
		if x.Header.Get("Authorization") != "" {
			t.Fatal("authorization recorded")
		}
	}
}
