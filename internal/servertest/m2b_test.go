package servertest_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestAlternating(t *testing.T) {
	t.Parallel()
	a, b := servertest.Alternating(), servertest.Alternating()
	for i, want := range []bool{false, true, false, true} {
		if got := a(); got != want {
			t.Errorf("call %d = %v, want %v", i, got, want)
		}
	}
	if b() {
		t.Error("a second coin starts again at false")
	}
}

func TestRig_M2bServicesBuilt(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2b: servertest.M2bOptions{Fixture: true}})
	s := r.Services
	if s.Duels == nil || s.Ears == nil || s.Clips == nil {
		t.Fatalf("services = duels %v ears %v clips %v", s.Duels, s.Ears, s.Clips)
	}
	if s.Deps.Duels != s.Duels || s.Deps.Ears != s.Ears || s.Deps.Clips != s.Clips ||
		s.Server.Duels != s.Duels || s.Server.Ears != s.Ears || s.Server.Clips != s.Clips {
		t.Error("Deps and Server must carry the built services")
	}
	var n int
	if err := r.Store.R.QueryRow(`SELECT count(*) FROM duels`).Scan(&n); err != nil || n != 4 {
		t.Errorf("duels = %d, %v", n, err)
	}
	if err := r.Store.R.QueryRow(`SELECT count(*) FROM renditions WHERE kind = 'spectrogram'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("spectrograms = %d, %v", n, err)
	}
	// the clip service is the real one running on the fakes: rendering needs the fixture master file
	if _, err := os.Stat(filepath.Join(r.DataDir, "derived")); err != nil {
		t.Errorf("derived dir: %v", err)
	}
	h, _ := r.Agent(t)
	if resp := h.Do(t, "GET", "/api/duels/next", nil); resp.Status != 200 {
		t.Errorf("next_duel = %d %s, want 200", resp.Status, resp.Body)
	}
}

func TestRig_M2bWithoutFixtureIsEmpty(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true})
	var n int
	if err := r.Store.R.QueryRow(`SELECT count(*) FROM duels`).Scan(&n); err != nil || n != 0 {
		t.Errorf("duels = %d, %v", n, err)
	}
}

func TestRig_M2bOverrideAndWrap(t *testing.T) {
	t.Parallel()
	spy := cliptest.NewRenderer()
	var wrapped bool
	var seen duels.ClipRenderer
	r := servertest.New(t, servertest.Options{Fixture: true, M2b: servertest.M2bOptions{
		FakeClips: true,
		WrapClips: func(in duels.ClipRenderer) duels.ClipRenderer { wrapped = true; seen = in; return in },
		Override:  func(s *app.Services) { s.Duels = duels.New(duels.Options{Clips: spy}) },
	}})
	if !wrapped || seen == nil {
		t.Errorf("WrapClips not called with the fake renderer: wrapped=%v seen=%v", wrapped, seen)
	}
	if r.Services.Deps.Duels != r.Services.Duels || r.Services.Server.Duels != r.Services.Duels {
		t.Error("Override must reach Deps and Server")
	}
}

// TestWriteFixtureDB_M2b is the second half of `make fixture-db`: with MM_FIXTURE_DB=<path> it adds the
// duels and Ears rows (and the spectrogram JPEG) to the fixture database written by librarytest's
// TestWriteFixtureDB; otherwise it builds the whole fixture in a temp dir.
func TestWriteFixtureDB_M2b(t *testing.T) {
	t.Parallel()
	path := os.Getenv("MM_FIXTURE_DB")
	if path == "" {
		path = filepath.Join(t.TempDir(), "fixture", "metamusic.db")
		lt.WriteFixtureDB(t, path)
	}
	dir := filepath.Dir(path)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	duelstest.Fixture(t, st, dir, lt.Fixtures{}, lt.FixedNow, nil)
	e := earstest.Fixture(t, st, root, lt.Fixtures{}, lt.FixedNow)
	if _, err := os.Stat(filepath.Join(dir, "derived", e.SpectroSHA[:2], e.SpectroSHA+".spectro.jpg")); err != nil {
		t.Fatal(err)
	}
}
