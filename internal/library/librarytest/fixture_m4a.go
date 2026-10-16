package librarytest

import (
	"context"
	"encoding/binary"
	"encoding/json"
	mrand "math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Fixed ids and names of the M4a rows (C117.3).
const (
	OfflineSetID     = "00000000-0000-7000-8000-0000000000f1" // the playlist the migration creates
	ConnectorGrantID = "01900000-0000-7000-8000-000000000501"
	SubsonicClientID = "01900000-0000-7000-8000-000000000502"
	BookmarkComment  = "01900000-0000-7000-8000-000000000503"
)

// SecretKey is the secret_key of the servertest rigs (testKey); the Subsonic client's API key is HMAC-hashed with it,
// so the exposed plaintext verifies only in a rig that uses the same key (not in Canary rigs). Built at run time so
// no literal looks like a credential.
var SecretKey = strings.Repeat("0123456789abcdef", 2)

// FixtureM4a is FixtureM3 plus the M4a rows (schema version 9, C117.3): two offline-set items (fixture tracks 1 and
// 2), one connector grant (read,write, limits null; through connectors.Store.Ensure), one Subsonic client created
// through auth.SubsonicStore.Create (API key and password; the plaintexts are returned in Fixtures and never
// written to a file), one bookmark with a comment, a play queue of two tracks, and status_set_at on every track.
func FixtureM4a(tb testing.TB, st *store.Store, now time.Time) Fixtures {
	tb.Helper()
	return addM4a(tb, st, now, FixtureM3(tb, st, now))
}

// ApplyM4a adds the M4a rows to a library that already holds FixtureM3's rows (servertest uses it).
func ApplyM4a(tb testing.TB, st *store.Store, now time.Time) {
	tb.Helper()
	addM4a(tb, st, now, Fixtures{})
}

// chachaReader is a deterministic random source so the fixture's credentials are the same in every build.
type chachaReader struct {
	mu sync.Mutex
	r  *mrand.ChaCha8
}

func (c *chachaReader) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.r.Read(p)
}

func addM4a(tb testing.TB, st *store.Store, now time.Time, fx Fixtures) Fixtures {
	tb.Helper()
	ctx := context.Background()
	stamp := now.UTC().Format(time.RFC3339) // the 20-character form of the pre-M3 columns (C103)

	// 1. the offline set: tracks 1 and 2, written like the playlists service would (revision + 1)
	items, _ := json.Marshal([]string{TrackLuna, TrackCitta})
	mustExec(tb, st, `UPDATE playlists SET items_json = ?, items_count = 2, revision = revision + 1, updated_by = ?, updated_at = ? WHERE role = 'offline'`,
		string(items), humanKey, stamp)
	fx.OfflineSetID = OfflineSetID

	// 2. one connector grant through the one insert path
	gid := ConnectorGrantID
	grant, _, err := connectors.NewStore(st, func() time.Time { return now }, func() string { return gid }).
		Ensure(ctx, "fixture-connector", "fixture-subject-0001", "fixture", "Fixture Connector")
	if err != nil {
		tb.Fatal(err)
	}
	fx.ConnectorGrantID = grant.ID

	// 3. one Subsonic client (API key and password) with deterministic credentials
	var seed [32]byte
	binary.LittleEndian.PutUint64(seed[:], 0x4d34)
	cid := SubsonicClientID
	creds := auth.NewSubsonicStore(st, []byte(SecretKey), func() time.Time { return now }, func() string { return cid }, &chachaReader{r: mrand.NewChaCha8(seed)})
	client, secrets, err := creds.Create(ctx, auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Scopes: []auth.Scope{auth.ScopeAdmin}},
		auth.NewSubsonicClient{Name: "Fixture phone", Username: "fixture-phone", DefaultFormat: "raw", Write: true, APIKey: true, Password: true})
	if err != nil {
		tb.Fatal(err)
	}
	fx.SubsonicClientID, fx.SubsonicUsername = client.ID, client.Username
	fx.SubsonicAPIKey, fx.SubsonicPassword = secrets.APIKey, secrets.Password

	// 4. one bookmark with a comment (D-M4a-12: the comment is a timestamped note of source subsonic), a play queue of 2
	at := store.FormatTime(now.Add(-time.Hour))
	cat := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	mustExec(tb, st, `INSERT INTO comments(id, track_id, body, at_second, kind, state, source, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES (?,?,?,?,'note','open','subsonic','device',?,?,?,?)`, BookmarkComment, TrackLuna, "fixture bookmark note", 30, client.ID, client.Name, cat, cat)
	mustExec(tb, st, `INSERT INTO subsonic_bookmarks(track_id, position_ms, comment, comment_id, created_at, updated_at, updated_by)
		VALUES (?,?,?,?,?,?,?)`, TrackLuna, 30000, "fixture bookmark note", BookmarkComment, at, at, "device:"+client.ID)
	queue, _ := json.Marshal([]string{TrackLuna, TrackCitta})
	mustExec(tb, st, `INSERT INTO subsonic_play_queue(id, track_ids_json, current_track_id, position_ms, changed_at, changed_by)
		VALUES (1, ?, ?, 12000, ?, 'fixture-client')`, string(queue), TrackLuna, at)

	// 5. status_set_at on every track (rows inserted by SQL after the migration's backfill)
	mustExec(tb, st, `UPDATE tracks SET status_set_at = created_at WHERE status_set_at IS NULL`)
	return fx
}
