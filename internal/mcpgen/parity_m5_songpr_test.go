package mcpgen_test

import (
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// m5SongPRCases are the parity cases of the M5 Song PR operations (lane M5-S1 owns this file alone). The three cases are one
// contiguous run on purpose: get_patch reads the id propose_patch answered (prev), list_patches then finds that one patch.
// The patch edits Luna's chorus line against the fixture's chorus fix comment; ids and the author reference differ per face.
var m5SongPRCases = []ParityCase{
	{Op: "propose_patch", Args: fixedArgs(map[string]any{"base_track_id": lt.TrackLuna, "addresses_comment_ids": []string{lt.CommentLunaFix}, "rationale": "parity",
		"ops": []map[string]any{{"op": "replace_line", "section_idx": 1, "line_idx": 0, "text": "sing it louder"}}}), Masks: []string{"id", "author"}},
	{Op: "get_patch", Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		id, _ := prev["id"].(string)
		return map[string]any{"id": id}
	}, Masks: []string{"id", "author"}},
	{Op: "list_patches", Args: fixedArgs(map[string]any{"track_id": lt.TrackLuna, "limit": 5}), Masks: []string{"id", "author"}},
	{Op: "propose_patch", Variant: "refused", Args: fixedArgs(map[string]any{"base_track_id": lt.TrackLuna, "addresses_comment_ids": []string{lt.CommentLunaNote}, "rationale": "parity",
		"ops": []map[string]any{{"op": "replace_line", "section_idx": 0, "line_idx": 0, "text": "x"}}}), Status: 422},
}
