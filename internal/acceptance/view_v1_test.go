package acceptance_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// v1Rig is the fixture library with TrackJobChild (a leaf of TrackLuna's lineage) discarded by the Owner.
func v1Rig(t *testing.T) (r *servertest.Rig, owner, bob, carol *servertest.Client) {
	t.Helper()
	r = servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, Files: true})
	owner = r.Human(t)
	bob = r.User(t, servertest.UserEditor, config.RoleEditor)
	carol = r.User(t, servertest.UserListener, config.RoleListener)
	owner.OK(t, "PATCH", "/api/tracks/"+lt.TrackJobChild, map[string]any{"status": "discarded"})
	r.Settle(t)
	return r, owner, bob, carol
}

func jsonObj(b []byte) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func ids(page any) []string {
	var out []string
	for _, it := range arr(obj(page)["items"]) {
		out = append(out, obj(it)["id"].(string))
	}
	return out
}

// TestListenerDiscardedIs404 pins C308.2 by id: a discarded take answers a Listener 404 NOT_FOUND on every by-id read,
// with the same body as an id that does not exist; the Owner and the Editor still read it.
func TestListenerDiscardedIs404(t *testing.T) {
	t.Parallel()
	_, owner, bob, carol := v1Rig(t)
	d := lt.TrackJobChild
	const missing = "01900000-0000-7000-8000-0000000fffff"
	reads := []string{
		"/api/tracks/%s", "/api/tracks/%s/peaks", "/api/tracks/%s/media", "/api/tracks/%s/analysis", "/api/tracks/%s/spectrogram",
		"/api/tracks/%s/download", "/api/tracks/%s/clip", "/api/tracks/%s/lineage",
	}
	for _, pat := range reads {
		path := strings.Replace(pat, "%s", d, 1)
		got := carol.Do(t, "GET", path, nil)
		want := carol.Do(t, "GET", strings.Replace(pat, "%s", missing, 1), nil)
		if got.Status != 404 || want.Status != 404 {
			t.Errorf("%s: discarded %d, missing %d; both want 404", pat, got.Status, want.Status)
			continue
		}
		if g, w := string(got.Body), string(want.Body); g != w {
			t.Errorf("%s: a discarded id is distinguishable from a missing one:\n%s\n%s", pat, g, w)
		}
		if resp := owner.Do(t, "GET", path, nil); resp.Status == 404 {
			t.Errorf("%s: the Owner lost the discarded take", pat)
		}
		if resp := bob.Do(t, "GET", path, nil); resp.Status == 404 {
			t.Errorf("%s: the Editor lost the discarded take", pat)
		}
	}
	if resp := carol.Do(t, "GET", "/api/compare?a="+lt.TrackLuna+"&b="+d, nil); resp.Status != 404 {
		t.Errorf("compare with a discarded b: %d", resp.Status)
	}
	if resp := carol.Do(t, "GET", "/api/compare?a="+d+"&b="+lt.TrackLuna, nil); resp.Status != 404 {
		t.Errorf("compare with a discarded a: %d", resp.Status)
	}
	if resp := owner.Do(t, "GET", "/api/compare?a="+lt.TrackLuna+"&b="+d, nil); resp.Status != 200 {
		t.Errorf("the Owner's compare: %d %s", resp.Status, resp.Body)
	}
	if resp := carol.Do(t, "PUT", "/api/tracks/"+d+"/rating", map[string]any{"stars": 4}); resp.Status != 404 {
		t.Errorf("rating a discarded take: %d", resp.Status)
	}
	evs := []map[string]any{{"id": "01900000-0000-7000-8000-00000000a001", "track_id": d, "session_id": "carol-session-1", "kind": "start", "client_at": lt.FixedNow.Format("2006-01-02T15:04:05Z07:00")}}
	body := string(carol.Do(t, "POST", "/api/listen-events", map[string]any{"events": evs}).Body)
	if !strings.Contains(body, "unknown_track") {
		t.Errorf("a play of a discarded take is not an unknown track: %s", body)
	}
}

