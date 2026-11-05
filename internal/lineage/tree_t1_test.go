package lineage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func t1Env(t *testing.T) (*lineage.Service, *store.Store, lt.T1Info) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	info := lt.FixtureT1(t, st, lt.FixedNow)
	return lineage.New(lineage.Options{Store: st, Now: func() time.Time { return lt.FixedNow }}), st, info
}

func nodeOf(t *testing.T, tr lineage.Tree, id string) lineage.Node {
	t.Helper()
	for _, n := range tr.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %s not in tree", id)
	return lineage.Node{}
}

// TestLineage_AlsoFrom: a morph of A and B sits under A; B is an annotation of the morph and gains no child edge.
func TestLineage_AlsoFrom(t *testing.T) {
	t.Parallel()
	svc, st, info := t1Env(t)
	a, b := info.Tx["A"], info.Tx["B"]
	m := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 1, Kind: "morph", Parent: a, ParentRole: "a", Also: []lt.TransformInput{{Role: "b", Track: b}}})
	tr, err := svc.Tree(context.Background(), a, 0)
	if err != nil {
		t.Fatal(err)
	}
	mn := nodeOf(t, tr, m)
	if mn.ParentID == nil || *mn.ParentID != a || mn.Operator != "morph" {
		t.Errorf("morph node parent=%v operator=%q", mn.ParentID, mn.Operator)
	}
	if len(mn.AlsoFrom) != 1 || mn.AlsoFrom[0].TrackID != b || mn.AlsoFrom[0].Role != "b" {
		t.Errorf("also_from = %+v", mn.AlsoFrom)
	}
	if an := nodeOf(t, tr, a); len(an.AlsoFrom) != 0 || an.AlsoFrom == nil || an.ChildrenCount != 1 {
		t.Errorf("A: also_from=%+v children=%d", an.AlsoFrom, an.ChildrenCount)
	}
	trB, err := svc.Tree(context.Background(), b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bn := nodeOf(t, trB, b); bn.ChildrenCount != 0 || len(trB.Nodes) != 1 {
		t.Errorf("B gained an edge: children=%d nodes=%d", bn.ChildrenCount, len(trB.Nodes))
	}
}

// TestLineage_TransformNodes: origin is derived, the transform block names kind and spec hash, plain nodes carry none.
func TestLineage_TransformNodes(t *testing.T) {
	t.Parallel()
	svc, st, info := t1Env(t)
	a := info.Tx["A"]
	g := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 2, Kind: "glacier", Parent: a})
	g2 := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 3, Kind: "master", Parent: g})
	tr, err := svc.Tree(context.Background(), g2, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{g, g2} {
		n := nodeOf(t, tr, id)
		if n.Origin != "transform" || n.Transform == nil || n.Transform.TransformID != id || len(n.Transform.SpecHash) != 64 || n.JobID != nil {
			t.Errorf("%s: origin=%q transform=%+v job=%v", id, n.Origin, n.Transform, n.JobID)
		}
		if n.AlsoFrom == nil || len(n.AlsoFrom) != 0 {
			t.Errorf("%s: also_from = %#v, want empty non-nil", id, n.AlsoFrom)
		}
	}
	if nodeOf(t, tr, g).Operator != "glacier" || nodeOf(t, tr, g2).Generation != nodeOf(t, tr, g).Generation+1 {
		t.Error("operator or generation of the chain is wrong")
	}
	if an := nodeOf(t, tr, a); an.Origin != "import" || an.Transform != nil {
		t.Errorf("A: origin=%q transform=%+v", an.Origin, an.Transform)
	}
}

// TestLineage_ScoreRenderRoot: a score_render is a motherless root of its own tree.
func TestLineage_ScoreRenderRoot(t *testing.T) {
	t.Parallel()
	svc, st, info := t1Env(t)
	r := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 4, Kind: "score_render"})
	child := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 5, Kind: "widen", Parent: r})
	_ = info
	tr, err := svc.Tree(context.Background(), child, 0)
	if err != nil {
		t.Fatal(err)
	}
	rn := nodeOf(t, tr, r)
	if tr.RootID != r || rn.ParentID != nil || rn.Generation != 0 || rn.Origin != "transform" || rn.Transform == nil || rn.Transform.Kind != "score_render" || rn.ChildrenCount != 1 {
		t.Errorf("root: tree root=%s node=%+v", tr.RootID, rn)
	}
}
