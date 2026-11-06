package mcpgen_test

// m5CallbacksCases are the parity cases of the M5 callback operations (lane M5-C1 owns this file alone). The parity rig has no Song PR
// child, so the reads see an empty list and the id operations the same 404 on every face; the behaviour with data (verdicts,
// transitions, isolation) is covered by internal/callbacks and ops_callbacks_m5_test.go.
var m5CallbacksCases = []ParityCase{
	{Op: "list_callbacks", Args: fixedArgs(map[string]any{"limit": 5})},
	{Op: "list_callbacks", Variant: "filtered", Args: fixedArgs(map[string]any{"state": []string{"pending"}, "kind": []string{"fix", "keep"}, "limit": 5})},
	{Op: "get_callback", Args: fixedArgs(map[string]any{"id": "01900000-0000-7000-8000-00000000ffff"}), Status: 404},
	{Op: "submit_callback_verdict", Args: fixedArgs(map[string]any{"id": "01900000-0000-7000-8000-00000000ffff", "verdict": "fixed"}), Status: 404},
}
