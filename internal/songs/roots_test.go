package songs_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// partition renders the Songs of a world as sorted "roots of one Song" strings: the structure that must not depend on
// the order roots were reconciled in (ids of Songs differ, the grouping must not).
func partition(w *world) string {
	rows, err := w.st.R.Query(`SELECT song_id, root_track_id FROM song_roots ORDER BY song_id, root_track_id`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	groups := map[string][]string{}
	for rows.Next() {
		var s, r string
		if err := rows.Scan(&s, &r); err != nil {
			w.t.Fatal(err)
		}
		groups[s] = append(groups[s], r)
	}
	var out []string
	for _, g := range groups {
		sort.Strings(g)
		out = append(out, strings.Join(g, "+"))
	}
	sort.Strings(out)
	return strings.Join(out, " | ")
}

func TestSongs_RootsAttach_FromDraft_N3(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	rd := sc.Recipe("n3_v1", "pop")
	sc.Ref(rd, "i.csv", "Synthetic Muse 070", "", "variant-a")
	d := sc.Draft("n3_v1", librarytest.DraftOpts{Recipe: rd, Group: "n3", Label: "variant-a"})
	w.settle()
	s := w.liveSongOfDraft(d)
	title, muse, angle := w.str(`SELECT title FROM songs WHERE id = ?`, s), w.str(`SELECT muse_id FROM songs WHERE id = ?`, s), w.str(`SELECT angle FROM songs WHERE id = ?`, s)
	// from_draft n = 3: the draft seed plus two new motherless seeds, all roots, all produced by jobs of the draft
	batch := sc.Batch(`[{"item_index":0},{"item_index":0},{"item_index":0}]`, 3, "active")
	var roots []string
	for i := range 3 {
		r := rd
		if i > 0 {
			r = sc.Recipe(fmt.Sprintf("n3_s%d", i), "pop")
		}
		root := sc.Track(r, fmt.Sprintf("Seed %d", i), "", sc.At(2*i+2))
		job := sc.Job(batch, i, d, "", r)
		sc.JobProduced(job, root)
		if i == 0 {
			sc.Fulfil(d, root)
		}
		roots = append(roots, root)
	}
	w.settle()
	if w.liveSongs() != 1 {
		t.Fatalf("live songs = %d, want one Song for three seeds", w.liveSongs())
	}
	if n := w.count(`SELECT COUNT(*) FROM song_roots WHERE song_id = ?`, s); n != 3 {
		t.Errorf("song_roots = %d, want 3", n)
	}
	for _, r := range roots {
		if w.songOfTrack(r) != s {
			t.Errorf("take %s not in the draft Song", r)
		}
	}
	if w.str(`SELECT title FROM songs WHERE id = ?`, s) != title || w.str(`SELECT muse_id FROM songs WHERE id = ?`, s) != muse || w.str(`SELECT angle FROM songs WHERE id = ?`, s) != angle {
		t.Errorf("the draft Song's title, Muse and angle must stay untouched")
	}
	if n := w.count(`SELECT COUNT(*) FROM songs WHERE draft_id IS NULL`); n != 0 {
		t.Errorf("no extra Unassigned Song may appear: %d", n)
	}
	w.verify()
}

func TestSongs_RerenderFulfilledDraft(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	r := sc.Recipe("rr_v1", "pop")
	root := sc.Track(r, "First", "", sc.At(0))
	d := sc.Draft("rr_v1", librarytest.DraftOpts{Recipe: r, Fulfilled: root})
	w.settle()
	s := w.songOfTrack(root)
	// re-rendering the fulfilled draft allocates a new recipe and yields another root of the same Song
	r2 := sc.Recipe("rr_v1b", "pop")
	again := sc.Track(r2, "Again", "", sc.At(2))
	batch := sc.Batch(`[{"item_index":0}]`, 1, "active")
	sc.JobProduced(sc.Job(batch, 0, d, "", r2), again)
	w.settle()
	if w.songOfTrack(again) != s || w.liveSongs() != 1 {
		t.Errorf("the new root must join the draft's Song: %q vs %q, %d live", w.songOfTrack(again), s, w.liveSongs())
	}
	if via := w.str(`SELECT via FROM song_roots WHERE root_track_id = ?`, again); via != "draft" {
		t.Errorf("via = %q, want draft", via)
	}
	w.verify()
}

func TestSongs_ComposeNew_N3_OneSong(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	batch := sc.Batch(`[{"item_index":0},{"item_index":0},{"item_index":0}]`, 3, "active")
	for i := range 3 {
		r := sc.Recipe(fmt.Sprintf("cn_%d", i), "pop")
		sc.JobProduced(sc.Job(batch, i, "", "", r), sc.Track(r, fmt.Sprintf("Compose %d", i), "", sc.At(2*i)))
	}
	w.settle()
	if w.liveSongs() != 1 || w.count(`SELECT COUNT(*) FROM song_roots`) != 3 {
		t.Errorf("compose_new n = 3 must give one Song with 3 roots: %d Songs, %d roots", w.liveSongs(), w.count(`SELECT COUNT(*) FROM song_roots`))
	}
	if vias := w.str(`SELECT group_concat(via) FROM (SELECT via FROM song_roots ORDER BY via)`); vias != "job_item,job_item,own" {
		t.Errorf("vias = %q", vias)
	}
	w.verify()

	// two compose items of identical style and lyrics stay two Songs: the key is the item index, not a hash
	w2 := newWorld(t)
	sc2 := w2.sc
	b2 := sc2.Batch(`[{"item_index":0},{"item_index":1}]`, 2, "active")
	for i := range 2 {
		r := sc2.Recipe(fmt.Sprintf("same_%d", i), "pop, 100bpm")
		sc2.JobProduced(sc2.Job(b2, i, "", "", r), sc2.Track(r, "Same", "", sc2.At(2*i)))
	}
	w2.settle()
	if w2.liveSongs() != 2 {
		t.Errorf("two identical compose items must stay two Songs, got %d", w2.liveSongs())
	}
	w2.verify()
}

func TestSongs_RootsOrderIndependent(t *testing.T) {
	t.Parallel()
	build := func(eager bool) string {
		w := newWorld(t)
		sc := w.sc
		rd := sc.Recipe("oi_v1", "pop")
		d := sc.Draft("oi_v1", librarytest.DraftOpts{Recipe: rd})
		if eager {
			w.settle()
		}
		batch := sc.Batch(`[{"item_index":0},{"item_index":0},{"item_index":0}]`, 3, "active")
		var roots []string
		for i := range 3 {
			r := rd
			if i > 0 {
				r = sc.Recipe(fmt.Sprintf("oi_s%d", i), "pop")
			}
			roots = append(roots, sc.Track(r, fmt.Sprintf("S%d", i), "", sc.At(2*i)))
			sc.JobProduced(sc.Job(batch, i, d, "", r), roots[i])
			if eager {
				w.settle() // across passes, one root at a time
			}
		}
		sc.Fulfil(d, roots[0])
		w.settle()
		w.verify()
		// the primary root may differ with the order; the grouping of roots into Songs must not
		return partition(w)
	}
	if a, b := build(false), build(true); a != b {
		t.Errorf("order-dependent structure: %q vs %q", a, b)
	}
}

func TestSongs_TwoBacklogDraftsOneRoot(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	r := sc.Recipe("tb_v1", "pop")
	d1 := sc.Draft("tb_1", librarytest.DraftOpts{Recipe: r, Group: "tb", Label: "one"})
	r2 := sc.Recipe("tb_v2", "pop")
	d2 := sc.Draft("tb_2", librarytest.DraftOpts{Recipe: r2, Group: "tb", Label: "two"})
	w.settle()
	if w.liveSongs() != 2 {
		t.Fatalf("setup: %d live songs, want 2", w.liveSongs())
	}
	// one new root take fulfils both backlog drafts in one transaction (the importer's core-match flow)
	root := sc.Track(r, "Core", "", sc.At(0))
	sc.Fulfil(d1, root)
	sc.Fulfil(d2, root)
	w.settle()
	if n := w.liveSongs(); n != 1 {
		t.Fatalf("live songs = %d, want one Song per root", n)
	}
	if n := w.count(`SELECT COUNT(*) FROM songs WHERE retired_at IS NULL AND root_track_id IS NULL`); n != 0 {
		t.Errorf("a live rootless Song remains: %d", n)
	}
	w.verify()
}

func TestSongs_LateFulfilOfReconciledRoot(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	r := sc.Recipe("lf_v1", "pop")
	root := sc.Track(r, "Existing", "", sc.At(0))
	w.settle() // the root already has its own Song
	d := sc.Draft("lf_1", librarytest.DraftOpts{Recipe: r, Group: "lf", Label: "late"})
	w.settle() // and the draft has its Song
	if w.liveSongs() != 2 {
		t.Fatalf("setup: %d live songs, want 2", w.liveSongs())
	}
	sc.Fulfil(d, root)
	w.settle()
	if n := w.liveSongs(); n != 1 {
		t.Fatalf("live songs = %d, want 1 after the late fulfilment", n)
	}
	w.verify()
}
