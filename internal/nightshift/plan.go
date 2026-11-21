package nightshift

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/mandates"
)

// Plan kinds.
const (
	KindExploit = "exploit"
	KindExplore = "explore"
)

// Dropped reasons (the first failing rule of C373.2, in this order).
const (
	DropEngine         = "engine"
	DropMinStars       = "min_stars"
	DropRootFilter     = "root_filter"
	DropPerLineage     = "per_lineage"
	DropBacklogged     = "backlogged"
	DropRecentUnjudged = "recent_unjudged"
	DropMode           = "mode"
)

const (
	minDuelsForU = 3   // a ladder strength counts from this many duels
	priorSD      = 1.5 // the ladder prior, used for a lineage without counted duels
	bpmStep      = 8
	bpmFloor     = 60 // the lint_recipe BPM range
	bpmCeil      = 180
)

// Lineage is one lineage as the planner sees it: its best take and the facts that gate it (C373.1).
type Lineage struct {
	RootID, BestTrackID, Engine, Style string
	BestStars                          *int     // the Owner's stars of the best take (owner_ratings)
	U, SD                              *float64 // ladder strength of the best take; nil without counted duels
	NDuels                             int
	RecentUnjudged                     bool // a night-shift take of this root from the last 3 nights has no Owner rating
	Backlogged                         bool // the callbacks backlog gate would refuse an agent item on this root
}

// Input is everything Plan reads; nothing else (no clock, no randomness, no database).
type Input struct {
	Lineages   []Lineage
	Scope      mandates.Scope
	ByRoot     map[string]int // takes already made per root under the mandate (spend.MandateUsage.ByRoot)
	MaxTakes   int
	MinStars   int
	ExplorePPM int64
	TierB      bool               // carried for the caller's estimate; the planner does not price
	Seed       uint64             // fnv64a of the night key
	TieBreak   map[string]float64 // root => sum of standardized values of trusted critics; nil when absent
}

// Planned is one chosen take.
type Planned struct {
	Kind   string // exploit | explore
	RootID string
	Item   generation.Item
	Reason string // short and lyric-free
}

// Dropped is a lineage the planner refused, with the first rule it failed.
type Dropped struct{ RootID, Reason string }

// Plan chooses tonight's takes: at most one per lineage (D-M6N-7), exploit by the Owner's own signal, explore where the ladder
// is least certain. Pure: the same Input always returns the same plan.
func Plan(in Input) (planned []Planned, dropped []Dropped) {
	n := min(in.MaxTakes, in.Scope.MaxTakesPerBatch)
	cands := make([]*cand, 0, len(in.Lineages))
	for i := range in.Lineages {
		l := &in.Lineages[i]
		if reason := scopeFilter(in, l); reason != "" {
			dropped = append(dropped, Dropped{RootID: l.RootID, Reason: reason})
			continue
		}
		cands = append(cands, &cand{l: l, tie: in.TieBreak[l.RootID], hash: rootHash(in.Seed, l.RootID)})
	}
	if n <= 0 || len(cands) == 0 {
		return nil, dropped
	}
	explore := int((int64(n)*in.ExplorePPM + 500_000) / 1_000_000)
	exploit := n - explore

	exploitPool := slices.DeleteFunc(slices.Clone(cands), func(c *cand) bool { return !stars(c.l, in.MinStars) })
	sort.SliceStable(exploitPool, func(i, j int) bool { return exploitLess(exploitPool[i], exploitPool[j]) })

	b := &builder{in: in, used: map[string]bool{}, failed: map[string]bool{}}
	b.fill(KindExploit, exploitPool, exploit)

	exploreFloor := max(in.Scope.MinStars, in.MinStars-1)
	explorePool := slices.DeleteFunc(slices.Clone(cands), func(c *cand) bool {
		return b.used[c.l.RootID] || !(stars(c.l, exploreFloor) || sd(c.l) >= 1.0)
	})
	sort.SliceStable(explorePool, func(i, j int) bool { return exploreLess(explorePool[i], explorePool[j]) })
	b.fill(KindExplore, explorePool, explore)

	// Unfilled slots of one pool go to the other: an exhausted pool leaves its remainder to the sibling.
	if left := n - len(b.out); left > 0 {
		b.fill(KindExplore, explorePool, left)
	}
	if left := n - len(b.out); left > 0 {
		b.fill(KindExploit, exploitPool, left)
	}
	for _, c := range cands { // a lineage no pool could build an item for under the mandate's modes
		if b.failed[c.l.RootID] && !b.used[c.l.RootID] {
			dropped = append(dropped, Dropped{RootID: c.l.RootID, Reason: DropMode})
		}
	}
	return b.out, dropped
}

type cand struct {
	l    *Lineage
	tie  float64
	hash uint64
}

func stars(l *Lineage, floor int) bool { return l.BestStars != nil && *l.BestStars >= floor }

func sd(l *Lineage) float64 {
	if l.SD == nil {
		return priorSD
	}
	return *l.SD
}

// countedU is the lineage's ladder strength when it rests on enough duels.
func countedU(l *Lineage) (float64, bool) {
	if l.U == nil || l.NDuels < minDuelsForU {
		return 0, false
	}
	return *l.U, true
}

