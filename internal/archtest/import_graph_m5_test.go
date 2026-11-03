package archtest

import (
	"strings"
	"testing"
)

// m5Domain are the M5 domain packages; none of them may reach the API layer (C250).
var m5Domain = []string{"internal/songpr", "internal/callbacks", "internal/attention", "internal/voice", "internal/anchor"}

// m5Rules are the C250 seams. Packages that do not exist yet are skipped, so the table stays correct as the lanes land them.
var m5Rules = []rule{
	{from: []string{"internal/generation"}, deny: []string{"internal/songpr", "internal/callbacks"},
		why: "generation reaches Song PRs and callbacks only through the injected PatchSource and BacklogGate (C250, C253.4, C255.5)"},
	{from: []string{"internal/jobs"}, deny: []string{"internal/callbacks"},
		why: "jobs reaches callbacks only through the injected TakeHook (C250, C255)"},
	{from: m5Domain, deny: []string{"internal/api", "internal/app", "internal/httpserver"},
		why: "the M5 domain packages sit below the API layer (C250)"},
	{from: []string{"internal/anchor"}, deny: []string{"internal/store", "internal/library", "internal/auth", "internal/problem", "internal/listening"},
		why: "anchor is a stdlib-only leaf (C250, C252)"},
}

// m5Imports are the direct internal imports allowed each M5 package that has a tight contract. A direct import outside the
// list needs a freeze fix, not a quiet edit.
var m5Imports = map[string][]string{
	"internal/anchor":    {},
	"internal/attention": {"listening", "anchor", "library", "store", "auth", "problem"},
}

func TestImportGraph_M5(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	msgs, checked := violations(g, m5Rules)
	for _, m := range msgs {
		t.Error(m)
	}
	// generation and jobs exist today; the M5 packages are counted as they land
	if checked < 2 {
		t.Fatalf("only %d constrained packages found; the table is stale", checked)
	}
	for pkg, ok := range m5Imports {
		p, exists := g[pkg]
		if !exists {
			t.Fatalf("%s does not exist", pkg)
		}
		set := map[string]bool{}
		for _, a := range ok {
			set["internal/"+a] = true
		}
		for _, imp := range p.Imports {
			if d := strings.TrimPrefix(imp, mod); d != imp && !set[d] {
				t.Errorf("%s imports %s, which C250 does not list", pkg, d)
			}
		}
	}
	// anchor imports nothing of the repository at all
	if p, ok := g["internal/anchor"]; ok {
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, mod) {
				t.Errorf("internal/anchor imports %s: it must stay stdlib-only", imp)
			}
		}
	}
}

// TestImportGraph_M5RulesBite: the checker finds deliberately forbidden edges, and the allowed-import tables name real packages.
func TestImportGraph_M5RulesBite(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	msgs, checked := violations(g, []rule{
		{from: []string{"internal/attention"}, deny: []string{"internal/listening"}, why: "probe"},
		{from: []string{"internal/library"}, deny: []string{"internal/anchor"}, why: "probe"},
	})
	if len(msgs) < 2 || checked < 2 {
		t.Fatalf("probe rules found %d violations in %d packages", len(msgs), checked)
	}
	for pkg, list := range m5Imports {
		for _, a := range list {
			if _, ok := g["internal/"+a]; !ok {
				t.Errorf("%s lists %s, which is not a package", pkg, a)
			}
		}
	}
}
