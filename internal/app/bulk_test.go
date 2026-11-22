package app_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

func podCfg(c *config.Config) {
	c.PodBackend = true
	c.PodImage = "ghcr.io/example/mm-worker@sha256:" + strings.Repeat("a", 64)
	c.PodGPUTypes = []string{"NVIDIA GeForce RTX 4090"}
	c.PodMinTakes, c.PodMaxTakes, c.PodBootS, c.PodIdleS, c.PodSlackS = 30, 100, 1200, 120, 300
	c.PodMaxRuntime, c.PodDeadman, c.PodOrphanAfter, c.PodWindowMax, c.PodDiskGB = 6*time.Hour, 10*time.Minute, 180*time.Second, 8*time.Hour, 40
}

func hasLoop(s *app.Services, name string) bool { return slices.Contains(loopNames(s), name) }

func podTables(t *testing.T, e *m3Env) int {
	t.Helper()
	var n int
	if err := e.st.R.QueryRow(`SELECT (SELECT COUNT(*) FROM pods) + (SELECT COUNT(*) FROM pod_assignments) + (SELECT COUNT(*) FROM pod_events) + (SELECT COUNT(*) FROM pod_safety_log)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// With MM_POD_BACKEND=0 (production) the pod backend does not exist: no backend in the set, no gate, no provider_state row, no poll route,
// no lifecycle loop and no client, over a database at schema 29 or later (the pod tables exist and stay empty). The loop therefore does
// nothing: it is not there to do anything.
func TestApp_PodBackendOffIsInert(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{config.BackendFake, config.BackendRunPod, config.BackendNone} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			e := newM3Env(t, func(c *config.Config) { c.Backend = mode })
			if mode == config.BackendRunPod {
				writeRunPodEnv(t, e, "")
			}
			var version int
			if err := e.st.R.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version < 29 {
				t.Fatalf("schema %d (%v): the production schema is 29", version, err)
			}
			s := e.build(t, app.Options{M3: app.M3Options{PodAPI: panicAPI{t}}}) // an injected client would be used only if a pod backend were built
			if s.Backends != nil {
				for _, n := range s.Backends.Names() {
					if provider.IsPodBackend(n) {
						t.Errorf("backend %s exists with MM_POD_BACKEND=0", n)
					}
				}
			}
			if hasLoop(s, "pod-lifecycle") {
				t.Errorf("loops %v", loopNames(s))
			}
			if s.Server.Pods != nil {
				t.Error("the poll route is mounted")
			}
			for _, n := range []string{provider.BackendFakePod, provider.BackendRunPodPod} {
				if _, ok := e.row(t, n); ok {
					t.Errorf("provider_state has a row for %s", n)
				}
			}
			if n := podTables(t, e); n != 0 {
				t.Errorf("pod tables hold %d rows", n)
			}
			// the loops that do exist run for a moment: nothing is written to the pod tables and no money moves
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			for _, l := range s.Loops {
				go l.Run(ctx)
			}
			<-ctx.Done()
			if n := podTables(t, e); n != 0 {
				t.Errorf("pod tables hold %d rows after the loops ran", n)
			}
			var led int
			if err := e.st.R.QueryRow(`SELECT COUNT(*) FROM ledger WHERE reason = 'pod_overhead'`).Scan(&led); err != nil || led != 0 {
				t.Errorf("pod overhead rows %d (%v)", led, err)
			}
		})
	}
}

// panicAPI fails the test on any call: it stands for the RunPod pod API in a process that must not use it.
type panicAPI struct{ t *testing.T }

func (p panicAPI) CreatePod(context.Context, pods.CreateBody) (pods.Pod, error) {
	p.t.Error("CreatePod called")
	return pods.Pod{}, nil
}
func (p panicAPI) GetPod(context.Context, string) (pods.Pod, error) {
	p.t.Error("GetPod called")
	return pods.Pod{}, nil
}
func (p panicAPI) ListPods(context.Context) ([]pods.Pod, error) {
	p.t.Error("ListPods called")
	return nil, nil
}
func (p panicAPI) DeletePod(context.Context, string) error { p.t.Error("DeletePod called"); return nil }
func (p panicAPI) CancelQueued(context.Context, string) error {
	p.t.Error("CancelQueued called")
	return nil
}
func (p panicAPI) PodLogsTail(context.Context, string, int) ([]string, error) {
	p.t.Error("PodLogsTail called")
	return nil, nil
}
func (p panicAPI) CatalogSecurePrices(context.Context, []string) (map[string]money.MicroUSD, error) {
	p.t.Error("CatalogSecurePrices called")
	return nil, nil
}
func (p panicAPI) PodBilling(context.Context, time.Time, time.Time, []string) ([]provider.BillBucket, error) {
	p.t.Error("PodBilling called")
	return nil, nil
}

// TestApp_ExtraBackendsList: the extra backends are a list of constructors, each built only under its own flag.
func TestApp_ExtraBackendsList(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mut  func(*config.Config)
		want []string
	}{
		{"neither", func(c *config.Config) {}, []string{provider.BackendFake}},
		{"ACE-Step only", aceCfg, []string{provider.BackendFake, provider.BackendFakeACEStep}},
		{"pods only", podCfg, []string{provider.BackendFake, provider.BackendFakePod}},
		{"both", func(c *config.Config) { aceCfg(c); podCfg(c) }, []string{provider.BackendFake, provider.BackendFakePod, provider.BackendFakeACEStep}}, // constructors run in init order: bulk.go before e2_hooks.go
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake; tc.mut(c) })
			s := e.build(t, app.Options{})
			if got := s.Backends.Names(); !slices.Equal(got, tc.want) {
				t.Errorf("backends %v, want %v", got, tc.want)
			}
			if !slices.Equal(s.Gates.Names(), tc.want) {
				t.Errorf("gates %v", s.Gates.Names())
			}
			// yue2 keeps one pin however many yue2 backends there are, and the default route is unchanged
			if be, err := s.Backends.Route("yue2", ""); err != nil || be != provider.BackendFake {
				t.Errorf("default yue2 route %q %v", be, err)
			}
		})
	}
}

func TestApp_PodBackendBuiltUnderFake(t *testing.T) {
	t.Parallel()
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake; podCfg(c) })
	s := e.build(t, app.Options{})
	if !hasLoop(s, "pod-lifecycle") || s.Server.Pods == nil {
		t.Fatalf("loops %v, poll route %v", loopNames(s), s.Server.Pods != nil)
	}
	if r, ok := e.row(t, provider.BackendFakePod); !ok || r.Armed != 1 || r.ChangedBy != "system:default" {
		t.Errorf("fake-pod row %+v %v", r, ok)
	}
	// the gate carries the pod's boot and window, so the global worker bound counts one worker over MM_POD_MAX_RUNTIME + BootS + K
	g, ok := s.Gates.For(provider.BackendFakePod)
	if !ok {
		t.Fatal("no gate")
	}
	if c := g.Config(); c.ColdStartS != 1200 || c.MaxRuntime != 6*time.Hour || c.Engine != provider.EngineYuE2 && c.Engine != "" {
		t.Errorf("gate %+v", c)
	}
	be, _ := s.Backends.Get(provider.BackendFakePod)
	if be.ColdStartS() != 0 || be.MaxRuntime() != 15*time.Minute {
		t.Errorf("the backend keeps its per-take values: cold %d max %s", be.ColdStartS(), be.MaxRuntime())
	}
	// the pods of the pod backend route only when asked for
	if b, err := s.Backends.Route("yue2", provider.BackendFakePod); err != nil || b != provider.BackendFakePod {
		t.Errorf("requested route %q %v", b, err)
	}
	// the provider admin can disarm it (the window rules need that)
	if did, err := s.Provider.SystemDisarm(context.Background(), provider.BackendFakePod, "window_idle"); err != nil || !did {
		t.Errorf("system disarm: %v %v", did, err)
	}
	if r, _ := e.row(t, provider.BackendFakePod); r.Armed != 0 || r.ChangedBy != jobs.SystemActor {
		t.Errorf("after the disarm %+v", r)
	}
	if reflect.DeepEqual(s.Reconciler, s.Reconciler.For(provider.BackendFakePod)) {
		t.Error("the default reconciler is not the pod backend's")
	}
	if s.Reconciler.For(provider.BackendFakePod) == nil {
		t.Error("no reconciler for the pod backend (its Trip is wired from it)")
	}
}

// Until the pod estimate is priced (BULK-P6) the real backend is built but closed: it cannot be armed and nothing is created for it.
func TestApp_RealPodBackendClosedUntilPriced(t *testing.T) {
	t.Parallel()
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendRunPod; podCfg(c) })
	writeRunPodEnv(t, e, "")
	s := e.build(t, app.Options{M3: app.M3Options{PodAPI: panicAPI{t}}})
	if !slices.Contains(s.Backends.Names(), provider.BackendRunPodPod) {
		t.Fatalf("backends %v", s.Backends.Names())
	}
	g, _ := s.Gates.For(provider.BackendRunPodPod)
	st, err := g.State(context.Background())
	if err != nil || st.Enabled || st.Reason != provider.ReasonKeysAbsent {
		t.Errorf("gate state %+v %v", st, err)
	}
	if r, ok := e.row(t, provider.BackendRunPodPod); !ok || r.Armed != 0 {
		t.Errorf("real pod backend row %+v %v: a new real backend starts disarmed", r, ok)
	}
}

// TestPodTripReasons_MatchThePodsPackage: the strings the loop opens trips with are the ones jobs names.
func TestPodTripReasons_MatchThePodsPackage(t *testing.T) {
	t.Parallel()
	for _, p := range [][2]string{{pods.TripOverrun, jobs.TripPodOverrun}, {pods.TripDeadman, jobs.TripPodDeadman}, {pods.TripUnknown, jobs.TripPodUnknown},
		{pods.TripPrice, jobs.TripPodPrice}, {pods.TripAccess, jobs.TripPodAccess}, {pods.TripExcess, jobs.TripPodExcess},
		{pods.TripCreateInvalid, jobs.TripPodCreateInvalid}} {
		if p[0] != p[1] || !strings.HasPrefix(p[0], "pod_") {
			t.Errorf("%q != %q", p[0], p[1])
		}
	}
}
