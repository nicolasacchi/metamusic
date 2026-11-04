package mcpgen_test

// m5AttentionCases are the parity cases of the M5 attention operations (lane M5-A1 owns this file alone). Until it lands each stub
// answers the same 501 on every face; the lane replaces the cases with real ones and drops Status 501.
var m5AttentionCases = []ParityCase{
	{Op: "get_listen_heat", Args: fixedArgs(map[string]any{"id": "x"}), Status: 501},
}
