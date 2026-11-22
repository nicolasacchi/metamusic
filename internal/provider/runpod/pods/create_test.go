package pods_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods/podtest"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const goldenPath = "testdata/create_body.golden.json"

func TestPodClient_CreateBodyGolden(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if _, err := e.cl.CreatePod(bg(), body()); err != nil {
		t.Fatal(err)
	}
	req, ok := e.srv.Last(podtest.OpCreate)
	if !ok {
		t.Fatal("no create request")
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, req.Body, "", "  "); err != nil {
		t.Fatal(err)
	}
	buf.WriteByte('\n')
	if *update {
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("create body differs from %s (run with -update):\n%s", goldenPath, buf.String())
	}
	// the secret of the poll URL is part of the golden only as the canary, never a real signature
	if !strings.Contains(string(want), `"cloud": "SECURE"`) || !strings.Contains(string(want), `"minCudaVersion": "12.8"`) {
		t.Error("golden lacks the fixed fields")
	}
}

func TestPodClient_CreateBodyEncoding(t *testing.T) {
	t.Parallel()
	b := body()
	b.Env = nil
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if env, ok := m["env"].(map[string]any); !ok || len(env) != 0 {
		t.Errorf("a nil env must encode as {}, got %v", m["env"])
	}
	if ports, ok := m["ports"].([]any); !ok || len(ports) != 0 {
		t.Errorf("ports must be [], got %v", m["ports"])
	}
	g := m["gpu"].(map[string]any)
	if g["count"] != float64(1) || g["id"] != gpu4090 || g["minCudaVersion"] != pods.MinCudaVersion {
		t.Errorf("gpu = %v", g)
	}
	for k, want := range map[string]any{"cloud": "SECURE", "startSsh": false, "startJupyter": false, "globalNetworking": false, "disk": float64(40)} {
		if m[k] != want {
			t.Errorf("%s = %v, want %v", k, m[k], want)
		}
	}
}

func TestPodClient_RejectsBadBodyBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	huge := body()
	huge.Env = map[string]string{"BIG": strings.Repeat("a", pods.MaxRequestBody)}
	for name, mod := range map[string]func(*pods.CreateBody){
		"canary name":       func(b *pods.CreateBody) { b.Name = "mm-c0190abcd123" },
		"name wrong":        func(b *pods.CreateBody) { b.Name = "pod-1" },
		"name uppercase":    func(b *pods.CreateBody) { b.Name = "mm-0190ABCD1234" },
		"name short":        func(b *pods.CreateBody) { b.Name = "mm-0190abcd" },
		"image tag":         func(b *pods.CreateBody) { b.Image = "ghcr.io/example/mm-worker:latest" },
		"image no digest":   func(b *pods.CreateBody) { b.Image = "" },
		"gpu empty":         func(b *pods.CreateBody) { b.GPUTypeID = " " },
		"disk small":        func(b *pods.CreateBody) { b.DiskGB = 19 },
		"disk large":        func(b *pods.CreateBody) { b.DiskGB = 201 },
		"empty env name":    func(b *pods.CreateBody) { b.Env = map[string]string{"": "x"} },
		"request too large": func(b *pods.CreateBody) { b.Env = huge.Env },
	} {
		b := body()
		mod(&b)
		_, err := e.cl.CreatePod(bg(), b)
		if name == "canary name" {
			if err != nil {
				t.Errorf("the canary name form must be accepted: %v", err)
			}
			continue
		}
		if !errors.Is(err, pods.ErrBadBody) {
			t.Errorf("%s: err = %v, want ErrBadBody", name, err)
		}
		if err != nil && strings.Contains(err.Error(), canary("sig")) {
			t.Errorf("%s: the error carries the poll URL", name)
		}
	}
	if n := e.srv.Calls(podtest.OpCreate); n != 1 { // only the canary-name case reached the server
		t.Errorf("create calls = %d, want 1", n)
	}
}

