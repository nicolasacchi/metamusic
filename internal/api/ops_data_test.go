package api_test

import (
	"encoding/json"

	"archive/zip"
	"bytes"
	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func code(t *testing.T, r *servertest.Response) string {
	t.Helper()
	return obj(t, r.JSON(t))["code"].(string)
}

// fetched is what a test needs of a media response.
type fetched struct {
	StatusCode int
	Header     http.Header
}

func fetch(t *testing.T, r *servertest.Rig, link string, hdr ...string) (fetched, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", r.URL+link, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return fetched{resp.StatusCode, resp.Header}, b
}

func TestPlaylists_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, rw...)

	c := human.Do(t, "POST", "/api/playlists", map[string]any{"name": "Night", "track_ids": []string{lt.TrackCitta, lt.TrackLuna}}, "Idempotency-Key", "p1")
	if c.Status != 201 {
		t.Fatalf("create: %d %s", c.Status, c.Body)
	}
	pl := obj(t, c.JSON(t))
	id := pl["id"].(string)
	if pl["revision"] != 1.0 || len(pl["items"].([]any)) != 2 || !strings.HasPrefix(pl["created_by"].(string), "human:") {
		t.Fatalf("playlist %v", pl)
	}
	if again := human.Do(t, "POST", "/api/playlists", map[string]any{"name": "Night", "track_ids": []string{lt.TrackCitta, lt.TrackLuna}}, "Idempotency-Key", "p1"); again.Status != 201 || obj(t, again.JSON(t))["id"] != id {
		t.Errorf("replay: %d %s", again.Status, again.Body)
	}
	if c := human.Do(t, "POST", "/api/playlists", map[string]any{"name": "x", "track_ids": []string{"nope"}}, "Idempotency-Key", "p2"); c.Status != 422 || code(t, c) != "UNKNOWN_TRACK" {
		t.Errorf("unknown track: %d %s", c.Status, c.Body)
	}

	// the agent sees the operator as "human" (D-M2a-19) and may extend the playlist
	g := agent.Do(t, "GET", "/api/playlists/"+id, nil)
	if g.Status != 200 || obj(t, g.JSON(t))["created_by"] != "human" {
		t.Errorf("agent view: %d %s", g.Status, g.Body)
	}
	u := agent.Do(t, "PATCH", "/api/playlists/"+id, map[string]any{"revision": 1, "add_track_ids": []string{lt.TrackTwinA}})
	if u.Status != 200 || obj(t, u.JSON(t))["revision"] != 2.0 || !strings.HasPrefix(obj(t, u.JSON(t))["updated_by"].(string), "agent:") {
		t.Fatalf("agent update: %d %s", u.Status, u.Body)
	}
	if s := human.Do(t, "PATCH", "/api/playlists/"+id, map[string]any{"revision": 1, "name": "stale"}); s.Status != 409 || code(t, s) != "REVISION_CONFLICT" {
		t.Errorf("stale: %d %s", s.Status, s.Body)
	}
	if s := human.Do(t, "PATCH", "/api/playlists/"+id, map[string]any{"revision": 2, "track_ids": []string{lt.TrackLuna}, "add_track_ids": []string{lt.TrackCitta}}); s.Status != 422 {
		t.Errorf("exclusive: %d %s", s.Status, s.Body)
	}
	if s := human.Do(t, "GET", "/api/playlists/missing", nil); s.Status != 404 {
		t.Errorf("missing: %d", s.Status)
	}
	hide := human.Do(t, "PATCH", "/api/playlists/"+id, map[string]any{"revision": 2, "hidden": true})
	if hide.Status != 200 {
		t.Fatalf("hide: %d %s", hide.Status, hide.Body)
	}
	ids := func(q string) []string {
		var out []string
		for _, it := range obj(t, agent.Do(t, "GET", "/api/playlists"+q, nil).JSON(t))["items"].([]any) {
			out = append(out, obj(t, it)["id"].(string))
		}
		return out
	}
	const offlineSet = "00000000-0000-7000-8000-0000000000f1" // created by the M4a migration
	if got := ids(""); !slices.Equal(got, []string{lt.PlaylistMix, offlineSet}) {
		t.Errorf("default list %v", got)
	}
	if got := ids("?include_hidden=true"); len(got) != 3 {
		t.Errorf("include_hidden list %v", got)
	}
}

