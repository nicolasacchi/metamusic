package textfold_test

import (
	"sort"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/textfold"
)

func TestNorm_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"case", "Mixed CASE", "mixed case"},
		{"spaces tabs nbsp", "a \t b  c  d", "a b c d"},
		{"diacritics", "Ärtist Éclair", "artist eclair"},
		{"hyphen kept", "Hip-Hop", "hip-hop"},
		{"trim", "  x  ", "x"},
		{"table letters", "Straße Ørsted", "strasse orsted"},
		{"empty", " \t ", ""},
	} {
		if got := textfold.Norm(tc.in); got != tc.want {
			t.Errorf("%s: Norm(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	got := []string{textfold.Norm("Zebra"), textfold.Norm("apple")}
	sort.Strings(got)
	if got[0] != "apple" {
		t.Errorf("Norm(Zebra) must sort after Norm(apple): %v", got)
	}
}
