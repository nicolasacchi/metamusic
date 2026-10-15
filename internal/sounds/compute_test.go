package sounds_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

const itLyrics = "[Verse 1]\nIo sono qui con te e non ho paura di quello che sarà\nIl cielo è blu e il mare è calmo per noi\nTu sei la mia luce nella notte che non finisce\n[Chorus]\nCanto per te questa canzone che non muore mai\nNon è la fine di una storia ma solo l'inizio"

func TestCompute_KnownRecipe(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	r := w.recipe("reggae, male vocal, 76bpm", itLyrics)
	tr := w.track(r, trackOpts{})
	w.score(tr, 92)
	w.compute([]string{tr}, nil)
	want := []string{
		"engine/yue2/recipe",
		"genre_family/reggae/style",
		"language/it/lyrics",
		"tempo_band/mid/plan", // 92 BPM
		"tempo_band/slow/declared",
		"vocal/male/style",
	}
	if got := w.rowsOf("take_sounds", "track_id", tr); !reflect.DeepEqual(got, want) {
		t.Errorf("take_sounds = %v, want %v", got, want)
	}
	var d string
	if err := w.st.R.QueryRow(`SELECT detail_json FROM take_sounds WHERE track_id = ? AND origin = 'declared'`, tr).Scan(&d); err != nil || d != `{"bpm":76}` {
		t.Errorf("declared detail = %q, %v", d, err)
	}
	// A score that agrees with the declared tempo adds no plan row.
	r2 := w.recipe("reggae, 76bpm", itLyrics)
	t2 := w.track(r2, trackOpts{})
	w.score(t2, 76)
	w.compute([]string{t2}, nil)
	if got := w.facetValues("take_sounds", "track_id", t2, "tempo_band"); !reflect.DeepEqual(got, []string{"slow"}) {
		t.Errorf("agreeing score: %v", got)
	}
	if n := w.count(`SELECT COUNT(*) FROM take_sounds WHERE track_id = ? AND origin = 'plan'`, t2); n != 0 {
		t.Errorf("plan rows = %d, want 0", n)
	}
	// An explicit language term wins over the lyrics.
	r3 := w.recipe("reggae, english, 100bpm", itLyrics)
	t3 := w.track(r3, trackOpts{})
	w.compute([]string{t3}, nil)
	if got := w.rowsOf("take_sounds", "track_id", t3); !slices.Contains(got, "language/en/style") || slices.Contains(got, "language/it/lyrics") {
		t.Errorf("language: %v", got)
	}
}

func TestCompute_TempoBandEdge(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	for _, c := range []struct {
		style, band string
	}{{"pop, 79bpm", "slow"}, {"pop, 80bpm", "mid"}, {"pop, 109bpm", "mid"}, {"pop, 110bpm", "upbeat"}, {"pop, 130bpm", "fast"}, {"pop, 160bpm", "rapid"}} {
		tr := w.track(w.recipe(c.style, "x"), trackOpts{})
		w.compute([]string{tr}, nil)
		if got := w.facetValues("take_sounds", "track_id", tr, "tempo_band"); !reflect.DeepEqual(got, []string{c.band}) {
			t.Errorf("%s: %v, want %s", c.style, got, c.band)
		}
	}
}

func TestCompute_DeclaredPrefersBpmDeclared(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	tr := w.track(w.recipe("pop, 150bpm", "x"), trackOpts{Declared: ip(90)})
	w.compute([]string{tr}, nil)
	if got := w.rowsOf("take_sounds", "track_id", tr); !slices.Contains(got, "tempo_band/mid/declared") || slices.Contains(got, "tempo_band/fast/declared") {
		t.Errorf("rows %v: tracks.bpm_declared must win over the style", got)
	}
	if n := w.count(`SELECT COUNT(*) FROM take_sounds WHERE track_id = ? AND origin = 'plan'`, tr); n != 0 {
		t.Errorf("no scores => no plan row, got %d", n)
	}
}

