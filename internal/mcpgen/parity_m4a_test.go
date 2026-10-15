package mcpgen_test

import (
	"encoding/json"
	"strings"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// m4aCases covers the eight M4a operations (C113, C117.2): all are MCP generic. The three read ops and the five
// admin ones run on the admin-scope PAT of every face. Until the owning lane replaces a stub, the REST answer is
// 501 NOT_IMPLEMENTED with details.task = "M4a-<task>" and the runner skips the case by name (m4aStubTask); B3's
// TestNoPendingM4aStubs turns the skip into a failure. The management cases mask ids, timestamps, keys and the
// one-time secrets; the owning lane tunes the masks when it fills the handler.
var m4aCases = []ParityCase{
	{Op: "get_offline_set", Masks: []string{"updated_at"}},
	{Op: "get_download_link", Args: fixedArgs(map[string]any{"id": lt.TrackLuna})},
	{Op: "get_prompt", Variant: "critique", Args: fixedArgs(map[string]any{"name": "critique_track", "track_id": lt.TrackLuna})},
	{Op: "list_connector_grants", Masks: []string{"id", "created_at", "updated_at", "last_used_at", "updated_by", "fetched_at", "keys"}},
	{Op: "set_connector_grant", Args: fixedArgs(map[string]any{"id": lt.ConnectorGrantID, "scopes": []string{"read", "write", "generate"}}), SkipGeneric: true,
		Masks: []string{"id", "created_at", "updated_at", "last_used_at", "updated_by", "revoked_by"}},
	{Op: "list_subsonic_clients", Masks: []string{"id", "created_at", "created_by", "last_seen_at", "api_key_prefix"}},
	{Op: "create_subsonic_client", Args: fixedArgs(map[string]any{"name": "Parity phone"}), SkipGeneric: true,
		Masks: []string{"id", "created_at", "created_by", "api_key", "password", "api_key_prefix", "client"}},
	{Op: "revoke_subsonic_client", Args: fixedArgs(map[string]any{"id": lt.SubsonicClientID}), SkipGeneric: true,
		Masks: []string{"id", "created_at", "created_by", "revoked_at", "api_key_prefix"}},
}

func init() {
	parityCases = append(parityCases, m4aCases...)
}

// m4aStubTask returns the task a REST body names when it is an M4a stub's 501 NOT_IMPLEMENTED problem, else "".
func m4aStubTask(status int, body []byte) string {
	if status != 501 {
		return ""
	}
	var p struct {
		Code    string `json:"code"`
		Details struct {
			Task string `json:"task"`
		} `json:"details"`
	}
	if json.Unmarshal(body, &p) != nil || p.Code != "NOT_IMPLEMENTED" || !strings.HasPrefix(p.Details.Task, "M4a-") {
		return ""
	}
	return p.Details.Task
}
