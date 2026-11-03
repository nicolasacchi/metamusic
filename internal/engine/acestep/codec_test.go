package acestep

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
)

func baseRecipe(t *testing.T, raw []byte) engine.Recipe {
	t.Helper()
	r, is := Engine{}.ParseRecipe(raw)
	if len(is) > 0 {
		t.Fatalf("base: %v", is)
	}
	return r
}

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }

func TestRecipeCodec_WithSeedAndID(t *testing.T) {
	t.Parallel()
	base := baseRecipe(t, req(nil))
	got, err := Engine{}.WithSeedAndID(base, 99, "syn-a-s99")
	if err != nil || got.Seed != 99 || got.ID != "syn-a-s99" || got.Hash == base.Hash || got.CoreHash == base.CoreHash {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := (Engine{}).WithSeedAndID(base, -1, "x"); err == nil {
		t.Error("negative seed accepted")
	}
	if _, err := (Engine{}).WithSeedAndID(engine.Recipe{Canonical: []byte(`{}`)}, 1, "x"); err == nil {
		t.Error("unparsable base accepted")
	}
	// same seed and id round trip is the identity
	same, err := Engine{}.WithSeedAndID(base, 7, "syn-a")
	if err != nil || same.Hash != base.Hash {
		t.Errorf("identity: %v", err)
	}
}

func TestApplyMode_Table(t *testing.T) {
	t.Parallel()
	base := baseRecipe(t, req(obj{"bpm": 100}))
	rep := baseRecipe(t, req(repaintOver(100, 400)))
	srcP := &SrcRef{TrackID: "trk-1", MasterSHA256: shaA, DurationDS: 2000}
	for _, tc := range []struct {
		name    string
		base    engine.Recipe
		p       engine.ModeParams
		patch   any
		wantErr string
		check   func(*testing.T, Request)
	}{
		{"new seed", base, engine.ModeParams{Mode: ModeNewSeed, Seed: 5, ID: "c"}, nil, "", func(t *testing.T, r Request) {
			if r.Seed != 5 || r.ID != "c" || r.Caption != "warm pop, female vocal" {
				t.Errorf("%+v", r)
			}
		}},
		{"style edit", base, engine.ModeParams{Mode: ModeStyleEdit, Seed: 7}, &Patch{Style: sp("dark synth"), Thinking: new(bool)}, "", func(t *testing.T, r Request) {
			if r.Caption != "dark synth" || r.Thinking || r.ID != "syn-a" {
				t.Errorf("%+v", r)
			}
		}},
		{"lyric edit", base, engine.ModeParams{Mode: ModeLyricEdit, Seed: 7}, &Patch{Lyrics: sp("[Chorus]\nnew")}, "", func(t *testing.T, r Request) {
			if r.Lyrics != "[Chorus]\nnew" {
				t.Errorf("%+v", r)
			}
		}},
		{"tempo patch", base, engine.ModeParams{Mode: ModeSameSeedEdit, Seed: 7}, &Patch{BPM: OptInt{Set: true, Value: 120}, KeyScale: OptString{Set: true, Value: "c minor"}, TimeSignature: OptString{Set: true, Value: "3"}, DurationDS: ip(2400)}, "", func(t *testing.T, r Request) {
			if *r.BPM != 120 || r.KeyScale != "C Minor" || r.TimeSignature != "3" || r.DurationDS != 2400 {
				t.Errorf("%+v", r)
			}
		}},
		{"tempo removed", base, engine.ModeParams{Mode: ModeSameSeedEdit, Seed: 7}, &Patch{BPM: OptInt{Set: true, Null: true}, KeyScale: OptString{Set: true, Null: true}, TimeSignature: OptString{Set: true, Null: true}}, "", func(t *testing.T, r Request) {
			if r.BPM != nil || r.KeyScale != "" || r.TimeSignature != "" {
				t.Errorf("%+v", r)
			}
		}},
		{"port", base, engine.ModeParams{Mode: ModePort, Seed: 7}, &Patch{Style: sp("x")}, "", nil},
		{"instrumental via empty lyrics", base, engine.ModeParams{Mode: ModeLyricEdit, Seed: 7}, &Patch{Lyrics: sp("")}, "", func(t *testing.T, r Request) {
			if !r.Instrumental {
				t.Errorf("%+v", r)
			}
		}},
		{"repaint", base, engine.ModeParams{Mode: ModeRepaint, Seed: 8, ID: "c"}, &Patch{Src: srcP, RepaintFromDS: ip(100), RepaintToDS: ip(500)}, "", func(t *testing.T, r Request) {
			if r.Task != TaskRepaint || r.Thinking || r.DurationDS != 0 || *r.RepaintToDS != 500 || r.Src.DurationDS != 2000 {
				t.Errorf("%+v", r)
			}
		}},
		{"repaint without source", base, engine.ModeParams{Mode: ModeRepaint, Seed: 8}, &Patch{}, "needs a source", nil},
		{"repaint nil patch", base, engine.ModeParams{Mode: ModeRepaint, Seed: 8}, nil, "needs a source", nil},
		{"repaint bad range", base, engine.ModeParams{Mode: ModeRepaint, Seed: 8}, &Patch{Src: srcP, RepaintFromDS: ip(500), RepaintToDS: ip(100)}, "range_order", nil},
		{"extend", base, engine.ModeParams{Mode: ModeExtend, Seed: 8}, &Patch{Src: srcP, ExtendDS: ip(300)}, "", func(t *testing.T, r Request) {
			if r.Task != TaskExtend || *r.ExtendDS != 300 {
				t.Errorf("%+v", r)
			}
		}},
		{"extend from repaint base drops range", rep, engine.ModeParams{Mode: ModeExtend, Seed: 8}, &Patch{Src: srcP, ExtendDS: ip(300)}, "", func(t *testing.T, r Request) {
			if r.RepaintFromDS != nil || r.RepaintToDS != nil {
				t.Errorf("%+v", r)
			}
		}},
		{"complete", base, engine.ModeParams{Mode: ModeComplete, Seed: 8}, &Patch{Src: srcP, Tracks: []string{"drums", "bass"}, Lyrics: sp("")}, "", func(t *testing.T, r Request) {
			if r.Task != TaskComplete || r.Class() != ClassBase || r.Steps != DefaultBaseSteps || strings.Join(r.Tracks, ",") != "bass,drums" {
				t.Errorf("%+v", r)
			}
		}},
		{"stems", base, engine.ModeParams{Mode: ModeStems, Seed: 8}, &Patch{Src: srcP, Tracks: []string{"vocals"}, Lyrics: sp("")}, "", func(t *testing.T, r Request) {
			if r.Task != TaskExtract || len(r.Tracks) != 1 {
				t.Errorf("%+v", r)
			}
		}},
		{"stems two tracks refused", base, engine.ModeParams{Mode: ModeStems, Seed: 8}, &Patch{Src: srcP, Tracks: []string{"vocals", "bass"}, Lyrics: sp("")}, "tracks_too_many", nil},
		{"back to turbo from complete", baseRecipe(t, req(completeOver("bass"))), engine.ModeParams{Mode: ModeReference, Seed: 8}, &Patch{Ref: &RefRef{TrackID: "r", ClipSHA256: shaB, FromDS: 0, ToDS: 300}, Lyrics: sp("[Verse]\nx")}, "", func(t *testing.T, r Request) {
			if r.Task != TaskText2Music || r.Steps != TurboSteps || r.GuidanceMilli != nil || r.DurationDS != 1000 {
				t.Errorf("%+v", r)
			}
		}},
		{"reference", base, engine.ModeParams{Mode: ModeReference, Seed: 8}, &Patch{Ref: &RefRef{TrackID: "r", ClipSHA256: shaB, FromDS: 0, ToDS: 300}}, "", func(t *testing.T, r Request) {
			if r.Ref == nil || r.Task != TaskText2Music || r.DurationDS != 1800 {
				t.Errorf("%+v", r)
			}
		}},
		{"reference without ref", base, engine.ModeParams{Mode: ModeReference, Seed: 8}, &Patch{}, "needs a reference", nil},
		{"variant of repaint refused", rep, engine.ModeParams{Mode: ModeNewSeed, Seed: 8}, nil, "needs a text2music base", nil},
		{"unknown mode", base, engine.ModeParams{Mode: "cover", Seed: 8}, nil, "not an engine mode", nil},
		{"wrong patch type", base, engine.ModeParams{Mode: ModeNewSeed, Seed: 8}, "x", "not a *acestep.Patch", nil},
		{"unparsable base", engine.Recipe{Canonical: []byte(`{`)}, engine.ModeParams{Mode: ModeNewSeed}, nil, "does not parse", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Engine{}.ApplyMode(tc.base, tc.p, tc.patch)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r, _ := mustParse(t, got.Canonical)
			if tc.check != nil {
				tc.check(t, r)
			}
		})
	}
}

func TestApplyMode_ParamsVersionChanged(t *testing.T) {
	t.Parallel()
	// A stored recipe of an older (known) version is re-pinned with a warning. Simulate by rewriting the
	// version the file knows nothing of: only the current version is known, so the warning path is exercised
	// through a parsed request whose version is forced.
	r, _ := mustParse(t, req(nil))
	r.ParamsVersion = "old"
	c, _ := Canonical(r)
	got, err := Engine{}.derive(engine.Recipe{Canonical: c}, engine.ModeParams{Seed: 1}, nil)
	if err == nil {
		t.Fatalf("an unknown version must not derive silently: %+v", got)
	}
}