// TestListenerDiscardedNeverListed pins C308.2 in lists, counts and trees.
func TestListenerDiscardedNeverListed(t *testing.T) {
	t.Parallel()
	r, owner, bob, carol := v1Rig(t)
	d := lt.TrackJobChild
	has := func(l []string) bool {
		for _, id := range l {
			if id == d {
				return true
			}
		}
		return false
	}
	// search, with and without an explicit status filter
	for _, q := range []string{"", "?status=discarded", "?status=discarded&status=draft&status=final&status=to_relisten", "?q=luna"} {
		if has(ids(carol.OK(t, "GET", "/api/tracks"+q, nil))) {
			t.Errorf("search %q lists the discarded take to a Listener", q)
		}
	}
	if !has(ids(owner.OK(t, "GET", "/api/tracks?status=discarded", nil))) || !has(ids(bob.OK(t, "GET", "/api/tracks?status=discarded", nil))) {
		t.Errorf("the Owner and the Editor must still list the discarded take")
	}
	if got := obj(carol.OK(t, "GET", "/api/tracks?status=discarded", nil))["total"]; got != float64(0) {
		t.Errorf("status=discarded total for a Listener: %v", got)
	}
	// facets: status, style, tag, stars counts
	statuses := func(c *servertest.Client) map[string]float64 {
		out := map[string]float64{}
		for _, s := range arr(obj(c.OK(t, "GET", "/api/library/facets", nil))["statuses"]) {
			out[obj(s)["status"].(string)] = obj(s)["count"].(float64)
		}
		return out
	}
	if n := statuses(carol)["discarded"]; n != 0 {
		t.Errorf("facet status count reveals %v discarded takes", n)
	}
	if n := statuses(owner)["discarded"]; n != 1 {
		t.Errorf("the Owner's facet count: %v", n)
	}
	// the lineage of the parent and the siblings of the discarded take's sibling
	for _, n := range arr(obj(carol.OK(t, "GET", "/api/tracks/"+lt.TrackLuna+"/lineage", nil))["nodes"]) {
		if obj(n)["id"] == d {
			t.Errorf("the lineage tree shows the discarded take to a Listener")
		}
	}
	found := false
	for _, n := range arr(obj(owner.OK(t, "GET", "/api/tracks/"+lt.TrackLuna+"/lineage", nil))["nodes"]) {
		found = found || obj(n)["id"] == d
	}
	if !found {
		t.Errorf("the Owner's lineage tree lost the discarded take")
	}
	for _, s := range arr(obj(obj(carol.OK(t, "GET", "/api/tracks/"+lt.TrackLuna, nil))["lineage"])["siblings"]) {
		if obj(s)["id"] == d {
			t.Errorf("siblings show the discarded take to a Listener")
		}
	}
	// a playlist and a crate that hold it
	pl := obj(owner.OK(t, "POST", "/api/playlists", map[string]any{"name": "Mixed", "track_ids": []string{lt.TrackLuna, d}}, "Idempotency-Key", "v1-playlist"))
	plID := pl["id"].(string)
	if n := len(arr(obj(owner.OK(t, "GET", "/api/playlists/"+plID, nil))["items"])); n != 2 {
		t.Fatalf("the Owner's playlist has %d items", n)
	}
	cp := obj(carol.OK(t, "GET", "/api/playlists/"+plID, nil))
	if items := arr(cp["items"]); len(items) != 1 || obj(obj(items[0])["track"])["id"] != lt.TrackLuna || obj(items[0])["position"] != float64(1) {
		t.Errorf("a Listener's playlist items: %v", cp["items"])
	}
	for _, it := range arr(obj(carol.OK(t, "GET", "/api/playlists", nil))["items"]) {
		if obj(it)["id"] == plID && obj(it)["items_count"] != float64(1) {
			t.Errorf("a Listener's playlist count: %v", it)
		}
	}
	cr := obj(owner.OK(t, "POST", "/api/crates", map[string]any{"name": "Takes", "kind": "manual", "subject": "take", "items": []map[string]any{{"track_id": lt.TrackLuna}, {"track_id": d}}}, "Idempotency-Key", "v1-crate"))
	crID := obj(cr["crate"])["id"]
	if crID == nil {
		crID = cr["id"]
	}
	if got := ids(carol.OK(t, "GET", "/api/tracks?crate_id="+crID.(string), nil)); has(got) || len(got) != 1 {
		t.Errorf("a Listener's crate members: %v", got)
	}
	// the Song's take count
	var song string
	if err := r.Store.R.QueryRow(`SELECT song_id FROM take_songs WHERE track_id = ?`, d).Scan(&song); err != nil {
		t.Fatal(err)
	}
	takes := func(c *servertest.Client) float64 {
		for _, it := range arr(obj(c.OK(t, "GET", "/api/songs?limit=100", nil))["items"]) {
			if obj(it)["id"] == song {
				return obj(it)["takes"].(float64)
			}
		}
		t.Fatalf("song %s not listed", song)
		return 0
	}
	if o, l := takes(owner), takes(carol); l != o-1 {
		t.Errorf("a Song's take count: Owner %v, Listener %v", o, l)
	}
	sv := obj(carol.OK(t, "GET", "/api/songs/"+song, nil))
	for _, c := range arr(sv["takes"]) {
		if obj(c)["id"] == d {
			t.Errorf("get_song lists the discarded take to a Listener")
		}
	}
	if resp := carol.Do(t, "GET", "/api/songs/"+d+"?by=track", nil); resp.Status != 404 {
		t.Errorf("get_song by a discarded take: %d", resp.Status)
	}
	if resp := carol.Do(t, "GET", "/api/songs?track_id="+d, nil); resp.Status != 200 || len(arr(jsonObj(resp.Body)["items"])) != 0 {
		t.Errorf("list_songs by a discarded take: %d %s", resp.Status, resp.Body)
	}
}

