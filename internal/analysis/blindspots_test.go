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
		{"tempo, key and energy", analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Timed: true, SectionSource: "measured", Tempo: true, Key: true, Energy: true}, []string{
			"tempo is an estimate that assumes a steady beat: tempo changes, rubato, free-time intros and half/double-time ambiguity are not judged",
			"key is an estimate from pitch-class statistics: modulations, modal or non-Western music, atonal or percussion-dominated passages and a drone that is not the tonic are not judged",
			"energy mixes onset rate, brightness and loudness: a quiet but dense mix and a loud sparse one are conflated by mastering, genre bias (ambient music mastered loud reads high), and it is not perceptual arousal",
		}},
		{"key only, with a spectrogram", analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Timed: true, SectionSource: "measured", Key: true, Spectrogram: true}, []string{
			"key is an estimate from pitch-class statistics: modulations, modal or non-Western music, atonal or percussion-dominated passages and a drone that is not the tonic are not judged",
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

// TestBlindSpots_UnchangedWithoutTierB: the two Tier B facts default to false and change nothing, whatever the other facts are.
func TestBlindSpots_UnchangedWithoutTierB(t *testing.T) {
	t.Parallel()
	conf := 0.7
	for _, f := range []analysis.BlindSpotFacts{
		{}, {TierA: true}, {TierA: true, HasSemantic: true, Timed: true, SectionSource: "plan", Confidence: &conf},
		{TierA: true, Lossy: true, Tempo: true, Key: true, Energy: true, Spectrogram: true},
	} {
		got := analysis.BlindSpots(f)
		if got[4] != "diction and intelligibility" || got[5] != "melody and harmony adherence to the score" {
			t.Errorf("%+v: fixed entries moved: %q", f, got[:6])
		}
	}
}

// TestBlindSpots_TierBWording: with Tier B facts the two fixed entries are reworded in place, nothing else moves.
func TestBlindSpots_TierBWording(t *testing.T) {
	t.Parallel()
	base := analysis.BlindSpots(analysis.BlindSpotFacts{TierA: true, HasSemantic: true})
	for _, c := range []struct {
		name         string
		facts        analysis.BlindSpotFacts
		diction, adh string
	}{
		{"diction", analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Diction: true},
			"diction measured by speech recognition on the separated vocals (CER per section); the recogniser can mishear sung words",
			"melody and harmony adherence to the score"},
		{"adherence", analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Adherence: true},
			"diction and intelligibility",
			"melody adherence measured coarsely (pitch classes per section, from the separated vocals), not note by note; harmony is not judged"},
	} {
		got := analysis.BlindSpots(c.facts)
		if got[4] != c.diction || got[5] != c.adh {
			t.Errorf("%s: entries = %q, %q", c.name, got[4], got[5])
		}
		if diff := cmp.Diff(base[:4], got[:4]); diff != "" {
			t.Errorf("%s: first four moved (-want +got):\n%s", c.name, diff)
		}
		if diff := cmp.Diff(base[6:], got[6:]); diff != "" {
			t.Errorf("%s: conditional entries moved (-want +got):\n%s", c.name, diff)
		}
	}
}
