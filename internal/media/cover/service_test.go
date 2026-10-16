package cover_test

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	svc  *cover.Service
	data *os.Root
	dir  string
	clk  *clock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.FixtureM2c(t, st, lt.FixedNow)
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	clk := &clock{t: lt.FixedNow}
	lib := library.New(st, root, clk.now)
	return &env{svc: cover.New(root, lib, clk.now), data: root, dir: dir, clk: clk}
}

func (e *env) covers(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(filepath.Join(e.dir, "derived", "covers"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestCover_EnsureTrackWritesPNG(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	obj, key, err := e.svc.EnsureTrack(context.Background(), lt.TrackLuna, 0)
	if err != nil {
		t.Fatal(err)
	}
	if obj.ContentType != "image/png" || obj.RelPath != "derived/covers/"+key[:2]+"/"+key+".png" || obj.DownloadName != "" {
		t.Errorf("object %+v key %s", obj, key)
	}
	raw, err := e.data.ReadFile(obj.RelPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != 512 || cfg.Height != 512 {
		t.Errorf("decode: %+v %v", cfg, err)
	}
	if st, _ := e.data.Stat(obj.RelPath); st.Mode().Perm() != 0o444 {
		t.Errorf("mode %v", st.Mode())
	}
	if entries, _ := os.ReadDir(filepath.Join(e.dir, "incoming")); len(entries) != 0 {
		t.Errorf("incoming not cleaned: %v", entries)
	}
	// the key addresses (root, seed, size): a size request is clamped first
	_, k2, err := e.svc.EnsureTrack(context.Background(), lt.TrackLuna, 300)
	if err != nil || k2 == key {
		t.Errorf("300 px key %s err %v", k2, err)
	}
	if _, _, err := e.svc.EnsureTrack(context.Background(), "no-such-track", 512); err == nil {
		t.Error("unknown track must fail")
	}
}

func TestCover_EnsureIdempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var wg sync.WaitGroup
	keys := make([]string, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, keys[i], errs[i] = e.svc.EnsureTrack(context.Background(), lt.TrackLuna, 64)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || keys[0] != keys[1] || keys[0] == "" {
		t.Fatalf("keys %v errs %v", keys, errs)
	}
	if n := len(e.covers(t)); n != 1 {
		t.Errorf("%d files, want 1", n)
	}
}

func TestCover_ResolveMissingIs404(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	key := cover.Key("r", 1, 512)
	for _, c := range []struct{ key, ext string }{{key, "png"}, {"nothex", "png"}, {strings.Repeat("a", 64), "flac"}} {
		if _, err := e.svc.Resolve(ctx, c.key, c.ext); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("Resolve(%q,%q) = %v", c.key, c.ext, err)
		}
	}
	obj, k, err := e.svc.EnsureTrack(ctx, lt.TrackLuna, 64)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Resolve(ctx, k, "png")
	if err != nil || got.RelPath != obj.RelPath || got.ContentType != "image/png" || got.ETag == "" {
		t.Errorf("resolve existing: %+v %v", got, err)
	}
}

func TestCover_ReferenceTrack(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	a, ka, err := e.svc.EnsureTrack(ctx, lt.TrackRefFlac, 64)
	if err != nil {
		t.Fatal(err)
	}
	b, kb, err := e.svc.EnsureTrack(ctx, lt.TrackRefFlac, 64)
	if err != nil || ka != kb || a.RelPath != b.RelPath {
		t.Errorf("reference track not deterministic: %s %s %v", ka, kb, err)
	}
	if want := cover.Key(lt.TrackRefFlac, 0, 64); ka != want { // a stub recipe has seed 0; the reference track is its own root
		t.Errorf("key %s, want %s", ka, want)
	}
}

func TestCover_EnsureAlbumIsRootTrackCover(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	_, kt, err := e.svc.EnsureTrack(ctx, lt.TrackLuna, 64)
	if err != nil {
		t.Fatal(err)
	}
	_, ka, err := e.svc.EnsureAlbum(ctx, lt.TrackLuna, 64)
	if err != nil || ka != kt {
		t.Errorf("album key %s, track key %s, %v", ka, kt, err)
	}
	if _, _, err := e.svc.EnsureAlbum(ctx, "nope", 64); err == nil {
		t.Error("unknown root must fail")
	}
}

func TestSweep_Covers(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	_, key, err := e.svc.EnsureTrack(ctx, lt.TrackLuna, 64)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.add(29 * 24 * time.Hour)
	if n, err := e.svc.Sweep(ctx, 30*24*time.Hour); err != nil || n != 0 {
		t.Fatalf("early sweep: %d %v", n, err)
	}
	e.clk.add(2 * 24 * time.Hour) // 31 days
	if n, err := e.svc.Sweep(ctx, 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if len(e.covers(t)) != 0 {
		t.Error("file survived the sweep")
	}
	if _, err := e.svc.Resolve(ctx, key, "png"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("swept key resolves: %v", err)
	}
	_, key2, err := e.svc.EnsureTrack(ctx, lt.TrackLuna, 64)
	if err != nil || key2 != key || len(e.covers(t)) != 1 {
		t.Errorf("recreate: key %s (was %s) err %v files %d", key2, key, err, len(e.covers(t)))
	}
	// a sweep with nothing on disk is not an error
	e2 := newEnv(t)
	if n, err := e2.svc.Sweep(ctx, time.Hour); err != nil || n != 0 {
		t.Errorf("empty sweep: %d %v", n, err)
	}
}
