package spend

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/money"
)

// TestQuoteTakes_PerTakeTerms: TakeIn.RTFMilli and LoadS replace the package terms per take (C152); the zero value keeps
// every yue2 call site byte-identical.
func TestQuoteTakes_PerTakeTerms(t *testing.T) {
	t.Parallel()
	p := QuoteParams{PricePPH: 690000, RTFMilli: DefaultRTFMilli, ColdStartS: 240, IdleS: 20, WorkersMax: 2, MaxRuntimeS: 900, KillSlackS: 15}
	q := QuoteTakes(p, []TakeIn{{AudioS: 180, RTFMilli: 150, LoadS: 10}})
	tc := q.Takes[0]
	if tc.GenS != 37 || tc.ExpectedS != 47 || tc.TimeoutS != 140 || tc.GenBudgetS != 70 {
		t.Errorf("turbo 180 s: gen %d expected %d timeout %d budget %d, want 37 47 140 70", tc.GenS, tc.ExpectedS, tc.TimeoutS, tc.GenBudgetS)
	}
	if want := money.ForSeconds(155, 690000); tc.TakeMicro != want {
		t.Errorf("take reserve %d, want %d", tc.TakeMicro, want)
	}
	if q.ETAS != 240+47 {
		t.Errorf("eta %d, want 287", q.ETAS)
	}
	b := QuoteTakes(p, []TakeIn{{AudioS: 410, RTFMilli: 1200, LoadS: 20}})
	if b.Takes[0].GenS != 512 || b.Takes[0].TimeoutS != 900 {
		t.Errorf("base 410 s: gen %d timeout %d, want 512 900", b.Takes[0].GenS, b.Takes[0].TimeoutS)
	}
	// the zero terms equal M3's
	if a, w := QuoteTakes(p, []TakeIn{{AudioS: 100, Full: true}}), QuoteBatch(p, []int{100}); a.Takes[0].GenS == w.Takes[0].GenS {
		t.Error("cot full must add planning")
	}
	z1, z2 := QuoteTakes(p, []TakeIn{{AudioS: 100}}), QuoteTakes(p, []TakeIn{{AudioS: 100, RTFMilli: DefaultRTFMilli, LoadS: LoadS}})
	if z1.MaxMicro != z2.MaxMicro || z1.Takes[0] != z2.Takes[0] {
		t.Error("explicit package terms differ from the zero value")
	}
}
