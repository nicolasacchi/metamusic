// Package tokens measures repetition in YuE2's semantic token streams (C49.3,
// definition mm-token-loops/1). This file holds the result types shared with
// the Ears dossier; the metrics themselves land in M2b-E1.
package tokens

// Repeat is the longest substring occurring twice without overlap.
type Repeat struct {
	Tokens    int     `json:"tokens"`
	S         float64 `json:"s"`
	FirstAtS  float64 `json:"first_at_s"`
	SecondAtS float64 `json:"second_at_s"`
}

// Periodic is the longest run with a constant period.
type Periodic struct {
	PeriodTokens int     `json:"period_tokens"`
	PeriodS      float64 `json:"period_s"`
	SpanTokens   int     `json:"span_tokens"`
	SpanS        float64 `json:"span_s"`
	AtS          float64 `json:"at_s"`
	Repeats      float64 `json:"repeats"`
}

// Stream holds the metrics of one token stream.
type Stream struct {
	Index              int       `json:"index"`
	NTokens            int       `json:"n_tokens"`
	DistinctTokens     int       `json:"distinct_tokens"`
	TokensPerS         float64   `json:"tokens_per_s"`         // NTokens / durationS, 2 decimals
	Distinct4GramRatio float64   `json:"distinct_4gram_ratio"` // |distinct 4-grams| / max(1, n-3)
	LongestRepeat      Repeat    `json:"longest_repeat"`
	PeriodicRun        Periodic  `json:"periodic_run"`
	GzipRatio10s       []float64 `json:"gzip_ratio_10s" nullable:"false"` // per full 10 s window (a final partial window counts if >= 50 % full)
	GzipRatioMin       *float64  `json:"gzip_ratio_min"`
	GzipRatioMedian    *float64  `json:"gzip_ratio_median"`
	Encoding           string    `json:"encoding"` // "u16le" if max token < 65536 else "u32le"
}

// Report is the token-loop analysis of one semantic.npy.
type Report struct {
	DType    string   `json:"dtype"`
	Shape    []int    `json:"shape" nullable:"false"`
	TimeAxis int      `json:"time_axis"`
	Streams  []Stream `json:"streams" nullable:"false"`
}
