package sounds_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

func me(term, facet, value, op string) sounds.MapEntry {
	return sounds.MapEntry{Term: term, Facet: facet, Value: value, Op: op}
}

func TestMap_Rules(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	many := make([]sounds.MapEntry, 51)
	for i := range many {
		many[i] = me("term"+strings.Repeat("a", i+1), "mood", "chill", "add")
	}
	long := strings.Repeat("x", 61)
	for _, c := range []struct {
		name string
		req  sounds.MapRequest
		code string
	}{
		{"tempo_band", sounds.MapRequest{Mappings: []sounds.MapEntry{me("fast one", "tempo_band", "fast", "add")}}, "FACET_NOT_MAPPABLE"},
		{"engine", sounds.MapRequest{Mappings: []sounds.MapEntry{me("fast one", "engine", "yue2", "add")}}, "FACET_NOT_MAPPABLE"},
		{"unknown facet", sounds.MapRequest{Mappings: []sounds.MapEntry{me("x y", "colour", "red", "add")}}, "INVALID_FACET"},
		{"bad era", sounds.MapRequest{Mappings: []sounds.MapEntry{me("long ago", "era", "30s", "add")}}, "INVALID_VALUE"},
		{"era not a decade", sounds.MapRequest{Mappings: []sounds.MapEntry{me("long ago", "era", "1990s", "add")}}, "INVALID_VALUE"},
		{"language three letters", sounds.MapRequest{Mappings: []sounds.MapEntry{me("klingon", "language", "tlh", "add")}}, "INVALID_VALUE"},
		{"language upper", sounds.MapRequest{Mappings: []sounds.MapEntry{me("klingon", "language", "TL", "add")}}, "INVALID_VALUE"},
		{"value with space", sounds.MapRequest{Mappings: []sounds.MapEntry{me("odd", "mood", "very sad", "add")}}, "INVALID_VALUE"},
		{"term without a letter", sounds.MapRequest{Mappings: []sounds.MapEntry{me("1234 - 5", "mood", "chill", "add")}}, "INVALID_TERM"},
		{"empty term after folding", sounds.MapRequest{Mappings: []sounds.MapEntry{me("  - / ", "mood", "chill", "add")}}, "INVALID_TERM"},
		{"term too long", sounds.MapRequest{Mappings: []sounds.MapEntry{me(long, "mood", "chill", "add")}}, "INVALID_TERM"},
		{"bad op", sounds.MapRequest{Mappings: []sounds.MapEntry{me("odd", "mood", "chill", "toggle")}}, "INVALID_OP"},
		{"51 mappings", sounds.MapRequest{Mappings: many}, "TOO_MANY_MAPPINGS"},
		{"remove not effective", sounds.MapRequest{Mappings: []sounds.MapEntry{me("nonsense words", "mood", "chill", "remove")}}, "MAPPING_NOT_EFFECTIVE"},
		{"add and remove together", sounds.MapRequest{Mappings: []sounds.MapEntry{me("zouk", "genre_family", "world", "add"), me("Zouk", "genre_family", "world", "remove")}}, "CONFLICTING_MAPPINGS"},
	} {
		_, err := w.sv.Map(ctx, agentP, c.req)
		var pe *problem.Error
		if !errors.As(err, &pe) || pe.Status != 422 || pe.Code != c.code {
			t.Errorf("%s: %v, want 422 %s", c.name, err, c.code)
		}
	}
	if n := w.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`); n != 1 {
		t.Errorf("rejected calls made %d versions", n)
	}
	// All unchanged => no version.
	res, err := w.sv.Map(ctx, agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("Reggae", "genre_family", "reggae", "add"), me("roots-reggae", "genre_family", "reggae", "add")}})
	if err != nil || res.Version != nil || res.Unchanged != 2 || res.MappingsApplied != 0 {
		t.Errorf("all unchanged: %+v %v", res, err)
	}
	if n := w.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`); n != 1 {
		t.Errorf("an all-unchanged call made a version")
	}
	// Mixed: one new, one unchanged, a note, the author recorded.
	note := "synthetic"
	res, err = w.sv.Map(ctx, agentP, sounds.MapRequest{Note: &note, Mappings: []sounds.MapEntry{me("Zouk Love", "genre_family", "world", "add"), me("reggae", "genre_family", "reggae", "add")}})
	if err != nil || res.Version == nil || *res.Version != 2 || res.MappingsApplied != 1 || res.Unchanged != 1 {
		t.Fatalf("mixed: %+v %v", res, err)
	}
	if got := w.str(`SELECT created_by || '/' || kind || '/' || note FROM sound_taxonomy_versions WHERE version = 2`); got != "agent:tok-1/extension/synthetic" {
		t.Errorf("version row %q", got)
	}
	if got := w.str(`SELECT term FROM sound_terms WHERE version = 2`); got != "zouk love" {
		t.Errorf("term stored as %q", got)
	}
	// Remove then re-add works across versions.
	if _, err := w.sv.Map(ctx, humanP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("zouk love", "genre_family", "world", "remove")}}); err != nil {
		t.Fatal(err)
	}
	if res, err := w.sv.Map(ctx, humanP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("zouk love", "genre_family", "world", "add")}}); err != nil || *res.Version != 4 {
		t.Errorf("re-add: %+v %v", res, err)
	}
}

