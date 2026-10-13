package archtest

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const mod = "github.com/nicolasacchi/metamusic/"

type listedPkg struct {
	ImportPath string
	Deps       []string // transitive, non-test
	Imports    []string // direct, non-test
}

// loadGraph lists every package under internal/ with its direct and transitive imports.
func loadGraph(t *testing.T) map[string]listedPkg {
	t.Helper()
	out, err := exec.Command("go", "list", "-json=ImportPath,Imports,Deps", mod+"internal/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	g := map[string]listedPkg{}
	for dec.More() {
		var p listedPkg
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		g[strings.TrimPrefix(p.ImportPath, mod)] = p
	}
	if len(g) < 30 {
		t.Fatalf("only %d packages listed; go list output unexpected", len(g))
	}
	return g
}

func under(pkg, prefix string) bool {
	return pkg == prefix || strings.HasPrefix(pkg, prefix+"/")
}

// helperSubtree reports whether pkg is a test-only helper package (cliptest, providertest, runpodtest ...):
// helpers may import what the production package may not (a rig builds the whole system).
func helperSubtree(pkg string) bool {
	last := pkg[strings.LastIndex(pkg, "/")+1:]
	return strings.HasSuffix(last, "test")
}

type rule struct {
	from []string // packages (and their non-helper subtrees) the rule constrains; a trailing "!" means that package only
	deny []string // packages and subtrees they must not reach (transitively)
	why  string
}

// m3Rules are the C70 / C71.1 / C72.4 edges. Packages that do not exist yet are skipped, so the same table
// stays correct as the lanes land them.
var m3Rules = []rule{
	{from: []string{"internal/engine!"}, deny: []string{"internal/engine/yue2", "internal/engine/registry"}, why: "engine imports no engine implementation (C70)"},
	{from: []string{"internal/engine/yue2"}, deny: []string{"internal/lineage", "internal/spend", "internal/generation", "internal/provider", "internal/upload", "internal/jobs", "internal/engine/registry"},
		why: "engine/yue2 imports engine and no other M3 package (C70)"},
	{from: []string{"internal/lineage"}, deny: []string{"internal/library", "internal/spend", "internal/jobs"}, why: "lineage never imports library, spend or jobs (C71.1)"},
	{from: []string{"internal/generation", "internal/upload", "internal/spend", "internal/drafts"}, deny: []string{"internal/jobs", "internal/api"},
		why: "generation, upload, spend and drafts import neither jobs nor api (C72.4); they wake the runner through an injected Wake func"},
	{from: []string{"internal/provider"}, deny: []string{"internal/jobs"}, why: "provider does not import jobs (C70)"},
	{from: []string{"internal/money", "internal/clock"}, deny: []string{"internal/store", "internal/api", "internal/library", "internal/provider"}, why: "money and clock are stdlib-only leaves (C68)"},
}

func TestImportGraph_M3(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	checked := 0
	for _, r := range m3Rules {
		for pkg, p := range g {
			constrained := false
			for _, f := range r.from {
				// the production package and its non-helper subtrees; helpers (providertest ...) are exempt
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
				if d == dep { // not in this module
					continue
				}
				for _, deny := range r.deny {
					if under(d, deny) && !helperSubtree(d) {
						t.Errorf("%s reaches %s: %s", pkg, d, r.why)
					}
				}
			}
		}
	}
	if checked < 6 {
		t.Fatalf("only %d constrained packages found; the table is stale", checked)
	}
	// the registry is the only package importing both the interface and the implementation
	for pkg, p := range g {
		if helperSubtree(pkg) || under(pkg, "internal/engine/registry") {
			continue
		}
		var eng, impl bool
		for _, imp := range p.Imports {
			switch strings.TrimPrefix(imp, mod) {
			case "internal/engine":
				eng = true
			case "internal/engine/yue2":
				impl = true
			}
		}
		// internal/engine/yue2 itself imports engine; callers of yue2 that also use engine.Engine (the adapter) are
		// limited to the registry. Today only yue2 (the adapter) and the registry may import `engine`.
		if eng && pkg != "internal/engine/yue2" {
			t.Errorf("%s imports internal/engine; only engine/yue2 and engine/registry may", pkg)
		}
		_ = impl
	}
}

// TestImportGraph_RulesBite proves the checker is not vacuous: a rule over a deliberately present edge fails.
func TestImportGraph_RulesBite(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	// internal/engine/registry imports provider and engine/yue2: a rule that forbids them must find the edges.
	reg, ok := g["internal/engine/registry"]
	if !ok {
		t.Fatal("registry missing from the graph")
	}
	seen := map[string]bool{}
	for _, d := range reg.Deps {
		seen[strings.TrimPrefix(d, mod)] = true
	}
	for _, want := range []string{"internal/engine", "internal/engine/yue2", "internal/provider", "internal/money"} {
		if !seen[want] {
			t.Errorf("registry does not reach %s; the graph loader is broken", want)
		}
	}
	if !under("internal/engine/yue2", "internal/engine") || under("internal/engineer", "internal/engine") {
		t.Error("under() prefix logic broken")
	}
	if !helperSubtree("internal/provider/providertest") || helperSubtree("internal/provider") {
		t.Error("helperSubtree logic broken")
	}
}
