package sequence_test

import (
	"context"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sequence"
)

// TestSuggest_HideDiscarded (U1-INT-B): for a viewer that cannot see discarded takes a Song whose takes are all discarded does
// not exist (UNKNOWN_SONG, the answer of an id that was never there, and absent from a Record's slots), and a slot whose
// resolved take is discarded carries no take title. Every other viewer is unchanged.
func TestSuggest_HideDiscarded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRig(t)
	r.seedTSet()
	rec := r.newRecord(shuffled...)
	// S5's takes are all discarded; another Song keeps a take while its Pick take alone is discarded
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id IN (SELECT track_id FROM take_songs WHERE song_id = ?)`, r.song("S5"))
	var pick string
	for _, l := range []string{"S1", "S2", "S3", "S4", "S6"} {
		var n int
		if err := r.st.R.QueryRow(`SELECT COUNT(*) FROM take_songs WHERE song_id = ?`, r.song(l)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n >= 2 && pick == "" {
			pick = r.pickOf(r.song(l))
		}
	}
	if pick == "" {
		t.Fatal("the fixture has no Song with two takes")
	}
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, pick)

	own, err := r.svc.Suggest(ctx, sequence.SuggestInput{RecordID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(own.Order) != 6 {
		t.Fatalf("the unrestricted plan lost a Song: %v", r.labels(own.Order))
	}
	hidden, err := r.svc.Suggest(ctx, sequence.SuggestInput{RecordID: rec.ID, HideDiscarded: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.labels(hidden.Order); slices.Contains(got, "S5") || len(got) != 5 {
		t.Errorf("a hidden Song is in the Listener's plan: %v", got)
	}
	var title string
	if err := r.st.R.QueryRow(`SELECT title FROM tracks WHERE id = ?`, pick).Scan(&title); err != nil {
		t.Fatal(err)
	}
	for _, st := range hidden.Steps {
		if st.TakeTitle == title {
			t.Errorf("a step carries the title of the discarded take %q", title)
		}
	}
	// by song ids: the hidden Song is as unknown as one that does not exist
	_, errHidden := r.svc.Suggest(ctx, sequence.SuggestInput{SongIDs: []string{r.song("S1"), r.song("S5")}, HideDiscarded: true})
	_, errNone := r.svc.Suggest(ctx, sequence.SuggestInput{SongIDs: []string{r.song("S1"), "01900000-0000-7000-8000-00000000dead"}, HideDiscarded: true})
	a, b := wantProblem(t, errHidden, 422, sequence.CodeUnknownSong), wantProblem(t, errNone, 422, sequence.CodeUnknownSong)
	if a.Message != b.Message {
		t.Errorf("messages differ: %q vs %q", a.Message, b.Message)
	}
	if _, err := r.svc.Suggest(ctx, sequence.SuggestInput{SongIDs: []string{r.song("S1"), r.song("S5")}}); err != nil {
		t.Errorf("the unrestricted plan refused a Song with only discarded takes: %v", err)
	}
}