func scopeFilter(in Input, l *Lineage) string {
	s := in.Scope
	switch {
	case !slices.Contains(s.Engines, l.Engine):
		return DropEngine
	case !stars(l, s.MinStars):
		return DropMinStars
	case s.RootTrackIDs != nil && !slices.Contains(s.RootTrackIDs, l.RootID):
		return DropRootFilter
	case in.ByRoot[l.RootID]+1 > s.MaxTakesPerLineage:
		return DropPerLineage
	case l.Backlogged:
		return DropBacklogged
	case l.RecentUnjudged:
		return DropRecentUnjudged
	case !slices.Contains(s.Modes, lineage.ModeNewSeed) && !slices.Contains(s.Modes, lineage.ModeStyleEdit):
		return DropMode
	}
	return ""
}

// exploitLess: U desc (counted duels only, those first), stars desc, tie-break desc, seeded hash.
func exploitLess(a, b *cand) bool {
	ua, oka := countedU(a.l)
	ub, okb := countedU(b.l)
	if oka != okb {
		return oka
	}
	if oka && ua != ub {
		return ua > ub
	}
	if sa, sb := *a.l.BestStars, *b.l.BestStars; sa != sb {
		return sa > sb
	}
	if a.tie != b.tie {
		return a.tie > b.tie
	}
	return a.hash < b.hash
}

// exploreLess: SD desc (unknown first), tie-break desc, seeded hash.
func exploreLess(a, b *cand) bool {
	if (a.l.SD == nil) != (b.l.SD == nil) {
		return a.l.SD == nil
	}
	if a.l.SD != nil && *a.l.SD != *b.l.SD {
		return *a.l.SD > *b.l.SD
	}
	if a.tie != b.tie {
		return a.tie > b.tie
	}
	return a.hash < b.hash
}

type builder struct {
	in     Input
	used   map[string]bool
	failed map[string]bool
	out    []Planned
}

// fill takes up to want unused candidates from pool in rank order. A candidate whose item cannot be built under the mandate's
// modes is skipped (and reported as "mode" if no pool takes it) and the next one is tried.
func (b *builder) fill(kind string, pool []*cand, want int) {
	for _, c := range pool {
		if want <= 0 {
			return
		}
		if b.used[c.l.RootID] {
			continue
		}
		item, reason, ok := b.item(kind, c)
		if !ok {
			b.failed[c.l.RootID] = true // the other pool may still serve it
			continue
		}
		b.used[c.l.RootID] = true
		b.out = append(b.out, Planned{Kind: kind, RootID: c.l.RootID, Item: item, Reason: reason})
		want--
	}
}

func (b *builder) item(kind string, c *cand) (generation.Item, string, bool) {
	l := c.l
	parent := l.BestTrackID
	newSeed := generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &parent, NSeeds: 1}
	canSeed := slices.Contains(b.in.Scope.Modes, lineage.ModeNewSeed)
	if kind == KindExplore && slices.Contains(b.in.Scope.Modes, lineage.ModeStyleEdit) {
		if style, from, to, ok := shiftBPM(l.Style, b.in.Seed, l.RootID); ok {
			it := generation.Item{Mode: lineage.ModeStyleEdit, ParentTrackID: &parent, NSeeds: 1, Patch: &generation.Patch{Style: &style}}
			return it, fmt.Sprintf("explore: style edit, bpm %d to %d", from, to), true
		}
	}
	if !canSeed {
		return generation.Item{}, "", false
	}
	return newSeed, describe(kind, l), true
}

func describe(kind string, l *Lineage) string {
	s := fmt.Sprintf("%s: new seed, %d stars", kind, *l.BestStars)
	if u, ok := countedU(l); ok {
		s += fmt.Sprintf(", u %.2f over %d duels", u, l.NDuels)
	} else if kind == KindExplore {
		s += ", ladder uncertain"
	}
	return s
}

// shiftBPM moves the style's BPM hint by 8 in a seeded direction (the other direction when the result leaves 60..180).
func shiftBPM(style string, seed uint64, root string) (string, int, int, bool) {
	_, bpm := yue2.StyleTerms(style)
	if bpm == 0 {
		return "", 0, 0, false
	}
	step := bpmStep
	if !coinPlus(seed, root) {
		step = -bpmStep
	}
	to := bpm + step
	if to < bpmFloor || to > bpmCeil {
		to = bpm - step
	}
	if to < bpmFloor || to > bpmCeil {
		return "", 0, 0, false
	}
	out, ok := yue2.WithBPM(style, to)
	return out, bpm, to, ok
}

func rootHash(seed uint64, root string) uint64 { return fnvOf(seed, root, "") }

// coinPlus is the seeded coin of the BPM shift: true shifts up.
func coinPlus(seed uint64, root string) bool { return fnvOf(seed, root, "bpm")&1 == 1 }

func fnvOf(seed uint64, root, salt string) uint64 {
	h := fnv.New64a()
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], seed)
	_, _ = h.Write(b[:])
	_, _ = h.Write([]byte(root))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(salt))
	return h.Sum64()
}

// NightSeed is the planner seed of a night: fnv64a of its key ("2026-11-21" style).
func NightSeed(nightKey string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(nightKey))
	return h.Sum64()
}
