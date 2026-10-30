package archtest

import (
	"strings"
	"testing"
)

// l3NextActionDeny are the packages that must never reach `nextactions`, the one new reader of Muse text (C133, C144.2).
var l3NextActionDeny = []string{"internal/nextactions"}

// l3Rules are the C133 edges (transitive deny rules). Packages that do not exist yet are skipped, so the same table stays
// correct as the L3 lanes land them.
var l3Rules = []rule{
	{from: []string{"internal/sequencer"}, deny: []string{"internal/store", "internal/auth", "internal/sounds", "internal/songs", "internal/features", "internal/analysis", "internal/problem", "internal/textfold"},
		why: "sequencer is pure: standard library only (C133)"},
	{from: []string{"internal/provenance", "internal/picks", "internal/sounds"}, deny: []string{"internal/features"},
		why: "the Reconciler and the recompute hook reach features through an interface wired in app: provenance, picks and sounds never import features (C133, D-L3-41)"},
	{from: []string{"internal/records"}, deny: []string{"internal/features"}, why: "records imports sequence and sequencer but not features (C133)"},
	{from: []string{"internal/features", "internal/sequence", "internal/atlas"},
		deny: []string{"internal/api", "internal/app", "internal/httpserver", "internal/jobs", "internal/generation", "internal/spend", "internal/provider", "internal/muses", "internal/nextactions"},
		why:  "features, sequence and atlas sit below the API layer, spend nothing and never reach Muse text (C133, C144)"},
	{from: []string{"internal/features"}, deny: []string{"internal/provenance", "internal/records", "internal/atlas", "internal/sequence", "internal/nextactions"},
		why: "features is a leaf below provenance, records, atlas, sequence and nextactions (C133)"},
	{from: []string{"internal/atlas"}, deny: []string{"internal/features", "internal/records", "internal/sequence", "internal/nextactions"},
		why: "atlas reads feature rows by SQL: it imports neither features nor the Record or sequence packages (C133)"},
	{from: []string{"internal/nextactions"}, deny: []string{"internal/api", "internal/app", "internal/httpserver", "internal/jobs", "internal/generation", "internal/spend", "internal/provider",
		"internal/export", "internal/exports", "internal/notify", "internal/audit", "internal/backup", "internal/importer", "internal/tape", "internal/albums", "internal/records"},
		why: "nextactions reads Muse text and sits below the API layer; it reaches no export, notification, audit or L2 export-bound package (C144.2)"},
	{from: []string{"internal/export", "internal/exports", "internal/notify", "internal/audit", "internal/backup", "internal/importer", "internal/jobs", "internal/generation",
		"internal/lineage", "internal/library", "internal/redact", "internal/tape", "internal/albums", "internal/liner", "internal/media/cover", "internal/curation", "internal/records"},
		deny: l3NextActionDeny, why: "nothing that exports, notifies, backs up, serves Subsonic or builds a Record reaches nextactions: it reads Muse text (C133, C144.2)"},
}

// l3Imports are the direct imports C133 allows each L3 package (plus problem, which every leaf may use). A direct import
// outside the list needs a freeze fix, not a quiet edit.
var l3Imports = map[string][]string{
	"internal/features":    {"store", "sounds", "engine/yue2", "textfold", "analysis", "problem"},
	"internal/sequencer":   {},
	"internal/sequence":    {"store", "sequencer", "problem"},
	"internal/nextactions": {"store", "auth", "muses", "songview", "sounds", "songs", "problem"},
	"internal/atlas":       {"store", "songs", "sequencer", "textfold", "problem"},
}

// l3Packages are the packages the plan announces; the table may name them before they exist.
var l3Packages = map[string]bool{"features": true, "sequencer": true, "sequence": true, "nextactions": true, "atlas": true}

func TestImportGraph_L3(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	msgs, checked := violations(g, l3Rules)
	for _, m := range msgs {
		t.Error(m)
	}
	// the constrained packages that exist today (provenance, picks, sounds, records, the export-bound L1/L2 packages) are counted
	if checked < 12 {
		t.Fatalf("only %d constrained packages found; the table is stale", checked)
	}
	// subsonic* packages are constrained like the explicit list
	for pkg, p := range g {
		if !strings.Contains(pkg, "subsonic") || helperSubtree(pkg) {
			continue
		}
		for _, dep := range p.Deps {
			if d := strings.TrimPrefix(dep, mod); d != dep && under(d, "internal/nextactions") {
				t.Errorf("%s reaches %s: nothing that serves Subsonic reaches nextactions (C133)", pkg, d)
			}
		}
	}
	for pkg, ok := range l3Imports {
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
				t.Errorf("%s imports %s, which C133 does not list", pkg, d)
			}
		}
	}
	// analysis is a leaf (features reads only its *Def constants): it imports no internal package today and must keep it so
	if p, ok := g["internal/analysis"]; ok {
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, mod) {
				t.Errorf("internal/analysis imports %s: it must stay a leaf (C133)", imp)
			}
		}
	}
}

// TestImportGraph_L3RulesBite: the checker finds deliberately forbidden edges, and the direct-import table names real or
// announced packages.
func TestImportGraph_L3RulesBite(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	msgs, checked := violations(g, []rule{
		{from: []string{"internal/provenance"}, deny: []string{"internal/sounds"}, why: "probe"},
		{from: []string{"internal/records"}, deny: []string{"internal/store"}, why: "probe"},
	})
	if len(msgs) < 2 || checked < 2 {
		t.Fatalf("probe rules found %d violations in %d packages", len(msgs), checked)
	}
	for pkg, list := range l3Imports {
		for _, a := range list {
			if _, ok := g["internal/"+a]; !ok && !l3Packages[a] {
				t.Errorf("%s lists %s, which is not a package", pkg, a)
			}
		}
	}
	// no package is both a feature reader and a recompute hook owner: the two sets of the rules are disjoint
	for _, f := range []string{"internal/features", "internal/sequence", "internal/atlas", "internal/nextactions", "internal/sequencer"} {
		for _, p := range []string{"internal/provenance", "internal/picks", "internal/sounds"} {
			if under(f, p) || under(p, f) {
				t.Errorf("%s and %s overlap", f, p)
			}
		}
	}
}
