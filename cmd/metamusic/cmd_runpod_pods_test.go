package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods/podtest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// podRig is a data directory with a database, the pod key file and a fake pod API. The front server answers GET /v2/serverless with
// the status the test sets and forwards every other call to podtest.
type podRig struct {
	*rpRig
	key  string
	pod  *podtest.Server
	mu   sync.Mutex
	srv  int // answer of GET /v2/serverless
	seen []string
}

func newPodRig(t *testing.T) *podRig {
	t.Helper()
	r := newRPRig(t)
	key := rpCanary("podkey")
	pod := podtest.New(t, podtest.Options{Key: key})
	target, _ := url.Parse(pod.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	pr := &podRig{rpRig: r, key: key, pod: pod, srv: 403}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		pr.mu.Lock()
		pr.seen = append(pr.seen, req.Method+" "+req.URL.Path)
		st := pr.srv
		pr.mu.Unlock()
		if req.URL.Path == "/v2/serverless" {
			w.WriteHeader(st)
			return
		}
		proxy.ServeHTTP(w, req)
	}))
	t.Cleanup(front.Close)
	pr.writeKey(t, key)
	r.env["MM_RUNPOD_BASE_URL"] = front.URL
	r.env["MM_POD_GPU_TYPES"] = "NVIDIA L40S"
	return pr
}

