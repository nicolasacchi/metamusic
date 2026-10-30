package api_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// museCanary is a Muse whose title, artist and brief are unique strings built at runtime, plus a Session name canary.
type museCanary struct {
	ID, Title, Artist, Brief, Session string
	SongID                            string
}

func (c museCanary) texts() []string {
	return []string{c.Title, c.Artist, c.Brief, strings.Replace(c.Title, "Title", "Brief Title", 1)}
}

func randHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// canaryRig builds the settled L1 rig and a canary Muse assigned to the Song of two takes, and a canary Session name.
func canaryRig(t *testing.T) (*servertest.Rig, museCanary) {
	t.Helper()
	r, song, _ := picksRig(t)
	h := randHex(t)
	c := museCanary{Title: "Canary Title " + h, Artist: "Canary Artist " + h, Brief: "Canary Brief " + h, Session: "Canary Session " + h, SongID: song}
	human := r.Human(t)
	m := obj(t, human.OK(t, "POST", "/api/muses", map[string]any{"kind": "reference", "title": c.Title, "artist": c.Artist}, "Idempotency-Key", "canary-create"))
	c.ID, _ = m["id"].(string)
	if c.ID == "" {
		t.Fatalf("create_muse: %v", m)
	}
	// a brief Muse carries the brief canary (a reference Muse has no brief)
	human.OK(t, "POST", "/api/muses", map[string]any{"kind": "brief", "title": "Canary Brief Title " + h, "brief": c.Brief}, "Idempotency-Key", "canary-brief")
	human.OK(t, "POST", "/api/muses/"+c.ID+"/songs", map[string]any{"song_ids": []string{song}}, "Idempotency-Key", "canary-assign")
	var sid string
	var rev int
	if err := r.Store.R.QueryRow(`SELECT s.id, s.revision FROM sessions s JOIN session_members m ON m.session_id = s.id ORDER BY s.id LIMIT 1`).Scan(&sid, &rev); err != nil {
		t.Fatal(err)
	}
	human.OK(t, "PATCH", "/api/sessions/"+sid, map[string]any{"revision": rev, "name": c.Session})
	return r, c
}

// museTextOps may carry the canary Muse in their responses (D-L1-6, D-L1-36).
var museTextOps = []string{"list_muses", "get_muse", "create_muse", "update_muse", "get_song", "get_muse_board", "get_track_context", "get_draft", "get_record"}

// sessionNameOps may carry a collection-derived Session name.
var sessionNameOps = []string{"list_sessions", "update_session", "get_song"}

