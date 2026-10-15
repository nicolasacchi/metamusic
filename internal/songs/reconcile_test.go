package songs_test

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func TestSongs_ReconcileTracks(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	r1 := sc.Recipe("a_v1", "pop")
	root := sc.Track(r1, "Root Title", "", sc.At(0))
	w.settle()
	if n := w.liveSongs(); n != 1 {
		t.Fatalf("a fresh root gives %d Songs, want 1", n)
	}
	if got := w.str(`SELECT origin||'|'||title||'|'||COALESCE(draft_id,'-') FROM songs`); got != "root|Root Title|-" {
		t.Errorf("root Song = %q", got)
	}
	child := sc.Track(r1, "Child", root, sc.At(2))
	w.settle()
	if w.liveSongs() != 1 {
		t.Errorf("a child must not create a Song")
	}
	if a, b := w.songOfTrack(root), w.songOfTrack(child); a == "" || a != b {
		t.Errorf("take_songs maps root and child to %q and %q", a, b)
	}

	// a rootless draft Song whose draft a root then fulfils gains its root (id, Muse and revision unchanged)
	rd := sc.Recipe("d_v1", "pop")
	sc.Ref(rd, "i.csv", "Synthetic Muse 001", "Synthetic Artist 001", "variant-a")
	d := sc.Draft("d_v1", librarytest.DraftOpts{Recipe: rd, Group: "d", Label: "variant-a"})
	w.settle()
	s := w.liveSongOfDraft(d)
	if s == "" || w.str(`SELECT COALESCE(root_track_id,'') FROM songs WHERE id = ?`, s) != "" {
		t.Fatalf("draft Song %q should have no root yet", s)
	}
	muse, rev := w.str(`SELECT muse_id FROM songs WHERE id = ?`, s), w.str(`SELECT revision FROM songs WHERE id = ?`, s)
	if muse == "" {
		t.Fatal("the draft Song must carry the Muse of its ref")
	}
	t2 := sc.Track(rd, "Rendered", "", sc.At(4))
	sc.Fulfil(d, t2)
	w.settle()
	if got := w.str(`SELECT root_track_id FROM songs WHERE id = ?`, s); got != t2 {
		t.Errorf("the draft Song's root = %q, want %q", got, t2)
	}
	if w.str(`SELECT muse_id FROM songs WHERE id = ?`, s) != muse || w.str(`SELECT revision FROM songs WHERE id = ?`, s) != rev {
		t.Errorf("attaching a root changed the Muse or the revision")
	}
	if w.songOfTrack(t2) != s || w.liveSongs() != 2 {
		t.Errorf("the rendered take must belong to the draft Song; live songs = %d", w.liveSongs())
	}
	rep := w.settle() // a second pass changes nothing
	if rep.SongsCreated+rep.SongsAttached+rep.SongsRetired+rep.MusesCreated != 0 {
		t.Errorf("second pass: %+v", rep)
	}
	w.verify()
}