// ---- snapshot ----------------------------------------------------------------------------------------------

type doc map[string]any

func loadSnapshot(t testing.TB) doc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "openapi.snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d doc
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d doc) at(path ...string) any {
	var cur any = map[string]any(d)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func (d doc) deref(v any) map[string]any {
	m, _ := v.(map[string]any)
	if ref, ok := m["$ref"].(string); ok {
		m, _ = d.at(strings.Split(strings.TrimPrefix(ref, "#/"), "/")...).(map[string]any)
	}
	return m
}

// props collects the properties of a schema through $ref and allOf; unevaluated reports `unevaluatedProperties: false`.
func (d doc) props(schema any) (props map[string]map[string]any, required []string, closed bool) {
	props = map[string]map[string]any{}
	s := d.deref(schema)
	if s == nil {
		return
	}
	if u, ok := s["unevaluatedProperties"].(bool); ok && !u {
		closed = true
	}
	if ps, ok := s["properties"].(map[string]any); ok {
		for k, v := range ps {
			props[k] = d.deref(v)
		}
	}
	if rs, ok := s["required"].([]any); ok {
		for _, r := range rs {
			required = append(required, r.(string))
		}
	}
	if all, ok := s["allOf"].([]any); ok {
		for _, part := range all {
			p, r, c := d.props(part)
			for k, v := range p {
				props[k] = v
			}
			required = append(required, r...)
			closed = closed || c
		}
	}
	return
}

func TestSnapshot_PodPathsPresent(t *testing.T) {
	t.Parallel()
	d := loadSnapshot(t)
	for _, op := range []struct{ method, path, scope string }{
		{"post", "/v2/pods", "pods:write"},
		{"get", "/v2/pods", "pods:read"},
		{"get", "/v2/pods/{id}", "pods:read"},
		{"delete", "/v2/pods/{id}", "pods:write"},
		{"post", "/v2/pods/{id}/action", "pods:write"},
		{"get", "/v2/pods/{id}/logs", "pods:read"},
		{"delete", "/v2/queued-pods/{id}", "pods:write"},
		{"get", "/v2/catalog/gpus", "catalog:read"},
		{"get", "/v2/billing/pods", "billing:read"},
	} {
		o := d.at("paths", op.path, op.method)
		if o == nil {
			t.Errorf("%s %s is not in the snapshot", strings.ToUpper(op.method), op.path)
			continue
		}
		if got := d.at("paths", op.path, op.method, "x-runpod-scope"); got != op.scope {
			t.Errorf("%s %s: scope = %v, want %s", op.method, op.path, got, op.scope)
		}
	}
	// the create replies of C392.3
	for _, status := range []string{"201", "202", "400", "402", "403", "409", "413", "422", "429"} {
		if status == "402" {
			continue // 402 is documented in the description table, not as a response entry
		}
		if d.at("paths", "/v2/pods", "post", "responses", status) == nil {
			t.Errorf("POST /v2/pods: reply %s missing from the snapshot", status)
		}
	}
	if d.at("paths", "/v2/pods", "post", "description") == nil || !strings.Contains(d.at("paths", "/v2/pods", "post", "description").(string), "402") {
		t.Error("the create description no longer documents 402")
	}
	status := d.at("components", "schemas", "PodStatus", "enum").([]any)
	if len(status) != 6 {
		t.Errorf("PodStatus = %v", status)
	}
	// the query parameters the client sends
	for _, q := range []struct{ path, name string }{
		{"/v2/pods/{id}/logs", "tail"}, {"/v2/pods/{id}/logs", "source"}, {"/v2/billing/pods", "bucketSize"},
		{"/v2/billing/pods", "startTime"}, {"/v2/billing/pods", "endTime"}, {"/v2/catalog/gpus", "cloud"}, {"/v2/catalog/gpus", "product"},
		{"/v2/pods", "cursor"}, {"/v2/pods", "limit"},
	} {
		found := false
		for _, p := range d.at("paths", q.path, "get", "parameters").([]any) {
			if pm := d.deref(p); pm != nil && pm["name"] == q.name && pm["in"] == "query" {
				found = true
			}
		}
		if !found {
			t.Errorf("GET %s has no query parameter %s", q.path, q.name)
		}
	}
}

func TestPodClient_BodyMatchesSnapshot(t *testing.T) {
	t.Parallel()
	d := loadSnapshot(t)
	raw, err := json.Marshal(body())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	props, required, closed := d.props(map[string]any{"$ref": "#/components/schemas/CreatePodRequest"})
	if !closed {
		t.Fatal("CreatePodRequest is no longer closed (unevaluatedProperties): revisit the banned-field test")
	}
	for k := range got {
		if _, ok := props[k]; !ok {
			t.Errorf("body property %q is not in CreatePodRequest", k)
		}
	}
	for _, r := range required {
		if _, ok := got[r]; !ok {
			t.Errorf("required property %q is missing from the body", r)
		}
	}
	// types
	typeOf := func(p map[string]any) any { return p["type"] }
	checks := map[string]string{"name": "string", "image": "string", "cloud": "", "disk": "integer", "ports": "array", "startSsh": "boolean",
		"startJupyter": "boolean", "globalNetworking": "boolean", "env": "object"}
	for k, want := range checks {
		if want != "" && typeOf(props[k]) != want {
			t.Errorf("%s: snapshot type %v, want %s", k, typeOf(props[k]), want)
		}
	}
	if enum := d.at("components", "schemas", "Cloud", "enum").([]any); enum[0] != "SECURE" {
		t.Errorf("Cloud enum = %v", enum)
	}
	if def := props["cloud"]["default"]; def != nil && def != "SECURE" {
		t.Errorf("cloud default = %v", def)
	}
	gpuProps, gpuRequired, gpuClosed := d.props(props["gpu"])
	if !gpuClosed {
		t.Error("CreateGpuConfig is no longer closed")
	}
	g := got["gpu"].(map[string]any)
	for k := range g {
		if _, ok := gpuProps[k]; !ok {
			t.Errorf("gpu property %q is not in CreateGpuConfig", k)
		}
	}
	for _, r := range gpuRequired {
		if _, ok := g[r]; !ok {
			t.Errorf("gpu.%s is required and missing", r)
		}
	}
	if re, _ := gpuProps["minCudaVersion"]["pattern"].(string); !regexp.MustCompile(re).MatchString(pods.MinCudaVersion) {
		t.Errorf("minCudaVersion %q does not match %q", pods.MinCudaVersion, re)
	}
	if lo, _ := props["disk"]["minimum"].(float64); lo > 20 {
		t.Errorf("disk minimum is %v: the client's lower bound of 20 GB is too low", lo)
	}
	// the pod object the client reads
	podProps, _, _ := d.props(map[string]any{"$ref": "#/components/schemas/Pod"})
	for _, k := range []string{"id", "name", "status", "image", "cost", "createdAt", "startedAt"} {
		if _, ok := podProps[k]; !ok {
			t.Errorf("Pod has no property %q", k)
		}
	}
}

var bannedNames = []string{"queue", "templateId", "mounts", "dataCenterIds", "cpu", "registry",
	"gpuTypeIds", "gpuCount", "cloudType", "containerDiskInGb", "volumeInGb", "volumeMountPath"}

func walkKeys(v any, f func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			f(k)
			walkKeys(c, f)
		}
	case []any:
		for _, c := range x {
			walkKeys(c, f)
		}
	}
}

