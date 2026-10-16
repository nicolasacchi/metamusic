package app_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type m3Env struct {
	cfg  config.Config
	st   *store.Store
	root *os.Root
	dir  string
}

func newM3Env(t *testing.T, mutate func(*config.Config)) *m3Env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir, SecretsDir: filepath.Join(dir, "secrets"),
		Runner: true, JobMaxRuntime: 15 * time.Minute, JobTTL: 2 * time.Hour, ColdStartS: 180, KillSlackS: 15, PriceMarginPPM: 100000,
		BillingLag: 6 * time.Hour, ResidualHold: 48 * time.Hour}
	if mutate != nil {
		mutate(&cfg)
	}
	return &m3Env{cfg: cfg, st: st, root: root, dir: dir}
}

func (e *m3Env) build(t *testing.T, o app.Options) *app.Services {
	t.Helper()
	s, err := app.Build(context.Background(), e.cfg, config.Secrets{Key: key}, e.st, e.root, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

type stateRow struct {
	Armed       int
	ChangedBy   string
	QuoteJSON   sql.NullString
	RowsOfTable int
}

func (e *m3Env) row(t *testing.T, backend string) (stateRow, bool) {
	t.Helper()
	var r stateRow
	err := e.st.R.QueryRow(`SELECT armed, changed_by, quote_json FROM provider_state WHERE backend = ?`, backend).Scan(&r.Armed, &r.ChangedBy, &r.QuoteJSON)
	if err == sql.ErrNoRows {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

func (e *m3Env) writeRunPodEnv(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(e.cfg.SecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// built at run time so no RUNPOD_*KEY=<value> literal exists in the repository (C85's gitleaks rule)
	body := "RUNPOD_" + "JOBS_KEY=" + "canary-jobs-" + "0001\n" + "RUNPOD_" + "READ_KEY=" + "canary-read-" + "0002\n" + "RUNPOD_ENDPOINT_ID=" + "canary-ep-0003\n"
	if err := os.WriteFile(filepath.Join(e.cfg.SecretsDir, "runpod.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApp_BackendSelection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("none", func(t *testing.T) {
		t.Parallel()
		e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendNone })
		s := e.build(t, app.Options{})
		st, err := s.Gate.State(ctx)
		if err != nil || s.Backend != nil || s.Runner != nil || s.Reconciler != nil || st.Enabled || st.Reason != provider.ReasonBackendNone {
			t.Errorf("backend=%v runner=%v state=%+v err=%v", s.Backend, s.Runner, st, err)
		}
		if _, ok := e.row(t, provider.BackendFake); ok {
			t.Error("a provider_state row was written for backend none")
		}
	})

	t.Run("fake", func(t *testing.T) {
		t.Parallel()
		e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake; c.FakeBackend = "slow:2s" })
		s := e.build(t, app.Options{})
		if s.Backend == nil || s.Backend.Name() != provider.BackendFake || s.Fake == nil || s.Runner == nil || s.Reconciler == nil {
			t.Fatalf("fake wiring: backend=%v runner=%v reconciler=%v", s.Backend, s.Runner, s.Reconciler)
		}
		r, ok := e.row(t, provider.BackendFake)
		if !ok || r.Armed != 1 || r.ChangedBy != "system:default" {
			t.Errorf("fake row = %+v (%v), want armed by system:default", r, ok)
		}
		if st, err := s.Gate.State(ctx); err != nil || !st.Enabled || !st.Armed {
			t.Errorf("state %+v %v", st, err)
		}
		if s.Deps.Generation == nil || s.Deps.Spend == nil || s.Deps.Jobs == nil || s.Deps.Provider == nil || s.Deps.Lineage == nil ||
			s.Server.Uploads == nil || s.Server.Generation == nil {
			t.Error("M3 services not wired into api.Deps and httpserver.Options")
		}
		// the runner flag off builds no runner (mcp-stdio forces it)
		e.cfg.Runner = false
		if s2 := e.build(t, app.Options{}); s2.Runner != nil || s2.Reconciler != nil || s2.Backend == nil {
			t.Errorf("runner=false: runner=%v reconciler=%v backend=%v", s2.Runner, s2.Reconciler, s2.Backend)
		}
	})

	t.Run("runpod without keys", func(t *testing.T) {
		t.Parallel()
		e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendRunPod })
		s := e.build(t, app.Options{})
		st, err := s.Gate.State(ctx)
		if err != nil || s.Backend != nil || s.Runner != nil || st.Enabled || st.Reason != provider.ReasonKeysAbsent {
			t.Errorf("backend=%v runner=%v state=%+v err=%v", s.Backend, s.Runner, st, err)
		}
	})

	t.Run("runpod with keys", func(t *testing.T) {
		t.Parallel()
		e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendRunPod })
		e.writeRunPodEnv(t)
		s := e.build(t, app.Options{})
		if s.Backend == nil || s.Backend.Name() != provider.BackendRunPod || s.Runner == nil {
			t.Fatalf("serverless backend not built: backend=%v runner=%v", s.Backend, s.Runner)
		}
		r, ok := e.row(t, provider.BackendRunPod)
		if !ok || r.Armed != 0 || r.ChangedBy != "system:default" {
			t.Errorf("serverless row = %+v (%v), want DISARMED by system:default", r, ok)
		}
		// no cached quote yet: generation stays disabled until the reconciler observes one
		if st, err := s.Gate.State(ctx); err != nil || st.Enabled || st.Reason != provider.ReasonQuoteStale {
			t.Errorf("state %+v %v", st, err)
		}
	})

	t.Run("a second start never rewrites the row", func(t *testing.T) {
		t.Parallel()
		e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake })
		e.build(t, app.Options{})
		if _, err := e.st.W.Exec(`UPDATE provider_state SET armed = 0, changed_by = 'human:alice', reason = 'paused' WHERE backend = 'fake'`); err != nil {
			t.Fatal(err)
		}
		e.build(t, app.Options{})
		var armed int
		var by, reason string
		if err := e.st.R.QueryRow(`SELECT armed, changed_by, reason FROM provider_state WHERE backend = 'fake'`).Scan(&armed, &by, &reason); err != nil {
			t.Fatal(err)
		}
		if armed != 0 || by != "human:alice" || reason != "paused" {
			t.Errorf("row rewritten: armed=%d by=%q reason=%q", armed, by, reason)
		}
	})

	t.Run("an all-access key in runpod.env refuses to start", func(t *testing.T) {
		t.Parallel()
		e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendRunPod })
		if err := os.MkdirAll(e.cfg.SecretsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := "RUNPOD_" + "JOBS_KEY=a\nRUNPOD_" + "READ_KEY=b\nRUNPOD_ENDPOINT_ID=c\nRUNPOD_" + "API_KEY=d\n"
		if err := os.WriteFile(filepath.Join(e.cfg.SecretsDir, "runpod.env"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := app.Build(ctx, e.cfg, config.Secrets{Key: key}, e.st, e.root, app.Options{}); err == nil {
			t.Fatal("Build accepted an all-access key in runpod.env")
		}
	})
}

