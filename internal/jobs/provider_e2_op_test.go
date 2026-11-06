package jobs

import (
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// The numbers below are computed by hand from the dual rig: list price 690000 uPPH plus the admin's 100000 ppm margin is
// 759000; yue2 weighs ForSeconds(900+180+15, 759000) = 230863 per worker, ACE-Step ForSeconds(900+300+15, 759000) = 256163,
// two workers each.
const (
	opGBoth = 4 * 759000
	opXBoth = 2*230863 + 2*256163
	opXYue  = 2 * 230863
)

// dualAdmin is the dual rig's ProviderAdmin over both gates with spend wired for the bound.
func (d *dual) opAdmin(t *testing.T) *ProviderAdmin {
	t.Helper()
	a := d.admin(provider.ModeFake, true)
	gates, err := provider.NewGateSet(provider.BackendFake, a.o.Gate,
		provider.NewGate(d.st.R, provider.GateConfig{Mode: provider.ModeFake, Backend: aceName, Engine: provider.EngineACEStep, KeysLoaded: true, ColdStartS: 300, MaxRuntime: 15 * time.Minute}, d.clk.Now))
	if err != nil {
		t.Fatal(err)
	}
	a.UseBackends(gates, nil, nil)
	a.UseBound(BoundInputs{Spend: d.sp})
	return a
}

func TestProviderAdmin_StatusAll_PerBackendAndBound(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	a := d.opAdmin(t)
	ss, err := a.StatusAll(t.Context(), humanP)
	if err != nil {
		t.Fatal(err)
	}
	if ss.Default.State.Backend != provider.BackendFake || ss.Default.Engine != "yue2" {
		t.Errorf("default = %q/%q", ss.Default.State.Backend, ss.Default.Engine)
	}
	if len(ss.Backends) != 2 || ss.Backends[0].State.Backend != provider.BackendFake || ss.Backends[0].Engine != "yue2" ||
		ss.Backends[1].State.Backend != aceName || ss.Backends[1].Engine != provider.EngineACEStep || ss.Backends[1].ColdStartS != 300 {
		t.Fatalf("backends = %+v", ss.Backends)
	}
	if ss.Bound.GMicroPerHour != opGBoth || ss.Bound.XMicro != opXBoth || len(ss.Bound.Backends) != 2 {
		t.Errorf("bound = %+v, want G %d X %d", ss.Bound, opGBoth, opXBoth)
	}
	// a disarmed backend contributes nothing and is not listed
	d.exec(`UPDATE provider_state SET armed = 0 WHERE backend = ?`, aceName)
	if ss, err = a.StatusAll(t.Context(), humanP); err != nil {
		t.Fatal(err)
	}
	if ss.Bound.XMicro != opXYue || ss.Bound.GMicroPerHour != 2*759000 || len(ss.Bound.Backends) != 1 || ss.Bound.Backends[0] != provider.BackendFake {
		t.Errorf("bound with ACE disarmed = %+v", ss.Bound)
	}
	if ss.Backends[1].State.Armed {
		t.Error("ACE reads armed")
	}
}

func TestProviderAdmin_StatusFor(t *testing.T) {
	t.Parallel()
	d := newDual(t, false)
	a := d.opAdmin(t)
	ace, err := a.StatusFor(t.Context(), humanP, aceName)
	if err != nil || ace.State.Backend != aceName || ace.Engine != provider.EngineACEStep || ace.State.Armed {
		t.Fatalf("ACE: %+v %v", ace, err)
	}
	if def, err := a.StatusFor(t.Context(), humanP, ""); err != nil || def.State.Backend != provider.BackendFake || def.Engine != "yue2" {
		t.Errorf("default: %+v %v", def, err)
	}
	if _, err := a.StatusFor(t.Context(), humanP, "nope"); !errors.Is(err, provider.ErrNoBackend) {
		t.Errorf("unknown: %v", err)
	}
	// without a set the admin answers its one backend
	one := d.admin(provider.ModeFake, true)
	if ss, err := one.StatusAll(t.Context(), humanP); err != nil || len(ss.Backends) != 1 || ss.Backends[0].State.Backend != provider.BackendFake {
		t.Errorf("single backend: %+v %v", ss.Backends, err)
	}
}

func TestUpdateProviderState_ConcurrencyExceedsCap(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	rows := []struct {
		name                 string
		batchCap, monthCap   int64
		wantRefusal          bool
		wantG, wantX         int64
		wantBackends         int
		armACEAfterwardsWant int
	}{
		{name: "defaults allow both", batchCap: 5_000_000, monthCap: 10_000_000, armACEAfterwardsWant: 1},
		{name: "over the per-batch ceiling", batchCap: opXBoth - 1, monthCap: 10_000_000, wantRefusal: true, wantG: opGBoth, wantX: opXBoth, wantBackends: 2},
		{name: "exactly at the ceiling", batchCap: opXBoth, monthCap: 10_000_000, armACEAfterwardsWant: 1},
		{name: "over the month headroom (no spend yet, cap below X)", batchCap: 5_000_000, monthCap: opXBoth - 1, wantRefusal: true, wantG: opGBoth, wantX: opXBoth, wantBackends: 2},
		{name: "at the month headroom", batchCap: 5_000_000, monthCap: opXBoth, armACEAfterwardsWant: 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			d := newDual(t, false)
			a := d.opAdmin(t)
			d.exec(`UPDATE spend_caps SET batch_micro_usd = ?, monthly_micro_usd = ? WHERE id = 1`, row.batchCap, row.monthCap)
			_, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "go", Backend: aceName})
			var ce *ConcurrencyExceedsCapError
			if row.wantRefusal {
				if !errors.As(err, &ce) || int64(ce.GMicroPerHour) != row.wantG || int64(ce.XMicro) != row.wantX || len(ce.Backends) != row.wantBackends {
					t.Fatalf("err = %v (%+v), want CONCURRENCY_EXCEEDS_CAP G %d X %d", err, ce, row.wantG, row.wantX)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := d.count(`SELECT armed FROM provider_state WHERE backend = ?`, aceName); got != row.armACEAfterwardsWant {
				t.Errorf("ACE armed = %d, want %d", got, row.armACEAfterwardsWant)
			}
			if got := d.count(`SELECT armed FROM provider_state WHERE backend = 'fake'`); got != 1 {
				t.Errorf("yue2 armed = %d: arming ACE must never touch it", got)
			}
			// disarming is never refused by the bound
			if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &no, Reason: "stop", Backend: aceName}); err != nil {
				t.Errorf("disarm: %v", err)
			}
		})
	}
}

