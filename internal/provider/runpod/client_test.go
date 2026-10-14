package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
)

var in = json.RawMessage(`{"contract":"probe"}`)

func TestClient_UserAgentAndAuth(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := bg()
	if _, err := e.cl.Run(ctx, in, runpod.Policy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cl.GetEndpoint(ctx, runpod.KeyRead); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cl.CatalogGPUs(ctx, runpod.KeyRead); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cl.PatchEndpoint(ctx, runpod.KeyWrite, runpod.EndpointPatch{Name: ptr("mm-yue2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cl.Workers(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string]runpodtest.Kind{
		runpodtest.OpRun: runpodtest.KindJobs, runpodtest.OpGetEndpoint: runpodtest.KindRead, runpodtest.OpCatalog: runpodtest.KindRead,
		runpodtest.OpPatchEndpoint: runpodtest.KindWrite, runpodtest.OpWorkers: runpodtest.KindRead,
	}
	for _, r := range e.srv.Requests() {
		if got := r.Header.Get("User-Agent"); got != "metamusic/test (+https://github.com/nicolasacchi/metamusic)" {
			t.Errorf("%s: User-Agent %q", r.Op, got)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("%s: Accept %q", r.Op, r.Header.Get("Accept"))
		}
		if r.Key != want[r.Op] {
			t.Errorf("%s: key kind %q, want %q", r.Op, r.Key, want[r.Op])
		}
		if r.Key == runpodtest.KindAdmin {
			t.Errorf("%s used the admin key", r.Op)
		}
	}
	// serve mode has no admin key: CreateEndpoint is refused before any request.
	before := len(e.srv.Requests())
	if _, err := e.cl.CreateEndpoint(ctx, runpod.EndpointSpec{Name: "x"}); !errors.Is(err, provider.ErrNotPermitted) {
		t.Fatalf("CreateEndpoint without admin = %v", err)
	}
	if len(e.srv.Requests()) != before {
		t.Fatal("a request was sent")
	}
}

func TestClient_AdminKeyOnlyForCreate(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) {
		o.client = func(co *runpod.ClientOptions) {
			a := adminKey(t, canary("admin"))
			co.Admin = &a
		}
	})
	if _, err := e.cl.CreateEndpoint(bg(), runpod.EndpointSpec{Name: "mm-yue2", Type: "QUEUE"}); err != nil {
		t.Fatal(err)
	}
	r, _ := e.srv.Last(runpodtest.OpCreateEndpoint)
	if r.Key != runpodtest.KindAdmin {
		t.Fatalf("create used %q", r.Key)
	}
}

func TestClient_NoKeyIsNotPermitted(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) { o.noWrite = true })
	if _, err := e.cl.PatchEndpoint(bg(), runpod.KeyWrite, runpod.EndpointPatch{}); !errors.Is(err, provider.ErrNotPermitted) {
		t.Fatalf("PATCH without a write key = %v", err)
	}
	if e.srv.Calls(runpodtest.OpPatchEndpoint) != 0 {
		t.Fatal("request sent")
	}
}

