package engine

import "encoding/json"

// Recipe holds the engine-neutral facts of one recipe that the library, lineage and generation read
// (C163). Canonical is the engine-native canonical request, stored as recipes.request_json.
type Recipe struct {
	ID, Style, Lyrics, Cot string // Style is the yue2 style or an acestep caption; Cot is the engine's planning switch
	Seed                   int64
	Canonical              []byte
	Hash, CoreHash         string // hex sha256 of Canonical and of its identity-free core
	Warnings               []Issue
}

// ModeParams are the engine-independent facts of one derivation: the Operator Ledger operator, the seed
// and request id of the child, and the recorded policy.
type ModeParams struct {
	Mode, Operator, SeedPolicy string
	NSeeds                     int
	Seed                       int64
	ID                         string
}

// RecipeCodec is an optional capability of an Engine (type-asserted, like MinAudioPlanner): it parses,
// re-identifies and patches the engine's own canonical request. yue2 implements it by delegating to its
// existing functions, byte for byte.
type RecipeCodec interface {
	// ParseRecipe is strict; parsing a canonical request returns it unchanged.
	ParseRecipe(raw json.RawMessage) (Recipe, []Issue)
	// WithSeedAndID returns base with a new seed and request id, re-canonicalised.
	WithSeedAndID(base Recipe, seed int64, id string) (Recipe, error)
	// ApplyMode derives a child recipe. patch is the engine's own patch value (nil = none); an engine
	// that does not know the type of patch returns an error.
	ApplyMode(base Recipe, p ModeParams, patch any) (Recipe, error)
}

// CodecOf returns the engine's RecipeCodec, if it has one.
func CodecOf(e Engine) (RecipeCodec, bool) {
	c, ok := e.(RecipeCodec)
	return c, ok
}

// IngestPolicy is an optional capability of an Engine: which yue2-shaped worker facts its takes carry. An
// engine that does not implement it reports no truncation and teaches no RTF, so a new engine can never
// corrupt yue2's learned real-time factor or fail ingest for a field it does not have (C156).
type IngestPolicy interface {
	// ReportsTruncation reports whether the worker output must carry truncated{abc,semantic}.
	ReportsTruncation() bool
	// LearnsRTF reports whether ingest records the take's real-time factor in rtf_stats.
	LearnsRTF() bool
}

// ReportsTruncation is e's IngestPolicy answer; false for an engine without one.
func ReportsTruncation(e Engine) bool {
	p, ok := e.(IngestPolicy)
	return ok && p.ReportsTruncation()
}

// LearnsRTF is e's IngestPolicy answer; false for an engine without one.
func LearnsRTF(e Engine) bool {
	p, ok := e.(IngestPolicy)
	return ok && p.LearnsRTF()
}
