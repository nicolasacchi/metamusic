package mcpgen_test

// l3AtlasCases are the parity cases of get_song_atlas (lane AT2 owns this file). Until AT2 lands the stub answers 501 on every
// face; AT2 replaces the case and drops Status.
var l3AtlasCases = []ParityCase{
	{Op: "get_song_atlas", Args: fixedArgs(map[string]any{"x": "tempo", "y": "energy"}), Status: 501},
}
