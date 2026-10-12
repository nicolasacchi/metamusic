package importer

import (
	"sort"
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
		out = append(out, &node{ex: t, reqID: byRecipe[t.RecipeID].RequestID, at: parseTS(t.RecordedAt), frames: t.FramesSHA256})
	}
	return out
}

// planLineage infers sibling edges and applies mapping parent overrides (C16.5).
func (b *builder) planLineage() {
	exNodes := b.exNodes
	var plNodes []*node
	for _, t := range b.takes {
		plNodes = append(plNodes, &node{pl: t, reqID: t.recipe.reqID(), at: t.recordedAt, frames: t.g.members[0].frames})
	}
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
	for _, n := range plNodes {
		t := n.pl
		if !t.override() {
			continue
		}
		row := t.g.mapRow
		if row.Parent == "-" {
			continue
		}
		ref, ok := b.resolveParent(row.Parent, exNodes, plNodes, trackByID)
		if !ok || (ref.planned == t) {
			b.iss.warn(t.g.rep.e.ref(), row.Line, "parent_unresolved", "mapping parent names no unique take")
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

// generations reads tracks.generation (the snapshot does not carry it).
func (b *builder) generations() map[string]int {
	out := map[string]int{}
	if b.im.st == nil {
		return out
	}
	rows, err := b.im.st.R.QueryContext(b.ctx, `SELECT id, generation FROM tracks`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var g int
		if rows.Scan(&id, &g) == nil {
			out[id] = g
		}
	}
	return out
}

func (r trackRef) label(b *builder) string {
	if r.planned != nil {
		return r.planned.g.rep.e.ref()
	}
	return r.existing
}
