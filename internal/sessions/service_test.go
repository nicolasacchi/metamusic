package sessions_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs/songstest"
)

var (
	humanP = auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Via: "proxy"}
	agentP = auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Via: "token"}
)

func sp(s string) *string { return &s }
func bp(b bool) *bool     { return &b }

func pcode(err error) (string, int, map[string]any) {
	var pe *problem.Error
	if errors.As(err, &pe) {
		return pe.Code, pe.Status, pe.Details
	}
	return "", 0, nil
}

// scene: an import run with 2 roots and 1 child (2 Songs, 3 takes) and a collection of 3 backlog drafts.
func scene(t *testing.T) (w *songstest.World, run string) {
	t.Helper()
	w = songstest.New(t)
	sc := w.Sc
	run = sc.Run("completed", 0)
	r1, r2 := sc.Recipe("syn_a_v1", "pop"), sc.Recipe("syn_b_v1", "pop")
	t1 := sc.RootInRun(r1, "Synthetic A", run, sc.At(0))
	sc.RootInRun(r2, "Synthetic B", run, sc.At(2))
	sc.Track(r1, "Synthetic A child", t1, sc.At(4))
	for i := 0; i < 3; i++ {
		d := sc.Draft("syn_c"+string(rune('a'+i))+"_v1", librarytest.DraftOpts{Group: "g", Origin: "jsonl_upload"})
		sc.Collection(d, "Synthetic Collection")
	}
	w.Settle()
	return w, run
}

func TestSessions_List(t *testing.T) {
	t.Parallel()
	w, run := scene(t)
	ctx := context.Background()
	p, err := w.Sessions.List(ctx, sessions.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 2 || len(p.Items) != 2 {
		t.Fatalf("sessions: %+v", p)
	}
	byKind := map[string]sessions.Summary{}
	for _, s := range p.Items {
		byKind[s.Kind] = s
	}
	imp := byKind["import"]
	if imp.Drafts != 0 || imp.Takes != 2 || imp.Songs != 2 {
		t.Errorf("import session counts: drafts %d takes %d songs %d, want 0/2/2", imp.Drafts, imp.Takes, imp.Songs)
	}
	col := byKind["upload"]
	if col.Drafts != 3 || col.Takes != 0 || col.Songs != 3 {
		t.Errorf("collection session counts: %+v", col)
	}
	_ = run
	// newest first
	if p.Items[0].StartedAt < p.Items[1].StartedAt {
		t.Errorf("order: %s then %s", p.Items[0].StartedAt, p.Items[1].StartedAt)
	}
	// filters
	if q, _ := w.Sessions.List(ctx, sessions.ListFilter{Kinds: []string{"import"}}); q.Total != 1 || q.Items[0].Kind != "import" {
		t.Errorf("kind filter: %+v", q)
	}
	if q, _ := w.Sessions.List(ctx, sessions.ListFilter{Q: "COLLECTION"}); q.Total != 1 {
		t.Errorf("q filter: %+v", q)
	}
	if q, _ := w.Sessions.List(ctx, sessions.ListFilter{Q: "%"}); q.Total != 0 {
		t.Errorf("a wildcard in q must be literal: %+v", q)
	}
	// hidden
	if _, err := w.Sessions.Update(ctx, humanP, imp.ID, sessions.Patch{Revision: imp.Revision, Hidden: bp(true)}); err != nil {
		t.Fatal(err)
	}
	if q, _ := w.Sessions.List(ctx, sessions.ListFilter{}); q.Total != 1 {
		t.Errorf("hidden sessions must be omitted: %+v", q)
	}
	if q, _ := w.Sessions.List(ctx, sessions.ListFilter{IncludeHidden: true}); q.Total != 2 {
		t.Errorf("include_hidden: %+v", q)
	}
	// keyset
	var seen []string
	cur := ""
	for i := 0; i < 5; i++ {
		pg, err := w.Sessions.List(ctx, sessions.ListFilter{IncludeHidden: true, Limit: 1, Cursor: cur})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range pg.Items {
			seen = append(seen, it.ID)
		}
		if pg.NextCursor == nil {
			break
		}
		cur = *pg.NextCursor
	}
	if len(seen) != 2 || seen[0] == seen[1] {
		t.Errorf("keyset pages: %v", seen)
	}
	if _, err := w.Sessions.List(ctx, sessions.ListFilter{Cursor: "%%"}); err == nil {
		t.Error("malformed cursor accepted")
	}
}

func TestSessions_Update(t *testing.T) {
	t.Parallel()
	w, _ := scene(t)
	ctx := context.Background()
	p, _ := w.Sessions.List(ctx, sessions.ListFilter{Kinds: []string{"import"}})
	if len(p.Items) != 1 {
		t.Fatalf("import sessions: %d", len(p.Items))
	}
	s := p.Items[0]
	if _, err := w.Sessions.Update(ctx, humanP, s.ID, sessions.Patch{Revision: s.Revision}); func() bool { c, st, _ := pcode(err); return c != "VALIDATION_FAILED" || st != 422 }() {
		t.Errorf("empty patch: %v", err)
	}
	for _, name := range []string{"  ", strings.Repeat("n", 121)} {
		if _, err := w.Sessions.Update(ctx, humanP, s.ID, sessions.Patch{Revision: s.Revision, Name: &name}); func() bool { c, st, _ := pcode(err); return c != "VALIDATION_FAILED" || st != 422 }() {
			t.Errorf("name %d runes: %v", len(name), err)
		}
	}
	if _, err := w.Sessions.Update(ctx, humanP, s.ID, sessions.Patch{Revision: 9, Name: sp("x")}); func() bool {
		c, st, d := pcode(err)
		return c != "REVISION_CONFLICT" || st != 409 || d["current_revision"] != s.Revision
	}() {
		t.Errorf("stale: %v", err)
	}
	got, err := w.Sessions.Update(ctx, agentP, s.ID, sessions.Patch{Revision: s.Revision, Name: sp("  Renamed  ")})
	if err != nil || got.Name != "Renamed" || got.Revision != s.Revision+1 || got.UpdatedBy != agentP.Key() {
		t.Fatalf("rename: %+v %v", got, err)
	}
	// a renamed session keeps its name through later syncs
	w.Sc.RootInRun(w.Sc.Recipe("syn_late_v1", "pop"), "Synthetic Late", w.Str(`SELECT substr(source_key, 8) FROM sessions WHERE id = ?`, s.ID), w.Sc.At(6))
	w.Settle()
	if n := w.Str(`SELECT name FROM sessions WHERE id = ?`, s.ID); n != "Renamed" {
		t.Errorf("name after sync = %q", n)
	}
	if _, err := w.Sessions.Update(ctx, humanP, "nope", sessions.Patch{Revision: 1, Name: sp("x")}); func() bool { c, st, _ := pcode(err); return c != "NOT_FOUND" || st != 404 }() {
		t.Errorf("unknown: %v", err)
	}
	sum, err := w.Sessions.Summary(ctx, s.ID)
	if err != nil || sum.Takes != 3 || sum.Name != "Renamed" {
		t.Errorf("summary: %+v %v", sum, err)
	}
}
