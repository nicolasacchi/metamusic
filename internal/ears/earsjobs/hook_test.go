package earsjobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earsjobs"
	"github.com/nicolasacchi/metamusic/internal/jobs"
)

type spy struct {
	calls []string
	err   error
}

func (s *spy) AnalyzeTierB(_ context.Context, id string) (ears.TierBOutcome, error) {
	s.calls = append(s.calls, id)
	return ears.TierBOutcome{TrackID: id}, s.err
}

// TestTierBHook_RendersWithEarsOnly: only a take whose job needed the ears capability is analysed; AfterTakeTx never fails the
// take; an analysis failure is logged and swallowed (the sweep retries).
func TestTierBHook_RendersWithEarsOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		needs []string
		kind  string
		err   error
		want  int
	}{
		{"plain render", nil, "generate", nil, 0},
		{"v2 render", []string{"v2"}, "generate", nil, 0},
		{"render with ears", []string{"ears", "v2"}, "generate", nil, 1},
		{"ears needed, no document", []string{"ears", "v2"}, "generate", ears.ErrNoTierBDocument, 1},
		{"analysis failed", []string{"ears", "v2"}, "generate", errors.New("boom"), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := &spy{err: c.err}
			h := earsjobs.TierBHook{Ears: s}
			ti := jobs.TakeInfo{TrackID: "t1", JobID: "j1", Kind: c.kind, Needs: c.needs}
			if err := h.AfterTakeTx(ctx, nil, ti); err != nil {
				t.Fatalf("AfterTakeTx: %v", err)
			}
			if len(s.calls) != 0 {
				t.Fatal("AfterTakeTx analysed inside the take transaction")
			}
			h.AfterTakeCommit(ctx, ti)
			if len(s.calls) != c.want || (c.want == 1 && s.calls[0] != "t1") {
				t.Errorf("analyses = %v, want %d", s.calls, c.want)
			}
		})
	}
	earsjobs.TierBHook{}.AfterTakeCommit(ctx, jobs.TakeInfo{Needs: []string{"ears"}}) // no service: nothing happens
}
