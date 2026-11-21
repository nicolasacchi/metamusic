package pods_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods/podtest"
)

func TestPodClient_UserAgentAndTimeouts(t *testing.T) {
	t.Parallel()
	if pods.CreateTimeout != 60*time.Second || pods.OtherTimeout != 15*time.Second || pods.LogsReadTimeout != 5*time.Second ||
		pods.MaxRequestBody != 64<<10 || pods.MaxResponse != 1<<20 || pods.MaxLogTail != 5000 || pods.DefaultBase != "https://api.runpod.io" {
		t.Error("a C392.1 constant changed")
	}
	if got := pods.UserAgentFor("1.2.3"); got != "metamusic/1.2.3 (+https://github.com/nicolasacchi/metamusic)" {
		t.Errorf("UserAgentFor = %q", got)
	}
	e := newEnv(t)
	e.srv.AddPod(podtest.Pod{ID: "p1", Name: podName})
	if _, err := e.cl.GetPod(bg(), "p1"); err != nil {
		t.Fatal(err)
	}
	req, _ := e.srv.Last(podtest.OpGet)
	if req.Header.Get("User-Agent") != "metamusic/test (+https://github.com/nicolasacchi/metamusic)" || req.Header.Get("Authorization") != "Bearer "+e.key ||
		req.Header.Get("Accept") != "application/json" {
		t.Errorf("headers = %v", req.Header)
	}
	// a hung GET times out per attempt and is retried three times (four calls), then fails without a URL
	e = newEnv(t, func(o *envOpts) { o.timeouts.Other = 40 * time.Millisecond })
	e.srv.Fault(podtest.OpGet, podtest.Fault{Hang: true, Count: 10})
	var err error
	drive(t, e.clk, func() { _, err = e.cl.GetPod(bg(), "p1") })
	if err == nil || e.srv.Calls(podtest.OpGet) != 4 {
		t.Fatalf("err = %v, calls = %d, want 4", err, e.srv.Calls(podtest.OpGet))
	}
	if strings.Contains(err.Error(), e.srv.URL) || strings.Contains(err.Error(), "/v2/pods") {
		t.Errorf("the error carries the URL: %v", err)
	}
	// default User-Agent
	if got := pods.NewClient(pods.Options{KeyDir: e.dir}); got == nil {
		t.Error("nil client")
	}
}

func TestPodClient_GetRetries(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.AddPod(podtest.Pod{ID: "p1", Name: podName})
	e.srv.Fault(podtest.OpGet, podtest.Fault{Count: 2, Status: 500})
	var err error
	var p pods.Pod
	drive(t, e.clk, func() { p, err = e.cl.GetPod(bg(), "p1") })
	if err != nil || p.ID != "p1" || e.srv.Calls(podtest.OpGet) != 3 {
		t.Fatalf("pod %+v err %v calls %d", p, err, e.srv.Calls(podtest.OpGet))
	}
	// 429 with Retry-After is waited out, then succeeds
	e.srv.Fault(podtest.OpGet, podtest.Fault{Count: 1, Status: 429, RetryAfter: "3"})
	before := e.clk.Now()
	drive(t, e.clk, func() { _, err = e.cl.GetPod(bg(), "p1") })
	if err != nil || e.clk.Now().Sub(before) < 3*time.Second {
		t.Errorf("err %v, waited %v, want at least 3s", err, e.clk.Now().Sub(before))
	}
	// persistent 5xx: three retries, then the status as an APIError
	e.srv.Fault(podtest.OpGet, podtest.Fault{Count: 10, Status: 502, Detail: "bad gateway"})
	calls := e.srv.Calls(podtest.OpGet)
	drive(t, e.clk, func() { _, err = e.cl.GetPod(bg(), "p1") })
	var api *pods.APIError
	if !errors.As(err, &api) || api.Status != 502 || e.srv.Calls(podtest.OpGet)-calls != 4 {
		t.Errorf("err %v, calls %d", err, e.srv.Calls(podtest.OpGet)-calls)
	}
	// 4xx other than 429 is not retried
	e2 := newEnv(t)
	e2.srv.Fault(podtest.OpGet, podtest.Fault{Status: 400, Count: 5})
	if _, err := e2.cl.GetPod(bg(), "p1"); err == nil || e2.srv.Calls(podtest.OpGet) != 1 {
		t.Errorf("err %v calls %d", err, e2.srv.Calls(podtest.OpGet))
	}
}

