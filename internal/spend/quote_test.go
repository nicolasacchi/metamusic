package spend

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

var goldenParams = QuoteParams{PricePPH: 690000, RTFMilli: 440, ColdStartS: 180, IdleS: 20, WorkersMax: 2, MaxRuntimeS: 900, KillSlackS: 15}

func TestQuoteBatch_Example(t *testing.T) {
	t.Parallel()
	audio := []int{216, 216, 216, 216, 216, 216, 216}
	q := QuoteBatch(goldenParams, audio)
	tc := q.Takes[0]
	for name, p := range map[string][2]int64{
		"expected_s": {int64(tc.ExpectedS), 96}, "timeout_s": {int64(tc.TimeoutS), 186}, "take": {int64(tc.TakeMicro), 38525},
		"overhead": {int64(q.AttemptOverheadMicro), 38334}, "max_i": {int64(tc.MaxMicro), 76859}, "expected_i": {int64(tc.ExpectedMicro), 18400},
		"max": {int64(q.MaxMicro), 538013}, "retry": {int64(q.RetryAllowanceMicro), 153718}, "ceiling": {int64(q.CeilingMicro), 691731},
		"expected": {int64(q.ExpectedMicro), 167134}, "eta": {int64(q.ETAS), 564}, "overhead_s": {int64(q.AttemptOverheadS), 200},
	} {
		if p[0] != p[1] {
			t.Errorf("%s = %d, want %d", name, p[0], p[1])
		}
	}
	if len(q.Takes) != 7 {
		t.Errorf("takes = %d", len(q.Takes))
	}
}

func TestQuoteBatch_Clamp(t *testing.T) {
	t.Parallel()
	p := goldenParams
	p.RTFMilli = 2000
	tc := QuoteBatch(p, []int{360}).Takes[0]
	if tc.ExpectedS != 720 || tc.TimeoutS != 900 {
		t.Fatalf("expected %d timeout %d", tc.ExpectedS, tc.TimeoutS)
	}
	if want := money.ForSeconds(915, p.PricePPH); tc.TakeMicro != want {
		t.Errorf("take = %d, want ForSeconds(915) = %d", tc.TakeMicro, want)
	}
	if tc.ExpectedMicro != money.ForSeconds(720, p.PricePPH) {
		t.Errorf("expected micro = %d", tc.ExpectedMicro)
	}
}

func TestQuoteBatch_PerAttemptOverhead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ n, workers int }{{1, 2}, {3, 1}, {3, 2}, {5, 2}} {
		p := goldenParams
		p.WorkersMax = tc.workers
		audio := make([]int, tc.n)
		for i := range audio {
			audio[i] = 100 + 10*i
		}
		q := QuoteBatch(p, audio)
		var sumTake, maxI money.MicroUSD
		for _, c := range q.Takes {
			sumTake += c.TakeMicro
			maxI = max(maxI, c.MaxMicro)
		}
		if want := sumTake + money.MicroUSD(tc.n)*q.AttemptOverheadMicro; q.MaxMicro != want {
			t.Errorf("n=%d workers=%d: max %d, want Σtake + n×overhead = %d", tc.n, tc.workers, q.MaxMicro, want)
		}
		if want := money.MicroUSD(min(tc.n, tc.workers)) * maxI; q.RetryAllowanceMicro != want {
			t.Errorf("n=%d workers=%d: retry %d, want %d", tc.n, tc.workers, q.RetryAllowanceMicro, want)
		}
	}
	if q := QuoteBatch(goldenParams, []int{216}); q.MaxMicro != q.Takes[0].TakeMicro+q.AttemptOverheadMicro {
		t.Error("n=1 carries exactly one 200 s overhead")
	}
}

