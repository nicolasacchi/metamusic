package yue2

import (
	"strings"
	"testing"
)

func songLyrics(sections int, lines int) string {
	var b strings.Builder
	for i := 0; i < sections; i++ {
		b.WriteString("[Verse]\n")
		for j := 0; j < lines; j++ {
			b.WriteString("oro kala venu\n")
		}
	}
	return b.String()
}

func TestEstimateDuration(t *testing.T) {
	abc := string(abcDoc("M:4/4\nL:1/8\nQ:1/4=120\nK:C", bars(8)))
	untimed := string(abcDoc("M:4/4\nK:C", bars(8)))
	rows := []struct {
		name   string
		req    Request
		want   float64
		basis  string
		over   bool
		approx float64
	}{
		{"timed abc", Request{Style: "pop", Lyrics: songLyrics(4, 4), ABC: &abc}, 16.0, "abc", false, 1e-9},
		{"untimed abc falls back", Request{Style: "pop", Lyrics: songLyrics(4, 4), ABC: &untimed}, 4*4*2*2.4 + 4*2.4, "lyrics_default_bpm", false, 1e-9},
		{"default bpm example", Request{Style: "pop, english", Lyrics: "[Intro]\n" + songLyrics(4, 4)}, 105.6, "lyrics_default_bpm", false, 1e-9},
		{"style bpm", Request{Style: "pop, 120 bpm", Lyrics: songLyrics(4, 4)}, 72.0, "lyrics_style_bpm", false, 1e-9},
		{"60 sections over the cap", Request{Style: "pop", Lyrics: songLyrics(60, 4)}, 0, "lyrics_default_bpm", true, 0},
	}
	for _, tc := range rows {
		got, basis := EstimateDuration(tc.req)
		if basis != tc.basis {
			t.Errorf("%s: basis %q, want %q", tc.name, basis, tc.basis)
		}
		if tc.over {
			if got <= 360 {
				t.Errorf("%s: %v, want > 360", tc.name, got)
			}
			continue
		}
		if d := got - tc.want; d > tc.approx || d < -tc.approx {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
