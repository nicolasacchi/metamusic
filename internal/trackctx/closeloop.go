package trackctx

// The close-the-loop block of /context (C254, lane M5-A1). Everything here is additive and self-contained so a rebase
// onto neighbouring /context work stays trivial: one reader interface, one wiring method, one assembly function and the
// trim helpers. Without a reader, or without ancestors, in-flight patches and hotspots, no byte of the bundle changes
// (TestContext_Goldens_PreM5Unchanged).

import (
	"context"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/anchor"
	"github.com/nicolasacchi/metamusic/internal/attention"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/judgments"
)

// CloseLoopReader is what the bundle needs of the attention side: the timed sections of a take (to map a comment's spot
// from an ancestor onto this take), its listening hotspots and the Song PRs still rendering for some comments.
// *attention.Service implements it; trackctx declares it so the wiring stays in app.
type CloseLoopReader interface {
	Timing(ctx context.Context, trackID string) (anchor.Timing, error)
	Hotspots(ctx context.Context, trackID string) ([]attention.Hotspot, error)
	InFlightPatches(ctx context.Context, commentIDs []string) (map[string][]string, error)
}

// UseCloseLoop wires the reader. A nil reader (the default) leaves the bundle exactly as it was before M5.
func (b *Builder) UseCloseLoop(r CloseLoopReader) { b.cl = r }

// Limits of the close-the-loop fields.
const (
	maxLyricLines    = 12  // lines of the mapped lyrics section shown on a comment
	maxLyricLineLen  = 160 // runes per line
	maxSpotHotspots  = 3
	spotSecondsScale = 1000 // spots are rounded to milliseconds
)

// closeLoop is what the assembly learned about one take's loop state.
type closeLoop struct {
	ancestorOpenFix int
	active          bool // per-comment fields and ancestor_open_fix are shown
}

