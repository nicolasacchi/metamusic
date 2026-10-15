package sounds_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// facetWorld is the 40-Song table of the faceted-counts tests: draft Songs whose genre cycles 4 ways, vocal 2 ways
// (independently of the genre) and requested tempo 4 ways.
type fsong struct{ genre, vocal, band string }

func facetWorld(t *testing.T) (*world, map[string]fsong) {
	t.Helper()
	w := newWorld(t, true)
	genres := []string{"reggae", "rock", "jazz", "pop"}
	vocals := []string{"male", "female"}
	bpms := []int{70, 100, 120, 140}
	bands := []string{"slow", "mid", "upbeat", "fast"}
	table := map[string]fsong{}
	var takes, songIDs []string
	for i := range 40 {
		s := fsong{genres[i%4], vocals[(i/4)%2], bands[(i/2)%4]}
		tr := w.track(w.recipe(fmt.Sprintf("%s, %s vocal, %dbpm", s.genre, s.vocal, bpms[(i/2)%4]), "x"), trackOpts{})
		id := w.song(tr)
		takes, songIDs = append(takes, tr), append(songIDs, id)
		table[id] = s
	}
	w.exec(`DELETE FROM provenance_dirty`)
	w.compute(takes, songIDs)
	if len(table) != 40 {
		t.Fatalf("songs = %d", len(table))
	}
	return w, table
}

// oracle counts the table the way the standard faceted semantics say.
func oracle(table map[string]fsong, sel map[string][]string, facet string) map[string]int {
	out := map[string]int{}
	for _, s := range table {
		ok := true
		for f, vals := range sel {
			if f == facet {
				continue
			}
			var v string
			switch f {
			case "genre_family":
				v = s.genre
			case "vocal":
				v = s.vocal
			case "tempo_band":
				v = s.band
			case "engine":
				v = "yue2"
			}
			if !slices.Contains(vals, v) {
				ok = false
			}
		}
		if !ok {
			continue
		}
		switch facet {
		case "genre_family":
			out[s.genre]++
		case "vocal":
			out[s.vocal]++
		case "tempo_band":
			out[s.band]++
		case "engine":
			out["yue2"]++
		}
	}
	return out
}

func countsOf(vs []sounds.FacetValueCount) map[string]int {
	m := map[string]int{}
	for _, v := range vs {
		m[v.Value] = v.Count
	}
	return m
}

func TestFacets_CountsExcludeOwnSelection(t *testing.T) {
	t.Parallel()
	w, table := facetWorld(t)
	ctx := context.Background()
	for _, sel := range []map[string][]string{
		nil,
		{"genre_family": {"reggae"}},
		{"genre_family": {"reggae", "rock"}, "vocal": {"male"}},
		{"vocal": {"female"}, "tempo_band": {"slow", "fast"}},
		{"genre_family": {"jazz"}, "vocal": {"male"}, "tempo_band": {"mid"}},
		{"engine": {"yue2"}},
	} {
		fc, err := w.sv.Facets(ctx, songs.ListFilter{Sounds: sel})
		if err != nil {
			t.Fatal(err)
		}
		for _, facet := range []string{"genre_family", "vocal", "tempo_band", "engine"} {
			want := oracle(table, sel, facet)
			if got := countsOf(fc.Facets[facet]); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("selection %v facet %s: %v, want %v", sel, facet, got, want)
			}
		}
		matching := 0
		for _, s := range table {
			ok := true
			for f, vals := range sel {
				v := map[string]string{"genre_family": s.genre, "vocal": s.vocal, "tempo_band": s.band, "engine": "yue2"}[f]
				ok = ok && slices.Contains(vals, v)
			}
			if ok {
				matching++
			}
		}
		if fc.SongsMatching != matching {
			t.Errorf("selection %v: songs_matching %d, want %d", sel, fc.SongsMatching, matching)
		}
		for _, f := range sounds.Facets {
			if fc.Facets[f] == nil {
				t.Errorf("facet %s missing from the answer", f)
			}
		}
		if fc.TaxonomyVersion != 1 {
			t.Errorf("version %d", fc.TaxonomyVersion)
		}
	}
	// The selected value's siblings stay visible and the order is count desc, value asc.
	fc, _ := w.sv.Facets(ctx, songs.ListFilter{Sounds: map[string][]string{"genre_family": {"reggae"}}})
	if len(fc.Facets["genre_family"]) != 4 || !sort.SliceIsSorted(fc.Facets["genre_family"], func(i, j int) bool {
		a, b := fc.Facets["genre_family"][i], fc.Facets["genre_family"][j]
		return a.Count > b.Count || a.Count == b.Count && a.Value < b.Value
	}) {
		t.Errorf("genre facet: %+v", fc.Facets["genre_family"])
	}
	if fc.Selected["genre_family"][0] != "reggae" {
		t.Errorf("selected %v", fc.Selected)
	}
	if _, err := w.sv.Facets(ctx, songs.ListFilter{Sounds: map[string][]string{"colour": {"red"}}}); err == nil {
		t.Error("an unknown facet must be rejected")
	}
}

