package archtest

import (
	"strings"
	"testing"
)

// t1Rules are the binding import seams of T1 (the Architecture section of the plan). Packages that do not exist yet are
// skipped, so the table stays correct as the lanes land them.
var t1Rules = []rule{
	{from: []string{"internal/transform"}, deny: internalPkgs("api", "jobs", "spend", "provider", "generation", "upload"),
		why: "transform spends no money and calls no backend: it never reaches api, jobs, spend, provider, generation or upload"},
	{from: []string{"internal/jobs"}, deny: []string{"internal/transform"}, why: "jobs never imports transform; the runner is woken through an injected Wake func"},
	{from: []string{"internal/library"}, deny: []string{"internal/transform"}, why: "library never imports transform"},
}

// internalPkgs turns short names into internal/ package paths.
func internalPkgs(names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "internal/" + n
	}
	return out
}

// dspAllowed is what the pure DSP and score trees may import from this module: themselves, the exported analysis package
// (analysis.OnsetEnvelope, D-T1-20) and each other.
func dspAllowed(d string) bool {
	return under(d, "internal/dsp") || under(d, "internal/score") || d == "internal/analysis"
}

func TestImportGraph_T1(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	msgs, checked := violations(g, t1Rules)
	for _, m := range msgs {
		t.Error(m)
	}
	if _, ok := g["internal/transform"]; !ok {
		t.Fatal("internal/transform does not exist")
	}
	if checked < 3 {
		t.Fatalf("only %d constrained packages found; the table is stale", checked)
	}
	// dsp and score import nothing of the application (test helpers are exempt)
	pure := 0
	for pkg, p := range g {
		if !(under(pkg, "internal/dsp") || under(pkg, "internal/score")) || helperSubtree(pkg) {
			continue
		}
		pure++
		for _, dep := range p.Deps {
			d := strings.TrimPrefix(dep, mod)
			if d != dep && !dspAllowed(d) {
				t.Errorf("%s reaches %s: dsp and score import nothing of the application (only analysis and each other)", pkg, d)
			}
		}
	}
	if pure < 4 {
		t.Fatalf("only %d dsp packages found", pure)
	}
}

// TestImportGraph_T1RulesBite: the checker finds a deliberately forbidden edge, so a green run is not vacuous.
func TestImportGraph_T1RulesBite(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	if _, ok := g["internal/transform"]; !ok {
		t.Skip("internal/transform does not exist")
	}
	// transform really reaches library (and store); a rule that forbids it must fire
	msgs, _ := violations(g, []rule{{from: []string{"internal/transform"}, deny: []string{"internal/library"}, why: "probe"}})
	if len(msgs) == 0 {
		t.Fatal("a rule over a present edge found nothing")
	}
	// the dsp tree really reaches internal/dsp/pcm: an allow-list that left it out would fire
	if dspAllowed("internal/store") || !dspAllowed("internal/dsp/pcm") || !dspAllowed("internal/analysis") {
		t.Fatal("dspAllowed is wrong")
	}
}
