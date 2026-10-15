package runpod_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
)

// recFunc adapts a function to runpod.Recorder.
type recFunc func(runpod.Exchange)

func (f recFunc) Record(x runpod.Exchange) { f(x) }

// probeEnv is an inactive endpoint (workers.max = 0) with the three non-admin keys.
func probeEnv(t *testing.T, mod ...func(*envOpts)) *env {
	t.Helper()
	e := newEnv(t, mod...)
	e.srv.SetEndpoint(func(ep map[string]any) { ep["workers"].(map[string]any)["max"] = 0 })
	return e
}

func runProbe(t *testing.T, e *env) (*runpod.KeysReport, error) {
	t.Helper()
	var rep *runpod.KeysReport
	var err error
	drive(t, e.clk, func() {
		rep, err = runpod.ProbeKeys(bg(), runpod.ProbeOptions{Client: e.cl, Keys: e.keys, Clock: e.clk})
	})
	return rep, err
}

func TestProbe_SchemaDiff(t *testing.T) {
	t.Parallel()
	snap := runpod.Snapshot()
	t.Run("identical", func(t *testing.T) {
		t.Parallel()
		r, err := runpod.CompareSchema(snap, snap)
		if err != nil {
			t.Fatal(err)
		}
		if r.Drift || !r.OK || len(r.Changed) != 0 || r.SnapshotSHA != r.LiveSHA {
			t.Fatalf("report %+v", r)
		}
		if r.CachedModelField {
			t.Error("the snapshot has no cached-model field")
		}
		if strings.Join(r.FlashbootValues, ",") != "OFF,FLASHBOOT,PRIORITY_FLASHBOOT" {
			t.Errorf("flashboot = %v", r.FlashbootValues)
		}
	})
	t.Run("harmless change is drift but not a failure", func(t *testing.T) {
		t.Parallel()
		live := bytes.Replace(snap, []byte(`"Half-open hourly buckets;`), []byte(`"Half-open hourly buckets, now documented;`), 1)
		if bytes.Equal(live, snap) {
			t.Fatal("test edit did not apply")
		}
		r, err := runpod.CompareSchema(snap, live)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Drift || !r.OK || len(r.Changed) != 1 || !strings.Contains(r.Changed[0], "BillingRecord") {
			t.Fatalf("report %+v", r)
		}
	})
	t.Run("renamed minCudaVersion fails", func(t *testing.T) {
		t.Parallel()
		live := bytes.ReplaceAll(snap, []byte(`"minCudaVersion"`), []byte(`"minCudaVer"`))
		r, err := runpod.CompareSchema(snap, live)
		if err != nil {
			t.Fatal(err)
		}
		if r.OK || !r.Drift {
			t.Fatalf("report %+v", r)
		}
		found := false
		for _, c := range r.Changed {
			found = found || strings.Contains(c, "minCuda")
		}
		if !found {
			t.Errorf("changed paths %v", r.Changed)
		}
		if !strings.Contains(strings.Join(r.Failures, "|"), "minCudaVersion") {
			t.Errorf("failures %v", r.Failures)
		}
	})
	t.Run("cached model field is reported", func(t *testing.T) {
		t.Parallel()
		live := bytes.Replace(snap, []byte(`"flashboot": {`), []byte(`"cachedModels": {"type": "array"}, "flashboot": {`), 1)
		r, err := runpod.CompareSchema(snap, live)
		if err != nil || !r.CachedModelField {
			t.Fatalf("report %+v err %v", r, err)
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		t.Parallel()
		if _, err := runpod.CompareSchema(snap, []byte("<html>")); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestProbe_ClassifiesAgainstFake(t *testing.T) {
	t.Parallel()
	t.Run("full permissions", func(t *testing.T) {
		t.Parallel()
		e := probeEnv(t)
		rep, err := runProbe(t, e)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]map[string]string{
			"jobs":  {"health": "allowed", "status": "allowed", "cancel": "allowed", "retry": "allowed", "purge_queue": "allowed", "run": "allowed"},
			"read":  {"get_endpoint": "allowed", "workers": "allowed", "catalog": "allowed", "billing": "allowed", "graphql_balance": "allowed", "endpoint_patch": "forbidden"},
			"write": {"endpoint_patch": "allowed"},
		}
		for kind, calls := range want {
			for name, v := range calls {
				if got := rep.Keys[kind][name].Verdict; got != v {
					t.Errorf("%s.%s = %q want %q", kind, name, got, v)
				}
			}
		}
		if !rep.RetryAllowed || !rep.BillingReadable || !rep.Recommendation.WriteKeyScoped || rep.Recommendation.Fallback != "patch" {
			t.Errorf("report %+v", rep)
		}
	})
	t.Run("jobs key limited to health", func(t *testing.T) {
		t.Parallel()
		e := probeEnv(t, func(o *envOpts) {
			o.srv.Allow = map[runpodtest.Kind]runpodtest.Allow{runpodtest.KindJobs: {runpodtest.OpHealth: true}}
		})
		rep, err := runProbe(t, e)
		if err != nil {
			t.Fatal(err)
		}
		if rep.RetryAllowed || rep.Keys["jobs"]["cancel"].Verdict != "forbidden" || rep.Keys["jobs"]["status"].Verdict != "forbidden" {
			t.Errorf("jobs matrix %+v retry=%v", rep.Keys["jobs"], rep.RetryAllowed)
		}
		if e.srv.Calls(runpodtest.OpRun) != 0 && rep.Keys["jobs"]["run"].Verdict == "allowed" {
			t.Error("run must not be allowed")
		}
	})
	t.Run("read key only reads, write patch refused", func(t *testing.T) {
		t.Parallel()
		e := probeEnv(t, func(o *envOpts) { o.srv.PatchForbidden = true })
		rep, err := runProbe(t, e)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Keys["write"]["endpoint_patch"].Verdict != "forbidden" || rep.Recommendation.WriteKeyScoped || rep.Recommendation.Fallback != "manual-max-0" || rep.Recommendation.VAE != "image" {
			t.Errorf("write matrix %+v rec %+v", rep.Keys["write"], rep.Recommendation)
		}
	})
	t.Run("absent write key is not recorded", func(t *testing.T) {
		t.Parallel()
		e := probeEnv(t, func(o *envOpts) { o.noWrite = true })
		rep, err := runProbe(t, e)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := rep.Keys["write"]; ok || rep.Recommendation.WriteKeyScoped {
			t.Errorf("keys = %+v", rep.Keys)
		}
	})
	t.Run("edge block", func(t *testing.T) {
		t.Parallel()
		e := probeEnv(t)
		e.srv.Fault(runpodtest.OpCatalog, runpodtest.Fault{Status: 403, Body: "error code: 1010", Times: -1})
		rep, err := runProbe(t, e)
		if err != nil {
			t.Fatal(err)
		}
		if got := rep.Keys["read"]["catalog"].Verdict; got != "edge_blocked" {
			t.Errorf("catalog = %q", got)
		}
	})
}

func TestProbe_NeverRunsWhenActive(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.SetEndpoint(func(ep map[string]any) { ep["workers"].(map[string]any)["max"] = 2 })
	rep, err := runProbe(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Keys["jobs"]["run"].Verdict != "skipped" || !strings.Contains(rep.Keys["jobs"]["run"].Note, "active") {
		t.Errorf("run = %+v", rep.Keys["jobs"]["run"])
	}
	if n := e.srv.Calls(runpodtest.OpRun); n != 0 {
		t.Errorf("/run reached the server %d times", n)
	}
	if n := e.srv.Calls(runpodtest.OpPurge); n != 0 {
		t.Errorf("purge-queue reached an active endpoint %d times", n)
	}
	if rep.IdleBillingZero24h != nil {
		t.Error("an active endpoint has no idle-billing conclusion")
	}
}

func TestProbe_RunRecheckedBeforeEachCall(t *testing.T) {
	t.Parallel()
	var e *env
	e = probeEnv(t, func(o *envOpts) {
		o.client = func(co *runpod.ClientOptions) {
			co.Recorder = recFunc(func(x runpod.Exchange) {
				if x.Op == "run" {
					e.srv.SetEndpoint(func(ep map[string]any) { ep["workers"].(map[string]any)["max"] = 1 })
				}
			})
		}
	})
	rep, err := runProbe(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.srv.Calls(runpodtest.OpRun); n != 1 {
		t.Fatalf("/run calls = %d, want exactly 1", n)
	}
	if rep.Keys["jobs"]["run"].Verdict != "allowed" || rep.Keys["jobs"]["run_b"].Verdict != "skipped" {
		t.Errorf("jobs = %+v", rep.Keys["jobs"])
	}
}

func TestProbe_RecordsQueueShapes(t *testing.T) {
	t.Parallel()
	t.Run("ttl never reached: observation lasts about 30 s", func(t *testing.T) {
		t.Parallel()
		e := probeEnv(t)
		e.srv.NextJob(runpodtest.Behavior{Hold: true})
		rep, err := runProbe(t, e)
		if err != nil {
			t.Fatal(err)
		}
		if rep.TTLExpiry == nil || rep.TTLExpiry.Terminal || rep.TTLExpiry.Polls < 10 || rep.TTLExpiry.Polls > 20 {
			t.Errorf("ttl %+v", rep.TTLExpiry)
		}
		if rep.CancelledShape == nil || rep.CancelledShape.CancelStatus != "CANCELLED" || rep.CancelledShape.ExecutionTimeMS != nil {
			t.Errorf("cancelled %+v", rep.CancelledShape)
		}
		if rep.JobNotFound == nil || rep.JobNotFound.HTTP != 404 || rep.JobNotFound.Code != "NOT_FOUND" {
			t.Errorf("job_not_found %+v", rep.JobNotFound)
		}
		if rep.EndpointNotFound == nil {
			t.Error("endpoint_not_found missing")
		}
		last, ok := e.srv.Last(runpodtest.OpRun)
		if !ok {
			t.Fatal("no /run")
		}
		var body struct {
			Input  map[string]any `json:"input"`
			Policy map[string]any `json:"policy"`
		}
		if err := json.Unmarshal(last.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Policy["executionTimeout"] != float64(5000) || body.Policy["ttl"] != float64(10000) || body.Input["contract"] != "probe" {
			t.Errorf("run body = %s", last.Body)
		}
		if e.srv.Calls(runpodtest.OpRun) != 2 {
			t.Errorf("runs = %d", e.srv.Calls(runpodtest.OpRun))
		}
	})
	t.Run("ttl expiry shape", func(t *testing.T) {
		t.Parallel()
		e := probeEnv(t)
		e.srv.NextJob(runpodtest.Behavior{Expire: true})
		rep, err := runProbe(t, e)
		if err != nil {
			t.Fatal(err)
		}
		if rep.TTLExpiry == nil || !rep.TTLExpiry.Terminal || rep.TTLExpiry.Status != "TIMED_OUT" || rep.TTLExpiry.HTTP != 200 {
			t.Fatalf("ttl %+v", rep.TTLExpiry)
		}
		if rep.NeverRanShapes == nil || rep.NeverRanShapes.TTLHasExecutionTime || rep.NeverRanShapes.CancelledHasExecutionTime {
			t.Errorf("never ran %+v", rep.NeverRanShapes)
		}
	})
}

func TestProbe_NameOnlyPatch(t *testing.T) {
	t.Parallel()
	t.Run("sends only the name; nested objects survive a replacing server", func(t *testing.T) {
		t.Parallel()
		e := probeEnv(t, func(o *envOpts) { o.srv.PatchReplacesNested = true })
		if _, err := runProbe(t, e); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range e.srv.Requests() {
			if r.Op != runpodtest.OpPatchEndpoint {
				continue
			}
			n++
			var m map[string]any
			if err := json.Unmarshal(r.Body, &m); err != nil || len(m) != 1 || m["name"] != "mm-yue2" {
				t.Errorf("patch body = %s", r.Body)
			}
		}
		if n != 2 {
			t.Errorf("patches = %d (write and read key)", n)
		}
		if got := runpodtest.Num(e.srv.Endpoint()["workers"].(map[string]any)["max"]); got != 0 {
			t.Errorf("workers.max = %d after the probe", got)
		}
	})
	t.Run("a changed GET aborts with a critical error", func(t *testing.T) {
		t.Parallel()
		var e *env
		e = probeEnv(t, func(o *envOpts) {
			o.client = func(co *runpod.ClientOptions) {
				co.Recorder = recFunc(func(x runpod.Exchange) {
					if x.Op == "patch_endpoint" {
						e.srv.SetEndpoint(func(ep map[string]any) { ep["workers"].(map[string]any)["max"] = 3 })
					}
				})
			}
		})
		_, err := runProbe(t, e)
		if !errors.Is(err, runpod.ErrProbeCritical) {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "CRITICAL") {
			t.Errorf("message %q", err)
		}
	})
}

func TestProbe_IdleBillingZero(t *testing.T) {
	t.Parallel()
	rec := func(total float64) []map[string]any {
		return []map[string]any{{"startTime": "2026-10-14T10:00:00Z", "endTime": "2026-10-14T11:00:00Z", "serverlessId": "x",
			"totalAmount": total, "gpuAmount": total, "cpuAmount": 0, "diskAmount": 0, "feeAmount": 0}}
	}
	for _, tc := range []struct {
		name string
		b    []map[string]any
		want bool
	}{{"zero", rec(0), true}, {"empty", nil, true}, {"billed", rec(0.5), false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := probeEnv(t)
			e.srv.SetBilling(tc.b)
			rep, err := runProbe(t, e)
			if err != nil {
				t.Fatal(err)
			}
			if rep.IdleBillingZero24h == nil || *rep.IdleBillingZero24h != tc.want || !rep.BillingReadable {
				t.Errorf("idle zero = %v readable=%v", rep.IdleBillingZero24h, rep.BillingReadable)
			}
		})
	}
}

func TestProbe_NeverReadsAdminKey(t *testing.T) {
	t.Parallel()
	e := probeEnv(t)
	if _, err := runProbe(t, e); err != nil {
		t.Fatal(err)
	}
	for _, r := range e.srv.Requests() {
		if r.Key == runpodtest.KindAdmin || strings.Contains(r.Header.Get("Authorization"), e.set.Admin) {
			t.Fatalf("%s %s used the admin key", r.Method, r.Path)
		}
		if r.Op == runpodtest.OpCreateEndpoint {
			t.Fatal("the probe created an endpoint")
		}
	}
}

func TestKeyCapabilities_FileSafe(t *testing.T) {
	t.Parallel()
	t.Run("committed file", func(t *testing.T) {
		t.Parallel()
		raw, err := os.ReadFile("key-capabilities.json")
		if err != nil {
			t.Fatal(err)
		}
		var c runpod.Capabilities
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		if !c.Probed || !c.BillingReadable || c.Recommendation.WriteKeyScoped || c.Recommendation.Fallback != "manual-max-0" || c.Recommendation.VAE != "image" {
			t.Errorf("committed file is not the real key probe: %+v", c)
		}
		if err := runpod.ValidateCapabilities(raw); err != nil {
			t.Error(err)
		}
		if err := runpod.CheckFileSafe(raw, ""); err != nil {
			t.Error(err)
		}
		if !bytes.Equal(runpod.EmbeddedCapabilities, raw) {
			t.Error("the embedded copy differs from the file")
		}
	})
	t.Run("probe output is safe and valid", func(t *testing.T) {
		t.Parallel()
		e := probeEnv(t)
		rep, err := runProbe(t, e)
		if err != nil {
			t.Fatal(err)
		}
		schema, err := runpod.CompareSchema(runpod.Snapshot(), runpod.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.MarshalIndent(runpod.BuildCapabilities(t0, &schema, rep), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := runpod.CheckFileSafe(raw, e.epRaw); err != nil {
			t.Errorf("%v\n%s", err, raw)
		}
		if err := runpod.ValidateCapabilities(raw); err != nil {
			t.Errorf("%v\n%s", err, raw)
		}
		for _, secret := range []string{e.set.Jobs, e.set.Read, e.set.Write, e.set.Admin, e.epRaw} {
			if strings.Contains(string(raw), secret) {
				t.Error("a key or the endpoint id is in the output")
			}
		}
		var c runpod.Capabilities
		if err := json.Unmarshal(raw, &c); err != nil || !c.Probed {
			t.Errorf("probed = %v err %v", c.Probed, err)
		}
	})
	t.Run("schema report with long identifiers and digests passes", func(t *testing.T) {
		t.Parallel()
		// a live spec that gained a component whose name exceeds 31 characters: the changed paths carry it
		long := "ServerlessEndpointCreateRequestWithNetworkVolumeAndScalerSettings"
		var spec map[string]any
		if err := json.Unmarshal(runpod.Snapshot(), &spec); err != nil {
			t.Fatal(err)
		}
		spec["components"].(map[string]any)["schemas"].(map[string]any)[long] = map[string]any{"type": "object"}
		live, _ := json.Marshal(spec)
		schema, err := runpod.CompareSchema(runpod.Snapshot(), live)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(schema.Changed, " "), long) {
			t.Fatalf("test premise: changed = %v", schema.Changed)
		}
		raw, _ := json.MarshalIndent(runpod.BuildCapabilities(t0, &schema, nil), "", "  ")
		if err := runpod.CheckFileSafe(raw, "ep123"); err != nil {
			t.Errorf("a schema report with long identifiers was refused: %v\n%s", err, raw)
		}
		// full SHA-256 digests in the digest fields
		schema.SnapshotSHA = strings.Repeat("ab12", 16)
		schema.LiveSHA = strings.Repeat("0f", 32)
		raw, _ = json.MarshalIndent(runpod.BuildCapabilities(t0, &schema, nil), "", "  ")
		if err := runpod.CheckFileSafe(raw, ""); err != nil {
			t.Errorf("a 64-hex spec digest was refused: %v", err)
		}
	})
	t.Run("the relaxation does not weaken the guard", func(t *testing.T) {
		t.Parallel()
		tok := "rpa_" + strings.Repeat("A1b2", 10) // bearer-like: letters, digits, underscore
		acct := "user_" + strings.Repeat("2x9", 12)
		cases := map[string]string{
			"token in changed":       `{"schema":{"changed":["paths.` + tok + `.get"]}}`,
			"digit run in failures":  `{"schema":{"failures":["field missing: ` + strings.Repeat("a1", 20) + `"]}}`,
			"account id in changed":  `{"schema":{"changed":["` + acct + `"]}}`,
			"token in another field": `{"schema":{"flashboot_values":["` + tok + `"]}}`,
			"token as top level":     `{"format":"mm.runpod-probe/2","probed":false,"x":"` + tok + `"}`,
			"letters outside lists":  `{"schema":{"flashboot_values":["` + strings.Repeat("Abc", 20) + `"]}}`,
			"non-hex digest":         `{"schema":{"snapshot_sha":"` + strings.Repeat("g", 40) + `"}}`,
			"digest in other field":  `{"schema":{"live_sha":"ok","x":"` + strings.Repeat("ab", 32) + `"}}`,
		}
		for name, doc := range cases {
			if runpod.CheckFileSafe([]byte(doc), "") == nil {
				t.Errorf("%s passed", name)
			}
		}
		if runpod.CheckFileSafe([]byte(`{"schema":{"changed":["paths.abc"]}}`), "abc") == nil {
			t.Error("the endpoint id passed inside the schema report")
		}
	})
	t.Run("unsafe documents are refused", func(t *testing.T) {
		t.Parallel()
		long := `{"format":"mm.runpod-probe/2","probed":false,"x":"` + strings.Repeat("a", 40) + `"}`
		if runpod.CheckFileSafe([]byte(long), "") == nil {
			t.Error("a 40-character run passed")
		}
		if runpod.CheckFileSafe([]byte(`{"ep":"abc-endpoint"}`), "abc-endpoint") == nil {
			t.Error("the endpoint id passed")
		}
		if runpod.ValidateCapabilities([]byte(`{"format":"other","probed":false}`)) == nil {
			t.Error("wrong format passed")
		}
		if runpod.ValidateCapabilities([]byte(`{"format":"mm.runpod-probe/2","probed":true}`)) == nil {
			t.Error("a probed file without results passed")
		}
		if runpod.ValidateCapabilities([]byte(`{"format":"mm.runpod-probe/2","probed":false,"extra":1}`)) == nil {
			t.Error("unknown fields passed")
		}
	})
	t.Run("backend reads the probed file", func(t *testing.T) {
		t.Parallel()
		// the keys report of a good probe yields write+billing capabilities through backend.parseProbe
		e := probeEnv(t)
		rep, err := runProbe(t, e)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(runpod.BuildCapabilities(t0, nil, rep))
		b := runpod.NewBackend(runpod.BackendOptions{Keys: e.keys, ProbeJSON: raw, Clock: e.clk})
		caps := b.Capabilities()
		if !caps.ScaleToZero || !caps.RevertDrift || caps.Billing != "hour" {
			t.Errorf("caps = %+v", caps)
		}
	})
}
