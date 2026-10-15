package spend

import (
	"context"
	"database/sql"
	"testing"
)

// The incident (2026-10-15): a 225 s cot=full demo song was quoted expected 99 s, timeout 189 s, which
// left the worker 119 s of generation (timeout - 10 - upload budget) for a render that needs about 150.
func TestQuoteTakes_IncidentSong(t *testing.T) {
	t.Parallel()
	p := QuoteParams{PricePPH: 1_210_000, RTFMilli: DefaultRTFMilli, ColdStartS: 180, IdleS: 20, WorkersMax: 2, MaxRuntimeS: 900, KillSlackS: 15}
	for _, tc := range []struct {
		name                     string
		audio                    int
		full                     bool
		genS, expectedS, timeout int
	}{
		// full: gen = 10 + ceil(900*225/1000) = 213; timeout = ceil(1.6*213) + 70 + 10 = 421
		{"225 full", 225, true, 213, 223, 421},
		// 300 s full: gen = 10 + 270 = 280; timeout = 448 + 80 = 528
		{"300 full", 300, true, 280, 290, 528},
		// melody 225 s: gen = 10 + ceil(680*225/1000) = 163; timeout = ceil(260.8) + 80 = 341
		{"225 melody", 225, false, 163, 173, 341},
	} {
		c := QuoteTakes(p, []TakeIn{{AudioS: tc.audio, Full: tc.full}}).Takes[0]
		if c.GenS != tc.genS || c.ExpectedS != tc.expectedS || c.TimeoutS != tc.timeout {
			t.Errorf("%s: gen %d expected %d timeout %d, want %d %d %d", tc.name, c.GenS, c.ExpectedS, c.TimeoutS, tc.genS, tc.expectedS, tc.timeout)
		}
		// the worker's generation budget, as handler.deadlines computes it, is at least 1.6 x the expected generation
		if c.GenBudgetS != c.TimeoutS-WorkerFinishMarginS-UploadBudgetS || 5*c.GenBudgetS < 8*c.GenS {
			t.Errorf("%s: worker generation budget %d for expected generation %d", tc.name, c.GenBudgetS, c.GenS)
		}
	}
	if c := QuoteTakes(p, []TakeIn{{AudioS: 225, Full: true}}).Takes[0]; c.GenBudgetS < 150 {
		t.Errorf("a 225 s full song gets only %d s of worker generation budget", c.GenBudgetS)
	}
}

func TestQuoteTakes_FullSaneBand(t *testing.T) {
	t.Parallel()
	p := QuoteParams{PricePPH: 1_210_000, RTFMilli: DefaultRTFMilli, ColdStartS: 180, IdleS: 20, WorkersMax: 2, MaxRuntimeS: 900, KillSlackS: 15}
	c := QuoteTakes(p, []TakeIn{{AudioS: 300, Full: true}}).Takes[0]
	if c.ExpectedS < 270 || c.ExpectedS > 400 {
		t.Errorf("300 s full expected %d s, want within [270, 400]", c.ExpectedS)
	}
	if c.TimeoutS > p.MaxRuntimeS {
		t.Errorf("timeout %d above the runtime cap", c.TimeoutS)
	}
}

// QuoteBatch (no cot) and QuoteTakes with Full=false are the same quote; planning only adds time.
func TestQuoteTakes_FullCostsMore(t *testing.T) {
	t.Parallel()
	p := QuoteParams{PricePPH: 690_000, RTFMilli: 680, ColdStartS: 180, IdleS: 20, WorkersMax: 2, MaxRuntimeS: 900, KillSlackS: 15}
	a := QuoteBatch(p, []int{200})
	b := QuoteTakes(p, []TakeIn{{AudioS: 200}})
	if a.Takes[0] != b.Takes[0] {
		t.Errorf("%+v != %+v", a.Takes[0], b.Takes[0])
	}
	f := QuoteTakes(p, []TakeIn{{AudioS: 200, Full: true}}).Takes[0]
	if f.ExpectedS <= b.Takes[0].ExpectedS || f.TimeoutS <= b.Takes[0].TimeoutS {
		t.Errorf("full %+v not above melody %+v", f, b.Takes[0])
	}
}

