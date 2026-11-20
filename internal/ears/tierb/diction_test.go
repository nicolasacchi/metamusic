package tierb_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func rawOf(t *testing.T, lyrics string, o tierbtest.Opts) tierb.Raw {
	t.Helper()
	r, err := tierb.Parse(tierbtest.FromLyrics(lyrics, o))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDiction_Example(t *testing.T) {
	t.Parallel()
	lyrics := "[verse]\nla chiave del mare\nsotto il cielo\n"
	raw := rawOf(t, lyrics, tierbtest.Opts{
		Subs:  map[string]string{"chiave": "kiave", "mare": "male"},
		Drops: map[string]bool{"il": true},
	})
	res := tierb.Diction(tierb.RefWords(lyrics), raw)
	if len(res.Sections) != 1 || len(res.Sections[0].Lines) != 2 {
		t.Fatalf("shape: %+v", res.Sections)
	}
	l0, l1 := res.Sections[0].Lines[0], res.Sections[0].Lines[1]
	if !near(l0.CER, 3.0/18) || l0.CharEdits != 3 || l0.RefRunes != 18 {
		t.Errorf("line 0 CER = %v (%d/%d), want 3/18", l0.CER, l0.CharEdits, l0.RefRunes)
	}
	if l0.Heard != "la kiave del male" || !near(l0.WER, 0.5) {
		t.Errorf("line 0 heard %q WER %v", l0.Heard, l0.WER)
	}
	var ops0 []string
	for _, w := range l0.Words {
		ops0 = append(ops0, w.Op)
	}
	if diff := cmp.Diff([]string{"M", "S", "M", "S"}, ops0); diff != "" {
		t.Errorf("line 0 ops: %s", diff)
	}
	if !near(l0.Words[1].Sim, 1-2.0/6) || !near(l0.Words[3].Sim, 0.75) {
		t.Errorf("sims %v %v", l0.Words[1].Sim, l0.Words[3].Sim)
	}
	if l1.Words[1].Op != "D" || l1.Words[1].Fold != "il" || l1.CharEdits != 3 || l1.Heard != "sotto cielo" {
		t.Errorf("line 1: %+v", l1)
	}
	if len(res.Mangled) != 2 || res.Mangled[0].RefFold != "chiave" || res.Mangled[0].HeardFold != "kiave" ||
		res.Mangled[1].RefFold != "mare" || res.Mangled[1].Section != 0 || res.Mangled[1].Line != 0 || res.Mangled[1].Word != 3 {
		t.Errorf("mangled: %+v", res.Mangled)
	}
	// overall weighted by reference length: (3 + 3) / (18 + 14)
	if res.RefRunes != 32 || !near(res.CER, 6.0/32) {
		t.Errorf("overall CER %v over %d runes", res.CER, res.RefRunes)
	}
	if !l0.Timed || l0.StartS != 10 {
		t.Errorf("line 0 timing: %v %v", l0.Timed, l0.StartS)
	}
}

func TestDiction_PerfectASR(t *testing.T) {
	t.Parallel()
	lyrics := "[verse]\nla chiave del mare\n[chorus]\nsotto il cielo\n"
	res := tierb.Diction(tierb.RefWords(lyrics), rawOf(t, lyrics, tierbtest.Opts{}))
	if res.CER != 0 || res.WER != 0 || len(res.Mangled) != 0 || len(res.Sections) != 2 {
		t.Errorf("not perfect: %+v", res)
	}
	for _, s := range res.Sections {
		for _, l := range s.Lines {
			if l.CER != 0 || l.WER != 0 || l.Inserted != 0 || !l.Timed {
				t.Errorf("line %+v", l)
			}
		}
	}
}

func TestDiction_InsertionsAssigned(t *testing.T) {
	t.Parallel()
	lyrics := "[verse]\nla chiave\ndel mare\n"
	raw := rawOf(t, lyrics, tierbtest.Opts{})
	// the recogniser hears extra words: one inside line 0, one between the lines, one after the end
	words := raw.ASR.Words
	mk := func(w string, at float64) tierb.Word { return tierb.Word{W: w, Start: at, End: at + 0.1, P: 0.5} }
	raw.ASR.Words = []tierb.Word{words[0], mk(" ehi", 10.5), words[1], mk(" oh", 11.5), words[2], words[3], mk(" fine", 99)}
	res := tierb.Diction(tierb.RefWords(lyrics), raw)
	l0, l1 := res.Sections[0].Lines[0], res.Sections[0].Lines[1]
	if l0.Heard != "la ehi chiave" || l0.Inserted != 1 || l0.WordEdits != 1 {
		t.Errorf("line 0 (insertion inside): %+v", l0)
	}
	if l1.Heard != "oh del mare" || l1.Inserted != 1 {
		t.Errorf("line 1 (insertion between lines belongs to the next): %+v", l1)
	}
	if res.WordEdits != 2 {
		t.Errorf("the trailing insertion is dropped: %d edits", res.WordEdits)
	}
}

func TestDiction_Deterministic(t *testing.T) {
	t.Parallel()
	lyrics := "[verse]\nla chiave del mare\nsotto il cielo\n[chorus]\ncanto con te\n"
	raw := rawOf(t, lyrics, tierbtest.Opts{Subs: map[string]string{"canto": "tanto"}, Drops: map[string]bool{"con": true}})
	a, _ := json.Marshal(tierb.Diction(tierb.RefWords(lyrics), raw))
	b, _ := json.Marshal(tierb.Diction(tierb.RefWords(lyrics), raw))
	if string(a) != string(b) {
		t.Error("two runs differ")
	}
}

func TestDiction_NoASRWords(t *testing.T) {
	t.Parallel()
	lyrics := "[verse]\nla chiave\n"
	raw := rawOf(t, lyrics, tierbtest.Opts{DropSections: map[int]bool{0: true}})
	res := tierb.Diction(tierb.RefWords(lyrics), raw)
	if !near(res.CER, 1) || !near(res.WER, 1) || res.Sections[0].Lines[0].Timed {
		t.Errorf("nothing heard: %+v", res)
	}
}