// TestMuseText_OnlyInAllowlistedOps: every readable GET operation is called as the operator and as a read-scope agent
// with a canary Muse assigned to a Song; the canary text may appear only in the allowlisted operations' responses.
func TestMuseText_OnlyInAllowlistedOps(t *testing.T) {
	t.Parallel()
	r, c := canaryRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead)

	var track, draft string
	if err := r.Store.R.QueryRow(`SELECT t.id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = ? ORDER BY t.id LIMIT 1`, c.SongID).Scan(&track); err != nil {
		t.Fatal(err)
	}
	_ = r.Store.R.QueryRow(`SELECT id FROM recipe_drafts ORDER BY id LIMIT 1`).Scan(&draft)
	// a stored export (and a playlist zip when the rig can build one) must be swept too: "never in exports" is the rule
	exp := obj(t, human.OK(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{track}}}, "Idempotency-Key", "canary-export"))
	exportID, _ := exp["id"].(string)
	if exportID == "" {
		t.Fatalf("export_jsonl: %v", exp)
	}
	for _, text := range c.texts() {
		if strings.Contains(fmt.Sprint(exp), text) {
			t.Errorf("the JSONL export response carries the Muse canary %q", text)
		}
	}
	if pl := q1(r, `SELECT id FROM playlists ORDER BY id LIMIT 1`); pl != "" {
		if z := human.Do(t, "POST", "/api/playlists/"+pl+"/exports", nil, "Idempotency-Key", "canary-zip"); z.Status/100 == 2 {
			for _, text := range c.texts() {
				if strings.Contains(string(z.Body), text) {
					t.Errorf("the playlist export response carries the Muse canary %q", text)
				}
			}
		}
	}
	ids := map[string]string{
		"/api/tracks/": track, "/api/drafts/": draft, "/api/muses/": c.ID, "/api/songs/": c.SongID,
		"/api/batches/": q1(r, `SELECT id FROM batches ORDER BY id LIMIT 1`), "/api/jobs/": q1(r, `SELECT id FROM jobs ORDER BY id LIMIT 1`),
		"/api/duels/": q1(r, `SELECT id FROM duels ORDER BY id LIMIT 1`), "/api/imports/": q1(r, `SELECT id FROM import_runs ORDER BY id LIMIT 1`),
		"/api/playlists/": q1(r, `SELECT id FROM playlists ORDER BY id LIMIT 1`), "/api/exports/": exportID,
	}
	// {id} operations that the rig has no object for (the duel fixture is empty here); anything else must resolve
	// L2 {id} operations have no object in this rig (it holds no Records, Crates or tapes); lane INT-B's ops_l2_leaks_test.go
	// sweeps them with the L2 fixture and the canary Muse (C127.4).
	noObject := map[string]bool{"get_duel": true, "get_record": true, "get_record_changes": true, "get_crate": true, "get_tape": true}
	called := map[string]bool{}
	for _, op := range api.Operations(r.API) {
		if op.Method != "GET" || op.Meta.HumanOnly || op.Path == "/api/openapi.json" {
			continue
		}
		path := op.Path
		if strings.Contains(path, "{id}") {
			id := ""
			for prefix, v := range ids {
				if strings.HasPrefix(path, prefix) {
					id = v
				}
			}
			if id == "" {
				if !noObject[op.ID] {
					t.Errorf("%s (%s) has no object in the sweep rig: register its id or allowlist it", op.ID, path)
				}
				continue
			}
			path = strings.Replace(path, "{id}", id, 1)
		}
		if op.ID == "wait_for_job" || op.ID == "get_clip" || op.ID == "get_track_spectrogram" || op.ID == "get_media_links" {
			continue // blocking, binary or signed-URL routes: no Muse text and not what this test is about
		}
		for who, cl := range map[string]*servertest.Client{"human": human, "agent": agent} {
			resp := cl.Do(t, "GET", path, nil)
			called[op.ID] = true
			for _, text := range c.texts() {
				if strings.Contains(string(resp.Body), text) && !slices.Contains(museTextOps, op.ID) {
					t.Errorf("%s %s (%s, %s) leaks the Muse canary %q: %.200s", who, op.ID, path, op.Meta.Scope, text, resp.Body)
				}
			}
			if strings.Contains(string(resp.Body), c.Session) && !slices.Contains(sessionNameOps, op.ID) {
				t.Errorf("%s %s leaks the Session name canary", who, op.ID)
			}
		}
	}
	// the allowlisted operations really do show the canary (the sweep is not vacuous)
	for _, id := range []string{"list_muses", "get_muse", "get_song", "get_muse_board", "get_track_context"} {
		if !called[id] {
			t.Errorf("%s was not swept", id)
		}
	}
	if len(called) < 25 {
		t.Errorf("only %d operations swept", len(called))
	}
	for path, want := range map[string]string{
		"/api/muses/" + c.ID: c.Title, "/api/songs/" + c.SongID: c.Title, "/api/tracks/" + track + "/context": c.Title,
		"/api/sessions?limit=50": c.Session, "/api/board?limit=20": c.Title,
	} {
		if resp := agent.Do(t, "GET", path, nil); !strings.Contains(string(resp.Body), want) {
			t.Errorf("GET %s does not show %q: the allowlist is stale (%.200s)", path, want, resp.Body)
		}
	}
	// writes echo only where allowed: the response of assign_muse_songs carries no Muse text
	resp := human.Do(t, "POST", "/api/muses/"+c.ID+"/songs", map[string]any{"song_ids": []string{c.SongID}}, "Idempotency-Key", "canary-assign-2")
	for _, text := range c.texts() {
		if strings.Contains(string(resp.Body), text) {
			t.Errorf("assign_muse_songs echoes the Muse canary: %.200s", resp.Body)
		}
	}
}

