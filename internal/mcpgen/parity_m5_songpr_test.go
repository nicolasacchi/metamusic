package mcpgen_test

// m5SongPRCases are the parity cases of the M5 Song PR operations (lane M5-S1 owns this file alone). Until it lands each stub
// answers the same 501 on every face; the lane replaces the cases with real ones and drops Status 501.
var m5SongPRCases = []ParityCase{
	{Op: "propose_patch", Args: fixedArgs(map[string]any{"base_track_id": "t", "addresses_comment_ids": []string{"c"}, "rationale": "r", "ops": []map[string]any{{"op": "set_style", "style": "x"}}}), Status: 501},
	{Op: "get_patch", Args: fixedArgs(map[string]any{"id": "x"}), Status: 501},
	{Op: "list_patches", Args: fixedArgs(map[string]any{"limit": 5}), Status: 501},
}
