package features_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/features"
)

// camelotRows is C135.1 written out literally: tonic pitch class, mode, code, name.
var camelotRows = []struct {
	pc         int
	mode, code string
	name       string
}{
	{0, "major", "8B", "C major"}, {7, "major", "9B", "G major"}, {2, "major", "10B", "D major"},
	{9, "major", "11B", "A major"}, {4, "major", "12B", "E major"}, {11, "major", "1B", "B major"},
	{6, "major", "2B", "F# major"}, {1, "major", "3B", "Db major"}, {8, "major", "4B", "Ab major"},
	{3, "major", "5B", "Eb major"}, {10, "major", "6B", "Bb major"}, {5, "major", "7B", "F major"},
	{9, "minor", "8A", "A minor"}, {4, "minor", "9A", "E minor"}, {11, "minor", "10A", "B minor"},
	{6, "minor", "11A", "F# minor"}, {1, "minor", "12A", "C# minor"}, {8, "minor", "1A", "G# minor"},
	{3, "minor", "2A", "Eb minor"}, {10, "minor", "3A", "Bb minor"}, {5, "minor", "4A", "F minor"},
	{0, "minor", "5A", "C minor"}, {7, "minor", "6A", "G minor"}, {2, "minor", "7A", "D minor"},
}

func TestCamelot_Table(t *testing.T) {
	t.Parallel()
	if len(camelotRows) != 24 {
		t.Fatalf("table has %d rows", len(camelotRows))
	}
	for _, r := range camelotRows {
		if got := features.CamelotOf(r.pc, r.mode).String(); got != r.code {
			t.Errorf("CamelotOf(%d,%s) = %s, want %s", r.pc, r.mode, got, r.code)
		}
		if got := features.KeyName(r.pc, r.mode); got != r.name {
			t.Errorf("KeyName(%d,%s) = %q, want %q", r.pc, r.mode, got, r.name)
		}
	}
}

func TestCamelot_Edges(t *testing.T) {
	t.Parallel()
	if got := features.CamelotOf(21, "minor").String(); got != "8A" {
		t.Errorf("pc 21 wraps to 9: %s", got)
	}
	if got := features.CamelotOf(-3, "major").String(); got != "11B" {
		t.Errorf("pc -3 wraps to 9 (A major): %s", got)
	}
	if features.CamelotOf(0, "dorian") != (features.Camelot{}) || (features.Camelot{}).String() != "" {
		t.Error("unknown mode must give the zero Camelot")
	}
	if features.KeyName(0, "dorian") != "" {
		t.Error("unknown mode must give an empty name")
	}
	if got := features.KeyName(-3, "minor"); got != "A minor" {
		t.Errorf("KeyName(-3) = %q", got)
	}
}

func TestKeyName_Roundtrip(t *testing.T) {
	t.Parallel()
	for _, r := range camelotRows {
		pc, mode, ok := features.ParseStyleKey(features.KeyName(r.pc, r.mode))
		if !ok || pc != r.pc || mode != r.mode {
			t.Errorf("roundtrip %s: got (%d,%s,%v)", r.name, pc, mode, ok)
		}
	}
}
