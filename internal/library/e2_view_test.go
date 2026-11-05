package library

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestStemName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"stem-vocals.flac": "vocals", "stem-vocals-2.flac": "vocals", "stem-backing_vocals-10.flac": "backing_vocals"} {
		if got, ok := StemName(in); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"audio.flac", "stem-.flac", "stem-vocals.mp3", "lyrics.lrc", "stems-a.flac"} {
		if _, ok := StemName(in); ok {
			t.Errorf("%s accepted", in)
		}
	}
}

func TestRecipeACE(t *testing.T) {
	t.Parallel()
	raw := `{"id":"x","task":"repaint","caption":"c","lyrics":"","instrumental":true,"vocal_language":"en","seed":1,"thinking":false,"engine_params_version":"ace1","inference_steps":8,
	"src":{"track_id":"t1","master_sha256":"` + string(make([]byte, 0)) + `ab","duration_ds":2431},"repaint_from_ds":80,"repaint_to_ds":200,"ref":{"track_id":"t2","clip_sha256":"cd","from_ds":0,"to_ds":200}}`
	got := recipeACE(raw)
	want := &RecipeACE{Task: "repaint", TaskClass: "turbo",
		Src:  &RecipeSrc{TrackID: "t1", MasterSHA256: "ab", DurationS: 243.1},
		Ref:  &RecipeRef{TrackID: "t2", ClipSHA256: "cd", FromS: 0, ToS: 20},
		Edit: &RecipeEdit{FromS: f(8), ToS: f(20), Tracks: []string{}}}
	if d := cmp.Diff(want, got); d != "" {
		t.Error(d)
	}
	b, _ := json.Marshal(recipeACE(`{"task":"extract","tracks":["vocals"],"inference_steps":50,"guidance_milli":7000}`))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["task_class"] != "base" {
		t.Errorf("extract is base class: %s", b)
	}
	if recipeACE("not json") != nil {
		t.Error("garbage parsed")
	}
}

func f(v float64) *float64 { return &v }
