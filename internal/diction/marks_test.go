package diction_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/diction"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func wantProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Errorf("error = %v, want %d %s", err, status, code)
	}
}

// TestDictionMarks_UpsertTwice: the same mark written twice is one row; its stamp strictly increases even when the clock stands
// still, and a changed mark replaces the old one; a human and a critic mark of one line are two rows.
func TestDictionMarks_UpsertTwice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addTake("[Verse]\nuno due\ntre\n[Chorus]\nquattro")
	ctx := context.Background()
	set := func(p auth.Principal, mark string) diction.TrackMarks {
		tm, err := e.svc.SetMarks(ctx, p, id, []diction.MarkIn{{SectionIdx: 0, LineIdx: 1, Mark: mark}})
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	a := set(alice, "clear")
	b := set(alice, "clear")
	if len(b.Marks) != 1 || b.Marks[0].UpdatedAt <= a.Marks[0].UpdatedAt {
		t.Errorf("stamps %q then %q: want a strict increase", a.Marks[0].UpdatedAt, b.Marks[0].UpdatedAt)
	}
	c := set(alice, "unclear")
	if c.Marks[0].Mark != "unclear" || c.Marks[0].UpdatedAt <= b.Marks[0].UpdatedAt {
		t.Errorf("marks = %+v", c.Marks)
	}
	d := set(robot, "clear")
	if len(d.Marks) != 2 || d.Marks[0].Source != "critic" || d.Marks[1].Source != "human" {
		t.Errorf("marks = %+v", d.Marks)
	}
	e.advance(5e9)
	if f := set(alice, "none"); f.Marks[1].Mark != "none" || f.Marks[1].UpdatedAt != "2026-11-21T10:00:05.000Z" {
		t.Errorf("marks = %+v", f.Marks)
	}
	// a device is a person too
	dev := auth.Principal{Kind: auth.KindDevice, ID: "d1", Name: "phone", User: "alice"}
	if g := set(dev, "clear"); g.Marks[1].Source != "human" {
		t.Errorf("a device marks as human: %+v", g.Marks)
	}
	var by string
	if err := e.st.R.QueryRow(`SELECT marked_by FROM diction_marks WHERE source = 'critic'`).Scan(&by); err != nil || by != "agent:tok1" {
		t.Errorf("marked_by = %q %v", by, err)
	}
	if tm, err := e.svc.Marks(ctx, id); err != nil || len(tm.Marks) != 2 {
		t.Errorf("Marks = %+v %v", tm, err)
	}
	if _, err := e.svc.Marks(ctx, "nope"); !errors.Is(err, diction.ErrNotFound) {
		t.Errorf("unknown take: %v", err)
	}
}

// TestSetMarks_Validation: unknown lines and sections are 422 and write nothing (the batch is atomic); a principal that cannot mark
// is refused; an unknown take is not found.
func TestSetMarks_Validation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addTake("[Verse]\nuno due\n[Outro]\n")
	ctx := context.Background()
	for _, bad := range []diction.MarkIn{{SectionIdx: 0, LineIdx: 1}, {SectionIdx: 1, LineIdx: 0}, {SectionIdx: 9, LineIdx: 0}} {
		bad.Mark = "clear"
		_, err := e.svc.SetMarks(ctx, alice, id, []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}, bad})
		wantProblem(t, err, 422, "VALIDATION_FAILED")
	}
	_, err := e.svc.SetMarks(ctx, alice, id, []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "loud"}})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	_, err = e.svc.SetMarks(ctx, auth.System("runner"), id, []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}})
	wantProblem(t, err, 403, "FORBIDDEN")
	if _, err := e.svc.SetMarks(ctx, alice, "nope", []diction.MarkIn{{Mark: "clear"}}); !errors.Is(err, diction.ErrNotFound) {
		t.Errorf("unknown take: %v", err)
	}
	var n int
	if err := e.st.R.QueryRow(`SELECT count(*) FROM diction_marks`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d marks written by refused requests", n)
	}
}

