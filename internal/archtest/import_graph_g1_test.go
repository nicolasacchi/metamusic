package archtest

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// g1Packages are the packages the G1 program adds (C320.1), including the Revision 2 addition internal/objstore. Packages that do
// not exist yet are skipped, so the table stays correct as the track lanes land them. A package's subtree counts with it.
var g1Packages = []string{
	"internal/schedule", "internal/jobspec", "internal/ears/tierb", "internal/diction",
	"internal/score", "internal/desk", "internal/mandates", "internal/nightshift", "internal/morning", "internal/criticbench",
	"internal/provider/runpod/pods", "internal/upload/fallback", "internal/objstore",
}

// g1Forbidden are the DIRECT imports no G1 package may have (D-G1-17): the ACE-Step engine and its fake. The check is on direct
// imports only (Revision 2): jobs, generation, httpserver and provider/runpod reach engine/acestep transitively today, so a
// transitive check could list no package that imports them. A new package that wants the stronger guarantee adds its own
// transitive test.
var g1Forbidden = []string{"internal/engine/acestep", "internal/provider/fake"}

// g1Tokens must not appear in a non-test Go file of a G1 package.
var g1Tokens = []string{"BackendRunPodACEStep", "BackendFakeACEStep", `"acestep15"`}

// g1Leaves may import only the standard library and the listed internal packages (C320.3). The plan's sentence is garbled; the rule
// kept here is the narrowest reading that the lanes need: the injectable clock for schedule, canonical JSON for jobspec.
var g1Leaves = map[string][]string{
	"internal/schedule": {"internal/clock"},
	"internal/jobspec":  {"internal/canonjson"},
}

func g1Member(pkg string) (string, bool) {
	for _, p := range g1Packages {
		if under(pkg, p) {
			return p, true
		}
	}
	return "", false
}

// g1ImportOffenders checks direct imports (non-test) of every G1 package in g.
func g1ImportOffenders(g map[string]listedPkg) (off []string, checked int) {
	for pkg, p := range g {
		if helperSubtree(pkg) {
			continue
		}
		root, ok := g1Member(pkg)
		if !ok {
			continue
		}
		checked++
		for _, imp := range p.Imports {
			rel, internal := strings.CutPrefix(imp, mod)
			for _, f := range g1Forbidden {
				if internal && under(rel, f) {
					off = append(off, pkg+" imports "+rel+" (D-G1-17: ACE-Step stays out of the G1 packages)")
				}
			}
			if allow, leaf := g1Leaves[root]; leaf && pkg == root {
				switch {
				case internal:
					if !contains(allow, rel) {
						off = append(off, pkg+" imports "+rel+"; "+root+" may import only the standard library and "+strings.Join(allow, ", "))
					}
				case strings.Contains(strings.SplitN(imp, "/", 2)[0], "."):
					off = append(off, pkg+" imports third-party "+imp+"; "+root+" is a standard-library leaf")
				}
			}
		}
	}
	sort.Strings(off)
	return off, checked
}

