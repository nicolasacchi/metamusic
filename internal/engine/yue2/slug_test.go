package yue2

import (
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	rows := map[string]string{
		"Città d'Oro — Live!": "citta_d_oro_live",
		"Straße":              "strasse",
		"Ærø":                 "aero",
		"Łódź":                "lodz",
		"ＡＢＣ":                 "abc",
		"Hello 😀 World":       "hello_world",
		"  --x--  ":           "x",
		"Œuvre Đạt Þor":       "oeuvre_dat_thor",
		"":                    "",
		"!!!":                 "",
	}
	for in, want := range rows {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q want %q", in, got, want)
		}
	}
	long := Slug(strings.Repeat("abcdefghij ", 20))
	if len(long) > 80 || long == "" || strings.HasSuffix(long, "_") {
		t.Fatalf("len=%d %q", len(long), long)
	}
	if got := Slug(strings.Repeat("é", 200)); len(got) > 80 {
		t.Fatalf("len=%d", len(got))
	}
}

func TestGroupKeyAndSeedFamily(t *testing.T) {
	rows := []struct{ in, group, family string }{
		{"x_001_v2", "x_001", "x_001_v2"},
		{"song-s1101", "song", "song"},
		{"song_seed7", "song", "song"},
		{"plain", "plain", "plain"},
		{"Song_V3", "Song", "Song_V3"},
		{"a_v2_s5", "a_v2", "a_v2"},
		{"v2", "v2", "v2"},
		{"x.v2", "x.v2", "x.v2"},
	}
	for _, r := range rows {
		if g := GroupKey(r.in); g != r.group {
			t.Errorf("GroupKey(%q) = %q want %q", r.in, g, r.group)
		}
		if f := SeedFamily(r.in); f != r.family {
			t.Errorf("SeedFamily(%q) = %q want %q", r.in, f, r.family)
		}
	}
}
