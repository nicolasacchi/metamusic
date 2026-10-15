package muses_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func TestMuses_CreateRules(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 201)
	brief2001 := strings.Repeat("b", 2001)
	rows := []struct {
		name   string
		p      auth.Principal
		in     muses.NewMuse
		code   string
		status int
		source string
	}{
		{"reference ok human", human, muses.NewMuse{Kind: muses.KindReference, Title: "Synthetic Muse 001", Artist: ptr("Synthetic Artist 001")}, "", 0, "human"},
		{"reference ok agent no artist", agent, muses.NewMuse{Kind: muses.KindReference, Title: "Synthetic Muse 002"}, "", 0, "agent"},
		{"brief ok", human, muses.NewMuse{Kind: muses.KindBrief, Title: "Synthetic Brief", Brief: ptr("a warm synthetic idea")}, "", 0, "human"},
		{"empty title", human, muses.NewMuse{Kind: muses.KindReference, Title: "   "}, "VALIDATION_FAILED", 422, ""},
		{"title too long", human, muses.NewMuse{Kind: muses.KindReference, Title: long}, "VALIDATION_FAILED", 422, ""},
		{"artist too long", human, muses.NewMuse{Kind: muses.KindReference, Title: "T", Artist: ptr(long)}, "VALIDATION_FAILED", 422, ""},
		{"empty artist", human, muses.NewMuse{Kind: muses.KindReference, Title: "T", Artist: ptr(" ")}, "VALIDATION_FAILED", 422, ""},
		{"brief on a reference", human, muses.NewMuse{Kind: muses.KindReference, Title: "T", Brief: ptr("x")}, "VALIDATION_FAILED", 422, ""},
		{"artist on a brief", human, muses.NewMuse{Kind: muses.KindBrief, Title: "T", Brief: ptr("x"), Artist: ptr("a")}, "VALIDATION_FAILED", 422, ""},
		{"brief missing", human, muses.NewMuse{Kind: muses.KindBrief, Title: "T"}, "VALIDATION_FAILED", 422, ""},
		{"brief too long", human, muses.NewMuse{Kind: muses.KindBrief, Title: "T", Brief: ptr(brief2001)}, "VALIDATION_FAILED", 422, ""},
		{"unknown kind", human, muses.NewMuse{Kind: "poem", Title: "T"}, "VALIDATION_FAILED", 422, ""},
	}
	w := newWorld(t)
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			m, err := w.svc.Create(context.Background(), r.p, r.in)
			c, s := code(err)
			if c != r.code || s != r.status {
				t.Fatalf("err = %v (%s %d), want %s %d", err, c, s, r.code, r.status)
			}
			if err != nil {
				return
			}
			if m.Source != r.source || m.Revision != 1 || m.CreatedBy != r.p.Key() || m.UpdatedBy != r.p.Key() || m.ID == "" {
				t.Errorf("muse = %+v", m)
			}
		})
	}
	t.Run("duplicate key is 409 with ids", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		a, err := w.svc.Create(context.Background(), human, muses.NewMuse{Kind: muses.KindReference, Title: "Synthetic  MUSE 001", Artist: ptr("Synthetic Artist")})
		if err != nil {
			t.Fatal(err)
		}
		_, err = w.svc.Create(context.Background(), agent, muses.NewMuse{Kind: muses.KindReference, Title: "synthetic muse 001", Artist: ptr("synthetic  artist")})
		var pe *problem.Error
		ok := errors.As(err, &pe)
		if !ok || pe.Code != "MUSE_EXISTS" || pe.Status != 409 || pe.Details["muse_id"] != a.ID || pe.Details["hidden"] != false {
			t.Fatalf("err = %#v", err)
		}
		if strings.Contains(pe.Message, "Synthetic") || strings.Contains(pe.Message, "synthetic") {
			t.Errorf("message names Muse text: %q", pe.Message)
		}
		hide := true
		if _, err := w.svc.Update(context.Background(), human, a.ID, muses.Patch{Revision: 1, Hidden: &hide}); err != nil {
			t.Fatal(err)
		}
		_, err = w.svc.Create(context.Background(), agent, muses.NewMuse{Kind: muses.KindReference, Title: "synthetic muse 001", Artist: ptr("synthetic artist")})
		if pe, ok := asProblem(err); !ok || pe.Details["hidden"] != true {
			t.Fatalf("hidden duplicate: %#v", err)
		}
	})
	t.Run("two briefs with one title coexist", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		for i := 0; i < 2; i++ {
			if _, err := w.svc.Create(context.Background(), human, muses.NewMuse{Kind: muses.KindBrief, Title: "Same", Brief: ptr("x")}); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestMuses_UpdateRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	a, _ := w.svc.Create(ctx, human, muses.NewMuse{Kind: muses.KindReference, Title: "Synthetic Alpha", Artist: ptr("Synthetic Artist")})
	b, _ := w.svc.Create(ctx, human, muses.NewMuse{Kind: muses.KindReference, Title: "Synthetic Beta"})
	br, _ := w.svc.Create(ctx, human, muses.NewMuse{Kind: muses.KindBrief, Title: "Synthetic Brief", Brief: ptr("idea")})

	if _, err := w.svc.Update(ctx, human, a.ID, muses.Patch{Revision: 1}); func() bool { c, s := code(err); return c != "VALIDATION_FAILED" || s != 422 }() {
		t.Errorf("empty patch: %v", err)
	}
	if _, err := w.svc.Update(ctx, human, a.ID, muses.Patch{Revision: 7, Title: ptr("x")}); err == nil {
		t.Error("stale revision accepted")
	} else if pe, ok := asProblem(err); !ok || pe.Code != "REVISION_CONFLICT" || pe.Status != 409 || pe.Details["current_revision"] != 1 {
		t.Errorf("stale revision: %#v", err)
	}
	// title change recomputes key and title_fold
	got, err := w.svc.Update(ctx, agent, a.ID, muses.Patch{Revision: 1, Title: ptr("Synthetic  Gamma")})
	if err != nil || got.Revision != 2 || got.Title != "Synthetic Gamma" || got.UpdatedBy != agent.Key() {
		t.Fatalf("update: %+v %v", got, err)
	}
	if k := w.str(`SELECT key FROM muses WHERE id = ?`, a.ID); k != muses.Key("Synthetic Gamma", ptr("Synthetic Artist")) {
		t.Errorf("key = %q", k)
	}
	if f := w.str(`SELECT title_fold FROM muses WHERE id = ?`, a.ID); f != "synthetic gamma" {
		t.Errorf("title_fold = %q", f)
	}
	// collision
	if _, err := w.svc.Update(ctx, human, b.ID, muses.Patch{Revision: 1, Title: ptr("synthetic gamma"), Artist: ptr("Synthetic Artist")}); func() bool { c, s := code(err); return c != "MUSE_EXISTS" || s != 409 }() {
		t.Errorf("collision: %v", err)
	}
	// kind is immutable: brief text on a reference and artist on a brief are refused
	if _, err := w.svc.Update(ctx, human, b.ID, muses.Patch{Revision: 1, Brief: ptr("x")}); func() bool { c, _ := code(err); return c != "VALIDATION_FAILED" }() {
		t.Errorf("brief on reference: %v", err)
	}
	if _, err := w.svc.Update(ctx, human, br.ID, muses.Patch{Revision: 1, Artist: ptr("x")}); func() bool { c, _ := code(err); return c != "VALIDATION_FAILED" }() {
		t.Errorf("artist on brief: %v", err)
	}
	if got, err := w.svc.Update(ctx, human, br.ID, muses.Patch{Revision: 1, Brief: ptr("new idea")}); err != nil || *got.Brief != "new idea" || got.Kind != "brief" {
		t.Errorf("brief edit: %+v %v", got, err)
	}
	// hide / unhide
	hide, show := true, false
	h, err := w.svc.Update(ctx, human, b.ID, muses.Patch{Revision: 1, Hidden: &hide})
	if err != nil || h.HiddenAt == nil || h.Revision != 2 {
		t.Fatalf("hide: %+v %v", h, err)
	}
	u, err := w.svc.Update(ctx, human, b.ID, muses.Patch{Revision: 2, Hidden: &show})
	if err != nil || u.HiddenAt != nil || u.Revision != 3 {
		t.Fatalf("unhide: %+v %v", u, err)
	}
	if _, err := w.svc.Update(ctx, human, "no-such", muses.Patch{Revision: 1, Title: ptr("x")}); func() bool { c, s := code(err); return c != "NOT_FOUND" || s != 404 }() {
		t.Errorf("unknown: %v", err)
	}
}

func TestMuses_AssignSongs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	ss := w.songs(4)
	m, _ := w.svc.Create(ctx, human, muses.NewMuse{Kind: muses.KindReference, Title: "Synthetic Alpha"})

	res, err := w.svc.AssignSongs(ctx, human, m.ID, ss[:2])
	if err != nil || res.Assigned != 2 || res.Unchanged != 0 {
		t.Fatalf("assign: %+v %v", res, err)
	}
	for _, s := range ss[:2] {
		if w.str(`SELECT muse_id FROM songs WHERE id = ?`, s) != m.ID || w.str(`SELECT muse_source FROM songs WHERE id = ?`, s) != "manual" ||
			w.str(`SELECT revision FROM songs WHERE id = ?`, s) != "2" {
			t.Errorf("song %s not assigned as manual with revision 2", s)
		}
	}
	// unchanged songs are reported, not rewritten
	res, err = w.svc.AssignSongs(ctx, agent, m.ID, ss[1:3])
	if err != nil || res.Assigned != 1 || res.Unchanged != 1 {
		t.Fatalf("assign 2: %+v %v", res, err)
	}
	if rev := w.str(`SELECT revision FROM songs WHERE id = ?`, ss[1]); rev != "2" {
		t.Errorf("unchanged song revision = %s, want 2", rev)
	}
	// all-or-nothing: an unknown Song fails the whole call
	if _, err := w.svc.AssignSongs(ctx, human, m.ID, []string{ss[3], "nope"}); func() bool { c, s := code(err); return c != "UNKNOWN_SONG" || s != 422 }() {
		t.Errorf("unknown song: %v", err)
	}
	if w.str(`SELECT muse_id FROM songs WHERE id = ?`, ss[3]) != "" {
		t.Error("partial assignment survived a failed call")
	}
	// retired
	rs := w.retiredSong(ss[0])
	if _, err := w.svc.AssignSongs(ctx, human, m.ID, []string{rs}); err == nil {
		t.Error("retired song accepted")
	} else if pe, ok := asProblem(err); !ok || pe.Code != "SONG_RETIRED" || pe.Status != 409 || pe.Details["retired_into"] != ss[0] {
		t.Errorf("retired: %#v", err)
	}
	// hidden Muse
	hide := true
	if _, err := w.svc.Update(ctx, human, m.ID, muses.Patch{Revision: 1, Hidden: &hide}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.AssignSongs(ctx, human, m.ID, ss[:1]); func() bool { c, s := code(err); return c != "MUSE_HIDDEN" || s != 409 }() {
		t.Errorf("hidden muse: %v", err)
	}
	// input validation
	for _, ids := range [][]string{nil, {ss[0], ss[0]}, make([]string, 201)} {
		if _, err := w.svc.AssignSongs(ctx, human, m.ID, ids); func() bool { c, _ := code(err); return c != "VALIDATION_FAILED" }() {
			t.Errorf("ids %d: %v", len(ids), err)
		}
	}
	if _, err := w.svc.AssignSongs(ctx, human, "nope", ss[:1]); func() bool { c, s := code(err); return c != "NOT_FOUND" || s != 404 }() {
		t.Errorf("unknown muse: %v", err)
	}
}

func TestMuses_ListFTS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	for _, in := range []muses.NewMuse{
		{Kind: muses.KindReference, Title: "Synthetic Alpha Sunrise", Artist: ptr("Synthetic Artist 001")},
		{Kind: muses.KindReference, Title: "Synthetic Beta", Artist: ptr("Other Name")},
		{Kind: muses.KindBrief, Title: "Synthetic Brief", Brief: ptr("sunset over water")},
	} {
		if _, err := w.svc.Create(ctx, human, in); err != nil {
			t.Fatal(err)
		}
	}
	n := func(f muses.ListFilter) int {
		p, err := w.svc.List(ctx, f)
		if err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		if p.Total != len(p.Items) {
			t.Errorf("%+v: total %d items %d", f, p.Total, len(p.Items))
		}
		return len(p.Items)
	}
	cases := []struct {
		q    string
		want int
	}{
		{"", 3}, {"alpha", 1}, {"alp", 1}, {"synthetic alpha sun", 1}, {"SUNSET", 1}, {"sun", 2}, {"other", 1},
		{`"`, 3}, {`NEAR(alpha beta)`, 0}, {`alpha"`, 1}, {`*`, 3}, {`alpha OR beta`, 0}, {"title:alpha", 0}, {`-alpha`, 1}, {"((", 3},
	}
	for _, c := range cases {
		if got := n(muses.ListFilter{Q: c.q}); got != c.want {
			t.Errorf("q=%q: %d rows, want %d", c.q, got, c.want)
		}
	}
	if n(muses.ListFilter{Kind: muses.KindBrief}) != 1 || n(muses.ListFilter{Kind: muses.KindReference}) != 2 {
		t.Error("kind filter")
	}
}

