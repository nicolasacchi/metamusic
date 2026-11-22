package yue2

import (
	"reflect"
	"testing"
)

func TestWithBPM_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, style string
		bpm         int
		want        string
		ok          bool
	}{
		{"plain", "pop, 100 BPM", 108, "pop, 108 BPM", true},
		{"prefix form", "BPM 92; rock", 100, "BPM 100; rock", true},
		{"glued", "pop | 100bpm", 90, "pop | 90bpm", true},
		{"trailing words are another term", "pop, 100 bpm and more", 90, "pop, 100 bpm and more", false},
		{"below range hint", "pop, 30 BPM", 90, "pop, 30 BPM", false},
		{"first valid term only", "pop, 120 BPM, 90 BPM", 100, "pop, 100 BPM, 90 BPM", true},
		{"skips an out of range term", "pop, 30 BPM, 90 BPM", 100, "pop, 30 BPM, 100 BPM", true},
		{"no term", "pop, rock", 100, "pop, rock", false},
		{"new value too low", "pop, 100 BPM", 39, "pop, 100 BPM", false},
		{"new value too high", "pop, 100 BPM", 251, "pop, 100 BPM", false},
		{"bounds ok", "pop, 100 BPM", 40, "pop, 40 BPM", true},
		{"upper bound ok", "pop, 100 BPM", 250, "pop, 250 BPM", true},
		{"newline separator", "pop\n 100 BPM\nrock", 120, "pop\n 120 BPM\nrock", true},
		{"case kept", "pop, 100 Bpm", 110, "pop, 110 Bpm", true},
		{"empty", "", 100, "", false},
	} {
		got, ok := WithBPM(c.style, c.bpm)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: WithBPM(%q, %d) = (%q, %v), want (%q, %v)", c.name, c.style, c.bpm, got, ok, c.want, c.ok)
		}
	}
}

func TestWithBPM_AgreesWithStyleTerms(t *testing.T) {
	t.Parallel()
	styles := []string{
		"pop, 100 BPM", "BPM 92; rock", "pop | 100bpm", "pop, 100 bpm and more", "pop, 30 BPM", "pop, 120 BPM, 90 BPM",
		"pop, 30 BPM, 90 BPM", "pop", "", "indie rock, female vocal, 128 BPM, dreamy", "  80   bpm , jazz", "jazz;bpm 70;piano",
		"rock\n140 BPM", "pop, 250 BPM", "pop, 251 BPM", "pop, 40 BPM", "pop, 39 BPM", "BPM 300, pop", "pop, bpm 55 | soul",
		"soul, 99BPM, soul", "lofi, 75 BPM,  chill", "trap;145 bpm;dark", "folk, 100 BPM, 100 BPM", "metal, 200 BPM",
	}
	for _, s := range styles {
		terms0, bpm0 := StyleTerms(s)
		const target = 111
		got, ok := WithBPM(s, target)
		if ok != (bpm0 != 0) {
			t.Errorf("%q: ok = %v but StyleTerms bpm = %d", s, ok, bpm0)
		}
		terms1, bpm1 := StyleTerms(got)
		if ok && bpm1 != target {
			t.Errorf("%q -> %q: StyleTerms bpm = %d, want %d", s, got, bpm1, target)
		}
		if !ok && got != s {
			t.Errorf("%q: unchanged style expected, got %q", s, got)
		}
		if !reflect.DeepEqual(terms0, terms1) {
			t.Errorf("%q -> %q: terms changed %v -> %v", s, got, terms0, terms1)
		}
	}
}
