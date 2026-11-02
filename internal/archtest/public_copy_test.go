package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const privateMarker = "<!-- PRIVATE APPENDIX -->"

// publicCopy runs scripts/l1-public-copy.sh on a synthetic draft and returns the copy, its output and the exit error.
func publicCopy(t *testing.T, draft string) (copied, output string, err error) {
	t.Helper()
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "draft.md"), filepath.Join(dir, "copy.md")
	if err := os.WriteFile(src, []byte(draft), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(repoRoot(t), "scripts", "l1-public-copy.sh"), src, dst).CombinedOutput()
	b, _ := os.ReadFile(dst)
	return string(b), string(out), err
}

const syntheticAppendix = "\n## Appendix P (PRIVATE)\n### P.4 Gate patterns\nSynthetic Muse 900 by Synthetic Artist 900\n"

func TestPublicCopy_StopsAtMarker(t *testing.T) {
	t.Parallel()
	draft := "# Plan\n\npublic text\n" + privateMarker + "\n" + syntheticAppendix
	got, out, err := publicCopy(t, draft)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if got != "# Plan\n\npublic text\n" {
		t.Fatalf("copy = %q", got)
	}
	for _, bad := range []string{privateMarker, "## Appendix P", "Synthetic Muse 900", "Synthetic Artist 900"} {
		if strings.Contains(got, bad) {
			t.Errorf("copy contains %q", bad)
		}
	}
}

// TestPublicCopy_StopsAtMarker_L2 is the L2 shape: a draft with an Appendix P holding P.4 gate patterns (synthetic
// Record and Crate titles) must reach the copy without the appendix, the marker or any P.4 pattern.
func TestPublicCopy_StopsAtMarker_L2(t *testing.T) {
	t.Parallel()
	appendix := "\n## Appendix P (PRIVATE — never copied to `docs/plans/`)\n### P.4 Gate patterns\nSynthetic Record 900\nSynthetic Crate 900\nSynthetic Muse 900\n"
	draft := "# L2 plan\n\n## Revision log\npublic text\n" + privateMarker + "\n" + appendix
	got, out, err := publicCopy(t, draft)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	for _, bad := range []string{privateMarker, "## Appendix P", "P.4", "Synthetic Record 900", "Synthetic Crate 900", "Synthetic Muse 900"} {
		if strings.Contains(got, bad) {
			t.Errorf("copy contains %q", bad)
		}
	}
	if !strings.Contains(got, "public text") {
		t.Errorf("copy lost the public text: %q", got)
	}
}

// TestPublicCopy_StopsAtMarker_L3 is the L3 shape: a revision log above the marker reaches the copy, while the appendix
// with P.4 patterns (synthetic Muse, Song and taxonomy terms) does not.
func TestPublicCopy_StopsAtMarker_L3(t *testing.T) {
	t.Parallel()
	appendix := "\n## Appendix P (PRIVATE — never copied to `docs/plans/`)\n### P.4 Gate patterns\nSynthetic Muse 900\nSynthetic Song 900\nsynthetic-term-900\n"
	draft := "# L3 plan\n\n## Revision log\n\n### Revision 3\npublic text\n" + privateMarker + "\n" + appendix
	got, out, err := publicCopy(t, draft)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	for _, bad := range []string{privateMarker, "## Appendix P", "P.4", "Synthetic Muse 900", "Synthetic Song 900", "synthetic-term-900"} {
		if strings.Contains(got, bad) {
			t.Errorf("copy contains %q", bad)
		}
	}
	for _, want := range []string{"## Revision log", "### Revision 3", "public text"} {
		if !strings.Contains(got, want) {
			t.Errorf("copy lost %q: %q", want, got)
		}
	}
}

func TestPublicCopy_RefusesBadMarkers(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"missing": "# Plan\n" + syntheticAppendix,
		"twice":   "a\n" + privateMarker + "\nb\n" + privateMarker + "\n" + syntheticAppendix,
	}
	for name, draft := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, _, err := publicCopy(t, draft)
			if err == nil {
				t.Fatalf("script accepted the draft; copy = %q", got)
			}
		})
	}
}

// loadPrivatePatterns returns the operator's private gate patterns (P.4), or nil when none are configured here.
func loadPrivatePatterns(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	paths := []string{filepath.Join(root, ".public-gate-patterns")}
	if out, err := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
		paths = append(paths, filepath.Join(strings.TrimSpace(string(out)), "..", ".public-gate-patterns"))
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var pats []string
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				pats = append(pats, l)
			}
		}
		return pats
	}
	return nil
}

