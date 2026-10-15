package trackctx_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/songwriting"
)

// TestContext_SongwritingTargets: the operator's duration targets ride in the bundle (after _links) so an agent
// that critiques or rewrites a take sees them; without a source the key is absent and the golden files stay valid.
func TestContext_SongwritingTargets(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	ctx := context.Background()
	b, err := e.b.Build(ctx, agent, lt.TrackLuna, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys(t, marshal(t, b)) {
		if k == "songwriting" {
			t.Fatal("songwriting present without a source")
		}
	}
	sw := songwriting.New(e.st)
	e.b.UseSongwriting(sw)
	if _, err := sw.Set(ctx, songwriting.Patch{MinDurationS: new(200)}); err != nil {
		t.Fatal(err)
	}
	b, err = e.b.Build(ctx, agent, lt.TrackLuna, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	want := []string{"format", "track", "recipe", "provenance", "score", "lineage", "judgments", "perception", "listening", "_links", "songwriting", "next_actions", "_budget"}
	if diff := cmp.Diff(want, keys(t, raw)); diff != "" {
		t.Errorf("top-level keys:\n%s", diff)
	}
	got := rawMap(t, raw)["songwriting"].(map[string]any)
	if got["min_duration_s"] != float64(200) || got["target_min_duration_s"] != float64(240) || got["target_max_duration_s"] != float64(300) {
		t.Errorf("songwriting section: %v", got)
	}
	if g, _ := got["guidance"].(string); g == "" {
		t.Errorf("no guidance text: %v", got)
	}
	if b.Budget.Chars != utf16Len(string(raw)) {
		t.Errorf("budget %d does not measure the bundle with the section (%d)", b.Budget.Chars, utf16Len(string(raw)))
	}
}
