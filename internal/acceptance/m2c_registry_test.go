package acceptance_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestAcceptance_M2c_RegistryDerived: every operation whose shape M2c changed answers with equal JSON
// over REST, MCP (HTTP) and MCP (stdio), for a lossy and a FLAC reference track (D-M2c-13).
func TestAcceptance_M2c_RegistryDerived(t *testing.T) {
	r, _ := refRig(t, servertest.Options{})
	refs := refTracks(t, r)
	lossy, flac := refs["syn trio p1"].ID, refs["syn ref a"].ID
	agent, _ := r.Agent(t, auth.ScopeRead)
	httpSess, stdioSess := r.MCP(t, auth.ScopeRead), r.Stdio(t, auth.ScopeRead)

	type call struct {
		op, path string
		args     map[string]any
	}
	calls := []call{
		{"get_track", "/api/tracks/" + lossy, map[string]any{"id": lossy}},
		{"get_track", "/api/tracks/" + flac, map[string]any{"id": flac}},
		{"get_media_links", "/api/tracks/" + lossy + "/media", map[string]any{"id": lossy}},
		{"get_media_links", "/api/tracks/" + flac + "/media", map[string]any{"id": flac}},
		{"get_track_context", "/api/tracks/" + lossy + "/context", map[string]any{"id": lossy}},
		{"get_track_context", "/api/tracks/" + flac + "/context", map[string]any{"id": flac}},
		{"search_tracks", "/api/tracks?" + url.Values{"lossy": {"true"}, "limit": {"50"}}.Encode(), map[string]any{"lossy": true, "limit": 50}},
		{"search_tracks", "/api/tracks?" + url.Values{"recipe_known": {"false"}, "limit": {"50"}}.Encode(), map[string]any{"recipe_known": false, "limit": 50}},
	}
	for _, c := range calls {
		rest := agent.OK(t, "GET", c.path, nil)
		for _, f := range []struct {
			name string
			call func() any
		}{
			{"mcp", func() any { return callTool(t, httpSess, c.op, c.args, false) }},
			{"stdio", func() any { return callTool(t, stdioSess, c.op, c.args, false) }},
		} {
			if diff := cmp.Diff(rest, f.call()); diff != "" {
				t.Errorf("%s %v: REST and %s differ (-rest +%s):\n%s", c.op, c.args, f.name, f.name, diff)
			}
		}
	}
}

