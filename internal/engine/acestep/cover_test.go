package acestep

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
)

func coverOver(milli any) obj {
	return obj{"task": "cover", "duration_ds": nil, "thinking": false, "src": src(1000), "cover_strength_milli": milli, "caption": "slow jazz trio"}
}

func TestParse_Cover(t *testing.T) {
	t.Parallel()
	good := req(coverOver(500))
	r, c1 := mustParse(t, good)
	if r.Task != TaskCover || r.Class() != ClassTurbo || r.Steps != TurboSteps || r.GuidanceMilli != nil || *r.CoverStrengthMilli != 500 {
		t.Errorf("%+v", r)
	}
	if _, c2 := mustParse(t, c1); !bytes.Equal(c1, c2) {
		t.Errorf("canonical not a fixed point:\n%s\n%s", c1, c2)
	}
	if !bytes.Contains(c1, []byte(`"cover_strength_milli":500`)) {
		t.Errorf("canonical: %s", c1)
	}
	for name, tc := range map[string]struct {
		raw  []byte
		want string
	}{
		"strength 0 ok":       {req(coverOver(0)), ""},
		"strength 1000 ok":    {req(coverOver(1000)), ""},
		"empty lyrics ok":     {req(merge(coverOver(300), obj{"lyrics": ""})), ""},
		"instrumental ok":     {req(merge(coverOver(300), obj{"lyrics": "", "instrumental": true})), ""},
		"strength negative":   {req(coverOver(-1)), "bad_strength@cover_strength_milli"},
		"strength 1001":       {req(coverOver(1001)), "bad_strength@cover_strength_milli"},
		"strength missing":    {req(coverOver(nil)), "strength_required@cover_strength_milli"},
		"strength on t2m":     {req(obj{"cover_strength_milli": 500}), "strength_forbidden@cover_strength_milli"},
		"strength on repaint": {req(merge(repaintOver(100, 400), obj{"cover_strength_milli": 5})), "strength_forbidden@cover_strength_milli"},
		"no src":              {req(merge(coverOver(500), obj{"src": nil})), "src_required@src"},
		"duration":            {req(merge(coverOver(500), obj{"duration_ds": 100})), "duration_forbidden@duration_ds"},
		"range":               {req(merge(coverOver(500), obj{"repaint_from_ds": 1, "repaint_to_ds": 300})), "range_forbidden@repaint_from_ds"},
		"extend":              {req(merge(coverOver(500), obj{"extend_ds": 300})), "extend_forbidden@extend_ds"},
		"tracks":              {req(merge(coverOver(500), obj{"tracks": []string{"bass"}})), "tracks_forbidden@tracks"},
		"guidance":            {req(merge(coverOver(500), obj{"guidance_milli": 7000})), "guidance_forbidden@guidance_milli"},
		"lrc":                 {req(merge(coverOver(500), obj{"lrc": true})), "lrc_task@lrc"},
		"thinking":            {req(merge(coverOver(500), obj{"thinking": true})), "thinking_ignored@thinking"},
		"ref":                 {req(merge(coverOver(500), obj{"ref": obj{"track_id": "r", "clip_sha256": shaB, "from_ds": 0, "to_ds": 300}})), "ref_forbidden@ref"},
		"instrumental+lyrics": {req(merge(coverOver(500), obj{"instrumental": true})), "instrumental_lyrics@lyrics"},
		"missing caption":     {req(merge(coverOver(500), obj{"caption": ""})), "bad_caption@caption"},
	} {
		_, _, is := Parse(tc.raw)
		if got := codes(is); got != tc.want {
			t.Errorf("%s: %q want %q", name, got, tc.want)
		}
	}
}

func merge(a, b obj) obj {
	o := obj{}
	for k, v := range a {
		o[k] = v
	}
	for k, v := range b {
		o[k] = v
	}
	return o
}

