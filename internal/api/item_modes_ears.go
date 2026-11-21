package api

import "github.com/nicolasacchi/metamusic/internal/lineage"

// The Ears track's item mode (M5b C340.1): analyze listens to chosen takes. It joins ItemIn.Mode's enum through the registry
// (item_modes.go) instead of the struct tag, so the other tracks' modes keep their place.
func init() { addItemModes(lineage.ModeAnalyze) }
