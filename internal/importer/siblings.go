package importer

import (
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// trackRef points at an existing library track or a take planned in this run.
type trackRef struct {
	existing string
	planned  *plannedTake
}

func (r trackRef) zero() bool { return r.existing == "" && r.planned == nil }

// node is a track (existing or planned) taking part in lineage inference.
type node struct {
	ex     *library.SnapTrack
	pl     *plannedTake
	reqID  string
	at     time.Time
	frames string
}

func (n *node) ref() trackRef {
	if n.pl != nil {
		return trackRef{planned: n.pl}
	}
	return trackRef{existing: n.ex.ID}
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func earlier(a, b *node) bool {
	if !a.at.Equal(b.at) {
		return a.at.Before(b.at)
	}
	return a.frames < b.frames
}

// existingNodes lists library tracks as nodes.
func (b *builder) existingNodes() []*node {
	byRecipe := map[string]library.SnapRecipe{}
	for _, r := range b.snap.Recipes {
		byRecipe[r.ID] = r
	}
	out := make([]*node, 0, len(b.snap.Tracks))
	for i := range b.snap.Tracks {
		t := &b.snap.Tracks[i]
		if t.Reference { // stubs never take part in seed families or mothers (D-M2c-8)
			continue
		}
		out = append(out, &node{ex: t, reqID: byRecipe[t.RecipeID].RequestID, at: parseTS(t.RecordedAt), frames: t.FramesSHA256})
	}
	return out
}

// planLineage infers sibling edges and applies mapping parent overrides (C16.5).
func (b *builder) planLineage() {
	exNodes := b.exNodes
	var plNodes []*node
	for _, t := range b.takes {
		if t.reference() {
			continue
		}
		plNodes = append(plNodes, &node{pl: t, reqID: t.recipe.reqID(), at: t.recordedAt, frames: t.g.members[0].frames})
	}
	b.planReferenceGroups()
	trackByID := b.trackByID

	// Sibling inference, per seed family; mapping overrides sit out.
	fam := map[string][]*node{}
	for _, n := range exNodes {
		k := yue2.SeedFamily(n.reqID)
		fam[k] = append(fam[k], n)
	}
	for _, n := range plNodes {
		if n.pl.override() {
			continue
		}
		k := yue2.SeedFamily(n.reqID)
		fam[k] = append(fam[k], n)
	}
	keys := make([]string, 0, len(fam))
	for k := range fam {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		members := fam[k]
		var pls, exs []*node
		for _, n := range members {
			if n.pl != nil {
				pls = append(pls, n)
			} else {
				exs = append(exs, n)
			}
		}
		if len(pls) == 0 || len(members) < 2 {
			continue
		}
		sort.SliceStable(pls, func(i, j int) bool { return earlier(pls[i], pls[j]) })
		var root *node
		if len(exs) > 0 {
			// Existing lineage is never rewritten: attach to the earliest existing root.
			for _, n := range exs {
				rt := trackByID[n.ex.RootTrackID]
				if rt == nil {
					rt = n.ex
				}
				cand := &node{ex: rt, reqID: b.requestIDOf(rt), at: parseTS(rt.RecordedAt), frames: rt.FramesSHA256}
				if root == nil || earlier(cand, root) {
					root = cand
				}
			}
		} else {
			root = pls[0]
		}
		for _, n := range pls {
			if n == root {
				continue
			}
			rule := "seed_family"
			if n.reqID == root.reqID {
				rule = "same_request_id"
			}
			n.pl.parent, n.pl.rule = root.ref(), rule
			if root.ex != nil && n.at.Before(root.at) {
				b.iss.warn(n.pl.g.rep.e.ref(), 0, "older_than_root", "attached to the existing lineage root")
			}
		}
	}

	// Mapping parent overrides.
	for _, t := range b.takes {
		if !t.override() {
			continue
		}
		row := t.g.mapRow
		if row.Parent == "-" {
			continue
		}
		if t.reference() && b.groupByKey(row.Parent) == nil {
			// a reference take can only descend from a file named by path or sha256: key (C61)
			b.iss.warn(t.g.rep.e.ref(), row.Line, "reference_parent_unresolved", "mapping parent of a reference take must name an audio file")
			continue
		}
		ref, ok := b.resolveParent(row.Parent, exNodes, plNodes, trackByID)
		if !ok || (ref.planned == t) || b.isReferenceRef(ref) != t.reference() {
			code := "parent_unresolved"
			if t.reference() {
				code = "reference_parent_unresolved"
			}
			b.iss.warn(t.g.rep.e.ref(), row.Line, code, "mapping parent names no unique take of the same kind")
			continue
		}
		t.parent, t.rule = ref, "mapping"
	}

	// Resolve roots and generations; break cycles.
	gens := b.generations()
	state := map[*plannedTake]int{} // 1 visiting, 2 done
	var resolve func(t *plannedTake)
	resolve = func(t *plannedTake) {
		if state[t] == 2 {
			return
		}
		state[t] = 1
		switch {
		case t.parent.zero():
			t.root, t.generation = trackRef{planned: t}, 0
		case t.parent.planned != nil:
			p := t.parent.planned
			if state[p] == 1 {
				b.iss.warn(t.g.rep.e.ref(), 0, "parent_cycle", "lineage cycle broken; take stays a root")
				t.parent, t.rule = trackRef{}, ""
				t.root, t.generation = trackRef{planned: t}, 0
			} else {
				resolve(p)
				if t.parent.zero() {
					t.root, t.generation = trackRef{planned: t}, 0
				} else {
					t.root, t.generation = p.root, p.generation+1
				}
			}
		default:
			ex := trackByID[t.parent.existing]
			root := t.parent.existing
			if ex != nil {
				root = ex.RootTrackID
			}
			t.root, t.generation = trackRef{existing: root}, gens[t.parent.existing]+1
		}
		state[t] = 2
	}
	for _, t := range b.takes {
		resolve(t)
	}
}

func (b *builder) requestIDOf(t *library.SnapTrack) string {
	for _, r := range b.snap.Recipes {
		if r.ID == t.RecipeID {
			return r.RequestID
		}
	}
	return ""
}

// override reports whether a mapping row dictates this take's parent.
func (t *plannedTake) override() bool {
	return t.g.mapRow != nil && t.g.mapRow.Parent != ""
}

// resolveParent resolves a mapping parent value: an inbox path or sha256:
// key (that take) or a request id (the unique root of that request's takes).
func (b *builder) resolveParent(v string, ex, pl []*node, trackByID map[string]*library.SnapTrack) (trackRef, bool) {
	if g := b.groupByKey(v); g != nil {
		switch {
		case g.take != nil:
			return trackRef{planned: g.take}, true
		case g.existingTrack != "":
			return trackRef{existing: g.existingTrack}, true
		}
		return trackRef{}, false
	}
	roots := map[trackRef]bool{}
	for _, n := range append(append([]*node{}, ex...), pl...) {
		if n.reqID != v {
			continue
		}
		roots[b.rootRefOf(n, trackByID)] = true
	}
	if len(roots) != 1 {
		return trackRef{}, false
	}
	for r := range roots {
		return r, true
	}
	return trackRef{}, false
}

// rootRefOf is the lineage root of a node as planned so far.
func (b *builder) rootRefOf(n *node, trackByID map[string]*library.SnapTrack) trackRef {
	if n.ex != nil {
		return trackRef{existing: n.ex.RootTrackID}
	}
	t := n.pl
	seen := map[*plannedTake]bool{}
	for !t.parent.zero() && !seen[t] {
		seen[t] = true
		if t.parent.planned == nil {
			if ex := trackByID[t.parent.existing]; ex != nil {
				return trackRef{existing: ex.RootTrackID}
			}
			return trackRef{existing: t.parent.existing}
		}
		t = t.parent.planned
	}
	return trackRef{planned: t}
}

// groupByKey finds an audio group by inbox path or sha256:<hex> key.
func (b *builder) groupByKey(v string) *audioGroup {
	for _, g := range b.groups {
		for _, m := range g.members {
			if m.e.path == v || "sha256:"+m.e.sha == v {
				return g
			}
		}
	}
	return nil
}

// generations maps stored track ids to their generation, from the snapshot
// (one read transaction, C11).
func (b *builder) generations() map[string]int {
	out := make(map[string]int, len(b.snap.Tracks))
	for _, t := range b.snap.Tracks {
		out[t.ID] = t.Generation
	}
	return out
}

func (r trackRef) label(b *builder) string {
	if r.planned != nil {
		return r.planned.g.rep.e.ref()
	}
	return r.existing
}

// isReferenceRef reports whether r points at a reference take (planned or stored).
func (b *builder) isReferenceRef(r trackRef) bool {
	if r.planned != nil {
		return r.planned.reference()
	}
	if t := b.trackByID[r.existing]; t != nil {
		return t.Reference
	}
	return false
}

// refGroupKey splits a reference title slug "<song>_p<N>" into its song key and ordinal.
var refGroupRe = regexp.MustCompile(`^(.+)_p([0-9]+)$`)

type refMember struct {
	n  int
	at time.Time
	// sha orders ties; exactly one of pl / ex is set
	sha string
	pl  *plannedTake
	ex  *library.SnapTrack
}

func refLess(a, b refMember) bool {
	if a.n != b.n {
		return a.n < b.n
	}
	if !a.at.Equal(b.at) {
		return a.at.Before(b.at)
	}
	return a.sha < b.sha
}

// planReferenceGroups infers the lineage of lossy reference takes named "<song>_p<N>" (D-M2c-8): the
// lowest ordinal is the root, every other member a child of it (rule reference_group). Stored lossy
// reference tracks join through their slug; an existing root wins over planned members, and stored
// lineage is never rewritten. Mapping parents override and sit out.
func (b *builder) planReferenceGroups() {
	groups := map[string][]refMember{}
	ordinal := func(slug string) (string, int, bool) {
		m := refGroupRe.FindStringSubmatch(slug)
		if m == nil {
			return "", 0, false
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return "", 0, false
		}
		return m[1], n, true
	}
	for i := range b.snap.Tracks {
		t := &b.snap.Tracks[i]
		if !t.Reference || !t.Lossy {
			continue
		}
		if key, n, ok := ordinal(t.Slug); ok {
			groups[key] = append(groups[key], refMember{n: n, at: parseTS(t.RecordedAt), sha: t.MasterSHA256, ex: t})
		}
	}
	for _, t := range b.takes {
		if !t.reference() || !t.g.isMP3() || t.override() {
			continue
		}
		if key, n, ok := ordinal(yue2.Slug(t.title)); ok {
			groups[key] = append(groups[key], refMember{n: n, at: t.recordedAt, sha: t.g.rep.e.sha, pl: t})
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ms := groups[k]
		planned := 0
		for _, m := range ms {
			if m.pl != nil {
				planned++
			}
		}
		if planned == 0 || len(ms) < 2 {
			continue
		}
		var root *refMember
		for i := range ms { // an existing root wins over planned members
			if ms[i].ex != nil && (root == nil || refLess(ms[i], *root)) {
				root = &ms[i]
			}
		}
		if root == nil {
			for i := range ms {
				if root == nil || refLess(ms[i], *root) {
					root = &ms[i]
				}
			}
		}
		b.refGroups++
		for _, m := range ms {
			if m.pl == nil || (root.pl == m.pl) {
				continue
			}
			if root.ex != nil {
				m.pl.parent = trackRef{existing: root.ex.ID}
				if m.n < root.n {
					b.iss.warn(m.pl.g.rep.e.ref(), 0, "older_than_root", "attached to the existing lineage root")
				}
			} else {
				m.pl.parent = trackRef{planned: root.pl}
			}
			m.pl.rule = "reference_group"
		}
	}
}
