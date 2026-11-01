package features_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/features"
)

func TestParseStyleKey_Table(t *testing.T) {
	t.Parallel()
	rows := []struct {
		style string
		pc    int
		mode  string
		ok    bool
	}{
		{"reggae, in A minor, 76bpm", 9, "minor", true},
		{"in a minor", 9, "minor", true},
		{"in A Minor", 9, "minor", true},
		{"in F# major", 6, "major", true},
		{"F# major anthem", 6, "major", true},
		{"Bb minor", 10, "minor", true},
		{"A minor", 9, "minor", true},
		{"warm, Am", 9, "minor", true},
		{"pop, F#m, upbeat", 6, "minor", true},
		{"Bbm", 10, "minor", true},
		{"jam in e minor groove", 4, "minor", true},
		{"rock, in E♭ major", 3, "major", true},
		{"C♯ minor ballad", 1, "minor", true},
		{"in Bb maj", 10, "major", true},
		{"a major hit", 0, "", false},
		{"drama", 0, "", false},
		{"major label vibes", 0, "", false},
		{"Amazing", 0, "", false},
		{"Am groove", 0, "", false},
		{"begin a minor", 0, "", false},
		{"", 0, "", false},
	}
	for _, r := range rows {
		pc, mode, ok := features.ParseStyleKey(r.style)
		if ok != r.ok || pc != r.pc || mode != r.mode {
			t.Errorf("ParseStyleKey(%q) = (%d,%q,%v), want (%d,%q,%v)", r.style, pc, mode, ok, r.pc, r.mode, r.ok)
		}
	}
}

func TestParseABCKey_Table(t *testing.T) {
	t.Parallel()
	hdr := func(k string) string { return "X:1\nT:Synthetic\nM:4/4\nL:1/8\nK:" + k + "\nCDEF|\n" }
	rows := []struct {
		abc  string
		pc   int
		mode string
		ok   bool
	}{
		{hdr("C"), 0, "major", true},
		{hdr("Am"), 9, "minor", true},
		{hdr("F#m"), 6, "minor", true},
		{hdr("Bb"), 10, "major", true},
		{hdr("D mix"), 2, "major", true},
		{hdr("G dor"), 7, "minor", true},
		{hdr("E phr"), 4, "minor", true},
		{hdr("F lyd"), 5, "major", true},
		{hdr("Cmaj"), 0, "major", true},
		{hdr("A minor % comment"), 9, "minor", true},
		{hdr("none"), 0, "", false},
		{hdr("HP"), 0, "", false},
		{hdr("C exp ^f"), 0, "", false},
		{"X:1\nT:No key\nCDEF|\n", 0, "", false},
		{"X:1\r\nK:D\r\nK:Am\r\n", 2, "major", true}, // the first K: wins
		{"X:1\nK:none\nK:C\n", 0, "", false},
		{"", 0, "", false},
	}
	for i, r := range rows {
		pc, mode, ok := features.ParseABCKey(r.abc)
		if ok != r.ok || pc != r.pc || mode != r.mode {
			t.Errorf("row %d ParseABCKey(%q) = (%d,%q,%v), want (%d,%q,%v)", i, r.abc, pc, mode, ok, r.pc, r.mode, r.ok)
		}
	}
}
