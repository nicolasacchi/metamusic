package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type aceTestBackend struct{ *providertest.Backend }

func (aceTestBackend) Engine() string { return provider.EngineACEStep }

func buildSetEnv(t *testing.T, acestep bool, extra func(in e2Inputs) ([]e2Extra, error)) (*Services, *store.Store) {
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
		BillingLag: 6 * time.Hour, ResidualHold: 48 * time.Hour, Backend: config.BackendFake, ACEStep: acestep, ACEStepColdStartS: 300, ACEStepMaxRuntime: 15 * time.Minute}
	// not parallel: e2ExtraBackends is process-wide, read only when ACEStep is set
	old := e2ExtraBackends
	e2ExtraBackends = extra
	t.Cleanup(func() { e2ExtraBackends = old })
	s, err := Build(context.Background(), cfg, config.Secrets{Key: []byte("0123456789abcdef0123456789abcdef")}, st, root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s, st
}

func armedOf(t *testing.T, st *store.Store, backend string) (int, bool) {
	t.Helper()
	var armed int
	err := st.R.QueryRow(`SELECT armed FROM provider_state WHERE backend = ?`, backend).Scan(&armed)
	return armed, err == nil
}

func TestBuildE2Set_WithoutACEStepIsM3(t *testing.T) {
	s, st := buildSetEnv(t, false, func(e2Inputs) ([]e2Extra, error) { t.Error("hook called without MM_ACESTEP"); return nil, nil })
	if got := s.Backends.Names(); !slices.Equal(got, []string{provider.BackendFake}) {
		t.Errorf("backends %v", got)
	}
	if s.Reconciler.Options().Scoped || s.Reconciler.Options().Peer {
		t.Error("a single backend must keep the unscoped reconciler")
	}
	if _, ok := armedOf(t, st, provider.BackendFakeACEStep); ok {
		t.Error("an ACE-Step row exists without MM_ACESTEP")
	}
}

func TestBuildE2Set_ACEStepBackendsStartPerD_M3_33(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wantArmed int
	}{
		{provider.BackendFakeACEStep, 1},   // the fakes are armed by system:default
		{provider.BackendRunPodACEStep, 0}, // any real backend starts disarmed
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st := buildSetEnv(t, true, func(e2Inputs) ([]e2Extra, error) {
				return []e2Extra{{Backend: aceTestBackend{providertest.New(tc.name, providertest.Script{})}, KeysLoaded: true}}, nil
			})
			if got := s.Backends.Names(); !slices.Equal(got, []string{provider.BackendFake, tc.name}) {
				t.Fatalf("backends %v", got)
			}
			if got := s.Gates.Names(); !slices.Equal(got, []string{provider.BackendFake, tc.name}) {
				t.Errorf("gates %v", got)
			}
			if a, ok := armedOf(t, st, tc.name); !ok || a != tc.wantArmed {
				t.Errorf("provider_state armed=%d exists=%v, want %d", a, ok, tc.wantArmed)
			}
			if a, ok := armedOf(t, st, provider.BackendFake); !ok || a != 1 {
				t.Errorf("default row armed=%d exists=%v", a, ok)
			}
			o := s.Reconciler.Options()
			if !o.Scoped || o.Peer || o.Backend.Name() != provider.BackendFake {
				t.Errorf("the app's reconciler is the default backend's: %+v", o)
			}
			g, _ := s.Gates.For(tc.name)
			be, _ := s.Backends.Get(tc.name)
			if c := g.Config(); c.Backend != tc.name || c.Engine != provider.EngineACEStep || c.ColdStartS != be.ColdStartS() || c.MaxRuntime != be.MaxRuntime() {
				t.Errorf("gate %+v", c)
			}
			// a gate of the new backend sees its own row: armed iff the row says so
			gst, err := g.State(context.Background())
			if err != nil || gst.Backend != tc.name || gst.Armed != (tc.wantArmed == 1) {
				t.Errorf("gate state %+v %v", gst, err)
			}
		})
	}
}
