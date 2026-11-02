package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func atlasRig(t *testing.T) *servertest.Rig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true, RealFeatures: true})
	r.Settle(t)
	return r
}

func getAtlas(t *testing.T, c *servertest.Client, query string) (int, atlas.Atlas, map[string]any) {
	t.Helper()
	resp := c.Do(t, "GET", "/api/atlas"+query, nil)
	var a atlas.Atlas
	var prob map[string]any
	if resp.Status == 200 {
		if err := json.Unmarshal(resp.Body, &a); err != nil {
			t.Fatalf("%v: %s", err, resp.Body)
		}
	} else {
		_ = json.Unmarshal(resp.Body, &prob)
	}
	return resp.Status, a, prob
}

// TestGetSongAtlas_REST: an agent reads the map of the settled L3 library: the frozen shape, the shared Song filter (a Muse, a
// smart and a manual Crate, a Sound facet, dim) and the stable errors.
func TestGetSongAtlas_REST(t *testing.T) {
	t.Parallel()
	r := atlasRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead)
	status, a, _ := getAtlas(t, agent, "")
	if status != 200 || a.Def != "mm.atlas/1" || len(a.Points) < 14 || a.NextCursor != nil || a.LayoutToken == "" {
		t.Fatalf("default map: %d, %d points, %+v", status, len(a.Points), a.Totals)
	}
	if a.Axes.X.Name != "tempo" || a.Axes.Y.Name != "energy" || a.ColorBy != "none" {
		t.Errorf("defaults: %s x %s, colour %s", a.Axes.X.Name, a.Axes.Y.Name, a.ColorBy)
	}
	resp := agent.Do(t, "GET", "/api/atlas", nil)
	for _, key := range []string{`"legend":[]`, `"guides":[]`, `"muses":[]`, `"points":[`, `"next_cursor":null`} {
		if !strings.Contains(string(resp.Body), key) {
			t.Errorf("body lacks %s", key)
		}
	}
	if strings.Contains(string(resp.Body), "Synthetic") && strings.Contains(string(resp.Body), "Muse") {
		t.Error("Muse text in the map")
	}
	if a.Totals.Basis.X.M == 0 || a.Totals.Basis.Y.P == 0 {
		t.Errorf("the library has measured tempo and predicted energy: %+v", a.Totals.Basis)
	}

	// The filter is the list_songs one: scope equals its total for a Muse, Sound facet, state and both Crates.
	m5 := dbStr(t, r, `SELECT id FROM muses WHERE title = ?`, lt.L3Muse5Title)
	manual, smart := crateID(t, r, "Synthetic Remake Candidates"), crateID(t, r, "Synthetic Unheard Reggae")
	for _, q := range []string{"muse_id=" + m5, "sound=genre_family:reggae", "state=rendered", "state=judged&unheard=true", "crate_id=" + manual, "crate_id=" + smart, "no_muse=true", "min_stars=4"} {
		want := m(agent.OK(t, "GET", "/api/songs?limit=1&"+q, nil))["total"]
		_, got, _ := getAtlas(t, agent, "?"+q)
		if float64(got.Totals.Scope) != want || got.Totals.ScopeTotal != got.Totals.Scope {
			t.Errorf("%s: scope %d, list_songs total %v", q, got.Totals.Scope, want)
		}
	}
	if _, g, _ := getAtlas(t, agent, "?crate_id="+smart); g.Totals.Scope == 0 {
		t.Error("the smart Crate selects nothing: the expansion proves nothing")
	}
	_, dim, _ := getAtlas(t, agent, "?dim=true&muse_id="+m5)
	if dim.Totals.Hit != 8 || dim.Totals.Scope <= 8 {
		t.Errorf("dim: %+v", dim.Totals)
	}

	// Colour, neighbours and the requested-versus-got preset.
	t3 := dbStr(t, r, `SELECT song_id FROM song_features ORDER BY song_id LIMIT 1`)
	_, near, _ := getAtlas(t, agent, "?color_by=muse&k=3&near_song_id="+t3)
	if len(near.Neighbors) != 3 || len(near.Legend) == 0 || len(near.Muses) == 0 {
		t.Errorf("neighbours %d, legend %d, muses %d", len(near.Neighbors), len(near.Legend), len(near.Muses))
	}
	_, rg, _ := getAtlas(t, agent, "?x=bpm_requested&y=bpm_measured")
	if len(rg.Guides) != 3 {
		t.Errorf("guides %v", rg.Guides)
	}

	// Stable errors.
	for q, want := range map[string][2]any{
		"?x=loudness":                       {422, "UNKNOWN_AXIS"},
		"?x=tempo&y=tempo":                  {422, "AXIS_PAIR_INVALID"},
		"?color_by=rainbow":                 {422, "UNKNOWN_COLOR_BY"},
		"?cursor=garbage":                   {422, "INVALID_CURSOR"},
		"?near_song_id=nope":                {404, "NOT_FOUND"},
		"?sound=bogus:x":                    {422, "UNKNOWN_FACET"},
		"?limit=601":                        {422, ""},
		"?crate_id=nope":                    {404, ""},
		"?k=26":                             {422, ""},
		"?min_stars=9":                      {422, ""},
		"?x=tempo&y=energy&cursor=" + "e30": {422, "INVALID_CURSOR"},
	} {
		status, _, prob := getAtlas(t, agent, q)
		if status != want[0] || want[1] != "" && prob["code"] != want[1] {
			t.Errorf("%s: %d %v, want %v", q, status, prob, want)
		}
	}
	// A stale page: the cursor of a map that changed answers 409.
	_, first, _ := getAtlas(t, agent, "?limit=3")
	if first.NextCursor == nil {
		t.Fatal("no cursor")
	}
	if _, err := r.Store.W.Exec(`UPDATE song_features SET energy_x10000 = 1, digest = ? WHERE song_id = (SELECT song_id FROM song_features ORDER BY song_id LIMIT 1)`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if status, _, prob := getAtlas(t, agent, "?limit=3&cursor="+url.QueryEscape(*first.NextCursor)); status != 409 || prob["code"] != "ATLAS_STALE" {
		t.Errorf("stale: %d %v", status, prob)
	}
	resp2, err := http.Get(r.URL + "/api/atlas")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != 401 {
		t.Errorf("anonymous: %d", resp2.StatusCode)
	}
}