func TestSongs_ReconcileDrafts_Table(t *testing.T) {
	t.Parallel()
	t.Run("backlog motherless draft", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		r := sc.Recipe("ex_001_v1", "pop")
		sc.Ref(r, "i.csv", "Synthetic Muse 001", "Synthetic Artist 001", "variant-a")
		d := sc.Draft("ex_001_v1", librarytest.DraftOpts{Recipe: r, Group: "ex_001", Label: "variant-a"})
		w.settle()
		s := w.liveSongOfDraft(d)
		if got := w.str(`SELECT origin||'|'||title||'|'||angle||'|'||angle_source||'|'||muse_source FROM songs WHERE id = ?`, s); got != "draft|Ex 001 · variant a|variant-a|reference|reference" {
			t.Errorf("draft Song = %q", got)
		}
		if w.str(`SELECT muse_id FROM songs WHERE id = ?`, s) == "" {
			t.Errorf("no Muse")
		}
		w.verify()
	})
	t.Run("fulfilled by a root attaches or creates with both", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		r := sc.Recipe("f_v1", "pop")
		root := sc.Track(r, "T", "", sc.At(0))
		d := sc.Draft("f_v1", librarytest.DraftOpts{Recipe: r, Fulfilled: root})
		w.settle()
		if w.liveSongs() != 1 {
			t.Fatalf("live songs = %d, want 1 (the draft attaches to the root's Song)", w.liveSongs())
		}
		if got := w.str(`SELECT COALESCE(draft_id,'')||'|'||root_track_id FROM songs`); got != d+"|"+root {
			t.Errorf("Song = %q, want draft %s and root %s", got, d, root)
		}
		w.verify()
	})
	t.Run("fulfilled by a child retires an existing takes-less Song", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		rr := sc.Recipe("m_v1", "pop")
		root := sc.Track(rr, "Mother", "", sc.At(0))
		rd := sc.Recipe("c_v1", "pop")
		d := sc.Draft("c_v1", librarytest.DraftOpts{Recipe: rd})
		w.settle()
		sd := w.liveSongOfDraft(d)
		if sd == "" || w.liveSongs() != 2 {
			t.Fatalf("setup: draft Song %q, live %d", sd, w.liveSongs())
		}
		child := sc.Track(rd, "Child", root, sc.At(2))
		sc.Fulfil(d, child)
		rep := w.settle()
		if rep.SongsRetired != 1 || w.liveSongs() != 1 {
			t.Errorf("report %+v, live songs %d", rep, w.liveSongs())
		}
		if got := w.str(`SELECT retired_into FROM songs WHERE id = ?`, sd); got != w.songOfTrack(root) {
			t.Errorf("retired_into = %q, want the mother's Song", got)
		}
		w.verify()
	})
	t.Run("draft fulfilled by a child with no Song yet", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		rr := sc.Recipe("m_v1", "pop")
		root := sc.Track(rr, "Mother", "", sc.At(0))
		child := sc.Track(rr, "Child", root, sc.At(2))
		sc.Draft("c_v1", librarytest.DraftOpts{Fulfilled: child})
		w.settle()
		if w.liveSongs() != 1 {
			t.Errorf("live songs = %d, want only the root's", w.liveSongs())
		}
		w.verify()
	})
	t.Run("inserted with a mother has no Song", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		root := sc.Track(sc.Recipe("m_v1", "pop"), "Mother", "", sc.At(0))
		sc.Draft("v_v1", librarytest.DraftOpts{Parent: root})
		w.settle()
		if w.liveSongs() != 1 || w.count(`SELECT COUNT(*) FROM songs WHERE origin = 'draft'`) != 0 {
			t.Errorf("a mothered draft must not get a Song of its own")
		}
		w.verify()
	})
	t.Run("gains a mother later then loses it", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		root := sc.Track(sc.Recipe("m_v1", "pop"), "Mother", "", sc.At(0))
		rd := sc.Recipe("g_v1", "pop")
		sc.Ref(rd, "i.csv", "Synthetic Muse 002", "", "variant-b")
		d := sc.Draft("g_v1", librarytest.DraftOpts{Recipe: rd})
		w.settle()
		old := w.liveSongOfDraft(d)
		museOld := w.str(`SELECT muse_id FROM songs WHERE id = ?`, old)
		sc.SetMother(d, root)
		w.settle()
		if w.str(`SELECT retired_at FROM songs WHERE id = ?`, old) == "" || w.str(`SELECT retired_into FROM songs WHERE id = ?`, old) != w.songOfTrack(root) {
			t.Errorf("the draft Song must be retired into the mother's Song")
		}
		if w.str(`SELECT muse_id FROM songs WHERE id = ?`, old) != museOld {
			t.Errorf("the retired Song's Muse must be untouched")
		}
		if w.liveSongOfDraft(d) != "" {
			t.Errorf("no live Song for a mothered draft")
		}
		sc.SetMother(d, "") // the mother is removed: a new Song, the retired row stays
		w.settle()
		now := w.liveSongOfDraft(d)
		if now == "" || now == old {
			t.Errorf("a new live Song is expected, got %q (old %q)", now, old)
		}
		if w.str(`SELECT retired_at FROM songs WHERE id = ?`, old) == "" {
			t.Errorf("the retired row must stay retired")
		}
		w.verify()
	})
	t.Run("two drafts fulfilled by one root", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		r := sc.Recipe("t_v1", "pop")
		root := sc.Track(r, "T", "", sc.At(0))
		d1 := sc.Draft("t_v1", librarytest.DraftOpts{Recipe: r, Fulfilled: root})
		d2 := sc.Draft("t_v2", librarytest.DraftOpts{Fulfilled: root})
		w.settle()
		if w.liveSongs() != 1 {
			t.Fatalf("live songs = %d, want 1", w.liveSongs())
		}
		attached := w.str(`SELECT draft_id FROM songs`)
		if attached != d1 && attached != d2 {
			t.Errorf("the Song's draft = %q", attached)
		}
		w.verify()
	})
	t.Run("a human edit survives later passes", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		r := sc.Recipe("h_v1", "pop")
		sc.Ref(r, "i.csv", "Synthetic Muse 003", "Synthetic Artist 003", "variant-a")
		d := sc.Draft("h_v1", librarytest.DraftOpts{Recipe: r, Label: "variant-a"})
		w.settle()
		s := w.liveSongOfDraft(d)
		w.sc.TB.Helper()
		if _, err := w.st.W.Exec(`INSERT INTO muses(id,kind,title,title_fold,brief,key,source,created_by,created_at,updated_by,updated_at)
			VALUES('mx','brief','Synthetic Brief','synthetic brief','a synthetic brief','brief:mx','human','human:h',?,'human:h',?)`, sc.At(0), sc.At(0)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.st.W.Exec(`UPDATE songs SET muse_id='mx', muse_source='manual', title='My name', angle='mine', angle_source='manual', revision = revision + 1 WHERE id = ?`, s); err != nil {
			t.Fatal(err)
		}
		if _, err := w.st.W.Exec(`UPDATE recipe_drafts SET label = 'variant-b' WHERE id = ?`, d); err != nil { // dirty the draft again
			t.Fatal(err)
		}
		w.settle()
		if got := w.str(`SELECT muse_id||'|'||title||'|'||angle FROM songs WHERE id = ?`, s); got != "mx|My name|mine" {
			t.Errorf("a human edit was re-derived: %q", got)
		}
		w.verify()
	})
}

