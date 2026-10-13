package main

import (
	"strings"
	"testing"
)

// STUB(M2b-E3) phase: analyze is wired into the dispatcher and the usage text, parses its flags and
// exits 2 "not implemented". M2b-E3 replaces this test with the real command's.
func TestAnalyze_StubExitsTwo(t *testing.T) {
	t.Parallel()
	code, out, errs := do(t, nil, "", "analyze", "--dry-run", "--track", "t1", "--track", "t2", "--jobs", "2", "--json")
	if code != 2 || out != "" || !strings.Contains(errs, "not implemented") {
		t.Errorf("analyze = %d %q %q, want exit 2 with a not implemented message", code, out, errs)
	}
	code, _, errs = do(t, nil, "", "analyze", "--bogus")
	if code != 2 || errs == "" {
		t.Errorf("unknown flag: %d %q, want a usage error (exit 2)", code, errs)
	}
}

func TestUsage_ListsAnalyze(t *testing.T) {
	t.Parallel()
	_, out, _ := do(t, nil, "", "help")
	if !strings.Contains(out, "analyze [--track ID]... [--force] [--dry-run] [--jobs N] [--json]") {
		t.Errorf("usage lacks the analyze line:\n%s", out)
	}
}
