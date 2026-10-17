package api_test

import (
	"context"
	"encoding/json"

	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/url"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/songs/songstest"
)

// libRig is the L1 library rig: the fixtures plus the mirror of the real library structure, settled.
func libRig(t *testing.T) *servertest.Rig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true})
	r.Settle(t)
	return r
}

func str(v any) string { s, _ := v.(string); return s }

// TestLibraryOps runs the read and the write checks on one settled L1 rig (building it is the expensive part).
func TestLibraryOps(t *testing.T) {
	t.Parallel()
	r := libRig(t)
	t.Run("read_shapes", func(t *testing.T) { libraryReadShapes(t, r) })
	t.Run("write_rules", func(t *testing.T) { libraryWriteRules(t, r) })
	t.Run("song_filters", func(t *testing.T) { songFilterShapes(t, r) })
	t.Run("track_amendments", func(t *testing.T) { trackAmendments(t, r) })
}

func libraryReadShapes(t *testing.T, r *servertest.Rig) {
	human := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead)

	// list_muses: rows with coverage; the synthetic mirror has index Muses
	ms := m(agent.OK(t, "GET", "/api/muses?limit=200", nil))
	items := arr(ms["items"])
	if len(items) < 6 || ms["total"] != float64(len(items)) {
		t.Fatalf("muses: %d items, total %v", len(items), ms["total"])
	}
	first := m(items[0])
	for _, k := range []string{"id", "kind", "title", "artist", "brief", "source", "created_by", "updated_at", "revision", "hidden", "songs", "draft", "rendered", "judged", "picked", "queued_jobs"} {
		if _, ok := first[k]; !ok {
			t.Errorf("muse summary lacks %q: %v", k, first)
		}
	}
	// q is a phrase-prefix search; a hostile q cannot break the query
	found := m(agent.OK(t, "GET", "/api/muses?q="+url.QueryEscape("Synthetic Muse 001"), nil))
	if arr(found["items"]) == nil || len(arr(found["items"])) != 1 {
		t.Errorf("q search: %v", found)
	}
	for _, q := range []string{`"`, `NEAR(a b)`, `a OR b`, `*`} {
		if resp := agent.Do(t, "GET", "/api/muses?q="+url.QueryEscape(q), nil); resp.Status != 200 {
			t.Errorf("q=%q: %d %s", q, resp.Status, resp.Body)
		}
	}
	museID := str(first["id"])

	// get_muse: coverage, angles, songs
	d := m(agent.OK(t, "GET", "/api/muses/"+museID, nil))
	cov := m(d["coverage"])
	if m(d["muse"])["id"] != museID || cov["songs"] != d["songs_total"] || float64(len(arr(d["songs"]))) != d["songs_total"] || d["angles"] == nil {
		t.Errorf("get_muse: %v", d)
	}
	if z := m(agent.OK(t, "GET", "/api/muses/"+museID+"?songs_limit=0", nil)); len(arr(z["songs"])) != 0 || z["songs_total"] != d["songs_total"] {
		t.Errorf("songs_limit=0: %v", z)
	}
	if resp := agent.Do(t, "GET", "/api/muses/nope", nil); resp.Status != 404 || m(resp.JSON(t))["code"] != "NOT_FOUND" || strings.Contains(string(resp.Body), "Synthetic") {
		t.Errorf("unknown muse: %d %s", resp.Status, resp.Body)
	}

	// list_songs: filters and the single Song of a draft / take
	all := m(agent.OK(t, "GET", "/api/songs?limit=100", nil))
	songs := arr(all["items"])
	if len(songs) == 0 || all["total"] == nil {
		t.Fatalf("songs: %v", all)
	}
	s0 := m(songs[0])
	for _, k := range []string{"id", "title", "angle", "muse_id", "state", "takes", "pick", "draft_id", "root_track_id", "queued_jobs", "open_proposals", "hidden", "archived", "updated_at"} {
		if _, ok := s0[k]; !ok {
			t.Errorf("song summary lacks %q", k)
		}
	}
	var dsong, rs map[string]any
	for _, it := range songs {
		switch m(it)["state"] {
		case "draft":
			if dsong == nil {
				dsong = m(it)
			}
		case "rendered", "judged", "picked":
			if rs == nil {
				rs = m(it)
			}
		}
	}
	if dsong == nil || rs == nil {
		t.Fatalf("the mirror must hold a draft and a rendered Song")
	}
	if pg := m(agent.OK(t, "GET", "/api/songs?limit=2", nil)); len(arr(pg["items"])) != 2 || pg["next_cursor"] == nil || pg["total"] != all["total"] {
		t.Errorf("paging: %v", pg["next_cursor"])
	}
	if resp := agent.Do(t, "GET", "/api/songs?cursor=%25%25", nil); resp.Status != 422 {
		t.Errorf("bad cursor: %d", resp.Status)
	}

	// get_song by id, draft and take
	v := m(agent.OK(t, "GET", "/api/songs/"+str(dsong["id"]), nil))
	sc := m(v["song"])
	if sc["id"] != dsong["id"] || sc["state"] != "draft" || m(v["draft"])["id"] != dsong["draft_id"] || v["takes"] == nil || v["sounds"] == nil || v["sessions"] == nil || v["proposals"] == nil || v["events"] == nil {
		t.Errorf("get_song: %v", v)
	}
	if bd := m(agent.OK(t, "GET", "/api/songs/"+str(dsong["draft_id"])+"?by=draft", nil)); m(bd["song"])["id"] != dsong["id"] {
		t.Errorf("by=draft: %v", bd)
	}
	rv := m(agent.OK(t, "GET", "/api/songs/"+str(rs["id"]), nil))
	tk := arr(rv["takes"])
	if len(tk) == 0 || rv["takes_total"] == nil {
		t.Fatalf("rendered Song without takes: %v", rv)
	}
	t0 := m(tk[0])
	for _, k := range []string{"id", "title", "status", "generation", "origin", "duration_s", "recorded_at", "lossy", "recipe_known", "stars", "is_pick", "pinned", "has_ears", "flags", "ladder", "tempo"} {
		if _, ok := t0[k]; !ok {
			t.Errorf("take card lacks %q", k)
		}
	}
	if bt := m(agent.OK(t, "GET", "/api/songs/"+str(t0["id"])+"?by=track", nil)); m(bt["song"])["id"] != rs["id"] {
		t.Errorf("by=track: %v", bt)
	}
	if resp := agent.Do(t, "GET", "/api/songs/nope", nil); resp.Status != 404 || m(resp.JSON(t))["code"] != "NOT_FOUND" {
		t.Errorf("unknown song: %d", resp.Status)
	}
	if resp := agent.Do(t, "GET", "/api/songs/x?by=bogus", nil); resp.Status != 422 {
		t.Errorf("bad by: %d", resp.Status)
	}

	// list_sessions
	ss := m(human.OK(t, "GET", "/api/sessions", nil))
	if len(arr(ss["items"])) == 0 {
		t.Fatalf("sessions: %v", ss)
	}
	for _, k := range []string{"id", "kind", "name", "started_at", "ended_at", "hidden", "revision", "drafts", "takes", "songs"} {
		if _, ok := m(arr(ss["items"])[0])[k]; !ok {
			t.Errorf("session lacks %q", k)
		}
	}
	if k := m(agent.OK(t, "GET", "/api/sessions?kind=import&kind=upload", nil)); k["total"] == nil {
		t.Errorf("kind filter: %v", k)
	}

	// get_muse_board: counts and chips agree
	bc := m(agent.OK(t, "GET", "/api/board", nil))
	bk := m(agent.OK(t, "GET", "/api/board?detail=chips&limit=500", nil))
	if len(arr(bc["rows"])) != len(arr(bk["rows"])) || fmt.Sprint(bc["totals"]) != fmt.Sprint(bk["totals"]) || bc["taxonomy_version"] == nil {
		t.Errorf("board counts vs chips: %v / %v", bc["totals"], bk["totals"])
	}
	if resp := agent.Do(t, "GET", "/api/board?sound=colour:red", nil); resp.Status != 422 {
		t.Errorf("board unknown facet: %d", resp.Status)
	}
	if resp := agent.Do(t, "GET", "/api/board?detail=all", nil); resp.Status != 422 {
		t.Errorf("board bad detail: %d", resp.Status)
	}
}