func TestPodClient_BannedFields(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if _, err := e.cl.CreatePod(bg(), body()); err != nil {
		t.Fatal(err)
	}
	req, _ := e.srv.Last(podtest.OpCreate)
	var sent map[string]any
	if err := json.Unmarshal(req.Body, &sent); err != nil {
		t.Fatal(err)
	}
	// the env map holds free names by design; only the structure of the body is checked
	structure := map[string]any{}
	for k, v := range sent {
		if k != "env" {
			structure[k] = v
		}
	}
	walkKeys(structure, func(k string) {
		for _, b := range bannedNames {
			if k == b {
				t.Errorf("the create body carries the banned property %q", k)
			}
		}
	})
	// the encoder and the golden never mention them either
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"types.go", "client.go", "api.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range bannedNames {
			if bytes.Contains(src, []byte(`json:"`+b+`"`)) || bytes.Contains(src, []byte(`json:"`+b+`,`)) {
				t.Errorf("%s declares a JSON field %q", f, b)
			}
		}
	}
	for _, b := range bannedNames {
		if bytes.Contains(golden, []byte(`"`+b+`"`)) {
			t.Errorf("the golden carries %q", b)
		}
	}
	// no float arithmetic anywhere in the client
	for _, f := range []string{"types.go", "client.go", "api.go"} {
		src, _ := os.ReadFile(f)
		for _, bad := range []string{"float64", "float32", "ParseFloat"} {
			if bytes.Contains(src, []byte(bad)) {
				t.Errorf("%s uses %s", f, bad)
			}
		}
	}
}

