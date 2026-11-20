package tierbtest_test

import (
	"bytes"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
)

const lyrics = "[intro]\n\n[verse]\nla chiave del mare\nsotto il cielo\n\n[chorus]\ncanto con te\n"

func TestFakeTierB_Deterministic(t *testing.T) {
	t.Parallel()
	o := tierbtest.Opts{Transcribe: true, Subs: map[string]string{"mare": "male"}, Drops: map[string]bool{"il": true}}
	a, b := tierbtest.FromLyrics(lyrics, o), tierbtest.FromLyrics(lyrics, o)
	if !bytes.Equal(a, b) {
		t.Error("two calls differ")
	}
	if bytes.Equal(a, tierbtest.FromLyrics(lyrics, tierbtest.Opts{Transcribe: true})) {
		t.Error("options do not change the document")
	}
	raw, err := tierb.Parse(a)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Pitch == nil || len(raw.Pitch.Cents) != len(raw.Pitch.Conf) {
		t.Fatalf("pitch: %+v", raw.Pitch)
	}
	voiced := 0
	for _, c := range raw.Pitch.Conf {
		if c > 0 {
			voiced++
		}
	}
	if voiced == 0 {
		t.Error("no voiced frames")
	}
	if n := int(raw.Input.DurationS/tierb.PitchHopS + 0.999); len(raw.Pitch.Cents) != n {
		t.Errorf("%d frames for %.2f s", len(raw.Pitch.Cents), raw.Input.DurationS)
	}
	// 10 lyric words, one dropped
	if len(raw.ASR.Words) != 9 || len(raw.ASR.Segments) != 3 {
		t.Errorf("%d words, %d segments", len(raw.ASR.Words), len(raw.ASR.Segments))
	}
}

func TestFakeTierB_Variants(t *testing.T) {
	t.Parallel()
	for name, o := range map[string]tierbtest.Opts{
		"plain": {}, "auto language": {Lang: "auto"}, "error": {Status: "error"}, "oom": {Status: "error", Code: "OOM"},
		"sections dropped": {DropSections: map[int]bool{1: true, 2: true}}, "explicit duration": {DurationS: 120, Transcribe: true},
	} {
		raw, err := tierb.Parse(tierbtest.FromLyrics(lyrics, o))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if name == "auto language" && raw.ASR.Forced {
			t.Error("auto language is not forced")
		}
	}
	if _, ok := (tierbtest.Script{BySHA: map[string]string{"abc": lyrics}}).Lyrics("abc"); !ok {
		t.Error("Script.Lyrics")
	}
}
