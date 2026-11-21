package api

// Schema names of the M5b Ears track (C330.3), registered per file (G1 D-G1-12). The Tier B shapes reuse the pure package's
// document types, whose Go names (Stages, Models, Timings, Resources) are too generic for the one OpenAPI namespace; the
// adherence block is TierBAdherence, never Adherence (L3's).
func init() {
	addWireNames(map[string]string{
		"internal/ears.TierB":           "TierB",
		"internal/ears.TierBDiction":    "TierBDiction",
		"internal/ears.TierBAdherence":  "TierBAdherence",
		"internal/ears/tierb.Stages":    "TierBStages",
		"internal/ears/tierb.Models":    "TierBModels",
		"internal/ears/tierb.Model":     "TierBModel",
		"internal/ears/tierb.ASRModel":  "TierBASRModel",
		"internal/ears/tierb.Timings":   "TierBTimings",
		"internal/ears/tierb.Resources": "TierBResources",
	})
}
