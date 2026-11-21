package archtest

import (
	"sort"
	"strings"
	"testing"
)

// m6nRule forbids DIRECT (non-test) imports of the M6N packages (C365.1), on top of TestImportGraph_G1. A package that does not exist
// yet is skipped, so the table stays correct as the track lanes land them; a package's non-helper subtree counts with it.
type m6nRule struct {
	pkg       string
	forbidden []string
	why       string
}

var m6nRules = []m6nRule{
	// the authority never reaches the engine room: cancelling is injected (mandates.Canceller), generation asks mandates through a seam
	{"internal/mandates", []string{"internal/jobs", "internal/generation", "internal/api", "internal/app", "internal/httpserver", "internal/nightshift",
		"internal/morning", "internal/criticbench", "internal/provider", "internal/engine"}, "a mandate is the authority, not an actor"},
	{"internal/nightshift", []string{"internal/jobs", "internal/api", "internal/app", "internal/httpserver", "internal/morning", "internal/criticbench"},
		"the night shift acts through generation, never jobs"},
	{"internal/morning", []string{"internal/generation", "internal/spend", "internal/jobs", "internal/mandates", "internal/nightshift"},
		"the Morning Crate never spends"},
	{"internal/criticbench", []string{"internal/duels", "internal/generation", "internal/spend", "internal/jobs"},
		"the bench reads rows with SQL, never the duels service"},
}

func m6nOffenders(g map[string]listedPkg) (off []string, checked int) {
	for pkg, p := range g {
		if helperSubtree(pkg) {
			continue
		}
		for _, r := range m6nRules {
			if !under(pkg, r.pkg) {
				continue
			}
			checked++
			for _, imp := range p.Imports {
				rel, internal := strings.CutPrefix(imp, mod)
				if !internal {
					continue
				}
				for _, f := range r.forbidden {
					if under(rel, f) {
						off = append(off, pkg+" imports "+rel+" ("+r.why+")")
					}
				}
			}
		}
	}
	sort.Strings(off)
	return off, checked
}

// TestImportGraph_M6N pins the M6N package rules (C365.1) on direct imports.
func TestImportGraph_M6N(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	off, checked := m6nOffenders(g)
	for _, o := range off {
		t.Error(o)
	}
	if _, ok := g["internal/mandates"]; !ok || checked < 1 {
		t.Fatalf("internal/mandates not found among %d checked packages; the table is stale", checked)
	}

	// the checks bite: planted offenders are found, allowed edges are not
	planted := map[string]listedPkg{
		"internal/mandates":              {Imports: []string{mod + "internal/spend", mod + "internal/jobs", "database/sql"}},
		"internal/mandates/mandatestest": {Imports: []string{mod + "internal/jobs"}}, // helper subtree: exempt
		"internal/nightshift":            {Imports: []string{mod + "internal/generation", mod + "internal/mandates", mod + "internal/jobs"}},
		"internal/morning":               {Imports: []string{mod + "internal/store", mod + "internal/spend"}},
		"internal/criticbench":           {Imports: []string{mod + "internal/ladder", mod + "internal/duels"}},
		"internal/jobs":                  {Imports: []string{mod + "internal/spend"}}, // not an M6N package
	}
	got, n := m6nOffenders(planted)
	if n != 4 {
		t.Errorf("planted check covered %d packages, want 4", n)
	}
	joined := strings.Join(got, "\n")
	for _, w := range []string{"internal/mandates imports internal/jobs", "internal/nightshift imports internal/jobs",
		"internal/morning imports internal/spend", "internal/criticbench imports internal/duels"} {
		if !strings.Contains(joined, w) {
			t.Errorf("planted offender %q not reported in %v", w, got)
		}
	}
	for _, bad := range []string{"mandatestest", "internal/nightshift imports internal/generation", "internal/nightshift imports internal/mandates",
		"internal/criticbench imports internal/ladder", "internal/jobs imports"} {
		if strings.Contains(joined, bad) {
			t.Errorf("%q was reported but is allowed: %v", bad, got)
		}
	}
}