func TestObservedBaseRTF(t *testing.T) {
	t.Parallel()
	// canary main: 35.0 s generation for 61.5 s of melody audio: (35.0 - 10) / 61.5 = 0.4065
	if got := ObservedBaseRTF(35_000, 61_500, false); got != 407 {
		t.Errorf("melody base = %d, want 407", got)
	}
	// the same run with planning: base excludes the planning constant, so planning is not learned twice
	if got := ObservedBaseRTF(35_000, 61_500, true); got != 407-PlanMilli {
		t.Errorf("full base = %d, want %d", got, 407-PlanMilli)
	}
	// shorter than the fixed load, or nothing left after planning: clamped
	if got := ObservedBaseRTF(5_000, 60_000, false); got != RTFMin {
		t.Errorf("below fixed load = %d", got)
	}
	if got := ObservedBaseRTF(30_000, 0, false); got != RTFMin {
		t.Errorf("zero audio = %d", got)
	}
}

func TestRTFMilli_LearnedRowsApply(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	allowed := []string{"PRO 6000 MIG 24GB", "RTX 3090", "RTX 4090", "RTX A5000"}
	// seeded rows have zero samples: priors, not measurements
	if v, ok, err := r.s.RTFMilli(ctx, r.st.R, allowed); err != nil || v != DefaultRTFMilli || ok {
		t.Fatalf("seeded only: %d %v %v, want default %d unlearned", v, ok, err, DefaultRTFMilli)
	}
	// the production row: the worker-reported name, learned
	r.exec(`INSERT INTO rtf_stats(gpu_class, rtf_milli, samples, updated_at) VALUES ('RTX PRO 6000 Blackwell Server Edition MIG 1g.24gb', 575, 3, '2026-10-15T20:00:00.000Z')`)
	// 575 is below the default for the allowed classes nobody measured: the estimate must not drop
	if v, ok, err := r.s.RTFMilli(ctx, r.st.R, allowed); err != nil || v != DefaultRTFMilli || !ok {
		t.Errorf("learned 575, three unmeasured classes: %d %v %v, want %d", v, ok, err, DefaultRTFMilli)
	}
	// a slower learned value raises it, matched through the name normalisation
	r.exec(`UPDATE rtf_stats SET rtf_milli = 900 WHERE samples = 3`)
	if v, _, _ := r.s.RTFMilli(ctx, r.st.R, allowed); v != 900 {
		t.Errorf("learned 900 = %d, want 900", v)
	}
	// a quote over only the learned class uses the learned value even below the default
	r.exec(`UPDATE rtf_stats SET rtf_milli = 575 WHERE samples = 3`)
	if v, ok, _ := r.s.RTFMilli(ctx, r.st.R, []string{"PRO 6000 MIG 24GB"}); v != 575 || !ok {
		t.Errorf("only the learned class: %d %v, want 575", v, ok)
	}
}

func TestRecordRTF_LegacyRowAndSeedReplacement(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.exec(`INSERT INTO rtf_stats(gpu_class, rtf_milli, samples, updated_at) VALUES ('RTX PRO 6000 Blackwell Server Edition MIG 1g.24gb', 600, 3, '2026-10-15T20:00:00.000Z')`)
	// the worker-reported name updates the legacy row in place: no second row for the same class
	r.must(func(tx *sql.Tx) error {
		return r.s.RecordRTF(ctx, tx, "RTX PRO 6000 Blackwell Server Edition MIG 1g.24gb", 60_000, 100_000, false) // base (60-10)/100 = 500
	})
	if got := r.val(`SELECT COUNT(*) FROM rtf_stats WHERE gpu_class LIKE '%6000%'`); got != 1 {
		t.Errorf("rows for the class = %d, want 1", got)
	}
	if got := r.val(`SELECT rtf_milli FROM rtf_stats WHERE samples = 4`); got != (4*600+500+2)/5 {
		t.Errorf("ewma = %d", got)
	}
	// the first real sample of a seeded class replaces the prior
	r.must(func(tx *sql.Tx) error { return r.s.RecordRTF(ctx, tx, "NVIDIA RTX A5000", 60_000, 100_000, false) })
	if got := r.val(`SELECT rtf_milli FROM rtf_stats WHERE gpu_class = 'A5000'`); got != 500 {
		t.Errorf("A5000 after first sample = %d, want 500", got)
	}
	// a new class is stored under its normalised key
	r.must(func(tx *sql.Tx) error {
		return r.s.RecordRTF(ctx, tx, "NVIDIA GeForce RTX 4080", 60_000, 100_000, false)
	})
	if got := r.val(`SELECT COUNT(*) FROM rtf_stats WHERE gpu_class = 'RTX 4080'`); got != 1 {
		t.Errorf("normalised key missing")
	}
}
