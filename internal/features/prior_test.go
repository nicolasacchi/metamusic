package features_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestPredictEnergy_Table(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name string
		f    features.Facets
		want int
		ok   bool
	}{
		{"chill reggae slow", features.Facets{Mood: []string{"chill"}, GenreFamily: []string{"reggae"}, TempoBand: []string{"slow"}}, 3333, true},
		{"energetic dance fast", features.Facets{Mood: []string{"energetic"}, GenreFamily: []string{"dance"}, TempoBand: []string{"fast"}}, 8500, true},
		{"only reggae", features.Facets{GenreFamily: []string{"reggae"}}, 5000, true},
		{"pop and electronic", features.Facets{GenreFamily: []string{"pop", "electronic"}}, 6500, true},
		{"none", features.Facets{}, 0, false},
		{"unknown skipped", features.Facets{Mood: []string{"zzz", "chill"}, GenreFamily: []string{"reggae"}}, 3750, true},
		{"all unknown is absent", features.Facets{Mood: []string{"zzz"}, TempoBand: []string{"fast"}}, 8000, true},
		{"only unknown", features.Facets{Mood: []string{"zzz"}}, 0, false},
		{"half up facet mean", features.Facets{Mood: []string{"chill", "dreamy", "warm"}}, 2833, true}, // 8500/3 = 2833.3
		{"half up", features.Facets{GenreFamily: []string{"ambient", "folk"}, Mood: []string{"chill"}}, 2500, true},
	}
	for _, r := range rows {
		got, ok := features.PredictEnergy(r.f)
		if ok != r.ok || got != r.want {
			t.Errorf("%s: PredictEnergy = (%d,%v), want (%d,%v)", r.name, got, ok, r.want, r.ok)
		}
	}
}

func TestFeatures_RegistryGolden(t *testing.T) {
	t.Parallel()
	prior := features.Prior()
	if prior.Def != "mm-energy-prior/1" {
		t.Errorf("def = %q", prior.Def)
	}
	// Every taxonomy mood and genre_family value of the seed, and every tempo band, has a prior row.
	for _, term := range sounds.SeedV1() {
		var table map[string]int
		switch term.Facet {
		case "mood":
			table = prior.Mood
		case "genre_family":
			table = prior.GenreFamily
		default:
			continue
		}
		if _, ok := table[term.Value]; !ok {
			t.Errorf("%s value %q has no prior row", term.Facet, term.Value)
		}
	}
	for _, b := range sounds.TempoBands {
		if _, ok := prior.TempoBand[b]; !ok {
			t.Errorf("tempo band %q has no prior row", b)
		}
	}
	if len(prior.TempoBand) != len(sounds.TempoBands) {
		t.Errorf("prior has stale tempo bands: %v", prior.TempoBand)
	}
	seedValues := map[string]map[string]bool{"mood": {}, "genre_family": {}}
	for _, term := range sounds.SeedV1() {
		if m, ok := seedValues[term.Facet]; ok {
			m[term.Value] = true
		}
	}
	for facet, table := range map[string]map[string]int{"mood": prior.Mood, "genre_family": prior.GenreFamily} {
		var stale []string
		for v := range table {
			if !seedValues[facet][v] {
				stale = append(stale, v)
			}
		}
		sort.Strings(stale)
		if len(stale) > 0 {
			t.Errorf("%s prior rows without a taxonomy value: %v", facet, stale)
		}
	}

	got, err := json.MarshalIndent(prior, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	p := filepath.Join("testdata", "prior_v1.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got) {
		t.Errorf("prior differs from prior_v1.golden.json: a changed number needs a new PriorDef (regenerate deliberately with -update)")
	}
}

func TestPrior_ReturnsCopy(t *testing.T) {
	t.Parallel()
	p := features.Prior()
	p.Mood["chill"] = 1
	if features.Prior().Mood["chill"] != 2500 {
		t.Error("Prior must return a copy")
	}
}
