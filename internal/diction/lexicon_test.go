package diction_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/diction"
)

func entry(t *testing.T, e *env, word string) (diction.LexiconEntry, bool) {
	t.Helper()
	page, err := e.svc.Lexicon(context.Background(), diction.LexiconQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range page.Entries {
		if en.Word == word {
			return en, true
		}
	}
	return diction.LexiconEntry{}, false
}

// TestLexicon_Aggregate is the C344.3 corpus: six takes of "la chiave del mare", four heard "kiave", one right, one dropped.
func TestLexicon_Aggregate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.sixTakes()
	c, ok := entry(t, e, "chiave")
	if !ok {
		t.Fatal("chiave is not in the lexicon")
	}
	if c.Attempts != 6 || c.Mangled != 4 || c.Dropped != 1 || c.Rate != 0.667 || !c.Risk || c.Tracks != 6 || c.Status != "active" {
		t.Errorf("chiave = %+v, want attempts 6, mangled 4, dropped 1, rate 0.667, risk", c)
	}
	if diff := cmp.Diff([]diction.HeardCount{{Text: "kiave", N: 4}}, c.TypicalHeard); diff != "" {
		t.Errorf("typical_heard (-want +got):\n%s", diff)
	}
	if len(c.Examples) != 3 || c.Examples[0].Heard != "kiave" || c.Examples[0].TrackID != ids[0] {
		t.Errorf("examples = %+v", c.Examples)
	}
	m, _ := entry(t, e, "mare")
	if m.Attempts != 6 || m.Mangled != 1 || m.Rate != 0.167 || m.Risk {
		t.Errorf("mare = %+v, want 6/1/0.167, no risk", m)
	}

	// a human clear on one kiave line removes that line from the counts: 5 / 3 / 0.6
	if _, err := e.svc.SetMarks(context.Background(), alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}}); err != nil {
		t.Fatal(err)
	}
	c, _ = entry(t, e, "chiave")
	if c.Attempts != 5 || c.Mangled != 3 || c.Rate != 0.6 || !c.Risk {
		t.Errorf("after a human clear: chiave = %+v, want 5/3/0.6", c)
	}

	// an ignored word is no risk, and the note comes back
	got, err := e.svc.UpdateEntry(context.Background(), alice, "it", "Chiave", diction.EntryPatch{Status: ptr("ignored"), Note: ptr("a name")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ignored" || got.Risk || got.Note == nil || *got.Note != "a name" || got.Attempts != 5 {
		t.Errorf("after the override: %+v", got)
	}
}

func ptr[T any](v T) *T { return &v }

// TestLexicon_SkipsStopwords: la and del are Italian function words and short words are not counted; a word of three runes is.
func TestLexicon_SkipsStopwords(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.sixTakes()
	for _, w := range []string{"la", "del"} {
		if _, ok := entry(t, e, w); ok {
			t.Errorf("%s is listed", w)
		}
	}
	id := e.addTake("[Verse]\nsole di me")
	e.addDiction(id, "[Verse]\nsole di me", diction2Opts())
	e.advance(6 * time.Second) // past the rebuild window
	if _, ok := entry(t, e, "di"); ok {
		t.Error("a two-letter word is listed")
	}
	if _, ok := entry(t, e, "sole"); !ok {
		t.Error("sole is not listed")
	}
}

// TestLexicon_LanguageKeyed: the same fold under two languages is two entries, and the stop word list is Italian only.
func TestLexicon_LanguageKeyed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.addTake("[Verse]\nche cosa")
	e.addDiction(a, "[Verse]\nche cosa", diction2Opts())
	b := e.addTake("[Verse]\nche cosa")
	o := diction2Opts()
	o.Lang = "en"
	e.addDiction(b, "[Verse]\nche cosa", o)
	page, err := e.svc.Lexicon(context.Background(), diction.LexiconQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	langs := map[string]int{}
	for _, en := range page.Entries {
		langs[en.Word+"/"+en.Lang]++
	}
	if langs["cosa/it"] != 1 || langs["cosa/en"] != 1 || langs["che/it"] != 0 || langs["che/en"] != 1 {
		t.Errorf("entries = %v", langs)
	}
	it, err := e.svc.Lexicon(context.Background(), diction.LexiconQuery{Lang: "it"})
	if err != nil || it.Total != 1 || it.Calibration.Lang != "it" {
		t.Errorf("lang filter: %+v %v", it, err)
	}
}

