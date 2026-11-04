package sounds_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// aceRecipe inserts an acestep15 recipe: the caption is the style, the request carries vocal_language.
func (w *world) aceRecipe(caption, lyrics, lang string) string {
	w.t.Helper()
	id := w.newID()
	rj, _ := json.Marshal(map[string]any{"id": "req-" + id[24:], "task": "text2music", "caption": caption, "lyrics": lyrics,
		"vocal_language": lang, "bpm": 96, "keyscale": "A Minor", "duration_ds": 1200, "seed": 7})
	w.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'acestep15',?,?,?,?,7,'',?,?,'job',?)`, id, "req-"+id[24:], string(rj), caption, lyrics, librarytest.Sha("h"+id), librarytest.Sha("c"+id), "2026-10-06T12:00:00Z")
	return id
}

// TestSounds_ACEStepFacets: the engine facet appears by itself, the caption maps to style phrases, the declared tempo comes
// from tracks.bpm_declared, and the request's vocal_language is used when the lyrics have no detectable language.
func TestSounds_ACEStepFacets(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	tr := w.track(w.aceRecipe("reggae, male vocal", "[Verse]\nla la", "it"), trackOpts{Declared: ip(96)})
	w.compute([]string{tr}, nil)
	want := []string{
		"engine/acestep15/recipe",
		"genre_family/reggae/style",
		"language/it/recipe", // lyrics too short to detect, vocal_language answers
		"tempo_band/mid/declared",
		"vocal/male/style",
	}
	if got := w.rowsOf("take_sounds", "track_id", tr); !reflect.DeepEqual(got, want) {
		t.Errorf("take_sounds = %v, want %v", got, want)
	}

	// Detectable lyrics win over vocal_language; an explicit caption term wins over both.
	t2 := w.track(w.aceRecipe("pop", itLyrics, "en"), trackOpts{})
	t3 := w.track(w.aceRecipe("pop, english", itLyrics, "it"), trackOpts{})
	// Values that are not a two-letter facet value give no language row.
	t4 := w.track(w.aceRecipe("pop", "[Verse]\nla la", "unknown"), trackOpts{})
	t5 := w.track(w.aceRecipe("pop", "[Verse]\nla la", "yue"), trackOpts{})
	w.compute([]string{t2, t3, t4, t5}, nil)
	if got := w.rowsOf("take_sounds", "track_id", t2); !slices.Contains(got, "language/it/lyrics") || slices.Contains(got, "language/en/recipe") {
		t.Errorf("lyrics must win: %v", got)
	}
	if got := w.rowsOf("take_sounds", "track_id", t3); !slices.Contains(got, "language/en/style") || len(w.facetValues("take_sounds", "track_id", t3, "language")) != 1 {
		t.Errorf("caption language must win: %v", got)
	}
	for _, id := range []string{t4, t5} {
		if got := w.facetValues("take_sounds", "track_id", id, "language"); len(got) != 0 {
			t.Errorf("non two-letter vocal_language gave %v", got)
		}
	}
}

// TestSounds_ACEStepVocalLanguageEntersDigest: changing vocal_language is a changed input, so the take recomputes.
func TestSounds_ACEStepVocalLanguageEntersDigest(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	a := w.track(w.aceRecipe("pop", "[Verse]\nla", "it"), trackOpts{})
	b := w.track(w.aceRecipe("pop", "[Verse]\nla", "fr"), trackOpts{})
	w.compute([]string{a, b}, nil)
	da := w.str(`SELECT input_digest FROM sound_state WHERE subject_kind = 'take' AND subject_id = ?`, a)
	db := w.str(`SELECT input_digest FROM sound_state WHERE subject_kind = 'take' AND subject_id = ?`, b)
	if da == "" || da == db {
		t.Errorf("digests %q %q", da, db)
	}
}
