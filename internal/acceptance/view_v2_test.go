package acceptance_test

import (
	"encoding/json"
	"github.com/nicolasacchi/metamusic/internal/config"
	"strings"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// keysOf collects every object key of a JSON body.
func keysOf(b []byte) map[string]bool {
	var v any
	_ = json.Unmarshal(b, &v)
	out := map[string]bool{}
	var walk func(any)
	walk = func(n any) {
		switch x := n.(type) {
		case map[string]any:
			for k, c := range x {
				out[k] = true
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(v)
	return out
}

// v2Paths are the Listener-reachable reads with ids filled in.
func v2Paths(t *testing.T, owner *servertest.Client) []string {
	t.Helper()
	songs := arr(obj(owner.OK(t, "GET", "/api/songs?limit=100", nil))["items"])
	paths := []string{
		"/api/tracks?limit=50", "/api/tracks/" + lt.TrackLuna, "/api/tracks/" + lt.TrackLuna + "/lineage", "/api/songs?limit=100",
		"/api/atlas", "/api/sounds/search", "/api/sounds/taxonomy", "/api/tags", "/api/library/facets", "/api/playlists", "/api/crates",
		"/api/records", "/api/compare?a=" + lt.TrackLuna + "&b=" + lt.TrackRefFlac,
	}
	for _, s := range songs {
		paths = append(paths, "/api/songs/"+obj(s)["id"].(string))
	}
	if ps := arr(obj(owner.OK(t, "GET", "/api/playlists", nil))["items"]); len(ps) > 0 {
		paths = append(paths, "/api/playlists/"+obj(ps[0])["id"].(string))
	}
	if cs := arr(obj(owner.OK(t, "GET", "/api/crates", nil))["items"]); len(cs) > 0 {
		paths = append(paths, "/api/crates/"+obj(cs[0])["id"].(string))
	}
	for _, r := range arr(obj(owner.OK(t, "GET", "/api/records", nil))["items"]) {
		id := obj(r)["id"].(string)
		paths = append(paths, "/api/records/"+id, "/api/records/"+id+"/changes")
	}
	return paths
}

// TestListenerRedaction pins C308.3: whatever a Listener reads carries no money, recipe internals, Muse structure, private
// notes or smart-crate filter, while the Owner and the Editor keep what they had.
func TestListenerRedaction(t *testing.T) {
	t.Parallel()
	r, owner, bob, carol := v1Rig(t)
	_ = r
	listenerNever := []string{
		"seed", "request_id", "critic_stars", "status_set_by", "muse_id", "muse", "museum", "queued_jobs", "draft_id", "open_proposals",
		"cost", "cost_basis", "cost_display_usd", "cost_display_micro_usd", "display_micro_usd", "display_usd", "cot", "hash",
		"master_sha256", "audio_md5", "import", "playlists", "listening", "comments", "open_comments", "filter",
		"proposals", "events", "sessions", "draft", "liner", "operator", "rationale", "job_id", "mi", "muses", "evidence", "duels", "ladder", "average",
	}
	editorNever := []string{"cost", "cost_basis", "cost_display_usd", "cost_display_micro_usd", "display_micro_usd", "display_usd"}
	seenOwner := map[string]bool{}
	for _, p := range v2Paths(t, owner) {
		ob := owner.Do(t, "GET", p, nil)
		if ob.Status != 200 {
			t.Errorf("owner %s: %d", p, ob.Status)
			continue
		}
		for k := range keysOf(ob.Body) {
			seenOwner[k] = true
		}
		cb := carol.Do(t, "GET", p, nil)
		if cb.Status == 404 && strings.HasPrefix(p, "/api/records/") { // a draft Record is not there for a Listener (C305)
			continue
		}
		if cb.Status != 200 {
			if !strings.Contains(p, "/api/compare") {
				t.Errorf("listener %s: %d %s", p, cb.Status, cb.Body)
			}
			continue
		}
		got := keysOf(cb.Body)
		for _, k := range listenerNever {
			if got[k] {
				t.Errorf("%s: a Listener receives %q", p, k)
			}
		}
		for k := range got {
			if strings.HasSuffix(k, "_usd") || strings.HasSuffix(k, "_micro_usd") {
				t.Errorf("%s: a Listener receives the money key %q", p, k)
			}
		}
		eb := bob.Do(t, "GET", p, nil)
		if eb.Status != 200 {
			t.Errorf("editor %s: %d", p, eb.Status)
			continue
		}
		for k := range keysOf(eb.Body) {
			for _, bad := range editorNever {
				if k == bad || strings.HasSuffix(k, "_usd") {
					t.Errorf("%s: an Editor receives %q", p, k)
				}
			}
		}
	}
	for _, p := range []string{"/api/tracks/" + lt.TrackLuna} {
		var tr map[string]any
		_ = json.Unmarshal(carol.Do(t, "GET", p, nil).Body, &tr)
		if _, ok := tr["generation"]; ok {
			t.Errorf("a Listener's track carries the generation block")
		}
	}
	// the redaction is not vacuous: the Owner's side carries the keys
	for _, k := range []string{"seed", "request_id", "muse_id", "cost_display_usd", "critic_stars", "draft", "generation", "filter"} {
		if !seenOwner[k] {
			t.Errorf("the Owner never saw %q, so the Listener check for it proves nothing", k)
		}
	}
	// the Editor still sees Muse structure and recipe internals
	eb := keysOf(bob.Do(t, "GET", "/api/songs?limit=100", nil).Body)
	if !eb["muse_id"] {
		t.Errorf("the Editor lost the Muse id")
	}
	if !keysOf(bob.Do(t, "GET", "/api/tracks/"+lt.TrackLuna, nil).Body)["seed"] {
		t.Errorf("the Editor lost the recipe seed")
	}
}

// TestOwnerShapeUnchanged pins that the Owner's responses keep every redactable key, null where there is no value, so the
// Owner, agents and the Owner's devices get the shape they had before U1.
func TestOwnerShapeUnchanged(t *testing.T) {
	t.Parallel()
	_, owner, _, _ := v1Rig(t)
	items := arr(obj(owner.OK(t, "GET", "/api/tracks?limit=50", nil))["items"])
	for _, it := range items {
		m := obj(it)
		for _, k := range []string{"seed", "request_id", "critic_stars", "cost_display_usd", "cost_display_micro_usd"} {
			if _, ok := m[k]; !ok {
				t.Fatalf("the Owner's track summary lost %q: %v", k, m)
			}
		}
	}
	tr := obj(owner.OK(t, "GET", "/api/tracks/"+lt.TrackLuna, nil))
	for _, k := range []string{"generation", "import", "playlists", "listening", "status_set_by", "comments"} {
		if _, ok := tr[k]; !ok {
			t.Errorf("the Owner's track lost %q", k)
		}
	}
	if _, ok := obj(tr["rating"])["average"]; !ok {
		t.Errorf("the Owner's rating lost the average")
	}
}

// TestListenerNoAverageRating pins D-U1-16: the average rating never reaches a Listener, an Editor still gets it.
func TestListenerNoAverageRating(t *testing.T) {
	t.Parallel()
	_, owner, bob, carol := v1Rig(t)
	owner.OK(t, "PUT", "/api/tracks/"+lt.TrackLuna+"/rating", map[string]any{"stars": 4})
	bob.OK(t, "PUT", "/api/tracks/"+lt.TrackLuna+"/rating", map[string]any{"stars": 2})
	carol.OK(t, "PUT", "/api/tracks/"+lt.TrackLuna+"/rating", map[string]any{"stars": 5})
	if _, ok := obj(obj(carol.OK(t, "GET", "/api/tracks/"+lt.TrackLuna, nil))["rating"])["average"]; ok {
		t.Errorf("a Listener's track carries the average")
	}
	if keysOf(carol.Do(t, "PUT", "/api/tracks/"+lt.TrackLuna+"/rating", map[string]any{"stars": 5}).Body)["average"] {
		t.Errorf("a Listener's rate_track response carries the average")
	}
	if avg := obj(obj(bob.OK(t, "GET", "/api/tracks/"+lt.TrackLuna, nil))["rating"])["average"]; avg == nil {
		t.Errorf("the Editor lost the average")
	}
	if avg := obj(obj(owner.OK(t, "GET", "/api/tracks/"+lt.TrackLuna, nil))["rating"])["average"]; avg == nil {
		t.Errorf("the Owner lost the average")
	}
}

// v2Rig is the fixture library with nothing discarded yet.
func v2Rig(t *testing.T) (r *servertest.Rig, owner, bob, carol *servertest.Client) {
	t.Helper()
	r = servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, Files: true})
	r.Settle(t)
	return r, r.Human(t), r.User(t, servertest.UserEditor, config.RoleEditor), r.User(t, servertest.UserListener, config.RoleListener)
}

// TestListenerAllDiscardedSong pins the V1 hand-off (a): a Song whose takes are all discarded does not list, count, plot or
// resolve for a Listener, and no Song field of a Song that survives names or derives from a discarded take.
func TestListenerAllDiscardedSong(t *testing.T) {
	t.Parallel()
	r, owner, bob, carol := v2Rig(t)
	d := lt.TrackJobChild
	var song string
	if err := r.Store.R.QueryRow(`SELECT song_id FROM take_songs WHERE track_id = ?`, d).Scan(&song); err != nil {
		t.Fatal(err)
	}
	listed := func(c *servertest.Client, path string) bool {
		for _, it := range arr(obj(c.OK(t, "GET", path, nil))["items"]) {
			if obj(it)["id"] == song {
				return true
			}
		}
		return false
	}
	// the Song has a second, visible take, rated and picked by the Owner on the discarded one first
	rows, err := r.Store.R.Query(`SELECT track_id FROM take_songs WHERE song_id = ?`, song)
	if err != nil {
		t.Fatal(err)
	}
	var takes []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		takes = append(takes, id)
	}
	rows.Close()
	if !listed(carol, "/api/songs?limit=100") {
		t.Fatalf("setup: the Song is not listed to a Listener while a visible take remains")
	}
	owner.OK(t, "PUT", "/api/tracks/"+d+"/rating", map[string]any{"stars": 5})
	owner.OK(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": d})
	r.Settle(t)
	owner.OK(t, "PATCH", "/api/tracks/"+d, map[string]any{"status": "discarded"})
	r.Settle(t)
	for _, it := range arr(obj(carol.OK(t, "GET", "/api/songs?limit=100", nil))["items"]) {
		m := obj(it)
		if m["id"] != song {
			continue
		}
		if p := obj(m["pick"]); p != nil && p["take_id"] == d {
			t.Errorf("a Listener's Song summary names the discarded Pick take")
		}
		if m["root_track_id"] == d {
			t.Errorf("a Listener's Song summary names the discarded root")
		}
	}
	for _, id := range takes {
		if id != d {
			owner.OK(t, "PATCH", "/api/tracks/"+id, map[string]any{"status": "discarded"})
		}
	}
	r.Settle(t)
	if !listed(owner, "/api/songs?limit=100") || !listed(bob, "/api/songs?limit=100") {
		t.Errorf("the Owner and the Editor lost a Song whose takes are discarded")
	}
	if listed(carol, "/api/songs?limit=100") {
		t.Errorf("list_songs lists a Song of discarded takes to a Listener")
	}
	if n, o := obj(carol.OK(t, "GET", "/api/songs?limit=100", nil))["total"].(float64), obj(owner.OK(t, "GET", "/api/songs?limit=100", nil))["total"].(float64); n != o-1 {
		t.Errorf("list_songs total: Listener %v, Owner %v", n, o)
	}
	for _, it := range arr(obj(carol.OK(t, "GET", "/api/sounds/search", nil))["songs"]) {
		if obj(it)["id"] == song {
			t.Errorf("search_sounds lists it")
		}
	}
	for _, pt := range arr(obj(carol.OK(t, "GET", "/api/atlas", nil))["points"]) {
		if obj(pt)["id"] == song {
			t.Errorf("the Atlas plots it")
		}
	}
	atlas := func(c *servertest.Client) float64 {
		return obj(obj(c.OK(t, "GET", "/api/atlas", nil))["totals"])["scope_total"].(float64)
	}
	if atlas(carol) != atlas(owner)-1 {
		t.Errorf("the Atlas scope total: Listener %v, Owner %v", atlas(carol), atlas(owner))
	}
	if resp := carol.Do(t, "GET", "/api/songs/"+song, nil); resp.Status != 404 {
		t.Errorf("get_song of a Song of discarded takes: %d", resp.Status)
	}
	if resp := owner.Do(t, "GET", "/api/songs/"+song, nil); resp.Status != 200 {
		t.Errorf("the Owner's get_song: %d", resp.Status)
	}
}

// TestListenerSongStateIgnoresDiscarded pins that a Song's state and Pick for a Listener derive from visible takes only.
func TestListenerSongStateIgnoresDiscarded(t *testing.T) {
	t.Parallel()
	r, owner, _, carol := v2Rig(t)
	d := lt.TrackJobChild
	var song string
	if err := r.Store.R.QueryRow(`SELECT song_id FROM take_songs WHERE track_id = ?`, d).Scan(&song); err != nil {
		t.Fatal(err)
	}
	state := func(c *servertest.Client) (string, any) {
		for _, it := range arr(obj(c.OK(t, "GET", "/api/songs?limit=100", nil))["items"]) {
			if obj(it)["id"] == song {
				m := obj(it)
				return m["state"].(string), obj(m["pick"])
			}
		}
		t.Fatalf("Song not listed")
		return "", nil
	}
	owner.OK(t, "PUT", "/api/tracks/"+d+"/rating", map[string]any{"stars": 5})
	owner.OK(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": d})
	r.Settle(t)
	owner.OK(t, "PATCH", "/api/tracks/"+d, map[string]any{"status": "discarded"})
	r.Settle(t)
	if _, op := state(carol); op != nil && obj(op)["take_id"] == d {
		t.Errorf("a Listener's Song names the discarded take as its Pick: %v", op)
	}
	sv := obj(carol.OK(t, "GET", "/api/songs/"+song, nil))
	if strings.Contains(string(jb(sv)), d) {
		t.Errorf("get_song names the discarded take: %s", jb(sv))
	}
}

func jb(v any) []byte { b, _ := json.Marshal(v); return b }

// TestListenerSmartCrateHiddenFilter pins the V1 hand-off (b): a smart Crate whose stored filter holds a banned filter is
// refused for a Listener through crate_id, in every list that takes it; the Owner and the Editor still expand it.
func TestListenerSmartCrateHiddenFilter(t *testing.T) {
	t.Parallel()
	_, owner, bob, carol := v1Rig(t)
	mk := func(name, subject string, filter map[string]any) string {
		c := obj(owner.OK(t, "POST", "/api/crates", map[string]any{"name": name, "kind": "smart", "subject": subject, "filter": filter}, "Idempotency-Key", "v2-"+name))
		if id := obj(c["crate"])["id"]; id != nil {
			return id.(string)
		}
		return c["id"].(string)
	}
	songCrate := mk("angle-crate", "song", map[string]any{"angle": "variant-a"})
	takeCrate := mk("comments-crate", "take", map[string]any{"has_open_comments": true})
	okCrate := mk("state-crate", "song", map[string]any{"state": []string{"rendered"}})
	for _, p := range []string{"/api/songs?crate_id=" + songCrate, "/api/sounds/search?crate_id=" + songCrate, "/api/tracks?crate_id=" + takeCrate, "/api/sequence?crate_id=" + songCrate} {
		resp := carol.Do(t, "GET", p, nil)
		if resp.Status != 422 || !strings.Contains(string(resp.Body), "FILTER_NOT_ALLOWED") {
			t.Errorf("%s for a Listener: %d %s", p, resp.Status, resp.Body)
		}
		if strings.Contains(string(resp.Body), "angle") || strings.Contains(string(resp.Body), "has_open_comments") {
			t.Errorf("%s: the refusal reveals the stored filter: %s", p, resp.Body)
		}
		if r := owner.Do(t, "GET", p, nil); r.Status == 422 && strings.Contains(string(r.Body), "FILTER_NOT_ALLOWED") {
			t.Errorf("%s refused to the Owner", p)
		}
		if r := bob.Do(t, "GET", p, nil); r.Status == 422 && strings.Contains(string(r.Body), "FILTER_NOT_ALLOWED") {
			t.Errorf("%s refused to the Editor", p)
		}
	}
	if resp := carol.Do(t, "GET", "/api/songs?crate_id="+okCrate, nil); resp.Status != 200 {
		t.Errorf("a harmless smart Crate is refused to a Listener: %d %s", resp.Status, resp.Body)
	}
}

// TestIdentityMasking pins C309: a Listener sees only their own name, other users as "user"; an Editor and the Owner see names.
func TestIdentityMasking(t *testing.T) {
	t.Parallel()
	_, owner, bob, carol := v1Rig(t)
	owner.OK(t, "POST", "/api/crates", map[string]any{"name": "mine", "kind": "manual", "subject": "take", "items": []map[string]any{{"track_id": lt.TrackLuna}}}, "Idempotency-Key", "id-mask-1")
	bob.OK(t, "POST", "/api/crates", map[string]any{"name": "bobs", "kind": "manual", "subject": "take", "items": []map[string]any{{"track_id": lt.TrackLuna}}}, "Idempotency-Key", "id-mask-2")
	by := func(c *servertest.Client) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, it := range arr(obj(c.OK(t, "GET", "/api/crates", nil))["items"]) {
			m := obj(it)
			out[m["name"].(string)] = obj(m["created_by"])
		}
		return out
	}
	lc := by(carol)
	for _, n := range []string{"mine", "bobs"} {
		if lc[n]["name"] != "user" || lc[n]["id"] != "user" {
			t.Errorf("a Listener sees %v as the author of %s", lc[n], n)
		}
	}
	for _, n := range []string{"bobs", "mine"} {
		if b := string(jb(lc[n])); strings.Contains(b, "bob") || strings.Contains(b, "alice") {
			t.Errorf("a username reaches a Listener: %s", b)
		}
	}
	ec := by(bob)
	if ec["bobs"]["name"] != "bob" {
		t.Errorf("an Editor does not see their own name: %v", ec["bobs"])
	}
	if ec["mine"]["name"] == "user" || ec["mine"]["name"] == nil {
		t.Errorf("an Editor does not see the Owner's name: %v", ec["mine"])
	}
	if by(owner)["bobs"]["name"] != "bob" {
		t.Errorf("the Owner does not see the Editor's name")
	}
}

// TestEditorHeldOps pins the four holds V2 lifted: get_track_context and get_stats for an Editor, get_export own only,
// put_device_profile own only; a Listener reaches none of them.
func TestEditorHeldOps(t *testing.T) {
	t.Parallel()
	_, owner, bob, carol := v1Rig(t)
	for _, p := range []string{"/api/tracks/" + lt.TrackLuna + "/context", "/api/stats"} {
		if resp := carol.Do(t, "GET", p, nil); resp.Status != 403 {
			t.Errorf("a Listener's %s: %d", p, resp.Status)
		}
		if resp := bob.Do(t, "GET", p, nil); resp.Status != 200 {
			t.Errorf("an Editor's %s: %d %s", p, resp.Status, resp.Body)
		}
	}
	ctxKeys := keysOf(bob.Do(t, "GET", "/api/tracks/"+lt.TrackLuna+"/context", nil).Body)
	for _, k := range []string{"muse", "cost"} {
		if ctxKeys[k] {
			t.Errorf("an Editor's context carries %q", k)
		}
	}
	if !keysOf(owner.Do(t, "GET", "/api/tracks/"+lt.TrackLuna+"/context", nil).Body)["muse"] {
		t.Errorf("the Owner's context lost the muse block")
	}
	stats := obj(bob.OK(t, "GET", "/api/stats", nil))
	for _, k := range []string{"generation", "backups", "storage", "imports"} {
		if _, ok := stats[k]; ok {
			t.Errorf("an Editor's stats carry %q", k)
		}
	}
	if _, ok := obj(owner.OK(t, "GET", "/api/stats", nil))["generation"]; !ok {
		t.Errorf("the Owner's stats lost the generation block")
	}

	// get_export: own only
	body := map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{lt.TrackLuna}}}
	oe := obj(owner.OK(t, "POST", "/api/exports/jsonl", body, "Idempotency-Key", "v2-exp-owner"))
	be := obj(bob.OK(t, "POST", "/api/exports/jsonl", body, "Idempotency-Key", "v2-exp-bob"))
	if resp := bob.Do(t, "GET", "/api/exports/"+be["id"].(string), nil); resp.Status != 200 {
		t.Errorf("an Editor's own export: %d %s", resp.Status, resp.Body)
	}
	got := bob.Do(t, "GET", "/api/exports/"+oe["id"].(string), nil)
	missing := bob.Do(t, "GET", "/api/exports/01900000-0000-7000-8000-0000000fffff", nil)
	if got.Status != 404 || string(got.Body) != string(missing.Body) {
		t.Errorf("an Editor reads the Owner's export: %d %s (missing: %s)", got.Status, got.Body, missing.Body)
	}
	if resp := owner.Do(t, "GET", "/api/exports/"+be["id"].(string), nil); resp.Status != 200 {
		t.Errorf("the Owner reads an Editor's export: %d", resp.Status)
	}
	if resp := carol.Do(t, "GET", "/api/exports/"+be["id"].(string), nil); resp.Status != 403 {
		t.Errorf("a Listener reads an export: %d", resp.Status)
	}

	// put_device_profile: own only
	prof := map[string]any{"label": "phone", "user_agent": "ua", "quality": "auto", "quality_source": "default", "probe_result": "none", "flac_can_play": "", "probe_version": 0}
	if resp := bob.Do(t, "PUT", "/api/device-profiles/dev-bob-0001", prof); resp.Status != 200 {
		t.Fatalf("an Editor writes a new profile: %d %s", resp.Status, resp.Body)
	}
	if resp := carol.Do(t, "PUT", "/api/device-profiles/dev-bob-0001", prof); resp.Status != 404 {
		t.Errorf("a Listener overwrites an Editor's profile: %d %s", resp.Status, resp.Body)
	}
	if resp := bob.Do(t, "PUT", "/api/device-profiles/dev-bob-0001", prof); resp.Status != 200 {
		t.Errorf("an Editor rewrites their own profile: %d", resp.Status)
	}
	if resp := carol.Do(t, "PUT", "/api/device-profiles/dev-carol-001", prof); resp.Status != 200 {
		t.Errorf("a Listener writes a new profile: %d %s", resp.Status, resp.Body)
	}
	if resp := bob.Do(t, "PUT", "/api/device-profiles/dev-carol-001", prof); resp.Status != 404 {
		t.Errorf("an Editor overwrites a Listener's profile: %d", resp.Status)
	}
	if resp := owner.Do(t, "PUT", "/api/device-profiles/dev-bob-0001", prof); resp.Status != 200 {
		t.Errorf("the Owner overwrites a profile: %d", resp.Status)
	}
}

