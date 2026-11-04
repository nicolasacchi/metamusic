package acceptance_test

// Lane M5-C1 owns this file: the scripted calls of the callback operations.
func init() {
	uiScript["list_callbacks"] = func(*uiState) map[string]any { return map[string]any{"state": []string{"pending"}, "limit": 10} }
	uiScript["get_callback"] = func(*uiState) map[string]any { return map[string]any{"id": "01900000-0000-7000-8000-00000000ffff"} }
	uiScript["submit_callback_verdict"] = func(*uiState) map[string]any {
		return map[string]any{"id": "01900000-0000-7000-8000-00000000ffff", "verdict": "fixed"}
	}
	uiOrder = append(uiOrder, "list_callbacks", "get_callback", "submit_callback_verdict")
}
