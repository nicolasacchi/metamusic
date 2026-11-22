package criticbench_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/criticbench"
)

type fakeScalars struct {
	rows  map[string]criticbench.TierBScalar
	asked [][]string
	err   error
}

func (f *fakeScalars) TierBScalars(_ context.Context, ids []string) (map[string]criticbench.TierBScalar, error) {
	f.asked = append(f.asked, ids)
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]criticbench.TierBScalar{}
	for _, id := range ids {
		if r, ok := f.rows[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func fp(f float64) *float64 { return &f }

func TestBench_TierBCriticsFromScalars(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := &fakeScalars{rows: map[string]criticbench.TierBScalar{}}
	e.svc.Use(criticbench.NewTierB(f))
	lines := e.lineages(12, 4) // stars rise with k
	outside := e.take("")      // a take outside every pair
	f.rows[outside] = criticbench.TierBScalar{WER: fp(0.9)}
	for _, l := range lines {
		for k, id := range l {
			// the hypotheses hold: CER falls and melody chroma rises with the stars; WER is never stored
			f.rows[id] = criticbench.TierBScalar{CER: fp(0.4 - 0.1*float64(k)), MelodyChroma: fp(0.2 * float64(k+1))}
		}
	}
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	cer := find(b, "tier_b.cer")
	if cer == nil || cer.Tier != criticbench.TierB || cer.Kind != criticbench.KindMetric || *cer.Direction != -1 || *cer.MinDelta != 0.05 {
		t.Fatalf("cer: %+v", cer)
	}
	if r := cer.Scores.Ratings; r.Pairs != 72 || r.Agree != 72 || r.NEff != 48 || cer.State != criticbench.StateTrusted {
		t.Errorf("cer ratings: %+v state %s", r, cer.State)
	}
	mc := find(b, "tier_b.melody_chroma")
	if mc == nil || *mc.Direction != 1 || *mc.MinDelta != 0.1 || mc.State != criticbench.StateTrusted {
		t.Errorf("melody chroma: %+v", mc)
	}
	// the B1 rule: a critic with no readable value in the windows is absent (the WER row sits on a take outside every pair)
	if find(b, "tier_b.wer") != nil {
		t.Error("tier_b.wer has no value in the reference windows: it must be absent")
	}
	for _, ids := range f.asked {
		for _, id := range ids {
			if id == outside {
				t.Errorf("a take outside the windows was asked for: %s", id)
			}
		}
	}
}

func TestBench_TierBAbsentWithoutRows(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.svc.Use(criticbench.NewTierB(&fakeScalars{}))
	e.lineages(3, 3)
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	for _, id := range []string{"tier_b.cer", "tier_b.wer", "tier_b.melody_chroma"} {
		if find(b, id) != nil {
			t.Errorf("%s listed without Tier B rows", id)
		}
	}
	// and with no pairs at all the source is not even asked
	e2 := newEnv(t)
	f := &fakeScalars{}
	e2.svc.Use(criticbench.NewTierB(f))
	e2.bench(criticbench.Query{IncludeUnknown: true})
	if len(f.asked) != 0 {
		t.Errorf("asked %v with no reference", f.asked)
	}
}

func TestBench_TierBSourceErrorFailsTheRead(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.svc.Use(criticbench.NewTierB(&fakeScalars{err: errors.New("boom")}))
	e.lineages(2, 2)
	if _, err := e.svc.Bench(context.Background(), criticbench.Query{IncludeUnknown: true}); err == nil {
		t.Error("expected the scalar read error")
	}
}

// the planner's tie-break input: only trusted metric critics, with values for any take (not only those in the windows).
func TestTrustedMetrics_OnlyTrustedWithValuesForAnyTake(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := &fakeScalars{rows: map[string]criticbench.TierBScalar{}}
	e.svc.Use(criticbench.NewTierB(f))
	lines := e.lineages(12, 4)
	for _, l := range lines {
		for k, id := range l {
			f.rows[id] = criticbench.TierBScalar{
				CER:          fp(0.4 - 0.1*float64(k)),         // right: trusted
				MelodyChroma: fp(0.2 * float64(4-k)),           // backwards: contrary, never inverted
				WER:          fp(float64((k*7+len(id))%2) / 2), // noise-ish
			}
			e.analysis(id, "energy", "e", fmt.Sprintf(`{"energy":%d}`, k)) // tier A: lower stars lower energy = hypothesis holds
		}
	}
	fresh := e.take("") // not in any pair, but with values
	f.rows[fresh] = criticbench.TierBScalar{CER: fp(0.07)}
	e.analysis(fresh, "energy", "e", `{"energy":9}`)
	got, err := e.svc.TrustedMetrics(context.Background(), []string{fresh, lines[0][0]})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]criticbench.TrustedMetric{}
	for _, m := range got {
		byID[m.ID] = m
	}
	if _, ok := byID["tier_b.melody_chroma"]; ok {
		t.Error("a contrary critic must never be returned (nor inverted)")
	}
	cer, ok := byID["tier_b.cer"]
	if !ok || cer.Direction != -1 {
		t.Fatalf("cer: %+v (%v)", cer, got)
	}
	if d := cmp.Diff(map[string]float64{fresh: 0.07, lines[0][0]: 0.4}, cer.Values); d != "" {
		t.Errorf("cer values (-want +got):\n%s", d)
	}
	en, ok := byID["tier_a.energy"]
	if !ok || en.Direction != 1 || en.Values[fresh] != 9 {
		t.Errorf("tier A trusted energy: %+v", en)
	}
}
