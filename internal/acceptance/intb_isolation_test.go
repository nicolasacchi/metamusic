package acceptance_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// ratingOf returns a track's rating block as one role reads it.
func ratingOf(t *testing.T, c *servertest.Client, id string) map[string]any {
	t.Helper()
	return obj(obj(c.OK(t, "GET", "/api/tracks/"+id, nil))["rating"])
}

func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }

func dbRatings(t *testing.T, w *intbWorld, id string) map[string]int {
	t.Helper()
	rows, err := w.R.Store.R.Query(`SELECT rater, stars FROM ratings WHERE track_id = ? AND source = 'human'`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var r string
		var s int
		if err := rows.Scan(&r, &s); err != nil {
			t.Fatal(err)
		}
		out[r] = s
	}
	return out
}

// TestIntBRatingShapes (D-U1-16, C306.4, C314.5): the Owner, the Editor, the Listener, an Editor's and a Listener's phone and
// an agent each read one track that three users rated with canary verdicts, and each gets exactly their own shape.
func TestIntBRatingShapes(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	id := w.ratedTrack(t) // alice 4, bob 2, carol 5, each with a canary verdict
	bobPhone, carolPhone := w.R.DeviceFor(t, "bob"), w.R.DeviceFor(t, "carol")
	mine := func(r map[string]any) float64 { m := obj(r["mine"]); s, _ := m["stars"].(float64); return s }

	o := ratingOf(t, w.Own, id)
	if mine(o) != 4 || !hasKey(o, "others") || !hasKey(o, "average") || !hasKey(o, "critic") {
		t.Errorf("Owner: %v", o)
	}
	var users []string
	for _, x := range o["others"].([]any) {
		users = append(users, fmt.Sprint(obj(x)["user"]))
	}
	if len(users) != 2 || !strings.Contains(strings.Join(users, ","), "bob") || !strings.Contains(strings.Join(users, ","), "carol") {
		t.Errorf("the Owner does not see each user's row: %v", users)
	}
	if avg := obj(o["average"]); avg["n"] != float64(3) {
		t.Errorf("Owner average: %v", avg)
	}

	for name, c := range map[string]*servertest.Client{"editor": w.Bob, "editor phone": bobPhone} {
		r := ratingOf(t, c, id)
		body, _ := json.Marshal(r)
		if mine(r) != 2 || hasKey(r, "others") || !hasKey(r, "average") {
			t.Errorf("%s: %s", name, body)
		}
		for _, bad := range []string{"carol", "alice", "owner " + canaryVerdict, "carol " + canaryVerdict} {
			if strings.Contains(string(body), bad) {
				t.Errorf("%s sees %q: %s", name, bad, body)
			}
		}
	}
	for name, c := range map[string]*servertest.Client{"listener": w.Carol, "listener phone": carolPhone} {
		r := ratingOf(t, c, id)
		body, _ := json.Marshal(r)
		if mine(r) != 5 || hasKey(r, "others") || hasKey(r, "average") {
			t.Errorf("%s: %s", name, body)
		}
		for _, bad := range []string{"bob", "alice", "owner " + canaryVerdict, "bob " + canaryVerdict} {
			if strings.Contains(string(body), bad) {
				t.Errorf("%s sees %q: %s", name, bad, body)
			}
		}
		// the answer to a rating write has the same shape
		put := string(c.Do(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 5, "verdict": "carol " + canaryVerdict}).Body)
		if strings.Contains(put, "average") || strings.Contains(put, "others") {
			t.Errorf("%s rate_track answers with %s", name, put)
		}
	}

	// Subsonic: a Listener's client reports its own userRating and never an average; an Editor's client its own
	for user, want := range map[string]float64{"carol": 5, "bob": 2} {
		c := w.R.SubsonicClientFor(t, user, subsonictest.ClientOpts{Name: user + " rating app"})
		for _, js := range []bool{true, false} {
			resp := c.Call(t, "getSong", url.Values{"id": {id}}, subsonictest.CallOpts{JSON: js})
			if strings.Contains(string(resp.Body), "averageRating") && user == "carol" {
				t.Errorf("a Listener's client reports averageRating: %s", resp.Body)
			}
			if js {
				song := obj(obj(resp.Doc["subsonic-response"])["song"])
				if song == nil {
					song = obj(resp.Doc["song"])
				}
				if got, _ := song["userRating"].(float64); got != want {
					t.Errorf("%s's client userRating = %v, want %v: %s", user, song["userRating"], want, resp.Body)
				}
			}
			for _, bad := range []string{"alice", "owner " + canaryVerdict} {
				if strings.Contains(string(resp.Body), bad) {
					t.Errorf("%s's client sees %q", user, bad)
				}
			}
		}
	}

	// the agent rating sweep: a token sees the Owner row (human), the critic row (mine), the average and never a user's row
	agent, _ := w.R.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	canaries := []string{"bob " + canaryVerdict, "carol " + canaryVerdict, `"others"`, `"bob"`, `"carol"`}
	check := func(where string, body []byte) {
		for _, bad := range canaries {
			if strings.Contains(string(body), bad) {
				t.Errorf("%s: carries %s: %.300s", where, bad, body)
			}
		}
	}
	// the agent rates first, so its own (critic) row exists; get_track answers the Owner's row as human, the critic's as mine and
	// critic, no average (R1: never for an agent, amended in plan Revision 15) and no user's row
	rate := agent.Do(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 3, "verdict": "agent verdict"})
	check("REST rate_track", rate.Body)
	rt := obj(obj(agent.OK(t, "GET", "/api/tracks/"+id, nil))["rating"])
	for _, k := range []string{"human", "critic", "mine"} {
		if rt[k] == nil {
			t.Errorf("the agent's rating lacks %q: %v", k, rt)
		}
	}
	if hasKey(rt, "others") || hasKey(rt, "average") {
		t.Errorf("the agent sees others or the average: %v", rt)
	}
	if diff := cmp.Diff(rt["critic"], rt["mine"]); diff != "" {
		t.Errorf("the agent's mine is not its critic row: %s", diff)
	}
	if h := obj(rt["human"]); h["stars"] != float64(4) {
		t.Errorf("the agent's human row is not the Owner's: %v", h)
	}
	body, _ := json.Marshal(rt)
	check("REST get_track", body)
	sess := w.R.MCP(t, auth.ScopeRead, auth.ScopeWrite)
	for _, generic := range []bool{false, true} {
		res := rawTool(t, sess, "get_track", map[string]any{"id": id}, generic)
		b, _ := json.Marshal(res)
		check(fmt.Sprintf("MCP get_track generic=%v", generic), b)
		res = rawTool(t, sess, "rate_track", map[string]any{"id": id, "stars": 3, "verdict": "agent verdict"}, generic)
		b, _ = json.Marshal(res)
		check(fmt.Sprintf("MCP rate_track generic=%v", generic), b)
	}
	check("table audit_log", []byte(dumpTables(t, w.R.Store.R, "audit_log")["audit_log"]))
	for _, held := range intbSQLStrings(t, w.R, `SELECT CAST(response AS TEXT) FROM idempotency WHERE principal_key LIKE 'agent:%'`) {
		check("an agent's held response", []byte(held))
	}
	// and the DB agrees about whose rows are whose
	if got := dbRatings(t, w, id); got["owner"] != 4 || got["bob"] != 2 || got["carol"] != 5 {
		t.Errorf("rows by rater: %v", got)
	}
}

