package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
	"github.com/nicolasacchi/metamusic/internal/trackctx/trackctxtest"
)

func u16(b []byte) int { return len(utf16.Encode([]rune(string(b)))) }

func TestMeasure_MatchesRESTBody(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	agent, _ := r.Agent(t)
	// a track whose title and comments need HTML escaping and non-BMP characters
	if _, err := r.Store.W.Exec(`UPDATE tracks SET title = ? WHERE id = ?`, "Tom & <Jerry> \U0001F3B5 è", lt.TrackLuna); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{lt.TrackLuna, lt.TrackShort, lt.TrackTwinB} {
		resp := agent.Do(t, "GET", "/api/tracks/"+id+"/context", nil)
		if resp.Status != 200 {
			t.Fatalf("%d %s", resp.Status, resp.Body)
		}
		body := bytes.TrimSuffix(resp.Body, []byte("\n"))
		b, err := r.Services.Deps.Context.Build(context.Background(), auth.Principal{Kind: auth.KindAgent, ID: "x", Name: "x"}, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := trackctx.Measure(&b), u16(body); got != want {
			t.Errorf("%s: Measure %d, REST body %d units", id, got, want)
		}
		if got := obj(t, obj(t, resp.JSON(t))["_budget"])["chars"]; got != float64(u16(body)) {
			t.Errorf("%s: _budget.chars %v, REST body %d units", id, got, u16(body))
		}
	}
}

func TestContext_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	agent, _ := r.Agent(t)
	if resp := agent.Do(t, "GET", "/api/tracks/"+lt.TrackLuna+"/context?include=foo", nil); resp.Status != 422 {
		t.Errorf("include=foo: %d %s", resp.Status, resp.Body)
	}
	if resp := agent.Do(t, "GET", "/api/tracks/nope/context", nil); resp.Status != 404 {
		t.Errorf("unknown track: %d", resp.Status)
	}
	resp := agent.Do(t, "GET", "/api/tracks/"+lt.TrackLuna+"/context?include=lyrics&include=comments", nil)
	if resp.Status != 200 || obj(t, obj(t, resp.JSON(t))["_budget"])["limit"] != float64(trackctx.IncludeLimit) {
		t.Errorf("includes: %d %.300s", resp.Status, resp.Body)
	}
	// the operator's links live twelve hours
	human := r.Human(t)
	links := obj(t, obj(t, human.Do(t, "GET", "/api/tracks/"+lt.TrackLuna+"/context", nil).JSON(t))["_links"])
	if exp := obj(t, links["stream"])["expires_at"]; exp != lt.FixedNow.Add(12*3600e9).Format("2006-01-02T15:04:05Z") {
		t.Errorf("human links expire %v", exp)
	}
}

// TestAcceptance_M2a_ContextBudget: the worst-case track answers within 24 000
// UTF-16 units over REST and over MCP, and _budget.chars equals each measured length.
func TestAcceptance_M2a_ContextBudget(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	id := trackctxtest.WorstCase(t, r.Store, r.Clock.Now())
	agent, _ := r.Agent(t)

	resp := agent.Do(t, "GET", "/api/tracks/"+id+"/context", nil)
	if resp.Status != 200 {
		t.Fatalf("%d %.300s", resp.Status, resp.Body)
	}
	body := bytes.TrimSuffix(resp.Body, []byte("\n"))
	chars := obj(t, obj(t, resp.JSON(t))["_budget"])["chars"]
	if n := u16(body); n > trackctx.DefaultLimit || chars != float64(n) {
		t.Errorf("REST: %d units, _budget.chars %v, limit %d", n, chars, trackctx.DefaultLimit)
	}

	sess := r.MCP(t, rw...)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_track_context", Arguments: map[string]any{"id": id}})
	if err != nil || res.IsError {
		t.Fatalf("mcp: %v %+v", err, res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content %T", res.Content[0])
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &v); err != nil {
		t.Fatal(err)
	}
	if n := u16([]byte(tc.Text)); n > trackctx.DefaultLimit || obj(t, v["_budget"])["chars"] != float64(n) {
		t.Errorf("MCP TextContent: %d units, _budget.chars %v", n, obj(t, v["_budget"])["chars"])
	}
	if !strings.Contains(tc.Text, `"truncated":["comments","children","lyrics"]`) {
		t.Errorf("truncated list missing in %.200s", tc.Text[len(tc.Text)-200:])
	}
}
