package ears_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/diction"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func TestTierBScalars(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putScore(lt.TrackLuna)
	r.putDoc(lt.TrackLuna, "analysis.json", singDoc(t, 0), time.Hour)
	r.analyze(lt.TrackLuna)
	ctx := context.Background()
	got, err := r.svc.TierBScalars(ctx, []string{lt.TrackLuna, lt.TrackCitta})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("scalars = %+v, want only the analysed take", got)
	}
	sc := got[lt.TrackLuna]
	if sc.CER == nil || *sc.CER != 0 || sc.WER == nil || *sc.WER != 0 || sc.MelodyChroma == nil || *sc.MelodyChroma != 1 {
		t.Errorf("luna = %+v", sc)
	}
	if m, err := r.svc.TierBScalars(ctx, nil); err != nil || len(m) != 0 {
		t.Errorf("no ids: %+v, %v", m, err)
	}
	// a row of a superseded definition is not current
	r.dropTriggers("analyses_immutable")
	r.exec(`UPDATE analyses SET version = 'mm-diction/0 old' WHERE analyzer = 'tier_b_diction'`)
	got, _ = r.svc.TierBScalars(ctx, []string{lt.TrackLuna})
	if sc := got[lt.TrackLuna]; sc.CER != nil || sc.MelodyChroma == nil {
		t.Errorf("after superseding the diction row: %+v", sc)
	}
}

// TestTierBScalars_NullChroma: a take without a plan score has no melody comparison, which stays nil.
func TestTierBScalars_NullChroma(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
	r.analyze(lt.TrackLuna)
	got, err := r.svc.TierBScalars(context.Background(), []string{lt.TrackLuna})
	if err != nil || got[lt.TrackLuna].CER == nil || got[lt.TrackLuna].MelodyChroma != nil {
		t.Errorf("scalars = %+v, %v", got, err)
	}
}

type fakeSummary struct {
	sum diction.ContextSummary
	err error
}

func (f fakeSummary) ContextSummary(context.Context, string) (diction.ContextSummary, error) {
	return f.sum, f.err
}

func TestPerception_DictionLinesAndRiskyWords(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
	r.analyze(lt.TrackLuna)
	ctx := context.Background()
	if p, _ := r.svc.Perception(ctx, lt.TrackLuna); p.Diction == nil || p.Diction.Lines != nil || p.Diction.RiskyWords != nil {
		t.Fatalf("nothing wired: %+v", p.Diction)
	}
	r.svc.UseDiction(fakeSummary{sum: diction.ContextSummary{HasLines: true, Clear: 5, Mixed: 2, Unclear: 1, RiskyWords: []string{"chiave"}}})
	p, err := r.svc.Perception(ctx, lt.TrackLuna)
	if err != nil || p.Diction.Lines == nil || *p.Diction.Lines != (ears.PerceptionDictionLines{Clear: 5, Mixed: 2, Unclear: 1}) ||
		len(p.Diction.RiskyWords) != 1 || p.Diction.RiskyWords[0] != "chiave" {
		t.Errorf("perception diction = %+v, %v", p.Diction, err)
	}
	// a take without a listening check has no diction block at all, whatever the lab says
	if p, _ := r.svc.Perception(ctx, lt.TrackCitta); p.Diction != nil {
		t.Errorf("no listening check: %+v", p.Diction)
	}
	// a lab that cannot answer leaves the CER block as it was
	r.svc.UseDiction(fakeSummary{err: errors.New("boom")})
	if p, err := r.svc.Perception(ctx, lt.TrackLuna); err != nil || p.Diction == nil || p.Diction.Lines != nil {
		t.Errorf("failing lab: %+v, %v", p.Diction, err)
	}
}
