package library_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func e2Library(t *testing.T) (*library.Service, *store.Store, lt.E2Info) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	info := lt.FixtureE2(t, st, lt.FixedNow)
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return library.New(st, root, func() time.Time { return lt.FixedNow }), st, info
}

func TestStemArtifactName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		n    int
		want string
	}{{"drums", 1, "stem-drums.flac"}, {"drums", 0, "stem-drums.flac"}, {"backing_vocals", 2, "stem-backing_vocals-2.flac"}} {
		if got := library.StemArtifactName(c.name, c.n); got != c.want {
			t.Errorf("%s/%d: %s, want %s", c.name, c.n, got, c.want)
		}
		if n, ok := library.StemName(library.StemArtifactName(c.name, c.n)); !ok || n != c.name {
			t.Errorf("%s/%d does not round-trip: %q %v", c.name, c.n, n, ok)
		}
	}
}

// TestNextStemArtifact_Numbering: the next free name of a stem on a take; the E2 fixture's AP take already holds stem-vocals.flac.
func TestNextStemArtifact_Numbering(t *testing.T) {
	t.Parallel()
	svc, st, info := e2Library(t)
	ctx := t.Context()
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	next := func(track, name string) string {
		t.Helper()
		a, _, err := library.NextStemArtifact(ctx, tx, track, name)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if got := next(info.Port, "vocals"); got != "stem-vocals-2.flac" {
		t.Errorf("second vocals = %s", got)
	}
	if got := next(info.Port, "drums"); got != "stem-drums.flac" {
		t.Errorf("first drums = %s", got)
	}
	if got := next(info.Root, "vocals"); got != "stem-vocals.flac" {
		t.Errorf("another take starts at 1: %s", got)
	}
	// attaching moves the counter; a name that merely starts with another stem's name does not count
	for _, name := range []string{"stem-vocals-2.flac", "stem-vocals-3.flac", "stem-vocals_extra.flac"} {
		if err := svc.AttachArtifact(ctx, tx, info.Port, library.NewArtifact{Name: name, SHA256: "ab" + name, Bytes: 1, Path: "takes/x/" + name}); err != nil {
			t.Fatal(err)
		}
	}
	if got := next(info.Port, "vocals"); got != "stem-vocals-4.flac" {
		t.Errorf("after 2 and 3: %s", got)
	}
	if got := next(info.Port, "vocals_extra"); got != "stem-vocals_extra-2.flac" {
		t.Errorf("a longer name is its own stem: %s", got)
	}
	// the primary key refuses a name twice, an unknown take is refused
	if err := svc.AttachArtifact(ctx, tx, info.Port, library.NewArtifact{Name: "stem-vocals-2.flac", SHA256: "x", Bytes: 1, Path: "p"}); err == nil {
		t.Error("the same artifact name twice was accepted")
	}
	if err := svc.AttachArtifact(ctx, tx, "no-such-take", library.NewArtifact{Name: "stem-bass.flac", SHA256: "x", Bytes: 1, Path: "p"}); !errors.Is(err, library.ErrNoSuchTrack) {
		t.Errorf("unknown take: %v", err)
	}
}

// TestResolve_StemAndLRC: a stem artifact is served by its digest as flac, lyrics.lrc as lrc (text/plain), and neither under the
// other's extension; a master still wins its own digest.
func TestResolve_StemAndLRC(t *testing.T) {
	t.Parallel()
	svc, st, info := e2Library(t)
	ctx := t.Context()
	var stemSHA, stemPath, lrcSHA string
	if err := st.R.QueryRow(`SELECT sha256, path FROM artifacts WHERE track_id = ? AND name = 'stem-vocals.flac'`, info.Port).Scan(&stemSHA, &stemPath); err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRow(`SELECT sha256 FROM artifacts WHERE track_id = ? AND name = 'lyrics.lrc'`, info.Port).Scan(&lrcSHA); err != nil {
		t.Fatal(err)
	}
	o, err := svc.Resolve(ctx, stemSHA, "flac")
	if err != nil || o.ContentType != "audio/flac" || o.RelPath != stemPath || o.ETag != stemSHA {
		t.Errorf("stem flac: %+v %v", o, err)
	}
	o, err = svc.Resolve(ctx, lrcSHA, "lrc")
	if err != nil || o.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("lrc: %+v %v", o, err)
	}
	if _, err := svc.Resolve(ctx, lrcSHA, "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("an lrc is not a flac: %v", err)
	}
	if _, err := svc.Resolve(ctx, stemSHA, "lrc"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("a stem is not an lrc: %v", err)
	}
	var masterSHA, masterPath string
	if err := st.R.QueryRow(`SELECT master_sha256, master_path FROM tracks WHERE id = ?`, info.Port).Scan(&masterSHA, &masterPath); err != nil {
		t.Fatal(err)
	}
	if o, err := svc.Resolve(ctx, masterSHA, "flac"); err != nil || o.RelPath != masterPath {
		t.Errorf("master: %+v %v", o, err)
	}
	// LRC and Stems read the same rows
	if l, err := svc.LRC(ctx, info.Port); err != nil || l == nil || l.SHA256 != lrcSHA {
		t.Errorf("LRC = %+v %v", l, err)
	}
	if l, err := svc.LRC(ctx, info.Root); err != nil || l != nil {
		t.Errorf("a take without lrc: %+v %v", l, err)
	}
}

func TestLatestStemArtifact(t *testing.T) {
	t.Parallel()
	_, st, info := e2Library(t)
	ctx := context.Background()
	add := func(track, name, sha string) {
		t.Helper()
		if _, err := st.W.Exec(`INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?,?,?,?,?,?)`, track, name, sha, 7, "p/"+name, "2026-11-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	port, other := info.Port, info.Root
	add(port, "stem-drums.flac", "d1")
	add(port, "stem-drums-2.flac", "d2")
	add(port, "stem-drums-10.flac", "d10")
	add(port, "stem-backing_vocals.flac", "bv")
	add(port, "stem-vocals-x.flac", "junk") // not a numbered name of vocals
	add(other, "stem-bass.flac", "otherbass")
	for _, c := range []struct {
		track, name string
		sha         string
		ok          bool
	}{
		{port, "drums", "d10", true},               // numeric, not lexical, order
		{port, "vocals", "stem-vocals.flac", true}, // the fixture's own stem
		{port, "bass", "", false},                  // none
		{port, "[a-z]*", "", false},                // GLOB metacharacters are inert
		{port, "backing_vocals", "bv", true},
		{other, "bass", "otherbass", true},
		{info.Root, "drums", "", false}, // another take's stems are not visible
	} {
		a, ok, err := library.LatestStemArtifact(ctx, st.R, c.track, c.name)
		if err != nil {
			t.Fatal(err)
		}
		if ok != c.ok {
			t.Errorf("%s/%s: ok = %v, want %v", c.track, c.name, ok, c.ok)
			continue
		}
		if ok && c.sha != "" && c.sha != "stem-vocals.flac" && a.SHA256 != c.sha {
			t.Errorf("%s/%s: sha = %s, want %s", c.track, c.name, a.SHA256, c.sha)
		}
		if ok && c.sha == "stem-vocals.flac" && a.Name != c.sha {
			t.Errorf("name = %s", a.Name)
		}
	}
}