func TestClient_RateLimitHeaders(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.Fault(runpodtest.OpHealth, runpodtest.Fault{Status: 200, Body: `{"jobs":{"inQueue":1},"workers":{}}`,
		Header: http.Header{"Ratelimit": {`"minute";r=0;t=10, "hour";r=2800;t=1812`}}})
	if _, err := e.cl.Health(bg()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := e.cl.Health(bg()); done <- err }()
	waitPending(t, e, 1)
	select {
	case <-done:
		t.Fatal("second request did not wait for the exhausted window")
	case <-time.After(20 * time.Millisecond):
	}
	if e.srv.Calls(runpodtest.OpHealth) != 1 {
		t.Fatalf("calls = %d while waiting", e.srv.Calls(runpodtest.OpHealth))
	}
	e.clk.Advance(9 * time.Second)
	select {
	case <-done:
		t.Fatal("released 1 s early")
	case <-time.After(20 * time.Millisecond):
	}
	e.clk.Advance(time.Second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitPending(t *testing.T, e *env, n int) {
	t.Helper()
	for i := 0; i < 5000 && e.clk.Pending() < n; i++ {
		time.Sleep(time.Millisecond)
	}
	if e.clk.Pending() < n {
		t.Fatal("nothing waited on the clock")
	}
}

func TestClient_RetriesGETOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.Fault(runpodtest.OpRun, runpodtest.Fault{Status: 503, Times: -1})
	_, err := e.cl.Run(bg(), in, runpod.Policy{})
	var re *runpod.Error
	if !errors.As(err, &re) || re.Code != runpod.Upstream || !re.Ambiguous() {
		t.Fatalf("Run = %v", err)
	}
	if n := e.srv.Calls(runpodtest.OpRun); n != 1 {
		t.Fatalf("/run reached the server %d times", n)
	}
	// GET: two 503s then success (1 s + 2 s backoff on the manual clock)
	e.srv.Fault(runpodtest.OpWorkers, runpodtest.Fault{Status: 503, Times: 2})
	drive(t, e.clk, func() {
		if _, err := e.cl.Workers(bg()); err != nil {
			t.Fatal(err)
		}
	})
	if n := e.srv.Calls(runpodtest.OpWorkers); n != 3 {
		t.Fatalf("workers calls = %d", n)
	}
	// non-retryable codes are not retried
	e.srv.Fault(runpodtest.OpWorkers, runpodtest.Fault{Status: 404})
	if _, err := e.cl.Workers(bg()); err == nil {
		t.Fatal("404 succeeded")
	}
	if n := e.srv.Calls(runpodtest.OpWorkers); n != 4 {
		t.Fatalf("404 was retried: %d", n)
	}
	// retries stop after three
	e.srv.Fault(runpodtest.OpWorkers, runpodtest.Fault{Status: 503, Times: -1})
	drive(t, e.clk, func() {
		if _, err := e.cl.Workers(bg()); err == nil {
			t.Fatal("persistent 503 succeeded")
		}
	})
	if n := e.srv.Calls(runpodtest.OpWorkers); n != 8 {
		t.Fatalf("workers calls = %d, want 8", n)
	}
}

func TestClient_PostsAreNeverRetried(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, op := range []string{runpodtest.OpCancel, runpodtest.OpPurge, runpodtest.OpPatchEndpoint} {
		e.srv.Fault(op, runpodtest.Fault{Status: 503, Times: -1})
	}
	_, _ = e.cl.Cancel(bg(), "j1")
	_ = e.cl.PurgeQueue(bg())
	_, _ = e.cl.PatchEndpoint(bg(), runpod.KeyWrite, runpod.EndpointPatch{Name: ptr("x")})
	for _, op := range []string{runpodtest.OpCancel, runpodtest.OpPurge, runpodtest.OpPatchEndpoint} {
		if n := e.srv.Calls(op); n != 1 {
			t.Errorf("%s reached the server %d times", op, n)
		}
	}
}

func TestClient_RunNotReplayable(t *testing.T) {
	t.Parallel()
	var sawGetBody atomic.Bool
	e := newEnv(t, func(o *envOpts) {
		o.client = func(co *runpod.ClientOptions) {
			co.HTTP = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
				if r.GetBody != nil {
					sawGetBody.Store(true)
				}
				return http.DefaultTransport.RoundTrip(r)
			})}
		}
	})
	e.srv.Fault(runpodtest.OpRun, runpodtest.Fault{Drop: true, Times: -1})
	_, err := e.cl.Run(bg(), in, runpod.Policy{ExecutionTimeoutMS: 186000, TTLMS: 7200000})
	var re *runpod.Error
	if !errors.As(err, &re) || !re.Ambiguous() {
		t.Fatalf("Run = %v", err)
	}
	if n := e.srv.Calls(runpodtest.OpRun); n != 1 {
		t.Fatalf("%d /run requests reached the server", n)
	}
	if sawGetBody.Load() {
		t.Fatal("request.GetBody set")
	}
	r, _ := e.srv.Last(runpodtest.OpRun)
	for _, h := range []string{"Idempotency-Key", "X-Idempotency-Key"} {
		if r.Header.Get(h) != "" {
			t.Errorf("%s header present", h)
		}
	}
	var body struct {
		Input  json.RawMessage
		Policy struct{ ExecutionTimeout, TTL int64 }
	}
	if err := json.Unmarshal(r.Body, &body); err != nil || body.Policy.ExecutionTimeout != 186000 || body.Policy.TTL != 7200000 {
		t.Fatalf("body %s (%v)", r.Body, err)
	}
}

