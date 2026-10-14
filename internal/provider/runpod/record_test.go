package runpod_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
)

func TestRecorder_Redacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var rec *runpod.FileRecorder
	uploadURL := "https://mm.example.com/worker/v1/uploads/" + strings.Repeat("u", 20) + "?exp=1&sig=" + canary("sig")
	startURL := "https://mm.example.com/worker/v1/attempts/x/start?sig=s"
	e := newEnv(t, func(o *envOpts) {
		o.srv.Runner = func(context.Context, string, json.RawMessage) runpodtest.Run {
			return runpodtest.Run{Output: json.RawMessage(`{"upload":"` + uploadURL + `","start":"` + startURL + `"}`), ExecutionMS: 1500}
		}
		o.client = func(co *runpod.ClientOptions) {
			r, err := runpod.NewFileRecorder(dir, co.Keys.EndpointID)
			if err != nil {
				t.Fatal(err)
			}
			rec, co.Recorder = r, r
		}
	})
	e.srv.SetWorkers([]map[string]any{{"id": "w-" + strings.Repeat("z", 8), "status": "RUNNING", "gpuTypeId": "gpu-a5000", "image": "ghcr.io/example/worker@sha256:" + strings.Repeat("c", 64)}})
	input := `{"contract":"mm.worker/1","uploads":{"audio.flac":{"url":"` + uploadURL + `"}},"start":{"url":"https://mm.example.com/worker/v1/attempts/x/start?sig=s"}}`
	res, err := e.cl.Run(bg(), json.RawMessage(input), runpod.Policy{ExecutionTimeoutMS: 5000, TTLMS: 10000})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.cl.Status(bg(), res.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.cl.Cancel(bg(), res.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cl.Workers(bg()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cl.GetEndpoint(bg(), runpod.KeyRead); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cl.Status(bg(), "missing-job"); err == nil {
		t.Fatal("want a 404")
	}
	if err := rec.Err(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 8 {
		t.Fatalf("files %v err %v", files, err)
	}
	if !strings.HasPrefix(filepath.Base(files[0]), "0001-run") || !strings.HasPrefix(filepath.Base(files[1]), "0002-status") {
		t.Errorf("names %v", files)
	}
	var all strings.Builder
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(raw)
		var x map[string]any
		if err := json.Unmarshal(raw, &x); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, k := range []string{"op", "method", "path_template", "status", "headers", "request_body", "response_body"} {
			if _, ok := x[k]; !ok {
				t.Errorf("%s lacks %s", filepath.Base(f), k)
			}
		}
		for k := range x["headers"].(map[string]any) {
			switch strings.ToLower(k) {
			case "content-type", "ratelimit", "ratelimit-policy", "retry-after", "date":
			default:
				t.Errorf("%s: header %q is not allowlisted", filepath.Base(f), k)
			}
		}
		if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", filepath.Base(f), st.Mode().Perm())
		}
	}
	text := all.String()
	secrets := []string{e.set.Jobs, e.set.Read, e.set.Write, e.set.Admin, e.epRaw, canary("sig"), "uploads/" + strings.Repeat("u", 20),
		"mm.example.com", "worker-canary-1", "w-zzzzzzzz", strings.Repeat("c", 64), "Authorization", "Bearer"}
	secrets = append(secrets, e.srv.JobIDs()...)
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Errorf("the recorded files contain %q", s)
		}
	}
	for _, want := range []string{`"redacted": true`, "<job-1>", "<worker-1>", "<endpoint>", "<upload-url>", "<start-url>"} {
		if !strings.Contains(text, want) {
			t.Errorf("the recorded files lack %q", want)
		}
	}
	// the 404 shape is kept
	if !strings.Contains(text, `"status": 404`) {
		t.Error("the 404 exchange is missing")
	}
	_ = runpodtest.OpRun
}

func TestRecorder_RejectsMissingDir(t *testing.T) {
	t.Parallel()
	if _, err := runpod.NewFileRecorder(filepath.Join(t.TempDir(), "nope"), "ep"); err == nil {
		t.Fatal("a missing directory was accepted")
	}
}
