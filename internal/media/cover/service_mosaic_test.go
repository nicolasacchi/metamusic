package cover_test

import (
	"context"
	"errors"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

func TestCover_EnsureMosaic_Idempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	in := rec("record-1", 3, 7)
	var wg sync.WaitGroup
	keys := make([]string, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, keys[i], errs[i] = e.svc.EnsureMosaic(context.Background(), in, 64)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || keys[0] != keys[1] || keys[0] != cover.MosaicKey(in, 64) {
		t.Fatalf("keys %v errs %v", keys, errs)
	}
	if n := len(e.covers(t)); n != 1 {
		t.Errorf("%d files, want 1", n)
	}
	obj, key, err := e.svc.EnsureMosaic(context.Background(), in, 70) // clamped to 96
	if err != nil || key != cover.MosaicKey(in, 96) || obj.ContentType != "image/png" || obj.ETag != key {
		t.Errorf("clamped: %+v %s %v", obj, key, err)
	}
	f, err := os.Open(filepath.Join(e.dir, obj.RelPath))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil || img.Bounds().Dx() != 96 {
		t.Errorf("decode: %v %v", img, err)
	}
	st, _ := os.Stat(filepath.Join(e.dir, obj.RelPath))
	if st.Mode().Perm() != 0o444 {
		t.Errorf("mode %v", st.Mode())
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := e.svc.EnsureMosaic(cctx, in, 64); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// A signed /media/<key>.png link resolves to the cached mosaic and serving it writes nothing.
func TestCover_MosaicResolveSigned(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	obj, key, err := e.svc.EnsureMosaic(ctx, rec("record-2", 1, 4), 64)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Resolve(ctx, key, "png")
	if err != nil || got.RelPath != obj.RelPath {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	signer := serve.NewSigner([]byte("synthetic-secret"), e.clk.now)
	link := signer.Sign(key, "png", time.Hour, time.Minute)
	req := newGet(link.URL)
	rr := serveOnce(serve.Handler(signer, e.data, e.svc, nil), req)
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("signed GET: %d %v", rr.Code, rr.Header())
	}
	if n := len(e.covers(t)); n != 1 {
		t.Errorf("serving changed the cache: %d files", n)
	}
	if _, err := e.svc.Resolve(ctx, cover.MosaicKey(rec("record-3", 1, 4), 64), "png"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("unrendered key: %v", err)
	}
}

func TestCover_MosaicSweep(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	in := rec("record-4", 1, 4)
	_, key, err := e.svc.EnsureMosaic(ctx, in, 64)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.add(29 * 24 * time.Hour)
	if n, err := e.svc.Sweep(ctx, 30*24*time.Hour); err != nil || n != 0 {
		t.Fatalf("early sweep: %d %v", n, err)
	}
	e.clk.add(2 * 24 * time.Hour)
	if n, err := e.svc.Sweep(ctx, 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if _, key2, err := e.svc.EnsureMosaic(ctx, in, 64); err != nil || key2 != key {
		t.Errorf("recreate: %s %v", key2, err)
	}
}

func newGet(url string) *http.Request { return httptest.NewRequest(http.MethodGet, url, nil) }

func serveOnce(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}
