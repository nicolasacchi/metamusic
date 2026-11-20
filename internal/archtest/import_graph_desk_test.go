package archtest

import (
	"strings"
	"testing"
)

// deskPureAllow lists, per package of the score toolchain (C350.2), the module packages its direct non-test imports may
// name. Everything else it imports is standard library. Packages that do not exist yet are skipped.
var deskPureAllow = map[string][]string{
	"internal/score":       {},
	"internal/score/abc":   {"internal/score"},
	"internal/score/smf":   {"internal/score"},
	"internal/score/synth": {"internal/score", "internal/mediatool"},
}

// deskNever are packages that must not reach internal/desk (transitively): the desk sits above them.
var deskNever = []string{"internal/generation", "internal/jobs", "internal/transform", "internal/library", "internal/songpr"}

func TestImportGraph_Desk(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	for _, must := range []string{"internal/score", "internal/score/abc"} {
		if _, ok := g[must]; !ok {
			t.Fatalf("%s does not exist", must)
		}
	}
	checked := 0
	for pkg, allow := range deskPureAllow {
		p, ok := g[pkg]
		if !ok {
			continue
		}
		checked++
		for _, imp := range p.Imports {
			d := strings.TrimPrefix(imp, mod)
			if d == imp {
				if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
					t.Errorf("%s imports %s: the score toolchain uses only the standard library", pkg, imp)
				}
				continue
			}
			okDep := false
			for _, a := range allow {
				okDep = okDep || d == a
			}
			if !okDep {
				t.Errorf("%s imports %s: allowed module imports are %v (C350.2)", pkg, d, allow)
			}
		}
	}
	if checked < 2 {
		t.Fatalf("only %d score packages found", checked)
	}
	// the pure packages reach nothing else of the application, transitively
	for _, pkg := range []string{"internal/score", "internal/score/abc", "internal/score/smf"} {
		for _, dep := range g[pkg].Deps {
			d := strings.TrimPrefix(dep, mod)
			if d != dep && d != "internal/score" {
				t.Errorf("%s reaches %s", pkg, d)
			}
		}
	}
	for _, pkg := range deskNever {
		for _, dep := range g[pkg].Deps {
			if under(strings.TrimPrefix(dep, mod), "internal/desk") {
				t.Errorf("%s reaches %s: desk sits above generation, jobs, transform, library and songpr", pkg, dep)
			}
		}
	}
	for _, dep := range g["internal/desk"].Deps {
		if under(strings.TrimPrefix(dep, mod), "internal/api") {
			t.Errorf("internal/desk reaches %s: desk never imports api", dep)
		}
	}
}

// TestImportGraph_DeskRulesBite: the checker sees the edge it polices, so a green run is not vacuous.
func TestImportGraph_DeskRulesBite(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	imports := false
	for _, imp := range g["internal/score/abc"].Imports {
		imports = imports || strings.TrimPrefix(imp, mod) == "internal/score"
	}
	if !imports {
		t.Fatal("internal/score/abc no longer imports internal/score: the allow-list is checking nothing")
	}
	if len(g["internal/score/abc"].Deps) == 0 {
		t.Fatal("no transitive deps loaded for score/abc")
	}
}
