package songs_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/songs"
)

func ptr(s string) *string { return &s }

func TestDefaultTitle_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, label, group, req string
		angle                   *string
		want                    string
	}{
		{"label kept", "Title Words", "ex_001", "ex_001_v1", nil, "Title Words"},
		{"label with angle kept", "Title Words", "ex_001", "ex_001_v1", ptr("variant-a"), "Title Words"},
		{"bare slug label with angle", "variant-a", "ex_001", "ex_001_v1", ptr("variant-a"), "Ex 001 · variant a"},
		{"bare slug label without angle", "variant-a", "ex_001", "ex_001_v1", nil, "Ex 001"},
		{"empty label with group", "", "ex_001", "ex_001_v1", ptr("remake-long"), "Ex 001 · remake long"},
		{"empty label and group", "", "", "ex_001_v1", nil, "Ex 001 V1"},
		{"empty label and group ignore angle", "", "", "ex_001_v1", ptr("variant-a"), "Ex 001 V1"},
		{"label equals angle", "Same", "g_1", "g_1_v1", ptr("Same"), "G 1 · Same"},
		{"everything empty", "", "", "", nil, "Untitled"},
		{"v2 label slug", "v2", "p90s_001", "p90s_001_v2", nil, "P90s 001"},
	} {
		if got := songs.DefaultTitle(tc.label, tc.group, tc.req, tc.angle); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	long := strings.Repeat("é", 250)
	got := songs.DefaultTitle(long, "", "x", nil)
	if utf8.RuneCountInString(got) != 200 || !utf8.ValidString(got) {
		t.Errorf("250-rune title must be cut at a rune boundary: %d runes", utf8.RuneCountInString(got))
	}
}

func TestHumanise_Table(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"ex_001_v1":  "Ex 001 V1",
		"p90s_001":   "P90s 001",
		"":           "",
		"__a__b__":   "A B",
		"éclair_one": "Éclair One",
		"already Ok": "Already Ok",
	} {
		if got := songs.Humanise(in); got != want {
			t.Errorf("Humanise(%q) = %q, want %q", in, got, want)
		}
	}
}
