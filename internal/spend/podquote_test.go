package spend

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/money"
)

func podParams() PodQuoteParams {
	return PodQuoteParams{
		Base:  QuoteParams{PricePPH: 300_000, RTFMilli: DefaultRTFMilli, MaxRuntimeS: 3600, KillSlackS: 15},
		BootS: 1200, IdleS: 120, SlackS: 300, OverrunS: PodOverrunGraceS + PodPassS, MaxRuntimeS: 8 * 3600, MaxRecreate: 1,
	}
}

func fifty() []TakeIn {
	takes := make([]TakeIn, 50)
	for i := range takes {
		takes[i] = TakeIn{AudioS: 216}
	}
	return takes
}

func TestPodQuote_Example(t *testing.T) {
	t.Parallel()
	p := podParams()
	p.OverrunS = 330
	q, err := QuotePod(p, fifty())
	if err != nil {
		t.Fatal(err)
	}
	tc := q.Batch.Takes[0]
	checks := []struct {
		name      string
		got, want int64
	}{
		{"expected_s", int64(tc.ExpectedS), 167}, {"timeout_s", int64(tc.TimeoutS), 332}, {"take", int64(tc.TakeMicro), 28_917},
		{"pod_overhead_s", int64(q.PodOverheadS), 1965}, {"pod_overhead", int64(q.PodOverheadMicro), 163_750},
		{"max", int64(q.Batch.MaxMicro), 1_609_600}, {"retry", int64(q.Batch.RetryAllowanceMicro), 192_667},
		{"ceiling", int64(q.Batch.CeilingMicro), 1_802_267}, {"expected", int64(q.Batch.ExpectedMicro), 805_850},
		{"deadline_s", int64(q.DeadlineS), 18_552}, {"eta_s", int64(q.Batch.ETAS), 9_550},
		{"attempt overhead", int64(q.Batch.AttemptOverheadMicro) + int64(q.Batch.AttemptOverheadS), 0},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if len(q.Batch.Takes) != 50 {
		t.Errorf("takes = %d", len(q.Batch.Takes))
	}
}

func TestPodQuote_LifetimeWithinCeiling(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(7)) // #nosec G404 -- deterministic, seeded test input
	for i := range 10_000 {
		p := podParams()
		p.Base.PricePPH = money.MicroUSD(100_000 + rng.Intn(900_000))
		p.Base.KillSlackS = 1 + rng.Intn(60)
		p.BootS, p.IdleS, p.SlackS = 60+rng.Intn(1800), rng.Intn(600), rng.Intn(600)
		p.MaxRuntimeS = 1 << 30
		p.MaxRecreate = rng.Intn(3)
		takes := make([]TakeIn, 1+rng.Intn(50))
		for j := range takes {
			takes[j] = TakeIn{AudioS: 10 + rng.Intn(300), Full: rng.Intn(2) == 0}
		}
		q, err := QuotePod(p, takes)
		if err != nil {
			t.Fatal(err)
		}
		var tmax int
		for _, tc := range q.Batch.Takes {
			tmax = max(tmax, tc.TimeoutS)
		}
		k, price := int64(p.Base.KillSlackS), p.Base.PricePPH
		f := func(s int64) money.MicroUSD { return money.ForSeconds(s, price) }
		life := int64(q.DeadlineS + p.Base.KillSlackS + p.OverrunS)
		if f(life) > q.Batch.MaxMicro+f(int64(tmax)+k) {
			t.Fatalf("case %d: a pod billed to the overrun trip costs %d > max + retry take %d", i, f(life), q.Batch.MaxMicro+f(int64(tmax)+k))
		}
		if f(life+int64(q.PodOverheadS)) > q.Batch.CeilingMicro && p.MaxRecreate >= 1 {
			t.Fatalf("case %d: with one re-created pod the cost %d exceeds the ceiling %d", i, f(life+int64(q.PodOverheadS)), q.Batch.CeilingMicro)
		}
		if q.Batch.CeilingMicro != q.Batch.MaxMicro+q.Batch.RetryAllowanceMicro {
			t.Fatalf("case %d: ceiling is not max + retry", i)
		}
	}
}

func TestPodQuote_RuntimeRefused(t *testing.T) {
	t.Parallel()
	p := podParams()
	p.MaxRuntimeS = 6 * 3600
	p.Base.MaxRuntimeS = 6 * 3600
	takes := make([]TakeIn, 60)
	for i := range takes {
		takes[i] = TakeIn{AudioS: 216, Full: true, RTFMilli: 20_000} // a very slow engine: 6 h-class timeouts
	}
	if _, err := QuotePod(p, takes); !errors.Is(err, ErrPodRuntime) {
		t.Fatalf("err = %v, want ErrPodRuntime", err)
	}
	if _, err := QuotePod(p, nil); !errors.Is(err, ErrPodNoTakes) {
		t.Fatalf("no takes: %v", err)
	}
}

func TestPodQuote_TakeInTermsCarried(t *testing.T) {
	t.Parallel()
	p := podParams()
	plain, err := QuotePod(p, []TakeIn{{AudioS: 120}})
	if err != nil {
		t.Fatal(err)
	}
	rich := TakeIn{AudioS: 120, EarsS: 40, RTFMilli: 900, LoadS: 25}
	got, err := QuotePod(p, []TakeIn{rich})
	if err != nil {
		t.Fatal(err)
	}
	want := QuoteTakes(QuoteParams{PricePPH: p.Base.PricePPH, RTFMilli: p.Base.RTFMilli, WorkersMax: 1, MaxRuntimeS: p.Base.MaxRuntimeS, KillSlackS: p.Base.KillSlackS}, []TakeIn{rich})
	if got.Batch.Takes[0] != want.Takes[0] {
		t.Fatalf("take terms not honoured: %+v vs %+v", got.Batch.Takes[0], want.Takes[0])
	}
	if got.Batch.Takes[0].EarsBudgetS == 0 || got.Batch.Takes[0].TimeoutS <= plain.Batch.Takes[0].TimeoutS {
		t.Errorf("Tier B term lost: %+v", got.Batch.Takes[0])
	}
	if got.DeadlineS <= plain.DeadlineS || got.Batch.ETAS != p.BootS+got.Batch.Takes[0].ExpectedS {
		t.Errorf("deadline %d vs %d, eta %d", got.DeadlineS, plain.DeadlineS, got.Batch.ETAS)
	}
}

func TestPodQuote_CanaryShape(t *testing.T) {
	t.Parallel()
	p := podParams()
	p.MaxRecreate = 0
	q, err := QuotePod(p, []TakeIn{{AudioS: 61}})
	if err != nil {
		t.Fatal(err)
	}
	tc := q.Batch.Takes[0]
	if q.Batch.RetryAllowanceMicro != tc.TakeMicro {
		t.Errorf("retry = %d, want one take %d and no re-creation", q.Batch.RetryAllowanceMicro, tc.TakeMicro)
	}
	if q.Batch.MaxMicro != tc.TakeMicro+q.PodOverheadMicro || q.Batch.CeilingMicro != q.Batch.MaxMicro+tc.TakeMicro {
		t.Errorf("canary money: %+v", q)
	}
	if q.DeadlineS != p.BootS+2*tc.TimeoutS+p.IdleS+p.SlackS {
		t.Errorf("deadline = %d", q.DeadlineS)
	}
}
