package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const mediaCacheControl = "private, max-age=31536000, immutable"

func objRoot(t *testing.T, files map[string][]byte) *os.Root {
	t.Helper()
	dir := t.TempDir()
	for name, b := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// TestServeObject_SameAsMediaHandler: ServeObject and the /media/ handler produce byte-identical headers and
// bodies for the same object across the Range matrix (full, first byte, open range, suffix, unsatisfiable, HEAD,
// download name, conditional).
func TestServeObject_SameAsMediaHandler(t *testing.T) {
	t.Parallel()
	body := objBytes()
	root := objRoot(t, map[string][]byte{"audio/a.flac": body})
	sha := strings.Repeat("cd", 32)
	mod := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, dl := range []string{"", "take__1001.flac"} {
		obj := Object{RelPath: "audio/a.flac", ContentType: "audio/flac", ETag: sha, ModTime: mod, DownloadName: dl}
		s := NewSigner(testKey, fixedClock(handlerNow))
		h := Handler(s, root, stubResolver{sha + ".flac": obj}, nil)
		target := s.Sign(sha, "flac", time.Hour, time.Minute).URL
		cases := []struct {
			name, method string
			hdr          map[string]string
		}{
			{"full", "GET", nil},
			{"head", "HEAD", nil},
			{"first byte", "GET", map[string]string{"Range": "bytes=0-0"}},
			{"open", "GET", map[string]string{"Range": "bytes=5000-"}},
			{"suffix", "GET", map[string]string{"Range": "bytes=-100"}},
			{"unsatisfiable", "GET", map[string]string{"Range": "bytes=99999-"}},
			{"if-none-match", "GET", map[string]string{"If-None-Match": `"` + sha + `"`}},
		}
		for _, c := range cases {
			req1 := httptest.NewRequest(c.method, target, nil)
			req2 := httptest.NewRequest(c.method, "/anything", nil)
			for k, v := range c.hdr {
				req1.Header.Set(k, v)
				req2.Header.Set(k, v)
			}
			w1, w2 := httptest.NewRecorder(), httptest.NewRecorder()
			h.ServeHTTP(w1, req1)
			ServeObject(w2, req2, root, obj, mediaCacheControl)
			if w1.Code != w2.Code {
				t.Errorf("%s/%q: status %d vs %d", c.name, dl, w1.Code, w2.Code)
			}
			if d := cmp.Diff(w1.Header(), w2.Header()); d != "" {
				t.Errorf("%s/%q: headers differ (-handler +serveobject):\n%s", c.name, dl, d)
			}
			if w1.Body.String() != w2.Body.String() {
				t.Errorf("%s/%q: bodies differ", c.name, dl)
			}
		}
	}
}

func TestServeObject_Headers(t *testing.T) {
	t.Parallel()
	root := objRoot(t, map[string][]byte{"x/p.png": []byte("\x89PNG....")})
	obj := Object{RelPath: "x/p.png", ContentType: "image/png", ETag: strings.Repeat("11", 32), ModTime: handlerNow}
	w := httptest.NewRecorder()
	ServeObject(w, httptest.NewRequest("GET", "/x", nil), root, obj, "private, max-age=60")
	want := map[string]string{
		"Content-Type": "image/png", "Accept-Ranges": "bytes", "Etag": `"` + obj.ETag + `"`, "Cache-Control": "private, max-age=60",
		"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cross-Origin-Resource-Policy": "same-origin",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if cd := w.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("Content-Disposition %q without a download name", cd)
	}
}

func TestServeObject_MissingFileIs500(t *testing.T) {
	t.Parallel()
	root := objRoot(t, map[string][]byte{"x/a": []byte("a")})
	w := httptest.NewRecorder()
	ServeObject(w, httptest.NewRequest("GET", "/x", nil), root, Object{RelPath: "x/gone", ContentType: "audio/flac", ETag: "e"}, "private")
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"INTERNAL"`) {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	ServeObject(w, httptest.NewRequest("GET", "/x", nil), nil, Object{RelPath: "x/a"}, "private")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("nil root: %d", w.Code)
	}
	w = httptest.NewRecorder()
	ServeObject(w, httptest.NewRequest("GET", "/x", nil), root, Object{RelPath: "x"}, "private")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("directory: %d", w.Code)
	}
}

// TestMediaPath_PNG: the path regexp accepts png (C110.3) and nothing else new.
func TestMediaPath_PNG(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("0f", 32)
	for ext, want := range map[string]bool{"png": true, "flac": true, "jpg": true, "gif": false, "PNG": false, "pngx": false} {
		if got := mediaPath.MatchString("/media/" + sha + "." + ext); got != want {
			t.Errorf("ext %s: match=%v, want %v", ext, got, want)
		}
	}
	// A signed png link is served with image/png.
	root := objRoot(t, map[string][]byte{"derived/covers/0f/c.png": []byte("png-bytes")})
	s := NewSigner(testKey, fixedClock(handlerNow))
	h := Handler(s, root, stubResolver{sha + ".png": {RelPath: "derived/covers/0f/c.png", ContentType: "image/png", ETag: sha, ModTime: handlerNow}}, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", s.Sign(sha, "png", time.Hour, time.Minute).URL, nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Body.String() != "png-bytes" {
		t.Fatalf("got %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
}

// TestResolverChainOrder: library, exports, clips, covers, downloads — first hit wins and a hit never reaches a later resolver.
func TestResolverChainOrder(t *testing.T) {
	t.Parallel()
	var order []string
	mk := func(name string, hit bool) Resolver {
		return resolverFunc(func() (Object, error) {
			order = append(order, name)
			if hit {
				return Object{RelPath: name}, nil
			}
			return Object{}, ErrNotFound
		})
	}
	got, err := Chain(mk("library", false), mk("exports", false), mk("clips", false), mk("covers", false), mk("downloads", true)).Resolve(t.Context(), "s", "flac")
	if err != nil || got.RelPath != "downloads" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if d := cmp.Diff([]string{"library", "exports", "clips", "covers", "downloads"}, order); d != "" {
		t.Fatal(d)
	}
	order = nil
	got, err = Chain(mk("library", true), mk("exports", true), mk("clips", true), mk("covers", true), mk("downloads", true)).Resolve(t.Context(), "s", "flac")
	if err != nil || got.RelPath != "library" || len(order) != 1 {
		t.Fatalf("library must answer first: %+v %v %v", got, err, order)
	}
}

type resolverFunc func() (Object, error)

func (f resolverFunc) Resolve(_ context.Context, _, _ string) (Object, error) { return f() }
