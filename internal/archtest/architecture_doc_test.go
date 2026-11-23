package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docSection returns the text of the "## <heading>" section of docs/architecture.md.
func docSection(t *testing.T, heading string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	start := strings.Index(doc, "\n## "+heading+"\n")
	if start < 0 {
		t.Fatalf("docs/architecture.md has no section %q", heading)
	}
	rest := doc[start+1:]
	if end := strings.Index(rest[3:], "\n## "); end >= 0 {
		rest = rest[:end+3]
	}
	return rest
}

// TestArchitectureDoc_T1Exceptions pins D-T1-18: the two CLI-only transform commands are documented parity exceptions
// with their reason, so CLAUDE.md rule 1 stays honest.
func TestArchitectureDoc_T1Exceptions(t *testing.T) {
	t.Parallel()
	sec := docSection(t, "Documented parity exceptions")
	for _, want := range []string{"transform verify", "transform sweep", "no ingest and no queue row", "host-operator maintenance"} {
		if !strings.Contains(sec, want) {
			t.Errorf("parity exceptions section lost %q", want)
		}
	}
}

// TestArchitectureDoc_ParityExceptions pins the other documented exceptions: binary and streaming routes, the voice note
// and the operator-only engine adoption.
func TestArchitectureDoc_ParityExceptions(t *testing.T) {
	t.Parallel()
	sec := docSection(t, "Documented parity exceptions")
	for _, want := range []string{"exclude:binary", "create_voice_note", "exclude:human-recording", "HUMAN_ONLY", "engines adopt|reject"} {
		if !strings.Contains(sec, want) {
			t.Errorf("parity exceptions section lost %q", want)
		}
	}
}

// TestArchitectureDoc_Sections pins the milestone sections X1 wrote, so a later edit cannot drop one silently.
func TestArchitectureDoc_Sections(t *testing.T) {
	t.Parallel()
	for heading, wants := range map[string][]string{
		"Engines (E2)":          {"registry", "runpod-serverless-acestep", "ENGINE_NOT_ADOPTED", "backend column"},
		"Transforms (T1)":       {"render child", "GOAMD64=v1", "FinishStream", "never spends money"},
		"Closing the loop (M5)": {"Song PR", "callbacks", "rewind", "voice"},
		"Ears Tier B, Recover and the Diction Lab (M5b)": {"mm.ears-b/1", "Recover never spends", "draft_recoveries", "TierBSweep", "HumanLabelledLines"},
		"Bulk pods and the upload fallback (BULK)":       {"runpod-pod", "/worker/v1/pods/", "pod_overhead", "pod_safety_log", "upload_drills", "fallback_objects", "off in production", "TestImportGraph_Bulk"},
	} {
		sec := docSection(t, heading)
		for _, want := range wants {
			if !strings.Contains(sec, want) {
				t.Errorf("section %q lost %q", heading, want)
			}
		}
	}
}

// TestPublicPlanCopies_E2T1M5 checks that the committed public copies of the E2, T1 and M5 plans hold neither the
// private marker nor Appendix P, and that each ends with a revision log.
func TestPublicPlanCopies_E2T1M5(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"2026-11-06-e2-acestep.md", "2026-11-06-t1-transforms.md", "2026-11-06-m5-close-the-loop.md"} {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "plans", name))
		if err != nil {
			t.Fatal(err)
		}
		doc := string(b)
		// Line-anchored, like the cut script: plan prose may mention the marker or Appendix P.
		for _, line := range strings.Split(doc, "\n") {
			if line == privateMarker || strings.HasPrefix(line, "## Appendix P") {
				t.Errorf("%s contains the private line %q", name, line)
			}
		}
		if !strings.Contains(doc, "\n## Revision log\n") {
			t.Errorf("%s has no revision log", name)
		}
	}
}

// TestRunbook_BulkOperatingSections pins the operator rules of the bulk sections of docs/runbook.md: what is off until the
// operator acts, how a window ends, how a live pod is stopped, the retention bound and the naming warning.
func TestRunbook_BulkOperatingSections(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "runbook.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, want := range []string{
		"## Bulk pods and the upload fallback: operating (BULK)",
		"are off until the operator sets them up",
		"Disarming does not stop a live pod",
		"window_ended",
		"at most 7 days",
		"written spend approval",
		"--max-usd",
		"Never create a pod named `mm-`",
		"upload fallback check",
		"probe --pods",
		"Key rotation",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("runbook lost %q", want)
		}
	}
}
