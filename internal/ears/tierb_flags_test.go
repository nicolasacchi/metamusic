package ears

import (
	"slices"
	"testing"
)

func dict(measured string, secs ...storedSection) *tierBDictionDoc {
	return &tierBDictionDoc{Sections: secs, Measured: tierBMeasuredNote{Status: measured}, Overall: TierBDictionOverall{RefWords: 20}}
}

func sec(label string, words int, cer float64) storedSection {
	return storedSection{TierBSection: TierBSection{Label: label, RefWords: words, CER: cer}}
}

func adh(chroma float64, mode int, transposes ...int) *TierBAdherence {
	a := &TierBAdherence{Basis: "coarse", MelodyChroma: &chroma, TransposeMode: &mode}
	for i, tr := range transposes {
		a.Sections = append(a.Sections, TierBAdherenceSection{Idx: i, Transpose: &tr})
	}
	return a
}

// TestFlags_TierB_Table: the ear checks of C338.2, in table order, appended after the Tier A rules.
func TestFlags_TierB_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   *tierBFlagInput
		want []string
	}{
		{"no listening check", nil, nil},
		{"clean", &tierBFlagInput{State: TierBOK, Words: 20, RefWords: 20, Diction: dict(measuredWritten, sec("verse", 8, 0.1))}, nil},
		{"poor section", &tierBFlagInput{State: TierBOK, Words: 20, RefWords: 20, Diction: dict(measuredWritten, sec("verse", 8, 0.51))}, []string{"diction_poor"}},
		{"cer exactly 0.5 is not poor", &tierBFlagInput{State: TierBOK, Words: 9, RefWords: 9, Diction: dict(measuredWritten, sec("verse", 8, 0.5))}, nil},
		{"short section is not judged", &tierBFlagInput{State: TierBOK, Words: 9, RefWords: 9, Diction: dict(measuredWritten, sec("chorus", 4, 0.9))}, nil},
		{"alignment failed", &tierBFlagInput{State: TierBOK, Words: 9, RefWords: 9, Diction: dict(measuredFailed, sec("verse", 5, 0.2))}, []string{"alignment_failed"}},
		{"alignment kept is quiet", &tierBFlagInput{State: TierBOK, Words: 9, RefWords: 9, Diction: dict(measuredKept)}, nil},
		{"no vocals", &tierBFlagInput{State: TierBOK, Words: 2, RefWords: 10, Diction: dict(measuredFailed)}, []string{"alignment_failed", "vocals_not_detected"}},
		{"three words are vocals", &tierBFlagInput{State: TierBOK, Words: 3, RefWords: 10}, nil},
		{"short lyrics, few words", &tierBFlagInput{State: TierBOK, Words: 0, RefWords: 9}, nil},
		{"error", &tierBFlagInput{State: TierBError, Code: "OOM"}, []string{"listening_check_failed"}},
		{"skipped", &tierBFlagInput{State: TierBSkipped}, []string{"listening_check_failed"}},
		{"missing", &tierBFlagInput{State: TierBMissing, RefWords: 50}, []string{"listening_check_failed"}},
		{"all four in order", &tierBFlagInput{State: TierBOK, Words: 1, RefWords: 30, Diction: dict(measuredFailed, sec("verse", 9, 0.8)), Adherence: adh(0.2, 5, 5, 5, 5)},
			[]string{"diction_poor", "alignment_failed", "vocals_not_detected", "melody_off_plan", "sung_transposed"}},
		{"melody off plan needs three sections", &tierBFlagInput{State: TierBOK, Words: 9, RefWords: 9, Adherence: adh(0.3, 0, 0, 0)}, nil},
		{"melody off plan", &tierBFlagInput{State: TierBOK, Words: 9, RefWords: 9, Adherence: adh(0.3, 0, 0, 0, 0)}, []string{"melody_off_plan"}},
		{"chroma 0.5 is on plan", &tierBFlagInput{State: TierBOK, Words: 9, RefWords: 9, Adherence: adh(0.5, 0, 0, 0, 0)}, nil},
		{"transposed in half the sections", &tierBFlagInput{State: TierBOK, Words: 9, RefWords: 9, Adherence: adh(0.9, 7, 7, 0)}, []string{"sung_transposed"}},
		{"transposed in fewer than half", &tierBFlagInput{State: TierBOK, Words: 9, RefWords: 9, Adherence: adh(0.9, 7, 7, 0, 0)}, nil},
		{"null adherence is quiet", &tierBFlagInput{State: TierBOK, Words: 9, RefWords: 9, Adherence: &TierBAdherence{Basis: "coarse"}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := codes(tierBFlags(c.in))
			if len(c.want) == 0 && len(got) == 0 {
				return
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("flags = %v, want %v", got, c.want)
			}
		})
	}
}

// TestFlags_TierB_AppendedAfterTierA: computeFlags keeps the Tier A order and appends the ear checks last.
func TestFlags_TierB_AppendedAfterTierA(t *testing.T) {
	t.Parallel()
	got := codes(computeFlags(flagInput{TruncatedABC: ptr(true), TierB: &tierBFlagInput{State: TierBError}}))
	if want := []string{"truncated", "listening_check_failed"}; !slices.Equal(got, want) {
		t.Errorf("flags = %v, want %v", got, want)
	}
}

// TestFlags_TierB_Evidence: the evidence names the worst poor section.
func TestFlags_TierB_Evidence(t *testing.T) {
	t.Parallel()
	fs := tierBFlags(&tierBFlagInput{State: TierBOK, Words: 20, RefWords: 20, Diction: dict(measuredWritten, sec("verse", 8, 0.6), sec("chorus", 8, 0.9))})
	if len(fs) != 1 || fs[0].Severity != "warn" || fs[0].Evidence["section"] != "chorus" || fs[0].Evidence["cer"] != 0.9 {
		t.Errorf("flags = %+v", fs)
	}
}