func q1(r *servertest.Rig, q string) string {
	var s string
	_ = r.Store.R.QueryRow(q).Scan(&s)
	return s
}

// TestMuseOps_ErrorsCarryIDsOnly: every refusal of the Muse, Song and Session operations, driven with a canary Muse,
// answers a problem+json that names ids and codes and never Muse text.
func TestMuseOps_ErrorsCarryIDsOnly(t *testing.T) {
	t.Parallel()
	r, c := canaryRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	missing := "01900000-0000-7000-8000-00000000ffff"
	var rev int
	if err := r.Store.R.QueryRow(`SELECT revision FROM muses WHERE id = ?`, c.ID).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	type call struct {
		name, method, path string
		body               any
		cl                 *servertest.Client
		status             int
		code               string
	}
	calls := []call{
		{"create duplicate", "POST", "/api/muses", map[string]any{"kind": "reference", "title": c.Title, "artist": c.Artist}, agent, 409, "MUSE_EXISTS"},
		{"create invalid kind", "POST", "/api/muses", map[string]any{"kind": "bogus", "title": c.Title, "artist": c.Artist, "brief": c.Brief}, agent, 422, ""},
		{"update stale revision", "PATCH", "/api/muses/" + c.ID, map[string]any{"revision": rev + 7, "title": c.Title + " x"}, agent, 409, "REVISION_CONFLICT"},
		{"get unknown", "GET", "/api/muses/" + missing, nil, agent, 404, ""},
		{"update unknown", "PATCH", "/api/muses/" + missing, map[string]any{"revision": 1, "title": c.Title}, agent, 404, ""},
		{"assign unknown song", "POST", "/api/muses/" + c.ID + "/songs", map[string]any{"song_ids": []string{missing}}, agent, 422, "UNKNOWN_SONG"},
		{"assign to unknown muse", "POST", "/api/muses/" + missing + "/songs", map[string]any{"song_ids": []string{c.SongID}}, agent, 404, ""},
		{"update unknown song", "PATCH", "/api/songs/" + missing, map[string]any{"revision": 1, "title": c.Title}, agent, 404, ""},
		{"update song stale revision", "PATCH", "/api/songs/" + c.SongID, map[string]any{"revision": 99, "title": c.Brief}, agent, 409, "REVISION_CONFLICT"},
		{"update song unknown muse", "PATCH", "/api/songs/" + c.SongID, map[string]any{"revision": 99, "muse_id": missing}, agent, 0, ""},
		{"update session stale", "PATCH", "/api/sessions/" + q1(r, `SELECT id FROM sessions ORDER BY id LIMIT 1`), map[string]any{"revision": 99, "name": c.Session + " x"}, agent, 409, "REVISION_CONFLICT"},
		{"get unknown song", "GET", "/api/songs/" + missing, nil, agent, 404, ""},
		{"pin as agent", "PUT", "/api/songs/" + c.SongID + "/pin", map[string]any{"take_id": missing}, agent, 403, "HUMAN_ONLY"},
		{"propose unknown take", "POST", "/api/songs/" + c.SongID + "/pick-proposals", map[string]any{"take_id": missing, "rationale": c.Brief}, agent, 0, ""},
		{"map invalid", "POST", "/api/sound-terms", map[string]any{"mappings": []map[string]any{{"term": c.Title, "facet": "nope", "value": c.Brief, "op": "add"}}}, agent, 0, ""},
		{"board unknown muse", "GET", "/api/board?muse=" + missing, nil, agent, 0, ""},
	}
	// a hidden Muse refuses an assignment: hidden after the others ran (the hide is a successful write)
	hidden := func() call {
		human.OK(t, "PATCH", "/api/muses/"+c.ID, map[string]any{"revision": rev, "hidden": true})
		return call{"assign to hidden muse", "POST", "/api/muses/" + c.ID + "/songs", map[string]any{"song_ids": []string{c.SongID}}, agent, 409, "MUSE_HIDDEN"}
	}
	for i := 0; i <= len(calls); i++ {
		var k call
		if i == len(calls) {
			k = hidden()
		} else {
			k = calls[i]
		}
		resp := k.cl.Do(t, k.method, k.path, k.body, "Idempotency-Key", fmt.Sprintf("refusal-%d", i))
		if resp.Status/100 == 2 && k.status != 0 {
			t.Errorf("%s: want a refusal, got %d %.200s", k.name, resp.Status, resp.Body)
			continue
		}
		if k.status != 0 && resp.Status != k.status {
			t.Errorf("%s: status %d, want %d (%.200s)", k.name, resp.Status, k.status, resp.Body)
		}
		if k.code != "" && !strings.Contains(string(resp.Body), `"code":"`+k.code+`"`) {
			t.Errorf("%s: code %s missing from %.200s", k.name, k.code, resp.Body)
		}
		if resp.Status/100 == 2 {
			continue
		}
		for _, text := range append(c.texts(), c.Session) {
			if strings.Contains(string(resp.Body), text) {
				t.Errorf("%s: the problem body carries Muse text %q: %.300s", k.name, text, resp.Body)
			}
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "problem+json") {
			t.Errorf("%s: content type %q", k.name, resp.Header.Get("Content-Type"))
		}
	}
}

