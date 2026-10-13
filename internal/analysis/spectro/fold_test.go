package spectro

import "testing"

func TestFoldASCII(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{"Città è <&>", "Citta e <&>"},
		{"Straße", "Strasse"},
		{"ok 🎵 go", "ok ? go"},
		{"a\tb\nc\r\x00d\x7fe", "a b c  d e"},
		{"日本", "??"},
		{"", ""},
	} {
		if got := FoldASCII(c.in); got != c.want {
			t.Errorf("FoldASCII(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
