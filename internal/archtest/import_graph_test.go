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
		// Packages may use the interface (generation, jobs ...) or the implementation's domain types (lineage,
		// drafts ...); only the registry connects the two (M3-B2 relaxed B1's stricter first draft, which also
		// forbade the legitimate engine.Registry fields of generation and jobs).
		if eng && impl {
			t.Errorf("%s imports both internal/engine and internal/engine/yue2; only engine/registry may", pkg)
		}
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

// l1Rules are the C87 / C99 edges of the L1 packages (transitive deny rules). Packages that do not exist yet are skipped.
var l1Rules = []rule{
	{from: []string{"internal/muses", "internal/songs", "internal/sessions", "internal/picks", "internal/sounds", "internal/provenance", "internal/songview", "internal/board"},
		deny: []string{"internal/api", "internal/app", "internal/httpserver", "internal/generation", "internal/jobs", "internal/library", "internal/drafts", "internal/duels",
			"internal/ears", "internal/judgments", "internal/export", "internal/exports", "internal/importer", "internal/notify"},
		why: "the L1 packages import none of api, app, httpserver, generation, jobs, library, drafts, duels, ears, judgments, export*, importer, notify (C87)"},
	{from: []string{"internal/muses", "internal/songs", "internal/sessions", "internal/picks", "internal/sounds"},
		deny: []string{"internal/songview", "internal/provenance", "internal/board"}, why: "a leaf never imports songview, provenance or board (C87)"},
	{from: []string{"internal/muses"}, deny: []string{"internal/songs", "internal/sessions", "internal/picks", "internal/sounds"},
		why: "muses imports no other L1 package (revision 2 removed the muses -> songs cycle)"},
	{from: []string{"internal/songs"}, deny: []string{"internal/sessions", "internal/picks", "internal/sounds"},
		why: "songs imports neither picks, sounds nor sessions (get_song lives in songview)"},
	{from: []string{"internal/sessions"}, deny: []string{"internal/muses", "internal/songs", "internal/picks", "internal/sounds"}, why: "sessions imports no other L1 package"},
	{from: []string{"internal/picks"}, deny: []string{"internal/muses", "internal/songs", "internal/sessions", "internal/sounds", "internal/duels"},
		why: "picks imports ladder (pure types) but not duels: the ladder arrives through the picks.Ladder interface (C87)"},
	{from: []string{"internal/sounds"}, deny: []string{"internal/muses", "internal/sessions", "internal/picks"}, why: "sounds may import songs, not muses, sessions or picks"},
	{from: []string{"internal/board"}, deny: []string{"internal/sessions", "internal/picks"}, why: "board imports store, muses, songs, sounds only"},
	// C99.1: the Muse-visibility rule. A package that serialises, ships or announces data must never reach Muse text.
	{from: []string{"internal/export", "internal/exports", "internal/notify", "internal/audit", "internal/backup", "internal/importer", "internal/jobs",
		"internal/generation", "internal/lineage", "internal/library", "internal/redact", "internal/subsonic"},
		deny: []string{"internal/muses", "internal/songview", "internal/board"}, why: "Muse text never reaches exports, notifications, audit, backups, the importer, jobs, generation, lineage, library, redact or Subsonic (C99.1)"},
}

func TestImportGraph_L1(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	checked := 0
	for _, r := range l1Rules {
		for pkg, p := range g {
			constrained := false
			for _, f := range r.from {
				if under(pkg, f) && !helperSubtree(pkg) {
					constrained = true
				}
			}
			// every package whose path contains "subsonic" is constrained like the explicit list (C99.1)
			if strings.Contains(r.why, "Subsonic") && strings.Contains(pkg, "subsonic") && !helperSubtree(pkg) {
				constrained = true
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
						t.Errorf("%s reaches %s: %s", pkg, d, r.why)
					}
				}
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d constrained packages found; the table is stale", checked)
	}
	// Direct imports of the L1 packages are limited to the "may import" column of C87 (plus problem, which every leaf
	// may use, and the standard library / third parties).
	allowed := map[string][]string{
		"internal/muses":      {"store", "auth", "textfold", "redact", "problem"},
		"internal/songs":      {"store", "auth", "muses", "textfold", "problem"},
		"internal/sessions":   {"store", "auth", "problem"},
		"internal/picks":      {"store", "auth", "ladder", "problem"},
		"internal/sounds":     {"store", "auth", "songs", "engine/yue2", "textfold", "problem"},
		"internal/provenance": {"store", "muses", "songs", "sessions", "picks", "sounds", "ladder", "problem"},
		"internal/songview":   {"store", "auth", "muses", "songs", "sessions", "picks", "sounds", "ladder", "problem"},
		"internal/board":      {"store", "auth", "muses", "songs", "sounds", "problem"},
	}
	for pkg, ok := range allowed {
		p, exists := g[pkg]
		if !exists {
			continue
		}
		set := map[string]bool{}
		for _, a := range ok {
			set["internal/"+a] = true
		}
		for _, imp := range p.Imports {
			d := strings.TrimPrefix(imp, mod)
			if d != imp && !set[d] {
				t.Errorf("%s imports %s, which C87 does not list", pkg, d)
			}
		}
	}
}

// TestImportGraph_L1_NoCycles pins the two cycles revision 2 removed: muses -> songs -> muses and songs -> picks/sounds.
func TestImportGraph_L1_NoCycles(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	reach := func(a, b string) bool {
		p, ok := g[a]
		if !ok {
			return false
		}
		for _, d := range p.Deps {
			if strings.TrimPrefix(d, mod) == b {
				return true
			}
		}
		return false
	}
	if reach("internal/muses", "internal/songs") && reach("internal/songs", "internal/muses") {
		t.Error("muses <-> songs cycle")
	}
	if reach("internal/songs", "internal/picks") || reach("internal/songs", "internal/sounds") {
		t.Error("songs must not reach picks or sounds")
	}
}
