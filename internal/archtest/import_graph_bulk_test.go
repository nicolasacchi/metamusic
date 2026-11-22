package archtest

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The bulk-track packages (C391, D-BULK-1): the pod client, backend, poll handler and lifecycle loop, the S3 client and the upload fallback.
// None of them may reach the serverless RunPod package, the ACE-Step engine, the fake provider, the job runner, generation, the HTTP server
// or the API (each of these reaches ACE-Step transitively today), directly or transitively. The app wires everything that crosses those
// lines (internal/app/bulk.go, internal/app/bulk_r2.go). Package paths are compared whole: `internal/jobspec` is not `internal/jobs`.
var (
	bulkRoots = []string{"internal/provider/runpod/pods", "internal/upload/fallback", "internal/objstore"}
	bulkDeny  = []string{"internal/engine/acestep", "internal/provider/runpod", "internal/provider/fake", "internal/jobs", "internal/generation",
		"internal/httpserver", "internal/api"}
	// bulkTokens must not appear in a non-test Go file of a bulk package.
	bulkTokens = []string{"BackendRunPodACEStep", "BackendFakeACEStep", `"acestep15"`}
	// bulkPodsImports are the only internal packages internal/provider/runpod/pods may import directly (C391.1). problem and reqinfo are the
	// poll handler's (BULK-P3): the problem+json writer and the shutdown signal of a held request.
	bulkPodsImports = []string{"internal/provider", "internal/config", "internal/money", "internal/spend", "internal/store", "internal/clock",
		"internal/redact", "internal/audit", "internal/problem", "internal/reqinfo"}
	// bulkObjstoreImports: stdlib and redact only.
	bulkObjstoreImports = []string{"internal/redact"}
)

func bulkMember(pkg string) (string, bool) {
	for _, r := range bulkRoots {
		if under(pkg, r) {
			return r, true
		}
	}
	return "", false
}

// bulkOffenders checks the transitive dependencies of every bulk package, and the direct imports of the two with a closed list.
func bulkOffenders(g map[string]listedPkg) (off []string, checked int) {
	for pkg, p := range g {
		if helperSubtree(pkg) {
			continue
		}
		root, ok := bulkMember(pkg)
		if !ok {
			continue
		}
		checked++
		for _, d := range p.Deps {
			rel, internal := strings.CutPrefix(d, mod)
			if !internal {
				continue
			}
			for _, deny := range bulkDeny {
				if rel == deny || (deny != "internal/provider/runpod" && under(rel, deny)) {
					off = append(off, pkg+" reaches "+rel+" (C391.2)")
				}
			}
		}
		var allow []string
		switch pkg {
		case "internal/provider/runpod/pods":
			allow = bulkPodsImports
		case "internal/objstore":
			allow = bulkObjstoreImports
		default:
			continue
		}
		for _, imp := range p.Imports {
			rel, internal := strings.CutPrefix(imp, mod)
			switch {
			case internal && !slices.Contains(allow, rel):
				off = append(off, pkg+" imports "+rel+"; "+root+" may import "+strings.Join(allow, ", ")+" (C391.1)")
			case !internal && strings.Contains(strings.SplitN(imp, "/", 2)[0], "."):
				off = append(off, pkg+" imports third-party "+imp+" (C391.1)")
			}
		}
	}
	sort.Strings(off)
	return off, checked
}

func bulkTokenOffenders(files map[string]string) (off []string) {
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")]
		if helperSubtree(dir) {
			continue
		}
		if _, ok := bulkMember(dir); !ok {
			continue
		}
		for _, tok := range bulkTokens {
			if strings.Contains(src, tok) {
				off = append(off, path+" contains "+tok)
			}
		}
	}
	sort.Strings(off)
	return off
}

// TestImportGraph_Bulk keeps the bulk packages off the serverless backend, ACE-Step, the fake provider and the layers above them
// (transitive, C391.2), and keeps the pod package and the S3 client to their declared imports (C391.1).
func TestImportGraph_Bulk(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	off, checked := bulkOffenders(g)
	for _, o := range off {
		t.Error(o)
	}
	for _, must := range bulkRoots[:1] {
		if _, ok := g[must]; !ok || checked < 1 {
			t.Fatalf("%s not found among %d checked packages; the table is stale", must, checked)
		}
	}
	root := repoRoot(t)
	files := map[string]string{}
	for p, s := range goFiles(t, filepath.Join(root, "internal")) {
		files["internal/"+p] = s
	}
	for _, o := range bulkTokenOffenders(files) {
		t.Error(o)
	}

	// the checks bite: planted offenders are found, allowed edges and helper packages are not
	planted := map[string]listedPkg{
		"internal/provider/runpod/pods": {Imports: []string{"time", mod + "internal/store", mod + "internal/jobs", "github.com/google/uuid"},
			Deps: []string{mod + "internal/store", mod + "internal/jobspec", mod + "internal/engine/acestep", mod + "internal/provider/runpod"}},
		"internal/objstore":                     {Imports: []string{mod + "internal/redact", mod + "internal/store"}, Deps: []string{mod + "internal/redact", mod + "internal/provider/fake"}},
		"internal/upload/fallback":              {Deps: []string{mod + "internal/jobspec", mod + "internal/httpserver"}},
		"internal/provider/runpod/pods/podtest": {Deps: []string{mod + "internal/provider/fake"}},  // helper subtree exempt
		"internal/jobs":                         {Deps: []string{mod + "internal/engine/acestep"}}, // not a bulk package
	}
	got, n := bulkOffenders(planted)
	if n != 3 {
		t.Errorf("planted check covered %d packages, want 3", n)
	}
	joined := strings.Join(got, "\n")
	for _, w := range []string{"pods imports internal/jobs;", "pods reaches internal/engine/acestep", "pods reaches internal/provider/runpod (C391.2)",
		"pods imports third-party github.com/google/uuid", "internal/objstore imports internal/store;", "internal/objstore reaches internal/provider/fake",
		"internal/upload/fallback reaches internal/httpserver"} {
		if !strings.Contains(joined, w) {
			t.Errorf("planted offender %q not reported in %v", w, got)
		}
	}
	for _, bad := range []string{"jobspec", "podtest", "internal/jobs reaches"} {
		if strings.Contains(joined, bad) {
			t.Errorf("%q was reported but is allowed: %v", bad, got)
		}
	}
	tok := bulkTokenOffenders(map[string]string{
		"internal/objstore/a.go":                  `x := "acestep15"`,
		"internal/provider/runpod/pods/b.go":      `// BackendFakeACEStep`,
		"internal/provider/runpod/pods/ok.go":     `package pods`,
		"internal/objstore/objstoretest/c.go":     `x := "acestep15"`, // helper subtree
		"internal/jobs/d.go":                      `x := "acestep15"`, // not a bulk package
		"internal/provider/runpod/pods/e_test.go": ``,
	})
	if len(tok) != 2 {
		t.Errorf("planted tokens = %v, want the objstore and pods files", tok)
	}
}