func libraryWriteRules(t *testing.T, r *servertest.Rig) {
	human := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	reader, _ := r.Agent(t, auth.ScopeRead)

	// create_muse: 201, idempotent replay, key conflict carries ids only
	body := map[string]any{"kind": "reference", "title": "Canary Inspiration Title", "artist": "Canary Inspiration Artist"}
	if resp := agent.Do(t, "POST", "/api/muses", body); resp.Status != 428 {
		t.Errorf("missing key: %d %s", resp.Status, resp.Body)
	}
	resp := agent.Do(t, "POST", "/api/muses", body, "Idempotency-Key", "k-muse-1")
	if resp.Status != 201 {
		t.Fatalf("create_muse: %d %s", resp.Status, resp.Body)
	}
	mu := m(resp.JSON(t))
	if mu["source"] != "agent" || mu["revision"] != float64(1) || mu["hidden"] != false || mu["brief"] != nil {
		t.Errorf("muse: %v", mu)
	}
	if rep := agent.Do(t, "POST", "/api/muses", body, "Idempotency-Key", "k-muse-1"); rep.Status != 201 || m(rep.JSON(t))["id"] != mu["id"] {
		t.Errorf("replay: %d %s", rep.Status, rep.Body)
	}
	dup := human.Do(t, "POST", "/api/muses", map[string]any{"kind": "reference", "title": "canary  inspiration title", "artist": "CANARY inspiration artist"}, "Idempotency-Key", "k-muse-2")
	dj := m(dup.JSON(t))
	if dup.Status != 409 || dj["code"] != "MUSE_EXISTS" || m(dj["details"])["muse_id"] != mu["id"] || strings.Contains(strings.ToLower(string(dup.Body)), "canary") {
		t.Errorf("duplicate: %d %s", dup.Status, dup.Body)
	}
	if bad := agent.Do(t, "POST", "/api/muses", map[string]any{"kind": "brief", "title": "T"}, "Idempotency-Key", "k-muse-3"); bad.Status != 422 {
		t.Errorf("brief without text: %d %s", bad.Status, bad.Body)
	}
	if ro := reader.Do(t, "POST", "/api/muses", body, "Idempotency-Key", "k-muse-4"); ro.Status != 403 {
		t.Errorf("read scope create: %d", ro.Status)
	}
	id := str(mu["id"])

	// update_muse: revision lock
	if st := agent.Do(t, "PATCH", "/api/muses/"+id, map[string]any{"revision": 9, "title": "x"}); st.Status != 409 || m(st.JSON(t))["code"] != "REVISION_CONFLICT" || m(m(st.JSON(t))["details"])["current_revision"] != float64(1) {
		t.Errorf("stale: %d %s", st.Status, st.Body)
	}
	up := m(agent.OK(t, "PATCH", "/api/muses/"+id, map[string]any{"revision": 1, "title": "Canary Renamed"}))
	if up["title"] != "Canary Renamed" || up["revision"] != float64(2) {
		t.Errorf("update_muse: %v", up)
	}
	if e := agent.Do(t, "PATCH", "/api/muses/"+id, map[string]any{"revision": 2}); e.Status != 422 {
		t.Errorf("empty patch: %d", e.Status)
	}

	// assign_muse_songs: all or nothing
	sl := arr(m(agent.OK(t, "GET", "/api/songs?limit=3", nil))["items"])
	ids := []any{m(sl[0])["id"], m(sl[1])["id"]}
	as := m(agent.OK(t, "POST", "/api/muses/"+id+"/songs", map[string]any{"song_ids": ids}))
	if as["assigned"] != float64(2) || as["unchanged"] != float64(0) {
		t.Errorf("assign: %v", as)
	}
	if bad := agent.Do(t, "POST", "/api/muses/"+id+"/songs", map[string]any{"song_ids": []any{m(sl[2])["id"], "nope"}}); bad.Status != 422 || m(bad.JSON(t))["code"] != "UNKNOWN_SONG" {
		t.Errorf("unknown song: %d %s", bad.Status, bad.Body)
	}
	if again := m(agent.OK(t, "POST", "/api/muses/"+id+"/songs", map[string]any{"song_ids": ids})); again["unchanged"] != float64(2) {
		t.Errorf("repeat assign: %v", again)
	}
	// the assignment shows in get_muse and get_song
	if dm := m(agent.OK(t, "GET", "/api/muses/"+id, nil)); dm["songs_total"] != float64(2) {
		t.Errorf("get_muse after assign: %v", dm["songs_total"])
	}
	gs := m(agent.OK(t, "GET", "/api/songs/"+str(ids[0]), nil))
	if m(gs["muse"])["id"] != id || m(gs["muse"])["title"] != "Canary Renamed" {
		t.Errorf("get_song muse: %v", gs["muse"])
	}

	// update_song: revision, angle, muse, hide
	sv := m(gs["song"])
	if st := agent.Do(t, "PATCH", "/api/songs/"+str(ids[0]), map[string]any{"revision": 99, "title": "x"}); st.Status != 409 || m(st.JSON(t))["code"] != "REVISION_CONFLICT" {
		t.Errorf("stale song: %d", st.Status)
	}
	us := m(agent.OK(t, "PATCH", "/api/songs/"+str(ids[0]), map[string]any{"revision": sv["revision"], "title": "Renamed Song", "angle": "my-angle", "muse_id": nil}))
	if us["title"] != "Renamed Song" || us["angle"] != "my-angle" || us["angle_source"] != "manual" {
		t.Errorf("update_song: %v", us)
	}
	if e := agent.Do(t, "PATCH", "/api/songs/"+str(ids[0]), map[string]any{"revision": us["revision"], "angle": "Not A Slug"}); e.Status != 422 || m(e.JSON(t))["code"] != "INVALID_ANGLE" {
		t.Errorf("invalid angle: %d %s", e.Status, e.Body)
	}
	cl := m(agent.OK(t, "PATCH", "/api/songs/"+str(ids[0]), map[string]any{"revision": us["revision"], "angle": nil}))
	if cl["angle"] != nil || cl["angle_source"] != "cleared" {
		t.Errorf("clear angle: %v", cl)
	}
	if e := agent.Do(t, "PATCH", "/api/songs/"+str(ids[0]), map[string]any{"revision": cl["revision"], "muse_id": "nope"}); e.Status != 422 || m(e.JSON(t))["code"] != "UNKNOWN_MUSE" {
		t.Errorf("unknown muse: %d %s", e.Status, e.Body)
	}

	// update_session
	sess := arr(m(human.OK(t, "GET", "/api/sessions", nil))["items"])
	s0 := m(sess[0])
	renamed := m(agent.OK(t, "PATCH", "/api/sessions/"+str(s0["id"]), map[string]any{"revision": s0["revision"], "name": "Canary Session"}))
	if renamed["name"] != "Canary Session" || renamed["revision"] != float64(m(s0)["revision"].(float64)+1) || renamed["songs"] != s0["songs"] {
		t.Errorf("update_session: %v", renamed)
	}
	if e := agent.Do(t, "PATCH", "/api/sessions/"+str(s0["id"]), map[string]any{"revision": s0["revision"], "name": "x"}); e.Status != 409 {
		t.Errorf("stale session: %d", e.Status)
	}
	if e := agent.Do(t, "PATCH", "/api/sessions/nope", map[string]any{"revision": 1, "name": "x"}); e.Status != 404 || strings.Contains(string(e.Body), "Canary") {
		t.Errorf("unknown session: %d %s", e.Status, e.Body)
	}
}

