package callbacks_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/callbacks"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func ids(p callbacks.Page) []string {
	var out []string
	for _, c := range p.Items {
		out = append(out, c.ID)
	}
	return out
}

// TestList_OrderFiltersAndPages: pending first (oldest first), then answered (newest first); filters by state, kind, child, the
// comment's take and the comment; keyset-free offset cursors page through; bad input is refused.
func TestList_OrderFiltersAndPages(t *testing.T) {
	t.Parallel()
	l := newLoop(t, 1, 2)
	ctx := context.Background()
	g := l.info.Lineage
	all, err := l.svc.List(ctx, human, callbacks.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 6 || len(all.Items) != 6 || all.NextCursor != nil {
		t.Fatalf("all: total %d items %d", all.Total, len(all.Items))
	}
	// answer two: they move behind the pending ones
	for _, id := range []string{l.id(1, "fix_chorus"), l.id(2, "keep")} {
		v := "fixed"
		if id == l.id(2, "keep") {
			v = "kept"
		}
		if _, err := l.svc.Submit(ctx, human, id, callbacks.VerdictInput{Verdict: v}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := l.svc.List(ctx, human, callbacks.Filter{})
	for n, c := range got.Items {
		wantState := "pending"
		if n >= 4 {
			wantState = "answered"
		}
		if c.State != wantState {
			t.Errorf("item %d state %s, want %s", n, c.State, wantState)
		}
	}
	pending, _ := l.svc.List(ctx, human, callbacks.Filter{State: []string{"pending"}})
	answered, _ := l.svc.List(ctx, human, callbacks.Filter{State: []string{"answered"}})
	both, _ := l.svc.List(ctx, human, callbacks.Filter{State: []string{"pending", "answered"}})
	if pending.Total != 4 || answered.Total != 2 || both.Total != 6 {
		t.Errorf("states: %d %d %d", pending.Total, answered.Total, both.Total)
	}
	keeps, _ := l.svc.List(ctx, human, callbacks.Filter{Kind: []string{"keep"}})
	child2, _ := l.svc.List(ctx, human, callbacks.Filter{ChildTrackID: g[2]})
	onG0, _ := l.svc.List(ctx, human, callbacks.Filter{TrackID: g[0]})
	onG1, _ := l.svc.List(ctx, human, callbacks.Filter{TrackID: g[1]})
	byComment, _ := l.svc.List(ctx, human, callbacks.Filter{CommentID: l.comment("fix_verse")})
	if keeps.Total != 2 || child2.Total != 3 || onG0.Total != 6 || onG1.Total != 0 || byComment.Total != 2 {
		t.Errorf("filters: keeps %d child2 %d onG0 %d onG1 %d byComment %d", keeps.Total, child2.Total, onG0.Total, onG1.Total, byComment.Total)
	}
	// pages of 4 then 2, no overlap
	p1, err := l.svc.List(ctx, human, callbacks.Filter{Limit: 4})
	if err != nil || p1.NextCursor == nil || len(p1.Items) != 4 {
		t.Fatalf("page 1: %+v %v", p1, err)
	}
	p2, err := l.svc.List(ctx, human, callbacks.Filter{Limit: 4, Cursor: *p1.NextCursor})
	if err != nil || p2.NextCursor != nil || len(p2.Items) != 2 {
		t.Fatalf("page 2: %+v %v", p2, err)
	}
	if !slices.Equal(append(ids(p1), ids(p2)...), ids(got)) {
		t.Errorf("paging reorders: %v + %v vs %v", ids(p1), ids(p2), ids(got))
	}
	for name, f := range map[string]callbacks.Filter{
		"limit":  {Limit: 101},
		"state":  {State: []string{"x"}},
		"kind":   {Kind: []string{"x"}},
		"cursor": {Cursor: "!!"},
	} {
		if _, err := l.svc.List(ctx, human, f); problemCode(err) != "VALIDATION_FAILED" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := newZero().List(ctx, human, callbacks.Filter{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("zero service: %v", err)
	}
}

// TestList_PendingOldestFirstAnsweredNewestFirst pins the two orders with distinct creation times.
func TestList_PendingOldestFirstAnsweredNewestFirst(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	g := r.info.Lineage
	patch := r.stubPatch(g[0], r.comment("fix_chorus"), r.comment("fix_verse"))
	var made []string
	for n, child := range []string{g[1], g[2]} {
		r.now = r.now.Add(time.Duration(n+1) * time.Minute)
		r.fence = store.LeaseFence{Owner: "runner-1", Now: r.now}
		r.exec(`UPDATE runner_lease SET expires_at = ? WHERE id = 1`, store.FormatTime(r.now.Add(time.Hour)))
		if err := r.hook(r.stubJob(patch, child, "")); err != nil {
			t.Fatal(err)
		}
		made = append(made, child)
	}
	ctx := context.Background()
	pend, _ := r.svc.List(ctx, human, callbacks.Filter{State: []string{"pending"}, Kind: []string{"fix"}})
	if len(pend.Items) != 4 || pend.Items[0].Child.ID != made[0] || pend.Items[3].Child.ID != made[1] {
		t.Fatalf("pending order: %v", ids(pend))
	}
	for _, c := range pend.Items {
		if _, err := r.svc.Submit(ctx, human, c.ID, callbacks.VerdictInput{Verdict: "not_yet"}); err != nil {
			t.Fatal(err)
		}
	}
	ans, _ := r.svc.List(ctx, human, callbacks.Filter{State: []string{"answered"}, Kind: []string{"fix"}})
	if len(ans.Items) != 4 || ans.Items[0].Child.ID != made[1] || ans.Items[3].Child.ID != made[0] {
		t.Fatalf("answered order: %v", ids(ans))
	}
}

// TestBacklogGate: the backlog is the unanswered callbacks of children in the lineage root; a human answer drains it; Limit is the
// configured maximum and 0 switches the gate off.
func TestBacklogGate(t *testing.T) {
	t.Parallel()
	l := newLoop(t, 1, 2)
	ctx := context.Background()
	root := l.info.Lineage[0]
	n, err := l.svc.Backlog(ctx, l.st.R, root)
	if err != nil || n != 6 {
		t.Fatalf("backlog %d %v, want 6", n, err)
	}
	if l.svc.Limit() != 6 {
		t.Errorf("limit %d", l.svc.Limit())
	}
	// a critic answer does not drain it, a human one does
	if _, err := l.svc.Submit(ctx, agent, l.id(1, "fix_chorus"), callbacks.VerdictInput{Verdict: "fixed"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := l.svc.Backlog(ctx, l.st.R, root); n != 6 {
		t.Errorf("a critic verdict drained the backlog: %d", n)
	}
	if _, err := l.svc.Submit(ctx, human, l.id(1, "fix_chorus"), callbacks.VerdictInput{Verdict: "fixed"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := l.svc.Backlog(ctx, l.st.R, root); n != 5 {
		t.Errorf("after a human answer %d, want 5", n)
	}
	if n, _ := l.svc.Backlog(ctx, l.st.R, l.info.Lineage[1]); n != 0 {
		t.Errorf("a non-root id has backlog %d", n)
	}
	if n, _ := l.svc.Backlog(ctx, l.st.R, "no-such-root"); n != 0 {
		t.Errorf("unknown root %d", n)
	}
}

// TestBacklogGate_Disabled: MM_CALLBACK_BACKLOG_MAX=0 reports limit 0 (the gate is off); a nil service too.
func TestBacklogGate_Disabled(t *testing.T) {
	t.Parallel()
	off := callbacks.New(callbacks.Options{BacklogMax: 0})
	if off.Limit() != 0 {
		t.Errorf("limit %d", off.Limit())
	}
	var none *callbacks.Service
	if none.Limit() != 0 {
		t.Error("a nil service must report limit 0")
	}
}