// TestGETNeverWritesDB_M2c: the changed GETs leave the database as it was, for the operator, an agent
// and a paired device.
func TestGETNeverWritesDB_M2c(t *testing.T) {
	r, _ := refRig(t, servertest.Options{})
	refs := refTracks(t, r)
	lossy, flac := refs["syn trio p1"].ID, refs["syn ref a"].ID
	human, dev := r.Human(t), r.Device(t)
	agent, _ := r.Agent(t, auth.ScopeRead)
	conn, err := r.Store.R.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	dataVersion := func() int64 {
		var v int64
		if err := conn.QueryRowContext(context.Background(), `PRAGMA data_version`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	gets := []string{
		"/api/tracks?lossy=true", "/api/tracks?lossy=false", "/api/tracks?recipe_known=false", "/api/tracks?recipe_known=true&limit=5",
		"/api/tracks/" + lossy, "/api/tracks/" + flac, "/api/tracks/" + lossy + "/media", "/api/tracks/" + flac + "/media",
		"/api/tracks/" + lossy + "/context", "/api/tracks/" + flac + "/context", "/api/tracks/" + lossy + "/analysis",
		"/api/compare?a=" + lossy + "&b=" + flac, "/api/ladder",
	}
	run := func(c *servertest.Client) {
		t.Helper()
		for _, p := range gets {
			if resp := c.Do(t, "GET", p, nil); resp.Status != 200 {
				t.Fatalf("GET %s: %d %s", p, resp.Status, resp.Body)
			}
		}
	}
	before, version := tableChecksums(t, r.Store.R, nil, nil), dataVersion()
	run(human)
	if got := dataVersion(); got != version {
		t.Errorf("PRAGMA data_version changed (%d -> %d) during the operator's GETs", version, got)
	}
	if diff := diffSums(before, tableChecksums(t, r.Store.R, nil, nil)); diff != "" {
		t.Errorf("the operator's GETs changed %s", diff)
	}
	skip := map[string]bool{"audit_log": true}
	drop := map[string][]string{"api_tokens": {"last_used_at"}, "devices": {"last_seen_at", "expires_at"}}
	before = tableChecksums(t, r.Store.R, skip, drop)
	run(agent)
	run(dev)
	if diff := diffSums(before, tableChecksums(t, r.Store.R, skip, drop)); diff != "" {
		t.Errorf("the agent's and the device's GETs changed %s", diff)
	}
}

// TestNoSecretLeak_M2c: the new fields and the reference/lossy flows expose no canary: responses of
// every face, the stored tables and the backup of a library holding recipe-less tracks.
func TestNoSecretLeak_M2c(t *testing.T) {
	r, _ := refRig(t, servertest.Options{Canary: true})
	l := newLeaks(t)
	l.add("secret_key", keyForms(canaryRigKey)...)
	l.add("edge_secret", canaryEdge)
	l.add("ntfy_token", canaryNtfy)
	l.add("operator_password", canaryPassword)
	human := r.Human(t)
	tok, _, err := r.Services.Tokens.Create(context.Background(), "m2c-leak", []auth.Scope{auth.ScopeRead, auth.ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	agent := servertest.Bearer(r, tok)
	l.add("pat", tok)
	dev, cookie := r.DeviceWithCookie(t, "M2c phone")
	l.add("device-cookie", cookie)
	refs := refTracks(t, r)
	lossy, flac := refs["syn trio p2"].ID, refs["syn ref b"].ID

	paths := []string{
		"/api/tracks?lossy=true", "/api/tracks?recipe_known=false", "/api/tracks/" + lossy, "/api/tracks/" + flac,
		"/api/tracks/" + lossy + "/media", "/api/tracks/" + flac + "/media", "/api/tracks/" + lossy + "/context", "/api/tracks/" + flac + "/context",
		"/api/tracks/" + lossy + "/analysis", "/api/ladder", "/api/openapi.json",
	}
	for _, c := range []*servertest.Client{human, agent, dev} {
		for _, p := range paths {
			c.Do(t, "GET", p, nil)
		}
	}
	agent.Do(t, "POST", "/api/drafts", map[string]any{"parent_track_id": lossy, "request": map[string]any{"id": "leak_d", "style": "pop", "lyrics": "[Verse]\nla", "cot": "full", "seed": 3}}, "Idempotency-Key", "m2c-leak-d")
	agent.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{lossy, flac}}}, "Idempotency-Key", "m2c-leak-x")
	sess := mcpWithToken(t, r, tok)
	for _, p := range []string{lossy, flac} {
		callTool(t, sess, "get_media_links", map[string]any{"id": p}, false)
		callTool(t, sess, "get_track_context", map[string]any{"id": p}, false)
	}

	harvest(t, l, []leakClient{{name: "human", c: human}, {name: "agent", c: agent}, {name: "device", c: dev, cookie: cookie}}, nil)
	waitBackupsIdle(t, agent)
	tables := dumpTables(t, r.Store.R, append([]string{"tracks", "recipes", "renditions", "analyses", "mutations"}, leakTables...)...)
	for name, text := range tables {
		l.scan("table "+name, text)
	}
	scanDir(t, l, "rig backups", r.BackupDir)
	l.flush()
	if l.scanned < 10 || !strings.Contains(tables["recipes"], "unknown") {
		t.Errorf("scanned %d texts; recipes dump holds stubs: %v", l.scanned, strings.Contains(tables["recipes"], "unknown"))
	}
}
