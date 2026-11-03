package acestep

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

func TestParse_CanonicalRoundTrip(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]json.RawMessage{
		"text2music": req(nil),
		"keyscale":   req(obj{"keyscale": "f#m", "bpm": 96, "timesignature": "3"}),
		"repaint":    req(repaintOver(100, 400)),
		"extend":     req(extendOver(305)),
		"complete":   req(completeOver("drums", "bass")),
		"html":       req(obj{"caption": "a <b> & c"}),
	} {
		_, c1 := mustParse(t, raw)
		_, c2 := mustParse(t, c1)
		if !bytes.Equal(c1, c2) {
			t.Errorf("%s: canonical not a fixed point:\n%s\n%s", name, c1, c2)
		}
		if bytes.Contains(c1, []byte(`\u003c`)) {
			t.Errorf("%s: HTML escaped in canonical bytes", name)
		}
	}
}

func TestParse_StampsDefaults(t *testing.T) {
	t.Parallel()
	r, _ := mustParse(t, req(obj{"vocal_language": nil}))
	if r.ParamsVersion != CurrentParamsVersion() || r.Steps != TurboSteps || r.VocalLanguage != "unknown" || r.GuidanceMilli != nil {
		t.Errorf("turbo defaults: %+v", r)
	}
	b, _ := mustParse(t, req(completeOver("bass")))
	if b.Steps != DefaultBaseSteps || b.GuidanceMilli == nil || *b.GuidanceMilli != DefaultGuidanceMilli || b.Class() != ClassBase {
		t.Errorf("base defaults: %+v", b)
	}
}

func TestParse_StampsParamsVersion(t *testing.T) {
	t.Parallel()
	_, c := mustParse(t, req(nil))
	if !bytes.Contains(c, []byte(`"engine_params_version":"`+CurrentParamsVersion()+`"`)) {
		t.Errorf("version not stamped: %s", c)
	}
}

func TestNormalizeKey(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Am": "A Minor", "c major": "C Major", "F#m": "F# Minor", "Bb": "Bb Major", "bb minor": "Bb Minor", "C": "C Major",
		"d♭ maj": "Db Major", "E min": "E Minor", "G M": "G Major", "A Minor": "A Minor", " e ": "E Major",
	} {
		if got, ok := NormalizeKey(in); !ok || got != want {
			t.Errorf("NormalizeKey(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "H", "C lydian", "major"} {
		if _, ok := NormalizeKey(in); ok {
			t.Errorf("NormalizeKey(%q) accepted", in)
		}
	}
	_, _, is := Parse(req(obj{"keyscale": "am"}))
	if len(is) != 1 || is[0].Code != "keyscale_normalised" || is[0].Severity != "warning" {
		t.Errorf("warning: %v", is)
	}
}

func TestParse_TracksSorted(t *testing.T) {
	t.Parallel()
	r, _ := mustParse(t, req(completeOver("synth", "bass")))
	if strings.Join(r.Tracks, ",") != "bass,synth" {
		t.Errorf("tracks %v", r.Tracks)
	}
}

func FuzzParse(f *testing.F) {
	for _, raw := range []json.RawMessage{req(nil), req(repaintOver(100, 400)), req(extendOver(305)), req(completeOver("bass")), []byte(`{`), []byte(`null`)} {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		r, c, is := Parse(raw)
		if HasErrors(is) {
			if c != nil {
				t.Fatalf("canonical returned with errors: %v", is)
			}
			return
		}
		r2, c2, is2 := Parse(c)
		if HasErrors(is2) || !bytes.Equal(c, c2) {
			t.Fatalf("canonical does not round trip: %s -> %s %v", c, c2, is2)
		}
		_ = r
		_ = r2
	})
}

// goldenRow is every byte an ACE-Step recipe contributes to the library.
type goldenRow struct {
	Case      string `json:"case"`
	Canonical string `json:"canonical,omitempty"`
	Hash      string `json:"sha_canonical,omitempty"`
	CoreHash  string `json:"sha_core,omitempty"`
	Content   string `json:"sha_content,omitempty"`
	Render    string `json:"sha_render,omitempty"`
	Err       string `json:"err,omitempty"`
}

