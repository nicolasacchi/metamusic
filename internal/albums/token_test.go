package albums_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// tokenRig is a FixtureL2 rig whose human client drives the real operations; the token is read from the rig's own catalog.
type tokenRig struct {
	t    *testing.T
	r    *servertest.Rig
	c    *servertest.Client
	last string
}

func newTokenRig(t *testing.T) *tokenRig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	tr := &tokenRig{t: t, r: r, c: r.Human(t)}
	tr.last = tr.token()
	return tr
}

func (tr *tokenRig) token() string {
	tr.t.Helper()
	tok, err := tr.r.Services.Albums.Token(context.Background())
	if err != nil || len(tok) != 16 {
		tr.t.Fatalf("Token = %q, %v", tok, err)
	}
	return tok
}

// moved asserts the token differs from the previous observation (and records the new one).
func (tr *tokenRig) moved(why string) {
	tr.t.Helper()
	got := tr.token()
	if got == tr.last {
		tr.t.Errorf("%s: the token did not change", why)
	}
	tr.last = got
}

// still asserts the token is unchanged.
func (tr *tokenRig) still(why string) {
	tr.t.Helper()
	if got := tr.token(); got != tr.last {
		tr.t.Errorf("%s: the token changed", why)
	}
}

// do advances the clock (so updated_at moves, as real time does) and calls the operation.
func (tr *tokenRig) do(method, path string, body any) *servertest.Response {
	tr.t.Helper()
	tr.r.Clock.Advance(time.Minute)
	return tr.c.Do(tr.t, method, path, body)
}

func (tr *tokenRig) ok(method, path string, body any) {
	tr.t.Helper()
	if r := tr.do(method, path, body); r.Status/100 != 2 {
		tr.t.Fatalf("%s %s: %d %s", method, path, r.Status, r.Body)
	}
}

func (tr *tokenRig) exec(q string, args ...any) {
	tr.t.Helper()
	tr.r.Clock.Advance(time.Minute)
	if _, err := tr.r.Store.W.Exec(q, args...); err != nil {
		tr.t.Fatalf("%s: %v", q, err)
	}
}

func (tr *tokenRig) revision(table, id string) int {
	tr.t.Helper()
	var rev int
	if err := tr.r.Store.R.QueryRow(`SELECT revision FROM `+table+` WHERE id = ?`, id).Scan(&rev); err != nil {
		tr.t.Fatal(err)
	}
	return rev
}

// TestAlbums_TokenStable: the token moves on publish, unpublish, a Pick event, a Song hide, a flag change, a Take title edit,
// a Take discard, a Song title edit and the Muse operations, each through its real operation where one exists today, and
// stays put for a rating of an unrelated Take, a failed edit and plain reads. (Publish and unpublish are written in SQL until
// lane R-a lands the Records service.)
func TestAlbums_TokenStable(t *testing.T) {
	t.Parallel()
	tr := newTokenRig(t)
	info := newWorld(t, nil).Info // fixture ids are deterministic, so a scratch FixtureL2 names the rig's rows

	// reads and a stable second read
	tr.c.OK(t, "GET", "/api/stats", nil)
	tr.still("a read")

	// publish and unpublish the draft R2
	tr.exec(`UPDATE records SET status = 'published', published_by = 'human:fixture', published_at = ?, first_published_at = ? WHERE id = ?`,
		"2026-10-28T09:00:00.000Z", "2026-10-28T09:00:00.000Z", info.Records["R2"])
	tr.moved("publish")
	tr.exec(`UPDATE records SET status = 'draft', published_by = NULL, published_at = NULL WHERE id = ?`, info.Records["R2"])
	tr.moved("unpublish")

	// a Pick event: the operator pins b2 on Song S3
	tr.ok("PUT", "/api/songs/"+info.Songs["S3"]+"/pin", map[string]any{"take_id": info.Takes["b2"]})
	tr.moved("a Pick event")

	// a rating of an unrelated Take changes nothing the catalog shows
	tr.ok("PUT", "/api/tracks/"+info.Takes["s6a"]+"/rating", map[string]any{"stars": 4})
	tr.still("a rating of an unrelated Take")

	// a failed edit does not count
	if r := tr.do("PATCH", "/api/tracks/"+info.Takes["a1"], map[string]any{"status": "bogus"}); r.Status < 400 {
		t.Fatalf("bogus status accepted: %d", r.Status)
	}
	tr.still("a rejected edit")

	// Take title and status edits (update_track), Song edits (update_song), Muse edits (update_muse, assign_muse_songs)
	tr.ok("PATCH", "/api/tracks/"+info.Takes["a1"], map[string]any{"title": "Synthetic Renamed Take"})
	tr.moved("a Take title edit")
	tr.ok("PATCH", "/api/tracks/"+info.Takes["c2"], map[string]any{"status": "discarded"})
	tr.moved("a Take discard")
	tr.ok("PATCH", "/api/songs/"+info.Songs["S3"], map[string]any{"revision": tr.revision("songs", info.Songs["S3"]), "title": "Synthetic Renamed Song"})
	tr.moved("a Song title edit")
	tr.ok("PATCH", "/api/songs/"+info.Songs["S2"], map[string]any{"revision": tr.revision("songs", info.Songs["S2"]), "hidden": true})
	tr.moved("a Song hide")
	tr.ok("PATCH", "/api/muses/"+info.Muses["M1"], map[string]any{"revision": tr.revision("muses", info.Muses["M1"]), "title": "Synthetic Renamed Muse"})
	tr.moved("update_muse")
	tr.ok("POST", "/api/muses/"+info.Muses["M2"]+"/songs", map[string]any{"song_ids": []string{info.Songs["S6"]}})
	tr.moved("assign_muse_songs")

	// the two flags, through the human-only operation; the same value again changes nothing
	tr.ok("PUT", "/human/api/curation/settings", map[string]any{"subsonic_muse_names": true})
	tr.moved("the subsonic_muse_names flag")
	tr.ok("PUT", "/human/api/curation/settings", map[string]any{"export_muse_names": true})
	tr.moved("the export_muse_names flag")
	tr.ok("PUT", "/human/api/curation/settings", map[string]any{"export_muse_names": true})
	tr.still("an unchanged flag")
}

