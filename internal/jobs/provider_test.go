package jobs

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func (r *rig) admin(mode string, keys bool, mods ...func(*ProviderOptions)) *ProviderAdmin {
	r.t.Helper()
	gate := provider.NewGate(r.st.R, provider.GateConfig{Mode: mode, KeysLoaded: keys, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, r.clk.Now)
	o := ProviderOptions{Store: r.st, Gate: gate, Clock: r.clk, KillSlackS: 15, TTL: 2 * time.Hour, PriceMarginPPM: 100000}
	for _, m := range mods {
		m(&o)
	}
	return NewProviderAdmin(o)
}

func TestUpdateProviderState_ArmDisarmReset(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.admin(provider.ModeFake, true)
	yes, no := true, false
	st, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &no, Reason: "maintenance"})
	if err != nil || st.State.Armed {
		t.Fatalf("disarm: %+v %v", st.State, err)
	}
	if got := r.str(`SELECT changed_by || '|' || reason FROM provider_state WHERE backend = 'fake'`); got != "human:h1|maintenance" {
		t.Errorf("row %q", got)
	}
	st, err = a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "back"})
	if err != nil || !st.State.Armed {
		t.Fatalf("arm: %+v %v", st.State, err)
	}
	// reset without an open trip is a no-op that still succeeds
	if _, err := a.Update(t.Context(), humanP, UpdateIn{ResetTrip: true, Reason: "nothing open"}); err != nil {
		t.Fatalf("reset without a trip: %v", err)
	}
	r.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES('trip-1', 'fake', ?, 'stuck_running', '{}', '[]')`, store.FormatTime(t0))
	st, err = a.Status(t.Context(), humanP)
	if err != nil || st.Trip == nil || st.Trip.ID != "trip-1" || !st.State.Tripped {
		t.Fatalf("status with a trip: %+v %v", st.Trip, err)
	}
	st, err = a.Update(t.Context(), humanP, UpdateIn{ResetTrip: true, Reason: "looked at it"})
	if err != nil || st.Trip != nil || st.State.Tripped {
		t.Fatalf("reset: %+v %v", st.Trip, err)
	}
	if got := r.str(`SELECT reset_by || '|' || reset_reason FROM provider_trips WHERE id = 'trip-1'`); got != "human:h1|looked at it" {
		t.Errorf("trip row %q", got)
	}
	if len(st.TripsRecent) != 1 || st.TripsRecent[0].ResetAt == nil {
		t.Errorf("recent trips %+v", st.TripsRecent)
	}
	// both at once
	r.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES('trip-2', 'fake', ?, 'crash_loop', '{}', '[]')`, store.FormatTime(t0.Add(time.Minute)))
	if st, err = a.Update(t.Context(), humanP, UpdateIn{Armed: &no, ResetTrip: true, Reason: "both"}); err != nil || st.State.Armed || st.State.Tripped {
		t.Errorf("both: %+v %v", st.State, err)
	}
}

