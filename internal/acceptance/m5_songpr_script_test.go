package acceptance_test

// Lane M5-S1 owns this file: the scripted calls of the Song PR operations.
func init() {
	uiScript["propose_patch"] = func(s *uiState) map[string]any {
		return map[string]any{"base_track_id": s.mother, "addresses_comment_ids": []string{s.comment}, "rationale": "scripted",
			"ops": []map[string]any{{"op": "set_style", "style": "scripted style"}}}
	}
	uiScript["get_patch"] = func(*uiState) map[string]any { return map[string]any{"id": "01900000-0000-7000-8000-00000000ffff"} }
	uiScript["list_patches"] = func(*uiState) map[string]any { return map[string]any{"limit": 10} }
	uiOrder = append(uiOrder, "propose_patch", "get_patch", "list_patches")
}