func TestClient_RunTimeout60s(t *testing.T) {
	t.Parallel()
	got := map[string]time.Duration{}
	e := newEnv(t, func(o *envOpts) {
		o.client = func(co *runpod.ClientOptions) {
			co.HTTP = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
				if d, ok := r.Context().Deadline(); ok {
					got[r.Method+" "+r.URL.Path[:strings.Index(r.URL.Path[1:], "/")+2]] = time.Until(d)
				}
				return http.DefaultTransport.RoundTrip(r)
			})}
		}
	})
	_, _ = e.cl.Run(bg(), in, runpod.Policy{})
	_, _ = e.cl.BillingServerless(bg(), runpod.BillingQuery{Start: t0, End: t0.Add(time.Hour)})
	_, _ = e.cl.Health(bg())
	near := func(d, want time.Duration) bool { return d > want-2*time.Second && d <= want }
	if !near(got["POST /v2/"], runpod.RunTimeout) || runpod.RunTimeout != 60*time.Second {
		t.Errorf("run deadline %v", got["POST /v2/"])
	}
	if d := got["GET /v2/"]; d <= 0 || d > runpod.OtherTimeout {
		t.Errorf("other deadline %v", d)
	}
	// a client timeout on Run is ambiguous
	slow := newEnv(t, func(o *envOpts) {
		o.client = func(co *runpod.ClientOptions) { co.Timeouts.Run = 30 * time.Millisecond }
	})
	slow.srv.Fault(runpodtest.OpRun, runpodtest.Fault{Status: 200, Body: `{"id":"j","status":"IN_QUEUE"}`, Sleep: 300 * time.Millisecond})
	_, err := slow.cl.Run(bg(), in, runpod.Policy{})
	var re *runpod.Error
	if !errors.As(err, &re) || !re.Timeout || !re.Ambiguous() || re.Code != runpod.Network {
		t.Fatalf("timeout Run = %v", err)
	}
}

func TestClient_ErrorBody(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	long := strings.Repeat("bad value ", 100)
	e.srv.Fault(runpodtest.OpPatchEndpoint, runpodtest.Fault{Status: 422,
		Body: `{"title":"Unprocessable","status":422,"detail":"` + long + ` Bearer ` + e.set.Write + `","errors":["` + long + `"]}`})
	_, err := e.cl.PatchEndpoint(bg(), runpod.KeyWrite, runpod.EndpointPatch{Name: ptr("x")})
	var re *runpod.Error
	if !errors.As(err, &re) || re.Code != runpod.BadRequest || re.Status != 422 {
		t.Fatalf("err = %v", err)
	}
	n := len(re.Detail)
	for _, s := range re.Errors {
		n += len(s)
	}
	if n > 500 {
		t.Fatalf("%d bytes of body kept", n)
	}
	if strings.Contains(re.Detail, e.set.Write) || strings.Contains(re.Error(), e.set.Write) {
		t.Fatal("key in error")
	}
}

func TestClient_ErrorsHaveNoURLOrKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	secrets := []string{e.set.Jobs, e.set.Read, e.set.Write, e.epRaw, e.srv.URL, "http://"}
	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: no error", name)
			return
		}
		for _, s := range secrets {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%s: error %q contains %q", name, err, s)
			}
		}
	}
	// a body that echoes the key and the endpoint id
	echo := `{"title":"x","detail":"Bearer ` + e.set.Jobs + ` at /v2/` + e.epRaw + `/run ` + e.epRaw + `"}`
	for _, op := range []string{runpodtest.OpRun, runpodtest.OpStatus, runpodtest.OpGetEndpoint} {
		e.srv.Fault(op, runpodtest.Fault{Status: 400, Body: echo})
	}
	_, err := e.cl.Run(bg(), in, runpod.Policy{})
	check("run", err)
	_, err = e.cl.Status(bg(), "j")
	check("status", err)
	_, err = e.cl.GetEndpoint(bg(), runpod.KeyRead)
	check("get", err)
	// transport error: closed server
	dead := newEnv(t)
	dead.srv.Close()
	_, err = dead.cl.Run(bg(), in, runpod.Policy{})
	check("dial", err)
	secrets = append(secrets, dead.srv.URL)
	for _, s := range []string{dead.srv.URL, dead.epRaw} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("dial error contains %q: %v", s, err)
		}
	}
}

func TestClient_ResponseTooLarge(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.Fault(runpodtest.OpWorkers, runpodtest.Fault{Status: 200, Body: `[` + strings.Repeat(`{"id":"w"},`, 100000) + `{"id":"w"}]`})
	_, err := e.cl.Workers(bg())
	if !errors.Is(err, runpod.ErrResponseTooLarge) {
		t.Fatalf("err = %v", err)
	}
	big := json.RawMessage(`{"x":"` + strings.Repeat("a", int(provider.MaxRunBody)) + `"}`)
	if _, err := e.cl.Run(bg(), big, runpod.Policy{}); !errors.Is(err, runpod.ErrRequestTooLarge) {
		t.Fatalf("oversized /run = %v", err)
	}
	if e.srv.Calls(runpodtest.OpRun) != 0 {
		t.Fatal("oversized /run was sent")
	}
	huge := strings.Repeat("a", 70<<10)
	if _, err := e.cl.PatchEndpoint(bg(), runpod.KeyWrite, runpod.EndpointPatch{Name: &huge}); !errors.Is(err, runpod.ErrRequestTooLarge) {
		t.Fatalf("oversized PATCH = %v", err)
	}
}

func TestClient_DateSkew(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) { o.srv.SkewOffset = 42 * time.Second })
	if _, ok := e.cl.Skew(); ok {
		t.Fatal("skew known before any request")
	}
	if _, err := e.cl.Health(bg()); err != nil {
		t.Fatal(err)
	}
	d, ok := e.cl.Skew()
	if !ok || d != 42*time.Second {
		t.Fatalf("skew = %v, %v", d, ok)
	}
}

func TestClient_GarbageIsDecodeError(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.Fault(runpodtest.OpStatus, runpodtest.Fault{Status: 200, Body: `<html>nope`})
	_, err := e.cl.Status(bg(), "j")
	var re *runpod.Error
	if !errors.As(err, &re) || re.Code != runpod.Decode || re.Retryable() {
		t.Fatalf("err = %v", err)
	}
	e.srv.Fault(runpodtest.OpRun, runpodtest.Fault{Status: 200, Body: `{"status":"IN_QUEUE"}`})
	if _, err := e.cl.Run(bg(), in, runpod.Policy{}); !errors.As(err, &re) || re.Code != runpod.Decode {
		t.Fatalf("run without id = %v", err)
	}
}

