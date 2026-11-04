package mcpgen_test

// m5DuelsCases are the parity cases of the M5 duel operations (lane M5-D1 owns this file alone). Until it lands each stub
// answers the same 501 on every face; the lane replaces the cases with real ones and drops Status 501.
var m5DuelsCases = []ParityCase{
	{Op: "suggest_duel_pairs", Args: fixedArgs(map[string]any{"scope": "global", "k": 3}), Status: 501},
}
