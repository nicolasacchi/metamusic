package sounds_test

import (
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestSounds_DspInheritsFromAncestor: a transform Take's style-derived facets come from the nearest non-dsp ancestor (through
// a chain of transforms); a score_render root keeps only the engine facet and the measured tempo_band.
func TestSounds_DspInheritsFromAncestor(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	parent := w.track(w.recipe("reggae, male vocal, 76bpm", itLyrics), trackOpts{})
	g := librarytest.InsertTransformTake(t, w.st, librarytest.FixedNow, librarytest.TransformTake{N: 1, Kind: "glacier", Parent: parent})
	g2 := librarytest.InsertTransformTake(t, w.st, librarytest.FixedNow, librarytest.TransformTake{N: 2, Kind: "master", Parent: g})
	root := librarytest.InsertTransformTake(t, w.st, librarytest.FixedNow, librarytest.TransformTake{N: 3, Kind: "score_render"})
	w.exec(`INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'tempo','mm-tempo/1 ffmpeg n9','A',?,?)`, root, `{"bpm":120,"confidence":3.1,"octave_ambiguous":false}`, "2026-10-06T12:00:00Z")
	w.compute([]string{g, g2, root}, nil)
	for _, id := range []string{g, g2} {
		want := []string{"engine/dsp/recipe", "genre_family/reggae/style", "vocal/male/style"}
		if got := w.rowsOf("take_sounds", "track_id", id); !reflect.DeepEqual(got, want) {
			t.Errorf("take_sounds of %s = %v, want %v (no declared tempo, no lyrics language)", id, got, want)
		}
	}
	want := []string{"engine/dsp/recipe", "tempo_band/upbeat/measured"}
	if got := w.rowsOf("take_sounds", "track_id", root); !reflect.DeepEqual(got, want) {
		t.Errorf("score_render root take_sounds = %v, want %v", got, want)
	}
}