func TestFacets_HiddenAndRetired(t *testing.T) {
	t.Parallel()
	w, table := facetWorld(t)
	var one string
	for id := range table {
		one = id
		break
	}
	w.exec(`UPDATE songs SET hidden_at = '2026-10-06T12:00:00Z', hidden_by = 'human:op' WHERE id = ?`, one)
	fc, _ := w.sv.Facets(context.Background(), songs.ListFilter{})
	if fc.SongsMatching != 39 {
		t.Errorf("hidden Song counted: %d", fc.SongsMatching)
	}
	fc, _ = w.sv.Facets(context.Background(), songs.ListFilter{IncludeHidden: true})
	if fc.SongsMatching != 40 {
		t.Errorf("include_hidden: %d", fc.SongsMatching)
	}
}

func TestFacets_TempoMismatches(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	mk := func(bpm int, measured float64) string {
		tr := w.track(w.recipe(fmt.Sprintf("pop, %dbpm", bpm), "x"), trackOpts{})
		if measured > 0 {
			w.tempo(tr, measured)
		}
		return tr
	}
	a := mk(100, 101) // agrees
	b := mk(100, 140) // mismatch
	c := mk(100, 190) // octave (200 +- 6 %): not a mismatch
	d := mk(100, 0)   // never measured
	e := mk(120, 150) // mismatch, band upbeat -> fast
	var takes, songIDs []string
	for _, tr := range []string{a, b, c, d, e} {
		takes, songIDs = append(takes, tr), append(songIDs, w.song(tr))
	}
	w.compute(takes, songIDs)
	fc, err := w.sv.Facets(ctx, songs.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range fc.Facets["tempo_band"] {
		if v.Mismatches == nil {
			t.Fatalf("tempo_band value %s has no mismatches count", v.Value)
		}
		got[v.Value] = fmt.Sprintf("%d/%d", v.Count, *v.Mismatches)
	}
	// 100->101 mid, 100->140 fast(mismatch), 100->190 rapid? 190 is rapid, octave, 100 never measured mid, 120->150 fast mismatch
	if fmt.Sprint(got) != "map[fast:2/2 mid:2/0 rapid:1/0]" {
		t.Errorf("tempo_band counts = %v", got)
	}
	for _, f := range []string{"genre_family", "vocal", "mood"} {
		for _, v := range fc.Facets[f] {
			if v.Mismatches != nil {
				t.Errorf("%s reports mismatches", f)
			}
		}
	}
	yes, no := true, false
	fc, _ = w.sv.Facets(ctx, songs.ListFilter{TempoMismatch: &yes})
	if fc.SongsMatching != 2 {
		t.Errorf("tempo_mismatch=true matches %d, want 2", fc.SongsMatching)
	}
	fc, _ = w.sv.Facets(ctx, songs.ListFilter{TempoMismatch: &no})
	if fc.SongsMatching != 3 {
		t.Errorf("tempo_mismatch=false matches %d, want 3", fc.SongsMatching)
	}
	// Selecting a band leaves that facet's own selection out of its counts but keeps tempo_mismatch applied: with
	// tempo_band=mid and tempo_mismatch=true nothing matches, yet the band facet lists the mismatching bands only.
	fc, err = w.sv.Facets(ctx, songs.ListFilter{TempoMismatch: &yes, Sounds: map[string][]string{"tempo_band": {"mid"}}})
	if err != nil {
		t.Fatal(err)
	}
	bands := map[string]string{}
	for _, v := range fc.Facets["tempo_band"] {
		bands[v.Value] = fmt.Sprintf("%d/%d", v.Count, *v.Mismatches)
	}
	if fc.SongsMatching != 0 || fmt.Sprint(bands) != "map[fast:2/2]" {
		t.Errorf("tempo_band=mid with tempo_mismatch: matching %d, bands %v", fc.SongsMatching, bands)
	}
}

// TestSearchSounds_Equivalence: songs_matching equals the total list_songs reports for the same filter.
func TestSearchSounds_Equivalence(t *testing.T) {
	t.Parallel()
	w, _ := facetWorld(t)
	ctx := context.Background()
	svc := songs.New(w.st, w.now, w.newID)
	for _, sel := range []map[string][]string{nil, {"genre_family": {"jazz", "pop"}}, {"vocal": {"male"}, "tempo_band": {"fast"}}} {
		f := songs.ListFilter{Sounds: sel, Limit: 50}
		page, err := svc.List(ctx, f)
		if errors.Is(err, errors.ErrUnsupported) {
			t.Skip("songs.List lands with L1-B5; the REST-level equivalence is checked there")
		}
		if err != nil {
			t.Fatal(err)
		}
		fc, err := w.sv.Facets(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if fc.SongsMatching != page.Total {
			t.Errorf("selection %v: songs_matching %d, list_songs total %d", sel, fc.SongsMatching, page.Total)
		}
	}
}