func TestClient_JobRecords(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) { o.srv.PollsToRun = 3 })
	r, err := e.cl.Run(bg(), in, runpod.Policy{})
	if err != nil || r.ID == "" || r.Status != "IN_QUEUE" {
		t.Fatalf("run = %+v %v", r, err)
	}
	var last runpod.JobStatus
	for i := 0; i < 3; i++ {
		if last, err = e.cl.Status(bg(), r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if last.Status != "COMPLETED" || last.ExecutionMS == nil || *last.ExecutionMS != 1500 || last.DelayMS == nil || last.WorkerID == "" || len(last.Output) == 0 {
		t.Fatalf("status = %+v", last)
	}
	c, err := e.cl.Cancel(bg(), r.ID)
	if err != nil || c.ID != r.ID || c.ExecutionMS != nil {
		t.Fatalf("cancel = %+v %v", c, err)
	}
	h, err := e.cl.Health(bg())
	if err != nil || h.Jobs == nil {
		t.Fatalf("health = %+v %v", h, err)
	}
	if err := e.cl.PurgeQueue(bg()); err != nil {
		t.Fatal(err)
	}
	// a failed job: the error text is redacted
	e2 := newEnv(t, func(o *envOpts) {
		o.srv.Runner = func(context_ context.Context, id string, _ json.RawMessage) runpodtest.Run {
			return runpodtest.Run{Failed: true, ErrText: "PIPELINE_ERROR: boom https://x.example/up?sig=abcdefabcdefabcdefabcdefabcdefabcd", ExecutionMS: 900}
		}
	})
	r2, _ := e2.cl.Run(bg(), in, runpod.Policy{})
	var js runpod.JobStatus
	for i := 0; i < 3; i++ {
		js, _ = e2.cl.Status(bg(), r2.ID)
	}
	if js.Status != "FAILED" || strings.Contains(js.Error, "sig=") || !strings.Contains(js.Error, "boom") {
		t.Fatalf("failed = %+v", js)
	}
}

func TestClient_JobAPIFixtures(t *testing.T) {
	t.Skip("fixtures arrive in M3-X3")
}

func TestClient_BalanceAndBilling(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	m, ok, err := e.cl.Balance(bg())
	if err != nil || !ok || m != 12_340_000 {
		t.Fatalf("balance = %v %v %v", m, ok, err)
	}
	e.srv.SetBalance("")
	if _, ok, err := e.cl.Balance(bg()); err != nil || ok {
		t.Fatalf("null balance ok=%v err=%v", ok, err)
	}
	e.srv.SetBilling([]map[string]any{{"startTime": "2026-10-14T10:00:00Z", "endTime": "2026-10-14T11:00:00Z", "serverlessId": "x",
		"totalAmount": json.Number("0.123456789"), "gpuAmount": json.Number("0.1"), "cpuAmount": json.Number("0.02"), "diskAmount": json.Number("0.003456789"), "feeAmount": json.Number("0")}})
	recs, err := e.cl.BillingServerless(bg(), runpod.BillingQuery{Start: t0.Add(-2 * time.Hour), End: t0})
	if err != nil || len(recs) != 1 || recs[0].Total != "0.123456789" || recs[0].GPU != "0.1" {
		t.Fatalf("billing = %+v %v", recs, err)
	}
	r, _ := e.srv.Last(runpodtest.OpBilling)
	for _, want := range []string{"bucketSize=hour", "serverlessId=", "startTime=2026-10-14T10%3A00%3A00Z", "endTime=2026-10-14T12%3A00%3A00Z"} {
		if !strings.Contains(r.Query, want) {
			t.Errorf("query %q lacks %q", r.Query, want)
		}
	}
	// list endpoints, workers with nulls
	e.srv.SetWorkers([]map[string]any{{"id": "w1", "status": "INITIALIZING", "isStale": false, "image": "img", "startedAt": nil, "uptimeSeconds": nil, "gpuTypeId": "gpu-a5000"},
		{"id": "w2", "status": "RUNNING", "startedAt": "2026-10-14T11:59:00Z", "uptimeSeconds": 60}})
	ws, err := e.cl.Workers(bg())
	if err != nil || len(ws) != 2 || ws[0].StartedAt != nil || ws[0].UptimeSeconds != nil || ws[1].UptimeSeconds == nil || *ws[1].UptimeSeconds != 60 {
		t.Fatalf("workers = %+v %v", ws, err)
	}
	eps, err := e.cl.ListEndpoints(bg(), runpod.KeyRead)
	if err != nil || len(eps) != 1 || eps[0].Name != "mm-yue2" {
		t.Fatalf("list = %+v %v", eps, err)
	}
}

func ptr[T any](v T) *T { return &v }
