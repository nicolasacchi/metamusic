package acceptance_test

import "slices"

// Lane M5-S1 owns this file: the scripted calls of the Song PR operations. They run right after add_comment (before
// update_comment resolves its note): the patch addresses that note on the archive track the script works with. The note has no
// time, so the patch names section 0 as its scope; it replaces the first lyric line. get_patch reads the id propose_patch answered
// (uiState.patch, set in afterStep).
func init() {
	uiScript["propose_patch"] = func(s *uiState) map[string]any {
		return map[string]any{"base_track_id": s.track, "addresses_comment_ids": []string{s.comment}, "rationale": "scripted", "extra_sections": []int{0},
			"ops": []map[string]any{{"op": "replace_line", "section_idx": 0, "line_idx": 0, "text": "a scripted line"}}}
	}
	uiScript["get_patch"] = func(s *uiState) map[string]any { return map[string]any{"id": s.patch} }
	uiScript["list_patches"] = func(s *uiState) map[string]any { return map[string]any{"track_id": s.track, "limit": 10} }
	uiOrder = slices.Insert(uiOrder, slices.Index(uiOrder, "add_comment")+1, "propose_patch", "get_patch", "list_patches")
}
