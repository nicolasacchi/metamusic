package library_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func t1World(t *testing.T) (*library.Service, *store.Store, lt.T1Info) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	info := lt.FixtureT1(t, st, lt.FixedNow)
	return library.New(st, nil, func() time.Time { return lt.FixedNow }), st, info
}

func pageIDs(p library.TrackPage) []string {
	out := []string{}
	for _, it := range p.Items {
		out = append(out, it.ID)
	}
	return out
}

// TestSearch_OriginTransform: origin=transform is derived from transform_id, job means a generation job only, and a
// transform's summary reports origin transform with its block.
func TestSearch_OriginTransform(t *testing.T) {
	t.Parallel()
	svc, st, info := t1World(t)
	ctx := context.Background()
	g := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 1, Kind: "glacier", Parent: info.Tx["A"]})
	page, err := svc.SearchTracks(ctx, library.SearchParams{Origin: []string{"transform"}})
	if err != nil || !slices.Equal(pageIDs(page), []string{g}) {
		t.Fatalf("origin=transform: %v %v", pageIDs(page), err)
	}
	it := page.Items[0]
	if it.Origin != "transform" || it.Transform == nil || it.Transform.Kind != "glacier" || it.Transform.TransformID != g || len(it.Transform.SpecHash) != 64 {
		t.Errorf("summary = origin %q transform %+v", it.Origin, it.Transform)
	}
	for _, o := range []string{"job", "import"} {
		p, err := svc.SearchTracks(ctx, library.SearchParams{Origin: []string{o}})
		if err != nil || slices.Contains(pageIDs(p), g) {
			t.Errorf("origin=%s lists the transform Take (%v)", o, err)
		}
		for _, x := range p.Items {
			if x.Origin == "transform" || x.Transform != nil {
				t.Errorf("origin=%s: %s reports %q", o, x.ID, x.Origin)
			}
		}
	}
	all, err := svc.SearchTracks(ctx, library.SearchParams{Origin: []string{"import", "job", "transform"}})
	if err != nil || !slices.Contains(pageIDs(all), g) {
		t.Errorf("all origins: %v", err)
	}
	if _, err := svc.SearchTracks(ctx, library.SearchParams{Origin: []string{"bogus"}}); err == nil {
		t.Error("an unknown origin is accepted")
	}
}

// TestSearch_LyricsOf: the morph partner finder (C211.4).
func TestSearch_LyricsOf(t *testing.T) {
	t.Parallel()
	svc, st, info := t1World(t)
	ctx := context.Background()
	a, b, nf, oct := info.Tx["A"], info.Tx["B"], info.Tx["NF"], info.Tx["OCT"]
	search := func(target string) ([]string, error) {
		p, err := svc.SearchTracks(ctx, library.SearchParams{LyricsOf: target})
		return pageIDs(p), err
	}
	// A and B share lyrics; NF and OCT extend them, so differ
	if got, err := search(a); err != nil || !slices.Equal(got, []string{b}) {
		t.Errorf("lyrics_of A = %v %v, want [B]", got, err)
	}
	if got, _ := search(b); !slices.Equal(got, []string{a}) {
		t.Errorf("lyrics_of B = %v, want [A]", got)
	}
	if got, _ := search(nf); len(got) != 0 {
		t.Errorf("lyrics_of NF = %v, want none", got)
	}
	_ = oct
	// CRLF vs LF and trailing whitespace are equal: rewrite B's lyrics in place (a fixture edit, bypassing the frozen trigger is not needed: recipes lyrics are plain)
	var rid string
	if err := st.R.QueryRow(`SELECT recipe_id FROM tracks WHERE id = ?`, b).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	exec := func(lyrics string) {
		t.Helper()
		if _, err := st.W.Exec(`DROP TRIGGER IF EXISTS recipes_immutable`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.Exec(`UPDATE recipes SET lyrics = ? WHERE id = ?`, lyrics, rid); err != nil {
			t.Fatal(err)
		}
	}
	exec("\r\n" + strings.ReplaceAll(lt.T1Lyrics, "\n", "  \r\n") + "  \r\n\r\n")
	if got, _ := search(a); !slices.Equal(got, []string{b}) {
		t.Errorf("CRLF/trailing-space variant: lyrics_of A = %v, want [B]", got)
	}
	exec(lt.T1Lyrics + " extra")
	if got, _ := search(a); len(got) != 0 {
		t.Errorf("a different text matched: %v", got)
	}
	// instrumental and whitespace-only targets answer nothing, even though other instrumentals exist
	for _, label := range []string{"I", "WS"} {
		if got, err := search(info.Tx[label]); err != nil || len(got) != 0 {
			t.Errorf("lyrics_of %s = %v %v, want []", label, got, err)
		}
	}
	// a transform Take as the target answers empty and never appears among candidates
	g := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 1, Kind: "glacier", Parent: a})
	if got, err := search(g); err != nil || len(got) != 0 {
		t.Errorf("lyrics_of a transform = %v %v, want []", got, err)
	}
	exec(lt.T1Lyrics)
	if got, _ := search(a); slices.Contains(got, g) {
		t.Errorf("a transform Take is a lyrics_of candidate: %v", got)
	}
	// references and lossy imports are excluded
	for _, p := range info.Parts {
		if got, _ := search(a); slices.Contains(got, p) {
			t.Errorf("lossy reference %s is a candidate", p)
		}
	}
	if _, err := search("01900000-0000-7000-8000-ffffffffffff"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown target: %v", err)
	}
}

// TestTrackDetail_TransformNoGenerationOf: a transform Take's detail reports origin transform, its block, no generation
// provenance and no error (the GenerationOf reader joins jobs and finds none).
func TestTrackDetail_TransformNoGenerationOf(t *testing.T) {
	t.Parallel()
	svc, st, info := t1World(t)
	g := lt.InsertTransformTake(t, st, lt.FixedNow, lt.TransformTake{N: 1, Kind: "widen", Parent: info.Tx["A"]})
	d, err := svc.Track(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if d.Origin != "transform" || d.Generation != nil || d.Transform == nil || d.Transform.Kind != "widen" || d.Recipe.Engine != "dsp" || len(d.StyleTerms) != 0 || d.BPMDeclared != nil {
		t.Errorf("detail = origin %q generation %v transform %+v engine %q terms %v", d.Origin, d.Generation, d.Transform, d.Recipe.Engine, d.StyleTerms)
	}
	if plain, err := svc.Track(context.Background(), info.Tx["A"]); err != nil || plain.Transform != nil || plain.Origin != "import" {
		t.Errorf("plain Take: origin %q transform %+v err %v", plain.Origin, plain.Transform, err)
	}
}
