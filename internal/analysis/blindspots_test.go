package analysis_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

func TestBlindSpots_Table(t *testing.T) {
	t.Parallel()
	fixed := []string{
		"timbre", "emotional delivery", "mix balance beyond loudness", "hook catchiness",
		"diction and intelligibility", "melody and harmony adherence to the score",
	}
	conf := 0.7
	tests := []struct {
		name  string
		facts analysis.BlindSpotFacts
		extra []string
	}{
		{"nothing run, untimed", analysis.BlindSpotFacts{}, []string{
			"silence, clipping and token loops (Tier A not run yet)",
			"section times unknown (no aligned score)",
		}},
		{"tier A, no semantic, untimed", analysis.BlindSpotFacts{TierA: true}, []string{
			"token loops (no semantic.npy for this take)",
			"section times unknown (no aligned score)",
		}},
		{"tier A with semantic, untimed", analysis.BlindSpotFacts{TierA: true, HasSemantic: true}, []string{
			"section times unknown (no aligned score)",
		}},
		{"timed from plan", analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Timed: true, SectionSource: "plan", Confidence: &conf}, []string{
			"section times are score-plan estimates (confidence 0.7)",
		}},
		{"timed from plan, no confidence", analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Timed: true, SectionSource: "plan"}, []string{
			"section times are score-plan estimates",
		}},
		{"timed from measured", analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Timed: true, SectionSource: "measured"}, nil},
		{"timed from manual", analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Timed: true, SectionSource: "manual"}, nil},
		{"spectrogram", analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Timed: true, SectionSource: "measured", Spectrogram: true}, []string{
			"a spectrogram shows energy over time and frequency, not pitch accuracy or words",
		}},
		{"everything open", analysis.BlindSpotFacts{Timed: true, SectionSource: "plan", Confidence: &conf, Spectrogram: true}, []string{
			"silence, clipping and token loops (Tier A not run yet)",
			"section times are score-plan estimates (confidence 0.7)",
			"a spectrogram shows energy over time and frequency, not pitch accuracy or words",
		}},
		{"no semantic and untimed and spectrogram", analysis.BlindSpotFacts{TierA: true, Spectrogram: true}, []string{
			"token loops (no semantic.npy for this take)",
			"section times unknown (no aligned score)",
			"a spectrogram shows energy over time and frequency, not pitch accuracy or words",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := append(append([]string{}, fixed...), tc.extra...)
			if diff := cmp.Diff(want, analysis.BlindSpots(tc.facts)); diff != "" {
				t.Errorf("blind spots (-want +got):\n%s", diff)
			}
		})
	}
}

// The first five entries are M2a C35's literal list (D-M2b-21).
func TestBlindSpots_FirstFiveAreM2aWording(t *testing.T) {
	t.Parallel()
	got := analysis.BlindSpots(analysis.BlindSpotFacts{})
	want := []string{"timbre", "emotional delivery", "mix balance beyond loudness", "hook catchiness", "diction and intelligibility"}
	if diff := cmp.Diff(want, got[:5]); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}
