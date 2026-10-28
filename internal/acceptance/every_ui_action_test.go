package acceptance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

type bearerRT struct{ tok string }

func (b bearerRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(req)
}

// mcpWithToken connects a streamable-HTTP MCP client that presents token tok.
func mcpWithToken(t *testing.T, r *servertest.Rig, tok string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "0"}, nil)
	sess, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: r.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{tok}}, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// uiState carries ids between the steps of the scripted agent session.
type uiState struct {
	track, timed, draft, playlist, comment, export, run, duel string
	// M3: a mother with a known recipe, the token of the last estimate, the batch and the job it created
	mother, token, batch, job string
	// M4a: a connector grant (inserted through connectors.Store.Ensure) and the Subsonic client create_subsonic_client made
	grant, subClient string
	// L1: ids of a Song, a Muse and a session, read from list_songs, create_muse and list_sessions
	song, muse, session string
	// the revisions of the settled Song and Session the script updates (read from the rig before the first call)
	songRev, sessionRev int
	// L2: the Record, Crate and tape the script creates; while an L2 stub stands they stay empty and l2ID falls back to a
	// placeholder the stub answers before it looks at
	record, crate, tape string
	recordRev, crateRev int
}

// l2ID is the id an L2 script step uses: the created object's, or a placeholder while the operation that creates it is a stub.
func l2ID(have, placeholder string) string {
	if have != "" {
		return have
	}
	return placeholder
}