// TestPublicCopy_CommittedCopyIsClean checks the tracked copy of the plan: no marker line, no appendix, no private pattern.
// The real P.4 patterns are private and absent on CI; the pattern check runs only where .public-gate-patterns exists
// (the build host), and `make gate` (pre-commit, pre-push, CI) enforces the same patterns on every commit.
func TestPublicCopy_CommittedCopyIsClean(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"2026-10-15-l1-library-provenance.md", "2026-10-28-l2-library-curation.md", "2026-11-02-l3-library-agents-atlas.md"} {
		t.Run(name, func(t *testing.T) { checkCommittedCopy(t, name) })
	}
}

func checkCommittedCopy(t *testing.T, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "plans", name))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Contains(text, "### P.") {
		t.Fatal("the committed copy holds an Appendix P subsection")
	}
	for _, l := range strings.Split(text, "\n") {
		if l == privateMarker {
			t.Fatal("the committed copy holds the private-appendix marker line")
		}
		if strings.HasPrefix(l, "## Appendix P") {
			t.Fatal("the committed copy holds the Appendix P heading")
		}
	}
	low := strings.ToLower(text)
	for _, p := range loadPrivatePatterns(t) {
		if strings.Contains(low, strings.ToLower(p)) {
			t.Fatal("the committed copy matches a private gate pattern")
		}
	}
}

// TestPublicCopy_GateFailsOnLeak proves the gate rejects a staged copy holding a private pattern (synthetic pattern).
func TestPublicCopy_GateFailsOnLeak(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	gl := filepath.Join(root, "bin", "gitleaks")
	if _, err := os.Stat(gl); err != nil {
		if p, err := exec.LookPath("gitleaks"); err == nil {
			gl = p
		} else {
			t.Skip("gitleaks not found (make tools)")
		}
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", ".")
	run("config", "user.email", "t@example.invalid")
	run("config", "user.name", "t")
	run("config", "commit.gpgsign", "false")
	for _, f := range []string{"scripts/public-gate.sh", "scripts/metrics-row.jq", ".gitleaks.toml"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, f)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, f), b, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	gate := func(extra string) error {
		c := exec.Command("scripts/public-gate.sh", "--staged")
		c.Dir = repo
		c.Env = append(os.Environ(), "GITLEAKS="+gl, "PUBLIC_GATE_PATTERNS=Synthetic Muse 900")
		out, err := c.CombinedOutput()
		if err != nil && extra == "" {
			t.Logf("gate output: %s", out)
		}
		return err
	}
	if err := os.WriteFile(filepath.Join(repo, "plan.md"), []byte("public text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "plan.md")
	if err := gate(""); err != nil {
		t.Fatalf("gate rejected a clean copy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "plan.md"), []byte("public text\nSynthetic Muse 900\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "plan.md")
	if err := gate("leak"); err == nil {
		t.Fatal("gate accepted a copy holding a private pattern")
	}
}

// TestRoadmap_L3Listed checks the roadmap lists L3 between L2 and M4a, marks it done, records the absorbed Song Atlas
// track (D-L3-27) and no longer lists "Song Atlas" among the M6 items.
func TestRoadmap_L3Listed(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "plans", "2026-10-12-roadmap.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	l2, l3, m4a := strings.Index(text, "### L2 "), strings.Index(text, "### L3 "), strings.Index(text, "### M4a ")
	if l2 < 0 || l3 < 0 || m4a < 0 || !(l2 < l3 && l3 < m4a) {
		t.Fatalf("L3 must be listed between L2 and M4a (L2 %d, L3 %d, M4a %d)", l2, l3, m4a)
	}
	section := text[l3:m4a]
	for _, want := range []string{"done", "D-L3-27", "Song Atlas"} {
		if !strings.Contains(section, want) {
			t.Errorf("the L3 entry does not mention %q", want)
		}
	}
	m6 := text[strings.Index(text, "### M6 "):]
	if i := strings.Index(m6, "## Inputs"); i > 0 {
		m6 = m6[:i]
	}
	if strings.Contains(m6, "Song Atlas,") || strings.Contains(m6, "Song Atlas.") {
		t.Error("M6 still lists the Song Atlas, which L3 delivers")
	}
}
