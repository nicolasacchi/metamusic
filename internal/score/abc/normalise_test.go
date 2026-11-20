package abc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pitchesOf(t *testing.T, tu *Tune) string {
	t.Helper()
	var sb strings.Builder
	for i := range tu.Voices {
		for _, n := range tu.Notes(i) {
			fmt.Fprintf(&sb, "%d:%d@%v+%v ", i, n.MIDI, n.Start, n.Dur)
		}
	}
	return sb.String()
}

func TestNormalise_SamePitches(t *testing.T) {
	t.Parallel()
	const h = "X:1\nM:4/4\nL:1/4\nK:C\n"
	srcs := []string{
		h + "^Ff F|\n",
		h + "^F f F f|\n",
		h + "_B,B b|\n",
		h + "^F4-|F4|\n",
		h + "[^FF]f|\n",
		h + "^F F z f|g f|\n",
		"X:1\nM:4/4\nL:1/4\nK:F\nB b _B b|B|\n",
		"X:1\nM:4/4\nL:1/4\nK:G\nf F ^f F|=F f|\n",
		h + "^F4-|f4|F4|\n",
		h + "(3^FfF c|\n",
		h + "V:1\n^Ff|\nV:2\nF,^F,|\n",
		h + "[K:D] ^Ff F|\n",
	}
	m, _ := filepath.Glob("testdata/*.abc")
	for _, f := range m {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, string(b))
	}
	for i, s := range srcs {
		tu := mustParse(t, s, DialectYuE2)
		text, changed := Normalise(tu)
		std, err := Parse(text, Options{Dialect: DialectStandard})
		if err != nil {
			t.Fatalf("%d: normalised text does not parse: %v\n%s", i, err, text)
		}
		if got, want := pitchesOf(t, std), pitchesOf(t, tu); got != want {
			t.Fatalf("%d: pitches differ\n got %s\nwant %s\ntext\n%s", i, got, want, text)
		}
		if !changed && string(text) != s {
			t.Fatalf("%d: unchanged but text differs", i)
		}
		if changed && string(text) == s {
			t.Fatalf("%d: changed but identical", i)
		}
	}
}

func TestNormalise_Text(t *testing.T) {
	t.Parallel()
	const h = "X:1\nM:4/4\nL:1/4\nK:C\n"
	rows := []struct{ name, src, want string }{
		{"carry to another octave", h + "^Ff F|\n", h + "^F^f F|\n"},
		{"natural after a sharp elsewhere", h + "^F f|F|\n", h + "^F ^f|F|\n"},
		{"flat key signature carried", "X:1\nM:4/4\nL:1/4\nK:F\nB b|\n", "X:1\nM:4/4\nL:1/4\nK:F\nB b|\n"},
		{"explicit natural in another octave", h + "^F =f F|\n", h + "^F =f =F|\n"},
	}
	for _, r := range rows {
		got, changed := Normalise(mustParse(t, r.src, DialectYuE2))
		if string(got) != r.want {
			t.Errorf("%s: got %q want %q", r.name, got, r.want)
		}
		if changed != (r.src != r.want) {
			t.Errorf("%s: changed %v", r.name, changed)
		}
	}
}

func TestNormalise_StandardAndNilUntouched(t *testing.T) {
	t.Parallel()
	s := "X:1\nM:4/4\nL:1/4\nK:C\n^Ff F|\n"
	text, changed := Normalise(mustParse(t, s, DialectStandard))
	if changed || string(text) != s {
		t.Fatalf("a standard tune is displayed as written: %q %v", text, changed)
	}
	if text, changed := Normalise(nil); text != nil || changed {
		t.Fatalf("nil: %q %v", text, changed)
	}
}
