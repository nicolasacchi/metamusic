package transform

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func f64(v float64) *float64 { return &v }

// goldenSpecs is one synthetic spec per kind, written by hand (every default written out), so all nine kinds are pinned
// whatever is registered.
func goldenSpecs() map[Kind]Spec {
	one := []InputRef{{Role: "source", TrackID: "00000000-0000-7000-8000-000000000001", MasterSHA256: shaA}}
	two := []InputRef{{Role: "a", TrackID: "00000000-0000-7000-8000-000000000001", MasterSHA256: shaA}, {Role: "b", TrackID: "00000000-0000-7000-8000-000000000002", MasterSHA256: shaB}}
	al := &Alignment{Inputs: []InputAlign{
		{BPM: 92, BPMSource: SrcDeclared, KeyPC: 9, KeyMinor: true, KeySource: "measured", DownbeatS: f64(0.4), BeatsPerBar: 4},
		{BPM: 128, BPMSource: SrcMeasured, OctaveFold: 1, KeyPC: 0, KeySource: "plan", DownbeatAuto: true, BeatsPerBar: 4},
	}}
	mk := func(k Kind, seed int64, in []InputRef, params string, a *Alignment) Spec {
		return Spec{Seed: seed, V: 1, Kind: k, DSPVersion: DSPVersion, SampleRate: 48000, Inputs: in, Params: json.RawMessage(params), Alignment: a, TargetLUFS: -16}
	}
	sc := mk(KindScoreRender, 0, nil, `{"bpm":120,"dialect":"standard","gain":0.6,"soundfont":"orchestra","tail_s":3}`, nil)
	sc.Synth = &SynthRef{Name: "orchestra", SoundfontSHA: shaB, Engine: "fluidsynth"}
	sc.Score = &ScoreRef{SHA256: shaA, Bytes: 1234, Dialect: "standard"}
	return map[Kind]Spec{
		KindMorph:       mk(KindMorph, 0, two, `{"a_at":{"bar":12},"b_at":{"bar":0},"bars":8,"beats_per_bar":4,"bpm_a":92,"bpm_b":128,"key_policy":"to_a","semitones_b":-2,"track_a_id":"00000000-0000-7000-8000-000000000001","track_b_id":"00000000-0000-7000-8000-000000000002"}`, al),
		KindShepard:     mk(KindShepard, 7, one, `{"bell_sigma_octaves":1,"direction":"up","duration_s":120,"layers":5,"loop_s":4,"period_s":24,"source_id":"00000000-0000-7000-8000-000000000001","start_s":0}`, nil),
		KindGlacier:     mk(KindGlacier, 7, one, `{"factor":20,"fade_in_s":4,"fade_out_s":4,"length_s":30,"source_id":"00000000-0000-7000-8000-000000000001","start_s":0,"window_s":0.5}`, nil),
		KindPolytempo:   mk(KindPolytempo, 0, two, `{"beats_per_bar":4,"bpm_a":92,"bpm_b":128,"converge_bar":20,"pan":"split","track_a_id":"00000000-0000-7000-8000-000000000001","track_b_id":"00000000-0000-7000-8000-000000000002","unison_bars":8}`, al),
		KindEnsemble:    mk(KindEnsemble, 7, one, `{"dry_db":-12,"source_id":"00000000-0000-7000-8000-000000000001","space":"cathedral","tail_s":20,"wet_db":-6}`, nil),
		KindScoreRender: sc,
		KindMaster:      mk(KindMaster, 0, one, `{"ceiling_dbtp":-1,"comp_amount":0.25,"low_cut_hz":25,"preset":"neutral","source_id":"00000000-0000-7000-8000-000000000001","target_lufs":null,"tilt_db":0}`, nil),
		KindWiden:       mk(KindWiden, 0, one, `{"bass_mono_hz":120,"decorrelate":0.1,"source_id":"00000000-0000-7000-8000-000000000001","width":1.3}`, nil),
		KindBinaural:    mk(KindBinaural, 7, one, `{"elevation_deg":0,"left_az_deg":-30,"model":"hrtf","preset":"studio","right_az_deg":30,"room":"studio","room_mix_db":-24,"source_id":"00000000-0000-7000-8000-000000000001","tail_s":1}`, nil),
	}
}