func TestHashes_AceStep_Golden(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"t2m", req(nil)},
		{"t2m_tempo_key", req(obj{"bpm": 96, "keyscale": "A Minor", "timesignature": "4"})},
		{"t2m_ref", req(obj{"ref": obj{"track_id": "trk-2", "clip_sha256": shaB, "from_ds": 0, "to_ds": 300}})},
		{"repaint", req(repaintOver(100, 400))},
		{"extend", req(extendOver(305))},
		{"complete", req(completeOver("bass", "drums"))},
		{"extract", req(func() obj { o := completeOver("vocals"); o["task"] = "extract"; return o }())},
		{"unicode", req(obj{"caption": "cantautore italiano", "lyrics": "[Verse]\nciao città è bello", "vocal_language": "it"})},
		{"invalid", req(obj{"id": nil})},
	}
	var rows []goldenRow
	for _, tc := range cases {
		rec, is := Engine{}.ParseRecipe(tc.raw)
		if len(is) > 0 {
			rows = append(rows, goldenRow{Case: tc.name, Err: codes(is)})
			continue
		}
		content, render := lineage.Keys(rec.Canonical)
		rows = append(rows, goldenRow{Case: tc.name, Canonical: string(rec.Canonical), Hash: rec.Hash, CoreHash: rec.CoreHash, Content: content, Render: render})
	}
	got, _ := json.MarshalIndent(rows, "", "  ")
	got = append(got, '\n')
	const path = "testdata/recipe_acestep.golden.json"
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("golden mismatch (run with -update):\n%s", got)
	}
}

func TestRenderHash_SrcChangesHash(t *testing.T) {
	t.Parallel()
	a := req(repaintOver(100, 400))
	o := repaintOver(100, 400)
	o["src"] = obj{"track_id": "trk-1", "master_sha256": shaB, "duration_ds": 1000}
	b := req(o)
	c := req(repaintOver(100, 401))
	ra, _ := Engine{}.ParseRecipe(a)
	rb, _ := Engine{}.ParseRecipe(b)
	rc, _ := Engine{}.ParseRecipe(c)
	ka, kb, kc := render(ra), render(rb), render(rc)
	if ka == kb || ka == kc || kb == kc {
		t.Errorf("render hashes collide: %s %s %s", ka, kb, kc)
	}
}

func render(r engine.Recipe) string { _, h := lineage.Keys(r.Canonical); return h }

func TestHashes_IncludeParamsVersion(t *testing.T) {
	t.Parallel()
	r, _ := mustParse(t, req(nil))
	r2 := r
	r2.ParamsVersion = "ace2"
	c1, _ := Canonical(r)
	c2, _ := Canonical(r2)
	a, b := RecipeOf(r, c1, nil), RecipeOf(r2, c2, nil)
	if a.Hash == b.Hash {
		t.Error("a version bump must change the hash")
	}
	if a.CoreHash != b.CoreHash {
		t.Error("a version bump must not change the core hash")
	}
}

func TestRecipeOf_Facts(t *testing.T) {
	t.Parallel()
	rec, is := Engine{}.ParseRecipe(req(nil))
	if len(is) != 0 || rec.ID != "syn-a" || rec.Cot != "think" || rec.Style != "warm pop, female vocal" || rec.Seed != 7 || rec.Hash == rec.CoreHash {
		t.Errorf("%+v %v", rec, is)
	}
	off, _ := Engine{}.ParseRecipe(req(obj{"thinking": false}))
	if off.Cot != "off" || off.CoreHash == rec.CoreHash {
		t.Errorf("cot off: %+v", off)
	}
	if _, is := (Engine{}).ParseRecipe([]byte(`{}`)); !HasErrors(is) {
		t.Error("empty object must not parse")
	}
}
