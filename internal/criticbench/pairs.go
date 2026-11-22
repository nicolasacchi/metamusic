package criticbench

import (
	"slices"
	"sort"
	"time"
)

// WindowRatings and WindowDuels are the newest pairs each reference keeps (C385.1).
const (
	WindowRatings = 400
	WindowDuels   = 200
	maxDisagree   = 10
	deltaEps      = 1e-9
)

// Pair is one judged pair of takes. For ratings Winner is the take the Owner rated higher; for duels, the Owner's blind verdict.
type Pair struct {
	A, B, Winner string
	Root         string    // lineage root of the pair (ratings)
	ID           string    // duel id; empty for a rating pair
	At           time.Time // the later rating's time, or the verdict's
}

// Key names the pair in a critic's verdict map and in Disagreements: the duel id, else "a|b".
func (p Pair) Key() string {
	if p.ID != "" {
		return p.ID
	}
	return p.A + "|" + p.B
}

// RatingPairs is the primary reference: every unordered pair of takes with the same lineage root, both rated by the Owner, with
// different stars, newest `window` by the later rating's time (ties by take id, so the result is deterministic).
func RatingPairs(owner map[string]int, roots map[string]string, at map[string]time.Time, window int) []Pair {
	byRoot := map[string][]string{}
	for id := range owner {
		if root := roots[id]; root != "" {
			byRoot[root] = append(byRoot[root], id)
		}
	}
	var out []Pair
	for root, ids := range byRoot {
		slices.Sort(ids)
		for i := range ids {
			for j := i + 1; j < len(ids); j++ {
				a, b := ids[i], ids[j]
				if owner[a] == owner[b] {
					continue
				}
				w := a
				if owner[b] > owner[a] {
					w = b
				}
				out = append(out, Pair{A: a, B: b, Winner: w, Root: root, At: laterOf(at[a], at[b])})
			}
		}
	}
	return newest(out, window)
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// newest sorts newest first (ties by key) and keeps at most n.
func newest(p []Pair, n int) []Pair {
	sort.Slice(p, func(i, j int) bool {
		if !p[i].At.Equal(p[j].At) {
			return p[i].At.After(p[j].At)
		}
		return p[i].Key() < p[j].Key()
	})
	if n < 0 {
		n = 0
	}
	if len(p) > n {
		p = p[:n]
	}
	return p
}

// Newest exposes the window rule to the services that build the duel reference.
func Newest(p []Pair, n int) []Pair { return newest(p, n) }

func distinctTakes(p []Pair) int {
	seen := map[string]struct{}{}
	for _, x := range p {
		seen[x.A] = struct{}{}
		seen[x.B] = struct{}{}
	}
	return len(seen)
}

// tally accumulates a critic's calls over a window.
type tally struct {
	window, judged, predicted, agree int
	called                           []Pair // pairs with a prediction
	wrong                            []Pair
}

func (t *tally) score() Score {
	cov := 0.0
	if t.window > 0 {
		cov = float64(t.judged) / float64(t.window)
	}
	s := newScore(t.judged, min(t.predicted, distinctTakes(t.called)), t.agree, t.predicted, cov)
	for _, p := range newest(t.wrong, maxDisagree) {
		s.Disagreements = append(s.Disagreements, p.Key())
	}
	return s
}

func (t *tally) call(p Pair, winner string) {
	t.predicted++
	t.called = append(t.called, p)
	if winner == p.Winner {
		t.agree++
	} else {
		t.wrong = append(t.wrong, p)
	}
}

// Metric scores a metric critic. It predicts A over B when direction x (m_A - m_B) >= minDelta (and B over A symmetrically); a
// closer pair, or one where the critic lacks a value, gets no prediction. Rate = Agree / Predicted.
func Metric(direction int, minDelta float64, v map[string]float64, pairs []Pair) Score {
	t := tally{window: len(pairs)}
	for _, p := range pairs {
		a, okA := v[p.A]
		b, okB := v[p.B]
		if !okA || !okB {
			continue
		}
		t.judged++
		d := float64(direction) * (a - b)
		switch {
		case d > 0 && d >= minDelta-deltaEps:
			t.call(p, p.A)
		case d < 0 && -d >= minDelta-deltaEps:
			t.call(p, p.B)
		}
	}
	return t.score()
}

// Pairwise scores a critic that names a winner per pair. verdicts maps Pair.Key() to the winning take id; a key with any other
// value (a tie, "both bad") is judged but not predicted, a missing key is not judged.
func Pairwise(pairs []Pair, verdicts map[string]string) Score {
	t := tally{window: len(pairs)}
	for _, p := range pairs {
		w, ok := verdicts[p.Key()]
		if !ok {
			continue
		}
		t.judged++
		if w == p.A || w == p.B {
			t.call(p, w)
		}
	}
	return t.score()
}

// SlotBias is the share of a critic's a/b verdicts that named slot A, with its Wilson interval; biased iff there are at least 20
// such verdicts and 0.5 lies outside the interval.
func SlotBias(aCount, abCount int) (share, lo, hi float64, biased bool) {
	lo, hi = Wilson(aCount, abCount)
	if abCount <= 0 {
		return 0, lo, hi, false
	}
	return float64(aCount) / float64(abCount), lo, hi, abCount >= 20 && (0.5 < lo || 0.5 > hi)
}

// Labels scores a classifier of lines against the Owner's marks, both keyed by line. A line the classifier did not call is not
// counted; NEff is the number of lines both have.
func Labels(pred, human map[string]bool) Score {
	keys := make([]string, 0, len(human))
	for k := range human {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	pairs, agree := 0, 0
	var wrong []string
	for _, k := range keys {
		p, ok := pred[k]
		if !ok {
			continue
		}
		pairs++
		if p == human[k] {
			agree++
		} else {
			wrong = append(wrong, k)
		}
	}
	cov := 0.0
	if len(human) > 0 {
		cov = float64(pairs) / float64(len(human))
	}
	s := newScore(pairs, pairs, agree, pairs, cov)
	if len(wrong) > maxDisagree {
		wrong = wrong[:maxDisagree]
	}
	s.Disagreements = wrong
	return s
}
