package acceptance_test

import (
	"context"
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
	track, timed, draft, playlist, comment, export, run string
}

// uiScript holds one scripted call per operation the web UI offers an agent.
// The test derives the required set from the registry, so a new operation with
// a UI route and no entry here fails the test by name; there is no exemption list.
var uiScript = map[string]func(s *uiState) map[string]any{
	"ping":                 func(*uiState) map[string]any { return nil },
	"whoami":               func(*uiState) map[string]any { return nil },
	"get_system_info":      func(*uiState) map[string]any { return nil },
	"get_stats":            func(*uiState) map[string]any { return nil },
	"get_library_facets":   func(*uiState) map[string]any { return nil },
	"list_tags":            func(*uiState) map[string]any { return nil },
	"list_backups":         func(*uiState) map[string]any { return nil },
	"list_device_profiles": func(*uiState) map[string]any { return nil },
	"get_device_probe":     func(*uiState) map[string]any { return nil },
	"list_tokens":          func(*uiState) map[string]any { return nil },
	"list_playlists":       func(*uiState) map[string]any { return nil },
	"list_export_sources":  func(*uiState) map[string]any { return nil },
	"search_tracks":        func(*uiState) map[string]any { return map[string]any{"limit": 100} },
	"list_drafts":          func(*uiState) map[string]any { return map[string]any{"limit": 100} },
	"list_import_runs":     func(*uiState) map[string]any { return map[string]any{"limit": 10} },
	"get_import_run":       func(s *uiState) map[string]any { return map[string]any{"id": s.run} },
	"get_track":            func(s *uiState) map[string]any { return map[string]any{"id": s.track} },
	"get_track_context":    func(s *uiState) map[string]any { return map[string]any{"id": s.track} },
	"get_media_links":      func(s *uiState) map[string]any { return map[string]any{"id": s.track} },
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
}

// uiOrder is the order of the scripted session: reads of the archive first, then writes, then reads of
// the results. get_audit is not listed: the test appends it.
var uiOrder = []string{
	"ping", "whoami", "get_system_info", "get_stats", "get_library_facets", "list_tags", "search_tracks",
	"get_track", "get_track_context", "get_media_links", "get_track_peaks",
	"set_tags", "rate_track", "update_track", "add_comment", "list_comments", "update_comment", "log_listen_events",
	"list_drafts", "get_draft", "lint_recipe", "create_draft", "update_draft", "upload_batch_jsonl",
	"create_playlist", "list_playlists", "get_playlist", "update_playlist", "export_playlist_zip", "get_export",
	"list_export_sources", "export_jsonl", "list_import_runs", "get_import_run",
	"list_device_profiles", "put_device_profile", "get_device_probe", "list_tokens", "list_backups", "create_backup",
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
	case "list_import_runs":
		if items, _ := m["items"].([]any); len(items) > 0 {
			s.run, _ = items[0].(map[string]any)["id"].(string)
		}
	}
}

// TestAcceptance_M2a_AgentPerformsEveryUIAction: an agent with a read+write PAT
// does, over streamable HTTP MCP, everything the web UI does.
func TestAcceptance_M2a_AgentPerformsEveryUIAction(t *testing.T) {
	r := archiveRig(t, servertest.Options{})
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
		[]auth.Scope{auth.ScopeRead, auth.ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	sess := mcpWithToken(t, r, tok)
	st := &uiState{}
	h := r.Human(t)
	st.track, st.timed = track(t, h, false), track(t, h, true)
	st.draft = obj(obj(h.OK(t, "GET", "/api/drafts?limit=1", nil))["items"].([]any)[0])["id"].(string)

	for _, op := range required {
		if op.ID != "get_audit" && !slices.Contains(uiOrder, op.ID) {
			t.Fatalf("the call order lacks %s", op.ID)
		}
	}
	var called []string
	for _, id := range uiOrder {
		op := have[id]
		args := uiScript[id](st)
		generic := op.Meta.MCP != "tool"
		res := callTool(t, sess, id, args, generic)
		st.afterStep(id, res)
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
	if d := cmp.Diff(want, gotOps); d != "" {
		t.Errorf("audit trail differs from the call order (-called +audited):\n%s", d)
	}
}
