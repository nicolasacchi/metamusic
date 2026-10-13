package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

var rw = []auth.Scope{auth.ScopeRead, auth.ScopeWrite}

func judgRig(t *testing.T) *servertest.Rig {
	t.Helper()
	return servertest.New(t, servertest.Options{Fixture: true, Files: true})
}

func obj(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %v", v)
	}
	return m
}

func put(t *testing.T, c *servertest.Client, path string, body any) *servertest.Response {
	t.Helper()
	return c.Do(t, "PUT", path, body)
}

func TestRate_SourceFromPrincipal_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, rw...)
	id := lt.TrackShort

	resp := put(t, human, "/api/tracks/"+id+"/rating", map[string]any{"stars": 4, "verdict": "keeper"})
	if resp.Status != 200 {
		t.Fatalf("human rate: %d %s", resp.Status, resp.Body)
	}
	body := obj(t, resp.JSON(t))
	hr := obj(t, body["human"])
	if hr["stars"] != 4.0 || hr["verdict"] != "keeper" || obj(t, hr["by"])["kind"] != "human" || body["critic"] != nil {
		t.Fatalf("human rating %v", body)
	}
	// the agent rates as the critic
	resp = put(t, agent, "/api/tracks/"+id+"/rating", map[string]any{"stars": 2})
	body = obj(t, resp.JSON(t))
	if resp.Status != 200 || obj(t, body["critic"])["stars"] != 2.0 || obj(t, body["human"])["stars"] != 4.0 {
		t.Fatalf("agent rate: %d %s", resp.Status, resp.Body)
	}
	// ... and an agent body that claims source=human cannot touch the human row, whatever Huma does with the field
	resp = put(t, agent, "/api/tracks/"+id+"/rating", map[string]any{"stars": 1, "source": "human"})
	if resp.Status != 200 && resp.Status != 422 {
		t.Fatalf("extra field: %d %s", resp.Status, resp.Body)
	}
	var stars int
	if err := r.Store.R.QueryRow(`SELECT stars FROM ratings WHERE track_id=? AND source='human'`, id).Scan(&stars); err != nil || stars != 4 {
		t.Fatalf("human row after the spoof attempt: %d %v", stars, err)
	}
	// ... the same on the raw field of the principal kind
	var kind string
	if err := r.Store.R.QueryRow(`SELECT principal_kind FROM ratings WHERE track_id=? AND source='human'`, id).Scan(&kind); err != nil || kind != "human" {
		t.Fatalf("human row principal kind %q", kind)
	}
	// stars is required (null clears)
	if resp := put(t, human, "/api/tracks/"+id+"/rating", map[string]any{"verdict": "x"}); resp.Status != 422 {
		t.Errorf("missing stars: %d", resp.Status)
	}
	if resp := put(t, human, "/api/tracks/nope/rating", map[string]any{"stars": 3}); resp.Status != 404 {
		t.Errorf("unknown track: %d", resp.Status)
	}
	// a read-only token cannot rate
	ro, _ := r.Agent(t, auth.ScopeRead)
	if resp := put(t, ro, "/api/tracks/"+id+"/rating", map[string]any{"stars": 3}); resp.Status != 403 {
		t.Errorf("read-only rate: %d", resp.Status)
	}
}

func TestRate_ClearAndVerdict_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	path := "/api/tracks/" + lt.TrackLuna + "/rating"
	resp := put(t, human, path, map[string]any{"stars": nil})
	body := obj(t, resp.JSON(t))
	if resp.Status != 200 || obj(t, body["human"])["stars"] != nil || obj(t, body["human"])["verdict"] != nil || obj(t, body["critic"])["stars"] != 2.0 {
		t.Fatalf("clear: %d %s", resp.Status, resp.Body)
	}
	if resp := put(t, human, path, map[string]any{"stars": 3, "verdict": strings.Repeat("v", 281)}); resp.Status != 422 {
		t.Errorf("281 chars: %d %s", resp.Status, resp.Body)
	}
	if resp := put(t, human, path, map[string]any{"stars": 3, "verdict": strings.Repeat("v", 280)}); resp.Status != 200 {
		t.Errorf("280 chars: %d", resp.Status)
	}
	if resp := put(t, human, path, map[string]any{"stars": 6}); resp.Status != 422 {
		t.Errorf("6 stars: %d", resp.Status)
	}
}

