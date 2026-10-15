package jobs

import (
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// The X3 incident (2026-10-15): RunPod /health said workers {running:1} with no job in queue or in progress for
// minutes, while the workers listing showed nothing the reconciler could act on. The trip and the meter must
// work from the health counters alone.

// sweepHealth makes the next sweeps return exactly one /health shape and the given listing.
func (r *rig) sweepHealth(h provider.Health, maxWorkers int, ws ...provider.Worker) {
	r.mu.Lock()
	r.sweepFn = func() (provider.SweepReport, error) {
		hh := h
		return provider.SweepReport{Workers: ws, Health: &hh, ObservedAt: r.clk.Now(),
			Endpoint: &provider.EndpointConfig{Name: "mm-yue2", WorkersMax: maxWorkers}}, nil
	}
	r.mu.Unlock()
}

var healthRunning1 = provider.Health{Running: 1}

// passesFor runs a pass every minute for d.
func (r *rig) passesFor(rec *Reconciler, d time.Duration) {
	r.t.Helper()
	for el := time.Duration(0); el <= d; el += time.Minute {
		r.mustPass(rec)
		r.advance(time.Minute)
	}
}

func TestReconciler_HealthRunningWithoutJobsTrips(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		list       []provider.Worker
		maxWorkers int
		disarmed   bool
		scaleOK    bool
	}{
		{name: "empty listing", maxWorkers: 1, scaleOK: true},
		{name: "listing without uptime and with a pod-lifecycle status", maxWorkers: 1, scaleOK: true,
			list: []provider.Worker{{ID: "w1", Status: "EXITED"}}},
		{name: "endpoint already at max 0, no write key", maxWorkers: 0},
		{name: "provider disarmed", maxWorkers: 1, disarmed: true, scaleOK: true},
		{name: "disarmed, max 0, no write key", maxWorkers: 0, disarmed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.caps.ScaleToZero = tc.scaleOK
			if tc.disarmed {
				r.disarm()
			}
			rec := r.recon()
			r.sweepHealth(healthRunning1, tc.maxWorkers, tc.list...)
			r.mustPass(rec)
			if n := r.count(`SELECT COUNT(*) FROM provider_trips`); n != 0 {
				t.Fatal("one observation must never trip")
			}
			r.advance(time.Minute)
			r.mustPass(rec)
			if n := r.count(`SELECT COUNT(*) FROM provider_trips`); n != 0 {
				t.Fatal("tripped before 2 minutes")
			}
			r.advance(time.Minute + time.Second)
			r.mustPass(rec)
			if got := r.openTripReason(); got != TripStuckRunning {
				t.Fatalf("trip %q (running worker, no job in progress for 2 minutes)", got)
			}
			r.mu.Lock()
			purges, scales := r.purges, r.scales
			r.mu.Unlock()
			if purges != 1 {
				t.Errorf("purges %d", purges)
			}
			if tc.scaleOK != (scales == 1) {
				t.Errorf("scale-to-zero calls %d with write capability %v", scales, tc.scaleOK)
			}
			if _, ok := r.findNote("Generation stopped: stuck_running"); !ok {
				t.Errorf("notes %v", r.noteTitles())
			}
			if !tc.scaleOK {
				m, ok := r.findNote("Set max workers to 0 now")
				if !ok || m.Priority != 5 {
					t.Fatalf("no critical manual alert: %v", r.noteTitles())
				}
				already := strings.Contains(m.Body, "already 0")
				if already != (tc.maxWorkers == 0) {
					t.Errorf("alert body %q with observed max %d", m.Body, tc.maxWorkers)
				}
				if !strings.Contains(m.Body, "1 worker") {
					t.Errorf("alert does not say a worker is up: %q", m.Body)
				}
			}
		})
	}
}

func TestReconciler_HealthInProgressJobIsEvidence(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	r.sweepHealth(provider.Health{Running: 1, InProgress: 1}, 1)
	r.passesFor(rec, 6*time.Minute)
	if n := r.count(`SELECT COUNT(*) FROM provider_trips`); n != 0 {
		t.Errorf("a worker with a job in progress tripped (%d)", n)
	}
}

func TestReconciler_HealthStuckResetsWhenWorkerGone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	r.sweepHealth(healthRunning1, 1)
	r.mustPass(rec)
	r.advance(3 * time.Minute)
	r.sweepHealth(provider.Health{}, 1)
	r.mustPass(rec)
	r.advance(time.Minute)
	r.sweepHealth(healthRunning1, 1)
	r.mustPass(rec)
	r.advance(time.Minute)
	r.mustPass(rec)
	if n := r.count(`SELECT COUNT(*) FROM provider_trips`); n != 0 {
		t.Error("non-consecutive observations tripped")
	}
}

func TestReconciler_HealthInitializingTenMinutesTrips(t *testing.T) {
	t.Parallel()
	for _, h := range []provider.Health{{Initializing: 1}, {Unhealthy: 1}} {
		r := newRig(t)
		rec := r.recon()
		r.sweepHealth(h, 1)
		r.passesFor(rec, 9*time.Minute)
		if n := r.count(`SELECT COUNT(*) FROM provider_trips`); n != 0 {
			t.Fatalf("%+v tripped before 10 minutes", h)
		}
		r.advance(2 * time.Minute)
		r.mustPass(rec)
		if got := r.openTripReason(); got != TripCrashLoop {
			t.Errorf("%+v: trip %q", h, got)
		}
	}
}

func TestReconciler_MeterCountsHealthLifetimes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	r.sweepHealth(healthRunning1, 1)
	r.mustPass(rec)
	r.advance(90 * time.Second)
	r.mustPass(rec)
	if n := r.count(`SELECT COUNT(*) FROM worker_meter`); n != 1 {
		t.Fatalf("lifetimes %d, want 1 from the health counters", n)
	}
	if up := r.count(`SELECT uptime_s FROM worker_meter`); up != 90 {
		t.Errorf("uptime_s %d, want 90", up)
	}
	// the worker goes away and comes back: a second lifetime
	r.sweepHealth(provider.Health{}, 1)
	r.advance(time.Minute)
	r.mustPass(rec)
	r.sweepHealth(healthRunning1, 1)
	r.advance(time.Minute)
	r.mustPass(rec)
	if n := r.count(`SELECT COUNT(*) FROM worker_meter`); n != 2 {
		t.Errorf("lifetimes %d, want 2", n)
	}
}

func TestReconciler_MeterDoesNotDoubleCountListedWorkers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	r.sweepHealth(healthRunning1, 1, worker("w1", "RUNNING", upt(30), t0))
	r.mustPass(rec)
	r.advance(time.Minute)
	r.mustPass(rec)
	if n := r.count(`SELECT COUNT(*) FROM worker_meter`); n != 1 {
		t.Errorf("lifetimes %d, want the listed worker only", n)
	}
}
