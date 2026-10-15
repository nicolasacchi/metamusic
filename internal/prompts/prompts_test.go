package prompts_test

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/prompts"
)

// TestCatalog_Names pins the frozen catalog (C112.2); A1 adds the rendering goldens.
func TestCatalog_Names(t *testing.T) {
	t.Parallel()
	var names []string
	for _, s := range prompts.Catalog {
		names = append(names, s.Name)
		if s.Version != "1" || s.Title == "" {
			t.Errorf("%s: version %q title %q", s.Name, s.Version, s.Title)
		}
	}
	if want := []string{"critique_track", "write_variant", "session_review"}; !slices.Equal(names, want) {
		t.Fatalf("catalog = %v, want %v", names, want)
	}
	for _, s := range prompts.Catalog[:2] {
		if len(s.Args) == 0 || s.Args[0].Name != "track_id" || !s.Args[0].Required {
			t.Errorf("%s needs a required track_id first", s.Name)
		}
	}
}
