package generation

import (
	"encoding/json"
	"time"
)

// Limits (D-M3-7).
const (
	MaxItems = 20
	MaxTakes = 50
	// TokenTTL is how long an estimate's confirmation token lives (C72.2).
	TokenTTL = 10 * time.Minute
)

// Issue is one lint finding of a take (the engine-neutral subset the estimate reports).
type Issue struct {
	Code     string `json:"code"`
	Field    string `json:"field"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// planTake is one expanded take, the unit of estimates.items_json (C71.4). The create transaction trusts
// only this JSON.
type planTake struct {
	TakeIndex     int             `json:"take_index"`
	ItemIndex     int             `json:"item_index"`
	Mode          string          `json:"mode"`
	Operator      string          `json:"operator"`
	ParentTrackID *string         `json:"parent_track_id"`
	DraftID       *string         `json:"draft_id"`
	DraftRevision *int            `json:"draft_revision"`
	RequestJSON   string          `json:"request_json"`
	RecipeHash    string          `json:"recipe_hash"`
	ContentHash   string          `json:"content_hash"`
	RenderHash    string          `json:"render_hash"`
	RequestID     string          `json:"request_id"`
	Seed          int64           `json:"seed"`
	Title         string          `json:"title"`
	ReuseRecipeID *string         `json:"reuse_recipe_id"`
	Params        paramsDoc       `json:"params"`
	Patch         json.RawMessage `json:"patch"` // null or the patch document

	ExpectedAudioS   int     `json:"expected_audio_s"` // never below MinAudioS
	MinAudioS        int     `json:"min_audio_s"`      // forced minimum render length (worker min_tokens); 0 = none
	ExpectedS        int     `json:"expected_s"`
	TimeoutS         int     `json:"timeout_s"`
	ExpectedMicroUSD int64   `json:"expected_micro_usd"`
	TakeMicroUSD     int64   `json:"take_micro_usd"`
	OverheadMicroUSD int64   `json:"attempt_overhead_micro_usd"`
	MaxMicroUSD      int64   `json:"max_micro_usd"`
	InputBytes       int     `json:"input_bytes"`
	Warnings         []Issue `json:"warnings"`
}

// paramsDoc is jobs.mutation_json.params (C71.4).
type paramsDoc struct {
	Mode       string  `json:"mode"`
	SeedPolicy string  `json:"seed_policy"`
	NSeeds     int     `json:"n_seeds"`
	Via        *string `json:"via"`
}

// quoteDoc is estimates.quote_json: the frozen inputs of the quote.
type quoteDoc struct {
	PricePPH         int64    `json:"price_pph_micro_usd"`
	ListPricePPH     int64    `json:"list_price_pph_micro_usd"`
	MarginPPM        int64    `json:"price_margin_ppm"`
	RTFMilli         int      `json:"rtf_milli"`
	ColdStartS       int      `json:"cold_start_s"`
	IdleS            int      `json:"idle_s"`
	WorkersMax       int      `json:"workers_max"`
	MaxRuntimeS      int      `json:"max_runtime_s"`
	KillSlackS       int      `json:"kill_slack_s"`
	AttemptOverheadS int      `json:"attempt_overhead_s"`
	ETAS             int      `json:"eta_s"`
	GPUPools         []string `json:"gpu_pools"`
	PriceObservedAt  *string  `json:"price_observed_at"`
}