// uiScript holds one scripted call per operation the web UI offers an agent.
// The test derives the required set from the registry, so a new operation with
// a UI route and no entry here fails the test by name; there is no exemption list.
var uiScript = map[string]func(s *uiState) map[string]any{
	"ping":                     func(*uiState) map[string]any { return nil },
	"whoami":                   func(*uiState) map[string]any { return nil },
	"get_system_info":          func(*uiState) map[string]any { return nil },
	"get_songwriting_settings": func(*uiState) map[string]any { return nil },
	"get_stats":                func(*uiState) map[string]any { return nil },
	"get_library_facets":       func(*uiState) map[string]any { return nil },
	"list_tags":                func(*uiState) map[string]any { return nil },
	"list_backups":             func(*uiState) map[string]any { return nil },
	"list_device_profiles":     func(*uiState) map[string]any { return nil },
	"get_device_probe":         func(*uiState) map[string]any { return nil },
	"list_tokens":              func(*uiState) map[string]any { return nil },
	"list_playlists":           func(*uiState) map[string]any { return nil },
	"list_export_sources":      func(*uiState) map[string]any { return nil },
	"search_tracks":            func(*uiState) map[string]any { return map[string]any{"limit": 100} },
	"list_drafts":              func(*uiState) map[string]any { return map[string]any{"limit": 100} },
	"list_import_runs":         func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"get_import_run":           func(s *uiState) map[string]any { return map[string]any{"id": s.run} },
	"get_track":                func(s *uiState) map[string]any { return map[string]any{"id": s.track} },
	"get_track_context":        func(s *uiState) map[string]any { return map[string]any{"id": s.track} },
	"get_media_links":          func(s *uiState) map[string]any { return map[string]any{"id": s.track} },
	"get_track_peaks": func(s *uiState) map[string]any {
		return map[string]any{"id": s.track, "level": "overview"}
	},
	"get_draft":     func(s *uiState) map[string]any { return map[string]any{"id": s.draft} },
	"get_playlist":  func(s *uiState) map[string]any { return map[string]any{"id": s.playlist} },
	"get_export":    func(s *uiState) map[string]any { return map[string]any{"id": s.export} },
	"list_comments": func(s *uiState) map[string]any { return map[string]any{"id": s.track, "limit": 10} },
	"set_tags": func(s *uiState) map[string]any {
		return map[string]any{"id": s.track, "add": []string{"ballad"}}
	},
	"rate_track": func(s *uiState) map[string]any {
		return map[string]any{"id": s.track, "stars": 2, "verdict": "scripted"}
	},
	"update_track": func(s *uiState) map[string]any {
		return map[string]any{"id": s.track, "status": "to_relisten"}
	},
	"add_comment": func(s *uiState) map[string]any {
		return map[string]any{"id": s.track, "body": "scripted note", "kind": "note"}
	},
	"update_comment": func(s *uiState) map[string]any {
		return map[string]any{"id": s.comment, "state": "resolved"}
	},
	"log_listen_events": func(s *uiState) map[string]any {
		return map[string]any{"events": []map[string]any{{"id": "01900000-0000-7000-8000-0000000000b1", "track_id": s.track,
			"session_id": "scripted-session", "kind": "start", "client_at": "2026-10-06T11:59:00Z"}}}
	},
	"lint_recipe": func(*uiState) map[string]any {
		return map[string]any{"items": []map[string]any{{"request": map[string]any{"id": "scripted", "style": "pop", "lyrics": "[Verse]\nla"}}}}
	},
	"create_draft": func(*uiState) map[string]any {
		return map[string]any{"request": map[string]any{"id": "scripted_draft", "style": "english, pop", "lyrics": "[Verse]\nla la\n[Chorus]\nla", "cot": "full", "seed": 4}}
	},
	"update_draft": func(s *uiState) map[string]any {
		return map[string]any{"id": s.draft, "revision": 1, "label": "scripted"}
	},
	"upload_batch_jsonl": func(*uiState) map[string]any {
		return map[string]any{"jsonl": `{"id":"scripted_u1","style":"english","lyrics":"[Verse]\nla","cot":"full","seed":1}` + "\n", "collection": "scripted"}
	},
	"create_playlist": func(s *uiState) map[string]any {
		return map[string]any{"name": "Scripted", "track_ids": []string{s.track}}
	},
	"update_playlist": func(s *uiState) map[string]any {
		return map[string]any{"id": s.playlist, "revision": 1, "name": "Scripted day"}
	},
	"export_playlist_zip": func(s *uiState) map[string]any { return map[string]any{"id": s.playlist} },
	"export_jsonl": func(s *uiState) map[string]any {
		return map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{s.track}}}
	},
	"put_device_profile": func(*uiState) map[string]any {
		return map[string]any{"device_id": "scripted-device", "label": "Scripted", "user_agent": "UA/1", "quality": "high", "quality_source": "probe",
			"probe_result": "failed", "flac_can_play": "maybe", "flac_decode_ok": false, "probe_version": 1}
	},
	"create_backup": func(*uiState) map[string]any { return nil },
	"get_audit":     func(*uiState) map[string]any { return nil }, // arguments built by the test

	// M2b (C53): duels, ladder and Ears.
	"create_duel": func(s *uiState) map[string]any {
		return map[string]any{"a_track_id": s.track, "b_track_id": s.timed, "blind": true}
	},
	"next_duel":  func(*uiState) map[string]any { return nil },
	"get_duel":   func(s *uiState) map[string]any { return map[string]any{"id": s.duel} },
	"list_duels": func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"submit_duel": func(s *uiState) map[string]any {
		return map[string]any{"id": s.duel, "verdict": "tie", "note": "scripted", "latency_ms": 1200, "switches": 2}
	},
	"get_ladder":         func(*uiState) map[string]any { return nil },
	"get_track_analysis": func(s *uiState) map[string]any { return map[string]any{"id": s.track} },
	"get_track_spectrogram": func(s *uiState) map[string]any {
		return map[string]any{"id": s.track, "inline": false}
	},
	"compare_tracks": func(s *uiState) map[string]any { return map[string]any{"a": s.track, "b": s.timed} },

	// M3 (C86.1): the spend ops are driven estimate first, then create with the returned token (spendScript).
	"estimate_generation": func(s *uiState) map[string]any {
		return map[string]any{"items": []map[string]any{{"mode": "new_seed", "parent_track_id": s.mother, "n_seeds": 2}}}
	},
	"estimate_variant": func(s *uiState) map[string]any {
		return map[string]any{"id": s.mother, "mode": "new_seed", "n_seeds": 2}
	},
	"list_batches":        func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"get_batch":           func(s *uiState) map[string]any { return map[string]any{"id": s.batch} },
	"list_jobs":           func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"get_job":             func(s *uiState) map[string]any { return map[string]any{"id": s.job} },
	"wait_for_job":        func(s *uiState) map[string]any { return map[string]any{"id": s.job, "timeout_s": 0} },
	"cancel_job":          func(s *uiState) map[string]any { return map[string]any{"id": s.job, "reason": "scripted"} },
	"cancel_batch":        func(s *uiState) map[string]any { return map[string]any{"id": s.batch, "reason": "scripted"} },
	"get_budget":          func(*uiState) map[string]any { return nil },
	"get_spend":           func(*uiState) map[string]any { return nil },
	"list_ledger":         func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"get_provider_status": func(*uiState) map[string]any { return nil },
	"get_lineage":         func(s *uiState) map[string]any { return map[string]any{"id": s.mother} },

	// M4a (C113): the three read ops and the five admin management ops (the scripted agent holds scope admin).
	"get_offline_set":   func(*uiState) map[string]any { return nil },
	"get_download_link": func(s *uiState) map[string]any { return map[string]any{"id": s.track} },
	"get_prompt": func(s *uiState) map[string]any {
		return map[string]any{"name": "critique_track", "track_id": s.track}
	},
	"list_connector_grants": func(*uiState) map[string]any { return nil },
	"set_connector_grant": func(s *uiState) map[string]any {
		return map[string]any{"id": s.grant, "scopes": []string{"read", "write"}}
	},
	"list_subsonic_clients": func(*uiState) map[string]any { return nil },
	"create_subsonic_client": func(*uiState) map[string]any {
		return map[string]any{"name": "Scripted phone"}
	},
	"revoke_subsonic_client": func(s *uiState) map[string]any {
		id := s.subClient
		if id == "" { // create_subsonic_client is still a stub: any id reaches the stub's 501
			id = "00000000-0000-7000-8000-00000000dead"
		}
		return map[string]any{"id": id}
	},
	// L1 (C101.2): the 14 operations with a UI route and an MCP face.
	"list_muses": func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"get_muse":   func(s *uiState) map[string]any { return map[string]any{"id": s.muse} },
	"create_muse": func(*uiState) map[string]any {
		return map[string]any{"kind": "reference", "title": "Synthetic Muse 900", "artist": "Synthetic Artist 900"}
	},
	"update_muse": func(s *uiState) map[string]any {
		return map[string]any{"id": s.muse, "revision": 1, "title": "Synthetic Muse 901"}
	},
	"assign_muse_songs": func(s *uiState) map[string]any {
		return map[string]any{"id": s.muse, "song_ids": []string{s.song}}
	},
	"get_song": func(s *uiState) map[string]any { return map[string]any{"id": s.song} },
	"update_song": func(s *uiState) map[string]any {
		return map[string]any{"id": s.song, "revision": s.songRev, "title": "Scripted song"}
	},
	"list_pick_events": func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"list_sessions":    func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"update_session": func(s *uiState) map[string]any {
		return map[string]any{"id": s.session, "revision": s.sessionRev, "name": "Scripted session"}
	},
	"search_sounds":      func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"get_sound_taxonomy": func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"map_sound_term": func(*uiState) map[string]any {
		return map[string]any{"mappings": []map[string]any{{"term": "dub", "facet": "genre_family", "value": "reggae", "op": "add"}}}
	},
	"get_muse_board": func(*uiState) map[string]any { return map[string]any{"limit": 10} },

	// L2 (C129): the 15 operations with a UI route and an MCP face (publish_record and set_curation_settings are human-only).
	"list_records": func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"create_record": func(s *uiState) map[string]any {
		return map[string]any{"title": "Synthetic Record 001", "song_ids": []string{s.song}}
	},
	"get_record": func(s *uiState) map[string]any { return map[string]any{"id": l2ID(s.record, "rec-stub")} },
	"update_record": func(s *uiState) map[string]any {
		return map[string]any{"id": l2ID(s.record, "rec-stub"), "revision": max(s.recordRev, 1), "subtitle": "scripted"}
	},
	"edit_record_slots": func(s *uiState) map[string]any {
		return map[string]any{"id": l2ID(s.record, "rec-stub"), "revision": max(s.recordRev, 1), "ops": []map[string]any{{"op": "pin_all"}}}
	},
	"get_record_changes": func(s *uiState) map[string]any { return map[string]any{"id": l2ID(s.record, "rec-stub"), "limit": 10} },
	"propose_record": func(s *uiState) map[string]any {
		return map[string]any{"id": l2ID(s.record, "rec-stub"), "revision": max(s.recordRev, 1), "rationale": "scripted"}
	},
	"export_record_tape": func(s *uiState) map[string]any { return map[string]any{"id": l2ID(s.record, "rec-stub")} },
	"get_tape":           func(s *uiState) map[string]any { return map[string]any{"id": l2ID(s.tape, "tape-stub")} },
	"list_crates":        func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"create_crate":       func(*uiState) map[string]any { return map[string]any{"name": "Synthetic Crate 001", "kind": "manual"} },
	"get_crate":          func(s *uiState) map[string]any { return map[string]any{"id": l2ID(s.crate, "crate-stub")} },
	"update_crate": func(s *uiState) map[string]any {
		return map[string]any{"id": l2ID(s.crate, "crate-stub"), "revision": max(s.crateRev, 1), "description": "scripted"}
	},
	"edit_crate_items": func(s *uiState) map[string]any {
		return map[string]any{"id": l2ID(s.crate, "crate-stub"), "add": []map[string]any{{"song_id": s.song}}}
	},
	"get_curation_settings": func(*uiState) map[string]any { return nil },
}