// ---- reply table ----------------------------------------------------------------------------------------------

func TestPodClient_ReplyTable(t *testing.T) {
	t.Parallel()
	secret := canary("secret-detail")
	for _, tc := range []struct {
		name   string
		fault  *podtest.Fault
		check  func(t *testing.T, err error)
		wantOK bool
	}{
		{name: "201", wantOK: true},
		{name: "400 refused maybe capacity", fault: &podtest.Fault{Status: 400, Detail: "no instances available"}, check: func(t *testing.T, err error) {
			var r *pods.ErrRefused
			if !errors.As(err, &r) || r.Status != 400 || !r.Capacity || !strings.Contains(r.Detail, "no instances") {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "403 refused next type", fault: &podtest.Fault{Status: 403}, check: func(t *testing.T, err error) {
			var r *pods.ErrRefused
			if !errors.As(err, &r) || r.Status != 403 || r.Capacity || errors.Is(err, provider.ErrAccess) {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "409 refused", fault: &podtest.Fault{Status: 409}, check: func(t *testing.T, err error) {
			var r *pods.ErrRefused
			if !errors.As(err, &r) || r.Status != 409 || r.Capacity {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "402 balance", fault: &podtest.Fault{Status: 402}, check: func(t *testing.T, err error) {
			if !errors.Is(err, pods.ErrBalance) {
				t.Errorf("err = %v", err)
			}
		}},
		{name: "422 invalid", fault: &podtest.Fault{Status: 422, Detail: "gpu.id is required"}, check: func(t *testing.T, err error) {
			if !errors.Is(err, pods.ErrInvalid) || !strings.Contains(err.Error(), "gpu.id") {
				t.Errorf("err = %v", err)
			}
		}},
		{name: "429 rate limited", fault: &podtest.Fault{Status: 429, RetryAfter: "7"}, check: func(t *testing.T, err error) {
			var r *pods.ErrRateLimited
			if !errors.As(err, &r) || r.RetryAfter != 7*time.Second {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "500 ambiguous", fault: &podtest.Fault{Status: 500, Detail: secret}, check: func(t *testing.T, err error) {
			var a *pods.ErrAmbiguous
			if !errors.As(err, &a) || a.Status != 500 {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "503 ambiguous", fault: &podtest.Fault{Status: 503}, check: func(t *testing.T, err error) {
			var a *pods.ErrAmbiguous
			if !errors.As(err, &a) || a.Status != 503 {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "401 access", fault: &podtest.Fault{Status: 401}, check: func(t *testing.T, err error) {
			if !errors.Is(err, provider.ErrAccess) {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "413 other", fault: &podtest.Fault{Status: 413}, check: func(t *testing.T, err error) {
			var a *pods.APIError
			if !errors.As(err, &a) || a.Status != 413 {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "404 other", fault: &podtest.Fault{Status: 404}, check: func(t *testing.T, err error) {
			var a *pods.APIError
			if !errors.As(err, &a) || a.Status != 404 {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "201 unreadable", fault: &podtest.Fault{Status: 201, Body: `{"id":`}, check: func(t *testing.T, err error) {
			var a *pods.ErrAmbiguous
			if !errors.As(err, &a) || a.Status != 201 {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "201 without id", fault: &podtest.Fault{Status: 201, Body: `{"name":"x"}`}, check: func(t *testing.T, err error) {
			var a *pods.ErrAmbiguous
			if !errors.As(err, &a) {
				t.Errorf("err = %#v", err)
			}
		}},
		{name: "201 bad cost", fault: &podtest.Fault{Status: 201, Body: `{"id":"p1","cost":"abc"}`}, check: func(t *testing.T, err error) {
			var a *pods.ErrAmbiguous
			if !errors.As(err, &a) {
				t.Errorf("err = %#v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			if tc.fault != nil {
				e.srv.Fault(podtest.OpCreate, *tc.fault)
			}
			p, err := e.cl.CreatePod(bg(), body())
			if tc.wantOK {
				if err != nil || p.ID == "" || p.Name != podName || p.Status != pods.StatusProvisioning || p.CostPPH != 690000 || p.Image != testDigest || p.CreatedAt == nil {
					t.Fatalf("pod = %+v, err %v", p, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("no error, pod %+v", p)
			}
			tc.check(t, err)
			if strings.Contains(err.Error(), secret) && tc.name != "500 ambiguous" {
				t.Errorf("unexpected detail echo: %v", err)
			}
			if got := e.srv.Calls(podtest.OpCreate); got != 1 {
				t.Errorf("create calls = %d, want exactly 1", got)
			}
		})
	}
}

func TestPodClient_Create202CancelsQueued(t *testing.T) {
	t.Parallel()
	for _, withPod := range []bool{false, true} {
		e := newEnv(t)
		e.srv.QueueNextCreate(withPod)
		_, err := e.cl.CreatePod(bg(), body())
		if !errors.Is(err, pods.ErrQueued) {
			t.Fatalf("withPod=%v: err = %v, want ErrQueued", withPod, err)
		}
		if e.srv.Calls(podtest.OpCancelQueued) != 1 {
			t.Errorf("withPod=%v: the queue entry was not cancelled", withPod)
		}
		if withPod {
			if e.srv.Calls(podtest.OpDelete) != 1 || len(e.srv.Pods()) != 0 {
				t.Errorf("the pod the queue entry resolved to was not deleted: %d live", len(e.srv.Pods()))
			}
		} else if e.srv.Calls(podtest.OpDelete) != 0 {
			t.Error("a pod delete without a pod")
		}
		if e.srv.Calls(podtest.OpCreate) != 1 {
			t.Errorf("create replayed: %d", e.srv.Calls(podtest.OpCreate))
		}
	}
	// the cleanup fails: still ErrQueued, with the failure told
	e := newEnv(t)
	e.srv.QueueNextCreate(true)
	e.srv.Fault(podtest.OpCancelQueued, podtest.Fault{Status: 500})
	_, err := e.cl.CreatePod(bg(), body())
	if !errors.Is(err, pods.ErrQueued) || !strings.Contains(err.Error(), "cleanup failed") {
		t.Errorf("err = %v", err)
	}
	// an unreadable 202
	e = newEnv(t)
	e.srv.Fault(podtest.OpCreate, podtest.Fault{Status: 202, Body: `nope`})
	if _, err := e.cl.CreatePod(bg(), body()); !errors.Is(err, pods.ErrQueued) || !strings.Contains(err.Error(), "name sweep") {
		t.Errorf("unreadable 202: %v", err)
	}
}

func TestPodClient_NeverReplaysCreateOrDelete(t *testing.T) {
	t.Parallel()
	faults := map[string]podtest.Fault{
		"500":   {Status: 500},
		"429":   {Status: 429, RetryAfter: "1"},
		"reset": {Reset: true},
		"hang":  {Hang: true},
	}
	for name, f := range faults {
		t.Run("create "+name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t, func(o *envOpts) { o.timeouts.Create = 150 * time.Millisecond; o.inproc = true })
				e.srv.Fault(podtest.OpCreate, f)
				_, err := e.cl.CreatePod(bg(), body())
				if err == nil {
					t.Fatal("no error")
				}
				if name == "429" {
					var r *pods.ErrRateLimited
					if !errors.As(err, &r) {
						t.Errorf("err = %v", err)
					}
				} else {
					var a *pods.ErrAmbiguous
					if !errors.As(err, &a) {
						t.Errorf("err = %v, want ErrAmbiguous", err)
					}
				}
				if got := e.srv.Calls(podtest.OpCreate); got != 1 {
					t.Errorf("create calls = %d, want 1", got)
				}
			})
		})
		t.Run("delete "+name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t, func(o *envOpts) { o.timeouts.Other = 150 * time.Millisecond; o.inproc = true })
				e.srv.AddPod(podtest.Pod{ID: "p1", Name: podName})
				e.srv.Fault(podtest.OpDelete, f)
				err := e.cl.DeletePod(bg(), "p1")
				if err == nil {
					t.Fatal("no error")
				}
				if got := e.srv.Calls(podtest.OpDelete); got != 1 {
					t.Errorf("delete calls = %d, want 1", got)
				}
				if name != "429" {
					var a *pods.ErrAmbiguous
					if !errors.As(err, &a) {
						t.Errorf("err = %v, want ErrAmbiguous", err)
					}
				}
			})
		})
	}
	// an applied create whose reply is lost: the pod exists, the client reports ambiguity, nothing is replayed
	e := newEnv(t)
	e.srv.Fault(podtest.OpCreate, podtest.Fault{Status: 502, AfterEffect: true})
	_, err := e.cl.CreatePod(bg(), body())
	var a *pods.ErrAmbiguous
	if !errors.As(err, &a) || len(e.srv.Pods()) != 1 || e.srv.Calls(podtest.OpCreate) != 1 {
		t.Errorf("err = %v, pods %d, calls %d", err, len(e.srv.Pods()), e.srv.Calls(podtest.OpCreate))
	}
	// a cancelled context is also ambiguous for a create
	ctx, cancel := context.WithCancel(bg())
	cancel()
	e2 := newEnv(t)
	if _, err := e2.cl.CreatePod(ctx, body()); !errors.As(err, &a) || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled create: %v", err)
	}
}

func TestPodClient_DoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.srv.Fault(podtest.OpGet, podtest.Fault{Status: 302, Location: "https://example.com/elsewhere"})
	_, err := e.cl.GetPod(bg(), "p1")
	var a *pods.APIError
	if !errors.As(err, &a) || a.Status != 302 {
		t.Fatalf("err = %v, want a 302 APIError", err)
	}
	if n := e.srv.Calls(podtest.OpGet); n != 1 {
		t.Errorf("calls = %d", n)
	}
}

// the real binary must not link the fake (TestNoTestHelpersInBinary also scans this)
func TestPodtestNotLinkedByTheClient(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", "github.com/nicolasacchi/metamusic/internal/provider/runpod/pods").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	deps := map[string]bool{}
	for _, l := range strings.Fields(string(out)) {
		deps[l] = true
	}
	for _, banned := range []string{
		"provider/runpod/pods/podtest", "provider/runpod", "provider/fake", "jobs", "generation", "httpserver", "api", "engine/acestep",
	} {
		if deps["github.com/nicolasacchi/metamusic/internal/"+banned] {
			t.Errorf("the pods package reaches internal/%s", banned)
		}
	}
}
