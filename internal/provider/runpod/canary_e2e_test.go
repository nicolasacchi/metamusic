package runpod_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// TestCanary_MainAgainstWiredServerOverRunpodtest is the canary rehearsal: the real wiring (app.Build, the
// runner, the reconciler and the ingest bridge) over the serverless backend, with runpodtest standing in for
// RunPod and a simulated worker uploading through the real HTTP routes. It never touches the network
// beyond loopback and spends nothing; its keys are runtime-built canary placeholders.
func TestCanary_MainAgainstWiredServerOverRunpodtest(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("heavy: renders audio and runs the wired runner")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}

	// the app's public listener comes first: the URLs handed to the worker must point at it
	ts := httptest.NewUnstartedServer(http.NotFoundHandler())
	publicURL := "http://" + ts.Listener.Addr().String()

	set := runpodtest.KeySet{Jobs: canary("jobs"), Read: canary("read"), Write: canary("write"), Admin: canary("admin")}
	ep := canary("endpoint")
	tools := mediatool.Tools{FFmpeg: "ffmpeg", FLAC: "flac", Metaflac: "metaflac"}
	// the worker's uploads reach the app over loopback HTTP, as they would through the public route
	sim := fake.NewSimWorker(fake.SimWorkerOptions{Tools: tools, Uploader: fake.HTTPUploader{}})
	rp := runpodtest.New(t, runpodtest.Options{Keys: set, EndpointID: ep, Runner: runpodtest.FakeWorker(sim), PollsToRun: 2})

	env := "RUNPOD_JOBS_KEY=" + set.Jobs + "\nRUNPOD_READ_KEY=" + set.Read + "\nRUNPOD_WRITE_KEY=" + set.Write + "\nRUNPOD_ENDPOINT_ID=" + ep + "\n"
	if err := os.WriteFile(filepath.Join(secrets, "runpod.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "secret_key"), []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AuthMode: "local", PublicURL: publicURL, RateScale: 1000, DataDir: dir, SecretsDir: secrets, Addr: ts.Listener.Addr().String(),
		Backend: config.BackendRunPod, Runner: true, JobMaxRuntime: 15 * time.Minute, JobTTL: 2 * time.Hour, ColdStartS: 180, KillSlackS: 15,
		PriceMarginPPM: 100000, BillingLag: 6 * time.Hour, ResidualHold: 48 * time.Hour,
		FFmpeg: "ffmpeg", FLAC: "flac", Metaflac: "metaflac"}
	sec, err := config.LoadSecrets(secrets, "local")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	svc, err := app.Build(ctx, cfg, sec, st, root, app.Options{M3: app.M3Options{Live: true, Shutdown: ctx,
		RunPod: &runpod.BackendOptions{MgmtBase: rp.URL, JobsBase: rp.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	if svc.Runner == nil || svc.Reconciler == nil {
		t.Fatal("no runner over the serverless backend with keys present")
	}
	h, err := httpserver.New(svc.Server)
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = h
	ts.Start()
	defer ts.Close()

	// the operator's step (runpod arm): the row is disarmed by default
	if _, err := st.W.Exec(`UPDATE provider_state SET armed = 1, changed_by = 'system:cli' WHERE backend = 'runpod-serverless'`); err != nil {
		t.Fatal(err)
	}
	var bg sync.WaitGroup
	bg.Add(2)
	go func() { defer bg.Done(); _ = svc.Runner.Run(ctx) }()
	go func() { defer bg.Done(); _ = svc.Reconciler.Run(ctx) }()
	defer func() { cancel(); bg.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for !svc.Runner.Holding() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !svc.Runner.Holding() {
		t.Fatal("the runner never took the lease")
	}
	if err := svc.Reconciler.Pass(ctx); err != nil {
		t.Fatalf("reconciler pass: %v", err)
	}

	tok, _, err := svc.Tokens.Create(ctx, "canary", []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := runpod.RunCanary(ctx, runpod.CanaryOptions{BaseURL: publicURL, Token: tok, Case: runpod.CaseMain,
		SweepEvery: 200 * time.Millisecond, WaitS: 5, AfterCancelWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Outcome != "pass" || rep.Exit != runpod.ExitPass || rep.FinalState != "completed" {
		t.Fatalf("canary report %+v", rep)
	}
	for _, c := range rep.Checks {
		if !c.OK {
			t.Errorf("check %q failed: %s", c.Name, c.Note)
		}
	}
	// every call went to the test server with the right key kinds, and the admin key was never used
	if n := rp.Calls("run"); n != 1 {
		t.Errorf("run calls = %d, want 1", n)
	}
	for _, r := range rp.Requests() {
		if r.Key == runpodtest.KindAdmin {
			t.Errorf("%s used the admin key", r.Op)
		}
	}
}
