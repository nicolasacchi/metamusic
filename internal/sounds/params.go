package sounds

// Constants of the Sound facets (C87, D-L1-17).
const (
	// MaxMappingsPerCall bounds one map_sound_term call.
	MaxMappingsPerCall = 50

	// Tempo band edges in BPM: slow < 80 <= mid < 110 <= upbeat < 130 <= fast < 160 <= rapid.
	BandMid, BandUpbeat, BandFast, BandRapid = 80, 110, 130, 160

	// TempoOctaveTol: a measured tempo within 6 % of 2x or 1/2 the requested one is an octave error, not a mismatch.
	TempoOctaveTol = 0.06
	// TempoAbsTol and TempoRelTol: a measurement further than max(4, 5 % of requested) from the request is a mismatch.
	TempoAbsTol = 4.0
	TempoRelTol = 0.05

	// MaxPartRunes cuts one style part before matching; MaxUnmappedRunes is the longest part reported as unmapped;
	// MaxUnmapped caps the unmapped list; MaxPhraseWords is the longest matched n-gram.
	MaxPartRunes     = 160
	MaxUnmappedRunes = 40
	MaxUnmapped      = 20
	MaxPhraseWords   = 5
)
