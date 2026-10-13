package servertest_test

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// TestParity_DeterministicTokens: three rigs mint the same confirmation-token sequence from Options.Rand.
func TestParity_DeterministicTokens(t *testing.T) {
	t.Parallel()
	mint := func(o servertest.Options) []string {
		r := servertest.New(t, o)
		var out []string
		for range 4 {
			tok, err := r.Services.Spend.NewToken()
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, tok)
		}
		return out
	}
	a, b, c := mint(servertest.Options{}), mint(servertest.Options{}), mint(servertest.Options{Rand: servertest.SeededReader(1)})
	for i := range a {
		if a[i] != b[i] || a[i] != c[i] {
			t.Errorf("token %d differs between rigs: %q %q %q", i, a[i], b[i], c[i])
		}
	}
	if d := mint(servertest.Options{Rand: servertest.SeededReader(2)}); d[0] == a[0] {
		t.Error("a different seed must give different tokens")
	}
	if a[0] == a[1] {
		t.Error("two mints gave one token")
	}
}

func TestServertest_M3Options(t *testing.T) {
	t.Parallel()
	caps := spend.Caps{MonthlyMicro: 2_000_000, BatchMicro: 1_000_000, HumanDailyMicro: 900_000, AgentDailyMicro: 800_000, AutoApproveTakes: 3, AutoApproveMicro: 100_000}
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, Backend: "fake", Caps: &caps, FakeSpeed: fake.Speed{Slow: time.Second}})
	if r.Fake == nil || r.Runner == nil || r.Reconciler == nil || r.Backend.Name() != provider.BackendFake {
		t.Fatalf("fake rig: fake=%v runner=%v reconciler=%v", r.Fake, r.Runner, r.Reconciler)
	}
	var monthly, auto int64
	var by string
	if err := r.Store.R.QueryRow(`SELECT monthly_micro_usd, auto_approve_micro_usd, updated_by FROM spend_caps`).Scan(&monthly, &auto, &by); err != nil {
		t.Fatal(err)
	}
	if monthly != int64(money.MicroUSD(2_000_000)) || auto != 100_000 || by != "system:test" {
		t.Errorf("caps row = %d %d %s", monthly, auto, by)
	}
	var jobs int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&jobs); err != nil || jobs != 4 {
		t.Errorf("FixtureM3 jobs = %d (%v), want 4", jobs, err)
	}
	// the one manual clock drives the services' timers
	var _ clock.Clock = r.Clock
	before := r.Clock.Now()
	r.Clock.Advance(time.Minute)
	if got := r.Clock.Now().Sub(before); got != time.Minute {
		t.Errorf("clock advanced %v", got)
	}
	// without a backend the rig has no runner at all
	none := servertest.New(t, servertest.Options{})
	if none.Fake != nil || none.Runner != nil || none.Backend != nil {
		t.Error("backend none must build no backend and no runner")
	}
}
