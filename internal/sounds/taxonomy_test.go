package sounds_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

func TestEnsureSeed_Idempotent(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	ctx := context.Background()
	for range 2 {
		if err := w.sv.EnsureSeed(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`); n != 1 {
		t.Fatalf("versions = %d, want 1", n)
	}
	if got := w.str(`SELECT seed_id || '/' || kind || '/' || created_by FROM sound_taxonomy_versions WHERE version = 1`); got != "seed/1/seed/system:seed" {
		t.Errorf("version 1 = %q", got)
	}
	if n, want := w.count(`SELECT COUNT(*) FROM sound_terms`), len(sounds.SeedV1()); n != want {
		t.Errorf("sound_terms = %d, want %d (the seed table)", n, want)
	}
	// Concurrent starters on a second, unseeded library.
	w2 := newWorld(t, false)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w2.sv.EnsureSeed(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := w2.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`); n != 1 {
		t.Errorf("concurrent EnsureSeed made %d versions", n)
	}
	if n, want := w2.count(`SELECT COUNT(*) FROM sound_terms`), len(sounds.SeedV1()); n != want {
		t.Errorf("concurrent: sound_terms = %d, want %d", n, want)
	}
}

func TestTaxonomy_EffectiveFold(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	ix1, err := w.sv.Index(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ix1.Version != 1 || !ix1.Has("reggae", "genre_family", "reggae") || ix1.Has("skank", "genre_family", "reggae") {
		t.Fatalf("seed index: version %d", ix1.Version)
	}
	n1 := ix1.Len()
	if _, err := w.sv.Map(ctx, agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{{Term: "Skank", Facet: "genre_family", Value: "reggae", Op: "add"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.sv.Map(ctx, humanP, sounds.MapRequest{Mappings: []sounds.MapEntry{{Term: "skank", Facet: "genre_family", Value: "reggae", Op: "remove"}, {Term: "dub", Facet: "genre_family", Value: "reggae", Op: "remove"}}}); err != nil {
		t.Fatal(err)
	}
	ix2, _ := w.sv.Index(ctx)
	ix3, _ := w.sv.Index(ctx)
	if ix2 != ix3 || ix2.Version != 3 {
		t.Errorf("index not cached per version: %d %p %p", ix2.Version, ix2, ix3)
	}
	if ix2.Has("skank", "genre_family", "reggae") || ix2.Has("dub", "genre_family", "reggae") || ix2.Len() != n1-1 {
		t.Errorf("version 3: skank=%v dub=%v len %d want %d", ix2.Has("skank", "genre_family", "reggae"), ix2.Has("dub", "genre_family", "reggae"), ix2.Len(), n1-1)
	}
	// BuildIndex of an old version is unaffected by later rows.
	rows, err := w.loadRows()
	if err != nil {
		t.Fatal(err)
	}
	old := sounds.BuildIndex(2, rows)
	if !old.Has("skank", "genre_family", "reggae") || !old.Has("dub", "genre_family", "reggae") || old.Len() != n1+1 {
		t.Errorf("version 2 index changed by later rows: len %d", old.Len())
	}
	if got := sounds.BuildIndex(1, rows); got.Len() != n1 || got.Has("skank", "genre_family", "reggae") {
		t.Errorf("version 1 index len %d", got.Len())
	}
}

func (w *world) loadRows() ([]sounds.TermRow, error) {
	rs, err := w.st.R.Query(`SELECT version, term, facet, value, op FROM sound_terms ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []sounds.TermRow
	for rs.Next() {
		var r sounds.TermRow
		if err := rs.Scan(&r.Version, &r.Term, &r.Facet, &r.Value, &r.Op); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func TestIndex_CachedPerVersion(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	a, _ := w.sv.Index(context.Background())
	b, _ := w.sv.Index(context.Background())
	if a != b {
		t.Fatal("two calls at one version built two indexes")
	}
	if _, err := w.sv.Map(context.Background(), agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{{Term: "zouk", Facet: "genre_family", Value: "world", Op: "add"}}}); err != nil {
		t.Fatal(err)
	}
	c, _ := w.sv.Index(context.Background())
	if c == a || c.Version != 2 {
		t.Errorf("a new version must rebuild the index (version %d)", c.Version)
	}
}

func TestTaxonomy_Page(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	pg, err := w.sv.Taxonomy(ctx, sounds.TaxonomyQuery{Facet: "language", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if pg.CurrentVersion != 1 || len(pg.Items) != 3 || pg.NextCursor == nil || len(pg.Versions) != 1 || pg.Versions[0].SeedID == nil {
		t.Fatalf("page 1: %+v", pg)
	}
	var terms []string
	for _, it := range pg.Items {
		terms = append(terms, it.Facet+"/"+it.Value+"/"+it.Term)
		if it.SinceVersion != 1 {
			t.Errorf("since_version %d", it.SinceVersion)
		}
	}
	seen := terms
	for pg.NextCursor != nil {
		if pg, err = w.sv.Taxonomy(ctx, sounds.TaxonomyQuery{Facet: "language", Limit: 3, Cursor: *pg.NextCursor}); err != nil {
			t.Fatal(err)
		}
		for _, it := range pg.Items {
			seen = append(seen, it.Facet+"/"+it.Value+"/"+it.Term)
		}
	}
	if n := w.count(`SELECT COUNT(*) FROM sound_terms WHERE facet = 'language'`); len(seen) != n {
		t.Errorf("paged %d language items, want %d", len(seen), n)
	}
	if !reflect.DeepEqual(seen[:3], terms) || seen[0] >= seen[len(seen)-1] {
		t.Errorf("order: %v", seen)
	}
	if q, err := w.sv.Taxonomy(ctx, sounds.TaxonomyQuery{Q: "ITALO"}); err != nil || len(q.Items) == 0 {
		t.Errorf("q filter: %+v %v", q, err)
	}
	var pe *problem.Error
	if _, err := w.sv.Taxonomy(ctx, sounds.TaxonomyQuery{Cursor: "!!"}); !errors.As(err, &pe) || pe.Code != "INVALID_CURSOR" {
		t.Errorf("bad cursor: %v", err)
	}
	if _, err := w.sv.Taxonomy(ctx, sounds.TaxonomyQuery{Facet: "engine"}); !errors.As(err, &pe) || pe.Code != "INVALID_FACET" {
		t.Errorf("bad facet: %v", err)
	}
	if _, err := w.sv.Taxonomy(ctx, sounds.TaxonomyQuery{Limit: 500}); !errors.As(err, &pe) {
		t.Errorf("limit: %v", err)
	}
}

func TestTaxonomy_Unmapped_Ordering(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	styles := []string{"reggae, zzz glitch, wobble bass", "pop, wobble bass", "rock, wobble bass, aaa fizz", "rock, aaa fizz", "pop, zzz glitch"}
	for _, s := range styles {
		w.track(w.recipe(s, "x"), trackOpts{})
	}
	w.settle()
	pg, err := w.sv.Taxonomy(ctx, sounds.TaxonomyQuery{Unmapped: true})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(pg.Unmapped)
	// wobble bass 3, aaa fizz 2, zzz glitch 2 (count desc, then term)
	if want := "[{wobble bass 3} {aaa fizz 2} {zzz glitch 2}]"; got != want {
		t.Errorf("unmapped = %s, want %s", got, want)
	}
	if len(pg.Items) != 0 || len(pg.Versions) != 0 {
		t.Errorf("unmapped mode returns only the unmapped list: %+v", pg)
	}
	p1, _ := w.sv.Taxonomy(ctx, sounds.TaxonomyQuery{Unmapped: true, Limit: 1})
	p2, err := w.sv.Taxonomy(ctx, sounds.TaxonomyQuery{Unmapped: true, Limit: 2, Cursor: *p1.NextCursor})
	if err != nil || fmt.Sprint(p2.Unmapped) != "[{aaa fizz 2} {zzz glitch 2}]" || p2.NextCursor != nil {
		t.Errorf("cursor page: %v %v", p2, err)
	}
	if q, _ := w.sv.Taxonomy(ctx, sounds.TaxonomyQuery{Unmapped: true, Q: "FIZZ"}); fmt.Sprint(q.Unmapped) != "[{aaa fizz 2}]" {
		t.Errorf("q: %v", q.Unmapped)
	}
}

func TestUnmapped_Cache(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	tr := w.track(w.recipe("reggae, wobble bass, 90bpm, 1234", "x"), trackOpts{})
	w.settle()
	song := w.songOfTrack(tr)
	got := w.rowsOf2(`SELECT term FROM song_unmapped_terms WHERE song_id = ? ORDER BY term`, song)
	if !reflect.DeepEqual(got, []string{"wobble bass"}) { // the BPM part and digits are never unmapped terms
		t.Fatalf("unmapped = %v", got)
	}
	// Mapping the term empties the cache entry.
	if _, err := w.sv.Map(context.Background(), agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{{Term: "wobble bass", Facet: "genre_family", Value: "dubstep", Op: "add"}}}); err != nil {
		t.Fatal(err)
	}
	if got := w.rowsOf2(`SELECT term FROM song_unmapped_terms WHERE song_id = ?`, song); len(got) != 0 {
		t.Errorf("after the mapping: %v", got)
	}
}

func (w *world) rowsOf2(q string, args ...any) []string {
	w.t.Helper()
	rs, err := w.st.R.Query(q, args...)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var v string
		if err := rs.Scan(&v); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}