// TestAtlas_AgentDefaultPage: with no limit a person or device gets 400 points, any other principal 100; the default depends on the
// principal kind, never on the transport; limit 600 is the most; the first page of a larger map carries a cursor.
func TestAtlas_AgentDefaultPage(t *testing.T) {
	t.Parallel()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lt.FixtureL3Scale(t, st, lt.FixedNow, 450, 10)
	rig := l3Rig(t, api.Deps{Atlas: atlas.New(st, func() time.Time { return lt.FixedNow })})
	human := auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeRead}, Via: "test"}
	device := auth.Principal{Kind: auth.KindDevice, ID: "d", Name: "d", Scopes: []auth.Scope{auth.ScopeRead}, Via: "test"}
	for _, c := range []struct {
		name  string
		p     auth.Principal
		query string
		want  int
	}{
		{"agent", l3Agent(auth.ScopeRead), "", 100}, {"human", human, "", 400}, {"device", device, "", 400},
		{"agent explicit", l3Agent(auth.ScopeRead), "?limit=250", 250}, {"human explicit", human, "?limit=450", 450},
	} {
		rec := rig.callWith(c.p, "GET", "/api/atlas"+c.query, "")
		var a atlas.Atlas
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
		if len(a.Points) != c.want || (c.want < 450) != (a.NextCursor != nil) || a.Totals.Plotted != 450 {
			t.Errorf("%s: %d points (want %d), cursor %v, plotted %d", c.name, len(a.Points), c.want, a.NextCursor, a.Totals.Plotted)
		}
	}
}