func TestPodClient_RateLimitWindowWaits(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.Fault(podtest.OpList, podtest.Fault{Status: 200, Body: `{"pods":[],"pagination":{"hasNextPage":false,"nextCursor":null}}`,
		RateLimit: `"minute";r=0;t=12, "hour";r=2800;t=1812`})
	if _, err := e.cl.ListPods(bg()); err != nil {
		t.Fatal(err)
	}
	before := e.clk.Now()
	var err error
	drive(t, e.clk, func() { _, err = e.cl.ListPods(bg()) })
	if err != nil || e.clk.Now().Sub(before) < 12*time.Second {
		t.Errorf("err %v, waited %v, want at least 12s", err, e.clk.Now().Sub(before))
	}
}

func TestPodClient_ListPaginates(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) { o.srv.PageSize = 2 })
	for _, id := range []string{"p1", "p2", "p3", "p4", "p5"} {
		e.srv.AddPod(podtest.Pod{ID: id, Name: "mm-0190abcd123" + id[1:]})
	}
	e.srv.AddPod(podtest.Pod{ID: "foreign", Name: "someone-elses"})
	got, err := e.cl.ListPods(bg())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "foreign,p5,p4,p3,p2,p1" {
		t.Errorf("ids = %v", ids)
	}
	if n := e.srv.Calls(podtest.OpList); n != 3 {
		t.Errorf("list calls = %d, want 3 pages", n)
	}
	if r, _ := e.srv.Last(podtest.OpList); !strings.Contains(r.Query, "cursor=") || !strings.Contains(r.Query, "limit=1000") {
		t.Errorf("last query = %q", r.Query)
	}
	// a cursor that never advances is an error, not a loop
	e = newEnv(t)
	e.srv.Fault(podtest.OpList, podtest.Fault{Count: 100, Status: 200, Body: `{"pods":[],"pagination":{"hasNextPage":true,"nextCursor":"same"}}`})
	if _, err := e.cl.ListPods(bg()); err == nil || e.srv.Calls(podtest.OpList) != 2 {
		t.Errorf("err %v calls %d", err, e.srv.Calls(podtest.OpList))
	}
	e = newEnv(t)
	e.srv.Fault(podtest.OpList, podtest.Fault{Count: 100, Status: 200, Body: `{"pods":[],"pagination":{"hasNextPage":true,"nextCursor":null}}`})
	if _, err := e.cl.ListPods(bg()); err == nil {
		t.Error("a missing cursor was accepted")
	}
	// bodies that cannot be read
	for _, b := range []string{`[`, `{"pods":[{"id":"x","cost":"abc"}],"pagination":{"hasNextPage":false}}`} {
		e = newEnv(t)
		e.srv.Fault(podtest.OpList, podtest.Fault{Status: 200, Body: b})
		var api *pods.APIError
		if _, err := e.cl.ListPods(bg()); !errors.As(err, &api) {
			t.Errorf("body %q: err = %v", b, err)
		}
	}
	// access
	e = newEnv(t)
	e.srv.Fault(podtest.OpList, podtest.Fault{Status: 403})
	if _, err := e.cl.ListPods(bg()); !errors.Is(err, provider.ErrAccess) {
		t.Errorf("403 on list: %v", err)
	}
}