// songFilterShapes drives the list_songs and search_sounds filters over REST and MCP (the filters are part of the
// capability: a service-level test alone would not prove that agents and the UI can send them).
func songFilterShapes(t *testing.T, r *servertest.Rig) {
	agent, _ := r.Agent(t, auth.ScopeRead)
	list := func(q string) map[string]any {
		t.Helper()
		return m(agent.OK(t, "GET", "/api/songs?limit=100"+q, nil))
	}
	all := list("")
	total := all["total"].(float64)
	var draftSong, renderedSong map[string]any
	for _, it := range arr(all["items"]) {
		switch m(it)["state"] {
		case "draft":
			if draftSong == nil && m(it)["draft_id"] != nil {
				draftSong = m(it)
			}
		case "rendered", "judged", "picked":
			if renderedSong == nil && m(it)["root_track_id"] != nil {
				renderedSong = m(it)
			}
		}
	}
	if draftSong == nil || renderedSong == nil {
		t.Fatal("the mirror must hold a draft Song and a rendered Song")
	}
	for _, st := range []string{"draft", "rendered", "judged", "picked"} {
		page := list("&state=" + st)
		for _, it := range arr(page["items"]) {
			if m(it)["state"] != st {
				t.Errorf("state=%s listed a %v Song", st, m(it)["state"])
			}
		}
		if page["total"].(float64) > total {
			t.Errorf("state=%s: total %v > %v", st, page["total"], total)
		}
	}
	if two := list("&state=draft&state=rendered"); two["total"].(float64) < list("&state=draft")["total"].(float64) {
		t.Errorf("repeatable state: %v", two["total"])
	}
	// the single Song of a draft and of a take
	if got := arr(list("&draft_id=" + str(draftSong["draft_id"]))["items"]); len(got) != 1 || m(got[0])["id"] != draftSong["id"] {
		t.Errorf("draft_id: %v", got)
	}
	if got := arr(list("&track_id=" + str(renderedSong["root_track_id"]))["items"]); len(got) != 1 || m(got[0])["id"] != renderedSong["id"] {
		t.Errorf("track_id: %v", got)
	}
	if got := arr(list("&draft_id=nope")["items"]); len(got) != 0 {
		t.Errorf("unknown draft_id: %v", got)
	}
	// booleans: both polarities partition the list
	for _, f := range []string{"has_pick", "queued", "has_open_proposals", "tempo_mismatch"} {
		yes, no := list("&" + f + "=true")["total"].(float64), list("&" + f + "=false")["total"].(float64)
		if yes+no != total {
			t.Errorf("%s: %v + %v != %v", f, yes, no, total)
		}
	}
	nm, mu := list("&no_muse=true"), list("&muse_id="+str(renderedSong["muse_id"]))
	for _, it := range arr(nm["items"]) {
		if m(it)["muse_id"] != nil {
			t.Errorf("no_muse listed a Song with a Muse: %v", m(it)["id"])
		}
	}
	if renderedSong["muse_id"] != nil {
		for _, it := range arr(mu["items"]) {
			if m(it)["muse_id"] != renderedSong["muse_id"] {
				t.Errorf("muse_id listed %v", m(it)["muse_id"])
			}
		}
	}
	if q := list("&q=" + url.QueryEscape(strings.ToUpper(str(renderedSong["title"])))); q["total"].(float64) < 1 {
		t.Errorf("q is case-insensitive over the title: %v", q["total"])
	}
	if resp := agent.Do(t, "GET", "/api/songs?state=bogus", nil); resp.Status != 422 {
		t.Errorf("unknown state: %d", resp.Status)
	}
	if resp := agent.Do(t, "GET", "/api/songs?queued=maybe", nil); resp.Status != 422 {
		t.Errorf("non-boolean queued: %d", resp.Status)
	}
	if resp := agent.Do(t, "GET", "/api/songs?sound=colour:red", nil); resp.Status != 422 {
		t.Errorf("unknown facet: %d", resp.Status)
	}
	// search_sounds embeds the same SongFilterIn (its handler is L1-B6's): a bad value is refused by the binding
	// (422) before the handler runs, which only happens when the parameter is bound
	if resp := agent.Do(t, "GET", "/api/sounds/search?state=bogus", nil); resp.Status != 422 {
		t.Errorf("search_sounds state binding: %d", resp.Status)
	}

	// MCP: the same arguments reach the same filters
	sess := r.MCP(t, auth.ScopeRead)
	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		params := &mcp.CallToolParams{Name: tool, Arguments: args}
		if tool == "list_songs" { // a generic operation (D-L1-20): reached through call_operation
			params = &mcp.CallToolParams{Name: "call_operation", Arguments: map[string]any{"operation_id": tool, "arguments": args}}
		}
		res, err := sess.CallTool(context.Background(), params)
		if err != nil || res.IsError {
			t.Fatalf("%s %v: %v %+v", tool, args, err, res)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := call("list_songs", map[string]any{"state": []string{"draft"}, "limit": 100}); v["total"] != list("&state=draft")["total"] {
		t.Errorf("MCP list_songs state: %v", v["total"])
	}
	if v := call("list_songs", map[string]any{"draft_id": draftSong["draft_id"]}); len(arr(v["items"])) != 1 {
		t.Errorf("MCP list_songs draft_id: %v", v)
	}
	if v := call("list_songs", map[string]any{"has_pick": true, "limit": 100}); v["total"] != list("&has_pick=true")["total"] {
		t.Errorf("MCP list_songs has_pick: %v", v["total"])
	}
}

// trackAmendments covers C98 over REST: song_id/is_pick on the summaries, the song of a detail, the search filters
// and get_stats.library_model.
func trackAmendments(t *testing.T, r *servertest.Rig) {
	agent, _ := r.Agent(t, auth.ScopeRead)
	songsPage := m(agent.OK(t, "GET", "/api/songs?limit=100", nil))
	// the Pick rows come from the picks step of the Reconciler (L1-B7); until it joins, write one as the world helper does
	for _, it := range arr(songsPage["items"]) {
		if row := m(it); row["pick"] == nil && row["root_track_id"] != nil && row["state"] != "draft" {
			songstest.Over(t, r.Store).Pick(str(row["id"]), str(row["root_track_id"]), "ladder")
			break
		}
	}
	r.Settle(t)
	songsPage = m(agent.OK(t, "GET", "/api/songs?limit=100", nil))
	var song map[string]any
	for _, it := range arr(songsPage["items"]) {
		if m(it)["pick"] != nil {
			song = m(it)
			break
		}
	}
	if song == nil {
		t.Fatal("the mirror must hold a Song with a Pick")
	}
	pickTake := str(m(song["pick"])["take_id"])
	page := m(agent.OK(t, "GET", "/api/tracks?limit=100&song_id="+str(song["id"]), nil))
	if len(arr(page["items"])) == 0 {
		t.Fatalf("song_id filter: %v", page)
	}
	picks := 0
	for _, it := range arr(page["items"]) {
		row := m(it)
		if row["song_id"] != song["id"] {
			t.Errorf("take %v: song_id %v", row["id"], row["song_id"])
		}
		// wave 4b: the Song's title and angle ride on the summary (null-safe: the angle may be unset)
		if row["song_title"] != song["title"] || row["song_angle"] != song["angle"] {
			t.Errorf("take %v: song_title/song_angle %v/%v, the Song has %v/%v", row["id"], row["song_title"], row["song_angle"], song["title"], song["angle"])
		}
		if row["is_pick"] == true {
			picks++
			if row["id"] != pickTake {
				t.Errorf("is_pick on %v, the Pick is %v", row["id"], pickTake)
			}
		}
	}
	if picks != 1 {
		t.Errorf("%d picks in the Song", picks)
	}
	only := m(agent.OK(t, "GET", "/api/tracks?limit=100&is_pick=true", nil))
	if len(arr(only["items"])) == 0 {
		t.Error("is_pick=true is empty")
	}
	for _, it := range arr(only["items"]) {
		if m(it)["is_pick"] != true {
			t.Errorf("is_pick=true listed %v", m(it)["id"])
		}
	}
	d := m(agent.OK(t, "GET", "/api/tracks/"+pickTake, nil))
	sg := m(d["song"])
	if sg["id"] != song["id"] || sg["title"] != song["title"] || sg["pick_source"] != m(song["pick"])["source"] {
		t.Errorf("track detail song: %v vs %v", sg, song)
	}
	for _, k := range []string{"angle", "muse_id"} {
		if _, ok := sg[k]; !ok {
			t.Errorf("detail song lacks %q", k)
		}
	}
	sess := arr(m(agent.OK(t, "GET", "/api/sessions?limit=50", nil))["items"])
	if len(sess) > 0 {
		if got := m(agent.OK(t, "GET", "/api/tracks?limit=100&session_id="+str(m(sess[0])["id"]), nil)); got["total"] == nil {
			t.Errorf("session_id filter: %v", got)
		}
	}

	// /context carries the Song, Muse and Sounds sections after track
	c := m(agent.OK(t, "GET", "/api/tracks/"+pickTake+"/context", nil))
	cs := m(c["song"])
	if cs["id"] != song["id"] || m(cs["pick"])["is_this"] != true || cs["siblings"] == nil || c["sounds"] == nil {
		t.Errorf("context song: %v", c)
	}
	for _, sib := range arr(cs["siblings"]) {
		if m(sib)["id"] == pickTake {
			t.Error("the take is its own sibling")
		}
	}
	if song["muse_id"] != nil {
		if mu := m(c["muse"]); mu["id"] != song["muse_id"] {
			t.Errorf("context muse: %v", mu)
		}
	}

	// get_stats.library_model
	st := m(agent.OK(t, "GET", "/api/stats", nil))
	lm := m(st["library_model"])
	sc := m(lm["songs"])
	sum := sc["draft"].(float64) + sc["rendered"].(float64) + sc["judged"].(float64) + sc["picked"].(float64)
	if sc["total"] != sum || sc["total"].(float64) < 1 || lm["muses"].(float64) < 1 || lm["sessions"] == nil || lm["taxonomy_version"] == nil || lm["pending"] != float64(0) {
		t.Errorf("library_model: %v", lm)
	}
}