// TestLexiconOverrides_UpsertTwice: a patch keeps what it does not name, an empty note removes the note, the stamp strictly
// increases, and the word and language are validated.
func TestLexiconOverrides_UpsertTwice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.sixTakes()
	ctx := context.Background()
	st, note := ptr("ignored"), ptr("a name")
	a, err := e.svc.UpdateEntry(ctx, alice, "it", "chiave", diction.EntryPatch{Status: st, Note: note})
	if err != nil || a.Status != "ignored" || *a.Note != "a name" {
		t.Fatalf("a = %+v %v", a, err)
	}
	var s1, s2 string
	e.st.R.QueryRow(`SELECT updated_at FROM lexicon_overrides`).Scan(&s1)
	b, err := e.svc.UpdateEntry(ctx, alice, "it", "chiave", diction.EntryPatch{Note: ptr("")})
	if err != nil || b.Status != "ignored" || b.Note != nil {
		t.Fatalf("b = %+v %v", b, err)
	}
	e.st.R.QueryRow(`SELECT updated_at FROM lexicon_overrides`).Scan(&s2)
	if s2 <= s1 {
		t.Errorf("stamps %q then %q", s1, s2)
	}
	c, err := e.svc.UpdateEntry(ctx, alice, "it", "chiave", diction.EntryPatch{Status: ptr("active")})
	if err != nil || c.Status != "active" || !c.Risk {
		t.Fatalf("c = %+v %v", c, err)
	}
	// a word never heard is accepted when it is three letters or more, and answers zero attempts
	d, err := e.svc.UpdateEntry(ctx, alice, "it", "Tramonto", diction.EntryPatch{Status: ptr("ignored")})
	if err != nil || d.Word != "tramonto" || d.Attempts != 0 || d.Status != "ignored" {
		t.Errorf("d = %+v %v", d, err)
	}
	_, err = e.svc.UpdateEntry(ctx, alice, "it", "ma", diction.EntryPatch{Status: ptr("ignored")})
	wantProblem(t, err, 404, "NOT_FOUND")
	for _, f := range map[string]func() error{
		"bad lang": func() error {
			_, err := e.svc.UpdateEntry(ctx, alice, "ITALIAN", "mare", diction.EntryPatch{Status: st})
			return err
		},
		"two words": func() error {
			_, err := e.svc.UpdateEntry(ctx, alice, "it", "la mare", diction.EntryPatch{Status: st})
			return err
		},
		"empty word": func() error {
			_, err := e.svc.UpdateEntry(ctx, alice, "it", "''", diction.EntryPatch{Status: st})
			return err
		},
		"empty patch": func() error { _, err := e.svc.UpdateEntry(ctx, alice, "it", "mare", diction.EntryPatch{}); return err },
		"bad status": func() error {
			_, err := e.svc.UpdateEntry(ctx, alice, "it", "mare", diction.EntryPatch{Status: ptr("banned")})
			return err
		},
		"too long note": func() error {
			n := string(make([]rune, 501))
			_, err := e.svc.UpdateEntry(ctx, alice, "it", "mare", diction.EntryPatch{Note: &n})
			return err
		},
	} {
		wantProblem(t, f(), 422, "VALIDATION_FAILED")
	}
}

// TestLexicon_Filters: risk, q and paging.
func TestLexicon_Filters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.sixTakes()
	ctx := context.Background()
	yes, no := true, false
	r, _ := e.svc.Lexicon(ctx, diction.LexiconQuery{Risk: &yes})
	if r.Total != 1 || r.Entries[0].Word != "chiave" {
		t.Errorf("risk=true: %+v", r.Entries)
	}
	n, _ := e.svc.Lexicon(ctx, diction.LexiconQuery{Risk: &no})
	if n.Total != 1 || n.Entries[0].Word != "mare" {
		t.Errorf("risk=false: %+v", n.Entries)
	}
	q, _ := e.svc.Lexicon(ctx, diction.LexiconQuery{Q: "CHIA"})
	if q.Total != 1 {
		t.Errorf("q: %+v", q.Entries)
	}
	p1, err := e.svc.Lexicon(ctx, diction.LexiconQuery{Limit: 1})
	if err != nil || len(p1.Entries) != 1 || p1.NextCursor == nil || p1.Total != 2 {
		t.Fatalf("p1 = %+v %v", p1, err)
	}
	p2, err := e.svc.Lexicon(ctx, diction.LexiconQuery{Limit: 1, Cursor: *p1.NextCursor})
	if err != nil || len(p2.Entries) != 1 || p2.NextCursor != nil || p2.Entries[0].Word != "mare" {
		t.Errorf("p2 = %+v %v", p2, err)
	}
	_, err = e.svc.Lexicon(ctx, diction.LexiconQuery{Cursor: "garbage!"})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	_, err = e.svc.Lexicon(ctx, diction.LexiconQuery{Cursor: "bDo5OQ"}) // l:99
	wantProblem(t, err, 422, "VALIDATION_FAILED")
}
