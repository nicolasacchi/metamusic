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

// An INITIALIZING worker is not billed (image pull, cached-model download: 30+ min on a fresh host in canary
// #2), so elapsed time alone never trips; one warning is sent after 30 minutes.
func TestReconciler_InitializingNeverTripsOnTime(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		h    provider.Health
		ws   []provider.Worker
	}{
		{"health only", provider.Health{Initializing: 1}, nil},
		{"listed", provider.Health{Initializing: 1}, []provider.Worker{worker("w1", "INITIALIZING", nil, t0)}},
		{"listed with uptime", provider.Health{Initializing: 1}, []provider.Worker{worker("w1", "INITIALIZING", upt(2000), t0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			rec := r.recon()
			r.sweepHealth(tc.h, 1, tc.ws...)
			r.passesFor(rec, 29*time.Minute)
			if _, ok := r.findNote("Worker still initializing"); ok {
				t.Fatal("warned before 30 minutes")
			}
			r.passesFor(rec, 11*time.Minute) // 40 minutes in total
			if n := r.tripCount(); n != 0 {
				t.Fatalf("a 40 minute INITIALIZING worker tripped: %v", r.openTripReason())
			}
			warnings := 0
			for _, title := range r.noteTitles() {
				if title == "Worker still initializing" {
					warnings++
				}
			}
			if warnings != 1 {
				t.Errorf("warnings %d, want exactly 1 (notes %v)", warnings, r.noteTitles())
			}
			if snap := rec.Snapshot(); snap.InitializingSince.IsZero() {
				t.Error("snapshot has no initializing_since")
			}
		})
	}
}

func TestReconciler_InitializingSinceClearsWhenRunning(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	r.sweepHealth(provider.Health{Initializing: 1}, 1)
	r.mustPass(rec)
	if rec.Snapshot().InitializingSince.IsZero() {
		t.Fatal("not set while initializing")
	}
	r.sweepHealth(provider.Health{Running: 1, InProgress: 1}, 1)
	r.advance(time.Minute)
	r.mustPass(rec)
	if !rec.Snapshot().InitializingSince.IsZero() {
		t.Error("still set while running")
	}
}

// A real loop: the same worker keeps cycling running -> initializing.
func TestReconciler_RestartLoopTrips(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		cycles   int
		gap      time.Duration
		wantTrip bool
	}{
		{"three restarts in 30 minutes", 3, 5 * time.Minute, true},
		{"two restarts do not trip", 2, 5 * time.Minute, false},
		{"three restarts spread over more than 30 minutes", 3, 20 * time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, rec := meterRig(t)
			job := r.seedBatch(batchSpec{jobs: 3}).Jobs[0]
			r.submitOne(job)
			for range tc.cycles {
				r.sweepHealth(provider.Health{Running: 1, InProgress: 1}, 1)
				r.mustPass(rec)
				r.advance(time.Minute)
				r.sweepHealth(provider.Health{Initializing: 1}, 1)
				r.mustPass(rec)
				r.advance(tc.gap)
			}
			if tc.wantTrip {
				if got := r.openTripReason(); got != TripCrashLoop {
					t.Fatalf("trip %q, want crash_loop", got)
				}
			} else if n := r.tripCount(); n != 0 {
				t.Fatalf("tripped: %v", r.openTripReason())
			}
		})
	}
}

// UNHEALTHY that persists past the grace period is a crash loop (health or listing).
func TestReconciler_PersistentUnhealthyTrips(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ws   []provider.Worker
	}{
		{"health only", nil},
		{"listed", []provider.Worker{worker("w1", "UNHEALTHY", nil, t0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			rec := r.recon()
			r.sweepHealth(provider.Health{Unhealthy: 1}, 1, tc.ws...)
			r.passesFor(rec, 4*time.Minute)
			if n := r.tripCount(); n != 0 {
				t.Fatal("tripped inside the grace period")
			}
			r.passesFor(rec, 3*time.Minute)
			if got := r.openTripReason(); got != TripCrashLoop {
				t.Fatalf("trip %q", got)
			}
		})
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

// Canary #2 (2026-10-15): one worker INITIALIZING (image pull) for 10 minutes with a submitted job, /health
// {running:0, initializing:1} and an empty listing. The pull is not billed, so nothing may trip.
func TestReconciler_ImagePullIsNotMeteredAndDoesNotTrip(t *testing.T) {
	t.Parallel()
	r, rec := meterRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	r.sweepHealth(provider.Health{Initializing: 1, InQueue: 1}, 1)
	r.passesFor(rec, 10*time.Minute)
	if got := r.tripCount(); got != 0 {
		t.Fatalf("an image pull tripped the provider: %d trips", got)
	}
	if n := r.count(`SELECT COUNT(*) FROM worker_meter`); n != 0 {
		t.Errorf("initializing worker metered: %d lifetimes", n)
	}
	// the pull ends and the worker runs the job: the lifetime starts now, not at the first INITIALIZING sweep
	r.sweepHealth(provider.Health{Running: 1, InProgress: 1}, 1)
	r.mustPass(rec)
	r.advance(time.Minute)
	r.mustPass(rec)
	if up := r.count(`SELECT uptime_s FROM worker_meter`); up != 60 {
		t.Errorf("uptime_s %d, want 60 (running time only)", up)
	}
	if got := r.tripCount(); got != 0 {
		t.Errorf("trips %d", got)
	}
}

func TestReconciler_ThrottledAndUnhealthyAreNotMetered(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	r.sweepHealth(provider.Health{Throttled: 1, Unhealthy: 1, Idle: 1}, 1)
	r.passesFor(rec, 5*time.Minute)
	if n := r.count(`SELECT COUNT(*) FROM worker_meter`); n != 0 {
		t.Errorf("lifetimes %d", n)
	}
}

// A RUNNING worker with no job still trips stuck_running after 2 minutes, and a worker that genuinely runs past
// the reservation still trips meter_exceeds_reserved.
func TestReconciler_RunningStuckAfterInitializing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	r.sweepHealth(provider.Health{Initializing: 1}, 1)
	r.passesFor(rec, 5*time.Minute)
	r.sweepHealth(healthRunning1, 1)
	r.mustPass(rec)
	r.advance(time.Minute)
	r.mustPass(rec)
	if r.tripCount() != 0 {
		t.Fatal("tripped before 2 minutes of running")
	}
	r.advance(time.Minute + time.Second)
	r.mustPass(rec)
	if got := r.openTripReason(); got != TripStuckRunning {
		t.Fatalf("trip %q", got)
	}
}

func TestReconciler_RunningPastReservationTrips(t *testing.T) {
	t.Parallel()
	r, rec := meterRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	r.sweepHealth(provider.Health{Running: 1, InProgress: 1}, 1)
	for range 60 {
		r.mustPass(rec)
		if r.tripCount() != 0 {
			break
		}
		r.advance(time.Minute)
	}
	if got := r.openTripReason(); got != TripMeterExceeds {
		t.Fatalf("trip %q, want %q", got, TripMeterExceeds)
	}
}

func (r *rig) tripCount() int { return r.count(`SELECT COUNT(*) FROM provider_trips`) }