func TestCover_CapsAndCheck(t *testing.T) {
	t.Parallel()
	f := textOnlyFeatures()
	if c := (Engine{Verified: f}).Caps(); c.Cover || slices.Contains(c.Tasks, ModeCover) {
		t.Errorf("unverified cover offered: %+v", c)
	}
	f.Tasks, f.CanaryCommit = []string{TaskText2Music, TaskCover}, "c0ffee"
	c := Engine{Verified: f}.Caps() // turbo: no base models needed
	if !c.Cover || !slices.Contains(c.Tasks, ModeCover) || !slices.IsSorted(c.Tasks) {
		t.Errorf("caps %+v", c)
	}
	r, _ := mustParse(t, req(coverOver(500)))
	if is := Check(r, c); len(is) != 0 {
		t.Errorf("check: %v", is)
	}
	c.Cover = false
	if is := Check(r, c); len(is) != 1 || is[0].Code != "ENGINE_CAPABILITY" || is[0].Message != "capability=cover" {
		t.Errorf("check off: %v", is)
	}
	if is := (Engine{Verified: textOnlyFeatures()}).Validate(req(coverOver(500))); len(is) != 1 || is[0].Message != "capability=cover" {
		t.Errorf("validate: %v", is)
	}
	if got := ExpectedSeconds(r); got != 100 {
		t.Errorf("ExpectedSeconds = %d", got)
	}
	long, _ := mustParse(t, req(merge(coverOver(500), obj{"src": src(5000)})))
	c.Cover = true
	if is := Check(long, c); len(is) != 1 || is[0].Message != "capability=max_seconds" {
		t.Errorf("long: %v", is)
	}
	if LearnsRTF(TaskCover) {
		t.Error("cover must not teach the class RTF")
	}
	if q := QuoteInputOf(r); q.Class != ClassTurbo || q.AudioS != 100 {
		t.Errorf("quote %+v", q)
	}
}

func TestCover_Canary(t *testing.T) {
	t.Parallel()
	base := textOnlyFeatures()
	// turbo: no base-task gate
	on := base.WithCanary([]string{"cover"}, false)
	if !on.has(TaskCover) || base.has(TaskCover) {
		t.Errorf("WithCanary: %v / %v", on.Tasks, base.Tasks)
	}
	r, _ := mustParse(t, req(coverOver(500)))
	if got := base.CanaryCapsUsed(r, []string{"cover"}); !slices.Equal(got, []string{"cover"}) {
		t.Errorf("used %v", got)
	}
	if got := on.CanaryCapsUsed(r, []string{"cover"}); len(got) != 0 {
		t.Errorf("verified: %v", got)
	}
	if !slices.Contains(CanaryCapNames(), "cover") {
		t.Error("CanaryCapNames lacks cover")
	}
}

func TestCover_NotVerifiedYet(t *testing.T) {
	t.Parallel()
	// The flip is an explicit later PR after the real canary passed.
	if EmbeddedFeatures().has(TaskCover) {
		t.Error("features.verified.json lists cover before the canary verified it")
	}
	b, err := os.ReadFile("features.verified.json")
	if err != nil || strings.Contains(string(b), `"cover"`) {
		t.Errorf("features.verified.json: %v", err)
	}
}

func TestCover_ApplyMode(t *testing.T) {
	t.Parallel()
	base := baseRecipe(t, req(nil))
	srcP := &SrcRef{TrackID: "trk-1", MasterSHA256: shaA, DurationDS: 2000}
	m := 250
	rec, err := Engine{}.ApplyMode(base, engine.ModeParams{Mode: ModeCover, Seed: 8, ID: "c"}, &Patch{Src: srcP, CoverStrengthMilli: &m, Style: sp("lofi")})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := mustParse(t, rec.Canonical)
	if r.Task != TaskCover || r.Class() != ClassTurbo || r.Steps != TurboSteps || r.DurationDS != 0 || r.Caption != "lofi" ||
		r.CoverStrengthMilli == nil || *r.CoverStrengthMilli != 250 || r.Thinking || r.Ref != nil {
		t.Errorf("%+v", r)
	}
	if _, err := (Engine{}).ApplyMode(base, engine.ModeParams{Mode: ModeCover, Seed: 8}, &Patch{Src: srcP}); err == nil || !strings.Contains(err.Error(), "strength_required") {
		t.Errorf("no strength: %v", err)
	}
	if _, err := (Engine{}).ApplyMode(base, engine.ModeParams{Mode: ModeCover, Seed: 8}, &Patch{CoverStrengthMilli: &m}); err == nil || !strings.Contains(err.Error(), "needs a source") {
		t.Errorf("no src: %v", err)
	}
	// a cover child going back to a variant of a cover base is refused like any non-text2music base
	if _, err := (Engine{}).ApplyMode(rec, engine.ModeParams{Mode: ModeNewSeed, Seed: 9}, nil); err == nil {
		t.Error("variant of cover accepted")
	}
	// strength does not leak into a later edit
	rec2, err := Engine{}.ApplyMode(rec, engine.ModeParams{Mode: ModeExtend, Seed: 9}, &Patch{Src: srcP, ExtendDS: ip(300)})
	if err != nil {
		t.Fatal(err)
	}
	if r2, _ := mustParse(t, rec2.Canonical); r2.CoverStrengthMilli != nil {
		t.Errorf("strength leaked: %+v", r2)
	}
}