func TestMuses_ListPagingAndHidden(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	var ids []string
	for _, ti := range []string{"b", "A", "c", "D", "e"} {
		m, err := w.svc.Create(ctx, human, muses.NewMuse{Kind: muses.KindReference, Title: "Synthetic " + ti})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	hide := true
	if _, err := w.svc.Update(ctx, human, ids[2], muses.Patch{Revision: 1, Hidden: &hide}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	cur := ""
	for i := 0; i < 10; i++ {
		p, err := w.svc.List(ctx, muses.ListFilter{Limit: 2, Cursor: cur})
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 4 {
			t.Errorf("total = %d, want 4", p.Total)
		}
		for _, it := range p.Items {
			seen = append(seen, it.Title)
		}
		if p.NextCursor == nil {
			break
		}
		cur = *p.NextCursor
	}
	if strings.Join(seen, ",") != "Synthetic A,Synthetic b,Synthetic D,Synthetic e" {
		t.Errorf("order (case-insensitive keyset, hidden excluded) = %v", seen)
	}
	p, _ := w.svc.List(ctx, muses.ListFilter{IncludeHidden: true})
	if p.Total != 5 {
		t.Errorf("include_hidden total = %d", p.Total)
	}
	if _, err := w.svc.List(ctx, muses.ListFilter{Cursor: "!!!"}); err == nil {
		t.Error("malformed cursor accepted")
	}
}

func TestMuses_RefAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	m, _ := w.svc.Create(ctx, human, muses.NewMuse{Kind: muses.KindReference, Title: "Synthetic Alpha", Artist: ptr("Synthetic Artist")})
	r, ok, err := w.svc.Ref(ctx, m.ID)
	if err != nil || !ok || r.ID != m.ID || r.Kind != "reference" || r.Title != "Synthetic Alpha" || r.Artist == nil || *r.Artist != "Synthetic Artist" {
		t.Fatalf("ref: %+v %v %v", r, ok, err)
	}
	if _, ok, err := w.svc.Ref(ctx, "nope"); ok || err != nil {
		t.Errorf("unknown ref: %v %v", ok, err)
	}
	if g, err := w.svc.Get(ctx, m.ID); err != nil || g.ID != m.ID {
		t.Errorf("get: %v", err)
	}
	if _, err := w.svc.Get(ctx, "nope"); func() bool { c, s := code(err); return c != "NOT_FOUND" || s != 404 }() {
		t.Errorf("get unknown: %v", err)
	}
}

func TestMuses_KeyGolden(t *testing.T) {
	t.Parallel()
	a := muses.Key("Mixed CASE  title", ptr("Ärtist"))
	b := muses.Key("mixed case title", ptr("artist"))
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
	if muses.Key("t", nil) != muses.Key("t", ptr("")) || muses.Key("t", nil) == muses.Key("t", ptr("a")) {
		t.Error("nil / empty / present artist")
	}
}
