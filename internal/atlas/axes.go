package atlas

import (
	"slices"
	"strconv"
)

// NoneBand is the value of the trailing band of a categorical axis for a Song without a value.
const NoneBand = "(none)"

// camelot lists the 24 Camelot codes in wheel order.
func camelot() []string {
	out := make([]string, 0, 24)
	for n := 1; n <= 12; n++ {
		out = append(out, strconv.Itoa(n)+"A", strconv.Itoa(n)+"B")
	}
	return out
}

// Axes is the registry (extension move 2), pinned by testdata/axes_v1.golden.json. Numeric domains are in the column's
// fixed-point units: centi-BPM, energy times 10 000 and duration in centiseconds (the column is seconds; the scope query
// converts). The categorical orders are the L1 seed value lists; only the categories present in a scope become bands.
// The key axis has no column of its own: its value is the Camelot code built from key_num and key_letter, with
// key_basis as its basis.
var Axes = []AxisDef{
	{Name: "tempo", Kind: KindNumeric, Unit: "BPM", Scale: "log2", Column: "bpm_x100", BasisColumn: "bpm_basis", Min: 6000, Max: 20000},
	{Name: "bpm_requested", Kind: KindNumeric, Unit: "BPM", Scale: "log2", Column: "bpm_requested_x100", Min: 6000, Max: 20000},
	{Name: "bpm_measured", Kind: KindNumeric, Unit: "BPM", Scale: "log2", Column: "bpm_measured_x100", Min: 6000, Max: 20000},
	{Name: "energy", Kind: KindNumeric, Scale: "linear", Column: "energy_x10000", BasisColumn: "energy_basis", Min: 0, Max: 10000},
	{Name: "duration", Kind: KindNumeric, Unit: "s", Scale: "linear", Column: "duration_s", Min: 6000, Max: 36000},
	{Name: "family", Kind: KindCategorical, Column: "family", Order: []string{"pop", "rock", "hip-hop", "electronic", "dance", "reggae", "funk-soul", "r-and-b", "jazz", "blues", "folk", "country", "classical", "latin", "world", "metal", "punk", "ambient", "cinematic", "singer-songwriter"}},
	{Name: "vocal", Kind: KindCategorical, Column: "vocal", Order: []string{"male", "female", "duet", "choir", "rap", "instrumental"}},
	{Name: "era", Kind: KindCategorical, Column: "era", Order: []string{"50s", "60s", "70s", "80s", "90s", "00s", "10s", "20s"}},
	{Name: "mood", Kind: KindCategorical, Column: "mood", Order: []string{"upbeat", "chill", "melancholic", "romantic", "dark", "dreamy", "energetic", "nostalgic", "aggressive", "warm"}},
	{Name: "language", Kind: KindCategorical, Column: "language", Order: []string{"en", "it", "es", "fr", "de", "pt"}},
	{Name: "key", Kind: KindCategorical, Column: "key", BasisColumn: "key_basis", Order: camelot()},
}

// Axis returns the registry entry called name.
func Axis(name string) (AxisDef, bool) {
	i := slices.IndexFunc(Axes, func(a AxisDef) bool { return a.Name == name })
	if i < 0 {
		return AxisDef{}, false
	}
	return Axes[i], true
}
