package diction_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/diction"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

func TestRisks_FollowTheLexicon(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.sixTakes()
	ctx := context.Background()
	got, err := e.svc.Risks(ctx, chiave)
	if err != nil {
		t.Fatal(err)
	}
	want := []yue2.DictionRisk{{Word: "chiave", Typical: "kiave", Attempts: 6, Mangled: 4}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("risks (-want +got):\n%s", diff)
	}
	// lyrics without the word, and an empty lyric, have none
	if got, _ := e.svc.Risks(ctx, "[Verse]\nsole e luna e vento"); len(got) != 0 {
		t.Errorf("other words: %+v", got)
	}
	if got, _ := e.svc.Risks(ctx, ""); len(got) != 0 {
		t.Errorf("empty: %+v", got)
	}
	// ignored: no risk
	if _, err := e.svc.UpdateEntry(ctx, alice, "it", "chiave", diction.EntryPatch{Status: ptr("ignored")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Risks(ctx, chiave); len(got) != 0 {
		t.Errorf("ignored: %+v", got)
	}
}

func TestRisks_Thresholds(t *testing.T) {
	t.Parallel()
	// two attempts, both mangled: too few attempts
	e := newEnv(t)
	for range 2 {
		id := e.addTake(chiave)
		e.addDiction(id, chiave, tierbtest.Opts{Subs: map[string]string{"chiave": "kiave"}})
	}
	if got, _ := e.svc.Risks(context.Background(), chiave); len(got) != 0 {
		t.Errorf("attempts 2: %+v", got)
	}
	// two mangled of five (rate 0.4): below half
	e = newEnv(t)
	for i := range 5 {
		id := e.addTake(chiave)
		o := tierbtest.Opts{}
		if i < 2 {
			o.Subs = map[string]string{"chiave": "kiave"}
		}
		e.addDiction(id, chiave, o)
	}
	if got, _ := e.svc.Risks(context.Background(), chiave); len(got) != 0 {
		t.Errorf("rate 0.4: %+v", got)
	}
}

func TestContext_DictionLines(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.sixTakes()
	ctx := context.Background()
	// take 0 heard "kiave" (a small CER, so a clear line); the lexicon makes chiave risky
	s, err := e.svc.ContextSummary(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Clear+s.Mixed+s.Unclear != 1 || !s.HasLines {
		t.Errorf("counts = %+v, want one line", s)
	}
	if diff := cmp.Diff([]string{"chiave"}, s.RiskyWords); diff != "" {
		t.Errorf("risky words (-want +got):\n%s", diff)
	}
	// a human mark replaces the class
	if _, err := e.svc.SetMarks(ctx, alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "unclear"}}); err != nil {
		t.Fatal(err)
	}
	s, _ = e.svc.ContextSummary(ctx, ids[0])
	if s.Clear != 0 || s.Mixed != 0 || s.Unclear != 1 {
		t.Errorf("after a human unclear: %+v", s)
	}
	// a take without a listening check has no summary
	plain := e.addTake(chiave)
	s, err = e.svc.ContextSummary(ctx, plain)
	if err != nil || s.HasLines || len(s.RiskyWords) != 0 {
		t.Errorf("no row: %+v, %v", s, err)
	}
	if _, err := e.svc.ContextSummary(ctx, "nope"); err == nil {
		t.Error("unknown track: no error")
	}
}

func TestLabelSource_HumanOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.sixTakes()
	ctx := context.Background()
	if _, err := e.svc.SetMarks(ctx, alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "unclear"}}); err != nil {
		t.Fatal(err)
	}
	e.advance(1000000)
	if _, err := e.svc.SetMarks(ctx, alice, ids[1], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetMarks(ctx, robot, ids[2], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}}); err != nil {
		t.Fatal(err)
	}
	// a withdrawn mark is not a label
	if _, err := e.svc.SetMarks(ctx, alice, ids[3], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetMarks(ctx, alice, ids[3], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "none"}}); err != nil {
		t.Fatal(err)
	}
	var src diction.LabelSource = e.svc
	got, err := src.HumanLabelledLines(ctx, "it", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("labels = %+v, want the two human marks", got)
	}
	if got[0].Key != (diction.LabelKey{TrackID: ids[1], Section: 0, Line: 0}) || got[0].Human != "clear" || got[1].Human != "unclear" || got[1].Key.TrackID != ids[0] {
		t.Errorf("order or content: %+v", got)
	}
	if got[0].Lang != "it" || got[0].CER <= 0 || got[0].MarkedAt.IsZero() || !got[0].MarkedAt.After(got[1].MarkedAt) {
		t.Errorf("fields: %+v", got[0])
	}
	if one, _ := src.HumanLabelledLines(ctx, "it", 1); len(one) != 1 || one[0].Key.TrackID != ids[1] {
		t.Errorf("limit 1: %+v", one)
	}
	if other, _ := src.HumanLabelledLines(ctx, "fr", 10); len(other) != 0 {
		t.Errorf("other language: %+v", other)
	}
	if all, _ := src.HumanLabelledLines(ctx, "", 10); len(all) != 2 {
		t.Errorf("any language: %+v", all)
	}
	if src.Thresholds() != diction.DefaultThresholds {
		t.Errorf("thresholds = %+v", src.Thresholds())
	}
}

func TestLabelSource_VersionChanges(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.sixTakes()
	ctx := context.Background()
	v := func() string {
		s, err := e.svc.Version(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	v0 := v()
	if v0 != v() {
		t.Error("version changes without a write")
	}
	e.advance(10)
	if _, err := e.svc.SetMarks(ctx, alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}}); err != nil {
		t.Fatal(err)
	}
	v1 := v()
	if v1 == v0 {
		t.Error("a mark did not change the version")
	}
	e.advance(10)
	if _, err := e.svc.SetMarks(ctx, alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "unclear"}}); err != nil {
		t.Fatal(err)
	}
	v2 := v()
	if v2 == v1 {
		t.Error("changing a mark did not change the version")
	}
	// a rewrite at the same timestamp still changes the version
	if _, err := e.svc.SetMarks(ctx, alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}}); err != nil {
		t.Fatal(err)
	}
	if v() == v2 {
		t.Error("a same-timestamp mark change did not change the version")
	}
	v2 = v()
	e.advance(10)
	id := e.addTake(chiave)
	e.addDiction(id, chiave, tierbtest.Opts{})
	if v() == v2 {
		t.Error("a new tier_b_diction row did not change the version")
	}
}
