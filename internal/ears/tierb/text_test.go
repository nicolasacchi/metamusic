package tierb_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
)

func TestNorm_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, norm, fold string }{
		{"L'amore", "l amore", "l amore"},
		{"L’amore, è così!", "l amore è così", "l amore e cosi"},
		{"  Sotto   il\tcielo  ", "sotto il cielo", "sotto il cielo"},
		{"già-fatto (bis)", "già fatto bis", "gia fatto bis"},
		{"CIELO 2", "cielo 2", "cielo 2"},
		{"é", "é", "e"}, // decomposed accent composes to NFC and keeps its word
		{"", "", ""},
		{"...", "", ""},
	} {
		if got := tierb.Norm(c.in); got != c.norm {
			t.Errorf("Norm(%q) = %q, want %q", c.in, got, c.norm)
		}
		if got := tierb.Fold(c.in); got != c.fold {
			t.Errorf("Fold(%q) = %q, want %q", c.in, got, c.fold)
		}
	}
}

func TestRefWords(t *testing.T) {
	t.Parallel()
	got := tierb.RefWords("[intro]\n\n[verse]\nL'amore è qui\nsotto il cielo\n[outro]\n")
	type w struct {
		S, L, W    int
		Text, Fold string
	}
	var flat []w
	for _, r := range got {
		flat = append(flat, w{r.Section, r.Line, r.Word, r.Text, r.Fold})
	}
	want := []w{
		{1, 0, 0, "L", "l"}, {1, 0, 1, "amore", "amore"}, {1, 0, 2, "è", "e"}, {1, 0, 3, "qui", "qui"},
		{1, 1, 0, "sotto", "sotto"}, {1, 1, 1, "il", "il"}, {1, 1, 2, "cielo", "cielo"},
	}
	if diff := cmp.Diff(want, flat); diff != "" {
		t.Errorf("RefWords (-want +got):\n%s", diff)
	}
	if got[2].Norm != "è" {
		t.Errorf("Norm keeps accents: %q", got[2].Norm)
	}
	if len(tierb.RefWords("[intro]\n[outro]\n")) != 0 {
		t.Error("instrumental sections have no words")
	}
}
