package features

// Constants of the features layer (L3 C133).
const (
	Def        = "mm-features/1"
	PriorDef   = "mm-energy-prior/1"
	BatchSongs = 100
)

// Plausible tempo range of a stored value; anything outside is discarded (C135.2).
const (
	MinBPM = 20.0
	MaxBPM = 300.0
)
