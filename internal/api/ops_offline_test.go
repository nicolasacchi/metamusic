package api_test

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func offlineRig(t *testing.T) *servertest.Rig {
	t.Helper()
	return servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, M4a: true, Files: true})
}

func offlineIDs(t *testing.T, set map[string]any) []string {
	t.Helper()
	var out []string
	for _, it := range set["items"].([]any) {
		out = append(out, obj(t, it)["track_id"].(string))
	}
	return out
}

// TestOfflineSet_AgentEdits: a PAT agent reads the set, edits it with update_playlist and reads it again; the
// same read over MCP call_operation equals the REST body (C111).
func TestOfflineSet_AgentEdits(t *testing.T) {
	t.Parallel()
	r := offlineRig(t)
	agent, _ := r.Agent(t, rw...)

	set := obj(t, agent.OK(t, "GET", "/api/offline-set", nil))
	if set["playlist_id"] != lt.OfflineSetID || set["limit_items"] != 100.0 || !cmp.Equal(offlineIDs(t, set), []string{lt.TrackLuna, lt.TrackCitta}) {
		t.Fatalf("set = %v", set)
	}
	rev := set["revision"].(float64)
	resp := agent.Do(t, "PATCH", "/api/playlists/"+lt.OfflineSetID, map[string]any{"revision": rev, "add_track_ids": []string{lt.TrackShort}})
	if resp.Status != 200 {
		t.Fatalf("update: %d %s", resp.Status, resp.Body)
	}
	after := obj(t, agent.OK(t, "GET", "/api/offline-set", nil))
	if !cmp.Equal(offlineIDs(t, after), []string{lt.TrackLuna, lt.TrackCitta, lt.TrackShort}) || after["revision"] != rev+1 {
		t.Fatalf("after = %v", after)
	}
	items := after["items"].([]any)
	if p := obj(t, items[2])["position"]; p != 3.0 {
		t.Errorf("position = %v", p)
	}

	want := servertest.Normalize(after)
	for name, sess := range map[string]*mcp.ClientSession{"mcp": r.MCP(t, rw...), "stdio": r.Stdio(t, rw...)} {
		res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "call_operation", Arguments: map[string]any{"operation_id": "get_offline_set", "arguments": map[string]any{}}})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		b, _ := json.Marshal(res.StructuredContent)
		var got any
		_ = json.Unmarshal(b, &got)
		if diff := cmp.Diff(want, servertest.Normalize(got)); diff != "" {
			t.Errorf("%s differs (-rest +%s):\n%s", name, name, diff)
		}
	}
}

// TestOfflineSet_TotalsAndMP3Null: a track without an MP3 rendition has mp3 null; a lossy reference track has flac null,
// lossy true and its master as mp3, counted in mp3_bytes (C111).
func TestOfflineSet_TotalsAndMP3Null(t *testing.T) {
	t.Parallel()
	r := offlineRig(t)
	human := r.Human(t)
	// Luna loses its MP3 rendition (an UPDATE of the derived row is allowed; DELETE is not)
	if _, err := r.Store.W.ExecContext(t.Context(), `UPDATE renditions SET kind = 'mp3_gone' WHERE track_id = ? AND kind = 'mp3_320'`, lt.TrackLuna); err != nil {
		t.Fatal(err)
	}
	rev := obj(t, human.OK(t, "GET", "/api/offline-set", nil))["revision"]
	if resp := human.Do(t, "PATCH", "/api/playlists/"+lt.OfflineSetID, map[string]any{"revision": rev, "track_ids": []string{lt.TrackLuna, lt.TrackRefFlac, lt.TrackLossy}}); resp.Status != 200 {
		t.Fatalf("update: %d %s", resp.Status, resp.Body)
	}
	set := obj(t, human.OK(t, "GET", "/api/offline-set", nil))
	items := set["items"].([]any)
	luna, ref, lossy := obj(t, items[0]), obj(t, items[1]), obj(t, items[2])
	if luna["mp3"] != nil || obj(t, luna["flac"])["bytes"].(float64) <= 0 || luna["lossy"] != false || luna["title"] != "Luna Park" {
		t.Errorf("luna = %v", luna)
	}
	if ref["mp3"] == nil || ref["flac"] == nil || ref["lossy"] != false {
		t.Errorf("ref = %v", ref)
	}
	if lossy["flac"] != nil || lossy["lossy"] != true || lossy["mp3"] == nil {
		t.Errorf("lossy = %v", lossy)
	}
	flac := obj(t, luna["flac"])
	if flac["sha256"] == "" || flac["sample_rate"].(float64) <= 0 || flac["bits_per_sample"].(float64) <= 0 {
		t.Errorf("flac = %v", flac)
	}
	totals := obj(t, set["totals"])
	wantFlac := obj(t, luna["flac"])["bytes"].(float64) + obj(t, ref["flac"])["bytes"].(float64)
	wantMP3 := obj(t, ref["mp3"])["bytes"].(float64) + obj(t, lossy["mp3"])["bytes"].(float64)
	if totals["items"] != 3.0 || totals["flac_bytes"] != wantFlac || totals["mp3_bytes"] != wantMP3 {
		t.Errorf("totals = %v, want flac %v mp3 %v", totals, wantFlac, wantMP3)
	}
}

// TestOfflineSet_LimitsREST: the 422s of C111 reach the client with their details.
func TestOfflineSet_LimitsREST(t *testing.T) {
	t.Parallel()
	r := offlineRig(t)
	human := r.Human(t)
	rev := obj(t, human.OK(t, "GET", "/api/offline-set", nil))["revision"]
	big := make([]string, 101)
	for i := range big {
		big[i] = lt.TrackLuna
	}
	resp := human.Do(t, "PATCH", "/api/playlists/"+lt.OfflineSetID, map[string]any{"revision": rev, "track_ids": big})
	p := obj(t, resp.JSON(t))
	if resp.Status != 422 || p["code"] != "OFFLINE_SET_TOO_LARGE" || obj(t, p["details"])["limit"] != 100.0 || obj(t, p["details"])["items"] != 101.0 {
		t.Errorf("too large: %d %s", resp.Status, resp.Body)
	}
	resp = human.Do(t, "PATCH", "/api/playlists/"+lt.OfflineSetID, map[string]any{"revision": rev, "hidden": true})
	p = obj(t, resp.JSON(t))
	if resp.Status != 422 || p["code"] != "VALIDATION_FAILED" || obj(t, p["details"])["reason"] != "offline_set_cannot_be_hidden" {
		t.Errorf("hide: %d %s", resp.Status, resp.Body)
	}
}

// TestPlaylists_RoleField: list_playlists shows role, offline for the protected set and null otherwise.
func TestPlaylists_RoleField(t *testing.T) {
	t.Parallel()
	r := offlineRig(t)
	agent, _ := r.Agent(t, rw...)
	list := obj(t, agent.OK(t, "GET", "/api/playlists", nil))["items"].([]any)
	seen := map[string]any{}
	for _, it := range list {
		m := obj(t, it)
		role, present := m["role"]
		if !present {
			t.Errorf("playlist %v has no role key", m["id"])
		}
		seen[m["id"].(string)] = role
	}
	if seen[lt.OfflineSetID] != "offline" || seen[lt.PlaylistMix] != nil || len(seen) != 2 {
		t.Errorf("roles = %v", seen)
	}
	pl := obj(t, agent.OK(t, "GET", "/api/playlists/"+lt.OfflineSetID, nil))
	if pl["role"] != "offline" {
		t.Errorf("get_playlist role = %v", pl["role"])
	}
}
