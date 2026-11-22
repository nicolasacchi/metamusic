package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const podName = provider.BackendRunPodPod

// podAdmin is an admin over the rig's default backend and a pod backend (mode fake, so it is enabled; the arming rule is keyed on the
// backend name). The pod's provider_state row is disarmed and carries a one-worker quote at 300000 uPPH.
func (r *rig) podAdmin(name string, bound bool) *ProviderAdmin {
	r.t.Helper()
	a := r.admin(provider.ModeFake, true)
	gates, err := provider.NewGateSet(provider.BackendFake, a.o.Gate,
		provider.NewGate(r.st.R, provider.GateConfig{Mode: provider.ModeFake, Backend: name, KeysLoaded: true, ColdStartS: 1200, MaxRuntime: 6 * time.Hour}, r.clk.Now))
	if err != nil {
		r.t.Fatal(err)
	}
	q, _ := json.Marshal(provider.Quote{Backend: name, ListPricePPH: 300000, ObservedAt: t0,
		Endpoint: provider.EndpointConfig{Name: name, Type: provider.EndpointTypePod, WorkersMax: 1, GPUCount: 1}})
	r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES(?, 0, 'system:default', ?, ?, ?)`,
		name, store.FormatTime(t0), string(q), store.FormatTime(t0))
	a.UseBackends(gates, nil, nil)
	if bound {
		a.UseBound(BoundInputs{Spend: r.sp})
	}
	return a
}

func (r *rig) probe(verdict string) {
	r.t.Helper()
	r.exec(`INSERT INTO pod_safety_log(kind, verdict, detail_json, created_by, created_at) VALUES('probe', ?, '{}', 'cli', ?)`, verdict, store.FormatTime(t0))
}

func TestArmPods_RequiresProbe(t *testing.T) {
	t.Parallel()
	yes := true
	rows := []struct {
		name     string
		probes   []string
		broad    bool
		backend  string
		wantArm  bool
		wantWhat string
	}{
		{name: "no probe row", backend: podName, wantWhat: ReasonPodProbeMissing},
		{name: "failed probe", probes: []string{"failed"}, backend: podName, wantWhat: ReasonPodProbeMissing},
		{name: "ok probe", probes: []string{"ok"}, backend: podName, wantArm: true},
		{name: "too broad without the flag", probes: []string{"too_broad"}, backend: podName, wantWhat: ReasonPodProbeMissing},
		{name: "too broad with the flag", probes: []string{"too_broad"}, broad: true, backend: podName, wantArm: true},
		{name: "the latest row decides (ok then failed)", probes: []string{"ok", "failed"}, backend: podName, wantWhat: ReasonPodProbeMissing},
		{name: "the latest row decides (failed then ok)", probes: []string{"failed", "ok"}, backend: podName, wantArm: true},
		{name: "the fake pod backend needs no probe", backend: provider.BackendFakePod, wantArm: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			a := r.podAdmin(row.backend, false)
			for _, v := range row.probes {
				r.probe(v)
			}
			_, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "window", Backend: row.backend, AcceptBroadPodKey: row.broad})
			var gd *GenerationDisabledError
			switch {
			case row.wantArm && err != nil:
				t.Fatalf("arm refused: %v", err)
			case !row.wantArm && (!errors.As(err, &gd) || gd.Reason != row.wantWhat):
				t.Fatalf("err = %v, want reason %q", err, row.wantWhat)
			}
			want := 0
			if row.wantArm {
				want = 1
			}
			if got := r.count(`SELECT armed FROM provider_state WHERE backend = ?`, row.backend); got != want {
				t.Errorf("armed = %d, want %d", got, want)
			}
		})
	}
}

// A re-arm of an armed backend must not move the window start (changed_at): the 8 h maximum counts from the first arming.
func TestArmPods_ReArmKeepsTheWindowStart(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.podAdmin(provider.BackendFakePod, false)
	yes, no := true, false
	arm := func(v *bool, why string) {
		t.Helper()
		if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: v, Reason: why, Backend: provider.BackendFakePod}); err != nil {
			t.Fatal(err)
		}
	}
	at := func() string {
		return r.str(`SELECT changed_at FROM provider_state WHERE backend = ?`, provider.BackendFakePod)
	}
	arm(&yes, "first")
	first := at()
	r.clk.Advance(7 * time.Hour)
	arm(&yes, "again")
	if got := at(); got != first {
		t.Errorf("re-arm moved changed_at from %s to %s", first, got)
	}
	arm(&no, "off")
	if got := at(); got == first {
		t.Error("a disarm kept the old changed_at")
	}
	r.clk.Advance(time.Hour)
	off := at()
	arm(&yes, "new window")
	if got := at(); got == off {
		t.Error("arming a disarmed backend kept the old changed_at")
	}
}

func TestArmPods_ProbeFlagChangesNothingElsewhere(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.admin(provider.ModeFake, true)
	no, yes := false, true
	if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &no, Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "x", AcceptBroadPodKey: true}); err != nil {
		t.Fatalf("a serverless backend needs no probe: %v", err)
	}
}

// TestArmPods_GlobalBound: the pod backend contributes one worker at its price over MM_POD_MAX_RUNTIME + BootS + K (C396.5): the gate carries
// the pod's boot and window, so arming it past the per-batch ceiling or the month headroom is refused like any backend.
func TestArmPods_GlobalBound(t *testing.T) {
	t.Parallel()
	yes := true
	// price 300000 plus the admin's 10 % margin = 330000; ForSeconds(21600 + 1200 + 15, 330000)
	wantX := int64(money.ForSeconds(21600+1200+15, 330000))
	if wantX != 2_091_375 {
		t.Fatalf("hand-computed X moved: %d", wantX)
	}
	for _, tc := range []struct {
		name     string
		batchCap int64
		refused  bool
	}{
		{"over the per-batch ceiling", 2_000_000, true},
		{"within it", 10_000_000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			a := r.podAdmin(podName, true)
			r.probe("ok")
			r.exec(`UPDATE spend_caps SET batch_micro_usd = ?, monthly_micro_usd = 100000000 WHERE id = 1`, tc.batchCap)
			_, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "window", Backend: podName})
			var ce *ConcurrencyExceedsCapError
			if tc.refused {
				if !errors.As(err, &ce) {
					t.Fatalf("err = %v, want CONCURRENCY_EXCEEDS_CAP", err)
				}
				if int64(ce.XMicro) < wantX {
					t.Errorf("X = %d, want at least the pod's own %d", ce.XMicro, wantX)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ss, err := a.StatusAll(t.Context(), humanP)
			if err != nil {
				t.Fatal(err)
			}
			if got := ss.Backends[1].MaxRuntimeS; got != 21600 {
				t.Errorf("pod window = %d s, want 21600", got)
			}
			if got := ss.Backends[1].ColdStartS; got != 1200 {
				t.Errorf("pod boot = %d s, want 1200", got)
			}
		})
	}
}

func TestSystemDisarm(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.podAdmin(podName, false)
	var rows []map[string]any
	a.UseSystemAudit(func(_ context.Context, op string, d map[string]any, _ money.MicroUSD, failed bool) {
		if op != OpBulkDisarm || failed {
			t.Errorf("audit %q failed=%v", op, failed)
		}
		rows = append(rows, d)
	})
	r.exec(`UPDATE provider_state SET armed = 1 WHERE backend = ?`, podName)
	did, err := a.SystemDisarm(t.Context(), podName, "window_ended")
	if err != nil || !did {
		t.Fatalf("disarm: %v %v", did, err)
	}
	if got := r.str(`SELECT armed || '|' || changed_by || '|' || reason FROM provider_state WHERE backend = ?`, podName); got != "0|system:bulk|window_ended" {
		t.Errorf("row %q", got)
	}
	if got := r.count(`SELECT armed FROM provider_state WHERE backend = 'fake'`); got != 1 {
		t.Errorf("another backend was disarmed (armed=%d)", got)
	}
	if len(rows) != 1 || rows[0]["reason"] != "window_ended" {
		t.Errorf("audit rows %+v", rows)
	}
	// already disarmed: nothing changes, nothing is audited
	if did, err = a.SystemDisarm(t.Context(), podName, "window_idle"); err != nil || did || len(rows) != 1 {
		t.Errorf("second disarm: %v %v rows=%d", did, err, len(rows))
	}
	if got := r.str(`SELECT reason FROM provider_state WHERE backend = ?`, podName); got != "window_ended" {
		t.Errorf("the first reason was overwritten: %q", got)
	}
	for _, bad := range []string{"", "  ", strings.Repeat("x", 501)} {
		if _, err := a.SystemDisarm(t.Context(), podName, bad); !errors.Is(err, ErrBadSystemReason) {
			t.Errorf("reason %q: %v", bad, err)
		}
	}
	if _, err := a.SystemDisarm(t.Context(), "nope", "x"); !errors.Is(err, provider.ErrNoBackend) {
		t.Errorf("unknown backend: %v", err)
	}
	if _, err := NewProviderAdmin(ProviderOptions{}).SystemDisarm(t.Context(), "", "x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("no store: %v", err)
	}
}

type stubPodMeter struct {
	secs  int64
	ready bool
	err   error
	calls int
}

func (m *stubPodMeter) OverheadSeconds(context.Context, Querier, string) (int64, bool, error) {
	m.calls++
	return m.secs, m.ready, m.err
}

func TestMeterRouter_UsesPodMeter(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	rt := NewMeterRouter(d.reconcilers())
	pm := &stubPodMeter{secs: 1234, ready: true}
	rt.Use(podName, pm)
	pod := d.seedBatch(batchSpec{jobs: 1, backend: podName})
	secs, ready, err := rt.OverheadSeconds(t.Context(), d.st.R, pod.Batch)
	if err != nil || secs != 1234 || !ready || pm.calls != 1 {
		t.Fatalf("pod batch: %d %v %v calls=%d", secs, ready, err, pm.calls)
	}
	// another backend's batch never reaches the pod meter
	ace := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	if _, _, err := rt.OverheadSeconds(t.Context(), d.st.R, ace.Batch); err != nil || pm.calls != 1 {
		t.Errorf("ACE batch: %v calls=%d", err, pm.calls)
	}
	// an error and a not-ready answer pass through unchanged
	pm.ready, pm.err = false, errors.New("boom")
	if _, ready, err := rt.OverheadSeconds(t.Context(), d.st.R, pod.Batch); err == nil || ready {
		t.Errorf("error not passed: %v %v", ready, err)
	}
}
