package textfold

import (
	"strings"
	"testing"
)

// TestSlug_Table pins the file-name rules, including the cases the export helper's clean-filename tests use.
func TestSlug_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in string
		max      int
		want     string
	}{
		{"plain", "Evening Songs", 120, "evening-songs"},
		{"diacritics fold", "Città d'Estate", 120, "citta-d-estate"},
		{"table letters", "Straße Ørsted Łódź", 120, "strasse-orsted-lodz"},
		{"allowed punctuation", "a.b_c-d", 120, "a.b_c-d"},
		{"edges trimmed", "  ..--x--..  ", 120, "x"},
		{"slashes and dots", "../../etc/passwd", 120, "etc-passwd"},
		{"empty", "", 120, ""},
		{"nothing left", "???", 120, ""},
		{"non ASCII only", "音楽", 120, ""},
		{"cut at max", strings.Repeat("a", 130), 120, strings.Repeat("a", 120)},
		{"cut then trimmed", strings.Repeat("a", 118) + "-bcd", 119, strings.Repeat("a", 118)},
		{"max 5", "abcdefgh", 5, "abcde"},
		{"runs are kept", "a  b", 120, "a--b"},
	} {
		if got := Slug(tc.in, tc.max); got != tc.want {
			t.Errorf("%s: Slug(%q, %d) = %q, want %q", tc.name, tc.in, tc.max, got, tc.want)
		}
	}
}

// TestSlug_OutputAlphabet: whatever goes in, only [a-z0-9._-] comes out and the bound holds.
func TestSlug_OutputAlphabet(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"A\x00B", "tab\there", "new\nline", "\u202eRTL", "quote\"s", "emoji \U0001F3B5 x", strings.Repeat("é", 200)} {
		got := Slug(in, 120)
		if len(got) > 120 {
			t.Errorf("Slug(%q) is %d bytes", in, len(got))
		}
		for _, r := range got {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
				t.Errorf("Slug(%q) = %q has %q", in, got, r)
			}
		}
	}
}