// TestIdem_CreateMuseCacheExpires: the stored create_muse response carries Muse text, so it must not outlive the
// idempotency window (C99.3): replayable inside 24 h, gone after the purge, never replayed afterwards.
func TestIdem_CreateMuseCacheExpires(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true})
	human := r.Human(t)
	body := map[string]any{"kind": "reference", "title": "Expiry Title " + randHex(t), "artist": "Expiry Artist"}
	first := human.Do(t, "POST", "/api/muses", body, "Idempotency-Key", "exp-1")
	if first.Status != 201 && first.Status != 200 {
		t.Fatalf("create_muse: %d %s", first.Status, first.Body)
	}
	rows := func() int {
		var n int
		if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM idempotency WHERE key = 'exp-1'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	r.Clock.Advance(23 * time.Hour)
	again := human.Do(t, "POST", "/api/muses", body, "Idempotency-Key", "exp-1")
	if again.Header.Get("Idempotent-Replayed") != "true" || string(again.Body) != string(first.Body) {
		t.Fatalf("inside the window the response replays: %d replayed=%q", again.Status, again.Header.Get("Idempotent-Replayed"))
	}
	r.Clock.Advance(3 * time.Hour) // past ExplicitTTL (24 h) plus the prune grace
	if rows() != 1 {
		t.Fatalf("the cached row must still exist before the purge")
	}
	n, err := r.Services.Idem.Prune(context.Background())
	if err != nil || n < 1 {
		t.Fatalf("prune: %d, %v", n, err)
	}
	if rows() != 0 {
		t.Errorf("the purge left the cached create_muse response")
	}
	late := human.Do(t, "POST", "/api/muses", body, "Idempotency-Key", "exp-1")
	if late.Header.Get("Idempotent-Replayed") == "true" || string(late.Body) == string(first.Body) {
		t.Errorf("an expired key must not replay the cached body: %d %.200s", late.Status, late.Body)
	}
}
