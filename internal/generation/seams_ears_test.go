package generation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

func dictionWarnings(e generation.Estimate) (out []generation.Warning) {
	for _, w := range e.Warnings {
		if w.Code == "diction_risk" {
			out = append(out, w)
		}
	}
	return out
}

func TestEstimate_DictionRiskWarning(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	luna := lt.TrackLuna
	item := generation.Item{Mode: lineage.ModeSameSeedEdit, ParentTrackID: &luna}
	if got := dictionWarnings(r.est(agent, item)); len(got) != 0 {
		t.Fatalf("no seam wired: %+v", got)
	}
	var asked []string
	r.svc.UseEars(generation.EarsSeams{DictionRisk: func(_ context.Context, lyrics string) ([]generation.Issue, error) {
		asked = append(asked, lyrics)
		return []generation.Issue{{Code: "diction_risk", Field: "lyrics", Severity: "warning", Message: "'chiave' was heard as 'kiave' in 4/6 takes"}}, nil
	}})
	got := dictionWarnings(r.est(agent, item))
	if len(got) != 1 || got[0].TakeIndex != 0 || got[0].Severity != "warning" || !strings.Contains(got[0].Text, "'chiave' was heard as 'kiave'") {
		t.Fatalf("warnings = %+v", got)
	}
	if len(asked) != 1 || asked[0] == "" {
		t.Errorf("the seam was asked %q, want the take's lyrics once", asked)
	}
	// a failing lexicon leaves the warning out and the estimate stands
	r.svc.UseEars(generation.EarsSeams{DictionRisk: func(context.Context, string) ([]generation.Issue, error) { return nil, errors.New("boom") }})
	if got := dictionWarnings(r.est(agent, item)); len(got) != 0 {
		t.Errorf("failing seam: %+v", got)
	}
}