func g1TokenOffenders(files map[string]string) (off []string) {
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")]
		if helperSubtree(dir) {
			continue
		}
		if _, ok := g1Member(dir); !ok {
			continue
		}
		for _, tok := range g1Tokens {
			if strings.Contains(src, tok) {
				off = append(off, path+" contains "+tok)
			}
		}
	}
	sort.Strings(off)
	return off
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// TestImportGraph_G1 keeps the new program packages off ACE-Step (C320.3, D-G1-17, direct imports only) and the two leaves small.
func TestImportGraph_G1(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	off, checked := g1ImportOffenders(g)
	for _, o := range off {
		t.Error(o)
	}
	if _, ok := g["internal/schedule"]; !ok || checked < 1 {
		t.Fatalf("internal/schedule not found among %d checked packages; the table is stale", checked)
	}
	root := repoRoot(t)
	files := map[string]string{}
	for p, s := range goFiles(t, filepath.Join(root, "internal")) {
		files["internal/"+p] = s
	}
	for _, o := range g1TokenOffenders(files) {
		t.Error(o)
	}

	// the checks bite: planted offenders are found, allowed edges are not
	planted := map[string]listedPkg{
		"internal/schedule":                     {Imports: []string{"time", mod + "internal/clock"}},
		"internal/jobspec":                      {Imports: []string{"encoding/json", mod + "internal/store", mod + "internal/engine/acestep", "github.com/google/uuid"}},
		"internal/desk":                         {Imports: []string{mod + "internal/provider/fake"}},
		"internal/morning":                      {Imports: []string{mod + "internal/store", mod + "internal/engine/yue2"}},
		"internal/jobs":                         {Imports: []string{mod + "internal/engine/acestep"}}, // not a G1 package: transitive-today, allowed
		"internal/provider/runpod/pods/podtest": {Imports: []string{mod + "internal/provider/fake"}},  // helper subtree exempt
	}
	got, n := g1ImportOffenders(planted)
	if n != 4 {
		t.Errorf("planted check covered %d packages, want 4", n)
	}
	joined := strings.Join(got, "\n")
	for _, w := range []string{"internal/desk imports internal/provider/fake", "internal/jobspec imports internal/engine/acestep",
		"internal/jobspec imports internal/store;", "internal/jobspec imports third-party github.com/google/uuid"} {
		if !strings.Contains(joined, w) {
			t.Errorf("planted offender %q not reported in %v", w, got)
		}
	}
	for _, bad := range []string{"internal/schedule", "internal/morning", "internal/jobs ", "podtest"} {
		if strings.Contains(joined, bad) {
			t.Errorf("%q was reported but is allowed: %v", bad, got)
		}
	}
	tok := g1TokenOffenders(map[string]string{
		"internal/desk/a.go":          `x := "acestep15"`,
		"internal/desk/desktest/b.go": `x := "acestep15"`, // helper subtree
		"internal/jobs/c.go":          `x := "acestep15"`, // not a G1 package
		"internal/morning/d.go":       `// uses BackendFakeACEStep`,
		"internal/schedule/ok.go":     `package schedule`,
	})
	if len(tok) != 2 {
		t.Errorf("planted tokens = %v, want the desk and morning files", tok)
	}
}

// capsWriteRE matches a SQL write to spend_caps.
var capsWriteRE = regexp.MustCompile(`(?i)\b(?:insert\s+(?:or\s+\w+\s+)?into|replace\s+into|update|delete\s+from)\s+spend_caps\b`)

func capsOffenders(files map[string]string) (off []string) {
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")]
		if helperSubtree(dir) || inDir(path, "internal/spend") || inDir(path, "internal/store/migrations") {
			continue
		}
		if capsWriteRE.MatchString(stripLineComments(src)) {
			off = append(off, path+": writes spend_caps")
		}
	}
	sort.Strings(off)
	return off
}

// TestNoCapWriterOutsideSpend: only internal/spend writes spend_caps (C327.2). The M3 SetCaps path lives in spend; a mandate or
// any track package must never raise a cap (D-G1-OP-3). Test helpers (servertest) and the migrations are exempt.
func TestNoCapWriterOutsideSpend(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	all := map[string]string{}
	for p, s := range goFiles(t, filepath.Join(root, "internal")) {
		all["internal/"+p] = s
	}
	for p, s := range goFiles(t, filepath.Join(root, "cmd")) {
		all["cmd/"+p] = s
	}
	if len(all) < 100 {
		t.Fatalf("scanned only %d files", len(all))
	}
	if _, ok := all["internal/spend/caps.go"]; !ok {
		t.Fatal("internal/spend/caps.go not scanned")
	}
	for _, o := range capsOffenders(all) {
		t.Errorf("C327.2 violated: %s", o)
	}
	got := capsOffenders(map[string]string{
		"internal/spend/caps.go":             "var q = `UPDATE spend_caps SET a=1`",
		"internal/mandates/a.go":             "var q = `UPDATE spend_caps SET a=1`",
		"internal/generation/b.go":           "var q = `INSERT OR REPLACE INTO spend_caps(id) VALUES (1)`",
		"internal/generation/c.go":           "var q = `SELECT updated_by FROM spend_caps WHERE id = 1`",
		"internal/generation/d.go":           "// UPDATE spend_caps is spend's job",
		"internal/servertest/rig.go":         "var q = `UPDATE spend_caps SET a=1`",
		"internal/store/migrations/x.sql.go": "var q = `UPDATE spend_caps SET a=1`",
		"cmd/metamusic/e.go":                 "var q = `delete from spend_caps`",
	})
	if len(got) != 3 || !strings.HasPrefix(got[0], "cmd/metamusic/e.go") || !strings.HasPrefix(got[1], "internal/generation/b.go") || !strings.HasPrefix(got[2], "internal/mandates/a.go") {
		t.Errorf("planted cap writers = %v", got)
	}
}
