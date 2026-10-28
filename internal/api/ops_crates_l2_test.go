package api_test

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// crateRig is the settled L2 rig: FixtureL2 over the L1 mirror.
func crateRig(t *testing.T) *servertest.Rig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	r.Settle(t)
	return r
}

func dbStr(t *testing.T, r *servertest.Rig, q string, args ...any) string {
	t.Helper()
	var s string
	if err := r.Store.R.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}

func crateID(t *testing.T, r *servertest.Rig, name string) string {
	return dbStr(t, r, `SELECT id FROM crates WHERE name = ?`, name)
}

func idsOf(v any) []string {
	var out []string
	for _, it := range arr(m(v)["items"]) {
		out = append(out, str(m(it)["id"]))
	}
	slices.Sort(out)
	return out
}

// TestCratesOps drives the five Crate operations over REST on one settled rig.
func TestCratesOps(t *testing.T) {
	t.Parallel()
	r := crateRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	reader, _ := r.Agent(t, auth.ScopeRead)
	s1 := dbStr(t, r, `SELECT id FROM songs WHERE title = 'Synthetic Song S1'`)

	// list_crates: counts only for manual Crates unless counts=true
	list := m(agent.OK(t, "GET", "/api/crates", nil))
	if list["total"] != float64(4) || len(arr(list["items"])) != 4 {
		t.Fatalf("list: %v", list)
	}
	for _, it := range arr(list["items"]) {
		c := m(it)
		if (c["kind"] == "manual") != (c["count"] != nil) {
			t.Errorf("%v: count %v", c["name"], c["count"])
		}
	}
	withCounts := m(agent.OK(t, "GET", "/api/crates?counts=true&kind=smart&subject=song", nil))
	if items := arr(withCounts["items"]); len(items) != 1 || m(items[0])["count"] != float64(2) {
		t.Errorf("counts=true: %v", withCounts)
	}

	// get_crate: the live count of the smart Crate and no warning
	smart := crateID(t, r, "Synthetic Unheard Reggae")
	got := m(agent.OK(t, "GET", "/api/crates/"+smart, nil))
	c := m(got["crate"])
	if got["count"] != float64(2) || len(arr(got["warnings"])) != 0 || c["kind"] != "smart" || m(c["filter"])["unheard"] != true || c["revision"] != float64(1) {
		t.Errorf("get_crate: %v", got)
	}
	if by := m(c["created_by"]); by["kind"] != "human" || by["name"] != "human" {
		t.Errorf("an agent sees the operator masked: %v", by)
	}
	if resp := agent.Do(t, "GET", "/api/crates/nope", nil); resp.Status != 404 {
		t.Errorf("unknown crate: %d", resp.Status)
	}

	// create_crate: manual with items, idempotent replay, scopes
	body := map[string]any{"name": "  Agent   picks ", "kind": "manual", "items": []map[string]any{{"song_id": s1}, {"song_id": s1}}}
	first := agent.Do(t, "POST", "/api/crates", body, "Idempotency-Key", "crate-1")
	if first.Status != 201 {
		t.Fatalf("create: %d %s", first.Status, first.Body)
	}
	created := m(first.JSON(t))
	cid := str(m(created["crate"])["id"])
	if created["count"] != float64(1) || m(created["crate"])["name"] != "Agent picks" || m(created["crate"])["subject"] != "song" {
		t.Errorf("created: %v", created)
	}
	if again := agent.Do(t, "POST", "/api/crates", body, "Idempotency-Key", "crate-1"); again.Status != 201 || str(m(m(again.JSON(t))["crate"])["id"]) != cid {
		t.Errorf("replay: %d %s", again.Status, again.Body)
	}
	if n := dbStr(t, r, `SELECT COUNT(*) FROM crates WHERE name = 'Agent picks'`); n != "1" {
		t.Errorf("the replay made %s crates", n)
	}
	if resp := reader.Do(t, "POST", "/api/crates", body, "Idempotency-Key", "crate-2"); resp.Status != 403 {
		t.Errorf("read-only agent: %d", resp.Status)
	}
	for name, tc := range map[string]struct {
		body map[string]any
		code string
		st   int
	}{
		"unknown key":    {map[string]any{"name": "k", "kind": "smart", "filter": map[string]any{"nope": 1}}, "UNKNOWN_FILTER_KEY", 422},
		"wrong type":     {map[string]any{"name": "k", "kind": "smart", "filter": map[string]any{"unheard": "yes"}}, "INVALID_FILTER", 422},
		"crate_id":       {map[string]any{"name": "k", "kind": "smart", "filter": map[string]any{"crate_id": "x"}}, "UNKNOWN_FILTER_KEY", 422},
		"smart no body":  {map[string]any{"name": "k", "kind": "smart"}, "VALIDATION_FAILED", 422},
		"smart items":    {map[string]any{"name": "k", "kind": "smart", "filter": map[string]any{}, "items": []map[string]any{{"song_id": s1}}}, "CRATE_IS_SMART", 409},
		"bad member":     {map[string]any{"name": "k", "kind": "manual", "items": []map[string]any{{"song_id": "nope"}}}, "UNKNOWN_SONG", 422},
		"wrong subject":  {map[string]any{"name": "k", "kind": "manual", "subject": "take", "items": []map[string]any{{"song_id": s1}}}, "SUBJECT_MISMATCH", 422},
		"member no id":   {map[string]any{"name": "k", "kind": "manual", "items": []map[string]any{{}}}, "VALIDATION_FAILED", 422},
		"control char":   {map[string]any{"name": "a\u0007b", "kind": "manual"}, "VALIDATION_FAILED", 422},
		"too many items": {map[string]any{"name": "k", "kind": "manual", "items": make([]map[string]any, 201)}, "", 422},
	} {
		resp := agent.Do(t, "POST", "/api/crates", tc.body, "Idempotency-Key", "bad-"+strings.ReplaceAll(name, " ", "-"))
		if resp.Status != tc.st || (tc.code != "" && str(m(resp.JSON(t))["code"]) != tc.code) {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
	}

	// update_crate: revision lock, filter and count
	upd := m(agent.OK(t, "PATCH", "/api/crates/"+smart, map[string]any{"revision": 1, "name": "Renamed", "filter": map[string]any{"unheard": true}}))
	if m(upd["crate"])["revision"] != float64(2) || upd["count"].(float64) <= 2 || m(upd["crate"])["name"] != "Renamed" {
		t.Errorf("update: %v", upd)
	}
	if resp := agent.Do(t, "PATCH", "/api/crates/"+smart, map[string]any{"revision": 1, "name": "Stale"}); resp.Status != 409 || str(m(resp.JSON(t))["code"]) != "STALE_REVISION" {
		t.Errorf("stale: %d %s", resp.Status, resp.Body)
	}
	if resp := agent.Do(t, "PATCH", "/api/crates/"+smart, map[string]any{"revision": 2}); resp.Status != 422 {
		t.Errorf("empty patch: %d", resp.Status)
	}
	if resp := reader.Do(t, "PATCH", "/api/crates/"+smart, map[string]any{"revision": 2, "name": "x"}); resp.Status != 403 {
		t.Errorf("read-only update: %d", resp.Status)
	}
	hid := m(human.OK(t, "PATCH", "/api/crates/"+cid, map[string]any{"revision": 1, "hidden": true}))
	if m(hid["crate"])["hidden"] != true {
		t.Errorf("hide: %v", hid)
	}
	if n := len(arr(m(agent.OK(t, "GET", "/api/crates", nil))["items"])); n != 4 {
		t.Errorf("a hidden crate is listed: %d", n)
	}

	// edit_crate_items
	s2 := dbStr(t, r, `SELECT id FROM songs WHERE title = 'Synthetic Song S2'`)
	res := m(agent.OK(t, "POST", "/api/crates/"+cid+"/items", map[string]any{"add": []map[string]any{{"song_id": s1}, {"song_id": s2}}, "remove": []map[string]any{{"song_id": "x"}}}))
	if res["added"] != float64(1) || res["already"] != float64(1) || res["not_member"] != float64(1) || res["removed"] != float64(0) || res["count"] != float64(2) {
		t.Errorf("items: %v", res)
	}
	if resp := agent.Do(t, "POST", "/api/crates/"+smart+"/items", map[string]any{"add": []map[string]any{{"song_id": s1}}}); resp.Status != 409 || str(m(resp.JSON(t))["code"]) != "CRATE_IS_SMART" {
		t.Errorf("smart items: %d %s", resp.Status, resp.Body)
	}
	if resp := agent.Do(t, "POST", "/api/crates/nope/items", map[string]any{"add": []map[string]any{{"song_id": s1}}}); resp.Status != 404 {
		t.Errorf("unknown crate items: %d", resp.Status)
	}
}

// TestListOps_CrateID: crate_id on list_songs, search_sounds and search_tracks, and the predicates of the L2 list parameters.
func TestListOps_CrateID(t *testing.T) {
	t.Parallel()
	r := crateRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead)
	smart := crateID(t, r, "Synthetic Unheard Reggae")
	manual := crateID(t, r, "Synthetic Remake Candidates")
	titles := func(v any) []string {
		var out []string
		for _, it := range arr(m(v)["items"]) {
			out = append(out, strings.TrimPrefix(str(m(it)["title"]), "Synthetic Song "))
		}
		slices.Sort(out)
		return out
	}
	explicit := "/api/songs?sound=genre_family:reggae&unheard=true&min_duration_s=240&pick_decided=false"
	want := []string{"S1", "S6"}
	for name, path := range map[string]string{"explicit": explicit, "smart crate": "/api/songs?crate_id=" + smart} {
		v := agent.OK(t, "GET", path, nil)
		got := titles(v)
		if !slices.Equal(got, want) || m(v)["total"] != float64(2) {
			t.Errorf("list_songs %s = %v (total %v), want %v", name, got, m(v)["total"], want)
		}
	}
	if got := titles(agent.OK(t, "GET", "/api/songs?crate_id="+manual, nil)); !slices.Equal(got, []string{"S1", "S4", "S6"}) {
		t.Errorf("manual crate: %v", got)
	}
	if got := titles(agent.OK(t, "GET", "/api/songs?crate_id="+smart+"&q=S6", nil)); !slices.Equal(got, []string{"S6"}) {
		t.Errorf("crate and q: %v", got)
	}
	// search_sounds accepts the same filters and counts through them
	ss := m(agent.OK(t, "GET", "/api/sounds/search?crate_id="+smart, nil))
	if ss["total"] != float64(2) {
		t.Errorf("search_sounds total %v", ss["total"])
	}
	// errors
	for name, tc := range map[string]struct {
		path string
		st   int
		code string
	}{
		"unknown crate":        {"/api/songs?crate_id=nope", 404, "NOT_FOUND"},
		"take crate on songs":  {"/api/songs?crate_id=" + crateID(t, r, "Synthetic To Judge"), 422, "SUBJECT_MISMATCH"},
		"song crate on tracks": {"/api/tracks?crate_id=" + manual, 422, "SUBJECT_MISMATCH"},
		"conflict":             {"/api/songs?crate_id=" + smart + "&unheard=false", 409, "FILTER_CONFLICT"},
		"stars exclusive":      {"/api/songs?min_stars=3&unrated=true", 422, "VALIDATION_FAILED"},
		"bad sound on tracks":  {"/api/tracks?sound=nofacet:x", 422, "VALIDATION_FAILED"},
	} {
		resp := agent.Do(t, "GET", tc.path, nil)
		if resp.Status != tc.st || str(m(resp.JSON(t))["code"]) != tc.code {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
	}
	// search_tracks through the smart Take crate equals the explicit parameters
	tcrate := crateID(t, r, "Synthetic Unheard Drafts")
	a := agent.OK(t, "GET", "/api/tracks?crate_id="+tcrate+"&limit=100", nil)
	b := agent.OK(t, "GET", "/api/tracks?status=draft&unheard=true&limit=100", nil)
	if !slices.Equal(idsOf(a), idsOf(b)) || m(a)["total"] != m(b)["total"] {
		t.Errorf("take crate %v vs explicit %v", idsOf(a), idsOf(b))
	}
	// has_pick keeps its meaning next to pick_decided
	hp := titles(agent.OK(t, "GET", "/api/songs?has_pick=true&q=Synthetic+Song&limit=100", nil))
	pd := titles(agent.OK(t, "GET", "/api/songs?pick_decided=true&q=Synthetic+Song&limit=100", nil))
	if !slices.Equal(hp, []string{"S1", "S2", "S3", "S4", "S5", "S6"}) || !slices.Equal(pd, []string{"S5"}) {
		t.Errorf("has_pick %v, pick_decided %v", hp, pd)
	}
}

func query(key string, v any) string {
	switch x := v.(type) {
	case []string:
		var parts []string
		for _, s := range x {
			parts = append(parts, key+"="+url.QueryEscape(s))
		}
		return strings.Join(parts, "&")
	default:
		return key + "=" + url.QueryEscape(fmt.Sprint(x))
	}
}

// vocabularyBehaviour is the behavioural half of TestCrateVocabulary_EqualsListOps: for every key of the vocabulary a query
// through the list operation and the same value in a stored filter (through crate_id) return the same ids; the non-saved set is
// refused inside a filter.
func vocabularyBehaviour(t *testing.T) {
	r := crateRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	muse := dbStr(t, r, `SELECT muse_id FROM songs WHERE title = 'Synthetic Song S1'`)
	samples := map[string]map[string]any{
		"song": {
			"muse_id": muse, "no_muse": true, "angle": "variant-a", "state": []string{"rendered", "picked"}, "queued": false, "session_id": "no-such-session",
			"sound": []string{"genre_family:reggae"}, "tempo_mismatch": false, "has_pick": true, "has_open_proposals": false, "q": "Synthetic Song",
			"pick_decided": false, "unheard": true, "min_duration_s": 240.0, "max_duration_s": 250.0, "min_stars": 3, "unrated": true,
			"tag": []string{"no-such-tag"}, "lossy": false, "recipe_known": true, "in_record": true,
		},
		"take": {
			"q": "Synthetic", "status": []string{"draft", "final"}, "style": []string{"reggae"}, "min_duration_s": 200.0, "max_duration_s": 250.0,
			"recorded_from": "2020-01-01", "recorded_to": "2030-01-01", "has_score": false, "truncated": false, "min_stars": 3, "unrated": true,
			"tag": []string{"no-such-tag"}, "has_open_comments": false, "recipe_known": true, "lossy": false, "is_pick": true, "session_id": "no-such-session",
			"unheard": true, "in_record": true, "sound": []string{"genre_family:reggae"},
		},
	}
	ops := map[string]string{"song": "/api/songs", "take": "/api/tracks"}
	nonEmpty := map[string]int{}
	for subject, vals := range samples {
		keys := crates.Keys(subject)
		if len(vals) != len(keys) {
			t.Errorf("%s: %d samples for %d vocabulary keys", subject, len(vals), len(keys))
		}
		for _, k := range keys {
			v, ok := vals[k.Name]
			if !ok {
				t.Errorf("%s.%s has no sample", subject, k.Name)
				continue
			}
			viaOp := agent.Do(t, "GET", ops[subject]+"?limit=100&"+query(k.Name, v), nil)
			if viaOp.Status != 200 {
				t.Errorf("%s.%s through the op: %d %s", subject, k.Name, viaOp.Status, viaOp.Body)
				continue
			}
			created := agent.Do(t, "POST", "/api/crates", map[string]any{"name": subject + " " + k.Name, "kind": "smart", "subject": subject, "filter": map[string]any{k.Name: v}}, "Idempotency-Key", "voc-"+subject+"-"+k.Name)
			if created.Status != 201 {
				t.Errorf("%s.%s as a stored filter: %d %s", subject, k.Name, created.Status, created.Body)
				continue
			}
			id := str(m(m(created.JSON(t))["crate"])["id"])
			viaCrate := agent.Do(t, "GET", ops[subject]+"?limit=100&crate_id="+id, nil)
			if viaCrate.Status != 200 {
				t.Errorf("%s.%s through crate_id: %d %s", subject, k.Name, viaCrate.Status, viaCrate.Body)
				continue
			}
			a, b := m(viaOp.JSON(t)), m(viaCrate.JSON(t))
			if !slices.Equal(idsOf(a), idsOf(b)) || a["total"] != b["total"] {
				t.Errorf("%s.%s: the op returns %v, the stored filter %v", subject, k.Name, idsOf(a), idsOf(b))
			}
			if got := m(agent.OK(t, "GET", "/api/crates/"+id, nil))["count"]; got != a["total"] {
				t.Errorf("%s.%s: the Crate counts %v, the op totals %v", subject, k.Name, got, a["total"])
			}
			if len(idsOf(a)) > 0 {
				nonEmpty[subject]++
			}
		}
	}
	if nonEmpty["song"] < 8 || nonEmpty["take"] < 10 {
		t.Errorf("too few samples select anything (%v): the comparison would be vacuous", nonEmpty)
	}
	for _, name := range crates.NonSaved {
		for subject := range samples {
			resp := agent.Do(t, "POST", "/api/crates", map[string]any{"name": "ns", "kind": "smart", "subject": subject, "filter": map[string]any{name: "x"}}, "Idempotency-Key", "ns-"+subject+"-"+name)
			if resp.Status != 422 || str(m(resp.JSON(t))["code"]) != "UNKNOWN_FILTER_KEY" {
				t.Errorf("%s.%s inside a filter: %d %s", subject, name, resp.Status, resp.Body)
			}
		}
	}
}
