package acceptance_test

// Lane M5-A1 owns this file: the scripted call of get_listen_heat.
func init() {
	uiScript["get_listen_heat"] = func(s *uiState) map[string]any { return map[string]any{"id": s.track} }
	uiOrder = append(uiOrder, "get_listen_heat")
}
