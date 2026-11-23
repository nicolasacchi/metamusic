package api

import "github.com/nicolasacchi/metamusic/internal/lineage"

// The Composer's Desk item mode (M5b C360.2, amendment A4): render_plan renders the score of stored Desk plans through the ordinary
// estimate and confirm flow. It joins ItemIn.Mode's enum through the registry (item_modes.go) like the Ears track's analyze.
func init() { addItemModes(lineage.ModeRenderPlan, lineage.ModePlanSeeds) }