func TestUpdateTrack_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, rw...)
	path := "/api/tracks/" + lt.TrackCitta
	resp := human.Do(t, "PATCH", path, map[string]any{"title": "New Title", "status": "final"})
	d := obj(t, resp.JSON(t))
	if resp.Status != 200 || d["title"] != "New Title" || d["status"] != "final" || d["status_set_by"] != "human:alice" {
		t.Fatalf("human: %d %s", resp.Status, resp.Body)
	}
	// an agent sees the operator as "human"
	resp = agent.Do(t, "GET", path, nil)
	d = obj(t, resp.JSON(t))
	if d["status_set_by"] != "human" {
		t.Errorf("agent view of status_set_by = %v", d["status_set_by"])
	}
	// the agent's own change is attributed to its key
	resp = agent.Do(t, "PATCH", path, map[string]any{"status": "to_relisten"})
	d = obj(t, resp.JSON(t))
	if resp.Status != 200 || !strings.HasPrefix(d["status_set_by"].(string), "agent:") {
		t.Fatalf("agent: %d %s", resp.Status, resp.Body)
	}
	if resp := human.Do(t, "PATCH", path, map[string]any{}); resp.Status != 422 {
		t.Errorf("empty patch: %d", resp.Status)
	}
	if resp := human.Do(t, "PATCH", path, map[string]any{"status": "gone"}); resp.Status != 422 {
		t.Errorf("bad status: %d", resp.Status)
	}
	if resp := human.Do(t, "PATCH", "/api/tracks/nope", map[string]any{"title": "x"}); resp.Status != 404 {
		t.Errorf("unknown: %d", resp.Status)
	}
}

func TestTags_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, rw...)
	path := "/api/tracks/" + lt.TrackCitta + "/tags"
	resp := agent.Do(t, "PATCH", path, map[string]any{"add": []string{"  Slow   Burn ", "x"}})
	if resp.Status != 200 {
		t.Fatalf("agent add: %d %s", resp.Status, resp.Body)
	}
	got := obj(t, resp.JSON(t))["tags"].([]any)
	if len(got) != 2 || obj(t, got[0])["name"] != "slow burn" || obj(t, got[0])["sources"].([]any)[0] != "agent" {
		t.Fatalf("tags %v", got)
	}
	if resp := human.Do(t, "PATCH", path, map[string]any{"remove": []string{"x"}}); resp.Status != 200 {
		t.Errorf("human remove: %d", resp.Status)
	}
	if resp := human.Do(t, "PATCH", path, map[string]any{"add": []string{"bad<tag"}}); resp.Status != 422 || !strings.Contains(string(resp.Body), "INVALID_TAG") {
		t.Errorf("invalid tag: %d %s", resp.Status, resp.Body)
	}
	if resp := human.Do(t, "PATCH", path, map[string]any{}); resp.Status != 422 {
		t.Errorf("empty: %d", resp.Status)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("t%d", i)
	}
	if resp := human.Do(t, "PATCH", path, map[string]any{"add": many}); resp.Status != 422 {
		t.Errorf("21 tags: %d", resp.Status)
	}
	resp = agent.Do(t, "GET", "/api/tags", nil)
	items := obj(t, resp.JSON(t))["items"].([]any)
	if resp.Status != 200 || len(items) < 4 || obj(t, items[0])["name"] != "ballad" {
		t.Errorf("list_tags: %d %s", resp.Status, resp.Body)
	}
}

func commentPath(track string) string { return "/api/tracks/" + track + "/comments" }

