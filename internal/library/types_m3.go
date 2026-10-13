package library

import "github.com/danielgtaylor/huma/v2"

// Types M3 adds to the track, draft and stats views (C81.1). The handlers that fill them belong to the
// owning lane (M3-B6); until then they carry their zero values.

// Env is the environment fingerprint of a generated take (D-M3-26).
type Env struct {
	EnvID       string            `json:"env_id"`
	Engine      string            `json:"engine"`
	PkgVersion  string            `json:"pkg_version"`
	CodeCommit  string            `json:"code_commit"`
	Weights     map[string]string `json:"weights" nullable:"false" doc:"Weight repository to pinned revision"`
	Decoder     string            `json:"decoder"`
	Backend     string            `json:"backend"`
	GPUClass    string            `json:"gpu_class"`
	ImageDigest string            `json:"image_digest"`
	Detail      EnvDetail         `json:"detail"`
}

// EnvDetail is what was observed the first time the fingerprint was seen.
type EnvDetail struct {
	GPUName     string `json:"gpu_name"`
	CUDA        string `json:"cuda"`
	Torch       string `json:"torch"`
	WorkerBuild string `json:"worker_build"`
	Python      string `json:"python"`
}

// GenerationCost is the cost shown for a generated take: reconciled when billing has been matched,
// provisional while the batch is outstanding, the estimate before that.
type GenerationCost struct {
	DisplayMicroUSD int64  `json:"display_micro_usd"`
	DisplayUSD      string `json:"display_usd" doc:"Exact decimal dollars"`
	Basis           string `json:"basis" enum:"estimate,provisional,reconciled"`
}

// GenerationInfo says how a take was generated; null for imported takes.
type GenerationInfo struct {
	JobID     string          `json:"job_id"`
	BatchID   string          `json:"batch_id"`
	Operator  string          `json:"operator" doc:"reroll_seed, same_seed_edit, lyric_edit, style_edit, from_draft or compose_new"`
	Rationale *string         `json:"rationale"`
	Cost      *GenerationCost `json:"cost"`
	Env       *Env            `json:"env"`
}

// BreakdownTerm is one style term of the generation report.
type BreakdownTerm struct {
	Term      string   `json:"term"`
	Tracks    int      `json:"tracks"`
	Rated     int      `json:"rated"`
	MeanStars *float64 `json:"mean_stars" doc:"Mean human stars over rated tracks (a float of stars, not money); null when none"`
}

// BreakdownSeed is one seed used by at least two tracks.
type BreakdownSeed struct {
	Seed      int64    `json:"seed"`
	Tracks    int      `json:"tracks"`
	Rated     int      `json:"rated"`
	MeanStars *float64 `json:"mean_stars"`
}

// GenerationStats is the generation block of get_stats.
type GenerationStats struct {
	JobsByState         map[string]int `json:"jobs_by_state" nullable:"false"`
	TakesMonth          int            `json:"takes_month"`
	SpendMonthMicroUSD  int64          `json:"spend_month_micro_usd"`
	SpendMonthUSD       string         `json:"spend_month_usd"`
	OutstandingMicroUSD int64          `json:"outstanding_micro_usd"`
	OutstandingUSD      string         `json:"outstanding_usd"`
}

// Breakdown is the spec's Phase-3 report: which style terms and seeds produced the best-rated takes.
// Reference tracks (stub recipes) are excluded: their seed 0 is a placeholder (D-M3-34).
type Breakdown struct {
	ByStyleTerm []BreakdownTerm `json:"by_style_term" nullable:"false" doc:"Top 30 by tracks"`
	BySeed      []BreakdownSeed `json:"by_seed" nullable:"false" doc:"Seeds used by at least two tracks, top 30"`
}

type (
	envShape        Env
	generationShape GenerationInfo
	genCostShape    GenerationCost
)

// Schema implements huma.SchemaProvider.
func (Env) Schema(r huma.Registry) *huma.Schema { return nullableObject[envShape](r) }

// Schema implements huma.SchemaProvider.
func (GenerationInfo) Schema(r huma.Registry) *huma.Schema { return nullableObject[generationShape](r) }

// Schema implements huma.SchemaProvider.
func (GenerationCost) Schema(r huma.Registry) *huma.Schema { return nullableObject[genCostShape](r) }
