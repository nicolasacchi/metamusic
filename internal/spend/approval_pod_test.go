package spend_test

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/spend"
)

// TestPodCanaryNeed_AlwaysNeeded: the pod canary always waits for the Owner, keeps the reasons it already had, adds its own once and
// drops any mandate (C398.1).
func TestPodCanaryNeed_AlwaysNeeded(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]spend.ApprovalNeed{
		"nothing needed":       {Reasons: []string{}},
		"nil reasons":          {},
		"already needed":       {Needed: true, Reasons: []string{spend.ReasonEditorBatch}},
		"covered by a mandate": {Reasons: []string{}, Mandate: "m-1"},
		"already a canary":     {Needed: true, Reasons: []string{spend.ReasonPodCanary}},
	} {
		got := spend.PodCanaryNeed(in)
		if !got.Needed || got.Mandate != "" || !slices.Contains(got.Reasons, spend.ReasonPodCanary) {
			t.Errorf("%s: %+v", name, got)
		}
		if n := len(slices.DeleteFunc(slices.Clone(got.Reasons), func(r string) bool { return r != spend.ReasonPodCanary })); n != 1 {
			t.Errorf("%s: the canary reason is there %d times", name, n)
		}
	}
	// the input is not modified
	in := spend.ApprovalNeed{Reasons: []string{spend.ReasonEditorBatch}}
	_ = spend.PodCanaryNeed(in)
	if len(in.Reasons) != 1 || in.Needed {
		t.Errorf("input changed: %+v", in)
	}
}