func TestComments_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, rw...)
	// create needs an Idempotency-Key
	if resp := human.Do(t, "POST", commentPath(lt.TrackLuna), map[string]any{"body": "x"}); resp.Status != 428 {
		t.Errorf("missing key: %d %s", resp.Status, resp.Body)
	}
	resp := human.Do(t, "POST", commentPath(lt.TrackLuna), map[string]any{"body": "sync is off", "at_second": 5.04, "kind": "fix"}, "Idempotency-Key", "k1")
	c := obj(t, resp.JSON(t))
	if resp.Status != 201 || c["at_second"] != 5.0 || c["link"] != "/tracks/"+lt.TrackLuna+"?t=5.0" || c["url"] != "http://mm.example.com/tracks/"+lt.TrackLuna+"?t=5.0" ||
		obj(t, c["section"])["label"] != "chorus" || c["state"] != "open" || c["source"] != "api" || obj(t, c["author"])["name"] != "alice" {
		t.Fatalf("created: %d %s", resp.Status, resp.Body)
	}
	// the same key replays
	again := human.Do(t, "POST", commentPath(lt.TrackLuna), map[string]any{"body": "sync is off", "at_second": 5.04, "kind": "fix"}, "Idempotency-Key", "k1")
	if again.Status != 201 || obj(t, again.JSON(t))["id"] != c["id"] {
		t.Errorf("replay: %d %s", again.Status, again.Body)
	}
	// out of range
	resp = human.Do(t, "POST", commentPath(lt.TrackLuna), map[string]any{"body": "x", "at_second": 99}, "Idempotency-Key", "k2")
	if resp.Status != 422 || !strings.Contains(string(resp.Body), "AT_SECOND_OUT_OF_RANGE") {
		t.Errorf("out of range: %d %s", resp.Status, resp.Body)
	}
	// NOT_AUTHOR and INVALID_TRANSITION
	patch := "/api/comments/" + c["id"].(string)
	resp = agent.Do(t, "PATCH", patch, map[string]any{"body": "mine now"})
	if resp.Status != 403 || !strings.Contains(string(resp.Body), "NOT_AUTHOR") {
		t.Errorf("not author: %d %s", resp.Status, resp.Body)
	}
	resp = agent.Do(t, "PATCH", patch, map[string]any{"state": "addressed"})
	if resp.Status != 409 || !strings.Contains(string(resp.Body), "INVALID_TRANSITION") {
		t.Errorf("transition: %d %s", resp.Status, resp.Body)
	}
	resp = agent.Do(t, "PATCH", patch, map[string]any{"state": "resolved"})
	if resp.Status != 200 || obj(t, resp.JSON(t))["state"] != "resolved" {
		t.Errorf("resolve: %d %s", resp.Status, resp.Body)
	}
	if resp := agent.Do(t, "PATCH", patch, map[string]any{}); resp.Status != 422 {
		t.Errorf("empty patch: %d", resp.Status)
	}
	if resp := agent.Do(t, "PATCH", "/api/comments/nope", map[string]any{"state": "open"}); resp.Status != 404 {
		t.Errorf("unknown comment: %d", resp.Status)
	}
	// list filters and params
	resp = human.Do(t, "GET", commentPath(lt.TrackLuna)+"?state=open&kind=fix&order=time&limit=5", nil)
	page := obj(t, resp.JSON(t))
	if resp.Status != 200 || page["total"] != 1.0 {
		t.Errorf("list: %d %s", resp.Status, resp.Body)
	}
	if resp := human.Do(t, "GET", commentPath(lt.TrackLuna)+"?limit=201", nil); resp.Status != 422 {
		t.Errorf("limit 201: %d", resp.Status)
	}
}

// TestAddComment_SourceByTransport: web (SPA), api (REST), mcp and stdio.
func TestAddComment_SourceByTransport(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, rw...)
	src := func(resp *servertest.Response) string {
		t.Helper()
		if resp.Status != 201 {
			t.Fatalf("create: %d %s", resp.Status, resp.Body)
		}
		return obj(t, resp.JSON(t))["source"].(string)
	}
	body := map[string]any{"body": "x"}
	if got := src(human.Do(t, "POST", commentPath(lt.TrackLuna), body, "Idempotency-Key", "a", "X-MM-Client", "spa")); got != "web" {
		t.Errorf("spa => %s", got)
	}
	if got := src(human.Do(t, "POST", commentPath(lt.TrackLuna), body, "Idempotency-Key", "b")); got != "api" {
		t.Errorf("human rest => %s", got)
	}
	// an agent cannot claim to be the SPA
	if got := src(agent.Do(t, "POST", commentPath(lt.TrackLuna), body, "Idempotency-Key", "c", "X-MM-Client", "spa")); got != "api" {
		t.Errorf("agent spoofing spa => %s", got)
	}
	call := func(sess *mcp.ClientSession) map[string]any {
		t.Helper()
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "add_comment", Arguments: map[string]any{"id": lt.TrackLuna, "body": "via mcp"}})
		if err != nil || res.IsError {
			t.Fatalf("add_comment: %v %+v", err, res)
		}
		b, _ := json.Marshal(res.StructuredContent)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	if got := call(r.MCP(t, rw...))["source"]; got != "mcp" {
		t.Errorf("mcp => %v", got)
	}
	if got := call(r.Stdio(t, rw...))["source"]; got != "stdio" {
		t.Errorf("stdio => %v", got)
	}
}

