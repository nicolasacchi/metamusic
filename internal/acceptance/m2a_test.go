package acceptance_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

var rw = []auth.Scope{auth.ScopeRead, auth.ScopeWrite}

// archiveRig starts a rig over the shared synthetic acceptance archive (heavy:
// skipped under -short, like every test that imports audio).
func archiveRig(t *testing.T, o servertest.Options) *servertest.Rig {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	o.Archive = &tpl
	return servertest.New(t, o)
}

// track returns the id of the first archive track with the wanted timed flag.
func track(t *testing.T, c *servertest.Client, timed bool) string {
	t.Helper()
	items := c.OK(t, "GET", "/api/tracks?limit=100", nil).(map[string]any)["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		if m["timed"] == timed {
			return m["id"].(string)
		}
	}
	t.Fatalf("no archive track with timed=%v", timed)
	return ""
}

// callTool calls a tool or, when generic, call_operation, and returns the
// decoded structured content (nil on an empty result); an error result fails.
func callTool(t *testing.T, s *mcp.ClientSession, op string, args map[string]any, generic bool) any {
	t.Helper()
	res := rawTool(t, s, op, args, generic)
	if res.IsError {
		t.Fatalf("%s: tool error: %+v", op, res.Content)
	}
	return decodeStructured(t, res)
}

// decodeStructured returns the structured content of a successful result as plain JSON values.
func decodeStructured(t *testing.T, res *mcp.CallToolResult) any {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

func rawTool(t *testing.T, s *mcp.ClientSession, op string, args map[string]any, generic bool) *mcp.CallToolResult {
	t.Helper()
	name, params := op, any(args)
	if generic {
		name, params = "call_operation", map[string]any{"operation_id": op, "arguments": args}
	}
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: params})
	if err != nil {
		t.Fatalf("%s via %s: %v", op, name, err)
	}
	return res
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }

// id3Frames returns the ID3v2.4 frames at the start of an MP3.
func id3Frames(t *testing.T, b []byte) map[string][][]byte {
	t.Helper()
	if len(b) < 10 || string(b[:3]) != "ID3" {
		t.Fatalf("no ID3 header")
	}
	size := int(b[6])<<21 | int(b[7])<<14 | int(b[8])<<7 | int(b[9])
	body := b[10 : 10+size]
	out := map[string][][]byte{}
	for len(body) >= 10 {
		n := int(body[4])<<21 | int(body[5])<<14 | int(body[6])<<7 | int(body[7])
		out[string(body[:4])] = append(out[string(body[:4])], body[10:10+n])
		body = body[10+n:]
	}
	return out
}

// zipPOPM exports a playlist ZIP as the human, downloads it and returns the POPM
// rating byte of the only entry (0 when it has no POPM frame).
func zipPOPM(t *testing.T, r *servertest.Rig, h *servertest.Client, playlist string, n int) byte {
	t.Helper()
	exp := obj(h.OK(t, "POST", "/api/playlists/"+playlist+"/exports", nil, "Idempotency-Key", fmt.Sprintf("zip-%d", n)))
	link := obj(exp["download"])["url"].(string)
	resp, err := http.Get(r.URL + link) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	zb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("zip download: %d", resp.StatusCode)
	}
	mp3 := readFirstMP3(t, zb)
	popm := id3Frames(t, mp3)["POPM"]
	if len(popm) == 0 {
		return 0
	}
	return popm[0][len(popm[0])-1]
}

// TestAcceptance_M2a_RatingIsolation: whatever an agent does over any face, the
// human rating, the star filters, the facets and the ZIP POPM stay the human's.
func TestAcceptance_M2a_RatingIsolation(t *testing.T) {
	r := archiveRig(t, servertest.Options{})
	h := r.Human(t)
	tr := track(t, h, false)
	h.OK(t, "PUT", "/api/tracks/"+tr+"/rating", map[string]any{"stars": 5})
	pl := obj(h.OK(t, "POST", "/api/playlists", map[string]any{"name": "Iso", "track_ids": []string{tr}}, "Idempotency-Key", "iso-pl"))["id"].(string)

	reader, _ := r.Agent(t, rw...)
	facets := func() any { return obj(reader.OK(t, "GET", "/api/library/facets", nil))["stars"] }
	wantFacets := facets()
	n := 0
	check := func(face string, wantCritic any) {
		t.Helper()
		n++
		if got := obj(obj(obj(reader.OK(t, "GET", "/api/tracks/"+tr, nil))["rating"])["human"])["stars"]; got != 5.0 {
			t.Errorf("%s: get_track rating.human.stars = %v, want 5", face, got)
		}
		found := false
		for _, it := range obj(reader.OK(t, "GET", "/api/tracks?min_stars=5&limit=100", nil))["items"].([]any) {
			found = found || obj(it)["id"] == tr
		}
		if !found {
			t.Errorf("%s: search_tracks min_stars=5 lost the track", face)
		}
		if d := cmp.Diff(wantFacets, facets()); d != "" {
			t.Errorf("%s: star facets changed (-want +got):\n%s", face, d)
		}
		ctx := obj(obj(reader.OK(t, "GET", "/api/tracks/"+tr+"/context", nil))["track"])
		if ctx["stars"] != 5.0 || ctx["critic_stars"] != wantCritic {
			t.Errorf("%s: context stars=%v critic_stars=%v, want 5 and %v", face, ctx["stars"], ctx["critic_stars"], wantCritic)
		}
		if got := zipPOPM(t, r, h, pl, n); got != 255 {
			t.Errorf("%s: ZIP POPM = %d, want 255 (5 stars)", face, got)
		}
	}
	check("baseline", nil)

	rest, _ := r.Agent(t, rw...)
	rest.OK(t, "PUT", "/api/tracks/"+tr+"/rating", map[string]any{"stars": 1, "verdict": "rest"})
	check("rest bearer", 1.0)

	m := r.MCP(t, rw...)
	callTool(t, m, "rate_track", map[string]any{"id": tr, "stars": 2}, false)
	check("mcp tool", 2.0)
	callTool(t, m, "rate_track", map[string]any{"id": tr, "stars": 3}, true)
	check("call_operation", 3.0)

	st := r.Stdio(t, rw...)
	callTool(t, st, "rate_track", map[string]any{"id": tr, "stars": 4}, false)
	check("stdio", 4.0)

	// Clearing the agent's own rating leaves the human row untouched.
	callTool(t, m, "rate_track", map[string]any{"id": tr, "stars": nil}, false)
	check("agent cleared", nil)
}

