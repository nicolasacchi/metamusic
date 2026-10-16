package sounds_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sounds"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestSeed_Golden(t *testing.T) {
	t.Parallel()
	got, err := json.MarshalIndent(sounds.SeedV1(), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	p := filepath.Join("testdata", "seed_v1.golden.json")
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
		t.Errorf("seed/1 differs from seed_v1.golden.json: a changed value needs a new seed id (regenerate deliberately with -update)")
	}
}

func TestSeed_Invariants(t *testing.T) {
	t.Parallel()
	rows := sounds.SeedV1()
	if len(rows) < 150 {
		t.Errorf("seed has %d terms, want >= 150", len(rows))
	}
	seen := map[sounds.SeedTerm]bool{}
	perGenre := map[string]int{}
	eraValues := map[string]bool{}
	for _, r := range rows {
		if seen[r] {
			t.Errorf("duplicate (term, facet, value) %+v", r)
		}
		seen[r] = true
		if !sounds.ValidTermFacet(r.Facet) {
			t.Errorf("facet %q is not mappable", r.Facet)
		}
		if !sounds.ValidValue(r.Facet, r.Value) {
			t.Errorf("value %q not allowed for facet %q", r.Value, r.Facet)
		}
		if sounds.NormTerm(r.Term) != r.Term || r.Term == "" || len(r.Term) > 60 {
			t.Errorf("term %q is not folded (or too long)", r.Term)
		}
		if r.Facet == "genre_family" {
			perGenre[r.Value]++
		}
		if r.Facet == "era" {
			eraValues[r.Value] = true
		}
	}
	for _, g := range []string{"pop", "rock", "hip-hop", "electronic", "dance", "reggae", "funk-soul", "r-and-b", "jazz", "blues", "folk", "country",
		"classical", "latin", "world", "metal", "punk", "ambient", "cinematic", "singer-songwriter"} {
		if perGenre[g] < 3 {
			t.Errorf("genre_family %q has %d terms, want >= 3", g, perGenre[g])
		}
	}
	for _, e := range []string{"50s", "60s", "70s", "80s", "90s", "00s", "10s", "20s"} {
		if !eraValues[e] {
			t.Errorf("era %q has no term", e)
		}
	}
}

func TestSeed_CoverageSynthetic(t *testing.T) {
	t.Parallel()
	ix := seedIndex()
	corpus := []string{
		"reggae, male vocal, warm, 76bpm", "female vocals, soft pop, dreamy", "90s eurodance, energetic, female vocal",
		"jazz, smooth jazz, piano, warm", "roots reggae, laid-back groove, male singer", "synthpop, 80s, upbeat",
		"acoustic guitar, singer songwriter, intimate", "heavy metal, aggressive, male vocal", "orchestral, cinematic, epic",
		"hip hop, boom bap, rap", "indie rock, male vocal, nostalgic", "country, honky tonk, male vocal", "ambient, drone, calm",
		"latin pop, salsa, upbeat", "classical piano solo", "folk, acoustic, female voice", "blues, slide, male vocal, gritty",
		"funk, soul, groovy bass, male vocal", "punk rock, fast, energetic", "disco, dance, female vocals, 120bpm",
		"trap, dark, 140bpm", "lofi, chill, instrumental", "r&b, smooth, female vocal", "house, deep house, club",
		"afrobeat, world music, upbeat", "bossa nova, female vocal, warm", "rock and roll, 50s, male vocal", "dubstep, aggressive",
		"gospel choir, uplifting", "techno, dark, 130bpm", "psychedelic rock, 60s, dreamy", "bluegrass, banjo, male vocal",
		"trance, euphoric, 138bpm", "new age, meditation, soothing", "soundtrack, film score, dramatic", "reggaeton, latin, upbeat",
		"ska, upbeat, brass", "grunge, 90s, raw", "swing, big band, 30s", "k pop, upbeat, female vocal",
	}
	n := 0
	for _, s := range corpus {
		for _, m := range sounds.Parse(ix, s).Matches {
			if m.Facet == "genre_family" {
				n++
				break
			}
		}
	}
	if pct := float64(n) / float64(len(corpus)); pct < 0.85 {
		t.Errorf("genre coverage %.0f%% (%d/%d), want >= 85%%", pct*100, n, len(corpus))
	}
}