// spendScript holds the spend operations that have no UI route of their own (the SPA confirms through
// create_generation_human): each is called right after the estimate whose token it spends.
var spendScript = map[string]func(s *uiState) map[string]any{
	// L1: list_songs has no UI route (the Sound browser lists Songs through search_sounds); the script calls it for parity with the Sound browser.
	"list_songs": func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"create_variant": func(s *uiState) map[string]any {
		return map[string]any{"id": s.mother, "confirmation_token": s.token, "rationale": "scripted"}
	},
}

// scriptArgs returns the scripted arguments of an operation, from uiScript or spendScript.
func scriptArgs(id string, s *uiState) map[string]any {
	if f, ok := uiScript[id]; ok {
		return f(s)
	}
	return spendScript[id](s)
}

// uiOrder is the order of the scripted session: reads of the archive first, then writes, then reads of
// the results. get_audit is not listed: the test appends it.
var uiOrder = []string{
	"ping", "whoami", "get_system_info", "get_songwriting_settings", "get_stats", "get_library_facets", "list_tags", "search_tracks",
	"get_track", "get_track_context", "get_media_links", "get_track_peaks",
	"set_tags", "rate_track", "update_track", "add_comment", "list_comments", "update_comment", "log_listen_events",
	"list_drafts", "get_draft", "lint_recipe", "create_draft", "update_draft", "upload_batch_jsonl",
	"create_playlist", "list_playlists", "get_playlist", "update_playlist", "export_playlist_zip", "get_export",
	"list_export_sources", "export_jsonl", "list_import_runs", "get_import_run",
	"list_device_profiles", "put_device_profile", "get_device_probe", "list_tokens", "list_backups", "create_backup",
	"create_duel", "next_duel", "get_duel", "list_duels", "submit_duel", "get_ladder",
	"get_track_analysis", "get_track_spectrogram", "compare_tracks",
	// M3: estimate first, then create with the returned token, then read and cancel what it made
	"estimate_variant", "create_variant", "estimate_generation", "list_batches", "get_batch", "list_jobs", "get_job", "wait_for_job",
	"cancel_job", "cancel_batch", "get_budget", "get_spend", "list_ledger", "get_provider_status", "get_lineage",
	// M4a
	"get_offline_set", "get_download_link", "get_prompt", "list_connector_grants", "set_connector_grant",
	"list_subsonic_clients", "create_subsonic_client", "revoke_subsonic_client",
	// L1: ids first (list_songs, create_muse, list_sessions), then the reads and writes that use them
	"list_songs", "create_muse", "list_muses", "get_muse", "update_muse", "list_sessions", "update_session", "get_song", "update_song",
	"assign_muse_songs", "list_pick_events", "search_sounds", "get_sound_taxonomy", "map_sound_term", "get_muse_board",
	// L2: create first (a Record from the Song, a Crate), then read and edit what they made; the tape comes last
	"create_record", "list_records", "get_record", "update_record", "edit_record_slots", "get_record_changes", "propose_record",
	"create_crate", "list_crates", "get_crate", "update_crate", "edit_crate_items", "get_curation_settings", "export_record_tape", "get_tape",
}