func TestPodClient_GetPodGone(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) { o.srv.KeepTerminated = true })
	if _, err := e.cl.GetPod(bg(), "nope"); !errors.Is(err, pods.ErrPodGone) {
		t.Errorf("404: %v", err)
	}
	e.srv.AddPod(podtest.Pod{ID: "p1", Name: podName})
	if err := e.cl.DeletePod(bg(), "p1"); err != nil {
		t.Fatal(err)
	}
	p, err := e.cl.GetPod(bg(), "p1")
	if !errors.Is(err, pods.ErrPodGone) || p.Status != pods.StatusTerminated || p.ID != "p1" {
		t.Errorf("terminated: %+v %v", p, err)
	}
	for _, bad := range []string{"", "a/b", "../x", "a b", strings.Repeat("a", 65)} {
		if _, err := e.cl.GetPod(bg(), bad); err == nil || errors.Is(err, pods.ErrPodGone) {
			t.Errorf("id %q: %v", bad, err)
		}
		if err := e.cl.DeletePod(bg(), bad); err == nil {
			t.Errorf("delete id %q accepted", bad)
		}
	}
	e.srv.Fault(podtest.OpGet, podtest.Fault{Status: 200, Body: `{"id":`})
	var api *pods.APIError
	if _, err := e.cl.GetPod(bg(), "p1"); !errors.As(err, &api) {
		t.Errorf("unreadable: %v", err)
	}
	e.srv.Fault(podtest.OpGet, podtest.Fault{Status: 200, Body: `{"id":"p1","cost":"x"}`})
	if _, err := e.cl.GetPod(bg(), "p1"); !errors.As(err, &api) {
		t.Errorf("bad cost: %v", err)
	}
	e.srv.Fault(podtest.OpGet, podtest.Fault{Status: 401})
	if _, err := e.cl.GetPod(bg(), "p1"); !errors.Is(err, provider.ErrAccess) {
		t.Errorf("401: %v", err)
	}
}

func TestPodClient_DeleteNotFoundIsSuccess(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if err := e.cl.DeletePod(bg(), "never-existed"); err != nil {
		t.Errorf("404 must be success: %v", err)
	}
	e.srv.AddPod(podtest.Pod{ID: "p1", Name: podName})
	if err := e.cl.DeletePod(bg(), "p1"); err != nil || !e.srv.Deleted("p1") {
		t.Errorf("delete: %v", err)
	}
	if err := e.cl.DeletePod(bg(), "p1"); err != nil { // already gone
		t.Errorf("second delete: %v", err)
	}
	if err := e.cl.CancelQueued(bg(), "q-unknown"); err != nil {
		t.Errorf("cancel of an unknown queue entry: %v", err)
	}
	e.srv.Fault(podtest.OpDelete, podtest.Fault{Status: 409, Detail: "busy"})
	var api *pods.APIError
	if err := e.cl.DeletePod(bg(), "p1"); !errors.As(err, &api) || api.Status != 409 {
		t.Errorf("409: %v", err)
	}
	e.srv.Fault(podtest.OpDelete, podtest.Fault{Status: 403})
	if err := e.cl.DeletePod(bg(), "p1"); !errors.Is(err, provider.ErrAccess) {
		t.Errorf("403 on delete: %v", err)
	}
	e.srv.Fault(podtest.OpDelete, podtest.Fault{Status: 429, RetryAfter: "9"})
	var rl *pods.ErrRateLimited
	if err := e.cl.DeletePod(bg(), "p1"); !errors.As(err, &rl) || rl.RetryAfter != 9*time.Second {
		t.Errorf("429: %v", err)
	}
	e.srv.Fault(podtest.OpDelete, podtest.Fault{Status: 200, Body: "{}"}) // any 2xx
	if err := e.cl.DeletePod(bg(), "p1"); err != nil {
		t.Errorf("200: %v", err)
	}
}