// TestAcceptance_M2a_TimestampCommentLink: a timed comment carries the SPA deep
// link, the absolute URL and the section label of the timed plan, over REST and MCP.
func TestAcceptance_M2a_TimestampCommentLink(t *testing.T) {
	r := archiveRig(t, servertest.Options{})
	h := r.Human(t)
	tr := track(t, h, true)
	detail := obj(h.OK(t, "GET", "/api/tracks/"+tr, nil))
	// The synthetic timed tracks are 6-12 s long; pick the middle of the last timed section.
	var label string
	var at float64
	for _, s := range detail["sections"].([]any) {
		sm := obj(s)
		if sm["start_s"] != nil && sm["end_s"] != nil {
			label = sm["label"].(string)
			at = (sm["start_s"].(float64) + sm["end_s"].(float64)) / 2
		}
	}
	if label == "" {
		t.Fatal("the timed archive track has no timed section")
	}
	want := fmt.Sprintf("/tracks/%s?t=%.1f", tr, at)
	check := func(face string, c map[string]any) {
		t.Helper()
		if c["link"] != want {
			t.Errorf("%s: link = %v, want %s", face, c["link"], want)
		}
		if c["url"] != "http://mm.example.com"+want {
			t.Errorf("%s: url = %v", face, c["url"])
		}
		if sec := obj(c["section"]); sec == nil || sec["label"] != label || sec["source"] != "plan" {
			t.Errorf("%s: section = %v, want label %s from the plan", face, c["section"], label)
		}
	}
	rest, _ := r.Agent(t, rw...)
	check("rest", obj(rest.OK(t, "POST", "/api/tracks/"+tr+"/comments", map[string]any{"body": "rest note", "at_second": at}, "Idempotency-Key", "tc-1")))
	m := r.MCP(t, rw...)
	check("mcp", obj(callTool(t, m, "add_comment", map[string]any{"id": tr, "body": "mcp note", "at_second": at}, false)))
}

// TestMCP_DerivedIdempotency_AddComment: without a key the MCP dispatcher derives
// one from (caller, operation, arguments) with a 10 minute window.
func TestMCP_DerivedIdempotency_AddComment(t *testing.T) {
	r := archiveRig(t, servertest.Options{})
	h := r.Human(t)
	tr := track(t, h, false)
	m := r.MCP(t, rw...)
	count := func() int {
		return len(obj(callTool(t, m, "list_comments", map[string]any{"id": tr, "limit": 100}, true))["items"].([]any))
	}
	args := map[string]any{"id": tr, "body": "second chorus drags"}
	a := callTool(t, m, "add_comment", args, false)
	b := callTool(t, m, "add_comment", args, false)
	if d := cmp.Diff(a, b); d != "" {
		t.Errorf("a replay must return the identical result (-first +second):\n%s", d)
	}
	if n := count(); n != 1 {
		t.Fatalf("same call twice without a key: %d comments, want 1", n)
	}
	k1 := map[string]any{"id": tr, "body": "second chorus drags", "idempotency_key": "key-one"}
	k2 := map[string]any{"id": tr, "body": "second chorus drags", "idempotency_key": "key-two"}
	callTool(t, m, "add_comment", k1, false)
	callTool(t, m, "add_comment", k2, false)
	if n := count(); n != 3 {
		t.Fatalf("two distinct explicit keys: %d comments, want 3", n)
	}
	r.Clock.Advance(10*time.Minute + time.Second)
	callTool(t, m, "add_comment", args, false)
	if n := count(); n != 4 {
		t.Fatalf("after the derived-key window: %d comments, want 4", n)
	}
}

// containsAny reports which of the strings occurs in s.
func containsAny(s string, needles ...string) string {
	for _, n := range needles {
		if n != "" && strings.Contains(s, n) {
			return n
		}
	}
	return ""
}

// readFirstMP3 returns the first .mp3 entry of a ZIP.
func readFirstMP3(t *testing.T, zb []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, ".mp3") {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rc.Close() }()
			b, _ := io.ReadAll(rc)
			return b
		}
	}
	t.Fatal("no mp3 in the ZIP")
	return nil
}