// afterStep records the ids later steps need.
func (s *uiState) afterStep(op string, res any) {
	m, _ := res.(map[string]any)
	switch op {
	case "add_comment":
		s.comment, _ = m["id"].(string)
	case "create_playlist":
		s.playlist, _ = m["id"].(string)
	case "export_playlist_zip":
		s.export, _ = m["id"].(string)
	case "create_draft":
		s.draft, _ = m["id"].(string)
	case "estimate_variant", "estimate_generation":
		s.token, _ = m["confirmation_token"].(string)
	case "create_variant":
		s.batch, _ = m["id"].(string)
		if jobs, _ := m["jobs"].([]any); len(jobs) > 0 {
			s.job, _ = jobs[0].(map[string]any)["id"].(string)
		}
	case "create_subsonic_client":
		if c, _ := m["client"].(map[string]any); c != nil {
			s.subClient, _ = c["id"].(string)
		}
	case "create_muse":
		if id, _ := m["id"].(string); id != "" {
			s.muse = id
		}
	case "create_duel":
		if id, _ := m["id"].(string); id != "" {
			s.duel = id
		}
	case "create_record":
		if rec, _ := m["record"].(map[string]any); rec != nil {
			s.record, _ = rec["id"].(string)
			if rv, ok := rec["revision"].(float64); ok {
				s.recordRev = int(rv)
			}
		}
	case "create_crate":
		if c, _ := m["crate"].(map[string]any); c != nil {
			s.crate, _ = c["id"].(string)
			if rv, ok := c["revision"].(float64); ok {
				s.crateRev = int(rv)
			}
		}
	case "export_record_tape":
		if id, _ := m["id"].(string); id != "" {
			s.tape = id
		}
	case "list_import_runs":
		if items, _ := m["items"].([]any); len(items) > 0 {
			s.run, _ = items[0].(map[string]any)["id"].(string)
		}
	}
}

