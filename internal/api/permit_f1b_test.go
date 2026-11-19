package api_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
)

// C305 (lane F1b): the classified op families, spelled out so a retag or a dropped tag fails here.
var (
	f1bListen = []string{
		"search_tracks",
		"get_track",
		"get_track_peaks",
		"get_track_analysis",
		"get_track_spectrogram",
		"get_media_links",
		"get_download_link",
		"get_clip",
		"compare_tracks",
		"get_ladder",
		"get_library_facets",
		"list_songs",
		"get_song",
		"get_song_atlas",
		"search_sounds",
		"get_sound_taxonomy",
		"list_tags",
		"get_lineage",
		"list_playlists",
		"get_playlist",
		"list_crates",
		"get_crate",
		"list_records",
		"get_record",
		"get_record_changes",
		"get_tape",
		"suggest_sequence",
		"whoami",
		"ping",
	}
	f1bRate = []string{
		"rate_track",
		"log_listen_events",
		"put_device_profile",
	}
	f1bOwnerOnly = []string{
		"get_ladder",
		"get_offline_set",
		"get_system_info",
		"create_voice_note",
		"get_listen_heat",
		"create_duel",
		"submit_duel",
		"next_duel",
		"suggest_duel_pairs",
		"list_duels",
		"get_duel",
		"submit_callback_verdict",
		"pin_pick",
		"dismiss_pick_proposal",
		"decide_taxonomy_proposal",
		"map_sound_term",
		"publish_record",
		"create_muse",
		"update_muse",
		"assign_muse_songs",
		"set_curation_settings",
		"update_songwriting_settings",
		"create_generation_human",
		"approve_batch",
		"reject_batch",
		"get_spend",
		"list_ledger",
		"set_spend_caps",
		"update_provider_state",
		"get_provider_status",
		"get_audit",
		"list_tokens",
		"create_token",
		"revoke_token",
		"list_backups",
		"create_backup",
		"list_connector_grants",
		"set_connector_grant",
		"list_import_runs",
		"get_import_run",
		"get_device_probe",
		"list_device_profiles",
		"create_pairing_code",
		"create_subsonic_client",
		"list_devices",
		"list_subsonic_clients",
		"revoke_device",
		"revoke_subsonic_client",
	}
	// Agent-facing or self-service ops that must stay untagged (C305, D-U1-6).
	f1bNotOwnerOnly = []string{"propose_pick", "revoke_own_device", "revoke_own_token", "update_track", "get_budget", "get_adherence", "rate_track"}
)

func f1bSpecMeta(t *testing.T) map[string]api.Meta {
	t.Helper()
	a := api.New(http.NewServeMux(), api.Deps{Version: "x"})
	b, err := api.OpenAPIJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string    `json:"operationId"`
			XMM         *api.Meta `json:"x-mm"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	out := map[string]api.Meta{}
	for _, item := range spec.Paths {
		for _, op := range item {
			if op.XMM != nil {
				out[op.OperationID] = *op.XMM
			}
		}
	}
	return out
}

func TestF1bTagging(t *testing.T) {
	t.Parallel()
	metas := f1bSpecMeta(t)
	for _, id := range f1bListen {
		if m, ok := metas[id]; !ok || m.Scope != auth.ScopeListen {
			t.Errorf("%s: want scope listen, got %q (registered %v)", id, m.Scope, ok)
		}
	}
	for _, id := range f1bRate {
		if m, ok := metas[id]; !ok || m.Scope != auth.ScopeRate {
			t.Errorf("%s: want scope rate, got %q (registered %v)", id, m.Scope, ok)
		}
	}
	for _, id := range f1bOwnerOnly {
		if m, ok := metas[id]; !ok || !m.OwnerOnly {
			t.Errorf("%s: want owner_only (registered %v)", id, ok)
		}
	}
	for _, id := range f1bNotOwnerOnly {
		if m, ok := metas[id]; !ok || m.OwnerOnly {
			t.Errorf("%s: must not be owner_only (registered %v)", id, ok)
		}
	}
	// Every class validate() requires the tag on carries it, in the serialised spec too.
	for id, m := range metas {
		if (m.HumanOnly || m.Cost == "approve" || m.Scope == auth.ScopeAdmin) && !m.OwnerOnly {
			t.Errorf("%s: human_only, approve or admin without owner_only", id)
		}
	}
}

func TestF1bEditorListenerFailClosed(t *testing.T) {
	t.Parallel()
	editor, listener := humanOf(config.RoleEditor), humanOf(config.RoleListener)
	for id, m := range f1bSpecMeta(t) {
		pe, pl := api.Permitted(editor, m), api.Permitted(listener, m)
		if m.OwnerOnly || m.HumanOnly || m.Cost == "approve" || m.Scope == auth.ScopeAdmin {
			if problemCode(pe) != "OWNER_ONLY" && problemCode(pe) != "HUMAN_ONLY" && pe == nil {
				t.Errorf("%s: editor passes an owner-class op", id)
			}
			if pl == nil {
				t.Errorf("%s: listener passes an owner-class op", id)
			}
		}
		// A Listener holds only listen and rate: it can never reach read, write, generate or admin ops.
		if pl == nil && m.Scope != auth.ScopeListen && m.Scope != auth.ScopeRate {
			t.Errorf("%s: listener passes scope %s", id, m.Scope)
		}
	}
	for _, id := range []string{"search_tracks", "whoami", "ping", "rate_track", "log_listen_events", "put_device_profile"} {
		m := f1bSpecMeta(t)[id]
		if pr := api.Permitted(listener, m); pr != nil {
			t.Errorf("%s: listener refused: %v", id, pr.Code)
		}
	}
	for _, id := range []string{"get_audit", "get_spend", "approve_batch", "get_offline_set", "create_duel", "create_muse", "create_pairing_code"} {
		m := f1bSpecMeta(t)[id]
		if got := problemCode(api.Permitted(editor, m)); got != "OWNER_ONLY" && got != "HUMAN_ONLY" {
			t.Errorf("%s: editor got %q, want OWNER_ONLY", id, got)
		}
	}
	if !slices.Contains(f1bOwnerOnly, "get_audit") {
		t.Fatal("table sanity")
	}
}