func TestPodClient_CostWithoutFloat(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) {
		o.srv.Prices = map[string]string{"cheap": "0.34", "thin": "0.0000004", "long": "0.30000000000000004", "zero": "0", "exp": "1.5e-1"}
	})
	for lit, want := range map[string]int64{"0.44": 440000, "0.1234565": 123457, "1.5e-1": 150000, "2": 2000000, "0.30000000000000004": 300001} {
		e.srv.Fault(podtest.OpGet, podtest.Fault{Status: 200, Body: `{"id":"p1","name":"x","status":"RUNNING","cost":` + lit + `}`})
		p, err := e.cl.GetPod(bg(), "p1")
		if err != nil || int64(p.CostPPH) != want || !p.Priced {
			t.Errorf("cost %s: %d %v, want %d", lit, p.CostPPH, err, want)
		}
	}
	for _, lit := range []string{"-0.1", `"x"`, `"1.2.3"`} {
		e.srv.Fault(podtest.OpGet, podtest.Fault{Status: 200, Body: `{"id":"p1","cost":` + lit + `}`})
		if _, err := e.cl.GetPod(bg(), "p1"); err == nil {
			t.Errorf("cost %s accepted", lit)
		}
	}
	e.srv.Fault(podtest.OpGet, podtest.Fault{Status: 200, Body: `{"id":"p1","name":"x","status":"RUNNING"}`})
	if p, err := e.cl.GetPod(bg(), "p1"); err != nil || p.CostPPH != 0 || p.Priced {
		t.Errorf("no cost: %+v %v", p, err)
	}

	prices, err := e.cl.CatalogSecurePrices(bg(), []string{"cheap", gpu4090, "exp"})
	if err != nil || prices["cheap"] != 340000 || prices[gpu4090] != 690000 || prices["exp"] != 150000 || len(prices) != 3 {
		t.Fatalf("prices %v %v", prices, err)
	}
	if r, _ := e.srv.Last(podtest.OpCatalog); !strings.Contains(r.Query, "cloud=SECURE") || !strings.Contains(r.Query, "product=POD") {
		t.Errorf("catalog query = %q", r.Query)
	}
	for _, id := range []string{"zero", "absent-type"} {
		_, err := e.cl.CatalogSecurePrices(bg(), []string{"cheap", id})
		var pe *pods.PriceError
		if !errors.As(err, &pe) || pe.TypeID != id || !strings.Contains(err.Error(), id) {
			t.Errorf("%s: err = %v, want PriceError", id, err)
		}
	}
	e.srv.Fault(podtest.OpCatalog, podtest.Fault{Status: 200, Body: `{"gpus":[{"id":"cheap","price":{"community":0.2}},{"id":"neg","price":{"secure":-1}}]}`})
	if _, err := e.cl.CatalogSecurePrices(bg(), []string{"cheap"}); err == nil {
		t.Error("a missing secure price was accepted")
	}
	e.srv.Fault(podtest.OpCatalog, podtest.Fault{Status: 200, Body: `{"gpus":[{"id":"neg","price":{"secure":-1}}]}`})
	if _, err := e.cl.CatalogSecurePrices(bg(), []string{"neg"}); err == nil {
		t.Error("a negative secure price was accepted")
	}
	e.srv.Fault(podtest.OpCatalog, podtest.Fault{Status: 200, Body: `nonsense`})
	var api *pods.APIError
	if _, err := e.cl.CatalogSecurePrices(bg(), []string{"cheap"}); !errors.As(err, &api) {
		t.Errorf("unreadable: %v", err)
	}
	if m, err := e.cl.CatalogSecurePrices(bg(), nil); err != nil || len(m) != 0 {
		t.Errorf("no ids: %v %v", m, err)
	}
	e.srv.Fault(podtest.OpCatalog, podtest.Fault{Status: 401})
	if _, err := e.cl.CatalogSecurePrices(bg(), []string{"cheap"}); !errors.Is(err, provider.ErrAccess) {
		t.Errorf("401: %v", err)
	}
}

