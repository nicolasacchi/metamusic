package archtest

import (
	"slices"
	"strings"
	"testing"
)

// earsRules are the M5b C330.1 / C337.5 edges. internal/ears and the pure internal/ears/tierb reach none of jobs, generation or
// spend (jobs already imports ears, and a listening check never creates GPU work or spend), and neither imports the ACE-Step
// engine (D-EARS-OP-2) or the fake provider. earsjobs, the one package that joins ears and jobs, is the only place they meet.
var earsRules = []rule{
	{from: []string{"internal/ears!", "internal/ears/tierb"},
		deny: []string{"internal/jobs", "internal/generation", "internal/spend", "internal/engine/acestep", "internal/provider"},
		why:  "internal/ears and internal/ears/tierb sit below jobs, generation and spend and never reach ACE-Step or a provider (C330.1, C337.5)"},
}

// earsJobsDeny are the packages internal/ears/earsjobs must not import directly: it analyses stored documents, submits no job and
// spends nothing (C327.1) and never touches ACE-Step or the fake provider (D-EARS-OP-2). The check is on direct imports because
// jobs itself reaches them transitively (the G1 precedent).
var earsJobsDeny = []string{"internal/generation", "internal/spend", "internal/engine/acestep", "internal/provider", "internal/upload"}

// earsTierBImports are the only internal packages internal/ears/tierb may import directly (C330.1). score/abc joins when the
// adherence lane lands it.
var earsTierBImports = []string{"internal/textfold", "internal/engine/yue2", "internal/score/abc"}

func TestImportGraph_Ears(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	msgs, checked := violations(g, earsRules)
	for _, m := range msgs {
		t.Error(m)
	}
	if checked < 2 {
		t.Fatalf("only %d constrained packages found; the table is stale", checked)
	}
	p, ok := g["internal/ears/tierb"]
	if !ok {
		t.Fatal("internal/ears/tierb is not listed")
	}
	for _, imp := range p.Imports {
		rel, internal := strings.CutPrefix(imp, mod)
		switch {
		case internal && !slices.Contains(earsTierBImports, rel):
			t.Errorf("internal/ears/tierb imports %s, which C330.1 does not list", rel)
		case !internal && strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") && !strings.HasPrefix(imp, "golang.org/x/text"):
			t.Errorf("internal/ears/tierb imports third-party %s", imp)
		}
	}
	// the take hook is where ears meets jobs: it exists and is the only ears package that imports jobs
	if hook, ok := g["internal/ears/earsjobs"]; !ok || !slices.Contains(hook.Imports, mod+"internal/jobs") {
		t.Errorf("internal/ears/earsjobs must import internal/jobs (listed: %v)", ok)
	}
	for _, imp := range g["internal/ears/earsjobs"].Imports {
		for _, deny := range earsJobsDeny {
			if rel, ok := strings.CutPrefix(imp, mod); ok && under(rel, deny) {
				t.Errorf("internal/ears/earsjobs imports %s (C327.1, D-EARS-OP-2)", rel)
			}
		}
	}
	for pkg, pk := range g {
		if !under(pkg, "internal/ears") || pkg == "internal/ears/earsjobs" || helperSubtree(pkg) {
			continue
		}
		if slices.Contains(pk.Imports, mod+"internal/jobs") {
			t.Errorf("%s imports internal/jobs; only internal/ears/earsjobs may", pkg)
		}
	}
}
