package lineage_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func listenerCtx() context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener})
}

// TestTree_ListenerHiddenRootBranches: a discarded root with two visible branches (one with a descendant) must not crash
// the Listener's tree, and the tree is the subtree of the requested take's topmost visible ancestor.
func TestTree_ListenerHiddenRootBranches(t *testing.T) {
	t.Parallel()
	svc, st, info := t1Env(t)
	root := info.Tx["A"]
	a := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 11, Kind: "glacier", Parent: root})
	b := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 12, Kind: "glacier", Parent: root})
	c := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 13, Kind: "master", Parent: b})
	if _, err := st.W.ExecContext(context.Background(), `UPDATE tracks SET status = 'discarded' WHERE id = ?`, root); err != nil {
		t.Fatal(err)
	}
	ctx := listenerCtx()
	tr, err := svc.Tree(ctx, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tr.RootID != a || len(tr.Nodes) != 1 || tr.Total != 1 || tr.Nodes[0].ChildrenCount != 0 {
		t.Errorf("tree of A: root=%s nodes=%d total=%d", tr.RootID, len(tr.Nodes), tr.Total)
	}
	tr, err = svc.Tree(ctx, c, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tr.RootID != b || len(tr.Nodes) != 2 || tr.Total != 2 || nodeOf(t, tr, b).ChildrenCount != 1 {
		t.Errorf("tree of C: root=%s nodes=%d total=%d", tr.RootID, len(tr.Nodes), tr.Total)
	}
}

// TestTree_ListenerHiddenMiddle: a discarded middle node's children hang from the nearest visible ancestor.
func TestTree_ListenerHiddenMiddle(t *testing.T) {
	t.Parallel()
	svc, st, info := t1Env(t)
	root := info.Tx["A"]
	m := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 11, Kind: "glacier", Parent: root})
	k1 := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 12, Kind: "master", Parent: m})
	k2 := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 13, Kind: "master", Parent: m})
	if _, err := st.W.ExecContext(context.Background(), `UPDATE tracks SET status = 'discarded' WHERE id = ?`, m); err != nil {
		t.Fatal(err)
	}
	tr, err := svc.Tree(listenerCtx(), root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Total != 3 || len(tr.Nodes) != 3 || nodeOf(t, tr, root).ChildrenCount != 2 {
		t.Errorf("total=%d nodes=%d", tr.Total, len(tr.Nodes))
	}
	for _, k := range []string{k1, k2} {
		if p := nodeOf(t, tr, k).ParentID; p == nil || *p != root {
			t.Errorf("%s parent = %v, want the root", k, p)
		}
	}
}
