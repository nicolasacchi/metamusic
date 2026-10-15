package archtest

import (
	"strings"
	"testing"
)

// m4aRules are the C116 edges. Packages that do not exist yet are skipped, so the table stays correct as the
// lanes land them.
var m4aRules = []rule{
	{from: []string{"internal/oidc", "internal/connectors", "internal/subsonic", "internal/media/cover", "internal/media/tagged", "internal/prompts"},
		deny: []string{"internal/api", "internal/gate", "internal/httpserver"},
		why:  "the M4a packages sit below the API layer: they never reach api, gate or httpserver (C116)"},
	{from: []string{"internal/oidc"}, deny: []string{"internal/connectors", "internal/store", "internal/spend"},
		why: "oidc verifies tokens only: it never reaches connectors, store or spend (C116)"},
	{from: []string{"internal/connectors"}, deny: []string{"internal/spend"},
		why: "connectors never imports spend; spend reads grant limits with its own SQL (C106, C116)"},
	{from: []string{"internal/subsonic"}, deny: []string{"internal/spend", "internal/jobs"},
		why: "subsonic reaches the domain only through services, never spend or jobs (C116)"},
}

// violations returns one message per forbidden transitive edge of rules in g, and how many packages the rules
// constrained.
func violations(g map[string]listedPkg, rules []rule) (msgs []string, checked int) {
	for _, r := range rules {
		for pkg, p := range g {
			constrained := false
			for _, f := range r.from {
				if exact, ok := strings.CutSuffix(f, "!"); ok {
					constrained = constrained || pkg == exact
				} else if under(pkg, f) && !helperSubtree(pkg) {
					constrained = true
				}
			}
			if !constrained {
				continue
			}
			checked++
			for _, dep := range p.Deps {
				d := strings.TrimPrefix(dep, mod)
				if d == dep {
					continue
				}
				for _, deny := range r.deny {
					if under(d, deny) && !helperSubtree(d) {
						msgs = append(msgs, pkg+" reaches "+d+": "+r.why)
					}
				}
			}
		}
	}
	return msgs, checked
}

func TestImportGraph_M4a(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	msgs, checked := violations(g, m4aRules)
	for _, m := range msgs {
		t.Error(m)
	}
	// every package that exists today is constrained by at least one rule
	want := 0
	for _, p := range []string{"internal/oidc", "internal/connectors", "internal/subsonic", "internal/media/cover", "internal/media/tagged", "internal/prompts"} {
		if _, ok := g[p]; ok {
			want++
		}
	}
	if checked < want {
		t.Fatalf("only %d constrained packages found, want at least %d; the table is stale", checked, want)
	}
}

// TestImportGraph_M4aRulesBite: the checker finds a deliberately forbidden edge, so a green run is not vacuous.
func TestImportGraph_M4aRulesBite(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	sub, ok := g["internal/subsonic"]
	if !ok {
		t.Skip("internal/subsonic does not exist")
	}
	if len(sub.Deps) == 0 {
		t.Fatal("no dependencies listed for internal/subsonic")
	}
	// subsonic really reaches library; a rule that forbids it must fire.
	msgs, _ := violations(g, []rule{{from: []string{"internal/subsonic"}, deny: []string{"internal/library"}, why: "probe"}})
	if len(msgs) == 0 {
		t.Fatal("a rule over a present edge found nothing")
	}
}
