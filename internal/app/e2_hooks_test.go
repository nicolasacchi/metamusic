package app_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

func aceCfg(c *config.Config) {
	c.ACEStep, c.ACEStepColdStartS, c.ACEStepMaxRuntime = true, 300, c.JobMaxRuntime
	c.ACEStepTasks, c.ACEStepPools = []string{"turbo"}, []string{"AMPERE_24", "ADA_24"}
}

func writeRunPodEnv(t *testing.T, e *m3Env, extra string) {
	t.Helper()
	if err := os.MkdirAll(e.cfg.SecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := "RUNPOD_JOBS_KEY=rp-jobs-aaaaaaaa\nRUNPOD_READ_KEY=rp-read-bbbbbbbb\nRUNPOD_WRITE_KEY=rp-write-cccccccc\nRUNPOD_ENDPOINT_ID=ep-yue-0000\n" + extra
	if err := os.WriteFile(filepath.Join(e.cfg.SecretsDir, "runpod.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestE2Hooks_FakeACEStepBackendInstalled: MM_ACESTEP=1 over the fake backend builds fake-acestep from the init in
// e2_hooks.go (no test installs a hook), and registers the engine so list_engines can read enabled=true.
func TestE2Hooks_FakeACEStepBackendInstalled(t *testing.T) {
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake; aceCfg(c) })
	s := e.build(t, app.Options{})
	if got := s.Backends.Names(); !slices.Equal(got, []string{provider.BackendFake, provider.BackendFakeACEStep}) {
		t.Fatalf("backends %v", got)
	}
	if _, ok := s.Engines["acestep15"]; !ok {
		t.Error("the registry does not hold acestep15 under MM_ACESTEP=1")
	}
	if _, ok := s.Engines["yue2"]; !ok {
		t.Error("the registry lost yue2")
	}
}

func TestE2Hooks_WithoutACEStepNothingChanges(t *testing.T) {
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake })
	s := e.build(t, app.Options{})
	if got := s.Backends.Names(); !slices.Equal(got, []string{provider.BackendFake}) {
		t.Fatalf("backends %v", got)
	}
	if _, ok := s.Engines["acestep15"]; ok {
		t.Error("acestep15 registered without MM_ACESTEP")
	}
}

// TestE2Hooks_RunPodKeysAbsent: no RUNPOD_ACESTEP_ENDPOINT_ID (or no runpod.env at all) builds no ACE-Step backend; the
// serverless M3 backend is untouched and nothing reaches a provider.
func TestE2Hooks_RunPodKeysAbsent(t *testing.T) {
	for name, withEnv := range map[string]bool{"no runpod.env": false, "env without the ACE endpoint": true} {
		t.Run(name, func(t *testing.T) {
			e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendRunPod; aceCfg(c) })
			if withEnv {
				writeRunPodEnv(t, e, "")
			}
			s := e.build(t, app.Options{})
			for _, n := range s.Backends.Names() {
				if s.Backends.EngineOf(n) == provider.EngineACEStep {
					t.Errorf("ACE-Step backend %s built without its endpoint id", n)
				}
			}
			var rows int
			if err := e.st.R.QueryRow(`SELECT COUNT(*) FROM provider_state WHERE backend = ?`, provider.BackendRunPodACEStep).Scan(&rows); err != nil || rows != 0 {
				t.Errorf("provider_state rows for the absent backend = %d (%v)", rows, err)
			}
		})
	}
}

// TestE2Hooks_RunPodACEEndpointBuiltDisarmed: with both endpoint ids the second backend joins the set, its own provider_state
// row disarmed (D-M3-33), and no provider call is made by Build.
func TestE2Hooks_RunPodACEEndpointBuiltDisarmed(t *testing.T) {
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendRunPod; aceCfg(c) })
	writeRunPodEnv(t, e, "RUNPOD_ACESTEP_ENDPOINT_ID=ep-ace-0000\n")
	s := e.build(t, app.Options{})
	if got := s.Backends.Names(); !slices.Equal(got, []string{provider.BackendRunPod, provider.BackendRunPodACEStep}) {
		t.Fatalf("backends %v", got)
	}
	if s.Backends.EngineOf(provider.BackendRunPodACEStep) != provider.EngineACEStep {
		t.Error("engine of the ACE-Step backend")
	}
	var armed int
	if err := e.st.R.QueryRow(`SELECT armed FROM provider_state WHERE backend = ?`, provider.BackendRunPodACEStep).Scan(&armed); err != nil || armed != 0 {
		t.Errorf("armed = %d (%v), want a disarmed row", armed, err)
	}
}

// TestBuildM3_ProviderStatusShowsTheProcessLimits is the root fix of the zero kill slack: buildM3 hands the provider admin the
// configured limits.
func TestBuildM3_ProviderStatusShowsTheProcessLimits(t *testing.T) {
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake })
	s := e.build(t, app.Options{})
	st, err := s.Provider.Status(context.Background(), auth.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if st.KillSlackS != 15 || st.PriceMarginPPM != 100000 || st.TTLS != 7200 {
		t.Errorf("kill_slack_s=%d price_margin_ppm=%d ttl_s=%d, want 15, 100000, 7200", st.KillSlackS, st.PriceMarginPPM, st.TTLS)
	}
	all, err := s.Provider.StatusAll(context.Background(), auth.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(s.Backends.Names(), ","), provider.BackendFake) || len(all.Backends) == 0 {
		t.Fatalf("status all = %+v", all)
	}
}