// TestTokenOps_Frozen pins the audited operation list of C126.5.
func TestTokenOps_Frozen(t *testing.T) {
	t.Parallel()
	if got := fmt.Sprint(albums.TokenOps); got != "[update_track update_song update_muse assign_muse_songs]" {
		t.Errorf("TokenOps = %s", got)
	}
}

// TestAlbums_TokenNoDirectoryScan: Token asks a Muse directory for its Version only (never Ordinals or Labels, which fail the
// test), the record kind never calls it at all, and a new Version moves the token.
func TestAlbums_TokenNoDirectoryScan(t *testing.T) {
	t.Parallel()
	spy := &museSpy{t: t}
	spy.version.Store("2|2026-10-28T10:00:00.000Z")
	w := newWorld(t, spy)
	ctx := context.Background()
	a, err := w.Svc.Token(ctx)
	if err != nil || spy.versions.Load() != 1 {
		t.Fatalf("Token = %q, %v after %d Version calls", a, err, spy.versions.Load())
	}
	list(t, w, albums.KindRecord)
	if _, _, err := w.Svc.Get(ctx, "rec-"+w.Info.Records["R1"]); err != nil {
		t.Fatal(err)
	}
	if spy.versions.Load() != 1 {
		t.Error("List and Get must not call the directory for the record kind")
	}
	if b, _ := w.Svc.Token(ctx); b != a {
		t.Error("an unchanged directory must give the same token")
	}
	spy.version.Store("3|2026-10-28T11:00:00.000Z")
	if b, _ := w.Svc.Token(ctx); b == a {
		t.Error("a new directory Version must move the token")
	}
}

// TestAlbums_TokenStatementCount: at most nine statements (C126.6), flags and directory included.
func TestAlbums_TokenStatementCount(t *testing.T) {
	t.Parallel()
	w := newWorld(t, &museSpy{t: t})
	before := w.N.Load()
	if _, err := w.Svc.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.N.Load() - before; got > 9 || got < 1 {
		t.Errorf("Token: %d statements, want 1..9", got)
	}
}

// failingDir and failingSettings return their error from every call.
type failingDir struct{}

func (failingDir) Ordinals(context.Context) (map[string]albums.MuseMeta, error) { return nil, errBoom }
func (failingDir) Labels(context.Context, []string) (map[string]string, error)  { return nil, errBoom }
func (failingDir) Version(context.Context) (string, error)                      { return "", errBoom }

type failingSettings struct{}

func (failingSettings) SubsonicMuseNames(context.Context) (bool, error) { return false, errBoom }

var errBoom = errors.New("boom")

type exportFailing struct{}

func (exportFailing) SubsonicMuseNames(context.Context) (bool, error) { return false, nil }
func (exportFailing) ExportMuseNames(context.Context) (bool, error)   { return false, errBoom }

// TestAlbums_ErrorsSurface: a failing store, directory or settings reader is an error, never a token or a list that looks fine.
func TestAlbums_ErrorsSurface(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t, nil)
	for name, set := range map[string]albums.Settings{"subsonic flag": failingSettings{}, "export flag": exportFailing{}} {
		svc := albums.New(w.St, records.New(w.St, nil, nil), nil, set, nil)
		if _, err := svc.Token(ctx); !errors.Is(err, errBoom) {
			t.Errorf("%s: Token = %v", name, err)
		}
	}
	svc := albums.New(w.St, records.New(w.St, nil, nil), failingDir{}, w.Set, nil)
	if _, err := svc.Token(ctx); !errors.Is(err, errBoom) {
		t.Errorf("directory: Token = %v", err)
	}
	// a nil settings reader is allowed (no flags to hash)
	if tok, err := albums.New(w.St, records.New(w.St, nil, nil), nil, nil, nil).Token(ctx); err != nil || len(tok) != 16 {
		t.Errorf("nil settings: %q %v", tok, err)
	}

	closedW := newWorld(t, nil)
	_ = closedW.St.Close()
	closed := albums.New(closedW.St, records.New(closedW.St, nil, nil), nil, nil, nil)
	if _, err := closed.List(ctx); err == nil {
		t.Error("List on a closed store must fail")
	}
	if _, _, err := closed.Get(ctx, "rec-x"); err == nil {
		t.Error("Get on a closed store must fail")
	}
	if _, err := closed.Token(ctx); err == nil {
		t.Error("Token on a closed store must fail")
	}
}