// TestApp_GenerationEnabledNoNetwork: the enabled/disabled state is computed from provider_state and key
// presence only; a backend whose every method panics is never called.
func TestApp_GenerationEnabledNoNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, mode := range []string{config.BackendFake, config.BackendRunPod} {
		e := newM3Env(t, func(c *config.Config) { c.Backend = mode })
		name := provider.BackendFake
		if mode == config.BackendRunPod {
			name = provider.BackendRunPod
		}
		spy := providertest.NewPanickingNamed(name)
		now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		s := e.build(t, app.Options{Now: func() time.Time { return now }, M3: app.M3Options{Backend: spy}})
		if mode == config.BackendRunPod {
			q := providertest.GoodQuote(name, now.Add(-time.Minute))
			b, _ := json.Marshal(q)
			if _, err := e.st.W.Exec(`UPDATE provider_state SET quote_json = ?, quote_at = ? WHERE backend = ?`, string(b), store.FormatTime(q.ObservedAt), name); err != nil {
				t.Fatal(err)
			}
		}
		st, err := s.Gate.State(ctx)
		if err != nil || !st.Enabled || st.Reason != "" {
			t.Fatalf("%s: state %+v %v", mode, st, err)
		}
		// the quote ages out: disabled, still without a backend call
		later := now.Add(2 * time.Hour)
		s2 := e.build(t, app.Options{Now: func() time.Time { return later }, M3: app.M3Options{Backend: spy}})
		st, err = s2.Gate.State(ctx)
		wantEnabled := mode == config.BackendFake
		if err != nil || st.Enabled != wantEnabled || (!wantEnabled && st.Reason != provider.ReasonQuoteStale) {
			t.Errorf("%s later: state %+v %v", mode, st, err)
		}
		if n := spy.Calls().Total(); n != 0 {
			t.Errorf("%s: %d backend calls while computing the state", mode, n)
		}
	}
}

