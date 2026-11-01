package mcpgen_test

// l3NextActionsCases are the parity cases of list_next_actions (lane NA1 owns this file): REST, the generic MCP tool and the
// SPA client answer the same page over the L3 fixture. The 501 stub case is gone.
var l3NextActionsCases = []ParityCase{
	{Op: "list_next_actions", Args: fixedArgs(map[string]any{"limit": 5})},
}