// TestListenerQueryOracles pins C308.3b: the Listener's q reads title and lyrics only, and the filters that read hidden data
// are refused with 422 FILTER_NOT_ALLOWED.
func TestListenerQueryOracles(t *testing.T) {
	t.Parallel()
	_, owner, bob, carol := v1Rig(t)
	// "ambient" is a style term of two fixture takes and in neither title nor lyrics
	if n := obj(owner.OK(t, "GET", "/api/tracks?q=ambient", nil))["total"].(float64); n == 0 {
		t.Fatalf("the Owner finds no take by style: the oracle test is vacuous")
	}
	if n := obj(bob.OK(t, "GET", "/api/tracks?q=ambient", nil))["total"].(float64); n == 0 {
		t.Errorf("the Editor lost style search")
	}
	if n := obj(carol.OK(t, "GET", "/api/tracks?q=ambient", nil))["total"].(float64); n != 0 {
		t.Errorf("a Listener's q matches style (%v hits)", n)
	}
	if n := obj(carol.OK(t, "GET", "/api/tracks?q=fx-luna", nil))["total"].(float64); n != 0 {
		t.Errorf("a Listener's q matches the request id (%v hits)", n)
	}
	if n := obj(carol.OK(t, "GET", "/api/tracks?q=luna", nil))["total"].(float64); n == 0 {
		t.Errorf("a Listener's q no longer matches titles")
	}
	for _, q := range []string{
		"/api/tracks?has_open_comments=true", "/api/tracks?session_id=x", "/api/tracks?origin=job", "/api/tracks?engine=yue2",
		"/api/tracks?root_track_id=" + lt.TrackLuna, "/api/tracks?lyrics_of=" + lt.TrackLuna, "/api/tracks?unheard=true",
		"/api/songs?muse_id=x", "/api/songs?angle=x", "/api/songs?session_id=x", "/api/songs?queued=true", "/api/songs?has_open_proposals=true",
		"/api/songs?draft_id=x", "/api/sounds/search?muse_id=x", "/api/atlas?angle=x", "/api/atlas?color_by=muse",
	} {
		resp := carol.Do(t, "GET", q, nil)
		if resp.Status != 422 || !strings.Contains(string(resp.Body), "FILTER_NOT_ALLOWED") {
			t.Errorf("%s for a Listener: %d %s", q, resp.Status, resp.Body)
		}
		if resp := owner.Do(t, "GET", q, nil); resp.Status == 422 && strings.Contains(string(resp.Body), "FILTER_NOT_ALLOWED") {
			t.Errorf("%s refused to the Owner", q)
		}
		if resp := bob.Do(t, "GET", q, nil); resp.Status == 422 && strings.Contains(string(resp.Body), "FILTER_NOT_ALLOWED") {
			t.Errorf("%s refused to the Editor", q)
		}
	}
}