// ancestorComments lists the non-hidden open fix and note comments of the take's ancestors (nearest first), newest first,
// at most room of them, and counts every open fix comment on the ancestors.
func (b *Builder) ancestorComments(ctx context.Context, ancestors []string, room int) (list []judgments.Comment, openFix int, err error) {
	var all []judgments.Comment
	for _, id := range ancestors {
		fixes, total, err := b.collect(ctx, id, judgments.CommentFilter{State: []string{"open"}, Kind: []string{"fix"}}, includeComments, nil)
		if err != nil {
			return nil, 0, err
		}
		if len(fixes) > 0 {
			openFix += total
		}
		notes, _, err := b.collect(ctx, id, judgments.CommentFilter{State: []string{"open"}, Kind: []string{"note"}}, includeComments, nil)
		if err != nil {
			return nil, 0, err
		}
		all = append(append(all, fixes...), notes...)
	}
	slices.SortStableFunc(all, func(x, y judgments.Comment) int {
		if c := strings.Compare(y.CreatedAt, x.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(y.ID, x.ID)
	})
	if room < 0 {
		room = 0
	}
	if len(all) > room {
		all = all[:room]
	}
	return all, openFix, nil
}

// roundTo rounds x to the given number of decimals.
func roundTo(x float64, decimals int) float64 {
	p := math.Pow10(decimals)
	return math.Round(x*p) / p
}

// spotOf maps the time of a comment onto this take. An own comment keeps its time; an ancestor's is anchored in its own
// take and mapped (C252). It also returns the lyrics section the spot lands in (kind and ordinal, "" when none).
func spotOf(c judgments.Comment, own bool, src, tgt anchor.Timing) (spot *omap, kind string, ordinal int) {
	if c.AtSecond == nil {
		return nil, "", 0
	}
	p := anchor.Compute(src, *c.AtSecond)
	at, basis, conf := *c.AtSecond, p.Basis, p.Confidence
	if !own {
		at, basis, conf = anchor.Map(p, tgt)
	}
	var label any
	if q := anchor.Compute(tgt, at); q.Basis == anchor.BasisSection {
		label, kind, ordinal = q.Label, q.Kind, q.Ordinal
	}
	return newOmap("at_s", roundTo(at, 3), "basis", basis, "confidence", roundTo(conf, 2), "label", label), kind, ordinal
}

// lyricLines returns the lines of the lyrics section a spot lands in: the nth section of its kind, else the section whose
// label the comment stored when it was written (untimed takes).
func lyricLines(secs []yue2.Section, kind string, ordinal int, storedLabel string) []string {
	idx := -1
	if kind != "" && ordinal > 0 {
		n := 0
		for i, s := range secs {
			if s.Kind == kind {
				if n++; n == ordinal {
					idx = i
					break
				}
			}
		}
	}
	if idx < 0 && storedLabel != "" {
		idx = slices.IndexFunc(secs, func(s yue2.Section) bool { return s.Label == storedLabel })
	}
	if idx < 0 || len(secs[idx].Lines) == 0 {
		return nil
	}
	lines := secs[idx].Lines[:min(len(secs[idx].Lines), maxLyricLines)]
	out := make([]string, len(lines))
	for i, l := range lines {
		if utf8.RuneCountInString(l) > maxLyricLineLen {
			l = string([]rune(l)[:maxLyricLineLen])
		}
		out[i] = l
	}
	return out
}

// closeLoopComments adds the close-the-loop fields to the comment list of one take: ancestors' open fix and note comments
// (within the existing list limit), and per comment its relation, the mapped spot, the lyric lines of the mapped section and
// the Song PRs still rendering. own are the comments already listed (raw is aligned with items). It returns the new list and
// the loop state; with nothing to report the list is returned untouched.
func (b *Builder) closeLoopComments(ctx context.Context, trackID, lyrics string, recipeKnown bool, ancestors []string, items []any, raw []judgments.Comment, want int) ([]any, closeLoop, error) {
	if b.cl == nil || b.j == nil {
		return items, closeLoop{}, nil
	}
	anc, ancOpen, err := b.ancestorComments(ctx, ancestors, want-len(items))
	if err != nil {
		return nil, closeLoop{}, err
	}
	all := append(slices.Clone(raw), anc...)
	ids := make([]string, len(all))
	for i, c := range all {
		ids[i] = c.ID
	}
	flight, err := b.cl.InFlightPatches(ctx, ids)
	if err != nil {
		return nil, closeLoop{}, err
	}
	if len(ancestors) == 0 && len(flight) == 0 {
		return items, closeLoop{}, nil
	}
	tgt, err := b.cl.Timing(ctx, trackID)
	if err != nil {
		return nil, closeLoop{}, err
	}
	timings := map[string]anchor.Timing{trackID: tgt}
	var secs []yue2.Section
	if recipeKnown && lyrics != "" {
		secs = yue2.LyricsSections(lyrics)
	}
	out := slices.Clone(items)
	for _, c := range anc {
		out = append(out, commentOmap(c))
	}
	for i, c := range all {
		om := out[i].(*omap)
		own := c.TrackID == trackID
		relation := "ancestor"
		if own {
			relation = "self"
		}
		src, ok := timings[c.TrackID]
		if !ok {
			if src, err = b.cl.Timing(ctx, c.TrackID); err != nil {
				return nil, closeLoop{}, err
			}
			timings[c.TrackID] = src
		}
		spot, kind, ordinal := spotOf(c, own, src, tgt)
		om.Set("relation", relation)
		if spot == nil {
			om.Set("spot", nil)
		} else {
			om.Set("spot", spot)
		}
		if c.State == "open" && (c.Kind == "fix" || c.Kind == "note") {
			stored := ""
			if c.Section != nil {
				stored = c.Section.Label
			}
			if lines := lyricLines(secs, kind, ordinal, stored); len(lines) > 0 {
				om.Set("lyric_lines", lines)
			}
		}
		if p := flight[c.ID]; len(p) > 0 {
			om.Set("in_flight_patch_ids", p)
		}
	}
	return out, closeLoop{ancestorOpenFix: ancOpen, active: true}, nil
}

// hotspotsSection returns the listening.hotspots value of a take: up to three {from_s, to_s, score, label}, nil when the
// take has none (the key is then left out so earlier bundles stay byte-identical).
func (b *Builder) hotspotsSection(ctx context.Context, trackID string) ([]any, error) {
	if b.cl == nil {
		return nil, nil
	}
	hs, err := b.cl.Hotspots(ctx, trackID)
	if err != nil || len(hs) == 0 {
		return nil, err
	}
	out := make([]any, 0, maxSpotHotspots)
	for _, h := range hs[:min(len(hs), maxSpotHotspots)] {
		var label any
		if h.Label != nil {
			label = *h.Label
		}
		out = append(out, newOmap("from_s", roundTo(h.FromS, 2), "to_s", roundTo(h.ToS, 2), "score", h.Score, "label", label))
	}
	return out, nil
}

// stripCommentKey removes key from every comment of the list; it reports whether any comment had it.
func stripCommentKey(comments any, key string) bool {
	l, ok := comments.([]any)
	if !ok {
		return false
	}
	changed := false
	for _, c := range l {
		if om, ok := c.(*omap); ok && om.Del(key) {
			changed = true
		}
	}
	return changed
}
