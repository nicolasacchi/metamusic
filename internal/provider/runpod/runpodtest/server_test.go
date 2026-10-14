package runpodtest_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
)

func keys() runpodtest.KeySet {
	pad := strings.Repeat("k", 10)
	return runpodtest.KeySet{Jobs: "canary-jobs-" + pad, Read: "canary-read-" + pad, Write: "canary-write-" + pad, Admin: "canary-admin-" + pad}
}

func newSrv(t *testing.T, o runpodtest.Options) (*runpodtest.Server, runpodtest.KeySet) {
	t.Helper()
	k := keys()
	o.Keys, o.EndpointID = k, "ep-canary-0000"
	return runpodtest.New(t, o), k
}

func call(t *testing.T, s *runpodtest.Server, method, path, tok, body string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, s.URL+path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestRunpodtest_PermissionModes(t *testing.T) {
	t.Parallel()
	s, k := newSrv(t, runpodtest.Options{})
	ep := "/v2/serverless/ep-canary-0000"
	jobs := "/v2/ep-canary-0000"
	type tc struct {
		name, method, path, body string
		allowed                  map[string]bool // key kind => allowed
	}
	jr := map[string]bool{"jobs": true, "admin": true}
	rd := map[string]bool{"read": true, "write": true, "admin": true}
	cases := []tc{
		{"run", "POST", jobs + "/run", `{"input":{"a":1}}`, jr},
		{"status", "GET", jobs + "/status/nope", "", map[string]bool{"jobs": true, "admin": true}}, // 404 counts as allowed below
		{"health", "GET", jobs + "/health", "", jr},
		{"purge", "POST", jobs + "/purge-queue", "", jr},
		{"get endpoint", "GET", ep, "", rd},
		{"workers", "GET", ep + "/workers", "", rd},
		{"catalog", "GET", "/v2/catalog/gpus", "", rd},
		{"billing", "GET", "/v2/billing/serverless", "", rd},
		{"graphql", "POST", "/graphql", `{}`, rd},
		{"patch", "PATCH", ep, `{"name":"mm-yue2"}`, map[string]bool{"write": true, "admin": true}},
		{"create", "POST", "/v2/serverless", `{"name":"n"}`, map[string]bool{"admin": true}},
	}
	toks := map[string]string{"jobs": k.Jobs, "read": k.Read, "write": k.Write, "admin": k.Admin}
	for _, c := range cases {
		for kind, tok := range toks {
			code, _, _ := call(t, s, c.method, c.path, tok, c.body)
			ok := code != 403 && code != 401
			if ok != c.allowed[kind] {
				t.Errorf("%s with %s key: status %d, allowed=%v", c.name, kind, code, c.allowed[kind])
			}
		}
		if code, _, _ := call(t, s, c.method, c.path, "", c.body); code != 401 {
			t.Errorf("%s without a bearer: %d", c.name, code)
		}
		if code, _, _ := call(t, s, c.method, c.path, "wrong", c.body); code != 401 {
			t.Errorf("%s with a wrong bearer: %d", c.name, code)
		}
	}
	// the catalog answers 401 without a bearer
	if code, _, _ := call(t, s, "GET", "/v2/catalog/gpus", "", ""); code != 401 {
		t.Fatal(code)
	}
	// custom permission override
	s.SetAllow(runpodtest.KindRead, runpodtest.Allow{runpodtest.OpWorkers: true})
	if code, _, _ := call(t, s, "GET", ep, k.Read, ""); code != 403 {
		t.Fatalf("overridden: %d", code)
	}
	if code, _, _ := call(t, s, "GET", ep+"/workers", k.Read, ""); code != 200 {
		t.Fatalf("overridden: %d", code)
	}
	if s.Calls(runpodtest.OpCatalog) < 5 {
		t.Fatal("counters do not count refused calls")
	}
}

func TestRunpodtest_PatchModes(t *testing.T) {
	t.Parallel()
	for _, replace := range []bool{false, true} {
		s, k := newSrv(t, runpodtest.Options{PatchReplacesNested: replace})
		ep := "/v2/serverless/ep-canary-0000"
		if code, _, _ := call(t, s, "PATCH", ep, k.Write, `{"workers":{"min":0}}`); code != 200 {
			t.Fatal(code)
		}
		w := s.Endpoint()["workers"].(map[string]any)
		if replace {
			if runpodtest.Num(w["max"]) != 3 {
				t.Fatalf("replace mode must default workers.max to 3: %v", w)
			}
		} else if runpodtest.Num(w["max"]) != 1 || runpodtest.Num(w["idleTimeout"]) != 20 {
			t.Fatalf("merge mode lost fields: %v", w)
		}
	}
	s, k := newSrv(t, runpodtest.Options{PatchForbidden: true})
	if code, _, _ := call(t, s, "PATCH", "/v2/serverless/ep-canary-0000", k.Admin, `{}`); code != 403 {
		t.Fatal(code)
	}
}

func TestRunpodtest_Faults(t *testing.T) {
	t.Parallel()
	s, k := newSrv(t, runpodtest.Options{})
	jobs := "/v2/ep-canary-0000"
	s.Fault(runpodtest.OpRun, runpodtest.Fault{Status: 429, Header: http.Header{"Retry-After": {"7"}, "Ratelimit": {`"minute";r=0;t=3`}}})
	code, _, h := call(t, s, "POST", jobs+"/run", k.Jobs, `{"input":{"a":1}}`)
	if code != 429 || h.Get("Retry-After") != "7" || h.Get("RateLimit") == "" {
		t.Fatalf("%d %v", code, h)
	}
	if code, _, _ := call(t, s, "POST", jobs+"/run", k.Jobs, `{"input":{"a":1}}`); code != 200 {
		t.Fatalf("fault applied twice: %d", code)
	}
	// accepted then 5xx: the job exists
	n := len(s.JobIDs())
	s.Fault(runpodtest.OpRun, runpodtest.Fault{Status: 503, AfterAccept: true})
	if code, _, _ := call(t, s, "POST", jobs+"/run", k.Jobs, `{"input":{"a":1}}`); code != 503 {
		t.Fatal(code)
	}
	if len(s.JobIDs()) != n+1 {
		t.Fatal("job not created behind the 5xx")
	}
	// 1010 body
	s.Fault(runpodtest.OpHealth, runpodtest.Fault{Status: 403, Body: "error code: 1010"})
	if code, b, _ := call(t, s, "GET", jobs+"/health", k.Jobs, ""); code != 403 || !strings.Contains(b, "1010") {
		t.Fatal(code, b)
	}
	// unknown route
	if code, _, _ := call(t, s, "GET", "/nope", k.Jobs, ""); code != 404 {
		t.Fatal(code)
	}
}

func TestRunpodtest_JobBehaviors(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewManual(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))
	s, k := newSrv(t, runpodtest.Options{Clock: clk, PurgeAfter: 30 * time.Minute})
	jobs := "/v2/ep-canary-0000"
	run := func(b runpodtest.Behavior) string {
		s.NextJob(b)
		_, body, _ := call(t, s, "POST", jobs+"/run", k.Jobs, `{"input":{"a":1}}`)
		var r struct{ ID string }
		_ = json.Unmarshal([]byte(body), &r)
		return r.ID
	}
	status := func(id string) map[string]any {
		_, body, _ := call(t, s, "GET", jobs+"/status/"+id, k.Jobs, "")
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		return m
	}
	exp := run(runpodtest.Behavior{Expire: true})
	if m := status(exp); m["status"] != "TIMED_OUT" || m["executionTime"] != nil {
		t.Fatalf("expire: %v", m)
	}
	dead := run(runpodtest.Behavior{WorkerDeath: true})
	status(dead)
	if m := status(dead); m["status"] != "FAILED" || m["output"] != nil || m["executionTime"] != nil {
		t.Fatalf("worker death: %v", m)
	}
	hold := run(runpodtest.Behavior{Hold: true})
	for i := 0; i < 5; i++ {
		if m := status(hold); m["status"] != "IN_QUEUE" {
			t.Fatalf("hold: %v", m)
		}
	}
	ok := run(runpodtest.Behavior{})
	var last map[string]any
	for i := 0; i < 3; i++ {
		last = status(ok)
	}
	if last["status"] != "COMPLETED" || last["executionTime"] == nil {
		t.Fatalf("normal: %v", last)
	}
	clk.Advance(31 * time.Minute)
	if code, _, _ := call(t, s, "GET", jobs+"/status/"+ok, k.Jobs, ""); code != 404 {
		t.Fatalf("results should be purged after 30 minutes: %d", code)
	}
	if code, _, _ := call(t, s, "GET", jobs+"/status/"+hold, k.Jobs, ""); code != 200 {
		t.Fatalf("unfinished jobs are never purged: %d", code)
	}
	// purge queue cancels queued jobs; cancel finishes running ones
	call(t, s, "POST", jobs+"/purge-queue", k.Jobs, "")
	if s.JobState(hold) != "CANCELLED" {
		t.Fatalf("purge left %s", s.JobState(hold))
	}
	if s.JobState("nope") != "" {
		t.Fatal("unknown job has a state")
	}
}