// TestIntBPerUserWrites: a rating made on a user's phone or Subsonic client is that user's, never the Owner's or another
// user's; the other users' rows do not move.
func TestIntBPerUserWrites(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	id := w.ratedTrack(t) // alice 4, bob 2, carol 5
	carolPhone, bobPhone := w.R.DeviceFor(t, "carol"), w.R.DeviceFor(t, "bob")
	carolSub := w.R.SubsonicClientFor(t, "carol", subsonictest.ClientOpts{Name: "carol write app", Write: true})
	bobSub := w.R.SubsonicClientFor(t, "bob", subsonictest.ClientOpts{Name: "bob write app", Write: true})
	carolPhone.OK(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 1})
	if got := dbRatings(t, w, id); got["carol"] != 1 || got["owner"] != 4 || got["bob"] != 2 {
		t.Errorf("a Listener's phone rating: %v", got)
	}
	bobPhone.OK(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 3})
	if got := dbRatings(t, w, id); got["bob"] != 3 || got["carol"] != 1 || got["owner"] != 4 {
		t.Errorf("an Editor's phone rating: %v", got)
	}
	if r := carolSub.Call(t, "setRating", url.Values{"id": {id}, "rating": {"2"}}, subsonictest.CallOpts{JSON: true}); r.Code != 0 {
		t.Fatalf("setRating: %s", r.Body)
	}
	if r := bobSub.Call(t, "setRating", url.Values{"id": {id}, "rating": {"5"}}, subsonictest.CallOpts{JSON: true}); r.Code != 0 {
		t.Fatalf("setRating: %s", r.Body)
	}
	if got := dbRatings(t, w, id); got["carol"] != 2 || got["bob"] != 5 || got["owner"] != 4 || len(got) != 3 {
		t.Errorf("Subsonic ratings: %v", got)
	}
	// each user still reads only their own
	if m := obj(ratingOf(t, w.Carol, id)["mine"]); m["stars"] != float64(2) {
		t.Errorf("carol reads %v", m)
	}
	if m := obj(ratingOf(t, w.Own, id)["mine"]); m["stars"] != float64(4) {
		t.Errorf("the Owner reads %v", m)
	}
}

