package ears

import (
	"context"
	"net/http"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

const inferredSibling = "inferred_sibling"

// Compare builds the compare_tracks payload (C51.6).
func (s *Service) Compare(ctx context.Context, a, b string) (Comparison, error) {
	if a == b {
		return Comparison{}, problem.E(http.StatusUnprocessableEntity, "COMPARE_SAME_TRACK", "a and b are the same track", map[string]any{"track_id": a})
	}
	sa, err := s.load(ctx, a)
	if err != nil {
		return Comparison{}, err
	}
	sb, err := s.load(ctx, b)
	if err != nil {
		return Comparison{}, err
	}
	refs, err := s.o.Library.TrackRefs(ctx, []string{a, b})
	if err != nil {
		return Comparison{}, err
	}
	ra, rb := refs[a], refs[b]
	c := Comparison{
		A: side(sa), B: side(sb), Relation: relation(ra, rb),
		Deltas: deltas(sa, sb), Flags: flagDiff(sa.flagCodes(), sb.flagCodes()), RecipeDiff: recipeDiff(sa, sb),
		BlindSpots: compareBlindSpots(sa, sb),
	}
	if s.o.Judgments != nil {
		if c.Duels, err = s.o.Judgments.PairStats(ctx, a, b); err != nil {
			return Comparison{}, err
		}
		str, nu, err := s.o.Judgments.Strengths(ctx, []string{a, b})
		if err != nil {
			return Comparison{}, err
		}
		c.Ladder.Nu = nu
		sta, oka := str[a]
		stb, okb := str[b]
		if oka {
			v := StrengthOrNull(sta)
			c.Ladder.A = &v
		}
		if okb {
			v := StrengthOrNull(stb)
			c.Ladder.B = &v
		}
		if oka && okb && sta.NDuels > 0 && stb.NDuels > 0 {
			win, tie, _ := ladder.PBeats(sta.U, stb.U, nu)
			c.Ladder.PABeatsB, c.Ladder.PTie = &win, &tie
		}
	}
	return c, nil
}

func side(st *state) Side {
	return Side{TrackID: st.d.ID, Title: st.d.Title, Seed: st.d.Recipe.Seed, RequestID: st.d.Recipe.RequestID,
		RootTrackID: st.d.Lineage.RootTrackID, DurationS: st.d.DurationS, Tier: st.tier, Flags: st.flagCodes(),
		RecipeKnown: st.d.RecipeKnown, Lossy: st.d.Lossy}
}

// relation is D-M2b-20: how the two takes are related in the Operator Ledger (first match wins).
func relation(a, b library.TrackRef) string {
	switch {
	case b.ParentTrackID == a.ID:
		if b.ParentOperator == inferredSibling {
			return "siblings"
		}
		return "parent_child"
	case a.ParentTrackID == b.ID:
		if a.ParentOperator == inferredSibling {
			return "siblings"
		}
		return "child_parent"
	case a.ParentTrackID != "" && a.ParentTrackID == b.ParentTrackID && a.ParentOperator == inferredSibling && b.ParentOperator == inferredSibling:
		return "siblings"
	case a.RootTrackID == b.RootTrackID:
		return "same_lineage"
	}
	return "unrelated"
}

// delta is b - a rounded to two decimals; nil when either side lacks the metric.
func delta(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	v := r2(*b - *a)
	return &v
}

func ptr[T any](v T) *T { return &v }

func (st *state) metrics() (lufs, tp, lra, bw, clipped, internal, loopSpan *float64) {
	if l := st.loudness; l != nil {
		lufs, tp, lra = ptr(l.IntegratedLUFS), ptr(l.TruePeakDBTP), ptr(l.LRA)
	}
	if sp := st.spectrum; sp != nil && sp.BandwidthHz != nil {
		bw = ptr(float64(*sp.BandwidthHz))
	}
	if c := st.clipping; c != nil {
		clipped = ptr(c.ClippedS)
	}
	if sl := st.silence; sl != nil {
		internal = ptr(sl.InternalS)
	}
	if t := st.tokens; t != nil {
		m := 0.0
		for _, s := range t.Streams {
			m = max(m, s.PeriodicRun.SpanS)
		}
		loopSpan = &m
	}
	return
}

func deltas(a, b *state) Deltas {
	al, at, ar, ab, ac, ai, ak := a.metrics()
	bl, bt, br, bb, bc, bi, bk := b.metrics()
	dur := func(s *state) *float64 { return ptr(s.d.DurationS) }
	return Deltas{IntegratedLUFS: delta(al, bl), TruePeakDBTP: delta(at, bt), LRALU: delta(ar, br), BandwidthHz: delta(ab, bb),
		DurationS: delta(dur(a), dur(b)), ClippedS: delta(ac, bc), InternalSilenceS: delta(ai, bi), TokenLoopSpanS: delta(ak, bk)}
}

func flagDiff(a, b []string) FlagDiff {
	d := FlagDiff{OnlyA: []string{}, OnlyB: []string{}, Both: []string{}}
	for _, c := range a {
		if slices.Contains(b, c) {
			d.Both = append(d.Both, c)
		} else {
			d.OnlyA = append(d.OnlyA, c)
		}
	}
	for _, c := range b {
		if !slices.Contains(a, c) {
			d.OnlyB = append(d.OnlyB, c)
		}
	}
	return d
}

func recipeDiff(a, b *state) RecipeDiff {
	ra, rb := a.d.Recipe, b.d.Recipe
	d := RecipeDiff{SameHash: ra.Hash == rb.Hash, Changed: []string{}, Seed: SeedPair{A: ra.Seed, B: rb.Seed},
		StyleTerms: StyleTermDiff{Added: []string{}, Removed: []string{}}}
	for _, c := range []struct {
		name string
		diff bool
	}{{"seed", ra.Seed != rb.Seed}, {"style", ra.Style != rb.Style}, {"lyrics", ra.Lyrics != rb.Lyrics}, {"cot", ra.Cot != rb.Cot}} {
		if c.diff {
			d.Changed = append(d.Changed, c.name)
		}
	}
	for _, t := range b.d.StyleTerms {
		if !slices.Contains(a.d.StyleTerms, t) {
			d.StyleTerms.Added = append(d.StyleTerms.Added, t)
		}
	}
	for _, t := range a.d.StyleTerms {
		if !slices.Contains(b.d.StyleTerms, t) {
			d.StyleTerms.Removed = append(d.StyleTerms.Removed, t)
		}
	}
	return d
}

// compareBlindSpots carries what holds for both takes: Tier A only when both have it, a semantic.npy
// when either has one, section times only when both are timed (the score-plan side decides the source).
func compareBlindSpots(a, b *state) []string {
	f := analysis.BlindSpotFacts{TierA: a.tier == "A" && b.tier == "A", HasSemantic: a.hasSemantic || b.hasSemantic,
		Timed: a.d.Timed && b.d.Timed,
		Lossy: a.d.Lossy || b.d.Lossy}
	if f.Timed {
		src, conf := a.sectionSource()
		if bs, bc := b.sectionSource(); bs == "plan" {
			src, conf = bs, bc
		}
		f.SectionSource, f.Confidence = src, conf
	}
	return analysis.BlindSpots(f)
}