func TestQuoteBatch_Properties(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(7, 11))
	for range 1000 {
		p := QuoteParams{PricePPH: money.MicroUSD(1 + rng.IntN(2_000_000)), RTFMilli: 50 + rng.IntN(4950), ColdStartS: rng.IntN(400),
			IdleS: 1 + rng.IntN(100), WorkersMax: 1 + rng.IntN(2), MaxRuntimeS: 60 + rng.IntN(2700), KillSlackS: rng.IntN(60)}
		audio := make([]int, 1+rng.IntN(50))
		for i := range audio {
			audio[i] = 1 + rng.IntN(360)
		}
		q := QuoteBatch(p, audio)
		if q.MaxMicro < q.ExpectedMicro || q.CeilingMicro < q.MaxMicro || q.CeilingMicro != q.MaxMicro+q.RetryAllowanceMicro {
			t.Fatalf("params %+v audio %v: expected %d max %d ceiling %d", p, audio, q.ExpectedMicro, q.MaxMicro, q.CeilingMicro)
		}
		for _, c := range q.Takes {
			if c.TimeoutS > p.MaxRuntimeS || c.ExpectedMicro > c.TakeMicro {
				t.Fatalf("take %+v params %+v", c, p)
			}
		}
	}
	if q := QuoteBatch(QuoteParams{PricePPH: 1}, nil); q.ETAS != 0 || q.MaxMicro != 0 {
		t.Errorf("empty quote %+v", q)
	}
}

func TestEffectivePrice_Margin(t *testing.T) {
	t.Parallel()
	if got := EffectivePrice(provider.Quote{ListPricePPH: 690000}, 100_000); got != 759000 {
		t.Errorf("price = %d, want 759000", got)
	}
}

func TestRTF_EWMA(t *testing.T) {
	t.Parallel()
	if got := NextRTF(440, 500); got != 452 {
		t.Errorf("NextRTF(440,500) = %d", got)
	}
	if got := NextRTF(440, 20); got != (4*440+50+2)/5 {
		t.Errorf("clamped low = %d", got)
	}
	if got := NextRTF(440, 99999); got != (4*440+5000+2)/5 {
		t.Errorf("clamped high = %d", got)
	}
	if ObservedRTF(216_000, 216_000) != 1000 || ObservedRTF(1, 1000) != 50 || ObservedRTF(5, 0) != 50 || ObservedRTF(1_000_000, 1000) != 5000 {
		t.Error("ObservedRTF")
	}
	r := newRig(t)
	ctx := context.Background()
	r.must(func(tx *sql.Tx) error { return r.s.RecordRTF(ctx, tx, "A5000", 108_000, 216_000) }) // obs 500
	if got := r.val(`SELECT rtf_milli FROM rtf_stats WHERE gpu_class='A5000'`); got != 452 {
		t.Errorf("A5000 = %d, want 452", got)
	}
	if got := r.val(`SELECT samples FROM rtf_stats WHERE gpu_class='A5000'`); got != 1 {
		t.Errorf("samples = %d", got)
	}
	r.must(func(tx *sql.Tx) error { return r.s.RecordRTF(ctx, tx, "H100", 2_000, 100_000) }) // obs 20 -> 50, new class
	if got := r.val(`SELECT rtf_milli FROM rtf_stats WHERE gpu_class='H100'`); got != 50 {
		t.Errorf("new class = %d, want clamped 50", got)
	}
	if err := r.tx(func(tx *sql.Tx) error { return r.s.RecordRTF(ctx, tx, "", 1, 1) }); err == nil {
		t.Error("empty class accepted")
	}
	// RTFMilli: max over the given classes, fallback to all rows
	if v, ok, err := r.s.RTFMilli(ctx, r.st.R, []string{"RTX 4090", "A5000"}); err != nil || !ok || v != 452 {
		t.Errorf("RTFMilli classes = %d %v %v", v, ok, err)
	}
	if v, _, _ := r.s.RTFMilli(ctx, r.st.R, []string{"nope"}); v != 452 {
		t.Errorf("fallback = %d", v)
	}
	if v, _, _ := r.s.RTFMilli(ctx, r.st.R, nil); v != 452 {
		t.Errorf("all = %d", v)
	}
}
