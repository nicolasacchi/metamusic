package yue2

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLyricsSections(t *testing.T) {
	lyr := "[Verse 1]\n  line a  \n\nline b\n[Chorus]\nhook\n[Verse 2]\nv2\n[Chorus]\nhook\n[Outro – Piano + fade]\nbye\n"
	got := LyricsSections(lyr)
	var labels, kinds []string
	for _, s := range got {
		labels = append(labels, s.Label)
		kinds = append(kinds, s.Kind)
	}
	if diff := cmp.Diff([]string{"verse 1", "chorus 1", "verse 2", "chorus 2", "outro"}, labels); diff != "" {
		t.Fatal(diff)
	}
	if diff := cmp.Diff([]string{"verse", "chorus", "verse", "chorus", "outro"}, kinds); diff != "" {
		t.Fatal(diff)
	}
	if diff := cmp.Diff([]string{"line a", "line b"}, got[0].Lines); diff != "" {
		t.Fatal(diff)
	}
	if got[0].Raw != "[Verse 1]" || got[4].Raw != "[Outro – Piano + fade]" || got[3].Idx != 3 {
		t.Fatalf("%+v", got)
	}
}

func TestLyricsSections_Preamble(t *testing.T) {
	got := LyricsSections("stray line\n[Verse]\nla\n")
	if len(got) != 2 || got[0].Kind != "other" || got[0].Lines[0] != "stray line" || got[1].Label != "verse" {
		t.Fatalf("%+v", got)
	}
	if got := LyricsSections("\n  \n[Verse]\nla"); len(got) != 1 {
		t.Fatalf("blank preamble must not create a section: %+v", got)
	}
	if got := LyricsSections(""); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestLyricsSections_Synonyms(t *testing.T) {
	cases := map[string]string{
		"[Pre Chorus]": "pre-chorus", "[Prechorus]": "pre-chorus", "[Pre-Chorus 2]": "pre-chorus",
		"[Instrumental Break]": "instrumental", "[Intro: soft]": "intro", "[Bridge (quiet)]": "bridge",
		"[Bridge-Piano]": "bridge", "[Outro-fade]": "outro", "[Verse-2]": "verse", "[Pre-Chorus - lift]": "pre-chorus",
		"[Solo]": "solo", "[Interlude]": "interlude", "[Whatever]": "other", "[Guitar Solo]": "other",
	}
	for h, kind := range cases {
		got := LyricsSections(h + "\nx")
		if len(got) != 1 || got[0].Kind != kind {
			t.Errorf("%s -> %+v, want %s", h, got, kind)
		}
	}
	// single-occurrence kinds unnumbered; unknown/other labelled by occurrence.
	got := LyricsSections("[Bridge]\na\n[Whatever]\nb\n[Whatever]\nc")
	if got[0].Label != "bridge" || got[1].Label != "other 1" || got[2].Label != "other 2" {
		t.Fatalf("%+v", got)
	}
}