// TestIntBOwnership: devices, Subsonic clients, exports and device profiles belong to the user who made them. A Listener or
// Editor lists and revokes only their own (a foreign id is 404), the Owner lists and revokes all.
func TestIntBOwnership(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	ownerPhone, _ := w.R.DeviceWithCookie(t, "owner phone")
	bobPhone, _ := w.R.DeviceForWithCookie(t, "bob", "bob phone")
	carolPhone, _ := w.R.DeviceForWithCookie(t, "carol", "carol phone")
	bobSub := w.R.SubsonicClientFor(t, "bob", subsonictest.ClientOpts{Name: "bob app"})
	carolSub := w.R.SubsonicClientFor(t, "carol", subsonictest.ClientOpts{Name: "carol app"})
	_ = ownerPhone

	listIDs := func(c *servertest.Client, path string) (ids []string, names []string) {
		for _, it := range arr(obj(c.OK(t, "GET", path, nil))["items"]) {
			ids = append(ids, fmt.Sprint(obj(it)["id"]))
			names = append(names, fmt.Sprint(obj(it)["name"]))
		}
		return
	}
	for _, kind := range []struct{ path, own, other string }{{"/human/api/devices", "carol phone", "bob phone"}, {"/human/api/subsonic-clients", "carol app", "bob app"}} {
		cids, cnames := listIDs(w.Carol, kind.path)
		bids, bnames := listIDs(w.Bob, kind.path)
		oids, onames := listIDs(w.Own, kind.path)
		if len(cids) != 1 || cnames[0] != kind.own || len(bids) != 1 || bnames[0] != kind.other {
			t.Errorf("%s: carol sees %v, bob sees %v", kind.path, cnames, bnames)
		}
		if len(oids) < 3 || !strings.Contains(strings.Join(onames, ","), kind.own) || !strings.Contains(strings.Join(onames, ","), kind.other) {
			t.Errorf("%s: the Owner sees %v", kind.path, onames)
		}
		// a foreign id is the 404 of a missing one, and nothing was revoked
		for _, other := range bids {
			if resp := w.Carol.Do(t, "POST", kind.path+"/"+other+"/revoke", nil); resp.Status != 404 {
				t.Errorf("carol revokes bob's entry on %s: %d %s", kind.path, resp.Status, resp.Body)
			}
		}
		if resp := w.Carol.Do(t, "POST", kind.path+"/"+oids[0]+"/revoke", nil); resp.Status != 404 && cids[0] != oids[0] {
			t.Errorf("carol revokes %s of the Owner's: %d", oids[0], resp.Status)
		}
	}
	if resp := bobPhone.Do(t, "GET", "/api/me", nil); resp.Status != 200 {
		t.Errorf("bob's phone lost its session to carol's attempts: %d", resp.Status)
	}
	if r := bobSub.Call(t, "ping", url.Values{}, subsonictest.CallOpts{JSON: true}); r.Code != 0 {
		t.Errorf("bob's Subsonic client lost its access: %s", r.Body)
	}
	// the Owner revokes carol's phone and client at once
	cids, _ := listIDs(w.Carol, "/human/api/devices")
	w.Own.OK(t, "POST", "/human/api/devices/"+cids[0]+"/revoke", nil)
	if resp := carolPhone.Do(t, "GET", "/api/me", nil); resp.Status != 401 {
		t.Errorf("a revoked phone still answers: %d", resp.Status)
	}
	sids, _ := listIDs(w.Carol, "/human/api/subsonic-clients")
	w.Own.OK(t, "POST", "/human/api/subsonic-clients/"+sids[0]+"/revoke", nil)
	if r := carolSub.Call(t, "ping", url.Values{}, subsonictest.CallOpts{JSON: true}); r.Code == 0 {
		t.Errorf("a revoked Subsonic client still answers")
	}

	// exports: an Editor's export is the Editor's; the Owner's is not the Editor's to read
	exp := func(c *servertest.Client, key string) string {
		e := obj(c.OK(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{w.Visible[0]}}}, "Idempotency-Key", key))
		return fmt.Sprint(e["id"])
	}
	bobExp, ownExp := exp(w.Bob, "intb-exp-bob"), exp(w.Own, "intb-exp-own")
	if w.Bob.Do(t, "GET", "/api/exports/"+bobExp, nil).Status != 200 || w.Own.Do(t, "GET", "/api/exports/"+bobExp, nil).Status != 200 {
		t.Errorf("an export is not readable by its maker and the Owner")
	}
	if resp := w.Bob.Do(t, "GET", "/api/exports/"+ownExp, nil); resp.Status != 404 {
		t.Errorf("an Editor reads the Owner's export: %d", resp.Status)
	}
	if resp := w.Carol.Do(t, "GET", "/api/exports/"+bobExp, nil); resp.Status != 403 {
		t.Errorf("a Listener reads an export: %d", resp.Status)
	}

	// device profiles: a row belongs to its creator's key
	prof := map[string]any{"label": "p", "user_agent": "ua", "quality": "auto", "quality_source": "default", "probe_result": "none", "flac_can_play": "", "probe_version": 0}
	for _, c := range []struct {
		who  *servertest.Client
		want int
	}{{w.Bob, 200}, {w.Carol, 404}, {w.Own, 200}} {
		if resp := c.who.Do(t, "PUT", "/api/device-profiles/intb-prof-00001", prof); resp.Status != c.want {
			t.Errorf("put_device_profile on the Editor's row: %d, want %d", resp.Status, c.want)
		}
	}
	if resp := w.Bob.Do(t, "PUT", "/api/device-profiles/intb-prof-00002", prof); resp.Status != 200 {
		t.Fatalf("bob: %d", resp.Status)
	}
	if resp := bobPhone.Do(t, "PUT", "/api/device-profiles/intb-prof-00002", prof); resp.Status != 200 {
		t.Errorf("an Editor's phone cannot update its user's profile row: %d", resp.Status)
	}
}

