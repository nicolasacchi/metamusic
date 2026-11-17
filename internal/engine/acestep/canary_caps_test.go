package acestep

import (
	"slices"
	"testing"
)

func TestIsCanaryID(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]bool{
		"mm-canary-20261117-reference-ref-a-1": true,
		"mm-canary-20261117-t2m-t2m-1":         true,
		"mm-canary-20261117-x":                 true,
		"mm-canary-2026111-x":                  false,
		"mm-canary-20261117":                   false,
		"mm-canary-2026111a-x":                 false,
		"mm-canary-202611170-x":                false,
		"xmm-canary-20261117-x":                false,
		"my-song":                              false,
		"":                                     false,
	} {
		if got := IsCanaryID(id); got != want {
			t.Errorf("IsCanaryID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestFeatures_WithCanary(t *testing.T) {
	t.Parallel()
	base := EmbeddedFeatures()
	if got := base.WithCanary(nil, true); !slices.Equal(Engine{Verified: got}.Caps().Tasks, Engine{Verified: base}.Caps().Tasks) {
		t.Error("no names must change nothing")
	}
	all := base.WithCanary([]string{"reference", "repaint", "extend", "complete", "extract", "lrc"}, true)
	c := Engine{Verified: all}.Caps()
	if !c.ReferenceAudio || !c.RepaintInterval || !c.Extend || !c.Complete || !c.Stems || !c.LRC {
		t.Errorf("caps %+v", c)
	}
	if c.RepaintMinS != base.Repaint.MinS || c.RepaintMaxS != base.Repaint.MaxS || c.ReferenceMaxS != base.ReferenceMaxS {
		t.Errorf("bounds come from the verified file: %+v", c)
	}
	// the receiver is not mutated and verified facts stay as they were
	if slices.Contains(base.Tasks, TaskRepaint) || base.Reference || base.LRC {
		t.Error("WithCanary mutated its receiver")
	}
	if all.VerifiedAt != base.VerifiedAt || all.CanaryCommit != base.CanaryCommit || all.ReferenceReproducible || all.SeedReproducible {
		t.Error("WithCanary must not claim verification")
	}
	// base-class modes need the base models: turbo-only tasks, or a bundle_only file, keep them off
	if c := (Engine{Verified: base.WithCanary([]string{"complete", "extract"}, false)}).Caps(); c.Complete || c.Stems {
		t.Errorf("turbo-only tasks: %+v", c)
	}
	bo := base
	bo.ModelsMode = ModelsBundleOnly
	if c := (Engine{Verified: bo.WithCanary([]string{"complete", "extract"}, true)}).Caps(); c.Complete || c.Stems {
		t.Errorf("bundle_only: %+v", c)
	}
}

func TestCanaryCapsUsed(t *testing.T) {
	t.Parallel()
	f := EmbeddedFeatures()
	on := []string{"reference", "repaint"}
	if got := f.CanaryCapsUsed(Request{Task: TaskRepaint, Ref: &RefRef{}}, on); !slices.Equal(got, []string{"reference", "repaint"}) {
		t.Errorf("got %v", got)
	}
	if got := f.CanaryCapsUsed(Request{Task: TaskText2Music}, on); len(got) != 0 {
		t.Errorf("got %v", got)
	}
	// a capability the file already verifies is not reported
	f.Reference = true
	if got := f.CanaryCapsUsed(Request{Task: TaskText2Music, Ref: &RefRef{}}, on); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
