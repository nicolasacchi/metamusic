package features

// The energy prior of mm-energy-prior/1 (C135.4): committed values x 10 000 per facet value. A new taxonomy value
// without a row here fails TestFeatures_RegistryGolden; a changed number needs a new PriorDef and golden.
var (
	moodPrior = map[string]int{
		"upbeat": 7500, "chill": 2500, "melancholic": 3000, "romantic": 3500, "dark": 5500,
		"dreamy": 2500, "energetic": 9000, "nostalgic": 4000, "aggressive": 9500, "warm": 3500,
	}
	genrePrior = map[string]int{
		"ambient": 1500, "classical": 3000, "folk": 3500, "singer-songwriter": 3000, "jazz": 4000,
		"blues": 4000, "country": 4500, "reggae": 5000, "world": 5000, "cinematic": 5000,
		"r-and-b": 5000, "funk-soul": 6500, "pop": 6000, "latin": 6500, "electronic": 7000,
		"hip-hop": 7000, "rock": 7500, "dance": 8500, "punk": 9000, "metal": 9500,
	}
	tempoPrior = map[string]int{"slow": 2500, "mid": 4500, "upbeat": 6500, "fast": 8000, "rapid": 9000}
)

// PriorTable is the committed prior, as pinned by testdata/prior_v1.golden.json.
type PriorTable struct {
	Def         string         `json:"def"`
	Mood        map[string]int `json:"mood"`
	GenreFamily map[string]int `json:"genre_family"`
	TempoBand   map[string]int `json:"tempo_band"`
}

// Prior returns a copy of the committed prior.
func Prior() PriorTable {
	cp := func(m map[string]int) map[string]int {
		o := make(map[string]int, len(m))
		for k, v := range m {
			o[k] = v
		}
		return o
	}
	return PriorTable{Def: PriorDef, Mood: cp(moodPrior), GenreFamily: cp(genrePrior), TempoBand: cp(tempoPrior)}
}

// divHalfUp is sum/n rounded half up, for non-negative sums and n > 0.
func divHalfUp(sum, n int) int { return (2*sum + n) / (2 * n) }

// facetMean is the mean prior of the known values; ok is false when none is known.
func facetMean(values []string, table map[string]int) (int, bool) {
	sum, n := 0, 0
	for _, v := range values {
		if p, known := table[v]; known {
			sum += p
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return divHalfUp(sum, n), true
}

// PredictEnergy is the facet prior, energy x 10 000 (C135.4): the mean of the per-facet means over mood, genre family
// and tempo band; unknown values are skipped and a facet with no known value counts as absent.
func PredictEnergy(f Facets) (x10000 int, ok bool) {
	sum, n := 0, 0
	for _, p := range []struct {
		v []string
		t map[string]int
	}{{f.Mood, moodPrior}, {f.GenreFamily, genrePrior}, {f.TempoBand, tempoPrior}} {
		if m, known := facetMean(p.v, p.t); known {
			sum += m
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return divHalfUp(sum, n), true
}
