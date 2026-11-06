package callbacks_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/callbacks"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// loop gives a patch on G0 that addresses the two open fixes, rendered as children G1 (manual sections) and G2 (lyrics only).
type loop struct {
	*rig
	patch string
	// callback ids by (child index, comment key)
	cb map[[2]string]string
}

func newLoop(t *testing.T, children ...int) *loop {
	t.Helper()
	r := newRig(t, nil)
	g := r.info.Lineage
	l := &loop{rig: r, patch: r.stubPatch(g[0], r.comment("fix_chorus"), r.comment("fix_verse")), cb: map[[2]string]string{}}
	if len(children) == 0 {
		children = []int{1}
	}
	for _, c := range children {
		if err := r.hook(r.stubJob(l.patch, g[c], "")); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range children {
		for _, k := range []string{"fix_chorus", "fix_verse", "keep"} {
			id := r.str(`SELECT id FROM callbacks WHERE child_track_id = ? AND comment_id = ?`, g[c], r.comment(k))
			l.cb[[2]string{string(rune('0' + c)), k}] = id
		}
	}
	return l
}

func (l *loop) id(child int, key string) string {
	return l.cb[[2]string{string(rune('0' + child)), key}]
}

func problemCode(err error) string {
	var pe *problem.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

// TestSpot_MapsToTheChild: the spot of a comment is computed on read: by section on a child with timed sections, proportionally on a
// lyrics-only child, with the mapped time, basis, confidence and label; links carry the times.
func TestSpot_MapsToTheChild(t *testing.T) {
	t.Parallel()
	l := newLoop(t, 1, 2)
	ctx := context.Background()
	g1, err := l.svc.Get(ctx, human, l.id(1, "fix_chorus"))
	if err != nil {
		t.Fatal(err)
	}
	// G0 plan: chorus 1 is 30..50, the comment is at 40 (halfway); G1 manual: chorus 1 is 36..58 => 47.0
	if g1.Spot.Basis != "section" || math.Abs(g1.Spot.ChildAtS-47) > 0.05 || g1.Spot.SourceAtS == nil || *g1.Spot.SourceAtS != 40 ||
		g1.Spot.Label == nil || *g1.Spot.Label != "chorus 1" || g1.Spot.Confidence <= 0.3 {
		t.Errorf("section spot %+v", g1.Spot)
	}
	if g1.Relation != "self" || g1.State != "pending" || g1.Kind != "fix" || g1.PatchID != l.patch || g1.Child.ID != l.info.Lineage[1] ||
		g1.Child.Seed == 0 || g1.Child.Title != "Synthetic Lineage G1" {
		t.Errorf("view %+v", g1)
	}
	if g1.Links.Child != "/tracks/"+l.info.Lineage[1]+"?t=47.0" || g1.Links.Source == nil || *g1.Links.Source != "/tracks/"+l.info.Lineage[0]+"?t=40.0" {
		t.Errorf("links %+v", g1.Links)
	}
	if g1.Comment.Body != "the chorus lands late" || g1.Comment.AtSecond == nil || g1.Comment.Section == nil || g1.Comment.Section.Label != "chorus 1" || g1.Comment.State != "addressed" {
		t.Errorf("comment %+v", g1.Comment)
	}
	g2, err := l.svc.Get(ctx, human, l.id(2, "fix_chorus"))
	if err != nil {
		t.Fatal(err)
	}
	if g2.Spot.Basis != "proportional" || g2.Spot.Confidence != 0.3 || math.Abs(g2.Spot.ChildAtS-40) > 0.05 || g2.Spot.Label != nil {
		t.Errorf("proportional spot %+v", g2.Spot)
	}
}

// TestSubmit_HumanFixedResolves: a human "fixed" resolves the comment and records state_before/state_after; a repeat is a replay,
// a different answer is CALLBACK_ALREADY_JUDGED.
func TestSubmit_HumanFixedResolves(t *testing.T) {
	t.Parallel()
	l := newLoop(t)
	ctx := context.Background()
	id := l.id(1, "fix_chorus")
	res, err := l.svc.Submit(ctx, human, id, callbacks.VerdictInput{Verdict: "fixed", Note: ptr("  now it lands  "), HeardAtS: ptr(46.5)})
	if err != nil {
		t.Fatal(err)
	}
	v := res.Verdict
	if res.Replayed || v.Source != "human" || v.PrincipalKind != "human" || v.Note == nil || *v.Note != "now it lands" || v.StateBefore == nil || *v.StateBefore != "addressed" ||
		v.StateAfter == nil || *v.StateAfter != "resolved" || res.CommentState == nil || *res.CommentState != "resolved" || v.MapBasis == nil || *v.MapBasis != "section" {
		t.Fatalf("result %+v", res)
	}
	if got := l.state(l.comment("fix_chorus")); got != "resolved" {
		t.Errorf("comment state %s", got)
	}
	again, err := l.svc.Submit(ctx, device, id, callbacks.VerdictInput{Verdict: "fixed", Note: ptr("now it lands")})
	if err != nil || !again.Replayed || again.Verdict.ID != v.ID || again.CommentState == nil || *again.CommentState != "resolved" {
		t.Fatalf("replay (a paired device is the same source): %+v %v", again, err)
	}
	if _, err := l.svc.Submit(ctx, human, id, callbacks.VerdictInput{Verdict: "worse"}); problemCode(err) != "CALLBACK_ALREADY_JUDGED" {
		t.Fatalf("different verdict: %v", err)
	}
	if _, err := l.svc.Submit(ctx, human, id, callbacks.VerdictInput{Verdict: "fixed"}); problemCode(err) != "CALLBACK_ALREADY_JUDGED" {
		t.Fatalf("same verdict, other note: %v", err)
	}
	if n := l.count(`SELECT COUNT(*) FROM callback_verdicts WHERE callback_id = ?`, id); n != 1 {
		t.Errorf("%d verdict rows", n)
	}
	got, _ := l.svc.Get(ctx, human, id)
	if got.State != "answered" || got.MyVerdict == nil || got.HumanVerdict == nil || got.HumanVerdict.ID != v.ID {
		t.Errorf("view after answer %+v", got)
	}
}

// TestSubmit_NotYetAndWorse: not_yet reopens, worse regresses; a callback on a resolved comment leaves it resolved.
func TestSubmit_NotYetAndWorse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ verdict, want string }{{"not_yet", "open"}, {"worse", "regressed"}} {
		l := newLoop(t)
		res, err := l.svc.Submit(context.Background(), human, l.id(1, "fix_verse"), callbacks.VerdictInput{Verdict: c.verdict})
		if err != nil || *res.CommentState != c.want || l.state(l.comment("fix_verse")) != c.want {
			t.Errorf("%s: %+v %v, state %s", c.verdict, res, err, l.state(l.comment("fix_verse")))
		}
	}
	l := newLoop(t)
	l.exec(`UPDATE comments SET state = 'resolved', state_changed_at = ?, state_changed_by = 'human:alice' WHERE id = ?`, "2026-10-07T00:00:00Z", l.comment("fix_chorus"))
	res, err := l.svc.Submit(context.Background(), human, l.id(1, "fix_chorus"), callbacks.VerdictInput{Verdict: "worse"})
	if err != nil || *res.Verdict.StateBefore != "resolved" || *res.Verdict.StateAfter != "resolved" || l.state(l.comment("fix_chorus")) != "resolved" {
		t.Errorf("resolved comment moved by a callback: %+v %v", res, err)
	}
}

