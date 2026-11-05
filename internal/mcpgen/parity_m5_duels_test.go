package mcpgen_test

// m5DuelsCases are the parity cases of the M5 duel operations (lane M5-D1 owns this file alone).
var m5DuelsCases = []ParityCase{
	{Op: "suggest_duel_pairs", Args: fixedArgs(map[string]any{"scope": "global", "k": 3})},
	{Op: "suggest_duel_pairs", Variant: "section", Args: fixedArgs(map[string]any{"scope": "global", "k": 3, "section_kind": "chorus", "section_ordinal": 1})},
}
