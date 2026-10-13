package mcpgen_test

import (
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// agentCases are the M2a-B8 cases: the context bundle, listen events and the
// audit log. The audit rows of the two rigs carry per-face facts (transport,
// client, token ids, request hashes), which the masks hide; the shared facts
// (operation, status, error code, time) must agree.
var agentCases = []ParityCase{
	{Op: "get_track_context", Args: fixedArgs(map[string]any{"id": lt.TrackLuna})},
	{Op: "log_listen_events", Args: fixedArgs(map[string]any{"events": []map[string]any{{
		"id": "01900000-0000-7000-8000-0000000000a1", "track_id": lt.TrackLuna, "session_id": "session-1", "kind": "start", "client_at": "2026-10-06T11:59:00Z"}}})},
	{Op: "get_audit", Args: fixedArgs(map[string]any{"op": []string{"update_track"}, "limit": 10}), Last: true,
		Masks: []string{"id", "principal", "transport", "client", "args_sha256", "idempotency", "created_ids"}},
}

func init() {
	parityCases = append(parityCases, agentCases...)
}