func TestAngle_RequestIDSuffix_Unambiguous(t *testing.T) {
	t.Parallel()
	t.Run("all refs agree", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		for _, id := range []string{"a_v2", "b_v2"} {
			r := sc.Recipe(id, "pop")
			sc.Ref(r, "i.csv", "Synthetic Muse 010", "", "variant-b")
			sc.Draft(id, librarytest.DraftOpts{Recipe: r})
		}
		bare := sc.Draft("c_v2", librarytest.DraftOpts{})
		w.settle()
		if got := w.str(`SELECT angle||'|'||angle_source FROM songs WHERE draft_id = ?`, bare); got != "variant-b|request_id" {
			t.Errorf("the draft without a ref got %q", got)
		}
	})
	t.Run("two types for one suffix give none", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		for i, vt := range []string{"variant-b", "variant-c"} {
			id := []string{"a_v2", "b_v2"}[i]
			r := sc.Recipe(id, "pop")
			sc.Ref(r, "i.csv", "Synthetic Muse 011", "", vt)
			sc.Draft(id, librarytest.DraftOpts{Recipe: r})
		}
		bare := sc.Draft("c_v2", librarytest.DraftOpts{})
		w.settle()
		if got := w.str(`SELECT COALESCE(angle,'none') FROM songs WHERE draft_id = ?`, bare); got != "none" {
			t.Errorf("an ambiguous suffix must give no angle, got %q", got)
		}
	})
	t.Run("two digit suffix", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		sc := w.sc
		r := sc.Recipe("p_v10", "pop")
		sc.Ref(r, "i.csv", "Synthetic Muse 012", "", "variant-z")
		sc.Draft("p_v10", librarytest.DraftOpts{Recipe: r})
		bare := sc.Draft("q_v10", librarytest.DraftOpts{})
		w.settle()
		if got := w.str(`SELECT angle FROM songs WHERE draft_id = ?`, bare); got != "variant-z" {
			t.Errorf("_v10 suffix gave %q", got)
		}
	})
}

func TestAngle_FromRef(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	r := sc.Recipe("x_v1", "pop")
	sc.Ref(r, "i.csv", "Synthetic Muse 020", "", "Remake Long")
	d := sc.Draft("x_v1", librarytest.DraftOpts{Recipe: r})
	w.settle()
	if got := w.str(`SELECT angle||'|'||angle_source FROM songs WHERE draft_id = ?`, d); got != "remake-long|reference" {
		t.Errorf("angle = %q", got)
	}
}

