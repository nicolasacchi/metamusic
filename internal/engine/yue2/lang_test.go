package yue2

import (
	"strings"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	for code, words := range stopWords {
		sentence := strings.Join(words, " ")
		if got := DetectLanguage("[Verse]\n" + sentence); got != code {
			t.Errorf("DetectLanguage(%s list) = %q", code, got)
		}
		if n := len(words); n < 25 {
			t.Errorf("%s list has only %d words", code, n)
		}
	}
	if got := DetectLanguage("[Verse]\noro kala venu\nmira dosa lano pera tilo"); got != "" {
		t.Errorf("invented syllables = %q, want empty", got)
	}
	mixed := strings.Join(stopWords["it"][:10], " ") + " " + strings.Join(stopWords["en"][:10], " ")
	if got := DetectLanguage(mixed); got != "" {
		t.Errorf("50/50 mix = %q, want empty", got)
	}
	if got := DetectLanguage(""); got != "" {
		t.Errorf("empty = %q", got)
	}
	// header lines never count
	if got := DetectLanguage("[the and you that was for are]\noro"); got != "" {
		t.Errorf("header words counted: %q", got)
	}
}