// TestComments_AuthorMaskedForAgents (D-M2a-19) through list_comments and get_track.
func TestComments_AuthorMaskedForAgents(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	agent, agentTok := r.Agent(t, rw...)

	list := func(c *servertest.Client) map[string]map[string]any {
		resp := c.Do(t, "GET", commentPath(lt.TrackLuna), nil)
		if resp.Status != 200 {
			t.Fatalf("list: %d %s", resp.Status, resp.Body)
		}
		out := map[string]map[string]any{}
		for _, it := range obj(t, resp.JSON(t))["items"].([]any) {
			m := obj(t, it)
			out[m["id"].(string)] = obj(t, m["author"])
		}
		return out
	}
	hv, av := list(human), list(agent)
	if hv[lt.CommentLunaFix]["id"] != lt.FixtureHuman || hv[lt.CommentLunaFix]["name"] != "fixture" || hv[lt.CommentLunaKeep]["name"] != "Fixture phone" {
		t.Errorf("operator view: %v", hv)
	}
	if diff := cmp.Diff(map[string]any{"kind": "human", "id": "human", "name": "human"}, av[lt.CommentLunaFix]); diff != "" {
		t.Errorf("human author for an agent (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]any{"kind": "device", "id": lt.FixtureDev, "name": "device"}, av[lt.CommentLunaKeep]); diff != "" {
		t.Errorf("device author for an agent (-want +got):\n%s", diff)
	}
	// the agent sees itself under its own key and name
	resp := agent.Do(t, "POST", commentPath(lt.TrackLuna), map[string]any{"body": "mine"}, "Idempotency-Key", "m1")
	if a := obj(t, obj(t, resp.JSON(t))["author"]); a["id"] != "agent:"+agentTok || a["kind"] != "agent" {
		t.Errorf("own author %v", a)
	}
	// get_track: rating rows are rendered per viewer
	var tracks = map[string]map[string]any{}
	for name, c := range map[string]*servertest.Client{"human": human, "agent": agent} {
		resp := c.Do(t, "GET", "/api/tracks/"+lt.TrackLuna, nil)
		tracks[name] = obj(t, obj(t, resp.JSON(t))["rating"])
	}
	if by := obj(t, obj(t, tracks["human"]["human"])["by"]); by["name"] != "fixture" || by["id"] != lt.FixtureHuman {
		t.Errorf("operator sees %v", by)
	}
	if diff := cmp.Diff(map[string]any{"kind": "human", "id": "human", "name": "human"}, obj(t, obj(t, tracks["agent"]["human"])["by"])); diff != "" {
		t.Errorf("rating.human.by for an agent (-want +got):\n%s", diff)
	}
	if by := obj(t, obj(t, tracks["agent"]["critic"])["by"]); by["name"] != "fixture-agent" || by["kind"] != "agent" {
		t.Errorf("critic view %v", by)
	}
	// nothing the agent receives contains the operator's name
	for _, resp := range agent.Responses() {
		if strings.Contains(string(resp.Body), "alice") || strings.Contains(string(resp.Body), "Fixture phone") || strings.Contains(string(resp.Body), "human:fixture") {
			t.Errorf("%s %s leaks a human name: %.200s", resp.Method, resp.Path, resp.Body)
		}
	}
}

// matrix is the filter matrix shared by the REST and MCP faces.
var matrix = []struct {
	name   string
	query  url.Values
	args   map[string]any
	want   []string
	sorted bool
}{
	{name: "min_stars=4", query: url.Values{"min_stars": {"4"}}, args: map[string]any{"min_stars": 4}, want: []string{lt.TrackLuna}},
	{name: "min_stars=3", query: url.Values{"min_stars": {"3"}}, args: map[string]any{"min_stars": 3}, want: []string{lt.TrackLuna, lt.TrackCitta}},
	{name: "unrated", query: url.Values{"unrated": {"true"}}, args: map[string]any{"unrated": true}, want: []string{lt.TrackTwinA, lt.TrackTwinB, lt.TrackShort}},
	{name: "tag=ballad", query: url.Values{"tag": {"ballad"}}, args: map[string]any{"tag": []string{"ballad"}}, want: []string{lt.TrackTwinA, lt.TrackTwinB}},
	{name: "tag=ballad&night", query: url.Values{"tag": {"ballad", "night"}}, args: map[string]any{"tag": []string{"ballad", "night"}}, want: []string{lt.TrackTwinA}},
	{name: "has_open_comments", query: url.Values{"has_open_comments": {"true"}}, args: map[string]any{"has_open_comments": true}, want: []string{lt.TrackLuna, lt.TrackShort}},
	{name: "stars_desc", query: url.Values{"sort": {"stars_desc"}}, args: map[string]any{"sort": "stars_desc"},
		want: []string{lt.TrackLuna, lt.TrackCitta, lt.TrackShort, lt.TrackTwinB, lt.TrackTwinA}, sorted: true},
}

func idsOfPage(t *testing.T, v any) []string {
	t.Helper()
	var out []string
	for _, it := range obj(t, v)["items"].([]any) {
		out = append(out, obj(t, it)["id"].(string))
	}
	return out
}

func TestAcceptance_M2a_RatingStatusTagFilters(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, rw...)
	sess := r.MCP(t, rw...)
	check := func(face, name string, got, want []string, sorted bool) {
		t.Helper()
		g, w := slices.Clone(got), slices.Clone(want)
		if !sorted {
			slices.Sort(g)
			slices.Sort(w)
		}
		if diff := cmp.Diff(w, g); diff != "" {
			t.Errorf("%s %s (-want +got):\n%s", face, name, diff)
		}
	}
	for _, m := range matrix {
		resp := human.Do(t, "GET", "/api/tracks?"+m.query.Encode(), nil)
		if resp.Status != 200 {
			t.Fatalf("%s: %d %s", m.name, resp.Status, resp.Body)
		}
		check("REST human", m.name, idsOfPage(t, resp.JSON(t)), m.want, m.sorted)
		resp = agent.Do(t, "GET", "/api/tracks?"+m.query.Encode(), nil)
		check("REST agent", m.name, idsOfPage(t, resp.JSON(t)), m.want, m.sorted)
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_tracks", Arguments: m.args})
		if err != nil || res.IsError {
			t.Fatalf("MCP %s: %v %+v", m.name, err, res)
		}
		b, _ := json.Marshal(res.StructuredContent)
		var v any
		_ = json.Unmarshal(b, &v)
		check("MCP agent", m.name, idsOfPage(t, v), m.want, m.sorted)
	}
	// min_stars together with unrated is a 422 on every face
	if resp := human.Do(t, "GET", "/api/tracks?min_stars=3&unrated=true", nil); resp.Status != 422 {
		t.Errorf("min_stars+unrated: %d", resp.Status)
	}
	if res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_tracks", Arguments: map[string]any{"min_stars": 3, "unrated": true}}); err != nil || !res.IsError {
		t.Errorf("MCP min_stars+unrated: %v %+v", err, res)
	}
	// rate, tag, comment and discard through the faces, then see it in search
	if resp := put(t, human, "/api/tracks/"+lt.TrackShort+"/rating", map[string]any{"stars": 5}); resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "set_tags", Arguments: map[string]any{"id": lt.TrackShort, "add": []string{"Night"}}}); err != nil {
		t.Fatal(err)
	}
	resp := agent.Do(t, "GET", "/api/tracks?min_stars=5&tag=night", nil)
	if got := idsOfPage(t, resp.JSON(t)); !slices.Equal(got, []string{lt.TrackShort}) {
		t.Errorf("after rate+tag: %v", got)
	}
	if resp := human.Do(t, "PATCH", "/api/tracks/"+lt.TrackShort, map[string]any{"status": "discarded"}); resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	resp = agent.Do(t, "GET", "/api/tracks?min_stars=5", nil)
	if got := idsOfPage(t, resp.JSON(t)); !slices.Equal(got, []string{lt.TrackLuna}) {
		t.Errorf("discarded track still listed: %v", got)
	}
	resp = agent.Do(t, "GET", "/api/tracks?min_stars=5&status=discarded", nil)
	if got := idsOfPage(t, resp.JSON(t)); !slices.Equal(got, []string{lt.TrackShort}) {
		t.Errorf("status=discarded: %v", got)
	}
}

func TestFacetsAndStats_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	agent, _ := r.Agent(t, rw...)
	f := obj(t, agent.Do(t, "GET", "/api/library/facets", nil).JSON(t))
	if len(f["tags"].([]any)) != 3 || len(f["stars"].([]any)) != 2 || f["unrated"] != 3.0 {
		t.Errorf("facets %v", f)
	}
	s := obj(t, agent.Do(t, "GET", "/api/stats", nil).JSON(t))
	j := obj(t, s["judgments"])
	if j["rated_human"] != 2.0 || j["rated_critic"] != 2.0 || j["open_fix"] != 1.0 || s["playlists"] != 1.0 {
		t.Errorf("stats %v", s)
	}
	_ = http.StatusOK
}
