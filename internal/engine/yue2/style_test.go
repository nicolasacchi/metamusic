package yue2

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestStyleTerms(t *testing.T) {
	terms, bpm := StyleTerms("Italian, Pop Rock ,  female vocal, 120 BPM")
	if diff := cmp.Diff([]string{"italian", "pop rock", "female vocal"}, terms); diff != "" || bpm != 120 {
		t.Fatalf("%v %d %s", terms, bpm, diff)
	}
	if terms, bpm := StyleTerms("pop, bpm 300"); bpm != 0 || len(terms) != 1 {
		t.Fatalf("%v %d", terms, bpm)
	}
	if _, bpm := StyleTerms("pop; bpm 96"); bpm != 96 {
		t.Fatalf("bpm word-first: %d", bpm)
	}
	if terms, bpm := StyleTerms("pop, 20 bpm"); bpm != 0 || len(terms) != 1 {
		t.Fatalf("%v %d", terms, bpm)
	}
	terms, _ = StyleTerms("Pop, pop ,POP|rock\nrock;;  ")
	if diff := cmp.Diff([]string{"pop", "rock"}, terms); diff != "" {
		t.Fatal(diff)
	}
	var parts []string
	for i := 0; i < 25; i++ {
		parts = append(parts, fmt.Sprintf("term%d", i))
	}
	terms, _ = StyleTerms(strings.Join(parts, ","))
	if len(terms) != 20 || terms[19] != "term19" {
		t.Fatalf("%d", len(terms))
	}
	terms, _ = StyleTerms(strings.Repeat("x", 41) + ",ok")
	if diff := cmp.Diff([]string{"ok"}, terms); diff != "" {
		t.Fatal(diff)
	}
}