func TestMuse_RootSongFromRecipeRefs(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	r := sc.Recipe("v_v1", "pop")
	sc.Ref(r, "i.csv", "Synthetic Muse 030", "Synthetic Artist 030", "variant-a")
	root := sc.Track(r, "Rendered varianti", "", sc.At(0))
	w.settle()
	s := w.songOfTrack(root)
	if w.str(`SELECT muse_source FROM songs WHERE id = ?`, s) != "reference" || w.str(`SELECT muse_id FROM songs WHERE id = ?`, s) == "" {
		t.Errorf("a root whose recipe carries an index row must get its Muse without a draft")
	}
	if w.str(`SELECT COALESCE(angle,'none') FROM songs WHERE id = ?`, s) != "none" {
		t.Errorf("root Songs get no angle")
	}
}

func TestMuse_LateRefReachesSong(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	r := sc.Recipe("l_v1", "pop")
	d := sc.Draft("l_v1", librarytest.DraftOpts{Recipe: r})
	w.settle()
	s := w.liveSongOfDraft(d)
	if w.str(`SELECT COALESCE(muse_id,'none') FROM songs WHERE id = ?`, s) != "none" {
		t.Fatal("no Muse expected before the ref")
	}
	sc.Ref(r, "i.csv", "Synthetic Muse 040", "", "variant-c") // the index row arrives late; pd_refs_ai re-queues the draft
	w.settle()
	if w.str(`SELECT muse_source||'|'||angle FROM songs WHERE id = ?`, s) != "reference|variant-c" {
		t.Errorf("the late ref did not reach the Song")
	}
}

func TestMuse_ClearedNeverRefilled(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	r := sc.Recipe("c_v1", "pop")
	d := sc.Draft("c_v1", librarytest.DraftOpts{Recipe: r})
	w.settle()
	s := w.liveSongOfDraft(d)
	if _, err := w.st.W.Exec(`UPDATE songs SET muse_id = NULL, muse_source = 'manual', angle = NULL, angle_source = 'cleared' WHERE id = ?`, s); err != nil {
		t.Fatal(err)
	}
	sc.Ref(r, "i.csv", "Synthetic Muse 050", "", "variant-a")
	w.settle()
	if got := w.str(`SELECT COALESCE(muse_id,'none')||'|'||COALESCE(angle,'none') FROM songs WHERE id = ?`, s); got != "none|none" {
		t.Errorf("an operator-cleared Muse and angle were refilled: %q", got)
	}
}

func TestSongs_RetireCarriesMuse(t *testing.T) {
	t.Parallel()
	for _, targetHasMuse := range []bool{false, true} {
		w := newWorld(t)
		sc := w.sc
		rr := sc.Recipe("m_v1", "pop")
		if targetHasMuse {
			sc.Ref(rr, "i.csv", "Synthetic Muse 060", "", "variant-a")
		}
		root := sc.Track(rr, "Mother", "", sc.At(0))
		rd := sc.Recipe("d_v1", "pop")
		sc.Ref(rd, "i.csv", "Synthetic Muse 061", "", "variant-b")
		d := sc.Draft("d_v1", librarytest.DraftOpts{Recipe: rd})
		w.settle()
		sd, sr := w.liveSongOfDraft(d), w.songOfTrack(root)
		museD := w.str(`SELECT muse_id FROM songs WHERE id = ?`, sd)
		museR, revR := w.str(`SELECT COALESCE(muse_id,'none') FROM songs WHERE id = ?`, sr), w.str(`SELECT revision FROM songs WHERE id = ?`, sr)
		sc.SetMother(d, root)
		w.settle()
		gotMuse, gotRev := w.str(`SELECT COALESCE(muse_id,'none') FROM songs WHERE id = ?`, sr), w.str(`SELECT revision FROM songs WHERE id = ?`, sr)
		if targetHasMuse {
			if gotMuse != museR || gotRev != revR {
				t.Errorf("a target with a Muse must keep it: %s/%s -> %s/%s", museR, revR, gotMuse, gotRev)
			}
			if strings.Contains(w.log.String(), "song_muse_carried") {
				t.Errorf("nothing was carried, yet it was logged")
			}
			continue
		}
		if gotMuse != museD || gotRev == revR {
			t.Errorf("the target must gain the retired Song's Muse and a revision bump: %s/%s -> %s/%s (want %s)", museR, revR, gotMuse, gotRev, museD)
		}
		if got := w.log.Count("song_muse_carried"); got != 1 {
			t.Errorf("song_muse_carried logged %d times, want 1", got)
		}
		if strings.Contains(w.log.String(), "Synthetic") {
			t.Errorf("the log record carries Muse text: %s", w.log.String())
		}
		w.verify()
	}
}