func TestCompute_ReferenceMeasuredOnly(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	tr := w.track(w.stubRecipe(), trackOpts{Ref: true})
	w.compute([]string{tr}, nil)
	if n := w.count(`SELECT COUNT(*) FROM take_sounds WHERE track_id = ?`, tr); n != 0 {
		t.Fatalf("a reference take with no tempo analysis has %d rows", n)
	}
	if n := w.count(`SELECT COUNT(*) FROM sound_state WHERE subject_kind = 'take' AND subject_id = ?`, tr); n != 1 {
		t.Fatalf("state rows = %d, want 1 (the take is fresh, just empty)", n)
	}
	w.tempo(tr, 95)
	w.compute([]string{tr}, nil)
	if got := w.rowsOf("take_sounds", "track_id", tr); !reflect.DeepEqual(got, []string{"tempo_band/mid/measured"}) {
		t.Errorf("rows = %v, want exactly one measured row", got)
	}
}

func TestCompute_NoTempoLane(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	tr := w.track(w.recipe("rock, 120bpm", itLyrics), trackOpts{})
	w.compute([]string{tr}, nil)
	before := w.rowsOf("take_sounds", "track_id", tr)
	if n := w.count(`SELECT COUNT(*) FROM take_sounds WHERE origin = 'measured'`); n != 0 {
		t.Errorf("measured rows without a tempo lane: %d", n)
	}
	w.tempo(tr, 0) // an analysis that found no tempo changes nothing but the digest
	w.compute([]string{tr}, nil)
	if got := w.rowsOf("take_sounds", "track_id", tr); !reflect.DeepEqual(got, before) {
		t.Errorf("rows changed: %v -> %v", before, got)
	}
}

func TestCompute_SongSubjectFollowsPick(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	r1 := w.recipe("reggae, male vocal, 76bpm", "x")
	a := w.track(r1, trackOpts{AgeMin: 10})
	r2 := w.recipe("hip hop, female vocal, 100bpm", "x")
	b := w.track(r2, trackOpts{Parent: a, AgeMin: 5}) // newest, same Song
	w.settle()
	song := w.songOfTrack(a)
	if song == "" || song != w.songOfTrack(b) {
		t.Fatalf("songs: %q %q", song, w.songOfTrack(b))
	}
	// No pick yet: the newest eligible take is the subject.
	if got := w.facetValues("song_sounds", "song_id", song, "genre_family"); !reflect.DeepEqual(got, []string{"hip-hop"}) {
		t.Fatalf("newest subject: %v", got)
	}
	w.pick(song, a)
	w.compute(nil, []string{song})
	if got := w.facetValues("song_sounds", "song_id", song, "genre_family"); !reflect.DeepEqual(got, []string{"reggae"}) {
		t.Errorf("pick subject: %v", got)
	}
	if got := w.facetValues("song_sounds", "song_id", song, "vocal"); !reflect.DeepEqual(got, []string{"male"}) {
		t.Errorf("vocal: %v", got)
	}
}

func TestCompute_SongDraftSubject(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	d := w.draft("jazz, female vocal, 100bpm")
	w.settle()
	song := w.songOfDraft(d)
	if got := w.facetValues("song_sounds", "song_id", song, "genre_family"); !reflect.DeepEqual(got, []string{"jazz"}) {
		t.Fatalf("draft subject: %v", got)
	}
	at := w.computedAt("song", song)
	w.compute(nil, []string{song})
	if w.computedAt("song", song) != at {
		t.Error("an unchanged draft was recomputed")
	}
	rj, _ := json.Marshal(map[string]any{"id": "x", "seed": 5, "style": "blues, male vocal", "lyrics": "x"})
	w.exec(`UPDATE recipe_drafts SET request_json = ?, revision = revision + 1 WHERE id = ?`, string(rj), d)
	w.settle()
	if got := w.facetValues("song_sounds", "song_id", song, "genre_family"); !reflect.DeepEqual(got, []string{"blues"}) {
		t.Errorf("after the draft revision: %v", got)
	}
}