// TestIntBOwnerStateUntouched (C306.7, C307; D-U1-9): an Editor and a Listener rate every take and play every take through the
// browser, their paired phone and their Subsonic client, and nothing the Owner curates from moves: stats and histograms,
// Song state and picks, next actions, unheard, the track's listening block (heat, completion, last_played), the Atlas and
// the Owner's own stars.
func TestIntBOwnerStateUntouched(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	for _, user := range []string{servertest.UserListener, servertest.UserEditor} {
		for _, via := range []string{"browser", "phone", "subsonic"} {
			t.Run(user+" "+via, func(t *testing.T) {
				t.Parallel()
				r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, M4a: true, L1: true, L2: true, L3: true, Files: true})
				var ownerPID string
				if err := r.Store.R.QueryRow(`SELECT principal_id FROM listen_events WHERE principal_kind = 'human' LIMIT 1`).Scan(&ownerPID); err != nil {
					t.Fatal(err)
				}
				r.Store.SetOwner(strings.TrimPrefix(ownerPID, "human:"))
				r.Settle(t)
				var tracks []string
				rows, err := r.Store.R.Query(`SELECT id FROM tracks WHERE status <> 'discarded' ORDER BY id LIMIT 40`)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var id string
					if err := rows.Scan(&id); err != nil {
						t.Fatal(err)
					}
					tracks = append(tracks, id)
				}
				_ = rows.Close()
				owner := r.Human(t)
				var web *servertest.Client
				var sub *subsonictest.Client
				switch via {
				case "browser":
					web = r.User(t, user, roleOf(user))
				case "phone":
					web = r.DeviceFor(t, user)
				default:
					sub = r.SubsonicClientFor(t, user, subsonictest.ClientOpts{Name: user + " state app", Write: true})
				}
				views := func() map[string]any {
					out := map[string]any{}
					for _, p := range []string{"/api/stats", "/api/songs?limit=100", "/api/next-actions", "/api/atlas", "/api/library/facets",
						"/api/tracks?unheard=true&limit=100&status=draft&status=to_relisten&status=final", "/api/tracks?min_stars=1&limit=100"} {
						out[p] = owner.OK(t, "GET", p, nil)
					}
					for _, id := range tracks {
						tr := obj(owner.OK(t, "GET", "/api/tracks/"+id, nil))
						if rt := obj(tr["rating"]); rt != nil { // the per-user rating shape may change; the Owner's own row may not
							tr["rating"] = map[string]any{"human": rt["human"], "mine": rt["mine"], "critic": rt["critic"]}
						}
						out["track "+id] = tr
						out["heat "+id] = owner.OK(t, "GET", "/api/tracks/"+id+"/heat", nil)
					}
					b, _ := json.Marshal(out)
					var norm map[string]any
					_ = json.Unmarshal(b, &norm)
					if s := obj(norm["/api/stats"]); s != nil {
						delete(s, "generated_at")
					}
					return norm
				}
				before := views()
				var events0 int
				_ = r.Store.R.QueryRow(`SELECT COUNT(*) FROM listen_events`).Scan(&events0)
				at := lt.FixedNow
				for i, id := range tracks {
					if sub != nil {
						if resp := sub.Call(t, "setRating", url.Values{"id": {id}, "rating": {"5"}}, subsonictest.CallOpts{JSON: true}); resp.Code != 0 {
							t.Fatalf("setRating: %s", resp.Body)
						}
						sub.Call(t, "scrobble", url.Values{"id": {id}, "submission": {"true"}}, subsonictest.CallOpts{JSON: true})
						sub.Call(t, "scrobble", url.Values{"id": {id}, "submission": {"false"}}, subsonictest.CallOpts{JSON: true})
						continue
					}
					web.OK(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 5, "verdict": canaryNote})
					var evs []map[string]any
					for j, kind := range []string{"start", "complete"} {
						evs = append(evs, map[string]any{"id": fmt.Sprintf("0190cccc-0000-7000-8000-%012x", i*10+j), "track_id": id, "session_id": fmt.Sprintf("%s-%s-%d", user, via, i),
							"kind": kind, "client_at": at.Add(-30 * 1e9).Format("2006-01-02T15:04:05Z")})
					}
					if resp := web.Do(t, "POST", "/api/listen-events", map[string]any{"events": evs}); resp.Status/100 != 2 || strings.Contains(string(resp.Body), `"rejected":[{`) {
						t.Fatalf("listen events: %d %s", resp.Status, resp.Body)
					}
				}
				var n int
				if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM ratings WHERE rater = ?`, user).Scan(&n); err != nil || n != len(tracks) {
					t.Fatalf("%s wrote %d ratings (%v), want %d: the check would be vacuous", user, n, err, len(tracks))
				}
				var events1 int
				_ = r.Store.R.QueryRow(`SELECT COUNT(*) FROM listen_events`).Scan(&events1)
				if events1 <= events0 {
					t.Fatalf("%s via %s wrote no listen event (%d -> %d): the heat and unheard check would be vacuous", user, via, events0, events1)
				}
				if diff := cmp.Diff(before, views()); diff != "" {
					t.Errorf("%s via %s moved Owner-derived state (-before +after):\n%s", user, via, diff)
				}
			})
		}
	}
}

func roleOf(user string) config.Role {
	if user == servertest.UserEditor {
		return config.RoleEditor
	}
	return config.RoleListener
}

// ratedTrack is a visible take that the world's three users rated.
func (w *intbWorld) ratedTrack(tb testing.TB) string {
	tb.Helper()
	for _, id := range []string{lt.TrackCitta, lt.TrackTwinA, lt.TrackRefFlac} {
		if slices.Contains(w.Visible, id) {
			return id
		}
	}
	tb.Fatal("no visible rated take")
	return ""
}

// TestIntBHandOffs is the explicit row for the V1 to V2 hand-offs (Revision 13): a Song whose takes are all discarded is
// closed on every surface a Listener has, as the Listener's phone and Subsonic client too, and a smart Crate whose stored
// filter holds a hidden filter is refused on every operation that takes a crate_id without naming the stored key. The Owner and
// the Editor keep both.
func TestIntBHandOffs(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	phone := w.R.DeviceFor(t, "carol")
	sub := w.R.SubsonicClientFor(t, "carol", subsonictest.ClientOpts{Name: "carol hand-off app"})
	gone := w.GoneSong
	for name, c := range map[string]*servertest.Client{"browser": w.Carol, "phone": phone} {
		for _, p := range []string{"/api/songs?limit=100", "/api/sounds/search", "/api/atlas", "/api/songs?limit=100&include_hidden=true&include_archived=true"} {
			if body := string(c.Do(t, "GET", p, nil).Body); strings.Contains(body, gone) {
				t.Errorf("%s %s names the all-discarded Song", name, p)
			}
		}
		if r := c.Do(t, "GET", "/api/songs/"+gone, nil); r.Status != 404 {
			t.Errorf("%s get_song of it: %d", name, r.Status)
		}
		// as unknown as an id that was never a Song
		a := c.Do(t, "GET", "/api/sequence?song_id="+gone+"&song_id="+w.Songs[0], nil)
		b := c.Do(t, "GET", "/api/sequence?song_id=01900000-0000-7000-8000-00000000dead&song_id="+w.Songs[0], nil)
		if a.Status != b.Status || signature(a.Status, a.Body) != strings.ReplaceAll(signature(b.Status, b.Body), "01900000-0000-7000-8000-00000000dead", gone) {
			t.Errorf("%s suggest_sequence: the all-discarded Song answers %d %s, an unknown id %d %s", name, a.Status, a.Body, b.Status, b.Body)
		}
		for _, crate := range []struct{ id, path string }{{w.SmartBad, "/api/songs?crate_id="}, {w.SmartBad, "/api/sounds/search?crate_id="}, {w.SmartBad, "/api/atlas?crate_id="},
			{w.SmartBad, "/api/sequence?crate_id="}, {w.SmartBadTake, "/api/tracks?crate_id="}} {
			resp := c.Do(t, "GET", crate.path+crate.id, nil)
			if resp.Status != 422 || !strings.Contains(string(resp.Body), "FILTER_NOT_ALLOWED") || strings.Contains(string(resp.Body), "angle") || strings.Contains(string(resp.Body), "has_open_comments") {
				t.Errorf("%s %s: %d %s", name, crate.path, resp.Status, resp.Body)
			}
		}
	}
	// Subsonic: no folder, album, song or search names a take of the Song
	for _, id := range intbSQLStrings(t, w.R, `SELECT track_id FROM take_songs WHERE song_id = ?`, gone) {
		for _, ep := range []string{"getSong", "getAlbum", "stream", "download", "getCoverArt"} {
			for _, idp := range []string{id, "al-" + id} {
				if r := sub.Call(t, ep, url.Values{"id": {idp}}, subsonictest.CallOpts{JSON: true}); r.HTTP == 200 && r.Code == 0 {
					t.Errorf("Subsonic %s %s answers", ep, idp)
				}
			}
		}
	}
	// the Owner and the Editor keep the Song and the Crates
	for name, c := range map[string]*servertest.Client{"owner": w.Own, "editor": w.Bob} {
		if r := c.Do(t, "GET", "/api/songs/"+gone, nil); r.Status != 200 {
			t.Errorf("%s lost the all-discarded Song: %d", name, r.Status)
		}
		if r := c.Do(t, "GET", "/api/songs?crate_id="+w.SmartBad, nil); r.Status != 200 {
			t.Errorf("%s is refused a smart Crate: %d %s", name, r.Status, r.Body)
		}
	}
}