// TestAcceptance_M2a_AgentPerformsEveryUIAction: an agent with a read+write PAT
// does, over streamable HTTP MCP, everything the web UI does.
func TestAcceptance_M2a_AgentPerformsEveryUIAction(t *testing.T) {
	r := archiveRig(t, servertest.Options{Backend: "fake", M2b: servertest.M2bOptions{Media: true, Backfill: true}})
	var required []api.OpInfo
	for _, op := range api.Operations(r.API) {
		m := op.Meta
		if m.HumanOnly || strings.HasPrefix(m.MCP, "exclude:") || !strings.HasPrefix(m.UI, "/") {
			continue
		}
		required = append(required, op)
	}
	if len(required) < 30 {
		t.Fatalf("derived only %d operations from the registry", len(required))
	}
	have := map[string]api.OpInfo{}
	for _, op := range required {
		have[op.ID] = op
		if _, ok := uiScript[op.ID]; !ok {
			t.Errorf("operation %s (ui %s) has no scripted agent call", op.ID, op.Meta.UI)
		}
	}
	for id := range uiScript {
		if _, ok := have[id]; !ok {
			t.Errorf("scripted call %s is not in the derived UI operation set", id)
		}
	}
	if t.Failed() {
		return
	}

	tok, agentID, err := r.Services.Tokens.Create(context.Background(), "ui-agent",
		[]auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	sess := mcpWithToken(t, r, tok)
	r.Settle(t) // the library structure and derived rows the L1 operations read (the rig never starts the Reconciler)
	st := &uiState{duel: "01900000-0000-7000-8000-000000000b01"}
	g, _, err := r.Services.Connectors.Ensure(context.Background(), "scripted-client", "scripted-subject", "scripted", "Scripted Connector")
	if err != nil {
		t.Fatal(err)
	}
	st.grant = g.ID
	h := r.Human(t)
	st.track, st.timed = track(t, h, false), track(t, h, true)
	if err := r.Store.R.QueryRow(`SELECT t.id FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id WHERE rc.engine <> 'unknown' AND t.lossy = 0 ORDER BY t.id LIMIT 1`).Scan(&st.mother); err != nil {
		t.Fatal(err)
	}
	if err := r.Store.R.QueryRow(`SELECT id, revision FROM songs WHERE retired_at IS NULL ORDER BY id LIMIT 1`).Scan(&st.song, &st.songRev); err != nil {
		t.Fatal(err)
	}
	if err := r.Store.R.QueryRow(`SELECT id, revision FROM sessions ORDER BY id LIMIT 1`).Scan(&st.session, &st.sessionRev); err != nil {
		t.Fatal(err)
	}
	st.draft = obj(obj(h.OK(t, "GET", "/api/drafts?limit=1", nil))["items"].([]any)[0])["id"].(string)

	for _, op := range required {
		if op.ID != "get_audit" && !slices.Contains(uiOrder, op.ID) {
			t.Fatalf("the call order lacks %s", op.ID)
		}
	}
	var called []string
	l2Stubbed := map[string]string{}
	for _, id := range uiOrder {
		op, ok := have[id]
		script := uiScript[id]
		if !ok { // a spend op without a UI route
			for _, o := range api.Operations(r.API) {
				if o.ID == id {
					op = o
				}
			}
			script = spendScript[id]
		}
		args := script(st)
		generic := op.Meta.MCP != "tool"
		raw := rawTool(t, sess, id, args, generic)
		if task := m4aStubOf(raw); raw.IsError && task != "" {
			t.Fatalf("scripted call %s answers 501 NOT_IMPLEMENTED (stub of %s): M4a-B3 left no pending stub", id, task)
		}
		if task := l2StubOf(raw); task != "" { // an L2 lane has not replaced this stub yet: skip by name (INT-B turns the skip into a failure)
			l2Stubbed[id] = task
			continue
		}
		if raw.IsError {
			t.Fatalf("%s: tool error: %+v", id, raw.Content)
		}
		st.afterStep(id, decodeStructured(t, raw))
		called = append(called, id)
	}

	// The audit lists every call, in order, as an mcp transport call of this agent.
	var rows []map[string]any
	cursor := ""
	for {
		a := map[string]any{"principal_id": "agent:" + agentID, "limit": 200}
		if cursor != "" {
			a["cursor"] = cursor
		}
		page := obj(callTool(t, sess, "get_audit", a, true))
		for _, it := range page["items"].([]any) {
			rows = append(rows, it.(map[string]any))
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	slices.Reverse(rows) // newest first => call order
	var gotOps []string
	for _, row := range rows {
		if op, _ := row["op"].(string); l2Stubbed[op] != "" && row["status"] == float64(501) {
			continue // the audited answer of a skipped L2 stub
		}
		if row["transport"] != "mcp" {
			t.Errorf("audit row %v has transport %v, want mcp", row["op"], row["transport"])
		}
		if s, _ := row["status"].(float64); int(s)/100 != 2 {
			t.Errorf("audit row %v has status %v", row["op"], row["status"])
		}
		gotOps = append(gotOps, row["op"].(string))
	}
	want := append(slices.Clone(called), "get_audit")
	if len(gotOps) == len(want)-1 {
		want = called // the audit row of the listing itself is written after the listing
	}
	// Spend and cancel operations write their audit row inside their own transaction (C80), with an id
	// drawn there, so under the rig's fixed clock they sort apart from the gate-written rows. Order is
	// compared without them; their presence is compared as a set.
	inTx := map[string]bool{"create_variant": true, "cancel_job": true, "cancel_batch": true}
	split := func(ops []string) (rest, tx []string) {
		for _, o := range ops {
			if inTx[o] {
				tx = append(tx, o)
			} else {
				rest = append(rest, o)
			}
		}
		slices.Sort(tx)
		return rest, tx
	}
	wantRest, wantTx := split(want)
	gotRest, gotTx := split(gotOps)
	if d := cmp.Diff(wantRest, gotRest); d != "" {
		t.Errorf("audit trail differs from the call order (-called +audited):\n%s", d)
	}
	if d := cmp.Diff(wantTx, gotTx); d != "" {
		t.Errorf("in-transaction audit rows differ (-called +audited):\n%s", d)
	}
}

// l2StubOf returns the task of an L2 stub's tool error (the problem 501 NOT_IMPLEMENTED with details.task "L2-..."), else "".
func l2StubOf(res *mcp.CallToolResult) string {
	if res == nil || !res.IsError || len(res.Content) == 0 {
		return ""
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return ""
	}
	var p struct {
		Code    string `json:"code"`
		Details struct {
			Task string `json:"task"`
		} `json:"details"`
	}
	if json.Unmarshal([]byte(tc.Text), &p) != nil || p.Code != "NOT_IMPLEMENTED" || !strings.HasPrefix(p.Details.Task, "L2-") {
		return ""
	}
	return p.Details.Task
}

// m4aStubOf returns the task of an M4a stub's tool error (the problem 501 NOT_IMPLEMENTED with details.task "M4a-..."), else "".
func m4aStubOf(res *mcp.CallToolResult) string {
	if res == nil || !res.IsError || len(res.Content) == 0 {
		return ""
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return ""
	}
	var p struct {
		Code    string `json:"code"`
		Details struct {
			Task string `json:"task"`
		} `json:"details"`
	}
	if json.Unmarshal([]byte(tc.Text), &p) != nil || p.Code != "NOT_IMPLEMENTED" || !strings.HasPrefix(p.Details.Task, "M4a-") {
		return ""
	}
	return p.Details.Task
}
