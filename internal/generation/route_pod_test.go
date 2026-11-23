package generation_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// withPodBackend adds the simulated pod backend (a second yue2 backend) and its own gate to the rig's sets, the way the app does
// when MM_POD_BACKEND=1. The returned gate config is the pod gate's.
func (x *rigE2) withPodBackend(mode string, keys bool) {
	x.t.Helper()
	pod := providertest.New(provider.BackendFakePod, providertest.Script{})
	set, err := provider.NewSet(provider.BackendFake, x.yue, x.ace, pod)
	if err != nil {
		x.t.Fatal(err)
	}
	podGate := provider.NewGate(x.st.R, provider.GateConfig{Mode: mode, Backend: provider.BackendFakePod, KeysLoaded: keys, MaxRuntime: x.opts.MaxRuntime}, x.clk.Now)
	aceGate, _ := x.gates.For(provider.BackendFakeACEStep)
	gates, err := provider.NewGateSet(provider.BackendFake, x.opts.Gate, aceGate, podGate)
	if err != nil {
		x.t.Fatal(err)
	}
	x.backends, x.gates = set, gates
	x.svc.UseEngines(set, gates, x.adoption, x.clipper)
	x.svc.UsePods(testPodConfig())
}

// testPodConfig is the pod estimate configuration of the rigs: the defaults of C390.2, the simulated backend (no canary rule).
func testPodConfig() generation.PodConfig {
	return generation.PodConfig{MinTakes: 30, MaxTakes: 100, BootS: 1200, IdleS: 120, SlackS: 300, MaxRuntimeS: 6 * 3600, MaxRecreate: 1,
		GPUTypes: []string{"gpu-a", "gpu-b"}}
}

// podItems is a pod-sized batch: n takes of the fixture's first take, ten seeds an item.
func podItems(x *rigE2, n int) []generation.Item {
	var out []generation.Item
	for ; n > 0; n -= 10 {
		out = append(out, newSeedItem(x.info.Takes["T1"], min(n, 10)))
	}
	return out
}

// gatesWithoutPod is the gate set of a process whose pod backend is gone.
func (x *rigE2) gatesWithoutPod() *provider.GateSet {
	x.t.Helper()
	aceGate, _ := x.gates.For(provider.BackendFakeACEStep)
	gs, err := provider.NewGateSet(provider.BackendFake, x.opts.Gate, aceGate)
	if err != nil {
		x.t.Fatal(err)
	}
	return gs
}

func (x *rigE2) estimateVia(backend string, items ...generation.Item) (generation.Estimate, error) {
	return x.svc.Estimate(context.Background(), human, generation.EstimateIn{Backend: backend, Items: items, Via: spend.ViaHumanCreate})
}

