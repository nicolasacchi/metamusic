package tierb_test

import (
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

const measuredLyrics = "[intro]\n\n[verse]\nla chiave del mare\nsotto il cielo\n\n[interlude]\n\n[chorus]\ncanto con te\ncanto con te\n\n[outro]\n"

func measure(t *testing.T, lyrics string, o tierbtest.Opts) ([]tierb.Measured, bool, tierb.Raw) {
	t.Helper()
	raw := rawOf(t, lyrics, o)
	ref := tierb.RefWords(lyrics)
	got, ok := tierb.MeasuredSections(ref, yue2.LyricsSections(lyrics), tierb.AlignRaw(ref, raw), raw)
	return got, ok, raw
}

func TestMeasuredSections_FromWords(t *testing.T) {
	t.Parallel()
	o := tierbtest.Opts{StartS: 13, Subs: map[string]string{"chiave": "kiave"}}
	got, ok, raw := measure(t, measuredLyrics, o)
	if !ok || len(got) != 5 {
		t.Fatalf("ok=%v, %d sections", ok, len(got))
	}
	truth := tierbtest.Truth(measuredLyrics, o)
	for i, sp := range truth {
		if !sp.Vocal {
			continue
		}
		if d := math.Abs(got[i].StartS - sp.StartS); d > 0.26 {
			t.Errorf("section %d starts at %.2f, truth %.2f", i, got[i].StartS, sp.StartS)
		}
		if got[i].Confidence < 0.9 {
			t.Errorf("section %d confidence %v", i, got[i].Confidence)
		}
	}
	// the leading intro fills 0 .. first vocal start; the outro starts 0.5 s after the last word
	if got[0].Kind != "intro" || got[0].StartS != 0 || math.Abs(got[0].EndS-got[1].StartS) > 1e-9 || got[0].Confidence != 0.5 {
		t.Errorf("intro: %+v", got[0])
	}
	if want := truth[3].EndS + 0.5; math.Abs(got[4].StartS-want) > 1e-9 {
		t.Errorf("outro starts %.2f, want %.2f", got[4].StartS, want)
	}
	// the interlude sits in the gap after the verse's last word
	if want := truth[1].EndS + 0.5; math.Abs(got[2].StartS-want) > 1e-9 || got[2].EndS != got[3].StartS {
		t.Errorf("interlude: %+v want start %.2f", got[2], want)
	}
	if got[4].EndS != raw.Input.DurationS {
		t.Errorf("last end %v, duration %v", got[4].EndS, raw.Input.DurationS)
	}
	for i := 1; i < len(got); i++ {
		if got[i].StartS != got[i-1].EndS {
			t.Errorf("section %d is not contiguous", i)
		}
	}
	// rows keep the lyric header and lines
	if got[1].Raw != "[verse]" || got[1].Label != "verse" || len(got[1].Lines) != 2 || got[1].Lines[1] != "sotto il cielo" {
		t.Errorf("verse row: %+v", got[1])
	}
	if got[3].Label != "chorus" || got[3].Idx != 3 {
		t.Errorf("chorus row: %+v", got[3])
	}
}

func TestMeasuredSections_AlignmentFailed(t *testing.T) {
	t.Parallel()
	// the recogniser heard nothing of the verse and the chorus: less than half of the vocal sections align
	got, ok, _ := measure(t, measuredLyrics, tierbtest.Opts{DropSections: map[int]bool{1: true, 3: true}})
	if ok || got != nil {
		t.Errorf("ok=%v, %d sections", ok, len(got))
	}
	// no instrumental-only song, no words at all
	if _, ok, _ := measure(t, "[intro]\n[outro]\n", tierbtest.Opts{}); ok {
		t.Error("a song without words cannot be measured")
	}
	// a section squeezed under a second is refused
	short := "[verse]\nla chiave\n[outro]\n"
	if _, ok, _ := measure(t, short, tierbtest.Opts{DurationS: 11.6}); ok {
		t.Error("outro shorter than 1 s accepted")
	}
}

func TestMeasuredSections_UnalignedVocalSectionInterpolated(t *testing.T) {
	t.Parallel()
	// one of three vocal sections is not heard: the other two still anchor, the missing one gets confidence 0
	lyrics := "[verse]\nla chiave del mare\n[bridge]\nsotto il cielo\n[chorus]\ncanto con te\n"
	got, ok, _ := measure(t, lyrics, tierbtest.Opts{DropSections: map[int]bool{1: true}})
	if !ok || len(got) != 3 {
		t.Fatalf("ok=%v %d", ok, len(got))
	}
	if got[1].Confidence != 0 || got[1].StartS <= got[0].StartS || got[1].StartS >= got[2].StartS {
		t.Errorf("bridge: %+v between %v and %v", got[1], got[0].StartS, got[2].StartS)
	}
}
