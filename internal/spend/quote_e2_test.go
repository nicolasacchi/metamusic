package spend

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/money"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

func e2Params(runtime int) QuoteParams {
	return QuoteParams{PricePPH: 690000, RTFMilli: DefaultRTFMilli, ColdStartS: 240, IdleS: 20, WorkersMax: 2, MaxRuntimeS: runtime, KillSlackS: 15}
}

// TestQuoteTakes_Yue2Golden pins the yue2 quote (zero per-take terms) across the E2 change: the golden was generated
// from main before E2-SP touched anything and must never need -update for a yue2 reason.
func TestQuoteTakes_Yue2Golden(t *testing.T) {
	t.Parallel()
	type row struct {
		Name  string
		P     QuoteParams
		Takes []TakeIn
		Quote BatchQuote
	}
	var rows []row
	add := func(name string, p QuoteParams, takes ...TakeIn) {
		rows = append(rows, row{name, p, takes, QuoteTakes(p, takes)})
	}
	add("melody61", e2Params(900), TakeIn{AudioS: 61})
	add("full225", e2Params(900), TakeIn{AudioS: 225, Full: true})
	add("batch_mixed", e2Params(900), TakeIn{AudioS: 30}, TakeIn{AudioS: 180, Full: true}, TakeIn{AudioS: 360})
	add("runtime_clamp", e2Params(300), TakeIn{AudioS: 360, Full: true})
	p := e2Params(900)
	p.RTFMilli, p.WorkersMax = 1500, 1
	add("learned_rtf", p, TakeIn{AudioS: 90}, TakeIn{AudioS: 90})
	rows = append(rows, row{"batch_wrapper", e2Params(900), nil, QuoteBatch(e2Params(900), []int{30, 120, 360})})
	got, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "quote_yue2.golden.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(got)+"\n"); diff != "" {
		t.Errorf("yue2 quote drifted (-want +got):\n%s", diff)
	}
}

type quoteCase struct {
	name                    string
	rtf, load, audio        int
	runtime                 int
	gen, exp, timeout       int
	refusedAtDefaultRuntime bool
}

// unclamped is D-E2-17b's refusal rule: ceil(1.6 gen) + 80 above the backend runtime.
func unclamped(gen int) int {
	return int(ceilDiv(8*int64(gen), 5)) + WorkerFinishMarginS + UploadBudgetS + TimeoutSlackS
}

// TestQuoteTakes_E2Goldens pins the review-oracle rows of C152 / D-E2-17b.
func TestQuoteTakes_E2Goldens(t *testing.T) {
	t.Parallel()
	cases := []quoteCase{
		{name: "turbo180", rtf: 150, load: 10, audio: 180, runtime: 900, gen: 37, exp: 47, timeout: 140},
		{name: "turbo600", rtf: 150, load: 10, audio: 600, runtime: 900, gen: 100, exp: 110, timeout: 240},
		{name: "base300", rtf: 1200, load: 20, audio: 300, runtime: 900, gen: 380, exp: 390, timeout: 688},
		{name: "base410", rtf: 1200, load: 20, audio: 410, runtime: 900, gen: 512, exp: 522, timeout: 900},
		{name: "base411", rtf: 1200, load: 20, audio: 411, runtime: 900, gen: 514, exp: 524, timeout: 900, refusedAtDefaultRuntime: true},
		{name: "base480", rtf: 1200, load: 20, audio: 480, runtime: 900, gen: 596, exp: 606, timeout: 900, refusedAtDefaultRuntime: true},
		{name: "base480_raised", rtf: 1200, load: 20, audio: 480, runtime: 1200, gen: 596, exp: 606, timeout: 1034},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			q := QuoteTakes(e2Params(c.runtime), []TakeIn{{AudioS: c.audio, RTFMilli: c.rtf, LoadS: c.load}})
			tc := q.Takes[0]
			if tc.GenS != c.gen || tc.ExpectedS != c.exp || tc.TimeoutS != c.timeout {
				t.Errorf("gen %d exp %d timeout %d, want %d %d %d", tc.GenS, tc.ExpectedS, tc.TimeoutS, c.gen, c.exp, c.timeout)
			}
			if got := unclamped(tc.GenS) > c.runtime; got != c.refusedAtDefaultRuntime {
				t.Errorf("unclamped timeout %d vs %d: refused=%v, want %v", unclamped(tc.GenS), c.runtime, got, c.refusedAtDefaultRuntime)
			}
		})
	}
}

// TestQuoteTakes_Turbo180Money pins the batch money of the turbo 180 s take.
func TestQuoteTakes_Turbo180Money(t *testing.T) {
	t.Parallel()
	p := e2Params(900)
	p.WorkersMax = 2
	q := QuoteTakes(p, []TakeIn{{AudioS: 180, RTFMilli: 150, LoadS: 10}})
	tc := q.Takes[0]
	for name, g := range map[string][2]money.MicroUSD{
		"take reserve": {tc.TakeMicro, 29709}, "attempt overhead": {q.AttemptOverheadMicro, 49834}, "take max": {tc.MaxMicro, 79543},
		"take expected": {tc.ExpectedMicro, 9009}, "batch expected": {q.ExpectedMicro, 58843}, "batch max": {q.MaxMicro, 79543},
		"retry": {q.RetryAllowanceMicro, 79543}, "ceiling": {q.CeilingMicro, 159086},
	} {
		if g[0] != g[1] {
			t.Errorf("%s = %d, want %d", name, g[0], g[1])
		}
	}
}

// TestQuoteTakes_MixedClassBatch: turbo and base takes share one batch, each priced with its own terms.
func TestQuoteTakes_MixedClassBatch(t *testing.T) {
	t.Parallel()
	p := e2Params(900)
	q := QuoteTakes(p, []TakeIn{{AudioS: 180, RTFMilli: 150, LoadS: 10}, {AudioS: 300, RTFMilli: 1200, LoadS: 20}})
	if q.Takes[0].GenS != 37 || q.Takes[1].GenS != 380 {
		t.Errorf("gens %d %d, want 37 380", q.Takes[0].GenS, q.Takes[1].GenS)
	}
	if q.ETAS != 240+q.Takes[1].ExpectedS {
		t.Errorf("eta %d", q.ETAS)
	}
}

// TestQuoteTakes_YueUnchanged: explicit zero terms equal the M3 wrapper.
func TestQuoteTakes_YueUnchanged(t *testing.T) {
	t.Parallel()
	p := e2Params(900)
	if diff := cmp.Diff(QuoteBatch(p, []int{40, 200}), QuoteTakes(p, []TakeIn{{AudioS: 40}, {AudioS: 200}})); diff != "" {
		t.Error(diff)
	}
}
