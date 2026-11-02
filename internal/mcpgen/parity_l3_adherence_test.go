package mcpgen_test

// l3AdherenceCases are the parity cases of get_adherence (lane AD owns this file): REST, the generic MCP tool and the SPA client
// answer the same document. The 501 stub case is gone.
var l3AdherenceCases = []ParityCase{
	{Op: "get_adherence", Args: fixedArgs(map[string]any{"min_n": 3})},
}
