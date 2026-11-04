package mcpgen_test

// m5CallbacksCases are the parity cases of the M5 callback operations (lane M5-C1 owns this file alone). Until it lands each stub
// answers the same 501 on every face; the lane replaces the cases with real ones and drops Status 501.
var m5CallbacksCases = []ParityCase{
	{Op: "list_callbacks", Args: fixedArgs(map[string]any{"limit": 5}), Status: 501},
	{Op: "get_callback", Args: fixedArgs(map[string]any{"id": "x"}), Status: 501},
	{Op: "submit_callback_verdict", Args: fixedArgs(map[string]any{"id": "x", "verdict": "fixed"}), Status: 501},
}
