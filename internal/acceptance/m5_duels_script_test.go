package acceptance_test

// Lane M5-D1 owns this file: the scripted call of suggest_duel_pairs.
func init() {
	uiScript["suggest_duel_pairs"] = func(*uiState) map[string]any { return map[string]any{"scope": "global", "k": 3} }
	uiOrder = append(uiOrder, "suggest_duel_pairs")
}
