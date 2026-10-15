package servertest_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestRig_M4aRows: Options.M4a adds the M4a fixture rows on top of Fixture, M2c and M3, and the services see them.
func TestRig_M4aRows(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2c: true, M3: true, M4a: true})
	count := func(q string) int {
		var n int
		if err := r.Store.R.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT items_count FROM playlists WHERE role = 'offline'`); n != 2 {
		t.Errorf("offline set items = %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM connector_grants`); n != 1 {
		t.Errorf("grants = %d", n)
	}
	g, err := r.Services.Connectors.Get(context.Background(), lt.ConnectorGrantID)
	if err != nil || g.Label != "Fixture Connector" {
		t.Errorf("Connectors.Get: %+v %v", g, err)
	}
	// the fixture's Subsonic key verifies in the rig (same secret key)
	if clients, err := r.Services.Subsonic.List(context.Background()); err != nil || len(clients) != 1 {
		t.Errorf("subsonic clients: %v %v", clients, err)
	}
	// and a human sees the offline set through the API
	h := r.Human(t)
	if resp := h.Do(t, "GET", "/api/playlists/"+lt.OfflineSetID, nil); resp.Status != http.StatusOK || !strings.Contains(string(resp.Body), `"role":"offline"`) {
		t.Errorf("get_playlist offline set: %d %s", resp.Status, resp.Body)
	}
}

func TestRig_M4aWithoutM4aHasNoRows(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true})
	var n int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM connector_grants`).Scan(&n); err != nil || n != 0 {
		t.Errorf("grants = %d (%v)", n, err)
	}
}

// TestRig_UnlimitedLimitsListEveryClass: the unlimited configuration names all eight classes, the three Subsonic
// ones included (Allow skips a class missing from its config, so the list is what keeps the intent explicit).
func TestRig_UnlimitedLimitsListEveryClass(t *testing.T) {
	t.Parallel()
	got := servertest.UnlimitedLimits()
	for _, c := range []ratelimit.Class{ratelimit.Agent, ratelimit.AgentHeavy, ratelimit.Human, ratelimit.HumanHeavy, ratelimit.Pair,
		ratelimit.Subsonic, ratelimit.SubsonicAuthFail, ratelimit.SubsonicAuthFailGlobal} {
		l, ok := got[c]
		if !ok || l.PerMinute < 1e6 || l.Burst < 1e6 {
			t.Errorf("class %s = %+v (listed %v), want effectively unlimited", c, l, ok)
		}
	}
	if len(got) != 8 {
		t.Errorf("%d classes listed, want 8", len(got))
	}
}

// TestRig_OIDCLoopbackPublicURL: an OIDC rig serves on loopback and publishes exactly that origin (C117.1), so the
// go-sdk client and the probe dial <PublicURL>/mcp. Skips (by name) until M4a-C1 provides the stub provider.
func TestRig_OIDCLoopbackPublicURL(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, OIDC: true})
	if r.OIDC == nil || !strings.HasPrefix(r.URL, "http://127.0.0.1:") || !strings.HasPrefix(r.OIDC.URL, "http://127.0.0.1:") {
		t.Fatalf("urls: rig %q provider %v", r.URL, r.OIDC)
	}
	resp, err := http.Get(r.URL + "/.well-known/oauth-protected-resource/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf strings.Builder
	b := make([]byte, 4096)
	n, _ := resp.Body.Read(b)
	buf.Write(b[:n])
	if resp.StatusCode != 200 || !strings.Contains(buf.String(), `"resource":"`+r.URL+`/mcp"`) || !strings.Contains(buf.String(), r.OIDC.URL) {
		t.Errorf("metadata: %d %s", resp.StatusCode, buf.String())
	}
}
