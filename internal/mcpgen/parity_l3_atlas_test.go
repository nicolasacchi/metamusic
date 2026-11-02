package mcpgen_test

// l3AtlasCases are the parity cases of get_song_atlas (lane AT2 owns this file): REST, the generic MCP tool and the SPA client
// answer the same map, with a filter and an overlay. The 501 stub case is gone.
var l3AtlasCases = []ParityCase{
	{Op: "get_song_atlas", Args: fixedArgs(map[string]any{"x": "tempo", "y": "energy", "color_by": "state", "dim": true, "state": []string{"rendered", "judged"}, "limit": 50})},
	{Op: "get_song_atlas", Variant: "categorical", Args: fixedArgs(map[string]any{"x": "family", "y": "vocal", "color_by": "muse"})},
}