func TestUpdateProviderState_Validation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.admin(provider.ModeFake, true)
	yes := true
	for _, reason := range []string{"", "   ", strings.Repeat("x", 501)} {
		if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: reason}); !errors.Is(err, ErrReasonRequired) {
			t.Errorf("reason %q: %v", reason, err)
		}
	}
	if _, err := a.Update(t.Context(), humanP, UpdateIn{Reason: "why"}); !errors.Is(err, ErrNothingToChange) {
		t.Errorf("nothing to change: %v", err)
	}
	// disabled runpod: keys absent
	rp := r.admin(provider.ModeRunPod, false)
	var gd *GenerationDisabledError
	if _, err := rp.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "try"}); !errors.As(err, &gd) || gd.Reason != provider.ReasonKeysAbsent {
		t.Errorf("keys absent: %v", err)
	}
	// no backend at all
	none := r.admin(provider.ModeNone, false)
	if _, err := none.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "try"}); !errors.As(err, &gd) || gd.Reason != provider.ReasonBackendNone {
		t.Errorf("no backend: %v", err)
	}
	if _, err := none.Update(t.Context(), humanP, UpdateIn{ResetTrip: true, Reason: "try"}); !errors.As(err, &gd) {
		t.Errorf("reset without a backend: %v", err)
	}
	// runpod with a stale quote cannot be armed; a fresh one can
	r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES(?, 0, 'system:default', ?)`, provider.BackendRunPod, store.FormatTime(t0))
	if _, err := r.admin(provider.ModeRunPod, true).Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "try"}); !errors.As(err, &gd) || gd.Reason != provider.ReasonQuoteStale {
		t.Errorf("stale quote: %v", err)
	}
	q, _ := json.Marshal(newNamedQuote(r))
	r.exec(`UPDATE provider_state SET quote_json = ?, quote_at = ? WHERE backend = ?`, string(q), store.FormatTime(t0), provider.BackendRunPod)
	st, err := r.admin(provider.ModeRunPod, true).Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "go"})
	if err != nil || !st.State.Armed {
		t.Errorf("fresh quote: %+v %v", st.State, err)
	}
}

func newNamedQuote(r *rig) provider.Quote {
	q := goodQuoteFor(provider.BackendRunPod, r.clk.Now())
	return q
}

func TestRestore_DisarmsAndRequiresReconcile(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.admin(provider.ModeFake, true)
	yes := true
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	g := r.ghost(job, "pending", false)
	restoredAt := r.clk.Now().Add(time.Hour)
	if err := backup.DisarmAfterRestore(t.Context(), r.st, restoredAt, r.newID); err != nil {
		t.Fatal(err)
	}
	if got := r.str(`SELECT armed || '|' || reason || '|' || changed_by || '|' || restored_at FROM provider_state WHERE backend = 'fake'`); got != "0|restored|system:cli|"+store.FormatTime(restoredAt) {
		t.Errorf("provider_state %q", got)
	}
	if got := r.count(`SELECT COUNT(*) FROM job_attempts WHERE state IN ('pending','submitted')`); got != 0 {
		t.Errorf("%d live attempts still pending or submitted", got)
	}
	for _, id := range []string{g, r.att(job, 1).ID} {
		if got := r.str(`SELECT state FROM job_attempts WHERE id = ?`, id); got != "unknown" {
			t.Errorf("attempt %s is %s", id, got)
		}
	}
	if r.auditOps("cli:restore-disarm") != 1 {
		t.Error("restore disarm not audited")
	}
	r.clk.Set(restoredAt)
	_, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "go"})
	if !errors.Is(err, ErrReconcileRequired) {
		t.Fatalf("arming right after a restore: %v", err)
	}
	st, err := a.Status(t.Context(), humanP)
	if err != nil || !st.RestoredUnreconciled || st.State.Armed {
		t.Errorf("status %+v %v", st.RestoredUnreconciled, err)
	}
	// a reconciliation window that ended before the restore does not cover it
	r.exec(`INSERT INTO billing_windows(window_start, window_end, granularity, buckets, billed_micro_usd, billed_compute_micro_usd, provisional_micro_usd, reserved_micro_usd, batches, verdict, fetched_at)
		VALUES(?, ?, 'hour', 1, 0, 0, 0, 0, 0, 'ok', ?)`, store.FormatTime(restoredAt.Add(-3*time.Hour)), store.FormatTime(restoredAt.Add(-time.Hour)), store.FormatTime(restoredAt))
	if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "go"}); !errors.Is(err, ErrReconcileRequired) {
		t.Fatalf("an earlier window: %v", err)
	}
	// billing none: only the explicit acknowledgment of `runpod arm --after-restore`
	st, err = a.Update(t.Context(), auth0CLI(), UpdateIn{Armed: &yes, Reason: "billing is not readable", AfterRestore: true})
	if err != nil || !st.State.Armed {
		t.Fatalf("after-restore acknowledgment: %+v %v", st.State, err)
	}
	if st.RestoredUnreconciled {
		t.Error("the acknowledgment clears the restore state")
	}
	// the other way out: a window that covers the restore
	r2 := newRig(t)
	a2 := r2.admin(provider.ModeFake, true)
	if err := backup.DisarmAfterRestore(t.Context(), r2.st, restoredAt, r2.newID); err != nil {
		t.Fatal(err)
	}
	r2.exec(`INSERT INTO billing_windows(window_start, window_end, granularity, buckets, billed_micro_usd, billed_compute_micro_usd, provisional_micro_usd, reserved_micro_usd, batches, verdict, fetched_at)
		VALUES(?, ?, 'hour', 1, 0, 0, 0, 0, 0, 'ok', ?)`, store.FormatTime(restoredAt.Add(-time.Hour)), store.FormatTime(restoredAt), store.FormatTime(restoredAt))
	if st, err := a2.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "covered"}); err != nil || !st.State.Armed {
		t.Errorf("covered restore: %+v %v", st.State, err)
	}
}

func TestGetProviderStatus_NoSecrets(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	canary := "canaryEndpointId0123"
	q := goodQuoteFor(provider.BackendFake, t0)
	q.Endpoint.RequestURLs = map[string]string{"run": "https://api.runpod.ai/v2/" + canary + "/run"}
	q.Endpoint.ImageDigest = "sha256:" + strings.Repeat("a", 64)
	raw, _ := json.Marshal(q)
	// what the reconciler persists is redacted; the admin must not add the id either way
	rec := r.recon()
	r.mu.Lock()
	r.quoteFn = func() (provider.Quote, error) { return q, nil }
	r.mu.Unlock()
	r.mustPass(rec)
	_ = raw
	a := r.admin(provider.ModeFake, true, func(o *ProviderOptions) {
		o.Sweep = rec.Snapshot
		o.Capabilities = r.be.Capabilities
	})
	r.sweep(worker("secret-worker-id", "RUNNING", upt(10), t0), worker("w2", "IDLE", upt(5), t0))
	r.mustPass(rec)
	st, err := a.Status(t.Context(), agentP)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(st)
	for _, bad := range []string{canary, "secret-worker-id", "api.runpod.ai", "https://", "worker_key"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("the status leaks %q: %s", bad, b)
		}
	}
	if st.Workers == nil || st.Workers.Running != 1 || st.Workers.Idle != 1 {
		t.Errorf("workers %+v", st.Workers)
	}
	if st.Endpoint == nil || st.Endpoint.ImageDigestShort != strings.Repeat("a", 12) || st.PricePerHour != money.MicroUSD(690000) {
		t.Errorf("endpoint %+v price %d", st.Endpoint, st.PricePerHour)
	}
	if st.LastReconcile == nil || st.Capability.Billing != "hour" || st.ColdStartS != 180 || st.KillSlackS != 15 || st.MaxRuntimeS != 900 || st.TTLS != 7200 || st.PriceMarginPPM != 100000 {
		t.Errorf("limits %+v", st)
	}
	if len(st.RTF) != 3 {
		t.Errorf("rtf %+v", st.RTF)
	}
	if st.Meter == nil || st.Meter.Lifetimes != 1 { // the IDLE worker was never RUNNING: no lifetime
		t.Errorf("meter %+v", st.Meter)
	}
}

func TestProviderStatus_TripActionsFromAudit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedBatch(batchSpec{})
	rec := r.recon()
	r.sweep(worker("w1", "RUNNING", upt(30), t0))
	r.mustPass(rec)
	r.advance(3 * time.Minute)
	r.mustPass(rec)
	a := r.admin(provider.ModeFake, true)
	st, err := a.Status(t.Context(), humanP)
	if err != nil || st.Trip == nil || st.Trip.Reason != TripStuckRunning {
		t.Fatalf("%+v %v", st.Trip, err)
	}
	if len(st.Trip.Actions) != 2 || st.Trip.Actions[0] != "purge_queue" || st.Trip.Actions[1] != "scale_to_zero" {
		t.Errorf("actions %v", st.Trip.Actions)
	}
}
