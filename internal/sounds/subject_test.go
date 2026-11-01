package sounds_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sounds"
)

func TestSubjectOf_Table(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t, true)
	subject := func(song string) sounds.Subject {
		t.Helper()
		got, err := sounds.SubjectOf(ctx, w.st.R, song)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// Pick take wins over a newer take.
	a := w.track(w.recipe("reggae", "x"), trackOpts{AgeMin: 10})
	b := w.track(w.recipe("pop", "x"), trackOpts{Parent: a, AgeMin: 5})
	w.settle()
	song := w.songOfTrack(a)
	if got := subject(song); got != (sounds.Subject{Kind: sounds.SubjectTake, ID: b}) {
		t.Errorf("no pick: %+v, want newest take %s", got, b)
	}
	w.pick(song, a)
	if got := subject(song); got != (sounds.Subject{Kind: sounds.SubjectTake, ID: a}) {
		t.Errorf("pick: %+v, want %s", got, a)
	}

	// Pick none: newest non-discarded take; an equal timestamp breaks by id descending.
	c := w.track(w.recipe("rock", "x"), trackOpts{AgeMin: 20})
	d := w.track(w.recipe("rock", "x"), trackOpts{Parent: c, AgeMin: 30})
	e := w.track(w.recipe("rock", "x"), trackOpts{Parent: c, AgeMin: 1, Status: "discarded"})
	w.settle()
	song2 := w.songOfTrack(c)
	if got := subject(song2); got != (sounds.Subject{Kind: sounds.SubjectTake, ID: c}) {
		t.Errorf("newest non-discarded: %+v, want %s (not %s discarded, not %s older)", got, c, e, d)
	}

	// Only discarded takes: falls to the draft when the Song has one.
	dr := w.draft("jazz")
	w.settle()
	song3 := w.songOfDraft(dr)
	if got := subject(song3); got != (sounds.Subject{Kind: sounds.SubjectDraft, ID: dr}) {
		t.Errorf("draft: %+v, want draft %s", got, dr)
	}
	tk := w.track(w.recipe("jazz", "x"), trackOpts{Status: "discarded"})
	w.exec(`INSERT INTO song_roots(root_track_id, song_id, via, attached_by, attached_at) VALUES(?, ?, 'draft', 'system:provenance', ?)`, tk, song3, "2026-10-06T12:00:00Z")
	if got := subject(song3); got != (sounds.Subject{Kind: sounds.SubjectDraft, ID: dr}) {
		t.Errorf("only discarded takes: %+v, want the draft", got)
	}

	// No draft and no take: none.
	root := w.track(w.recipe("folk", "x"), trackOpts{Status: "discarded"})
	song4 := w.song(root)
	if got := subject(song4); got.Kind != sounds.SubjectNone || got.ID != "" {
		t.Errorf("none: %+v", got)
	}
}