// TestSpec_Canonical_Golden: the canonical bytes, both hashes and the id of one spec per kind are committed.
func TestSpec_Canonical_Golden(t *testing.T) {
	t.Parallel()
	specs := goldenSpecs()
	if len(specs) != len(AllKinds) {
		t.Fatalf("%d golden specs for %d kinds", len(specs), len(AllKinds))
	}
	for _, k := range AllKinds {
		t.Run(string(k), func(t *testing.T) {
			c, err := Canonicalize(specs[k])
			if err != nil {
				t.Fatal(err)
			}
			jsonPath := filepath.Join("testdata", "spec_"+string(k)+".json")
			shaPath := filepath.Join("testdata", "spec_"+string(k)+".sha256")
			hashes := "spec_hash " + c.SpecHash + "\nrecipe_hash " + c.RecipeHash + "\nid " + c.ID + "\n"
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(jsonPath, append(append([]byte{}, c.Bytes...), '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(shaPath, []byte(hashes), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			wantJSON, err := os.ReadFile(jsonPath)
			if err != nil {
				t.Fatal(err)
			}
			wantSHA, err := os.ReadFile(shaPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSuffix(string(wantJSON), "\n") != string(c.Bytes) {
				t.Errorf("canonical bytes differ from %s:\n got %s\nwant %s", jsonPath, c.Bytes, wantJSON)
			}
			if string(wantSHA) != hashes {
				t.Errorf("hashes differ from %s:\n got %s\nwant %s", shaPath, hashes, wantSHA)
			}
		})
	}
}

// TestSpec_IDFromHash: the id is "dsp-<kind>-<first 20 hex of spec_hash>", spec_hash is the hash of the spec WITHOUT its id,
// recipes.hash is the hash of the bytes WITH it, and the two differ.
func TestSpec_IDFromHash(t *testing.T) {
	t.Parallel()
	for k, s := range goldenSpecs() {
		c, err := Canonicalize(s)
		if err != nil {
			t.Fatal(err)
		}
		if c.ID != "dsp-"+string(k)+"-"+c.SpecHash[:20] {
			t.Errorf("%s: id %s", k, c.ID)
		}
		if c.SpecHash == c.RecipeHash {
			t.Errorf("%s: spec_hash equals recipes.hash", k)
		}
		if c.RecipeHash != sha256Hex(c.Bytes) {
			t.Errorf("%s: recipe hash is not the sha256 of the canonical bytes", k)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(c.Bytes, &m); err != nil {
			t.Fatal(err)
		}
		delete(m, "id")
		noID, _ := json.Marshal(m)
		if c.SpecHash != sha256Hex(noID) { // json.Marshal of a map sorts keys, as canonjson does
			t.Errorf("%s: spec_hash is not the hash of the id-less canonical object", k)
		}
		// the id given in the input is ignored and recomputed
		s.ID = "dsp-other-0000"
		c2, _ := Canonicalize(s)
		if c2.ID != c.ID || c2.SpecHash != c.SpecHash || c2.RecipeHash != c.RecipeHash {
			t.Errorf("%s: the input id changed the result", k)
		}
	}
}

// TestSpec_DefaultsWrittenOut: two requests that differ only by spelling a default out hash equally, and one that differs by a
// value does not.
func TestSpec_DefaultsWrittenOut(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	src := r.tx("A")
	hash := func(q Request) string {
		t.Helper()
		e, err := r.svc.Estimate(t.Context(), human, q)
		if err != nil {
			t.Fatal(err)
		}
		return e.SpecHash
	}
	bare := hash(glacier(src))
	spelled := hash(req(KindGlacier, `{"source_id":"`+src+`","factor":20,"start_s":0,"window_s":0.5,"fade_in_s":4,"fade_out_s":4,"length_s":null}`))
	if bare != spelled {
		t.Errorf("spelling the defaults out changed the hash: %s vs %s", bare, spelled)
	}
	if other := hash(req(KindGlacier, `{"source_id":"`+src+`","factor":21}`)); other == bare {
		t.Error("a different factor kept the hash")
	}
	// the resolved parameters of the estimate hold every default
	e, _ := r.svc.Estimate(t.Context(), human, glacier(src))
	m := paramsMap(e.Resolved)
	for _, name := range []string{"factor", "start_s", "length_s", "window_s", "fade_in_s", "fade_out_s", "source_id"} {
		if _, ok := m[name]; !ok {
			t.Errorf("resolved params lack %s: %s", name, e.Resolved)
		}
	}
	// 20.004 is on the two-decimal step: it hashes like 20
	if h := hash(req(KindGlacier, `{"source_id":"`+src+`","factor":20.004}`)); h != bare {
		t.Error("a value off the step did not round to it")
	}
}

// TestDefaultSeed: the default seed of a track is stable, below 2^53, and differs between tracks.
func TestDefaultSeed(t *testing.T) {
	t.Parallel()
	a, b := DefaultSeed("track-a"), DefaultSeed("track-b")
	if a == b || a != DefaultSeed("track-a") {
		t.Errorf("seeds %d %d", a, b)
	}
	for _, s := range []int64{a, b, DefaultSeed(""), DefaultSeed("x")} {
		if s < 0 || s >= seedLimit {
			t.Errorf("seed %d out of range", s)
		}
	}
}
