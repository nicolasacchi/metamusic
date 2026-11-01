package sounds_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// ordRows returns "facet/value/ord" of a Song's rows ordered by ord.
func (w *world) ordRows(song string) []string {
	w.t.Helper()
	rows, err := w.st.R.Query(`SELECT facet || '/' || value || '/' || ord FROM song_sounds WHERE song_id = ? ORDER BY ord`, song)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func TestCompute_OrdPosition(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	tr := w.track(w.recipe("synthpop, reggae", "x"), trackOpts{})
	w.settle()
	song := w.songOfTrack(tr)
	want := []string{"engine/yue2/0", "genre_family/electronic/1", "genre_family/pop/2", "genre_family/reggae/3"}
	if got := w.ordRows(song); !reflect.DeepEqual(got, want) {
		t.Fatalf("ord rows = %v, want %v", got, want)
	}
	// The same (facet, value) emitted twice keeps its first position.
	tr2 := w.track(w.recipe("pop, synthpop, pop", "x"), trackOpts{})
	w.settle()
	want2 := []string{"engine/yue2/0", "genre_family/pop/1", "genre_family/electronic/2"}
	if got := w.ordRows(w.songOfTrack(tr2)); !reflect.DeepEqual(got, want2) {
		t.Errorf("duplicate emission: %v, want %v", got, want2)
	}
	// Tempo rows come last; ord is unique within the Song and contiguous from 0.
	tr3 := w.track(w.recipe("rock, male vocal, 100bpm", "x"), trackOpts{})
	w.settle()
	got := w.ordRows(w.songOfTrack(tr3))
	if want3 := []string{"engine/yue2/0", "genre_family/rock/1", "vocal/male/2", "tempo_band/mid/3"}; !reflect.DeepEqual(got, want3) {
		t.Errorf("tempo last: %v, want %v", got, want3)
	}
	// Take rows carry no ord column at all.
	if n := w.count(`SELECT COUNT(*) FROM pragma_table_info('take_sounds') WHERE name = 'ord'`); n != 0 {
		t.Error("take_sounds must not gain ord")
	}
}

func TestSound_PrimaryValue(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	tr := w.track(w.recipe("synthpop, reggae, male vocal", "x"), trackOpts{})
	w.settle()
	song := w.songOfTrack(tr)
	primary := func(facet string) string {
		return w.str(`SELECT value FROM song_sounds WHERE song_id = ? AND facet = ? ORDER BY ord LIMIT 1`, song, facet)
	}
	if got := primary("genre_family"); got != "electronic" {
		t.Errorf("primary genre_family = %q, want electronic (smallest ord: the taxonomy emits electronic before pop for synthpop)", got)
	}
	if got := primary("vocal"); got != "male" {
		t.Errorf("primary vocal = %q", got)
	}
	if n := w.count(`SELECT COUNT(*) FROM (SELECT ord FROM song_sounds WHERE song_id = ? GROUP BY ord HAVING COUNT(*) > 1)`, song); n != 0 {
		t.Errorf("%d tied ord values", n)
	}
}

func TestSound_DigestIncludesComputeDef(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t, true)
	tr := w.track(w.recipe("synthpop, reggae", "x"), trackOpts{})
	w.settle()
	song := w.songOfTrack(tr)
	ver := w.count(`SELECT MAX(version) FROM sound_taxonomy_versions`)

	digests := func() (take, songD string) {
		tx, err := w.st.W.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if take, err = sounds.TakeDigestOf(ctx, tx, ver, tr); err != nil {
			t.Fatal(err)
		}
		if songD, err = sounds.CurrentSongDigest(ctx, tx, ver, song); err != nil {
			t.Fatal(err)
		}
		legacy, err := sounds.LegacySongDigest(ctx, tx, ver, song)
		if err != nil {
			t.Fatal(err)
		}
		if legacy == songD {
			t.Fatal("the Song digest does not include the compute definition")
		}
		// A Song computed under /1 carries the legacy digest: plant it, with every row at ord 0.
		if _, err := tx.Exec(`UPDATE sound_state SET input_digest = ? WHERE subject_kind = 'song' AND subject_id = ?`, legacy, song); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE song_sounds SET ord = 0 WHERE song_id = ?`, song); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return take, songD
	}
	takeD, songD := digests()
	if sounds.SoundComputeDef != "mm-sound-compute/2" {
		t.Errorf("compute def = %q", sounds.SoundComputeDef)
	}
	// Take digests are byte-identical to L1/L2 (a literal captured before the amendment).
	if want := "977907219b90f46dcf495f6962343308ce8765f62285d12442302c43b3ca9912"; takeD != want {
		t.Errorf("take digest = %s, want %s", takeD, want)
	}
	// One pass rewrites the Song once and the take not at all; a second pass writes nothing.
	run := func() int {
		tx, err := w.st.W.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		n, err := w.sv.SweepStaleTx(ctx, tx, soundsEnv(w), 100)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := run(); n != 1 {
		t.Errorf("first pass rewrote %d subjects, want 1", n)
	}
	if got := w.str(`SELECT input_digest FROM sound_state WHERE subject_kind = 'song' AND subject_id = ?`, song); got != songD {
		t.Errorf("stored digest = %s, want %s", got, songD)
	}
	if got := w.ordRows(song); len(got) != 4 || got[3] != "genre_family/reggae/3" {
		t.Errorf("ord not rewritten: %v", got)
	}
	if n := run(); n != 0 {
		t.Errorf("second pass rewrote %d subjects, want 0", n)
	}
}