func TestUpdateProviderState_ArmingYue2NeverArmsACE(t *testing.T) {
	t.Parallel()
	d := newDual(t, false)
	a := d.opAdmin(t)
	yes, no := true, false
	if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &no, Reason: "off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "on"}); err != nil {
		t.Fatal(err)
	}
	if got := d.count(`SELECT armed FROM provider_state WHERE backend = ?`, aceName); got != 0 {
		t.Errorf("arming the default armed ACE-Step (armed=%d)", got)
	}
}

func TestUpdateProviderState_SingleBackendIgnoresBound(t *testing.T) {
	t.Parallel()
	d := newDual(t, false)
	a := d.admin(provider.ModeFake, true)
	a.UseBound(BoundInputs{Spend: d.sp})
	d.exec(`UPDATE spend_caps SET batch_micro_usd = 1`)
	yes, no := true, false
	if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &no, Reason: "off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "on"}); err != nil {
		t.Errorf("a one-backend process is M3: %v", err)
	}
}

// The app builds the admin without KillSlackS and PriceMarginPPM: BoundInputs carries the configuration so X is not understated.
func TestProviderAdmin_BoundUsesConfiguredSlackAndMargin(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	a := d.admin(provider.ModeFake, true, func(o *ProviderOptions) { o.KillSlackS, o.PriceMarginPPM = 0, 0 })
	gates, err := provider.NewGateSet(provider.BackendFake, a.o.Gate,
		provider.NewGate(d.st.R, provider.GateConfig{Mode: provider.ModeFake, Backend: aceName, Engine: provider.EngineACEStep, KeysLoaded: true, ColdStartS: 300, MaxRuntime: 15 * time.Minute}, d.clk.Now))
	if err != nil {
		t.Fatal(err)
	}
	a.UseBackends(gates, nil, nil)
	a.UseBound(BoundInputs{Spend: d.sp, KillSlackS: 15, PriceMarginPPM: 100000})
	ss, err := a.StatusAll(t.Context(), humanP)
	if err != nil {
		t.Fatal(err)
	}
	if ss.Bound.GMicroPerHour != opGBoth || ss.Bound.XMicro != opXBoth {
		t.Errorf("bound = %+v, want G %d X %d", ss.Bound, opGBoth, opXBoth)
	}
}