// TestCallbackSubmit_MultiChildOrder: with two children of one patch the final comment state does not depend on the order the
// human answers in (any fixed => resolved; else any worse => regressed; else open).
func TestCallbackSubmit_MultiChildOrder(t *testing.T) {
	t.Parallel()
	verdicts := []string{"fixed", "not_yet", "worse"}
	for _, a := range verdicts {
		for _, b := range verdicts {
			want := "open"
			switch {
			case a == "fixed" || b == "fixed":
				want = "resolved"
			case a == "worse" || b == "worse":
				want = "regressed"
			}
			for _, flip := range []bool{false, true} {
				l := newLoop(t, 1, 2)
				first, second := [2]any{1, a}, [2]any{2, b}
				if flip {
					first, second = second, first
				}
				for _, x := range [][2]any{first, second} {
					if _, err := l.svc.Submit(context.Background(), human, l.id(x[0].(int), "fix_chorus"), callbacks.VerdictInput{Verdict: x[1].(string)}); err != nil {
						t.Fatal(err)
					}
				}
				if got := l.state(l.comment("fix_chorus")); got != want {
					t.Errorf("%s then %s (flip %v): %s, want %s", a, b, flip, got, want)
				}
			}
		}
	}
}

// TestSubmit_CriticNeverMovesState: an agent's verdict is a critic opinion: it never moves the comment, records no state change,
// and is replayed or refused per principal; a second agent has its own slot.
func TestSubmit_CriticNeverMovesState(t *testing.T) {
	t.Parallel()
	l := newLoop(t)
	ctx := context.Background()
	id := l.id(1, "fix_chorus")
	res, err := l.svc.Submit(ctx, agent, id, callbacks.VerdictInput{Verdict: "fixed"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict.Source != "critic" || res.Verdict.StateBefore != nil || res.Verdict.StateAfter != nil || res.CommentState != nil || res.RegressionCommentID != nil {
		t.Errorf("critic result %+v", res)
	}
	if got := l.state(l.comment("fix_chorus")); got != "addressed" {
		t.Errorf("a critic moved the comment to %s", got)
	}
	if again, err := l.svc.Submit(ctx, agent, id, callbacks.VerdictInput{Verdict: "fixed"}); err != nil || !again.Replayed {
		t.Errorf("critic replay: %+v %v", again, err)
	}
	if _, err := l.svc.Submit(ctx, agent, id, callbacks.VerdictInput{Verdict: "worse"}); problemCode(err) != "CALLBACK_ALREADY_JUDGED" {
		t.Errorf("critic conflict: %v", err)
	}
	if r2, err := l.svc.Submit(ctx, agent2, id, callbacks.VerdictInput{Verdict: "not_yet"}); err != nil || r2.Replayed {
		t.Errorf("second critic: %+v %v", r2, err)
	}
	// the human's own answer is unaffected and then moves the state
	if r3, err := l.svc.Submit(ctx, human, id, callbacks.VerdictInput{Verdict: "worse"}); err != nil || *r3.CommentState != "regressed" {
		t.Errorf("human after critics: %+v %v", r3, err)
	}
}

// TestSubmit_Isolation: a human sees no critic verdict before answering, an agent none before its own; after answering each sees the
// critics; the human verdict is the operator's decision and is visible to all.
func TestSubmit_Isolation(t *testing.T) {
	t.Parallel()
	l := newLoop(t)
	ctx := context.Background()
	id := l.id(1, "fix_verse")
	if _, err := l.svc.Submit(ctx, agent, id, callbacks.VerdictInput{Verdict: "not_yet"}); err != nil {
		t.Fatal(err)
	}
	h, _ := l.svc.Get(ctx, human, id)
	if len(h.CriticVerdicts) != 0 || h.MyVerdict != nil || h.State != "pending" {
		t.Errorf("human before answering sees %+v", h)
	}
	b, _ := l.svc.Get(ctx, agent2, id)
	if len(b.CriticVerdicts) != 0 || b.MyVerdict != nil {
		t.Errorf("an agent that has not answered sees %+v", b)
	}
	mine, _ := l.svc.Get(ctx, agent, id)
	if mine.MyVerdict == nil || len(mine.CriticVerdicts) != 1 {
		t.Errorf("agent after answering: %+v", mine)
	}
	if _, err := l.svc.Submit(ctx, human, id, callbacks.VerdictInput{Verdict: "fixed"}); err != nil {
		t.Fatal(err)
	}
	h, _ = l.svc.Get(ctx, human, id)
	if len(h.CriticVerdicts) != 1 || h.CriticVerdicts[0].Source != "critic" || h.MyVerdict == nil || h.State != "answered" {
		t.Errorf("human after answering: %+v", h)
	}
	b, _ = l.svc.Get(ctx, agent2, id)
	if b.HumanVerdict == nil || len(b.CriticVerdicts) != 0 {
		t.Errorf("agent2 sees the human verdict but no critics before its own: %+v", b)
	}
}

// TestSubmit_KeepLost: a "lost" keep verdict writes a regression comment on the child (kind fix, open, source callback, at the
// mapped spot, authored by the human, origin = the callback) and never touches the keep comment; "kept" writes nothing.
func TestSubmit_KeepLost(t *testing.T) {
	t.Parallel()
	l := newLoop(t, 1)
	ctx := context.Background()
	id := l.id(1, "keep")
	res, err := l.svc.Submit(ctx, human, id, callbacks.VerdictInput{Verdict: "lost"})
	if err != nil || res.RegressionCommentID == nil || res.Verdict.RegressionCommentID == nil {
		t.Fatalf("%+v %v", res, err)
	}
	if got := l.state(l.comment("keep")); got != "open" {
		t.Errorf("keep comment moved to %s", got)
	}
	rc := *res.RegressionCommentID
	var kind, state, source, body, author, origin string
	var at float64
	if err := l.st.R.QueryRow(`SELECT kind, state, source, body, principal_id, origin_callback_id, at_second FROM comments WHERE id = ? AND track_id = ?`, rc, l.info.Lineage[1]).
		Scan(&kind, &state, &source, &body, &author, &origin, &at); err != nil {
		t.Fatal(err)
	}
	// keep at 90 in G0 chorus 2 (80..105): 0.4 through; G1 manual chorus 2 is 86..112 => 96.4
	if kind != "fix" || state != "open" || source != "callback" || body != "Lost from parent: keep the bass in the second chorus" || author != human.Key() || origin != id || math.Abs(at-96.4) > 0.06 {
		t.Errorf("regression comment: %s %s %s %q %s %s %v", kind, state, source, body, author, origin, at)
	}
	if res.Verdict.StateBefore == nil || *res.Verdict.StateBefore != "open" || *res.Verdict.StateAfter != "open" {
		t.Errorf("keep verdict states %+v", res.Verdict)
	}
	// replay returns the same regression comment and writes no second one
	again, err := l.svc.Submit(ctx, human, id, callbacks.VerdictInput{Verdict: "lost"})
	if err != nil || !again.Replayed || *again.RegressionCommentID != rc {
		t.Errorf("replay %+v %v", again, err)
	}
	if n := l.count(`SELECT COUNT(*) FROM comments WHERE origin_callback_id = ?`, id); n != 1 {
		t.Errorf("%d regression comments", n)
	}
	// an agent's "lost" writes no comment
	l2 := newLoop(t, 1)
	if r2, err := l2.svc.Submit(ctx, agent, l2.id(1, "keep"), callbacks.VerdictInput{Verdict: "lost"}); err != nil || r2.RegressionCommentID != nil {
		t.Errorf("critic lost: %+v %v", r2, err)
	}
	if n := l2.count(`SELECT COUNT(*) FROM comments WHERE source = 'callback'`); n != 0 {
		t.Errorf("%d callback comments from a critic", n)
	}
	l3 := newLoop(t, 1)
	if r3, err := l3.svc.Submit(ctx, human, l3.id(1, "keep"), callbacks.VerdictInput{Verdict: "kept"}); err != nil || r3.RegressionCommentID != nil {
		t.Errorf("kept: %+v %v", r3, err)
	}
}

// TestSubmit_Validation: a verdict outside the kind's set, an overlong note, a bad heard_at_s, an unknown id and a principal that
// cannot judge are refused; a zero service answers ErrUnsupported.
func TestSubmit_Validation(t *testing.T) {
	t.Parallel()
	l := newLoop(t)
	ctx := context.Background()
	long := make([]rune, 501)
	for i := range long {
		long[i] = 'x'
	}
	for name, c := range map[string]struct {
		id   string
		in   callbacks.VerdictInput
		code string
	}{
		"keep verdict on a fix": {l.id(1, "fix_chorus"), callbacks.VerdictInput{Verdict: "kept"}, "VALIDATION_FAILED"},
		"fix verdict on a keep": {l.id(1, "keep"), callbacks.VerdictInput{Verdict: "fixed"}, "VALIDATION_FAILED"},
		"unknown verdict":       {l.id(1, "fix_chorus"), callbacks.VerdictInput{Verdict: "maybe"}, "VALIDATION_FAILED"},
		"overlong note":         {l.id(1, "fix_chorus"), callbacks.VerdictInput{Verdict: "fixed", Note: ptr(string(long))}, "VALIDATION_FAILED"},
		"negative heard":        {l.id(1, "fix_chorus"), callbacks.VerdictInput{Verdict: "fixed", HeardAtS: ptr(-1.0)}, "VALIDATION_FAILED"},
		"unknown callback":      {"nope", callbacks.VerdictInput{Verdict: "fixed"}, "NOT_FOUND"},
	} {
		if _, err := l.svc.Submit(ctx, human, c.id, c.in); problemCode(err) != c.code {
			t.Errorf("%s: %v, want %s", name, err, c.code)
		}
	}
	if _, err := l.svc.Submit(ctx, auth.System("runner"), l.id(1, "fix_chorus"), callbacks.VerdictInput{Verdict: "fixed"}); problemCode(err) != "FORBIDDEN" {
		t.Errorf("system principal: %v", err)
	}
	if _, err := newZero().Submit(ctx, human, "x", callbacks.VerdictInput{Verdict: "fixed"}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("zero service: %v", err)
	}
	if n := l.count(`SELECT COUNT(*) FROM callback_verdicts`); n != 0 {
		t.Errorf("%d verdicts written by refused calls", n)
	}
	// a blank note is no note: replay with and without it is the same answer
	if r, err := l.svc.Submit(ctx, human, l.id(1, "fix_chorus"), callbacks.VerdictInput{Verdict: "not_yet", Note: ptr("   ")}); err != nil || r.Verdict.Note != nil {
		t.Errorf("blank note: %+v %v", r, err)
	}
	if r, err := l.svc.Submit(ctx, human, l.id(1, "fix_chorus"), callbacks.VerdictInput{Verdict: "not_yet"}); err != nil || !r.Replayed {
		t.Errorf("blank note replay: %+v %v", r, err)
	}
}