// TestApp_ProviderStatusIsWiredToTheSweep is the regression of the canary defect "max_workers_seen was 0 in
// every run": get_provider_status showed `workers: null` and the stub capabilities because neither the
// reconciler's snapshot nor the backend's capabilities were ever handed to the provider admin.
func TestApp_ProviderStatusIsWiredToTheSweep(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake; c.FakeBackend = "slow:2s" })
	s := e.build(t, app.Options{})
	adm := s.Deps.Provider
	before, err := adm.Status(ctx, auth.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if before.Workers != nil {
		t.Fatalf("workers before any sweep = %+v, want nil", before.Workers)
	}
	if !before.Capability.ScaleToZero || before.Capability.Billing != "hour" {
		t.Errorf("capabilities %+v are not the backend's (the fake can scale to zero and reads hourly billing)", before.Capability)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(20 * time.Second)
	for !s.Runner.Holding() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !s.Runner.Holding() {
		t.Fatal("the runner never took the lease")
	}
	if err := s.Reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := adm.Status(ctx, auth.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Workers == nil || after.LastReconcile == nil {
		t.Errorf("after a sweep: workers=%+v last_reconcile=%v, want both set", after.Workers, after.LastReconcile)
	}
}

// MM_RUNPOD_RECORD_DIR is read by the server process: the serverless backend it builds must record every
// RunPod exchange (the runner and the reconciler make them), redacted. Before this the option was carried
// into BackendOptions and then ignored, so a recording server wrote nothing.
func TestApp_RunPodRecordDirRecordsServerExchanges(t *testing.T) {
	t.Parallel()
	rec := t.TempDir()
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendRunPod; c.RunPodRecordDir = rec })
	// built at run time so no key literal exists in the repository (C85's gitleaks rule)
	set := runpodtest.KeySet{Jobs: "canary-" + "jobs-7001", Read: "canary-" + "read-7002", Write: "canary-" + "write-7003", Admin: "canary-" + "admin-7004"}
	ep := "canary-" + "ep-7005"
	rp := runpodtest.New(t, runpodtest.Options{Keys: set, EndpointID: ep})
	if err := os.MkdirAll(e.cfg.SecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := "RUNPOD_" + "JOBS_KEY=" + set.Jobs + "\nRUNPOD_" + "READ_KEY=" + set.Read + "\nRUNPOD_" + "WRITE_KEY=" + set.Write + "\nRUNPOD_ENDPOINT_ID=" + ep + "\n"
	if err := os.WriteFile(filepath.Join(e.cfg.SecretsDir, "runpod.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	s := e.build(t, app.Options{M3: app.M3Options{RunPod: &runpod.BackendOptions{MgmtBase: rp.URL, JobsBase: rp.URL}}})
	if _, err := s.Backend.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(rec, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("the server recorded nothing: %v %v", files, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{set.Jobs, set.Read, set.Write, ep, "Bearer", "Authorization"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s contains %q", filepath.Base(f), secret)
			}
		}
	}
}

// The server built its RunPod backend without the committed key probe, so Capabilities() stayed conservative
// (billing "none") in production: the reconciler never read RunPod's hourly billing and every residual hold
// waited out MM_RESIDUAL_HOLD. selectBackend must hand the embedded probe to the backend (the CLI always did).
func TestApp_RunPodBackendUsesTheCommittedKeyProbe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendRunPod })
	// built at run time so no key literal exists in the repository (C85's gitleaks rule)
	set := runpodtest.KeySet{Jobs: "canary-" + "jobs-7101", Read: "canary-" + "read-7102", Write: "canary-" + "write-7103", Admin: "canary-" + "admin-7104"}
	ep := "canary-" + "ep-7105"
	rp := runpodtest.New(t, runpodtest.Options{Keys: set, EndpointID: ep})
	if err := os.MkdirAll(e.cfg.SecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := "RUNPOD_" + "JOBS_KEY=" + set.Jobs + "\nRUNPOD_" + "READ_KEY=" + set.Read + "\nRUNPOD_" + "WRITE_KEY=" + set.Write + "\nRUNPOD_ENDPOINT_ID=" + ep + "\n"
	if err := os.WriteFile(filepath.Join(e.cfg.SecretsDir, "runpod.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	s := e.build(t, app.Options{M3: app.M3Options{RunPod: &runpod.BackendOptions{MgmtBase: rp.URL, JobsBase: rp.URL}}})
	st, err := s.Deps.Provider.Status(ctx, auth.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Capability.Billing != "hour" {
		t.Errorf("get_provider_status capabilities.billing = %q, want \"hour\" (read key present, committed probe says billing_readable)", st.Capability.Billing)
	}
}