// TestLexicon_CacheInvalidatesOnMarkUpdate: a write through the service shows at once; new Tier B rows wait for the rebuild window.
func TestLexicon_CacheInvalidatesOnMarkUpdate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.sixTakes()
	c, _ := entry(t, e, "chiave")
	if c.Attempts != 6 {
		t.Fatalf("attempts = %d", c.Attempts)
	}
	if _, err := e.svc.SetMarks(context.Background(), alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}}); err != nil {
		t.Fatal(err)
	}
	if c, _ = entry(t, e, "chiave"); c.Attempts != 5 {
		t.Errorf("after the mark: attempts = %d, want 5", c.Attempts)
	}
	// withdrawing the mark (none) brings the line back
	if _, err := e.svc.SetMarks(context.Background(), alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "none"}}); err != nil {
		t.Fatal(err)
	}
	if c, _ = entry(t, e, "chiave"); c.Attempts != 6 {
		t.Errorf("after none: attempts = %d, want 6", c.Attempts)
	}
	// a new Tier B row is picked up only after the rebuild window
	id := e.addTake(chiave)
	e.addDiction(id, chiave, diction2Opts())
	if c, _ = entry(t, e, "chiave"); c.Attempts != 6 {
		t.Errorf("inside the window: attempts = %d, want 6 (cached)", c.Attempts)
	}
	e.advance(6 * time.Second)
	if c, _ = entry(t, e, "chiave"); c.Attempts != 7 {
		t.Errorf("after the window: attempts = %d, want 7", c.Attempts)
	}
}

// TestMarks_CriticIsolation: an agent's marks are stored as critic and change neither the lexicon nor the calibration; a person's
// do, and a human mark of none is withdrawn, not a vote.
func TestMarks_CriticIsolation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.sixTakes()
	ctx := context.Background()
	tm, err := e.svc.SetMarks(ctx, robot, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.Marks) != 1 || tm.Marks[0].Source != "critic" {
		t.Fatalf("marks = %+v", tm.Marks)
	}
	if c, _ := entry(t, e, "chiave"); c.Attempts != 6 {
		t.Errorf("a critic mark changed the lexicon: %+v", c)
	}
	page, _ := e.svc.Lexicon(ctx, diction.LexiconQuery{})
	if page.Calibration != (diction.Calibration{Lang: "all"}) {
		t.Errorf("calibration = %+v after a critic mark", page.Calibration)
	}
	lines, err := e.svc.Lines(ctx, ids[0], "")
	if err != nil {
		t.Fatal(err)
	}
	if l := lines.Lines[0]; l.Mark.Critic == nil || *l.Mark.Critic != "clear" || l.Mark.Human != nil || l.ClassSource != "asr" {
		t.Errorf("line after a critic mark = %+v", l)
	}
	if _, err := e.svc.SetMarks(ctx, alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "unclear"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetMarks(ctx, alice, ids[1], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}}); err != nil {
		t.Fatal(err)
	}
	page, _ = e.svc.Lexicon(ctx, diction.LexiconQuery{Lang: "it"})
	if want := (diction.Calibration{LinesMarked: 2, TracksMarked: 2, Clear: 1, Unclear: 1, Lang: "it"}); page.Calibration != want {
		t.Errorf("calibration = %+v, want %+v", page.Calibration, want)
	}
}

func TestBuildLexicon_Deterministic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.sixTakes()
	a, _ := e.svc.Lexicon(context.Background(), diction.LexiconQuery{Limit: 200})
	b, _ := e.svc.Lexicon(context.Background(), diction.LexiconQuery{Limit: 200})
	if diff := cmp.Diff(a, b); diff != "" {
		t.Error(diff)
	}
	if len(a.Entries) == 0 || !a.Entries[0].Risk || a.Entries[0].Word != "chiave" {
		t.Errorf("risk entries sort first: %+v", a.Entries)
	}
}

// TestLexicon_RewriteOlderMarkWithFrozenClock: rewriting an older mark while a newer one exists must still invalidate the cache.
func TestLexicon_RewriteOlderMarkWithFrozenClock(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.sixTakes()
	ctx := context.Background()
	if _, err := e.svc.SetMarks(ctx, alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetMarks(ctx, alice, ids[1], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "clear"}}); err != nil {
		t.Fatal(err)
	}
	if c, _ := entry(t, e, "chiave"); c.Attempts != 4 {
		t.Fatalf("attempts = %d, want 4", c.Attempts)
	}
	if _, err := e.svc.SetMarks(ctx, alice, ids[0], []diction.MarkIn{{SectionIdx: 0, LineIdx: 0, Mark: "none"}}); err != nil {
		t.Fatal(err)
	}
	if c, _ := entry(t, e, "chiave"); c.Attempts != 5 {
		t.Errorf("after rewriting the older mark: attempts = %d, want 5", c.Attempts)
	}
}

// TestLexicon_ExcludesDiscardedTakes: a discarded take does not exist for an Editor, and the lexicon is one shared aggregate, so a
// discarded take contributes no attempts and no examples (once the rebuild window has passed).
func TestLexicon_ExcludesDiscardedTakes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.sixTakes()
	if c, _ := entry(t, e, "chiave"); c.Attempts != 6 {
		t.Fatalf("attempts = %d", c.Attempts)
	}
	e.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, ids[0])
	e.advance(6 * time.Second) // the rebuild window
	c, _ := entry(t, e, "chiave")
	if c.Attempts != 5 {
		t.Errorf("after discarding: attempts = %d, want 5", c.Attempts)
	}
	for _, ex := range c.Examples {
		if ex.TrackID == ids[0] {
			t.Errorf("example from the discarded take: %+v", ex)
		}
	}
}