// TestListenerAuditAndRate pins C304.4 end to end: a Listener browse writes no audit row, rate_track writes one row without
// the verdict text, and a session of play events writes none.
func TestListenerAuditAndRate(t *testing.T) {
	t.Parallel()
	r, _, _, carol := v1Rig(t)
	rows := func() int {
		var n int
		if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE principal_name = 'carol'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := rows()
	carol.OK(t, "GET", "/api/tracks", nil)
	carol.OK(t, "GET", "/api/songs", nil)
	if n := rows(); n != before {
		t.Errorf("a Listener's browse wrote %d audit rows", n-before)
	}
	carol.OK(t, "PUT", "/api/tracks/"+lt.TrackLuna+"/rating", map[string]any{"stars": 4, "verdict": "CANARY-NOTE-900"})
	if n := rows(); n != before+1 {
		t.Errorf("rate_track wrote %d audit rows", n-before)
	}
	var leak int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE principal_name = 'carol' AND (args_sha256 || principal_name || op || created_ids_json) LIKE '%CANARY%'`).Scan(&leak); err == nil && leak != 0 {
		t.Errorf("the verdict text is in the audit row")
	}
	var evs []map[string]any
	for i := 0; i < 20; i++ {
		evs = append(evs, map[string]any{"id": "01900000-0000-7000-8000-00000000b" + string(rune('a'+i)) + "01", "track_id": lt.TrackLuna, "session_id": "carol-session-1", "kind": "start", "client_at": lt.FixedNow.Format("2006-01-02T15:04:05Z07:00")})
	}
	carol.Do(t, "POST", "/api/listen-events", map[string]any{"events": evs})
	if n := rows(); n != before+1 {
		t.Errorf("log_listen_events wrote audit rows: %d", n-before-1)
	}
}

// TestListenerRecordsPublishedOnly pins C305: a Listener lists, reads and follows the changelog of published, non-hidden
// Records only; a draft Record answers 404 like a missing one, and a Song view names no draft Record.
func TestListenerRecordsPublishedOnly(t *testing.T) {
	t.Parallel()
	_, owner, bob, carol := v2Rig(t)
	status := func(c *servertest.Client, q string) map[string]string {
		out := map[string]string{}
		for _, it := range arr(obj(c.OK(t, "GET", "/api/records"+q, nil))["items"]) {
			out[obj(it)["id"].(string)] = obj(it)["status"].(string)
		}
		return out
	}
	all, seen := status(owner, ""), status(carol, "")
	var draft, published string
	for id, st := range all {
		if st == "draft" {
			draft = id
		}
		if st == "published" {
			published = id
		}
	}
	if draft == "" || published == "" {
		t.Fatalf("setup: the fixture lacks a draft and a published Record: %v", all)
	}
	if len(status(bob, "")) != len(all) {
		t.Errorf("the Editor lost a Record")
	}
	if _, ok := seen[draft]; ok || seen[published] != "published" {
		t.Errorf("a Listener's record list: %v", seen)
	}
	if len(status(carol, "?status=draft")) != 0 || len(status(carol, "?include_hidden=true")) != len(seen) {
		t.Errorf("a Listener reaches a draft Record through a filter")
	}
	if len(status(carol, "?status=draft&status=published")) != len(seen) {
		t.Errorf("an explicit published status was dropped")
	}
	for _, p := range []string{"/api/records/" + draft, "/api/records/" + draft + "/changes"} {
		if resp := carol.Do(t, "GET", p, nil); resp.Status != 404 {
			t.Errorf("%s for a Listener: %d", p, resp.Status)
		}
		if resp := owner.Do(t, "GET", p, nil); resp.Status != 200 {
			t.Errorf("%s for the Owner: %d", p, resp.Status)
		}
	}
	for _, p := range []string{"/api/records/" + published, "/api/records/" + published + "/changes"} {
		if resp := carol.Do(t, "GET", p, nil); resp.Status != 200 {
			t.Errorf("%s for a Listener: %d %s", p, resp.Status, resp.Body)
		}
	}
	if resp := carol.Do(t, "GET", "/api/records/01900000-0000-7000-8000-0000000fffff", nil); resp.Status != 404 {
		t.Errorf("a missing Record: %d", resp.Status)
	}
	for _, s := range arr(obj(owner.OK(t, "GET", "/api/songs?limit=100", nil))["items"]) {
		id := obj(s)["id"].(string)
		for _, r := range arr(obj(carol.OK(t, "GET", "/api/songs/"+id, nil))["records"]) {
			if obj(r)["id"] == draft {
				t.Errorf("a Listener's Song view names the draft Record")
			}
		}
	}
}
