package config

// loadEars reads the M5b Ears quote parameters (C330.5): the seconds a listening check (Tier B) is expected to take on the GPU
// worker, and the fetch time of an analyze job. The defaults are placeholders that the wave W2 canary calibrates. Errors name
// the variable, never the value.
func loadEars(c *Config, getenv func(string) string) error {
	for _, f := range []struct {
		name        string
		dst         *int
		def, lo, hi int64
	}{
		{"MM_EARS_LOAD_S", &c.EarsLoadS, 40, 0, 600},
		{"MM_EARS_RTF_MILLI", &c.EarsRTFMilli, 150, 10, 2000},
		{"MM_EARS_TRANSCRIBE_RTF_MILLI", &c.EarsTranscribeRTFMilli, 40, 0, 2000},
		{"MM_ANALYZE_FETCH_S", &c.AnalyzeFetchS, 20, 5, 300},
	} {
		n, err := intVar(getenv, f.name, f.def, f.lo, f.hi)
		if err != nil {
			return err
		}
		*f.dst = int(n)
	}
	return nil
}
