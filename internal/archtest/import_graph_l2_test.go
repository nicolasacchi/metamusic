package archtest

import (
	"strings"
	"testing"
)

// l2Leaves are the L2 packages that feed an export path (the catalog Subsonic reads, the tape, the cover and liner text):
// none of them may reach Muse text, so they sit below recordview, the only L2 package allowed to read it (C118, C127).
var l2Leaves = []string{"internal/records", "internal/liner", "internal/media/cover", "internal/tape", "internal/albums", "internal/curation"}

// l2Rules are the C118 edges (transitive deny rules). Packages that do not exist yet are skipped, so the same table stays
// correct as the lanes land them.
var l2Rules = []rule{
	{from: l2Leaves, deny: []string{"internal/muses", "internal/songview", "internal/board", "internal/recordview"},
		why: "records, liner, cover, tape, albums and curation never reach muses, songview, board or recordview: the Muse-visibility rule (C118, C127)"},
	{from: []string{"internal/crates"}, deny: []string{"internal/recordview", "internal/api", "internal/app", "internal/httpserver"},
		why: "crates is a leaf: it feeds the list operations and never reaches recordview or the API layer (C118)"},
	{from: []string{"internal/records", "internal/liner", "internal/tape", "internal/albums", "internal/curation", "internal/recordview"},
		deny: []string{"internal/api", "internal/app", "internal/httpserver", "internal/jobs", "internal/generation", "internal/spend", "internal/provider"},
		why:  "the L2 packages sit below the API layer and spend nothing: they import none of api, app, httpserver, jobs, generation, spend, provider (D-L2-22)"},
	{from: []string{"internal/export", "internal/exports", "internal/notify", "internal/audit", "internal/backup", "internal/importer", "internal/jobs",
		"internal/generation", "internal/lineage", "internal/library", "internal/redact", "internal/subsonic"},
		deny: []string{"internal/recordview"}, why: "nothing that exports, notifies, backs up or serves Subsonic reaches recordview (the L1 rule extended, C118)"},
	{from: []string{"internal/picks", "internal/provenance", "internal/muses", "internal/songs", "internal/sessions", "internal/sounds", "internal/songview", "internal/board"},
		deny: []string{"internal/records", "internal/crates", "internal/tape", "internal/albums", "internal/recordview", "internal/liner", "internal/curation"},
		why:  "the dependency points one way: L2 reads L1, no L1 package imports an L2 package (C118)"},
	{from: []string{"internal/tape"}, deny: []string{"internal/records", "internal/crates"}, why: "tape receives a Snapshot: it does not import records or crates (C118)"},
	{from: []string{"internal/records"}, deny: []string{"internal/songs", "internal/picks", "internal/sounds", "internal/sessions", "internal/library", "internal/crates", "internal/tape", "internal/albums"},
		why: "records imports no L1 package and no other L2 package: it reads songs, song_roots, song_picks and pick_events through SQL (C118)"},
	{from: []string{"internal/liner"}, deny: []string{"internal/store", "internal/auth"}, why: "liner is pure: standard library only (C118)"},
}

// l2Imports are the direct imports C118 allows each L2 package (plus problem, which every leaf may use). A direct import
// outside the list needs a freeze fix, not a quiet edit.
var l2Imports = map[string][]string{
	"internal/curation":   {"store", "auth", "problem"},
	"internal/records":    {"store", "auth", "problem"},
	"internal/liner":      {},
	"internal/crates":     {"store", "auth", "songs", "sounds", "library", "problem"},
	"internal/tape":       {"store", "auth", "mediatool", "media/flacmeta", "media/serve", "media/cover", "liner", "textfold", "problem"},
	"internal/albums":     {"store", "records", "textfold", "media/cover", "problem"},
	"internal/recordview": {"store", "auth", "records", "muses", "songs", "sessions", "sounds", "picks", "ears", "curation", "liner", "tape", "media/cover", "library", "problem"},
}

func TestImportGraph_L2(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	msgs, checked := violations(g, l2Rules)
	for _, m := range msgs {
		t.Error(m)
	}
	// every constrained package that exists is counted: the existing cover and Subsonic packages, the L1 leaves and whichever
	// L2 packages have landed
	if checked < 12 {
		t.Fatalf("only %d constrained packages found; the table is stale", checked)
	}
	// subsonic* packages are constrained like the explicit list (every package whose path contains "subsonic")
	for pkg, p := range g {
		if !strings.Contains(pkg, "subsonic") || helperSubtree(pkg) {
			continue
		}
		for _, dep := range p.Deps {
			if d := strings.TrimPrefix(dep, mod); d != dep && under(d, "internal/recordview") {
				t.Errorf("%s reaches %s: nothing that serves Subsonic reaches recordview (C118)", pkg, d)
			}
		}
	}
	for pkg, ok := range l2Imports {
		p, exists := g[pkg]
		if !exists {
			continue
		}
		set := map[string]bool{}
		for _, a := range ok {
			set["internal/"+a] = true
		}
		for _, imp := range p.Imports {
			if d := strings.TrimPrefix(imp, mod); d != imp && !set[d] {
				t.Errorf("%s imports %s, which C118 does not list", pkg, d)
			}
		}
	}
}

// TestImportGraph_L2RulesBite: the checker finds deliberately forbidden edges, so a green run is not vacuous: the cover
// package really imports library and serve, picks really imports store, and the direct-import table names real packages.
func TestImportGraph_L2RulesBite(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	if _, ok := g["internal/media/cover"]; !ok {
		t.Fatal("internal/media/cover is missing from the graph")
	}
	msgs, checked := violations(g, []rule{
		{from: []string{"internal/media/cover"}, deny: []string{"internal/library"}, why: "probe"},
		{from: []string{"internal/picks"}, deny: []string{"internal/store"}, why: "probe"},
	})
	if len(msgs) < 2 || checked < 2 {
		t.Fatalf("probe rules found %d violations in %d packages", len(msgs), checked)
	}
	// the L2 table only names packages that exist or are announced by the plan
	for pkg := range l2Imports {
		for _, a := range l2Imports[pkg] {
			if _, ok := g["internal/"+a]; !ok {
				switch a {
				case "media/flacmeta", "liner", "tape", "records", "curation", "recordview", "albums", "crates":
				default:
					t.Errorf("%s lists %s, which is not a package", pkg, a)
				}
			}
		}
	}
	// the L2 rules are about disjoint sets: no package is both a leaf and recordview
	for _, l := range l2Leaves {
		if under(l, "internal/recordview") {
			t.Errorf("%s is a leaf and recordview", l)
		}
	}
}