func (r *podRig) writeKey(t *testing.T, key string) {
	t.Helper()
	p := filepath.Join(r.sec, "runpod-pod.env")
	if err := os.WriteFile(p, []byte("RUNPOD_"+"POD_KEY="+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (r *podRig) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func (r *podRig) probeRows(t *testing.T) (verdicts []string, detail string) {
	t.Helper()
	rows, err := r.db(t).R.Query(`SELECT verdict, detail_json FROM pod_safety_log WHERE kind = 'probe' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v, &detail); err != nil {
			t.Fatal(err)
		}
		verdicts = append(verdicts, v)
	}
	return verdicts, detail
}

func (r *podRig) podState(t *testing.T) (armed int, by, at string, ok bool) {
	t.Helper()
	err := r.db(t).R.QueryRow(`SELECT armed, changed_by, changed_at FROM provider_state WHERE backend = ?`, provider.BackendRunPodPod).Scan(&armed, &by, &at)
	return armed, by, at, err == nil
}

// seedPodQuote caches the pod backend's quote the way the server's reconciler does.
func (r *podRig) seedPodQuote(t *testing.T, mod func(*provider.Quote)) {
	t.Helper()
	q := provider.Quote{Backend: provider.BackendRunPodPod, ListPricePPH: 860_000,
		Endpoint: provider.EndpointConfig{Type: provider.EndpointTypePod, WorkersMax: 1, GPUCount: 1, ImageDigest: "sha256:" + strings.Repeat("a", 64)}}
	if mod != nil {
		mod(&q)
	}
	raw, _ := json.Marshal(q)
	r.exec(t, `INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES (?,0,'system:default',?,?,?)
		ON CONFLICT(backend) DO UPDATE SET quote_json = excluded.quote_json, quote_at = excluded.quote_at`,
		provider.BackendRunPodPod, store.FormatTime(time.Now().Add(-time.Hour)), string(raw), store.FormatTime(time.Now().Add(-time.Minute)))
}

func (r *podRig) probeRow(t *testing.T, verdict string) {
	t.Helper()
	r.exec(t, `INSERT INTO pod_safety_log(kind, verdict, detail_json, created_by, created_at) VALUES ('probe', ?, '{"billing_readable":true}', 'system:cli', ?)`,
		verdict, store.FormatTime(time.Now()))
}

func TestProbePods_Rows(t *testing.T) {
	t.Parallel()
	t.Run("a narrow key is ok and billing is readable", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		code, out, errs := rp(t, r.env, "probe", "--pods")
		if code != 0 {
			t.Fatalf("probe: %d %s %s", code, out, errs)
		}
		vs, detail := r.probeRows(t)
		if len(vs) != 1 || vs[0] != "ok" {
			t.Fatalf("rows %v", vs)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(detail), &d); err != nil {
			t.Fatal(err)
		}
		if d["billing_readable"] != true || d["pods_read"] != true || d["catalog_read"] != true || d["serverless_read"] != false {
			t.Errorf("detail %v", d)
		}
		if n, _, _, _ := r.auditOps(t, "cli:runpod-probe-pods"); n != 1 {
			t.Errorf("audit rows %d", n)
		}
		for _, leak := range []string{r.key, "Bearer"} {
			if strings.Contains(out+errs+detail, leak) {
				t.Errorf("the key shows in the output or the row")
			}
		}
	})
	t.Run("read-only: no create, no delete, no log stream", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		if code, out, errs := rp(t, r.env, "probe", "--pods"); code != 0 {
			t.Fatalf("probe: %d %s %s", code, out, errs)
		}
		for _, q := range r.requests() {
			if !strings.HasPrefix(q, "GET ") {
				t.Errorf("the probe sent %s", q)
			}
		}
		if r.pod.Calls(podtest.OpCreate)+r.pod.Calls(podtest.OpDelete)+r.pod.Calls(podtest.OpLogs) != 0 || len(r.pod.Pods()) != 0 {
			t.Error("the probe changed pods")
		}
	})
	t.Run("a broad key is refused unless accepted; the row says too_broad either way", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		r.srv = 200
		code, _, errs := rp(t, r.env, "probe", "--pods")
		if code != 3 || !strings.Contains(errs, "too_broad") {
			t.Fatalf("broad key: %d %s", code, errs)
		}
		if code, out, errs := rp(t, r.env, "probe", "--pods", "--accept-broad-pod-key"); code != 0 || !strings.Contains(out, `"accepted_broad_key": true`) {
			t.Fatalf("accepted: %d %s %s", code, out, errs)
		}
		if vs, _ := r.probeRows(t); len(vs) != 2 || vs[0] != "too_broad" || vs[1] != "too_broad" {
			t.Errorf("rows %v", vs)
		}
	})
	t.Run("an unusable answer fails the probe", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		r.srv = 404
		if code, _, _ := rp(t, r.env, "probe", "--pods"); code != 1 {
			t.Fatalf("exit %d", code)
		}
		if vs, _ := r.probeRows(t); len(vs) != 1 || vs[0] != "failed" {
			t.Errorf("rows %v", vs)
		}
	})
	t.Run("a refused billing read is recorded, not fatal", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		r.pod.Fault(podtest.OpBilling, podtest.Fault{Count: 3, Status: 403})
		if code, out, errs := rp(t, r.env, "probe", "--pods"); code != 0 {
			t.Fatalf("probe: %d %s %s", code, out, errs)
		}
		if _, detail := r.probeRows(t); !strings.Contains(detail, `"billing_readable":false`) {
			t.Errorf("detail %s", detail)
		}
	})
	t.Run("no key, no types, no database, no mixing", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		if err := os.Remove(filepath.Join(r.sec, "runpod-pod.env")); err != nil {
			t.Fatal(err)
		}
		if code, _, errs := rp(t, r.env, "probe", "--pods"); code != 3 || !strings.Contains(errs, "keys_absent") {
			t.Errorf("no key: %d %s", code, errs)
		}
		r.writeKey(t, r.key)
		delete(r.env, "MM_POD_GPU_TYPES")
		if code, _, errs := rp(t, r.env, "probe", "--pods"); code != 2 || !strings.Contains(errs, "MM_POD_GPU_TYPES") {
			t.Errorf("no types: %d %s", code, errs)
		}
		r.env["MM_POD_GPU_TYPES"] = "NVIDIA L40S"
		if code, _, errs := rp(t, r.env, "probe", "--pods", "--keys"); code != 2 {
			t.Errorf("mixed: %d %s", code, errs)
		}
		if code, _, errs := rp(t, r.env, "probe", "--keys", "--accept-broad-pod-key"); code != 2 {
			t.Errorf("flag without --pods: %d %s", code, errs)
		}
	})
}

func TestCLI_PodsArmRefusedWithoutProbe(t *testing.T) {
	t.Parallel()
	arm := func(r *podRig, extra ...string) (int, string) {
		args := append([]string{"arm", "--backend", "runpod-pod", "--reason", "window"}, extra...)
		code, _, errs := rp(t, r.env, args...)
		return code, errs
	}
	t.Run("no probe row", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		r.seedPodQuote(t, nil)
		if code, errs := arm(r); code != 3 || !strings.Contains(errs, "pod_probe_missing") {
			t.Fatalf("%d %s", code, errs)
		}
		if a, _, _, _ := r.podState(t); a != 0 {
			t.Error("a refused arm armed the backend")
		}
	})
	t.Run("a failed probe", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		r.seedPodQuote(t, nil)
		r.probeRow(t, "failed")
		if code, errs := arm(r); code != 3 || !strings.Contains(errs, "pod_probe_missing") {
			t.Fatalf("%d %s", code, errs)
		}
	})
	t.Run("too_broad needs the flag", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		r.seedPodQuote(t, nil)
		r.probeRow(t, "too_broad")
		if code, errs := arm(r); code != 3 || !strings.Contains(errs, "accept-broad-pod-key") {
			t.Fatalf("%d %s", code, errs)
		}
		if code, errs := arm(r, "--accept-broad-pod-key"); code != 0 {
			t.Fatalf("with the flag: %d %s", code, errs)
		}
		if a, by, _, _ := r.podState(t); a != 1 || by != "system:cli" {
			t.Errorf("armed %d by %s", a, by)
		}
	})
	t.Run("no pod overhead quote", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		r.probeRow(t, "ok")
		if code, errs := arm(r); code != 3 || !strings.Contains(errs, "quote") {
			t.Fatalf("%d %s", code, errs)
		}
		r.seedPodQuote(t, func(q *provider.Quote) { q.ListPricePPH = 0 })
		if code, errs := arm(r); code != 3 || !strings.Contains(errs, "disabled") {
			t.Fatalf("zero price: %d %s", code, errs)
		}
	})
	t.Run("no pod key", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		r.seedPodQuote(t, nil)
		r.probeRow(t, "ok")
		if err := os.Remove(filepath.Join(r.sec, "runpod-pod.env")); err != nil {
			t.Fatal(err)
		}
		if code, errs := arm(r); code != 3 || !strings.Contains(errs, "keys_absent") {
			t.Fatalf("%d %s", code, errs)
		}
	})
	t.Run("an ok probe arms; re-arming keeps the window start", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		r.seedPodQuote(t, nil)
		r.probeRow(t, "ok")
		if code, errs := arm(r); code != 0 {
			t.Fatalf("%d %s", code, errs)
		}
		_, _, first, _ := r.podState(t)
		time.Sleep(5 * time.Millisecond)
		if code, errs := arm(r); code != 0 {
			t.Fatalf("%d %s", code, errs)
		}
		if _, _, second, _ := r.podState(t); second != first {
			t.Errorf("re-arm moved the window start: %s -> %s", first, second)
		}
		if n, p, tr, _ := r.auditOps(t, "cli:runpod-arm"); n != 2 || p != "system:cli" || tr != "cli" {
			t.Errorf("audit %d %s %s", n, p, tr)
		}
	})
	t.Run("pod-only and serverless-only flags do not mix", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t)
		for _, args := range [][]string{
			{"arm", "--backend", "runpod-pod", "--engine", "acestep15", "--reason", "x"},
			{"arm", "--backend", "runpod-pod", "--endpoint-name", "mm-yue2", "--reason", "x"},
			{"arm", "--backend", "runpod", "--reason", "x"},
			{"arm", "--endpoint-name", "mm-yue2", "--accept-broad-pod-key", "--reason", "x"},
		} {
			if code, _, errs := rp(t, r.env, args...); code != 2 {
				t.Errorf("%v: %d %s", args, code, errs)
			}
		}
	})
}

func TestCLI_PodsDisarmAndResetTrip(t *testing.T) {
	t.Parallel()
	r := newPodRig(t)
	r.seedPodQuote(t, nil)
	r.probeRow(t, "ok")
	if code, _, errs := rp(t, r.env, "arm", "--backend", "runpod-pod", "--reason", "go"); code != 0 {
		t.Fatalf("arm: %d %s", code, errs)
	}
	r.exec(t, `INSERT INTO provider_trips(id, backend, reason, tripped_at, details_json, actions_json) VALUES ('t1', ?, 'pod_overrun', ?, '{}', '[]')`, provider.BackendRunPodPod, store.FormatTime(time.Now()))
	if code, out, errs := rp(t, r.env, "reset-trip", "--backend", "runpod-pod", "--reason", "looked"); code != 0 || !strings.Contains(out, "trip reset") {
		t.Fatalf("reset-trip: %d %s %s", code, out, errs)
	}
	if code, _, errs := rp(t, r.env, "disarm", "--backend", "runpod-pod", "--reason", "done"); code != 0 {
		t.Fatalf("disarm: %d %s", code, errs)
	}
	if a, _, _, _ := r.podState(t); a != 0 {
		t.Error("still armed")
	}
	// the serverless backend was never touched
	if _, _, _, ok := func() (int, string, string, bool) {
		var a int
		err := r.db(t).R.QueryRow(`SELECT armed FROM provider_state WHERE backend = ?`, provider.BackendRunPod).Scan(&a)
		return a, "", "", err == nil
	}(); ok {
		t.Error("a pod command wrote the serverless backend's row")
	}
	if n, _, _, _ := r.auditOps(t, "cli:runpod-disarm"); n != 1 {
		t.Errorf("disarm audit rows %d", n)
	}
}

func TestCLI_StatusShowsPodKeyAndLivePods(t *testing.T) {
	t.Parallel()
	r := newPodRig(t)
	status := func() map[string]any {
		code, out, errs := rp(t, r.env, "status", "--backend", "runpod-pod", "--json")
		if code != 0 {
			t.Fatalf("status: %d %s %s", code, out, errs)
		}
		if strings.Contains(out, r.key) {
			t.Fatal("the key is in the status")
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := status()
	pods := m["pods"].(map[string]any)
	if m["backend"] != "runpod-pod" || pods["key_present"] != true || pods["probe"] != "missing" || pods["safety"] != "unverified" || m["live_pods"] != float64(0) {
		t.Errorf("status %v", m)
	}
	if _, ok := m["endpoint"]; ok {
		t.Error("the pod status asked the serverless endpoint")
	}
	r.probeRow(t, "ok")
	st := r.db(t)
	librarytest.FixtureM3(t, st, rpNow)
	seed := librarytest.SeedJob(t, st, rpNow, librarytest.JobSeed{Name: "podjob"})
	r.exec(t, `INSERT INTO pods(id,name,kind,batch_id,n,gpu_try,gpu_type,state,cloud,image_digest,price_pph_micro_usd,overhead_reserved_micro_usd,deadline_at,poll_exp,created_at,updated_at)
		VALUES('a','mm-aaaaaaaaaaaa','batch',?,1,1,'T','intent','SECURE','sha256:x',300000,0,?,1,?,?)`,
		seed.BatchID, store.FormatTime(rpNow.Add(time.Hour)), store.FormatTime(rpNow), store.FormatTime(rpNow))
	m = status()
	pods = m["pods"].(map[string]any)
	live := pods["live"].([]any)
	if pods["probe"] != "ok" || len(live) != 1 || m["live_pods"] != float64(1) {
		t.Errorf("status %v", m)
	}
	if err := os.Remove(filepath.Join(r.sec, "runpod-pod.env")); err != nil {
		t.Fatal(err)
	}
	if m = status(); m["pods"].(map[string]any)["key_present"] != false || m["keys_absent"] != true {
		t.Errorf("key removed: %v", m)
	}
	code, human, _ := rp(t, r.env, "status", "--backend", "runpod-pod")
	if code != 0 || !strings.Contains(human, "pod key:        absent") || !strings.Contains(human, "live pod mm-aaaaaaaaaaaa") {
		t.Errorf("human status %q", human)
	}
	if code, _, errs := rp(t, r.env, "status", "--backend", "runpod-pod", "--repoll"); code != 2 {
		t.Errorf("--repoll: %d %s", code, errs)
	}
}

// the pod commands are operator-only: nothing in the API surface names them (no operation, no MCP tool, no route).
func TestPodCLI_NotInTheAPISurface(t *testing.T) {
	t.Parallel()
	code, out, errs := do(t, nil, "", "openapi")
	if code != 0 {
		t.Fatalf("openapi: %d %s", code, errs)
	}
	for _, banned := range []string{"accept-broad-pod-key", "accept_broad_pod_key", "probe_pods", "runpod-probe"} {
		if strings.Contains(out, banned) {
			t.Errorf("the API surface mentions %q", banned)
		}
	}
}

// pod-selfterm spends what the Owner approves: an explicit --max-usd is required, and it never needs a RunPod key of its own (a REST client).
func TestCLI_PodSelftermCanaryNeedsMaxUSD(t *testing.T) {
	t.Parallel()
	r := newRPRigNoDB(t)
	env := map[string]string{"MM_TOKEN": rpCanary("token")}
	for k, v := range r.env {
		env[k] = v
	}
	if code, _, errs := rp(t, env, "canary", "--url", "http://127.0.0.1:1", "--case", "pod-selfterm"); code != 2 || !strings.Contains(errs, "--max-usd") {
		t.Errorf("exit %d: %s", code, errs)
	}
	// with it, the run reaches the (unreachable) server and fails there, before anything exists
	if code, _, errs := rp(t, env, "canary", "--url", "http://127.0.0.1:1", "--case", "pod-selfterm", "--max-usd", "0.60"); code == 0 || strings.Contains(errs, "--max-usd") {
		t.Errorf("exit %d: %s", code, errs)
	}
}
