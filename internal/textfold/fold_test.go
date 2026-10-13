package textfold

import "testing"

func TestTextfold_Fold(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"Straße", "Strasse"},
		{"Città", "Citta"},
		{"Ørsted", "Orsted"},
		{"þ", "th"},
		{"Þor", "THor"},
		{"ẞ Æon Œuvre Łódź Đa", "ss AEon OEuvre Lodz Da"},
		{"plain ASCII 123", "plain ASCII 123"},
		{"", ""},
		{"ｆｕｌｌ", "full"},
	} {
		if got := Fold(tc.in); got != tc.want {
			t.Errorf("Fold(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
