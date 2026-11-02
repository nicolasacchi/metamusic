package analysis

// EnergyDef identifies the energy analyzer (C136.2). analyses.version = EnergyDef + " " + tool token. The definition is a
// decision: every constant below, the weights and the piecewise maps change only with a new mm-energy/2 and a golden.
const EnergyDef = "mm-energy/1"

// energyParams is the one table of mm-energy/1.
var energyParams = struct {
	MinS float64 // under this many analysed seconds there is no energy

	OnsetSigma float64 // a peak of the onset envelope must exceed mean + OnsetSigma·σ
	OnsetGapS  float64 // and lie at least this far from the previous accepted peak
	OnsetFloor float64 // and reach this absolute strength (the envelope of a click track peaks in the hundreds, a steady pad stays under 4)

	CentroidWindow  int     // STFT frame length (Hann)
	CentroidHop     int     // STFT hop
	CentroidLoHz    float64 // band of the centroid
	CentroidHiHz    float64
	CentroidRangeDB float64 // only frames within this many dB of the loudest frame count

	SilentDBFS float64 // every 1024-sample frame RMS below this: silent, no energy

	// piecewise-linear maps to [0, 1], clamped: value Lo gives 0, value Hi gives 1
	OnsetLo, OnsetHi       float64 // onsets per second
	CentroidLo, CentroidHi float64 // Hz
	LoudLo, LoudHi         float64 // integrated LUFS

	WOnset, WCentroid, WLoud float64 // weights; the loudness term is dropped (weights renormalised) when no LUFS is stored
}{
	MinS:       20,
	OnsetSigma: 1.0, OnsetGapS: 0.12, OnsetFloor: 10,
	CentroidWindow: 1024, CentroidHop: 512, CentroidLoHz: 40, CentroidHiHz: 5000, CentroidRangeDB: 50,
	SilentDBFS: -60,
	OnsetLo:    0.5, OnsetHi: 5.0,
	CentroidLo: 600, CentroidHi: 2800,
	LoudLo: -26, LoudHi: -9,
	WOnset: 0.45, WCentroid: 0.30, WLoud: 0.25,
}
