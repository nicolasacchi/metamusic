package api_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
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
	// NOTE: the list_songs filters (state, sound, draft_id, ...) are not bound over REST/MCP yet: the frozen
	// listSongsIn embeds an unexported struct that huma skips and uses *bool query params it rejects (reported as a
	// Stage-1 deviation). They are covered at service level (songs.List) and through get_muse_board.
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