func TestExports_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	agent, _ := r.Agent(t, rw...)

	src := obj(t, agent.Do(t, "GET", "/api/exports/sources", nil).JSON(t))["items"].([]any)
	if len(src) != 3 {
		t.Fatalf("sources %v", src)
	}
	body := map[string]any{"selection": "source", "source_path": "music/songs.jsonl"}
	c := agent.Do(t, "POST", "/api/exports/jsonl", body, "Idempotency-Key", "e1")
	if c.Status != 201 {
		t.Fatalf("export: %d %s", c.Status, c.Body)
	}
	x := obj(t, c.JSON(t))
	jl := obj(t, x["jsonl"])
	if x["kind"] != "songs_jsonl" || jl["rows"] != 5.0 || jl["raw_rows"] != 5.0 || jl["content"] == nil {
		t.Fatalf("export %v", x)
	}
	// D-M2a-20: a source export is the original file and reports its original issue (two seeds of fx-twin)
	if v := obj(t, jl["validation"]); v["ok"] != false || len(v["issues"].([]any)) != 1 || obj(t, v["issues"].([]any)[0])["code"] != "duplicate_id" {
		t.Errorf("validation %v", v)
	}
	link := obj(t, x["download"])["url"].(string)
	resp, got := fetch(t, r, link)
	if resp.StatusCode != 200 || string(got) != jl["content"] || resp.Header.Get("Content-Type") != "application/x-ndjson" ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="songs.jsonl"` {
		t.Fatalf("download: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := fetch(t, r, "/media/"+x["sha256"].(string)+".jsonl"); resp.StatusCode != 403 {
		t.Errorf("unsigned download: %d", resp.StatusCode)
	}
	if replay := agent.Do(t, "POST", "/api/exports/jsonl", body, "Idempotency-Key", "e1"); replay.Status != 201 || obj(t, replay.JSON(t))["id"] != x["id"] {
		t.Errorf("replay: %d %s", replay.Status, replay.Body)
	}
	g := agent.Do(t, "GET", "/api/exports/"+x["id"].(string), nil)
	if g.Status != 200 || obj(t, g.JSON(t))["sha256"] != x["sha256"] {
		t.Errorf("get_export: %d %s", g.Status, g.Body)
	}

	for name, tc := range map[string]struct {
		body   map[string]any
		status int
		code   string
	}{
		"empty selection":   {map[string]any{"selection": "tracks", "tracks": map[string]any{"status": []string{"discarded"}}}, 422, "EXPORT_EMPTY"},
		"unknown source":    {map[string]any{"selection": "source", "source_path": "nope.jsonl"}, 422, "UNKNOWN_SOURCE"},
		"source needs path": {map[string]any{"selection": "source"}, 422, "VALIDATION_FAILED"},
		"mixed selection":   {map[string]any{"selection": "tracks", "source_path": "x"}, 422, "VALIDATION_FAILED"},
	} {
		if resp := agent.Do(t, "POST", "/api/exports/jsonl", tc.body, "Idempotency-Key", "k-"+strings.ReplaceAll(name, " ", "-")); resp.Status != tc.status || code(t, resp) != tc.code {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
	}
	if resp := agent.Do(t, "GET", "/api/exports/nope", nil); resp.Status != 404 {
		t.Errorf("unknown export: %d", resp.Status)
	}

	// tracks: unique ids, validator clean
	tr := agent.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{lt.TrackTwinA, lt.TrackTwinB}}}, "Idempotency-Key", "e2")
	tj := obj(t, obj(t, tr.JSON(t))["jsonl"])
	if tr.Status != 201 || obj(t, tj["validation"])["ok"] != true || tj["rows"] != 2.0 || len(tj["renamed"].([]any)) != 2 {
		t.Errorf("tracks export: %d %s", tr.Status, tr.Body)
	}
}

func TestExports_PlaylistZIP_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	c := human.Do(t, "POST", "/api/playlists/"+lt.PlaylistMix+"/exports", nil, "Idempotency-Key", "z1")
	if c.Status != 201 {
		t.Fatalf("zip export: %d %s", c.Status, c.Body)
	}
	x := obj(t, c.JSON(t))
	if x["kind"] != "playlist_zip" || obj(t, x["zip"])["entries"] != 3.0 {
		t.Fatalf("%v", x)
	}
	link := obj(t, x["download"])["url"].(string)
	resp, all := fetch(t, r, link)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" || resp.Header.Get("Content-Disposition") != `attachment; filename="fixture_mix.zip"` {
		t.Fatalf("download: %d %v", resp.StatusCode, resp.Header)
	}
	zr, err := zip.NewReader(bytes.NewReader(all), int64(len(all)))
	if err != nil || len(zr.File) != 4 {
		t.Fatalf("zip: %v %d", err, len(zr.File))
	}
	resp, part := fetch(t, r, link, "Range", "bytes=0-3")
	if resp.StatusCode != 206 || !bytes.Equal(part, all[:4]) {
		t.Errorf("range: %d % x", resp.StatusCode, part)
	}
	if resp := human.Do(t, "POST", "/api/playlists/nope/exports", nil, "Idempotency-Key", "z2"); resp.Status != 404 {
		t.Errorf("unknown playlist: %d", resp.Status)
	}
}

func TestExports_Expired_REST(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	agent, _ := r.Agent(t, rw...)
	c := agent.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "source", "source_path": "music/songs.jsonl"}, "Idempotency-Key", "e1")
	x := obj(t, c.JSON(t))
	link := obj(t, x["download"])["url"].(string)
	r.Clock.Advance(time.Minute) // the 15 minute link is still valid
	if n, err := r.Services.Exports.Sweep(t.Context(), 30*time.Second); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	if g := agent.Do(t, "GET", "/api/exports/"+x["id"].(string), nil); g.Status != 410 || code(t, g) != "EXPORT_EXPIRED" {
		t.Errorf("get after sweep: %d %s", g.Status, g.Body)
	}
	if resp, _ := fetch(t, r, link); resp.StatusCode != 404 {
		t.Errorf("a valid link to a swept export must answer 404, got %d", resp.StatusCode)
	}
}

// TestExports_GetExportParity: an export made over REST reads back identically over REST, MCP and stdio
// (same sha, same link at the fixed clock).
func TestExports_GetExportParity(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	agent, _ := r.Agent(t, rw...)
	c := agent.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "drafts", "drafts": map[string]any{"origin": "jsonl_upload"}}, "Idempotency-Key", "g1")
	if c.Status != 201 {
		t.Fatalf("%d %s", c.Status, c.Body)
	}
	id := obj(t, c.JSON(t))["id"].(string)
	want := servertest.Normalize(agent.Do(t, "GET", "/api/exports/"+id, nil).JSON(t))
	for name, sess := range map[string]*mcp.ClientSession{"mcp": r.MCP(t, rw...), "stdio": r.Stdio(t, rw...)} {
		res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "call_operation", Arguments: map[string]any{"operation_id": "get_export", "arguments": map[string]any{"id": id}}})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		b, _ := json.Marshal(res.StructuredContent)
		var got any
		_ = json.Unmarshal(b, &got)
		if diff := cmp.Diff(want, servertest.Normalize(got)); diff != "" {
			t.Errorf("%s: get_export differs (-rest +%s):\n%s", name, name, diff)
		}
	}
}
