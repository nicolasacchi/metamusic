package mcpgen_test

// l3NextActionsCases are the parity cases of list_next_actions (lane NA1 owns this file). Until NA1 lands the stub answers 501
// on every face; NA1 replaces the case and drops Status.
var l3NextActionsCases = []ParityCase{
	{Op: "list_next_actions", Args: fixedArgs(map[string]any{"limit": 5}), Status: 501},
}
