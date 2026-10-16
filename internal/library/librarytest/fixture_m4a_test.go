package librarytest

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestFixtureM4a_Rows: the M4a rows of C117.3 sit on top of FixtureM3, which keeps its own rows.
func TestFixtureM4a_Rows(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fx := FixtureM4a(t, st, FixedNow)

	// two offline-set items (fixture tracks 1 and 2), revision bumped, hidden never
	var itemsRaw string
	var rev int
	var hidden *string
	if err := st.R.QueryRow(`SELECT items_json, revision, hidden_at FROM playlists WHERE role = 'offline'`).Scan(&itemsRaw, &rev, &hidden); err != nil {
		t.Fatal(err)
	}
	var items []string
	if err := json.Unmarshal([]byte(itemsRaw), &items); err != nil || len(items) != 2 || items[0] != TrackLuna || items[1] != TrackCitta || hidden != nil || rev < 2 {
		t.Errorf("offline set: %v rev %d hidden %v (%v)", items, rev, hidden, err)
	}
	if n := count(t, st, `SELECT items_count FROM playlists WHERE role = 'offline'`); n != 2 {
		t.Errorf("items_count = %d", n)
	}
	// one connector grant (read,write; limits null) through connectors.Store.Ensure
	if n := count(t, st, `SELECT COUNT(*) FROM connector_grants WHERE id = ? AND scopes = 'read,write' AND daily_budget_micro_usd IS NULL AND auto_approve_takes IS NULL AND revoked_at IS NULL`, fx.ConnectorGrantID); n != 1 {
		t.Errorf("connector grant %q rows = %d", fx.ConnectorGrantID, n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM connector_grants`); n != 1 {
		t.Errorf("connector_grants rows = %d", n)
	}
	// one Subsonic client with an API key and a password, whose plaintexts the fixture exposes
	if n := count(t, st, `SELECT COUNT(*) FROM subsonic_clients WHERE id = ? AND api_key_hmac IS NOT NULL AND password_ct IS NOT NULL AND revoked_at IS NULL`, fx.SubsonicClientID); n != 1 {
		t.Errorf("subsonic client rows = %d", n)
	}
	if len(fx.SubsonicAPIKey) != len("mms_")+52 || fx.SubsonicPassword == "" || fx.SubsonicUsername == "" {
		t.Errorf("subsonic secrets not exposed: %d chars key, password %q, user %q", len(fx.SubsonicAPIKey), fx.SubsonicPassword, fx.SubsonicUsername)
	}
	creds := auth.NewSubsonicStore(st, []byte(SecretKey), func() time.Time { return FixedNow }, nil, nil)
	p, err := creds.VerifyAPIKey(context.Background(), fx.SubsonicAPIKey, "fixture-client")
	if err != nil || p.Kind != auth.KindDevice || p.Via != "subsonic" || p.ID != fx.SubsonicClientID {
		t.Errorf("the exposed API key does not verify with SecretKey: %+v %v", p, err)
	}
	// one bookmark with a comment (the comment exists, kind note, source subsonic), a play queue of 2
	if n := count(t, st, `SELECT COUNT(*) FROM subsonic_bookmarks b JOIN comments c ON c.id = b.comment_id WHERE b.comment IS NOT NULL AND b.deleted_at IS NULL AND c.kind = 'note' AND c.source = 'subsonic' AND c.track_id = b.track_id`); n != 1 {
		t.Errorf("bookmark with comment rows = %d", n)
	}
	var queue string
	if err := st.R.QueryRow(`SELECT track_ids_json FROM subsonic_play_queue WHERE id = 1`).Scan(&queue); err != nil {
		t.Fatal(err)
	}
	var qids []string
	if err := json.Unmarshal([]byte(queue), &qids); err != nil || len(qids) != 2 {
		t.Errorf("play queue %q (%v)", queue, err)
	}
	// status_set_at on every track
	if n := count(t, st, `SELECT COUNT(*) FROM tracks WHERE status_set_at IS NULL`); n != 0 {
		t.Errorf("%d tracks without status_set_at", n)
	}
	// FixtureM3's own rows are untouched (the offline-set migration row aside)
	if n := count(t, st, `SELECT COUNT(*) FROM playlists`); n != 2 {
		t.Errorf("playlists = %d, want the fixture mix and the offline set", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM batches`); n == 0 {
		t.Error("the M3 rows are missing")
	}
}

// TestFixtureM4a_ApplyMatches: ApplyM4a on a FixtureM3 library gives the M4a rows of FixtureM4a, and the fixture is
// deterministic (two builds expose the same plaintexts).
func TestFixtureM4a_ApplyMatches(t *testing.T) {
	t.Parallel()
	a, b := openStore(t), openStore(t)
	fxA := FixtureM4a(t, a, FixedNow)
	FixtureM3(t, b, FixedNow)
	ApplyM4a(t, b, FixedNow)
	for _, q := range []string{
		`SELECT items_json FROM playlists WHERE role = 'offline'`,
		`SELECT id || username || api_key_prefix FROM subsonic_clients`,
		`SELECT id || subject FROM connector_grants`,
		`SELECT track_id || position_ms || comment FROM subsonic_bookmarks`,
		`SELECT track_ids_json || position_ms FROM subsonic_play_queue`,
	} {
		var x, y string
		if err := a.R.QueryRow(q).Scan(&x); err != nil {
			t.Fatal(err)
		}
		if err := b.R.QueryRow(q).Scan(&y); err != nil || x != y {
			t.Errorf("%s: %q vs %q (%v)", q, x, y, err)
		}
	}
	c := openStore(t)
	if fxC := FixtureM4a(t, c, FixedNow); fxC.SubsonicAPIKey != fxA.SubsonicAPIKey || fxC.SubsonicPassword != fxA.SubsonicPassword {
		t.Error("the fixture's credentials are not deterministic")
	}
}

// TestFixtureM4a_EarlierFixturesUnchanged: M4a rows never leak into the earlier layers.
func TestFixtureM4a_EarlierFixturesUnchanged(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	Fixture(t, st, FixedNow)
	for _, table := range []string{"connector_grants", "subsonic_clients", "subsonic_bookmarks", "subsonic_play_queue"} {
		if n := count(t, st, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("Fixture holds %d %s rows", n, table)
		}
	}
	if n := count(t, st, `SELECT items_count FROM playlists WHERE role = 'offline'`); n != 0 {
		t.Errorf("Fixture's offline set holds %d items", n)
	}
}