func TestPodClient_BillingOwnPodsHourly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	h := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
	e.srv.SetBilling([]podtest.BillRecord{
		{Start: h(0), End: h(1), PodID: "mine1", Total: "0.5", GPU: "0.4", CPU: "0.05", Disk: "0.05"},
		{Start: h(0), End: h(1), PodID: "mine2", Total: "0.25", GPU: "0.2", CPU: "0.01", Disk: "0.04"},
		{Start: h(0), End: h(1), PodID: "someone-else", Total: "9", GPU: "9", CPU: "0", Disk: "0"},
		{Start: h(1), End: h(2), PodID: "mine1", Total: "0.1234565", GPU: "0.1234565"},
		{Start: h(2), End: h(3), PodID: "someone-else", Total: "1", GPU: "1"},
	})
	got, err := e.cl.PodBilling(bg(), h(0), h(3), []string{"mine1", "mine2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("buckets = %+v, want 2 (the foreign pod's buckets are dropped)", got)
	}
	if !got[0].Start.Equal(h(0)) || !got[0].End.Equal(h(1)) || got[0].Total != 750000 || got[0].Compute != 660000 || got[0].Disk != 90000 {
		t.Errorf("bucket 0 = %+v", got[0])
	}
	if !got[1].Start.Equal(h(1)) || got[1].Total != 123457 || got[1].Compute != 123457 || got[1].Disk != 0 {
		t.Errorf("bucket 1 = %+v", got[1])
	}
	if n := e.srv.Calls(podtest.OpBilling); n != 2 {
		t.Errorf("billing calls = %d, want one per own pod", n)
	}
	r, _ := e.srv.Last(podtest.OpBilling)
	for _, want := range []string{"podId=mine2", "bucketSize=hour", "startTime=2026-11-21T12%3A00%3A00Z", "endTime=2026-11-21T15%3A00%3A00Z"} {
		if !strings.Contains(r.Query, want) {
			t.Errorf("query %q lacks %s", r.Query, want)
		}
	}
	// no pods, no call
	calls := e.srv.Calls(podtest.OpBilling)
	if got, err := e.cl.PodBilling(bg(), h(0), h(3), nil); err != nil || got != nil || e.srv.Calls(podtest.OpBilling) != calls {
		t.Errorf("empty set: %v %v", got, err)
	}
	if got, err := e.cl.PodBilling(bg(), h(0), h(3), []string{"nobody"}); err != nil || len(got) != 0 {
		t.Errorf("no match: %v %v", got, err)
	}
	e.srv.Fault(podtest.OpBilling, podtest.Fault{Status: 200, Body: `{"records":[{"startTime":"2026-11-21T12:00:00Z","endTime":"2026-11-21T13:00:00Z","podId":"mine1","totalAmount":"abc"}]}`})
	var api *pods.APIError
	if _, err := e.cl.PodBilling(bg(), h(0), h(3), []string{"mine1"}); !errors.As(err, &api) {
		t.Errorf("bad amount: %v", err)
	}
	e.srv.Fault(podtest.OpBilling, podtest.Fault{Status: 200, Body: `[`})
	if _, err := e.cl.PodBilling(bg(), h(0), h(3), []string{"mine1"}); !errors.As(err, &api) {
		t.Errorf("unreadable: %v", err)
	}
	e.srv.Fault(podtest.OpBilling, podtest.Fault{Status: 403})
	if _, err := e.cl.PodBilling(bg(), h(0), h(3), []string{"mine1"}); !errors.Is(err, provider.ErrAccess) {
		t.Errorf("403: %v", err)
	}
}

func TestPodClient_LogsTailBounded(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *envOpts) { o.timeouts.Logs = 200 * time.Millisecond })
	e.srv.AddPod(podtest.Pod{ID: "p1", Name: podName})
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, "line "+string(rune('a'+i-1)))
	}
	lines[9] = "poll https://mm.example.com/worker/v1/pods/p1/poll?exp=1&sig=" + canary("sig") + " Authorization: Bearer " + e.key
	e.srv.SetLogs("p1", lines...)
	e.srv.HoldLogs(true) // the stream stays open: the client must stop on its own
	start := time.Now()
	got, err := e.cl.PodLogsTail(bg(), "p1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("the read was not bounded: %v", time.Since(start))
	}
	if len(got) != 3 || got[0] != "line h" || got[1] != "line i" {
		t.Errorf("lines = %q", got)
	}
	joined := strings.Join(got, "\n")
	for _, leak := range []string{canary("sig"), e.key, "sig=", "/pods/p1/"} {
		if strings.Contains(joined, leak) {
			t.Errorf("a log line leaked %q: %q", leak, joined)
		}
	}
	r, _ := e.srv.Last(podtest.OpLogs)
	if !strings.Contains(r.Query, "source=container") || !strings.Contains(r.Query, "tail=3") || r.Header.Get("Accept") != "text/event-stream" {
		t.Errorf("request = %q %v", r.Query, r.Header)
	}
	// the stream ends by itself: all lines up to n
	e.srv.HoldLogs(false)
	if got, err := e.cl.PodLogsTail(bg(), "p1", 50); err != nil || len(got) != 10 {
		t.Errorf("lines = %d %v", len(got), err)
	}
	for _, n := range []int{0, -1, pods.MaxLogTail + 1} {
		if _, err := e.cl.PodLogsTail(bg(), "p1", n); err == nil {
			t.Errorf("tail %d accepted", n)
		}
	}
	if _, err := e.cl.PodLogsTail(bg(), "../x", 5); err == nil {
		t.Error("a bad id was accepted")
	}
	if _, err := e.cl.PodLogsTail(bg(), "unknown", 5); !errors.Is(err, pods.ErrPodGone) {
		t.Errorf("unknown pod: %v", err)
	}
	e.srv.Fault(podtest.OpLogs, podtest.Fault{Status: 403})
	if _, err := e.cl.PodLogsTail(bg(), "p1", 5); !errors.Is(err, provider.ErrAccess) {
		t.Errorf("403: %v", err)
	}
	// plain text events and a read cut at the response bound
	e.srv.Fault(podtest.OpLogs, podtest.Fault{Status: 200, Body: "id: 1\ndata: not json\n\n: comment\ndata: {\"line\":\"two\"}\n\n"})
	if got, err := e.cl.PodLogsTail(bg(), "p1", 5); err != nil || len(got) != 2 || got[0] != "not json" || got[1] != "two" {
		t.Errorf("lines = %q %v", got, err)
	}
	e.srv.Fault(podtest.OpLogs, podtest.Fault{Status: 200, Body: strings.Repeat("data: x\n", pods.MaxResponse/8+10)})
	if got, err := e.cl.PodLogsTail(bg(), "p1", 5); err != nil || len(got) != 5 {
		t.Errorf("oversized stream: %d %v", len(got), err)
	}
	// the caller's own cancellation is an error, not a quiet end
	ctx, cancel := contextCancelled()
	defer cancel()
	e.srv.HoldLogs(true)
	if _, err := e.cl.PodLogsTail(ctx, "p1", 5); err == nil {
		t.Error("a cancelled context returned lines")
	}
}