// TestRouteGate_SecondYue2Backend: a yue2 batch that names the pod backend is judged by the pod backend's own gate; without the pod backend
// in the set the request is BACKEND_UNAVAILABLE, and nothing else about routing moves (C396.2).
func TestRouteGate_SecondYue2Backend(t *testing.T) {
	t.Parallel()
	t.Run("pod backend off: unavailable, default untouched", func(t *testing.T) {
		t.Parallel()
		x := newRigE2(t)
		item := newSeedItem(x.info.Takes["T1"], 1)
		_, err := x.estimateVia(provider.BackendFakePod, item)
		pe := wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
		if pe.Details["backend"] != provider.BackendFakePod {
			t.Errorf("details = %v", pe.Details)
		}
		_, err = x.estimateVia(provider.BackendRunPodPod, item)
		wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
		if e, err := x.estimateVia("", item); err != nil || e.Backend != provider.BackendFake {
			t.Errorf("default: %v, %v", e.Backend, err)
		}
	})
	t.Run("pod in the set, no gate set: unavailable", func(t *testing.T) {
		t.Parallel()
		x := newRigE2(t)
		set, _ := provider.NewSet(provider.BackendFake, x.yue, providertest.New(provider.BackendFakePod, providertest.Script{}))
		x.svc.UseEngines(set, nil, x.adoption, x.clipper)
		_, err := x.estimateVia(provider.BackendFakePod, newSeedItem(x.info.Takes["T1"], 1))
		wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
	})
	t.Run("pod in the set without a gate of its own: unavailable", func(t *testing.T) {
		t.Parallel()
		x := newRigE2(t)
		set, _ := provider.NewSet(provider.BackendFake, x.yue, providertest.New(provider.BackendFakePod, providertest.Script{}))
		x.svc.UseEngines(set, x.gates, x.adoption, x.clipper) // gates: fake and fake-acestep only
		_, err := x.estimateVia(provider.BackendFakePod, newSeedItem(x.info.Takes["T1"], 1))
		wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
	})
	t.Run("routed to the pod backend", func(t *testing.T) {
		t.Parallel()
		x := newRigE2(t)
		x.withPodBackend(provider.ModeFake, true)
		e, err := x.estimateVia(provider.BackendFakePod, podItems(x, 30)...)
		if err != nil {
			t.Fatal(err)
		}
		if e.Backend != provider.BackendFakePod || e.Provider.Backend != provider.BackendFakePod || e.Engine != "yue2" {
			t.Errorf("estimate backend %q, provider %q, engine %q", e.Backend, e.Provider.Backend, e.Engine)
		}
		if got := x.str(`SELECT backend FROM estimates WHERE id = ?`, e.ID); got != provider.BackendFakePod {
			t.Errorf("stored backend %q", got)
		}
		// ... and ACE-Step cannot be sent to it
		_, err = x.svc.Estimate(context.Background(), human, generation.EstimateIn{Engine: aceName, Backend: provider.BackendFakePod,
			Items: []generation.Item{parentItem(lineage.ModePort, x.info.Takes["T1"])}, Via: spend.ViaHumanCreate})
		wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
	})
	t.Run("the pod gate is its own: a disabled pod backend names itself and leaves serverless enabled", func(t *testing.T) {
		t.Parallel()
		x := newRigE2(t)
		x.withPodBackend(provider.ModeRunPod, false) // no key
		item := newSeedItem(x.info.Takes["T1"], 1)
		_, err := x.estimateVia(provider.BackendFakePod, podItems(x, 30)...)
		pe := wantProblem(t, err, 503, "GENERATION_DISABLED")
		if pe.Details["reason"] != provider.ReasonKeysAbsent {
			t.Errorf("details = %v", pe.Details)
		}
		if _, err := x.estimateVia("", item); err != nil {
			t.Errorf("the default route must not be affected: %v", err)
		}
	})
	t.Run("the capability gate is the pod backend's", func(t *testing.T) {
		t.Parallel()
		x := newRigE2(t)
		x.withPodBackend(provider.ModeFake, true)
		if err := provider.AppendContract(context.Background(), x.st.W, provider.WorkerContractRow{Backend: provider.BackendFakePod, ImageDigest: "sha256:" + strings.Repeat("4", 64),
			Caps: nil, Via: provider.ContractViaCLI, ChangedBy: "system:cli", ChangedAt: x.clk.Now(), Reason: "test"}); err != nil {
			t.Fatal(err)
		}
		// the serverless backend has no row (a simulated backend: everything open); the pod backend's row closes v2
		if _, err := x.estimateVia("", v2Item()); err != nil {
			t.Fatalf("serverless route: %v", err)
		}
		_, err := x.estimateVia(provider.BackendFakePod, append(podItems(x, 29), v2Item())...)
		pe := wantProblem(t, err, 503, "GENERATION_DISABLED")
		if pe.Details["reason"] != generation.ReasonWorkerOutdated || pe.Details["backend"] != provider.BackendFakePod {
			t.Errorf("details = %v", pe.Details)
		}
		if err := provider.AppendContract(context.Background(), x.st.W, provider.WorkerContractRow{Backend: provider.BackendFakePod, ImageDigest: "sha256:" + strings.Repeat("4", 64),
			Caps: []string{"agent", "v2"}, Via: provider.ContractViaCLI, ChangedBy: "system:cli", ChangedAt: x.clk.Now(), Reason: "test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := x.estimateVia(provider.BackendFakePod, append(podItems(x, 29), v2Item())...); err != nil {
			t.Fatalf("opened: %v", err)
		}
	})
}

// TestCreate_PodRoute: an estimate routed to the pod backend creates under the pod gate (armed state, caps), and a batch of a pod
// backend that left the set is stale.
func TestCreate_PodRoute(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	x.withPodBackend(provider.ModeFake, true)
	e, err := x.estimateVia(provider.BackendFakePod, podItems(x, 30)...)
	if err != nil {
		t.Fatal(err)
	}
	x.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES ('fake-pod', 0, 'system:default', ?)`, "2026-10-06T11:00:00.000Z")
	_, err = x.create(human, e, spend.ViaHumanCreate)
	pe := wantProblem(t, err, 503, "PROVIDER_DISARMED")
	if pe.Details["backend"] != provider.BackendFakePod && pe.Details["backend"] != nil {
		t.Errorf("details = %v", pe.Details)
	}
	x.exec(`UPDATE provider_state SET armed = 1 WHERE backend = 'fake-pod'`)
	c := x.mustCreate(human, e, spend.ViaHumanCreate)
	if got := x.str(`SELECT backend FROM batches WHERE id = ?`, c.BatchID); got != provider.BackendFakePod {
		t.Errorf("batch backend %q", got)
	}
	// the pod backend leaves the set between estimate and create
	e2, err := x.estimateVia(provider.BackendFakePod, podItems(x, 30)...)
	if err != nil {
		t.Fatal(err)
	}
	x.svc.UseEngines(x.backends, x.gatesWithoutPod(), x.adoption, x.clipper)
	_, err = x.create(human, e2, spend.ViaHumanCreate)
	pe = wantProblem(t, err, 409, "ESTIMATE_STALE")
	if pe.Details["reason"] != "backend_changed" {
		t.Errorf("details = %v", pe.Details)
	}
}

// TestRouteGate_PodOffIsIdentical: with no pod backend configured (production), an estimate over a database that already carries the pod
// tables is exactly the estimate of a process that has never heard of the pod backend: same fields, same stored row, and no pod route.
func TestRouteGate_PodOffIsIdentical(t *testing.T) {
	t.Parallel()
	a, b := newRigE2(t), newRigE2(t)
	b.withPodBackend(provider.ModeFake, true) // b has the pod backend; a does not; both estimate WITHOUT naming it
	if n := a.count(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'pods'`); n != 1 {
		t.Fatalf("the rig database lacks the pod tables (%d)", n)
	}
	if n := a.count(`SELECT COUNT(*) FROM pods`); n != 0 {
		t.Fatalf("pods rows: %d", n)
	}
	norm := func(e generation.Estimate) generation.Estimate {
		e.ID, e.Token, e.ExpiresAt = "", "", e.ExpiresAt.Truncate(0)
		return e
	}
	for _, items := range [][]generation.Item{{newSeedItem(a.info.Takes["T1"], 1)}, {newSeedItem(a.info.Takes["T1"], 3)}, {v2Item()}} {
		ea, err := a.estimateVia("", items...)
		if err != nil {
			t.Fatal(err)
		}
		eb, err := b.estimateVia("", items...)
		if err != nil {
			t.Fatal(err)
		}
		if ea.Backend != provider.BackendFake || !reflect.DeepEqual(norm(ea), norm(eb)) {
			t.Errorf("estimates differ:\n%+v\n%+v", norm(ea), norm(eb))
		}
		ra := a.str(`SELECT items_json || '|' || quote_json || '|' || backend || '|' || ceiling_micro_usd || '|' || price_pph_micro_usd FROM estimates WHERE id = ?`, ea.ID)
		rb := b.str(`SELECT items_json || '|' || quote_json || '|' || backend || '|' || ceiling_micro_usd || '|' || price_pph_micro_usd FROM estimates WHERE id = ?`, eb.ID)
		if ra != rb {
			t.Errorf("stored estimate rows differ:\n%s\n%s", ra, rb)
		}
	}
	// the pod route stays shut for a process without the backend, whatever the database holds
	_, err := a.estimateVia(provider.BackendRunPodPod, newSeedItem(a.info.Takes["T1"], 1))
	wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
}