func TestMap_AgentAllowed(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	// The service accepts any principal; the write scope is enforced by the operation's Meta (see the REST tests).
	if _, err := w.sv.Map(context.Background(), agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("zouk", "genre_family", "world", "add")}}); err != nil {
		t.Fatal(err)
	}
}

func TestMap_RecomputesAffected(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	d := w.draft("skank, 90bpm")
	tr := w.track(w.recipe("skank, 100bpm", "x"), trackOpts{})
	other := w.track(w.recipe("rock", "x"), trackOpts{})
	w.settle()
	song, takeSong := w.songOfDraft(d), w.songOfTrack(tr)
	if len(w.facetValues("song_sounds", "song_id", song, "genre_family")) != 0 {
		t.Fatal("skank is mapped before the mapping exists")
	}
	if got := w.rowsOf2(`SELECT term FROM song_unmapped_terms WHERE song_id = ?`, song); len(got) != 1 || got[0] != "skank" {
		t.Fatalf("unmapped before: %v", got)
	}
	res, err := w.sv.Map(ctx, agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("skank", "genre_family", "reggae", "add")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.RecomputedSongs != 3 || res.RecomputedTakes != 2 {
		t.Errorf("recomputed %+v, want every stale subject (2 takes, 3 songs)", res)
	}
	for _, s := range []string{song, takeSong} {
		if got := w.facetValues("song_sounds", "song_id", s, "genre_family"); len(got) != 1 || got[0] != "reggae" {
			t.Errorf("song %s genre = %v", s[len(s)-4:], got)
		}
	}
	if got := w.facetValues("take_sounds", "track_id", tr, "genre_family"); len(got) != 1 {
		t.Errorf("take genre %v", got)
	}
	if n := w.count(`SELECT COUNT(*) FROM song_unmapped_terms WHERE song_id = ?`, song); n != 0 {
		t.Errorf("the unmapped list still has the term")
	}
	_ = other
	// Everything is fresh at the new version.
	if n := w.count(`SELECT COUNT(*) FROM sound_state WHERE taxonomy_version <> 2`); n != 0 {
		t.Errorf("%d subjects stale after Map", n)
	}
	// And a second call finds nothing to recompute for an unchanged taxonomy.
	res2, _ := w.sv.Map(ctx, agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("skank", "genre_family", "reggae", "add")}})
	if res2.Version != nil || res2.RecomputedSongs != 0 {
		t.Errorf("no-op call: %+v", res2)
	}
}