func TestPodClient_ResponseBounds(t *testing.T) {
	t.Parallel()
	big := `{"id":"p1","name":"` + strings.Repeat("a", pods.MaxResponse) + `"}`
	e := newEnv(t)
	e.srv.Fault(podtest.OpGet, podtest.Fault{Status: 200, Body: big})
	var api *pods.APIError
	if _, err := e.cl.GetPod(bg(), "p1"); !errors.As(err, &api) || !strings.Contains(err.Error(), "too large") {
		t.Errorf("GET: %v", err)
	}
	// a create whose answer cannot be read has taken effect
	e.srv.Fault(podtest.OpCreate, podtest.Fault{Status: 201, Body: big})
	var amb *pods.ErrAmbiguous
	if _, err := e.cl.CreatePod(bg(), body()); !errors.As(err, &amb) {
		t.Errorf("create: %v", err)
	}
}

func TestPodClient_KeyReadPerCall(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.AddPod(podtest.Pod{ID: "p1", Name: podName})
	if _, err := e.cl.GetPod(bg(), "p1"); err != nil {
		t.Fatal(err)
	}
	calls := len(e.srv.Requests())

	// removed: the next call makes no request at all
	if err := os.Remove(filepath.Join(e.dir, "runpod-pod.env")); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func() error{
		"get":    func() error { _, err := e.cl.GetPod(bg(), "p1"); return err },
		"list":   func() error { _, err := e.cl.ListPods(bg()); return err },
		"delete": func() error { return e.cl.DeletePod(bg(), "p1") },
		"create": func() error { _, err := e.cl.CreatePod(bg(), body()); return err },
		"logs":   func() error { _, err := e.cl.PodLogsTail(bg(), "p1", 1); return err },
	} {
		err := f()
		if !errors.Is(err, pods.ErrNoKey) || !errors.Is(err, provider.ErrNotPermitted) {
			t.Errorf("%s: err = %v, want ErrNoKey", name, err)
		}
	}
	if len(e.srv.Requests()) != calls {
		t.Errorf("a request was sent without a key: %d -> %d", calls, len(e.srv.Requests()))
	}

	// a loose mode is refused with config's message, no request
	writeKey(t, e.dir, e.key, 0o644)
	if _, err := e.cl.GetPod(bg(), "p1"); err == nil || !strings.Contains(err.Error(), "runpod-pod.env") || strings.Contains(err.Error(), e.key) {
		t.Errorf("loose mode: %v", err)
	}
	if len(e.srv.Requests()) != calls {
		t.Error("a request was sent with a refused key file")
	}

	// restored, then rotated: the new value is used at once (the fake answers 401, which is an access error without the key text)
	writeKey(t, e.dir, e.key, 0o600)
	if _, err := e.cl.GetPod(bg(), "p1"); err != nil {
		t.Fatal(err)
	}
	rotated := e.key + "-rotated"
	writeKey(t, e.dir, rotated, 0o400)
	_, err := e.cl.GetPod(bg(), "p1")
	if !errors.Is(err, provider.ErrAccess) {
		t.Fatalf("rotated key: %v", err)
	}
	if r, _ := e.srv.Last(podtest.OpGet); r.Header.Get("Authorization") != "Bearer "+rotated {
		t.Error("the rotated key was not sent")
	}
	if strings.Contains(err.Error(), rotated) || strings.Contains(err.Error(), e.key) {
		t.Errorf("the error carries the key: %v", err)
	}
}

