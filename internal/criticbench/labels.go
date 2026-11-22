package criticbench

import (
	"context"
	"fmt"
	"math"

	"github.com/nicolasacchi/metamusic/internal/diction"
)

// WindowLines is how many of the Owner's newest marked lines the label critic is scored on (C386.4).
const WindowLines = 400

// LabelCriticID is the id of the Diction Lab's label critic.
const LabelCriticID = "diction.line_cer"

// sweepSteps is the threshold sweep: mixed = 0.15, 0.20, ... 0.50 (C386.4).
const sweepSteps = 8

// SweepMixed returns the i-th point of the sweep (0.15 + 0.05 i), rounded so the figure is exact in JSON.
func SweepMixed(i int) float64 { return math.Round((0.15+0.05*float64(i))*100) / 100 }

// LineKey names a marked line in a score's disagreement list.
func LineKey(k diction.LabelKey) string { return fmt.Sprintf("%s:%d:%d", k.TrackID, k.Section, k.Line) }

// predictUnclear is the label critic: a line is unclear iff its CER exceeds mixed.
func predictUnclear(cer, mixed float64) bool { return cer > mixed }

// ScoreLines is the agreement of "unclear iff CER > mixed" with the Owner's marks (pure; every line is called).
func ScoreLines(lines []diction.LabelledLine, mixed float64) Score {
	pred, human := map[string]bool{}, map[string]bool{}
	for _, l := range lines {
		k := LineKey(l.Key)
		pred[k] = predictUnclear(l.CER, mixed)
		human[k] = l.Human == diction.ClassUnclear
	}
	return Labels(pred, human)
}

// Sweep reports the agreement at every mixed threshold of the sweep so the calibration can pick from the Owner's own marks.
func Sweep(lines []diction.LabelledLine) []Threshold {
	out := make([]Threshold, 0, sweepSteps)
	for i := 0; i < sweepSteps; i++ {
		m := SweepMixed(i)
		agree := 0
		for _, l := range lines {
			if predictUnclear(l.CER, m) == (l.Human == diction.ClassUnclear) {
				agree++
			}
		}
		t := Threshold{Mixed: m, N: len(lines), Agree: agree}
		if len(lines) > 0 {
			t.Rate = round4(float64(agree) / float64(len(lines)))
		}
		lo, hi := Wilson(agree, len(lines))
		t.CI95 = [2]float64{round4(lo), round4(hi)}
		out = append(out, t)
	}
	return out
}

// labels is the Source of the label critic diction.line_cer. It reads the Owner's marks through diction.LabelSource (critic
// marks never appear there) and never recomputes a CER.
type labels struct{ src diction.LabelSource }

// NewLabels builds the label-critic source. A nil source yields no critic (use Service.Use only with a wired Diction Lab).
func NewLabels(src diction.LabelSource) Source { return labels{src: src} }

func (l labels) lines(ctx context.Context) ([]diction.LabelledLine, error) {
	lines, err := l.src.HumanLabelledLines(ctx, "", WindowLines)
	if err != nil {
		return nil, fmt.Errorf("criticbench: labelled lines: %w", err)
	}
	return lines, nil
}

// Critics lists the label critic only when the Owner has marked at least one line that has a CER.
func (l labels) Critics(ctx context.Context, _ Reader, _ *Reference) ([]Critic, error) {
	if l.src == nil {
		return nil, nil
	}
	lines, err := l.lines(ctx)
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	mixed := l.src.Thresholds().Mixed
	sc := ScoreLines(lines, mixed)
	c := Critic{ID: LabelCriticID, Label: "Line clarity from CER", Kind: KindLabels, Tier: TierDiction,
		Provenance: fmt.Sprintf("diction: a line is unclear when its CER exceeds %.2f (the threshold in force), against the Owner's clear/unclear marks", mixed),
		Scores:     Scores{Lines: &sc}, Thresholds: Sweep(lines)}
	c.settle()
	return []Critic{c}, nil
}

// Version joins the cache key: marks and diction rows live outside the tables the key covers.
func (l labels) Version(ctx context.Context) (string, error) {
	if l.src == nil {
		return "", nil
	}
	return l.src.Version(ctx)
}

// Lines reports window.lines: the marked lines scored.
func (l labels) Lines(ctx context.Context) (int, error) {
	if l.src == nil {
		return 0, nil
	}
	lines, err := l.lines(ctx)
	return len(lines), err
}