// uploadSink is a stand-in for the app's upload and start endpoints.
type uploadSink struct {
	mu    sync.Mutex
	puts  map[string]int
	start int
}

func (u *uploadSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	u.mu.Lock()
	defer u.mu.Unlock()
	if strings.Contains(r.URL.Path, "/start") {
		u.start++
		w.WriteHeader(200)
		return
	}
	u.puts[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]]++
	w.WriteHeader(201)
}

func TestRunpodtest_FakeWorkerRoundTrip(t *testing.T) {
	t.Parallel()
	sink := &uploadSink{puts: map[string]int{}}
	up := httptest.NewServer(sink)
	t.Cleanup(up.Close)

	headerOnly := func(id uint64, seconds float64) []byte {
		frames := bytes.Repeat([]byte{byte(id)}, 64)
		return flactest.Build(flactest.Spec{SampleRate: 48000, Channels: 2, BitsPerSample: 24, TotalSamples: uint64(seconds * 48000), Vendor: "libFLAC", Frames: frames})
	}
	sim := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: fake.HTTPUploader{}, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir()})
	s, k := newSrv(t, runpodtest.Options{Runner: runpodtest.FakeWorker(sim)})
	b := runpod.NewBackend(runpod.BackendOptions{Keys: configKeys(k, "ep-canary-0000"), MgmtBase: s.URL, JobsBase: s.URL, Version: "test",
		MaxRuntime: 15 * time.Minute, ColdStartS: 180})

	ups := map[string]provider.UploadTarget{}
	for _, a := range provider.Artifacts {
		ups[a.Name] = provider.UploadTarget{URL: up.URL + "/uploads/att1/" + a.Name, MaxBytes: a.MaxBytes}
	}
	spec := provider.TakeSpec{JobID: "job1", AttemptID: "att1", Engine: "yue2",
		Request:  json.RawMessage(`{"cot":"off","id":"syn-a-s1","lyrics":"[Verse]\nla","seed":1,"style":"pop"}`),
		TimeoutS: 186, TTL: 2 * time.Hour, UploadBudgetS: 60, Uploads: ups, StartURL: up.URL + "/attempts/att1/start?exp=1&sig=x",
		Expect: provider.WorkerExpect{Weights: yue2.Weights}, Deadline: time.Now().Add(time.Hour)}
	ref, err := b.Submit(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	var rs provider.RemoteStatus
	for i := 0; i < 3; i++ {
		if rs, err = b.Poll(t.Context(), ref); err != nil {
			t.Fatal(err)
		}
	}
	if rs.State != provider.Completed {
		t.Fatalf("state %s (%s)", rs.State, rs.ErrorText)
	}
	out, err := provider.DecodeOutput(rs.Output)
	if err != nil || out.Status != "ok" || out.AttemptID != "att1" {
		t.Fatalf("output = %+v %v", out, err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.start == 0 || sink.puts["audio.flac"] != 1 || sink.puts["mm-result.json"] != 1 {
		t.Fatalf("starts %d puts %v", sink.start, sink.puts)
	}
}

func configKeys(k runpodtest.KeySet, ep string) config.RunPodKeys {
	return config.RunPodKeys{Jobs: k.Jobs, Read: k.Read, Write: k.Write, EndpointID: ep}
}