func TestPodClient_ErrorsNeverLeak(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sig := canary("sig")
	hostile := "echo " + e.key + " at /v2/pods/7h9k2m4n6p/logs and https://mm.example.com/worker/v1/pods/7h9k2m4n6p/poll?exp=1&sig=" + sig +
		" Authorization: Bearer " + e.key
	for _, op := range []string{podtest.OpGet, podtest.OpList, podtest.OpDelete, podtest.OpCreate, podtest.OpCatalog, podtest.OpBilling, podtest.OpLogs} {
		e.srv.Fault(op, podtest.Fault{Count: 5, Status: 418, Detail: hostile})
	}
	e.srv.Fault(podtest.OpGet, podtest.Fault{Count: 5, Status: 418, Detail: hostile})
	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: no error", name)
			return
		}
		s := err.Error()
		var api *pods.APIError
		var ref *pods.ErrRefused
		if errors.As(err, &api) {
			s += " " + api.Detail
		}
		if errors.As(err, &ref) {
			s += " " + ref.Detail
		}
		for _, leak := range []string{e.key, sig, "7h9k2m4n6p", "sig="} {
			if strings.Contains(s, leak) {
				t.Errorf("%s: the error leaked %q: %s", name, leak, s)
			}
		}
	}
	_, err := e.cl.GetPod(bg(), "p1")
	check("get", err)
	_, err = e.cl.ListPods(bg())
	check("list", err)
	check("delete", e.cl.DeletePod(bg(), "p1"))
	_, err = e.cl.CreatePod(bg(), body())
	check("create", err)
	_, err = e.cl.CatalogSecurePrices(bg(), []string{gpu4090})
	check("catalog", err)
	_, err = e.cl.PodBilling(bg(), t0, t0.Add(time.Hour), []string{"p"})
	check("billing", err)
	e.srv.AddPod(podtest.Pod{ID: "p1"})
	_, err = e.cl.PodLogsTail(bg(), "p1", 3)
	check("logs", err)

	// a transport failure: the URL (and with it the base) is dropped
	e2 := newEnv(t)
	e2.srv.Fault(podtest.OpGet, podtest.Fault{Reset: true, Count: 10})
	var err2 error
	drive(t, e2.clk, func() { _, err2 = e2.cl.GetPod(bg(), "p1") })
	if err2 == nil || strings.Contains(err2.Error(), e2.srv.URL) || strings.Contains(err2.Error(), "/v2/pods") {
		t.Errorf("transport error = %v", err2)
	}
}
