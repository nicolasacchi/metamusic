package api_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

const morningCrateID = "01900000-0000-7000-8000-00000000ac01"

var morningTakes = []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA}

// seedMorningCrate writes a ready, unrevealed crate of three 5 s chapters on one side over fixture takes (no media tools needed).
func seedMorningCrate(t *testing.T, r *servertest.Rig) {
	t.Helper()
	ts := "2026-11-22T07:00:00.000Z"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := r.Store.W.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at,built_at,takes,anchors,duration_ms)
		VALUES(?,'scheduled','ready',7,'{}','system:morning',?,?,3,0,19500)`, morningCrateID, ts, ts)
	for i, tk := range morningTakes {
		start := int64(i) * 312000
		exec(`INSERT INTO morning_crate_items(crate_id,n,side_index,track_id,anchor,source,start_frame,audio_start_frame,end_frame,gain_cdb,limited)
			VALUES(?,?,0,?,0,'job',?,?,?,-120,0)`, morningCrateID, i+1, tk, start, start+72000, start+312000)
	}
	exec(`INSERT INTO morning_crate_sides(crate_id,side_index,sha256,path,bytes,samples) VALUES(?,0,?,?,100,936000)`,
		morningCrateID, strings.Repeat("ab", 32), "derived/morning/ab/"+strings.Repeat("ab", 32)+".flac")
}

func morningEvents(evs ...map[string]any) map[string]any { return map[string]any{"events": evs} }

func mev(i int, chapter int, kind string, sideMS int, stars any) map[string]any {
	e := map[string]any{"id": fmt.Sprintf("01900000-0000-7000-8000-0000000e%04x", i), "side": 0, "chapter": chapter, "kind": kind, "side_ms": sideMS,
		"client_at": "2026-11-22T07:05:00Z"}
	if stars != nil {
		e["stars"] = stars
	}
	return e
}

// TestMorningOps_RoleMatrix: the seven operations carry the C365 rows (Owner-only, the two person-only ones off MCP) and answer by role.
func TestMorningOps_RoleMatrix(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true})
	r.Settle(t)
	seedMorningCrate(t, r)
	want := map[string]struct {
		method, path, mcp, ui string
		scope                 auth.Scope
	}{
		"get_morning_crate_settings":    {"GET", "/api/morning-crates/settings", "generic", "/morning", auth.ScopeRead},
		"update_morning_crate_settings": {"PUT", "/api/morning-crates/settings", "generic", "/morning", auth.ScopeWrite},
		"list_morning_crates":           {"GET", "/api/morning-crates", "generic", "/morning", auth.ScopeRead},
		"get_morning_crate":             {"GET", "/api/morning-crates/{id}", "generic", "/morning/:id", auth.ScopeRead},
		"build_morning_crate":           {"POST", "/api/morning-crates", "generic", "/morning", auth.ScopeWrite},
		"submit_morning_crate_events":   {"POST", "/api/morning-crates/{id}/events", "exclude:person-only", "/morning/:id", auth.ScopeWrite},
		"reveal_morning_crate":          {"POST", "/api/morning-crates/{id}/reveal", "exclude:person-only", "/morning/:id", auth.ScopeWrite},
	}
	seen := 0
	for _, op := range api.Operations(r.API) {
		w, ok := want[op.ID]
		if !ok {
			continue
		}
		seen++
		m := op.Meta
		if op.Method != w.method || op.Path != w.path || m.MCP != w.mcp || m.UI != w.ui || m.Scope != w.scope || m.Cost != "free" || !m.OwnerOnly || m.HumanOnly {
			t.Errorf("%s: %s %s %+v", op.ID, op.Method, op.Path, m)
		}
	}
	if seen != len(want) {
		t.Fatalf("%d of %d operations registered", seen, len(want))
	}
	owner := r.Human(t)
	dev := r.Device(t)
	bob, carol := r.User(t, "bob", "editor"), r.User(t, "carol", "listener")
	reader, _ := r.Agent(t, auth.ScopeRead)
	writer, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	for name, c := range map[string]*servertest.Client{"owner": owner, "device": dev, "agent": reader} {
		for _, p := range []string{"/api/morning-crates", "/api/morning-crates/settings", "/api/morning-crates/" + morningCrateID} {
			if resp := c.Do(t, "GET", p, nil); resp.Status != 200 {
				t.Errorf("%s GET %s: %d %s", name, p, resp.Status, resp.Body)
			}
		}
	}
	for _, c := range map[string]*servertest.Client{"editor": bob, "listener": carol} {
		for _, p := range []string{"/api/morning-crates", "/api/morning-crates/settings", "/api/morning-crates/" + morningCrateID} {
			status(t, c.Do(t, "GET", p, nil), 403, "OWNER_ONLY")
		}
		status(t, c.Do(t, "POST", "/api/morning-crates/"+morningCrateID+"/reveal", nil), 403, "OWNER_ONLY")
		status(t, c.Do(t, "POST", "/api/morning-crates/"+morningCrateID+"/events", morningEvents(mev(1, 1, "marker", 2500, nil))), 403, "OWNER_ONLY")
	}
	// an agent reads and builds but never rates or reveals
	status(t, writer.Do(t, "POST", "/api/morning-crates/"+morningCrateID+"/reveal", nil), 403, "HUMAN_ONLY")
	status(t, writer.Do(t, "POST", "/api/morning-crates/"+morningCrateID+"/events", morningEvents(mev(1, 1, "marker", 2500, nil))), 403, "HUMAN_ONLY")
	status(t, reader.Do(t, "POST", "/api/morning-crates/"+morningCrateID+"/reveal", nil), 403, "")
	if got := scalar(t, r, `SELECT COUNT(*) FROM morning_crate_events`); got != "0" {
		t.Fatalf("refused callers wrote %s events", got)
	}
}

// TestMorningOps_GETNeverWrites: every GET leaves every table untouched, before and after the reveal.
func TestMorningOps_GETNeverWrites(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true})
	r.Settle(t)
	seedMorningCrate(t, r)
	owner := r.Human(t)
	count := func() string {
		return scalar(t, r, `SELECT (SELECT count(*) FROM morning_crates) || '/' || (SELECT count(*) FROM morning_crate_events) || '/' || (SELECT count(*) FROM comments) || '/' ||
			(SELECT count(*) FROM ratings) || '/' || (SELECT count(*) FROM listen_events) || '/' || (SELECT count(*) FROM playlists) || '/' || (SELECT count(*) FROM audit_log WHERE method <> 'GET')`)
	}
	get := func() {
		for _, p := range []string{"/api/morning-crates", "/api/morning-crates/settings", "/api/morning-crates/" + morningCrateID} {
			if resp := owner.Do(t, "GET", p, nil); resp.Status != 200 {
				t.Fatalf("GET %s: %d %s", p, resp.Status, resp.Body)
			}
		}
	}
	before := count()
	get()
	if after := count(); after != before {
		t.Fatalf("GETs wrote: %s -> %s", before, after)
	}
	status(t, owner.Do(t, "POST", "/api/morning-crates/"+morningCrateID+"/events", morningEvents(mev(1, 1, "strong_marker", 2500, nil))), 200, "")
	status(t, owner.Do(t, "POST", "/api/morning-crates/"+morningCrateID+"/reveal", nil), 200, "")
	before = count()
	get()
	if after := count(); after != before {
		t.Fatalf("GETs wrote after the reveal: %s -> %s", before, after)
	}
}

// TestMorningOps_RevealREST walks the Owner's morning: blind view, events (with a rejected one and a replay), the reveal, the view
// that now names the takes, the playlist, and the comment, rating and listen rows; the playlist reaches a Subsonic client only then.
func TestMorningOps_RevealREST(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true})
	r.Settle(t)
	seedMorningCrate(t, r)
	owner := r.Human(t)
	dev := r.Device(t)
	reader, _ := r.Agent(t, auth.ScopeRead)
	listener := r.SubsonicClientFor(t, "carol", subsonictest.ClientOpts{})
	crate := "/api/morning-crates/" + morningCrateID
	playlistNames := func(c *subsonictest.Client) string {
		resp := c.Call(t, "getPlaylists", url.Values{}, subsonictest.CallOpts{JSON: true})
		return string(resp.Body)
	}

	for name, c := range map[string]*servertest.Client{"owner": owner, "agent": reader} {
		body := string(c.Do(t, "GET", crate, nil).Body)
		for _, secret := range []string{"Twin A", "Luna", "root_track_id", lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s sees %q before the reveal: %s", name, secret, body)
			}
		}
	}
	if strings.Contains(playlistNames(listener), "Morning crate") {
		t.Fatal("a Subsonic client lists the crate playlist before the reveal")
	}

	resp := owner.Do(t, "POST", crate+"/events", morningEvents(
		mev(1, 1, "marker", 2500, nil), mev(2, 2, "skip", 9000, nil), mev(3, 3, "rate", 15000, 4), mev(4, 9, "marker", 0, nil)))
	status(t, resp, 200, "")
	body := obj(t, resp.JSON(t))
	rej, _ := body["rejected"].([]any)
	if body["accepted"] != float64(3) || len(rej) != 1 || obj(t, rej[0])["code"] != "UNKNOWN_CHAPTER" || body["revealed"] != false {
		t.Fatalf("events answer: %s", resp.Body)
	}
	if got := scalar(t, r, `SELECT COUNT(*) FROM comments WHERE source = 'headset'`); got != "0" {
		t.Fatalf("headset comments before the reveal: %s", got)
	}
	if got := string(owner.Do(t, "GET", crate, nil).Body); !strings.Contains(got, `"stars":4`) || strings.Contains(got, "Luna") {
		t.Fatalf("blind view after events: %s", got)
	}

	rv := dev.Do(t, "POST", crate+"/reveal", nil)
	status(t, rv, 200, "")
	c := obj(t, rv.JSON(t))
	if c["revealed"] != true || c["playlist_id"] == nil {
		t.Fatalf("revealed crate: %s", rv.Body)
	}
	chapters := c["chapters"].([]any)
	if obj(t, obj(t, chapters[0])["track"])["id"] != lt.TrackLuna || obj(t, chapters[0])["gain_db"] != -1.2 {
		t.Fatalf("chapter 1 after reveal: %v", chapters[0])
	}
	if got := scalar(t, r, `SELECT at_second || '/' || kind FROM comments WHERE source = 'headset'`); got != "1.0/marker" {
		t.Fatalf("headset comment = %s", got)
	}
	if got := scalar(t, r, `SELECT stars FROM ratings WHERE track_id = '`+lt.TrackTwinA+`' AND source = 'human' AND rater = 'owner'`); got != "4" {
		t.Fatalf("rating = %s", got)
	}
	if got := scalar(t, r, `SELECT kind FROM listen_events WHERE id = '01900000-0000-7000-8000-0000000e0002'`); got != "skip" {
		t.Fatalf("listen event = %s", got)
	}
	if !strings.Contains(playlistNames(listener), "Morning crate 2026-11-22") {
		t.Fatalf("the Listener's Subsonic client does not list the revealed playlist: %s", playlistNames(listener))
	}
	// revealing again changes nothing
	status(t, owner.Do(t, "POST", crate+"/reveal", nil), 200, "")
	if got := scalar(t, r, `SELECT COUNT(*) FROM playlists WHERE created_by = 'system:morning'`); got != "1" {
		t.Fatalf("playlists = %s", got)
	}
	// the crate itself stays the Owner's: a Listener still gets nothing from it
	status(t, r.User(t, "carol", "listener").Do(t, "GET", crate, nil), 403, "OWNER_ONLY")
}

// TestMorningOps_Settings_Build covers the settings round trip and the 202 of a build with nothing to build.
func TestMorningOps_SettingsAndBuild(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true})
	r.Settle(t)
	owner := r.Human(t)
	writer, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	status(t, writer.Do(t, "PUT", "/api/morning-crates/settings", map[string]any{"minutes": 30, "at": "06:30"}), 200, "")
	got := obj(t, owner.Do(t, "GET", "/api/morning-crates/settings", nil).JSON(t))
	if got["minutes"] != float64(30) || got["at"] != "06:30" || got["max_takes"] != float64(14) {
		t.Fatalf("settings %v", got)
	}
	status(t, owner.Do(t, "PUT", "/api/morning-crates/settings", map[string]any{"minutes": 30}), 200, "")
	status(t, owner.Do(t, "PUT", "/api/morning-crates/settings", map[string]any{"anchors": 2, "side_minutes": 5, "max_takes": 1}), 200, "")
	status(t, owner.Do(t, "POST", "/api/morning-crates", map[string]any{}), 428, "IDEMPOTENCY_KEY_REQUIRED") // no Idempotency-Key
	b := owner.Do(t, "POST", "/api/morning-crates", map[string]any{"include_track_ids": []string{"nope"}}, "Idempotency-Key", "mk-1")
	status(t, b, 422, "UNKNOWN_TRACK")
}

func scalar(t *testing.T, r *servertest.Rig, q string, args ...any) string {
	t.Helper()
	var v any
	if err := r.Store.R.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return fmt.Sprint(v)
}
