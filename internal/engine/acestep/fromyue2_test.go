package acestep

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

func warnCodes(is []engine.Issue) string {
	var out []string
	for _, i := range is {
		out = append(out, i.Severity[:1]+":"+i.Code)
	}
	return strings.Join(out, ",")
}

func TestFromYuE2_Golden(t *testing.T) {
	t.Parallel()
	y := yue2.Request{ID: "syn-y", Style: "synthwave, 96 BPM, female vocal", Lyrics: "[verse]\nla la\n[chorus]\nsing along", Cot: "full", Seed: 4242}
	r, is := FromYuE2(y, "syn-a")
	if HasErrors(is) {
		t.Fatal(is)
	}
	secs, _ := yue2.EstimateDuration(y)
	if r.Caption != "synthwave, female vocal" || r.BPM == nil || *r.BPM != 96 || r.Lyrics != "[Verse]\nla la\n\n[Chorus]\nsing along" ||
		r.Seed != 4242 || !r.Thinking || r.Steps != TurboSteps || r.Task != TaskText2Music || r.VocalLanguage != "unknown" {
		t.Errorf("%+v", r)
	}
	if want := min(max(10*int(secs+0.5), 300), 3600); r.DurationDS != want {
		t.Errorf("duration %d want %d", r.DurationDS, want)
	}
	if got := warnCodes(is); got != "w:cot_ignored,w:duration_estimated" {
		t.Errorf("warnings %s", got)
	}
	// the translation must be a valid request the engine accepts and round trips
	raw, _ := json.Marshal(r)
	if is := (Engine{Verified: EmbeddedFeatures()}).Validate(raw); len(is) != 0 {
		t.Errorf("translated request invalid: %v", is)
	}
	got, _ := json.MarshalIndent(map[string]any{"request": r, "warnings": warnCodes(is)}, "", "  ")
	got = append(got, '\n')
	const path = "testdata/fromyue2.golden.json"
	if *update {
		_ = os.WriteFile(path, got, 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != string(got) {
		t.Errorf("golden mismatch (run with -update):\n%s", got)
	}
}

func TestFromYuE2_ABCDropped(t *testing.T) {
	t.Parallel()
	abc := "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=100\nK:C\nCDEF|"
	_, is := FromYuE2(yue2.Request{Style: "jazz", Lyrics: "[verse]\nx y z", Cot: "off", Seed: 1, ABC: &abc}, "syn-a")
	if got := warnCodes(is); got != "w:abc_dropped,w:duration_estimated" {
		t.Errorf("warnings %s", got)
	}
}

func TestFromYuE2_CaptionTruncatedAtRuneBoundary(t *testing.T) {
	t.Parallel()
	style := strings.Repeat("é", MaxCaptionLen+10)
	r, is := FromYuE2(yue2.Request{Style: style, Lyrics: "[verse]\nx y z", Cot: "off"}, "syn-a")
	if n := len([]rune(r.Caption)); n != MaxCaptionLen || !strings.Contains(warnCodes(is), "w:caption_truncated") {
		t.Errorf("caption %d runes, %s", n, warnCodes(is))
	}
	if strings.ContainsRune(r.Caption, '�') {
		t.Error("cut inside a rune")
	}
}

func TestFromYuE2_Variants(t *testing.T) {
	t.Parallel()
	// language: the style tag wins, then detection; instrumental when there are no lyrics; numbered headers
	r, _ := FromYuE2(yue2.Request{Style: "italian pop", Lyrics: "[Verse 2]\nciao\n[Bridge]\nx", Cot: "off"}, "a")
	if r.VocalLanguage != "it" || !strings.HasPrefix(r.Lyrics, "[Verse 2]\nciao") || !strings.Contains(r.Lyrics, "[Bridge]") {
		t.Errorf("%+v", r)
	}
	en := "[verse]\nthe love and the night and you are with me and the world is mine and you know the way"
	if r, _ := FromYuE2(yue2.Request{Style: "pop", Lyrics: en, Cot: "off"}, "a"); r.VocalLanguage != "en" {
		t.Errorf("detected %q", r.VocalLanguage)
	}
	r, _ = FromYuE2(yue2.Request{Style: "ambient", Lyrics: "", Cot: "off"}, "a")
	if !r.Instrumental || r.Lyrics != "" {
		t.Errorf("%+v", r)
	}
	// text before the first header and unknown headers survive; unknown header keeps its own spelling
	r, _ = FromYuE2(yue2.Request{Style: "pop", Lyrics: "hello\n[Spoken Word]\nhey", Cot: "off"}, "a")
	if r.Lyrics != "hello\n\n[Spoken Word]\nhey" {
		t.Errorf("lyrics %q", r.Lyrics)
	}
	// bad id and an empty caption are errors
	if _, is := FromYuE2(yue2.Request{Style: "pop", Lyrics: "[verse]\nx", Cot: "off"}, "../x"); !HasErrors(is) {
		t.Error("bad id accepted")
	}
	if _, is := FromYuE2(yue2.Request{Style: "96 BPM", Lyrics: "[verse]\nx", Cot: "off"}, "a"); !HasErrors(is) {
		t.Error("empty caption accepted")
	}
	// a long song is clamped to 360 s of deciseconds at most
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("[verse]\nline\n")
	}
	if r, _ := FromYuE2(yue2.Request{Style: "pop 40 bpm", Lyrics: b.String(), Cot: "off"}, "a"); r.DurationDS != 3600 {
		t.Errorf("clamp %d", r.DurationDS)
	}
}
