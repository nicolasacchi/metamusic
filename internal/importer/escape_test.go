package importer

import "testing"

func TestEscape_ControlChars(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plain é ü", "plain é ü"},
		{"a\nb", `a\x0ab`},
		{"a\tb\rc", `a\x09b\x0dc`},
		{"nul\x00", `nul\x00`},
		{"esc\x1b[31m", `esc\x1b[31m`},
		{"del\x7f", `del\x7f`},
		{"c1\u0085x", `c1\u0085x`},
		{"c1\u009fx", `c1\u009fx`},
		{"rlo\u202eabc", `rlo\u202eabc`},
		{"lre\u202a pdf\u202c", `lre\u202a pdf\u202c`},
		{"iso\u2066x\u2069", `iso\u2066x\u2069`},
		{"mark\u200e\u200f\u061c", `mark\u200e\u200f\u061c`},
		{"bad\xffbyte", `bad\xffbyte`},
		{"", ""},
	} {
		if got := Escape(c.in); got != c.want {
			t.Errorf("Escape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
