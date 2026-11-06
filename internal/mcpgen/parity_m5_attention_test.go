package mcpgen_test

import lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"

// m5AttentionCases are the parity cases of the M5 attention operations (lane M5-A1 owns this file alone). The heat of a
// take and the 404 of an unknown one answer the same on every face.
var m5AttentionCases = []ParityCase{
	{Op: "get_listen_heat", Args: fixedArgs(map[string]any{"id": lt.TrackLuna})},
	{Op: "get_listen_heat", Variant: "unknown take", Args: fixedArgs(map[string]any{"id": "nope"}), Status: 404},
}