func TestCompute_SongTempoCollapse(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	tr := w.track(w.recipe("pop, 76bpm", "x"), trackOpts{})
	w.score(tr, 120)
	w.settle()
	song := w.songOfTrack(tr)
	detail := func() map[string]any {
		var s string
		if err := w.st.R.QueryRow(`SELECT detail_json FROM song_sounds WHERE song_id = ? AND facet = 'tempo_band'`, song).Scan(&s); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if n := w.count(`SELECT COUNT(*) FROM song_sounds WHERE song_id = ? AND facet = 'tempo_band'`, song); n != 1 {
		t.Fatalf("tempo rows = %d, want one collapsed row", n)
	}
	if d := detail(); d["basis"] != "declared" || d["requested_bpm"] != 76.0 || d["plan_bpm"] != 120.0 || d["measured_bpm"] != nil || d["mismatch"] != false {
		t.Errorf("declared collapse: %v", d)
	}
	w.tempo(tr, 110) // measured disagrees with the request
	w.settle()
	d := detail()
	if d["basis"] != "measured" || d["measured_bpm"] != 110.0 || d["mismatch"] != true || d["octave"] != false {
		t.Errorf("measured collapse: %v", d)
	}
	if got := w.facetValues("song_sounds", "song_id", song, "tempo_band"); !reflect.DeepEqual(got, []string{"upbeat"}) {
		t.Errorf("collapsed value = %v, want upbeat (measured wins)", got)
	}
	// An octave error is not a mismatch.
	t2 := w.track(w.recipe("pop, 70bpm", "x"), trackOpts{})
	w.tempo(t2, 141)
	w.settle()
	s2 := w.songOfTrack(t2)
	var s string
	_ = w.st.R.QueryRow(`SELECT detail_json FROM song_sounds WHERE song_id = ? AND facet = 'tempo_band'`, s2).Scan(&s)
	var m map[string]any
	_ = json.Unmarshal([]byte(s), &m)
	if m["octave"] != true || m["mismatch"] != false {
		t.Errorf("octave: %v", m)
	}
}

func TestCompute_Freshness(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	tr := w.track(w.recipe("reggae, 80bpm", "x"), trackOpts{})
	w.settle()
	song := w.songOfTrack(tr)
	tAt, sAt := w.computedAt("take", tr), w.computedAt("song", song)
	if ch := w.compute([]string{tr}, []string{song}); ch.Takes != 0 || ch.Songs != 0 {
		t.Errorf("unchanged digest rewrote %+v", ch)
	}
	if w.computedAt("take", tr) != tAt || w.computedAt("song", song) != sAt {
		t.Error("computed_at moved without a change")
	}
	// A taxonomy version bump rewrites both.
	w.exec(`INSERT INTO sound_taxonomy_versions(version,kind,created_by,created_at) VALUES((SELECT MAX(version)+1 FROM sound_taxonomy_versions),'extension','human:op','2026-10-06T12:00:00.000Z')`)
	ch := w.compute([]string{tr}, []string{song})
	if ch.Takes != 1 || ch.Songs != 1 {
		t.Errorf("after a version bump: %+v", ch)
	}
	if w.computedAt("take", tr) == tAt {
		t.Error("take not rewritten after the version bump")
	}
	// Input changes (a measured tempo) change the digest.
	w.tempo(tr, 82)
	if ch := w.compute([]string{tr}, nil); ch.Takes != 1 {
		t.Errorf("new analysis: %+v", ch)
	}
}

func TestSweepStale(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	var takes []string
	for i := 0; i < 5; i++ {
		takes = append(takes, w.track(w.recipe("rock, 100bpm", "x"), trackOpts{}))
	}
	w.settle()
	sweep := func(limit int) int {
		tx, err := w.st.W.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		n, err := w.sv.SweepStaleTx(context.Background(), tx, soundsEnv(w), limit)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := sweep(100); n != 0 {
		t.Fatalf("nothing is stale, swept %d", n)
	}
	w.exec(`DELETE FROM sound_state WHERE subject_kind = 'take' AND subject_id IN (?, ?, ?)`, takes[0], takes[1], takes[2])
	if n := sweep(2); n != 2 {
		t.Errorf("limit 2 swept %d", n)
	}
	if n := sweep(100); n != 1 {
		t.Errorf("rest swept %d, want 1", n)
	}
	// A changed input without a dirty mark (restored analyses, say) is found by the digest scan.
	w.exec(`DELETE FROM provenance_dirty`)
	w.tempo(takes[3], 140)
	w.exec(`DELETE FROM provenance_dirty`)
	if n := sweep(100); n < 1 {
		t.Errorf("digest-stale take not swept (%d)", n)
	}
	if got := w.rowsOf("take_sounds", "track_id", takes[3]); !slices.Contains(got, "tempo_band/fast/measured") {
		t.Errorf("rows %v", got)
	}
}