// TestListenerDiscardedRecordSlot pins C308.2 for Records: a slot pinned to a take that is later discarded is an empty
// slot for a Listener (no take, no id anywhere in the body) and a pin_ineligible one for the Owner.
func TestListenerDiscardedRecordSlot(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, Files: true})
	owner := r.Human(t)
	carol := r.User(t, servertest.UserListener, config.RoleListener)
	r.Settle(t)
	d := lt.TrackJobChild
	var song string
	if err := r.Store.R.QueryRow(`SELECT song_id FROM take_songs WHERE track_id = ?`, d).Scan(&song); err != nil {
		t.Fatal(err)
	}
	rec := obj(owner.OK(t, "POST", "/api/records", map[string]any{"title": "Side", "song_ids": []string{song}}, "Idempotency-Key", "v1-record"))
	rid := obj(rec["record"])["id"].(string)
	slot := obj(arr(rec["slots"])[0])
	slotID := obj(slot["slot"])["id"]
	if slotID == nil {
		slotID = slot["id"]
	}
	rev := obj(rec["record"])["revision"].(float64)
	owner.OK(t, "POST", "/api/records/"+rid+"/slots", map[string]any{"revision": rev, "ops": []any{map[string]any{"op": "pin", "slot_id": slotID, "take_id": d}}})
	owner.OK(t, "PATCH", "/api/tracks/"+d, map[string]any{"status": "discarded"})

	ob := string(owner.Do(t, "GET", "/api/records/"+rid, nil).Body)
	if !strings.Contains(ob, d) || !strings.Contains(ob, "pin_ineligible") {
		t.Fatalf("the Owner no longer sees the pinned discarded take: %s", ob)
	}
	cb := string(carol.Do(t, "GET", "/api/records/"+rid, nil).Body)
	if strings.Contains(cb, d) {
		t.Errorf("a Listener's record names the discarded take: %s", cb)
	}
	if !strings.Contains(cb, `"empty"`) || strings.Contains(cb, "pin_ineligible") {
		t.Errorf("a Listener's slot is not empty: %s", cb)
	}
	if body := string(carol.Do(t, "GET", "/api/records/"+rid+"/changes", nil).Body); strings.Contains(body, d) {
		t.Errorf("a Listener's changelog names the discarded take: %s", body)
	}
}

// TestListenerLineageHiddenRoot pins the tree rule of C308.2: when the root of a lineage is discarded, a Listener's tree
// starts at the topmost visible ancestor of the requested take and never names the root.
func TestListenerLineageHiddenRoot(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, Files: true})
	owner := r.Human(t)
	carol := r.User(t, servertest.UserListener, config.RoleListener)
	owner.OK(t, "PATCH", "/api/tracks/"+lt.TrackLuna, map[string]any{"status": "discarded"})
	tree := obj(carol.OK(t, "GET", "/api/tracks/"+lt.TrackJobChild+"/lineage", nil))
	nodes := arr(tree["nodes"])
	if len(nodes) != 1 || obj(nodes[0])["id"] != lt.TrackJobChild || tree["root_id"] != lt.TrackJobChild {
		t.Errorf("a Listener's tree under a discarded root: %v", tree)
	}
	if strings.Contains(string(carol.Do(t, "GET", "/api/tracks/"+lt.TrackJobChild+"/lineage", nil).Body), lt.TrackLuna) {
		t.Errorf("the tree names the discarded root")
	}
	if resp := carol.Do(t, "GET", "/api/tracks/"+lt.TrackLuna+"/lineage", nil); resp.Status != 404 {
		t.Errorf("lineage by the discarded root: %d", resp.Status)
	}
	full := arr(obj(owner.OK(t, "GET", "/api/tracks/"+lt.TrackJobChild+"/lineage", nil))["nodes"])
	if len(full) != 2 {
		t.Errorf("the Owner's tree: %d nodes", len(full))
	}
}
