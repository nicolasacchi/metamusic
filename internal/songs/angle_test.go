package songs_test

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/songs"
)

func TestAngleSlug_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"Remake Long", "remake-long", true},
		{"_x_", "x", true},
		{"variant-a", "variant-a", true},
		{"Variant__B", "variant-b", true},
		{"123", "123", false},
		{"éé", "", false},
		{"", "", false},
		{"a--b", "a-b", true},
		{strings.Repeat("a", 40), strings.Repeat("a", 32), true},
		{strings.Repeat("a", 31) + "-bcd", strings.Repeat("a", 31), true},
		{"9lives", "9lives", false},
	} {
		got, ok := songs.AngleSlug(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("AngleSlug(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
